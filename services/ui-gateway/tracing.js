'use strict';

// Minimal OpenTelemetry tracing with Node built-ins only:
// W3C Trace Context (traceparent) propagation + OTLP/HTTP JSON export to a collector,
// which forwards spans to Azure Monitor Application Insights.

const crypto = require('node:crypto');

const SPAN_KIND = { SERVER: 2, CLIENT: 3 };
const STATUS_ERROR = 2;
const MAX_QUEUED_SPANS = 2048;
const MAX_BATCH_SPANS = 256;
const FLUSH_MS = 5000;
const TRACEPARENT_RE = /^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})(-.*)?$/;

const randomHex = (bytes) => crypto.randomBytes(bytes).toString('hex');
const isZero = (hex) => /^0+$/.test(hex);

function parseTraceparent(header) {
  const m = TRACEPARENT_RE.exec(String(header || '').trim());
  if (!m || m[1] === 'ff' || (m[1] === '00' && m[5]) || isZero(m[2]) || isZero(m[3])) return null;
  return { traceId: m[2], spanId: m[3], sampled: (parseInt(m[4], 16) & 1) === 1 };
}

const formatTraceparent = (ctx) => `00-${ctx.traceId}-${ctx.spanId}-${ctx.sampled ? '01' : '00'}`;

function attr(key, value) {
  return Number.isInteger(value)
    ? { key, value: { intValue: String(value) } }
    : { key, value: { stringValue: String(value) } };
}

const nowNanos = () => (BigInt(Date.now()) * 1_000_000n).toString();

// Returns a tracer; when no OTLP endpoint is configured every method is a no-op (enabled === false).
function createTracer(env = process.env, { fetchImpl = globalThis.fetch, log = console.log } = {}) {
  let url = env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT;
  if (!url && env.OTEL_EXPORTER_OTLP_ENDPOINT) url = `${env.OTEL_EXPORTER_OTLP_ENDPOINT.replace(/\/+$/, '')}/v1/traces`;
  if (!url) {
    return { enabled: false, startSpan: () => null, shutdown: async () => {} };
  }

  const pod = env.POD_NAME || randomHex(8);
  const resource = [
    attr('service.name', env.OTEL_SERVICE_NAME || 'ui-gateway'),
    attr('service.version', env.APP_VERSION || 'v1'),
    // Becomes cloud_RoleInstance in Application Insights.
    attr('service.instance.id', pod),
    attr('k8s.pod.name', pod),
    attr('k8s.namespace.name', env.POD_NAMESPACE || 'unknown'),
    attr('k8s.node.name', env.NODE_NAME || 'unknown'),
    attr('telemetry.sdk.language', 'nodejs'),
  ];
  let queue = [];
  let failing = false;

  async function flush() {
    if (queue.length === 0) return;
    const spans = queue.splice(0, queue.length);
    const body = JSON.stringify({
      resourceSpans: [{ resource: { attributes: resource }, scopeSpans: [{ scope: { name: 'kubeplay/ui-gateway' }, spans }] }],
    });
    let error = null;
    try {
      const res = await fetchImpl(url, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body, signal: AbortSignal.timeout(5000),
      });
      if (!res.ok) error = `collector returned HTTP ${res.status}`;
    } catch (err) {
      error = err.message;
    }
    // Log only on state changes so an unreachable collector doesn't flood the logs.
    if (error && !failing) log(JSON.stringify({ msg: 'trace export failed, dropping spans', error, spans: spans.length }));
    if (!error && failing) log(JSON.stringify({ msg: 'trace export recovered' }));
    failing = Boolean(error);
  }

  const timer = setInterval(flush, FLUSH_MS);
  timer.unref();
  log(JSON.stringify({ msg: 'tracing enabled', otlpEndpoint: url }));

  // parent: span context from an incoming traceparent (or null). sampled=false still propagates the trace id.
  function startSpan(name, { kind, parent = null, sampled = true, attributes = {} }) {
    const ctx = {
      traceId: parent ? parent.traceId : randomHex(16),
      spanId: randomHex(8),
      sampled: parent ? parent.sampled : sampled,
    };
    const start = nowNanos();
    let ended = false;
    return {
      ctx,
      traceparent: formatTraceparent(ctx),
      setAttribute(key, value) { attributes[key] = value; },
      end({ error = false } = {}) {
        if (ended || !ctx.sampled) return;
        ended = true;
        if (queue.length >= MAX_QUEUED_SPANS) return;
        queue.push({
          traceId: ctx.traceId,
          spanId: ctx.spanId,
          ...(parent ? { parentSpanId: parent.spanId } : {}),
          name,
          kind,
          startTimeUnixNano: start,
          endTimeUnixNano: nowNanos(),
          attributes: Object.entries(attributes).map(([k, v]) => attr(k, v)),
          status: error ? { code: STATUS_ERROR } : {},
        });
        if (queue.length >= MAX_BATCH_SPANS) flush();
      },
    };
  }

  async function shutdown() {
    clearInterval(timer);
    await flush();
  }

  return { enabled: true, startSpan, shutdown, flush };
}

module.exports = { createTracer, parseTraceparent, formatTraceparent, SPAN_KIND };
