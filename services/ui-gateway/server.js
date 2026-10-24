'use strict';

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');

const PUBLIC_DIR = path.join(__dirname, 'public');
const API_ROUTE = /^\/api\/(inspector|notes)(\/.*)?$/;
const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon',
};
const SECURITY_HEADERS = {
  'X-Content-Type-Options': 'nosniff',
  'X-Frame-Options': 'DENY',
  'Content-Security-Policy': "default-src 'self'; style-src 'self' 'unsafe-inline'",
};

function loadConfig(env = process.env) {
  return {
    port: Number(env.PORT || 3000),
    upstreams: {
      inspector: env.INSPECTOR_URL || 'http://localhost:8080',
      notes: env.NOTES_URL || 'http://localhost:8000',
    },
    title: env.UI_TITLE || 'KubePlay',
    version: env.APP_VERSION || 'v1',
  };
}

const podName = () => process.env.POD_NAME || os.hostname();

function sendJSON(res, status, body) {
  const data = JSON.stringify(body);
  res.writeHead(status, { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data) });
  res.end(data);
}

async function probe(base, path) {
  const start = Date.now();
  try {
    const r = await fetch(new URL(path, base), { signal: AbortSignal.timeout(2000) });
    return { ok: r.ok, status: r.status, latencyMs: Date.now() - start };
  } catch (err) {
    return { ok: false, status: 0, latencyMs: Date.now() - start, error: err.name === 'TimeoutError' ? 'timeout' : 'unreachable' };
  }
}

function proxy(req, res, base, upstreamPath, search) {
  // Only the path/query come from the client, so the upstream host can't be overridden (no SSRF).
  const target = new URL(base);
  target.pathname = upstreamPath;
  target.search = search;

  const clientIp = req.socket.remoteAddress || '';
  const headers = { ...req.headers, host: target.host };
  headers['x-forwarded-for'] = req.headers['x-forwarded-for'] ? `${req.headers['x-forwarded-for']}, ${clientIp}` : clientIp;
  headers['x-forwarded-host'] = req.headers.host || '';
  headers['x-kubeplay-gateway'] = podName();

  // agent:false opens a new connection per request so kube-proxy/Docker DNS spread calls across pods.
  const upstreamReq = http.request(target, { method: req.method, headers, timeout: 35_000, agent: false }, (upRes) => {
    res.writeHead(upRes.statusCode, upRes.headers);
    upRes.pipe(res);
  });
  upstreamReq.on('timeout', () => upstreamReq.destroy(new Error('upstream timeout')));
  upstreamReq.on('error', (err) => {
    if (res.headersSent) return res.destroy();
    sendJSON(res, 502, { error: 'bad gateway', upstream: base, detail: err.message, gateway: podName() });
  });
  req.pipe(upstreamReq);
}

function serveStatic(req, res, pathname) {
  let rel;
  try {
    rel = decodeURIComponent(pathname);
  } catch {
    return sendJSON(res, 400, { error: 'bad path' });
  }
  if (rel === '/') rel = '/index.html';
  const filePath = path.join(PUBLIC_DIR, path.normalize(rel));
  if (!filePath.startsWith(PUBLIC_DIR + path.sep)) return sendJSON(res, 404, { error: 'not found' });

  fs.stat(filePath, (err, stat) => {
    if (err || !stat.isFile()) return sendJSON(res, 404, { error: 'not found' });
    res.writeHead(200, {
      ...SECURITY_HEADERS,
      'Content-Type': MIME[path.extname(filePath)] || 'application/octet-stream',
      'Content-Length': stat.size,
      'Cache-Control': 'no-cache',
    });
    if (req.method === 'HEAD') return res.end();
    fs.createReadStream(filePath).pipe(res);
  });
}

function createApp(cfg = loadConfig()) {
  return http.createServer((req, res) => {
    const { pathname, search } = new URL(req.url, 'http://localhost');

    if (pathname === '/healthz' || pathname === '/readyz') {
      return sendJSON(res, 200, { status: 'ok' });
    }
    if (pathname === '/api/gateway/info') {
      return sendJSON(res, 200, {
        service: 'ui-gateway',
        pod: podName(),
        namespace: process.env.POD_NAMESPACE || 'unknown',
        node: process.env.NODE_NAME || 'unknown',
        version: cfg.version,
        title: cfg.title,
        runtime: process.version,
        uptimeSeconds: Math.round(process.uptime()),
        platform: process.env.KUBERNETES_SERVICE_HOST ? 'kubernetes' : 'docker',
      });
    }
    if (pathname === '/api/gateway/status') {
      return Promise.all([probe(cfg.upstreams.inspector, '/readyz'), probe(cfg.upstreams.notes, '/readyz')])
        .then(([inspector, notes]) => sendJSON(res, 200, {
          gateway: { ok: true, status: 200, latencyMs: 0 },
          inspector,
          // notes-api readiness pings Redis, so it doubles as the Redis check.
          notes,
          redis: { ok: notes.ok, status: notes.status, latencyMs: notes.latencyMs },
        }));
    }
    const match = API_ROUTE.exec(pathname);
    if (match) {
      return proxy(req, res, cfg.upstreams[match[1]], match[2] || '/', search);
    }
    if (req.method !== 'GET' && req.method !== 'HEAD') {
      return sendJSON(res, 405, { error: 'method not allowed' });
    }
    return serveStatic(req, res, pathname);
  });
}

if (require.main === module) {
  const cfg = loadConfig();
  const server = createApp(cfg);
  server.listen(cfg.port, () => {
    console.log(JSON.stringify({ msg: 'ui-gateway listening', port: cfg.port, upstreams: cfg.upstreams }));
  });
  const shutdown = (signal) => {
    console.log(JSON.stringify({ msg: `${signal} received, closing server` }));
    server.close(() => process.exit(0));
    setTimeout(() => process.exit(0), 10_000).unref();
  };
  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);
}

module.exports = { createApp, loadConfig };
