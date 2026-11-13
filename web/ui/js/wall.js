// Wall screen: every monitor as a large hexagon, problems first and lit,
// for a TV in the operations room. Read-only. It is opened as
//   /wall#token=uka_…   (a read-only API token; never sent in the URL path)
// or by anyone already signed in to the console.
import { html, render, useState, useEffect, useRef } from './lib.js';
import { t, setLang, detectLang, loadLanguages, ago } from './i18n.js';

const PRODUCT = 'Unagikuma';

const REFRESH = 10000; // ms between updates
const STALE = 60000;   // older data than this is called out
const ORDER = { critical: 0, degraded: 1, unknown: 2, pending: 3, maintenance: 4, ok: 5 };

const token = (() => {
  const m = /(?:^|[#&])token=([^&]+)/.exec(location.hash);
  return m ? decodeURIComponent(m[1]) : '';
})();

async function api(path) {
  const r = await fetch('/api/v1' + path, {
    credentials: token ? 'omit' : 'same-origin',
    headers: token ? { Authorization: 'Bearer ' + token } : {},
  });
  if (!r.ok) throw Object.assign(new Error('HTTP ' + r.status), { status: r.status });
  return r.json();
}

const statusOf = (m) => (m.disabled ? 'paused' : m.status || 'pending');
const rank = (m) => (m.disabled ? 9 : ORDER[m.status] ?? 6);

// fit picks the largest hexagon that lets n of them fill w × h.
function fit(n, w, h) {
  let best = { size: 0, cols: 1 };
  for (let cols = 1; cols <= Math.max(1, n); cols++) {
    const rows = Math.ceil(n / cols);
    const byW = w / (cols + (rows > 1 ? 0.5 : 0));
    const byH = h / (0.75 * (rows - 1) + 1) / 1.1547;
    const size = Math.min(byW, byH, 300);
    if (size > best.size) best = { size, cols };
  }
  return best;
}

function useSize(ref) {
  const [s, setS] = useState({ w: 0, h: 0 });
  useEffect(() => {
    const o = new ResizeObserver(([e]) => setS({ w: e.contentRect.width, h: e.contentRect.height }));
    if (ref.current) o.observe(ref.current);
    return () => o.disconnect();
  }, []);
  return s;
}

function Hive({ items }) {
  const ref = useRef(null);
  const { w, h } = useSize(ref);
  const { size, cols } = fit(items.length, w, h);
  const hh = size * 1.1547;
  const gap = Math.max(4, size * 0.06); // a visible seam between cells
  const font = Math.max(11, Math.min(28, size / 7.5));
  const rows = Math.ceil(items.length / cols);
  const gridW = cols * size + (rows > 1 ? size / 2 : 0);
  const gridH = hh * (0.75 * (rows - 1) + 1);
  const ox = Math.max(0, (w - gridW) / 2), oy = Math.max(0, (h - gridH) / 2);
  return html`<div class="wall-hive" ref=${ref}>${size > 0 && items.map((m, i) => {
    const row = Math.floor(i / cols), col = i % cols;
    const st = statusOf(m);
    return html`<div class=${'wall-hex ' + st + (m.incident && m.incident.acked ? ' acked' : '')}
      style=${{ width: size - gap + 'px', height: hh - gap * 1.1547 + 'px', left: ox + col * size + (row % 2 ? size / 2 : 0) + gap / 2 + 'px', top: oy + row * hh * 0.75 + gap / 2 + 'px', fontSize: font + 'px', paddingInline: size * 0.13 + 'px' }}
      title=${m.title || m.name}>
      <span class="name">${m.title || m.name}</span>
      ${st !== 'ok' && size > 90 && html`<span class="st">${t(st === 'paused' ? 'ui.wall.paused' : 'status.' + st)}</span>`}
    </div>`;
  })}</div>`;
}

function Clock() {
  const [now, setNow] = useState(new Date());
  useEffect(() => { const id = setInterval(() => setNow(new Date()), 1000); return () => clearInterval(id); }, []);
  return html`<span class="clock">${now.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>`;
}

function Wall() {
  const [rows, setRows] = useState(null);
  const [err, setErr] = useState(null);
  const [at, setAt] = useState(0);
  const [, tick] = useState(0);
  useEffect(() => {
    let live = true;
    const load = async () => {
      try {
        const r = await api('/monitors');
        if (!live) return;
        setRows(r.items || []);
        setAt(Date.now());
        setErr(null);
      } catch (e) { if (live) setErr(e); }
    };
    load();
    const id = setInterval(load, REFRESH);
    const id2 = setInterval(() => tick(x => x + 1), 5000);
    return () => { live = false; clearInterval(id); clearInterval(id2); };
  }, []);
  useEffect(() => { document.title = PRODUCT + ' · ' + t('ui.wall.title'); }, []);

  if (err && err.status === 401 && !rows) {
    return html`<div class="wall-msg"><h1>${t('ui.wall.need_link')}</h1><p>${t('ui.wall.need_link_hint')}</p></div>`;
  }
  if (!rows) return html`<div class="wall-msg">${err ? html`<h1>${t('ui.wall.offline')}</h1>` : ''}</div>`;
  const items = [...rows].sort((a, b) => rank(a) - rank(b) || (a.title || a.name).localeCompare(b.title || b.name));
  const count = (s) => rows.filter(m => !m.disabled && m.status === s).length;
  const crit = count('critical'), warn = count('degraded'), unk = count('unknown');
  const overall = crit ? 'critical' : warn ? 'degraded' : unk ? 'unknown' : 'ok';
  const problems = items.filter(m => !m.disabled && (m.status === 'critical' || m.status === 'degraded'));
  const stale = at && Date.now() - at > STALE;
  return html`<div class="wall">
    <header class=${'wall-top ' + overall}>
      <img src="/logo.png" alt="" /><b class="prod">${PRODUCT}</b>
      <span class="overall">${overall === 'ok' ? '✓ ' + t('ui.wall.all_ok') : t('ui.wall.problems', { n: crit + warn })}</span>
      <span class="spacer"></span>
      ${[['critical', crit], ['degraded', warn], ['unknown', unk], ['ok', count('ok')]].map(([k, n]) => html`<span class=${'cnt ' + k + (n ? '' : ' zero')}>${n}<small>${t('status.' + k)}</small></span>`)}
      <${Clock} />
    </header>
    ${stale && html`<div class="wall-stale">${t('ui.wall.stale', { ago: ago(new Date(at).toISOString()) })}</div>`}
    <${Hive} items=${items} />
    ${problems.length > 0 && html`<footer class="wall-ticker">${problems.slice(0, 6).map(m => html`<span class=${m.status}>
      <b>${m.title || m.name}</b> ${m.last_change ? ago(m.last_change) : ''}${m.incident && m.incident.acked ? ' · ' + t('ui.wall.acked') : ''}</span>`)}</footer>`}
  </div>`;
}

(async () => {
  document.documentElement.dataset.theme = 'industrial';
  await loadLanguages();
  await setLang(detectLang(), false);
  render(html`<${Wall} />`, document.getElementById('app'));
})();
