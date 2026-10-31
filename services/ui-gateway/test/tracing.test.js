'use strict';

const test = require('node:test');
const assert = require('node:assert');
const http = require('node:http');
const { createApp, loadConfig } = require('../server');
const { createTracer, parseTraceparent, formatTraceparent } = require('../tracing');

function listen(server) {
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server.address().port)));
}

test('parseTraceparent accepts valid W3C headers only', () => {
  const valid = '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01';
  assert.deepStrictEqual(parseTraceparent(valid), { traceId: '4bf92f3577b34da6a3ce929d0e0e4736', spanId: '00f067aa0ba902b7', sampled: true });
  assert.strictEqual(formatTraceparent(parseTraceparent(valid)), valid);
  assert.strictEqual(parseTraceparent(valid.replace(/-01$/, '-00')).sampled, false);
  for (const bad of [undefined, '', 'garbage', valid.replace(/^00/, 'ff'), valid.toUpperCase(), `${valid}-extra`,
    '00-00000000000000000000000000000000-00f067aa0ba902b7-01', '00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01']) {
    assert.strictEqual(parseTraceparent(bad), null, String(bad));
  }
});

test('tracer is a no-op without an OTLP endpoint', () => {
  const tracer = createTracer({});
  assert.strictEqual(tracer.enabled, false);
  assert.strictEqual(tracer.startSpan('x', {}), null);
});

test('exports are serialized and shutdown waits for queued batches', async () => {
  const pending = [];
  let calls = 0;
  let active = 0;
  let maxActive = 0;
  const tracer = createTracer({ OTEL_EXPORTER_OTLP_ENDPOINT: 'http://collector' }, {
    log: () => {},
    fetchImpl: () => {
      calls += 1;
      active += 1;
      maxActive = Math.max(maxActive, active);
      return new Promise((resolve) => pending.push(() => {
        active -= 1;
        resolve({ ok: true });
      }));
    },
  });
  const addBatch = () => {
    for (let i = 0; i < 256; i += 1) {
      tracer.startSpan('test', { kind: 1 }).end();
    }
  };
  addBatch();
  addBatch();
  assert.strictEqual(calls, 1);
  let shutdownDone = false;
  const shutdown = tracer.shutdown().then(() => { shutdownDone = true; });
  pending[0]();
  await new Promise((resolve) => setImmediate(resolve));
  assert.strictEqual(calls, 2);
  assert.strictEqual(maxActive, 1);
  assert.strictEqual(shutdownDone, false);
  pending[1]();
  await shutdown;
  assert.strictEqual(shutdownDone, true);
});

test('gateway propagates traceparent and exports spans to the collector', async (t) => {
  const exported = [];
  const collector = http.createServer((req, res) => {
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
      if (req.url === '/v1/traces') exported.push(JSON.parse(body));
      res.end('{}');
    });
  });
  const upstream = http.createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ traceparent: req.headers.traceparent }));
  });
  const colPort = await listen(collector);
  const upPort = await listen(upstream);
  const tracer = createTracer({ OTEL_EXPORTER_OTLP_ENDPOINT: `http://127.0.0.1:${colPort}/`, POD_NAME: 'ui-gateway-abc' }, { log: () => {} });
  const cfg = loadConfig({ INSPECTOR_URL: `http://127.0.0.1:${upPort}`, NOTES_URL: 'http://127.0.0.1:1' });
  const gateway = createApp(cfg, tracer);
  const base = `http://127.0.0.1:${await listen(gateway)}`;
  t.after(() => { collector.close(); upstream.close(); gateway.close(); });

  const res = await fetch(`${base}/api/inspector/whoami`);
  const traceId = res.headers.get('x-trace-id');
  const { traceparent } = await res.json();
  assert.match(traceId, /^[0-9a-f]{32}$/);
  const upstreamCtx = parseTraceparent(traceparent);
  assert.strictEqual(upstreamCtx.traceId, traceId);
  assert.strictEqual(upstreamCtx.sampled, true);

  const poll = await fetch(`${base}/api/inspector/whoami`, { headers: { 'X-KubePlay-Poll': 'fleet' } });
  assert.strictEqual(poll.headers.get('x-trace-id'), null);
  assert.strictEqual(parseTraceparent((await poll.json()).traceparent).sampled, false);

  const down = await fetch(`${base}/api/notes/notes`);
  assert.strictEqual(down.status, 502);
  const failedTraceId = down.headers.get('x-trace-id');
  assert.match(failedTraceId, /^[0-9a-f]{32}$/);
  await down.text();

  await new Promise((r) => setTimeout(r, 50));
  await tracer.shutdown();
  const rs = exported.flatMap((b) => b.resourceSpans);
  assert.ok(rs[0].resource.attributes.some((a) => a.key === 'k8s.pod.name' && a.value.stringValue === 'ui-gateway-abc'));
  const spans = rs.flatMap((r) => r.scopeSpans.flatMap((s) => s.spans));
  assert.strictEqual(spans.length, 4, 'server + client span for the sampled call and the failed call; none for polling');

  const server = spans.find((s) => s.kind === 2 && s.traceId === traceId);
  const client = spans.find((s) => s.kind === 3 && s.traceId === traceId);
  assert.strictEqual(server.name, 'GET /api/inspector/*');
  assert.strictEqual(client.parentSpanId, server.spanId);
  assert.strictEqual(client.spanId, upstreamCtx.spanId);
  assert.ok(server.attributes.some((a) => a.key === 'http.response.status_code' && a.value.intValue === '200'));

  const failed = spans.filter((s) => s.traceId !== traceId);
  assert.strictEqual(failed.length, 2);
  assert.ok(failed.every((s) => s.status.code === 2));
  assert.ok(failed.every((s) => s.traceId === failedTraceId));
});
