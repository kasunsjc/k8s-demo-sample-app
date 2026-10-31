'use strict';

const $ = (sel) => document.querySelector(sel);
const STALE_MS = 5000;
const FORGET_MS = 30000;
const COLOR_RE = /^(#[0-9a-fA-F]{3,8}|[a-zA-Z]{3,20})$/;
const HEALTH = [
  { key: 'gateway', label: 'ui-gateway', concept: 'Deployment + NodePort/Ingress' },
  { key: 'inspector', label: 'inspector-api', concept: 'readinessProbe /readyz' },
  { key: 'notes', label: 'notes-api', concept: 'dependency-aware readiness' },
  { key: 'redis', label: 'redis', concept: 'StatefulSet + PVC' },
];

const pods = new Map();
let totalHits = 0;

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  Object.assign(node, props);
  for (const c of children) if (c !== null && c !== undefined) node.append(c);
  return node;
}

async function api(method, url, body, headers = {}) {
  const opts = { method, headers: { ...headers } };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = typeof body === 'string' ? body : JSON.stringify(body);
  }
  const start = performance.now();
  const res = await fetch(url, opts);
  const text = await res.text();
  let data = text;
  try { data = JSON.parse(text); } catch { /* plain text */ }
  // x-trace-id is set by the gateway when tracing is enabled; search for it in Application Insights.
  const traceId = res.headers.get('x-trace-id');
  return { status: res.status, data, headers: res.headers, ms: Math.round(performance.now() - start), traceId };
}

function log(message, ok = true) {
  const list = $('#log');
  const placeholder = list.querySelector('.empty');
  if (placeholder) placeholder.remove();
  list.prepend(el('li', { className: ok ? '' : 'bad' },
    el('time', { textContent: new Date().toLocaleTimeString() }), ` ${message}`));
  while (list.children.length > 60) list.lastChild.remove();
}

const fmtUptime = (s) => (s >= 3600 ? `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
  : s >= 60 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${s}s`);
const traceSuffix = (traceId) => (traceId ? ` · trace ${traceId}` : '');
const orDash = (v) => (v === undefined || v === null || v === 'unknown' || v === 'unset' ? '–' : v);

// ---- Header: gateway info + health (retried, so it recovers if upstreams start late) ----
async function loadGateway() {
  try {
    const { status, data } = await api('GET', '/api/gateway/info');
    if (status !== 200) throw new Error(`HTTP ${status}`);
    $('#title').textContent = data.title;
    document.title = data.title;
    const k8s = data.platform === 'kubernetes';
    $('#platform').textContent = k8s ? `Kubernetes · ns ${data.namespace}` : 'Docker Compose';
    $('#platform').className = `badge ${k8s ? 'k8s' : 'compose'}`;
    $('#gateway').textContent = `gateway ${data.pod} · ${data.version} · node ${orDash(data.node)} · up ${fmtUptime(data.uptimeSeconds)}`;
  } catch {
    $('#gateway').textContent = 'gateway: unreachable, retrying…';
  }
}

async function loadHealth() {
  let data = {};
  try { ({ data } = await api('GET', '/api/gateway/status')); } catch { /* gateway down */ }
  const chips = $('#health');
  const rows = $('#health-rows');
  chips.replaceChildren();
  rows.replaceChildren();
  for (const h of HEALTH) {
    const s = data[h.key] || { ok: false };
    const state = s.ok ? 'ok' : 'down';
    chips.append(el('span', { className: `chip-status ${state}`, title: `${h.label}: ${state}` }, el('i'), h.label));
    rows.append(el('tr', {},
      el('td', { textContent: h.label }),
      el('td', {}, el('span', { className: `pill ${state}`, textContent: s.ok ? 'ready' : (s.error || `HTTP ${s.status || '–'}`) })),
      el('td', { textContent: s.latencyMs !== undefined ? `${s.latencyMs} ms` : '–' }),
      el('td', { className: 'muted', textContent: h.concept })));
  }
}

// ---- Pod fleet ----
async function pollFleet() {
  if (!$('#polling').checked) return;
  try {
    // Marked as background polling so the gateway does not sample it into traces.
    const { status, data } = await api('GET', '/api/inspector/whoami', undefined, { 'X-KubePlay-Poll': 'fleet' });
    if (status !== 200 || typeof data !== 'object') return;
    const entry = pods.get(data.pod) || { hits: 0, firstSeen: Date.now() };
    entry.info = data;
    entry.hits += 1;
    entry.lastSeen = Date.now();
    pods.set(data.pod, entry);
    totalHits += 1;
  } catch { /* gateway unreachable */ }
}

function kv(label, value) {
  return el('div', { className: 'kv' }, el('span', { textContent: label }), el('b', { textContent: orDash(value) }));
}

function renderFleet() {
  const now = Date.now();
  const container = $('#pods');
  container.replaceChildren();
  const sorted = [...pods.entries()].sort(([a], [b]) => a.localeCompare(b));
  for (const [name, p] of sorted) {
    const age = now - p.lastSeen;
    if (age > FORGET_MS) { pods.delete(name); continue; }
    const i = p.info;
    const color = COLOR_RE.test(i.color) ? i.color : '#64748b';
    const share = totalHits ? Math.round((p.hits / totalHits) * 100) : 0;
    const stale = age > STALE_MS;
    const limits = i.limits || {};
    const bar = el('div', { className: 'bar' }, el('div', { className: 'bar-fill' }));
    bar.firstChild.style.width = `${share}%`;
    const tile = el('div', { className: `pod${stale ? ' stale' : ''}` },
      el('div', { className: 'pod-top' },
        el('span', { className: 'pod-version', textContent: i.version }),
        el('span', { className: `pill ${stale ? 'down' : 'ok'}`, textContent: stale ? 'not answering' : 'serving' })),
      el('div', { className: 'pod-name', textContent: name, title: name }),
      kv('node', i.node),
      kv('pod IP', i.podIP),
      kv('namespace', i.namespace),
      kv('service acct', i.serviceAccount),
      kv('limits', limits.cpuMillicores && limits.cpuMillicores !== 'unset'
        ? `${limits.cpuMillicores}m CPU · ${limits.memoryMiB} MiB` : 'none'),
      kv('uptime', fmtUptime(i.uptimeSeconds)),
      el('div', { className: 'share' }, el('span', { textContent: `${p.hits} hits` }), el('b', { textContent: `${share}%` })),
      bar);
    tile.style.setProperty('--pod-color', color);
    container.append(tile);
  }
  if (!container.children.length) {
    container.append(el('div', { className: 'empty', textContent: $('#polling').checked ? 'Waiting for inspector-api pods to answer…' : 'Polling paused.' }));
  }
  const live = [...pods.values()].filter((p) => now - p.lastSeen <= STALE_MS).length;
  $('#stats').textContent = `${live}/${pods.size} pods serving · ${totalHits} requests`;
}

$('#reset-fleet').addEventListener('click', () => { pods.clear(); totalHits = 0; renderFleet(); });

// ---- Chaos ----
document.querySelectorAll('button[data-url]').forEach((btn) => {
  btn.addEventListener('click', async () => {
    const { method, url } = btn.dataset;
    btn.disabled = true;
    try {
      const { status, data, ms, traceId } = await api(method, url);
      const pod = data && (data.pod || data.servedBy);
      log(`${method} ${url} → ${status} in ${ms} ms${pod ? ` (pod ${pod})` : ''}${data && data.effect ? ` — ${data.effect}` : ''}${traceSuffix(traceId)}`, status < 400);
    } catch (err) {
      log(`${method} ${url} failed: ${err.message}`, false);
    } finally {
      btn.disabled = false;
    }
  });
});

// ---- Request explorer ----
document.querySelectorAll('[data-preset]').forEach((chip) => {
  chip.addEventListener('click', () => {
    $('#ex-method').value = 'GET';
    $('#ex-path').value = chip.dataset.preset;
    $('#ex-body').value = '';
    $('#explorer').requestSubmit();
  });
});

function explorerRequest() {
  const method = $('#ex-method').value;
  let path = $('#ex-path').value.trim() || '/';
  if (!path.startsWith('/')) path = `/${path}`;
  const raw = $('#ex-body').value.trim();
  return { method, url: `/api/inspector${path}`, body: raw && method !== 'GET' ? raw : undefined };
}

$('#ex-copy-curl').addEventListener('click', async () => {
  const { method, url, body } = explorerRequest();
  const absoluteUrl = new URL(url, window.location.origin).href;
  const command = window.kubeplayBuildCurlCommand(method, absoluteUrl, body);
  try {
    if (!navigator.clipboard?.writeText) throw new Error('Clipboard API unavailable');
    await navigator.clipboard.writeText(command);
    $('#ex-meta').className = 'meta small';
    $('#ex-meta').textContent = 'cURL command copied to clipboard.';
  } catch {
    $('#ex-meta').className = 'meta small';
    $('#ex-meta').textContent = 'Clipboard unavailable; select and copy the cURL command below.';
    $('#ex-out').textContent = command;
  }
});

$('#explorer').addEventListener('submit', async (e) => {
  e.preventDefault();
  const { method, url, body } = explorerRequest();
  try {
    const { status, data, headers, ms, traceId } = await api(method, url, body);
    const type = headers.get('content-type') || '–';
    $('#ex-meta').className = `meta small ${status < 400 ? '' : 'bad'}`;
    $('#ex-meta').textContent = `${method} ${url} → HTTP ${status} · ${ms} ms · ${type}${traceSuffix(traceId)}`;
    $('#ex-out').textContent = typeof data === 'string' ? data : JSON.stringify(data, null, 2);
    log(`${method} ${url} → ${status} in ${ms} ms${traceSuffix(traceId)}`, status < 400);
  } catch (err) {
    $('#ex-meta').textContent = `Request failed: ${err.message}`;
  }
});

document.querySelectorAll('button[data-inspect]').forEach((btn) => {
  btn.addEventListener('click', async () => {
    try {
      const { status, data } = await api('GET', btn.dataset.inspect);
      $('#inspect').textContent = `HTTP ${status}\n${typeof data === 'string' ? data : JSON.stringify(data, null, 2)}`;
    } catch (err) {
      $('#inspect').textContent = `Request failed: ${err.message}`;
    }
  });
});

// ---- Notes ----
async function loadNotes() {
  const list = $('#notes');
  try {
    const { status, data } = await api('GET', '/api/notes/notes', undefined, { 'X-KubePlay-Poll': 'background' });
    list.replaceChildren();
    if (status !== 200) {
      $('#notes-meta').textContent = '';
      list.append(el('li', { className: 'bad', textContent: `notes-api unavailable (HTTP ${status})` }));
      return;
    }
    $('#notes-meta').textContent = `${data.count} notes · listed by pod ${data.servedBy}`;
    if (!data.notes.length) list.append(el('li', { className: 'empty', textContent: 'No notes yet — add one, then delete redis-0 to prove persistence.' }));
    for (const n of data.notes) {
      const del = el('button', { className: 'ghost icon', textContent: '✕', title: 'delete' });
      del.addEventListener('click', async () => {
        const res = await api('DELETE', `/api/notes/notes/${encodeURIComponent(n.id)}`);
        log(`deleted note ${n.id} → ${res.status}`, res.status < 400);
        loadNotes();
      });
      list.append(el('li', {},
        el('div', { className: 'grow' },
          el('strong', { textContent: n.title }),
          n.body ? el('div', { className: 'muted small', textContent: n.body }) : null,
          el('div', { className: 'muted small', textContent: `by ${n.createdBy} · ${new Date(n.createdAt).toLocaleTimeString()}` })),
        del));
    }
  } catch { /* ignore */ }
}

$('#note-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const { status, data, ms, traceId } = await api('POST', '/api/notes/notes', { title: $('#note-title').value, body: $('#note-body').value });
  log(`created note → ${status} in ${ms} ms${data && data.createdBy ? ` (pod ${data.createdBy})` : ''}${traceSuffix(traceId)}`, status < 400);
  if (status < 400) e.target.reset();
  loadNotes();
});

// ---- Jobs ----
document.querySelectorAll('button[data-job]').forEach((btn) => {
  btn.addEventListener('click', async () => {
    const { status, data, traceId } = await api('POST', '/api/notes/jobs', { type: btn.dataset.job, seconds: 10 });
    log(`enqueued ${btn.dataset.job} job → ${status}${data && data.queueLength !== undefined ? ` (queue ${data.queueLength})` : ''}${traceSuffix(traceId)}`, status < 400);
    loadJobs();
  });
});

async function loadJobs() {
  try {
    const { status, data } = await api('GET', '/api/notes/jobs', undefined, { 'X-KubePlay-Poll': 'background' });
    if (status !== 200) return;
    $('#queue').textContent = data.queueLength;
    $('#done').textContent = data.results.length;
    $('#workers').textContent = new Set(data.results.map((j) => j.processedBy)).size;
    const list = $('#jobs');
    list.replaceChildren();
    if (!data.results.length) list.append(el('li', { className: 'empty', textContent: 'No results yet. The CronJob reports every 5 minutes.' }));
    for (const j of data.results.slice(0, 10)) {
      list.append(el('li', {},
        el('div', { className: 'grow' },
          el('strong', { textContent: j.type || 'job' }),
          el('div', { className: 'muted small', textContent: `${j.processedBy} · ${j.finishedAt ? new Date(j.finishedAt).toLocaleTimeString() : ''}${traceSuffix(j.traceId)}` }),
          el('div', { className: 'muted small mono', textContent: JSON.stringify(j.output || j.error || {}) })),
        el('span', { className: `pill ${j.status === 'done' ? 'ok' : 'down'}`, textContent: j.status })));
    }
  } catch { /* ignore */ }
}

// ---- Boot ----
$('#log').append(el('li', { className: 'empty', textContent: 'Actions you take will appear here.' }));
loadGateway();
loadHealth();
loadNotes();
loadJobs();
renderFleet();
setInterval(pollFleet, 400);
setInterval(renderFleet, 1000);
setInterval(loadHealth, 3000);
setInterval(loadJobs, 3000);
setInterval(loadNotes, 5000);
setInterval(loadGateway, 10000);
