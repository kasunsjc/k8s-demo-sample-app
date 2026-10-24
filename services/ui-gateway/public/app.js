'use strict';

const $ = (sel) => document.querySelector(sel);
const STALE_MS = 5000;
const FORGET_MS = 30000;
const COLOR_RE = /^(#[0-9a-fA-F]{3,8}|[a-zA-Z]{3,20})$/;

const pods = new Map();
let totalHits = 0;

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  Object.assign(node, props);
  for (const c of children) node.append(c);
  return node;
}

async function api(method, url, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(url, opts);
  const text = await res.text();
  let data = text;
  try { data = JSON.parse(text); } catch { /* plain text */ }
  return { status: res.status, data };
}

function log(message, ok = true) {
  const item = el('li', { className: ok ? '' : 'bad' },
    el('time', { textContent: new Date().toLocaleTimeString() }), ` ${message}`);
  $('#log').prepend(item);
  while ($('#log').children.length > 50) $('#log').lastChild.remove();
}

// ---- Pod fleet ----
async function pollFleet() {
  if (!$('#polling').checked) return;
  try {
    const { status, data } = await api('GET', '/api/inspector/whoami');
    if (status !== 200 || typeof data !== 'object') return;
    const entry = pods.get(data.pod) || { hits: 0 };
    entry.info = data;
    entry.hits += 1;
    entry.lastSeen = Date.now();
    pods.set(data.pod, entry);
    totalHits += 1;
  } catch { /* gateway unreachable */ }
}

function renderFleet() {
  const now = Date.now();
  const container = $('#pods');
  container.replaceChildren();
  const sorted = [...pods.entries()].sort(([a], [b]) => a.localeCompare(b));
  for (const [name, p] of sorted) {
    const age = now - p.lastSeen;
    if (age > FORGET_MS) { pods.delete(name); continue; }
    const color = COLOR_RE.test(p.info.color) ? p.info.color : '#64748b';
    const share = totalHits ? Math.round((p.hits / totalHits) * 100) : 0;
    const tile = el('div', { className: `pod${age > STALE_MS ? ' stale' : ''}` },
      el('div', { className: 'pod-name', textContent: name }),
      el('div', { className: 'pod-version', textContent: p.info.version }),
      el('div', { className: 'pod-meta', textContent: `node: ${p.info.node}` }),
      el('div', { className: 'pod-meta', textContent: `ip: ${p.info.podIP}` }),
      el('div', { className: 'pod-meta', textContent: `uptime: ${p.info.uptimeSeconds}s` }),
      el('div', { className: 'pod-share', textContent: `${p.hits} hits · ${share}%` }),
      el('div', { className: 'pod-state', textContent: age > STALE_MS ? 'not answering' : 'serving' }));
    tile.style.setProperty('--pod-color', color);
    container.append(tile);
  }
  $('#stats').textContent = `${pods.size} pods · ${totalHits} requests`;
}

$('#reset-fleet').addEventListener('click', () => { pods.clear(); totalHits = 0; renderFleet(); });

// ---- Chaos / inspect ----
document.querySelectorAll('button[data-url]').forEach((btn) => {
  btn.addEventListener('click', async () => {
    const { method, url } = btn.dataset;
    try {
      const { status, data } = await api(method, url);
      const pod = data && (data.pod || data.servedBy);
      log(`${method} ${url} → ${status}${pod ? ` (pod ${pod})` : ''}${data && data.effect ? ` — ${data.effect}` : ''}`, status < 400);
    } catch (err) {
      log(`${method} ${url} failed: ${err.message}`, false);
    }
  });
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
  try {
    const { status, data } = await api('GET', '/api/notes/notes');
    const list = $('#notes');
    list.replaceChildren();
    if (status !== 200) {
      list.append(el('li', { className: 'bad', textContent: `notes-api unavailable (HTTP ${status})` }));
      return;
    }
    for (const n of data.notes) {
      const del = el('button', { className: 'ghost', textContent: '✕', title: 'delete' });
      del.addEventListener('click', async () => {
        const res = await api('DELETE', `/api/notes/notes/${encodeURIComponent(n.id)}`);
        log(`deleted note ${n.id} → ${res.status}`, res.status < 400);
        loadNotes();
      });
      list.append(el('li', {},
        el('strong', { textContent: n.title }),
        el('span', { className: 'muted', textContent: ` ${n.body} — by ${n.createdBy}` }),
        del));
    }
  } catch { /* ignore */ }
}

$('#note-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const { status, data } = await api('POST', '/api/notes/notes', { title: $('#note-title').value, body: $('#note-body').value });
  log(`created note → ${status}${data && data.createdBy ? ` (pod ${data.createdBy})` : ''}`, status < 400);
  if (status < 400) e.target.reset();
  loadNotes();
});

// ---- Jobs ----
document.querySelectorAll('button[data-job]').forEach((btn) => {
  btn.addEventListener('click', async () => {
    const { status, data } = await api('POST', '/api/notes/jobs', { type: btn.dataset.job, seconds: 10 });
    log(`enqueued ${btn.dataset.job} job → ${status}${data && data.queueLength ? ` (queue ${data.queueLength})` : ''}`, status < 400);
    loadJobs();
  });
});

async function loadJobs() {
  try {
    const { status, data } = await api('GET', '/api/notes/jobs');
    if (status !== 200) return;
    $('#queue').textContent = `queue length: ${data.queueLength}`;
    const list = $('#jobs');
    list.replaceChildren();
    for (const j of data.results.slice(0, 10)) {
      list.append(el('li', {},
        el('strong', { textContent: `${j.type || 'job'} ` }),
        el('span', { className: j.status === 'done' ? 'muted' : 'bad', textContent: `${j.status} by ${j.processedBy}` }),
        el('div', { className: 'muted small', textContent: JSON.stringify(j.output || j.error || {}) })));
    }
  } catch { /* ignore */ }
}

// ---- Boot ----
(async () => {
  try {
    const { data } = await api('GET', '/api/gateway/info');
    $('#title').textContent = data.title;
    document.title = data.title;
    $('#gateway').textContent = `gateway pod: ${data.pod} · ${data.version}`;
  } catch { /* ignore */ }
})();

setInterval(pollFleet, 400);
setInterval(renderFleet, 1000);
setInterval(loadJobs, 3000);
setInterval(loadNotes, 5000);
loadNotes();
loadJobs();
