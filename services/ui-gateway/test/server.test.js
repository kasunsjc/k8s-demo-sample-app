'use strict';

const test = require('node:test');
const assert = require('node:assert');
const http = require('node:http');
const { createApp, loadConfig } = require('../server');

function listen(server) {
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server.address().port)));
}

test('gateway proxies, serves UI and blocks traversal', async (t) => {
  const upstream = http.createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ path: req.url, host: req.headers.host, gw: req.headers['x-kubeplay-gateway'] }));
  });
  const upPort = await listen(upstream);
  const cfg = loadConfig({ INSPECTOR_URL: `http://127.0.0.1:${upPort}`, NOTES_URL: 'http://127.0.0.1:1' });
  const gateway = createApp(cfg);
  const port = await listen(gateway);
  const base = `http://127.0.0.1:${port}`;
  t.after(() => { upstream.close(); gateway.close(); });

  const proxied = await (await fetch(`${base}/api/inspector/whoami?x=1`)).json();
  assert.strictEqual(proxied.path, '/whoami?x=1');
  assert.ok(proxied.gw);

  const hijack = await (await fetch(`${base}/api/inspector//evil.example/x`)).json();
  assert.strictEqual(hijack.host, `127.0.0.1:${upPort}`);

  const down = await fetch(`${base}/api/notes/notes`);
  assert.strictEqual(down.status, 502);

  const index = await fetch(`${base}/`);
  assert.strictEqual(index.status, 200);
  assert.match(index.headers.get('content-type'), /text\/html/);

  const traversal = await fetch(`${base}/..%2fserver.js`);
  assert.strictEqual(traversal.status, 404);

  const info = await (await fetch(`${base}/api/gateway/info`)).json();
  assert.strictEqual(info.service, 'ui-gateway');
});
