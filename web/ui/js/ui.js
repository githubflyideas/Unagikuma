// Shared components and hooks.
import { html, useState, useEffect, useRef, useCallback, useLayoutEffect } from './lib.js';
import { t, ago, fmtTime, fmtNum, detailText } from './i18n.js';
import { onLive } from './live.js';
import { product } from './brand.js';
import { me } from './api.js';

// ---- routing (hash based, so the server needs no fallback routes) ----

export function parseHash() {
  const raw = location.hash.replace(/^#/, '') || '/';
  const [path, query] = raw.split('?');
  return { path, parts: path.split('/').filter(Boolean).map(decodeURIComponent), query: new URLSearchParams(query || '') };
}

export function useRoute() {
  const [r, setR] = useState(parseHash());
  useEffect(() => {
    const f = () => setR(parseHash());
    addEventListener('hashchange', f);
    return () => removeEventListener('hashchange', f);
  }, []);
  return r;
}

export function go(path) { location.hash = '#' + path; }
// replaceRoute changes the address without a new history entry and tells
// every view, as a navigation would.
export function replaceRoute(path) {
  history.replaceState(null, '', '#' + path);
  dispatchEvent(new HashChangeEvent('hashchange'));
}
// monitorsPath is the monitor list for a query and an order.
export function monitorsPath(q, sort) {
  const p = new URLSearchParams();
  if (q) p.set('q', q);
  if (sort && sort !== 'status') p.set('sort', sort);
  const s = p.toString();
  return '/monitors' + (s ? '?' + s : '');
}
export const MON_SORTS = ['status', 'name', 'last_change', 'latency', 'uptime'];
export function href(path) { return '#' + path; }
export const enc = encodeURIComponent;

// ---- data ----

// useFetch loads a value; reload() refetches without blanking the view.
export function useFetch(fn, deps) {
  const [state, setState] = useState({ data: null, error: null, loading: true });
  const seq = useRef(0);
  const load = useCallback(() => {
    const my = ++seq.current;
    setState(s => ({ ...s, loading: true }));
    return Promise.resolve().then(fn).then(
      data => { if (my === seq.current) setState({ data, error: null, loading: false }); },
      error => { if (my === seq.current) setState(s => ({ data: s.data, error, loading: false })); });
  }, deps);
  useEffect(() => { load(); }, [load]);
  return { ...state, reload: load, setData: (d) => setState(s => ({ ...s, data: typeof d === 'function' ? d(s.data) : d })) };
}

// useLive calls f on the given live event types, at most once per delay:
// the first event of a burst starts the wait and later ones join it. A
// wait that restarted on every event would never end while events keep
// coming (critical monitors report every round), and the page would stop
// updating on exactly the busy installations that need it.
export function useLive(types, f, delay = 400) {
  const ref = useRef(f);
  ref.current = f;
  useEffect(() => {
    let timer = null;
    const fire = () => {
      if (timer) return;
      timer = setTimeout(() => { timer = null; ref.current(); }, delay);
    };
    const offs = [...types, 'reset'].map(tp => onLive(tp, fire));
    return () => { clearTimeout(timer); offs.forEach(o => o()); };
  }, [types.join(','), delay]);
}

export function useInterval(f, ms) {
  const ref = useRef(f);
  ref.current = f;
  useEffect(() => {
    const id = setInterval(() => ref.current(), ms);
    return () => clearInterval(id);
  }, [ms]);
}

// useTick re-renders periodically (relative times).
export function useTick(ms = 15000) {
  const [, set] = useState(0);
  useInterval(() => set(x => x + 1), ms);
}

// ---- toasts ----

let toastSet = null;
export function toast(msg, kind) {
  if (toastSet) {
    const id = Math.random();
    toastSet(ts => [...ts, { id, msg, kind }]);
    setTimeout(() => toastSet(ts => ts.filter(x => x.id !== id)), kind === 'error' ? 7000 : 3500);
  }
}
export function toastError(e) { toast(errText(e), 'error'); }

export function Toasts() {
  const [items, set] = useState([]);
  toastSet = set;
  return html`<div class="toasts" role="status">${items.map(i => html`<div key=${i.id} class=${'toast ' + (i.kind || '')}>${i.msg}</div>`)}</div>`;
}

export function errText(e) {
  if (!e) return '';
  if (e.code && e.code !== 'internal') {
    const k = 'ui.err.' + e.code;
    const tr = t(k);
    if (tr !== k && !e.message) return tr;
    if (e.code === 'invalid' || e.code === 'conflict' || e.code === 'not_found') return e.message;
    if (tr !== k) return tr;
  }
  if (e.status === 0) return t('ui.err.network');
  return e.message || String(e);
}

// ---- small components ----

export function Pill({ status, soft }) {
  const s = status || 'pending';
  return html`<span class=${'pill ' + s + (soft ? ' soft' : '')}>${t('status.' + s)}</span>`;
}

export function Dot({ status, pulse }) {
  return html`<i class=${'dot ' + (status || 'pending') + (pulse ? ' pulse' : '')}></i>`;
}

export function Beats({ cells, big }) {
  if (!cells) return html`<div class=${'beats' + (big ? ' big' : '')}></div>`;
  return html`<div class=${'beats' + (big ? ' big' : '')}>${cells.map(c => {
    const st = c.s || '';
    const cls = st === 'ok' && c.fail > 0 ? 'partial' : st;
    const tip = c.n ? `${fmtTime(c.t)} · ${t('status.' + (st || 'unknown'))}${c.fail ? ' · ' + t('ui.beats.failed', { n: c.fail, total: c.n }) : ''}` : `${fmtTime(c.t)} · ${t('ui.beats.nodata')}`;
    return html`<i class=${cls} title=${tip}></i>`;
  })}</div>`;
}

export function Ago({ at, title }) {
  useTick();
  if (!at) return html`<span class="faint">–</span>`;
  return html`<span title=${title || fmtTime(at)}>${ago(at)}</span>`;
}

export function Loading() { return html`<div class="empty"><span class="spin"></span></div>`; }

export function Empty({ title, children }) {
  return html`<div class="empty">${title && html`<b>${title}</b>`}${children}</div>`;
}

export function ErrorBox({ error, retry }) {
  if (!error) return null;
  return html`<div class="banner error"><div class="spacer">${errText(error)}</div>${retry && html`<button class="btn sm" onClick=${retry}>${t('ui.retry')}</button>`}</div>`;
}

export function Card({ title, actions, children, flush, class: cls }) {
  return html`<section class=${'card ' + (cls || '')}>
    ${(title || actions) && html`<div class="hd"><h2>${title}</h2><div class="spacer"></div>${actions}</div>`}
    <div class=${'bd' + (flush ? ' flush' : '')}>${children}</div>
  </section>`;
}

export function Modal({ title, onClose, children, footer, wide }) {
  useEffect(() => {
    const f = (e) => { if (e.key === 'Escape') onClose(); };
    addEventListener('keydown', f);
    return () => removeEventListener('keydown', f);
  }, [onClose]);
  return html`<div class="modal-bg" onMouseDown=${(e) => { if (e.target === e.currentTarget) onClose(); }}>
    <div class=${'modal' + (wide ? ' wide' : '')} role="dialog" aria-modal="true">
      <div class="hd"><h2>${title}</h2><div class="spacer"></div><button class="btn ghost sm" onClick=${onClose} aria-label=${t('ui.close')}>✕</button></div>
      <div class="bd">${children}</div>
      ${footer && html`<div class="ft">${footer}</div>`}
    </div>
  </div>`;
}

// confirmDialog shows a modal and resolves true/false.
let confirmSet = null;
export function confirmDialog(message, { danger, ok } = {}) {
  return new Promise(resolve => confirmSet && confirmSet({ message, danger, ok, resolve }));
}
export function ConfirmHost() {
  const [c, set] = useState(null);
  confirmSet = set;
  if (!c) return null;
  const done = (v) => { set(null); c.resolve(v); };
  return html`<${Modal} title=${t('ui.confirm')} onClose=${() => done(false)} footer=${html`
      <button class="btn" onClick=${() => done(false)}>${t('ui.cancel')}</button>
      <button class=${'btn ' + (c.danger ? 'danger solid' : 'primary')} onClick=${() => done(true)}>${c.ok || t('ui.ok')}</button>`}>
    <p>${c.message}</p>
  <//>`;
}

let fieldSeq = 0;

// Field labels the first control inside it, so screen readers announce the
// label and a click on it focuses the control.
export function Field({ label, hint, children, error }) {
  const box = useRef(null);
  const id = useRef('');
  if (!id.current) id.current = 'fld-' + (++fieldSeq);
  useLayoutEffect(() => {
    const el = box.current?.querySelector('input:not([type=hidden]), select, textarea');
    const lb = box.current?.querySelector(':scope > label');
    if (!el || !lb) return;
    if (!el.id) el.id = id.current;
    lb.htmlFor = el.id;
  });
  return html`<div class="field" ref=${box}>${label && html`<label>${label}</label>`}${children}${error ? html`<div class="hint err-text">${error}</div>` : hint && html`<div class="hint">${hint}</div>`}</div>`;
}

export function Seg({ value, options, onChange, label }) {
  return html`<div class="seg" role="tablist" aria-label=${label}>${options.map(([v, text]) => html`<button type="button" role="tab" aria-selected=${v === value} class=${v === value ? 'on' : ''} onClick=${() => onChange(v)}>${text}</button>`)}</div>`;
}

export function Copy({ text, label }) {
  const [done, set] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement('textarea');
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      ta.remove();
    }
    set(true);
    setTimeout(() => set(false), 1500);
  };
  return html`<button type="button" class="btn sm" onClick=${copy}>${done ? t('ui.copied') : (label || t('ui.copy'))}</button>`;
}

export function pct(r, digits = 2) {
  if (r == null) return '–';
  if (r >= 1) return '100%';
  return fmtNum(Math.floor(r * 100 * 10 ** digits) / 10 ** digits, digits) + '%';
}

export function msText(v) {
  if (v == null || v === 0) return '–';
  if (v < 10) return fmtNum(v, 1) + ' ms';
  if (v < 10000) return fmtNum(Math.round(v)) + ' ms';
  return fmtNum(v / 1000, 1) + ' s';
}

export function bytes(n) {
  if (n == null) return '–';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return fmtNum(n, i ? 1 : 0) + ' ' + u[i];
}

export function reasonText(reason, detail) {
  if (!reason || reason === 'ok') return detailText(detail);
  const k = 'reason.' + reason;
  const tr = t(k);
  return tr === k ? (detailText(detail) || reason) : tr;
}

export function useDocTitle(title) {
  useEffect(() => { document.title = title ? title + ' · ' + product() : product(); }, [title]);
}

// quorumText says how a round was decided, in the UI language: failures
// out of nodes, how many were needed, and which nodes failed.
export function quorumText(q) {
  if (!q || !q.n || q.n < 2) return '';
  let s = t('ui.mon.quorum', { fail: q.fail, n: q.n, q: q.q_eff });
  if (q.degraded) s += ' · ' + t('ui.mon.quorum_degraded', { healthy: q.healthy, n: q.n });
  if (q.failing_nodes && q.failing_nodes.length) s += ' · ' + q.failing_nodes.join(', ');
  return s;
}

// openMonitor shows a monitor in the side drawer without leaving the page.
export function openMonitor(name) { dispatchEvent(new CustomEvent('unagikuma:monitor', { detail: name })); }

// gwBad: the gateway answered ping before and is down now. One that never
// answered (many routers ignore ping) is not a fault. Older nodes send no
// state: then any failure counts.
export function gwBad(g) { return g.state ? g.state === 'down' : !g.ok; }

// Teams and regions. The built-in admin team and the global region are
// shown by a translated name.
export function teamTitle(tm) {
  if (!tm) return '';
  if (tm.name === 'admin' && (!tm.title || tm.title === 'admin')) return t('ui.team.admin');
  return tm.title || tm.name;
}

export function regionTitle(name) {
  return name === 'global' ? t('ui.region.global') : name;
}

// writableTeams are the teams the signed-in user may add monitors to.
export function writableTeams(data) {
  const items = (data && data.items) || [];
  return me.teams ? items.filter(tm => me.teams[tm.name] === 'operator') : items;
}
