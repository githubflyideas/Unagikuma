// Drawer: a monitor at a glance beside the page it was opened from — how
// the last round was decided, its path, what happened and its record.
import { html, useState, useEffect } from '../lib.js';
import { t, fmtTime } from '../i18n.js';
import { get, post } from '../api.js';
import { useFetch, useLive, Loading, ErrorBox, Pill, msText, reasonText, quorumText, enc, toast, toastError } from '../ui.js';
import { EventRows } from './events.js';
import { Path } from './monitor.js';

function voteText(v) {
  if (v.vote === 'ok') return msText(v.latency_ms);
  if (v.vote === 'fail' || v.vote === 'abstain') return reasonText(v.reason, v.detail);
  return t('ui.vote.' + v.vote);
}

function Decide({ row }) {
  const q = row.quorum;
  if (!row.votes || !row.votes.length || !q) {
    return html`<div class="decide"><div class="drow"><b>${t('ui.path.host')}</b>
      <span class=${'vote ' + (row.status === 'ok' ? 'ok' : row.status === 'critical' ? 'fail' : 'abstain')}><i></i>${row.status === 'ok' ? msText(row.latency_ms) : reasonText(row.reason || row.unknown_reason, row.detail) || t('status.' + row.status)}</span>
      <span class="tag">${row.depth}</span></div>
      <p class="small muted" style=${{ margin: '8px 0 0' }}>${t('ui.drawer.single')}</p></div>`;
  }
  return html`<div class="decide">${row.votes.map(v => html`<div class="drow"><b>${v.node}</b>
      <span class=${'vote ' + (v.vote === 'ok' ? 'ok' : v.vote === 'fail' ? 'fail' : 'abstain')}><i></i>${voteText(v)}</span>
      ${v.excluded ? html`<span class="tag weak">${t('ui.vote.excluded')}</span>` : html`<span></span>`}</div>`)}
    <div class="formula">${t('ui.drawer.formula', { fail: q.fail, ok: q.ok, healthy: q.healthy, n: q.n, q: q.q_eff })} <${Pill} status=${row.status} />
      ${q.degraded && html`<div class="small muted">${t('ui.mon.quorum_degraded', { healthy: q.healthy, n: q.n })}</div>`}</div>
  </div>`;
}

// whyLines explains in plain words whether this monitor alerts and why
// not — the part of the job users cannot see from the outside.
function whyLines(row, events) {
  const out = [];
  const add = (cls, text) => out.push({ cls, text });
  const inc = row.incident;
  const q = row.quorum;
  if (row.disabled) add('pending', t('ui.why.paused'));
  else if (row.status === 'maintenance') add('maint', t('ui.why.maint'));
  else if (row.status === 'unreachable') add('pending', t('ui.why.parent', { parent: (row.parents || []).join(', ') }));
  else if (row.status === 'unknown') {
    if (row.unknown_reason === 'local_network') add('maint', t('ui.why.local'));
    else add('pending', t('ui.why.unknown', { reason: reasonText(row.unknown_reason, row.detail) }));
  }
  if (row.flags.includes('partial') && row.status !== 'critical') add('warn', t('ui.why.partial', { quorum: quorumText(q) }));
  if (row.status === 'degraded') add('warn', t('ui.why.slow'));
  if (row.status === 'critical' && row.state_type === 'soft') add('warn', t('ui.why.soft', { attempt: row.attempt, max: row.max_attempts }));
  if (inc) {
    if (inc.acked) add('ok', t('ui.why.acked', { who: inc.acked_by || '?' }));
    if (inc.silenced_until && new Date(inc.silenced_until) > new Date()) add('maint', t('ui.why.silenced', { until: fmtTime(inc.silenced_until) }));
    // A policy without channels counts its notices but sends nothing.
    const opened = new Date(inc.opened_at);
    const noch = (events || []).find(e => e.data && e.data.msg === 'no_channels' && new Date(e.at) >= opened);
    if (noch) add('crit', t('ui.why.no_channels', { policy: noch.data.policy }));
    else if (inc.capped) add('warn', t('ui.why.capped', { max: inc.notify_max }));
    else if (inc.notify_count > 0) add('ok', t('ui.why.notified', { n: inc.notify_count, max: inc.notify_max || '∞' }));
    else if (!inc.acked && row.state_type !== 'soft') add('pending', t('ui.why.queued'));
  }
  if (!out.length) add('ok', row.status === 'ok' ? t('ui.why.ok') : t('ui.why.none'));
  return out;
}

function Drawer({ name, onClose }) {
  const d = useFetch(() => get('/monitors/' + enc(name)), [name]);
  const tl = useFetch(() => get(`/monitors/${enc(name)}/timeline?limit=12`), [name]);
  useLive(['state', 'incident'], d.reload, 600);
  useLive(['event'], tl.reload, 1000);
  const run = (f, msg) => async () => {
    try { await f(); if (msg) toast(msg); d.reload(); } catch (e) { toastError(e); }
  };
  const body = () => {
    if (!d.data) return d.error ? html`<${ErrorBox} error=${d.error} retry=${d.reload} />` : html`<${Loading} />`;
    const { row, record, path } = d.data;
    const inc = row.incident;
    return html`<div class="hd">
        <div class="row"><${Pill} status=${row.status} soft=${row.state_type === 'soft' && row.status !== 'ok'} />
          <span class="tag">${row.written_type}</span><span class="tag" title=${t('ui.depth.hint')}>${row.depth}</span>
          ${row.scope && html`<span class="tag">${row.scope}</span>`}
          <div class="spacer"></div><button class="btn sm" onClick=${onClose}>${t('ui.close')} Esc</button></div>
        <h1 class="ellipsis">${row.title}</h1>
        <div class="mono muted ltr ellipsis">${row.target}</div>
        ${row.status !== 'ok' && (row.reason || row.unknown_reason) && html`<div style=${{ marginTop: '8px' }}>${reasonText(row.reason || row.unknown_reason, row.detail)}</div>`}
        <div class="row" style=${{ marginTop: '12px' }}>
          <button class="btn sm primary" onClick=${run(() => post(`/monitors/${enc(name)}/check`), t('ui.mon.check_queued'))}>${t('ui.mon.check_now')}</button>
          ${inc && !inc.acked && html`<button class="btn sm" onClick=${run(() => post(`/incidents/${inc.id}/ack`, {}), '')}>${t('ui.inc.ack')}</button>`}
          ${inc && html`<button class="btn sm" onClick=${run(() => post(`/incidents/${inc.id}/silence`, { duration_s: 3600 }), t('ui.inc.silenced'))}>${t('ui.inc.silence')} 1h</button>`}
          <a class="btn sm" href=${'#/monitors/' + enc(name) + '/edit'} onClick=${onClose}>${t('ui.edit')}</a>
          <a class="btn sm" href=${'#/monitors/' + enc(name)} onClick=${onClose}>${t('ui.drawer.full')}</a>
        </div>
      </div>
      <div class="sec"><h4>${t('ui.drawer.decide')}</h4><${Decide} row=${row} /></div>
      <div class="sec"><h4>${t('ui.why.title')}</h4><ul class="why">${whyLines(row, tl.data && tl.data.items).map(l => html`<li class=${l.cls}>${l.text}</li>`)}</ul></div>
      ${path && html`<div class="sec"><h4>${t('ui.path.title')}</h4><${Path} row=${row} path=${path} bare /></div>`}
      <div class="sec"><h4>${t('ui.drawer.timeline')}</h4>${tl.data ? html`<${EventRows} items=${tl.data.items} hideMonitor />` : html`<${Loading} />`}</div>
      ${record && html`<div class="sec"><h4>${t('ui.drawer.record', { line: record.line })}</h4><pre class="code">${record.text}</pre></div>`}`;
  };
  return html`<div class="scrim" onClick=${onClose}></div>
    <aside class="drawer" role="dialog" aria-modal="true" aria-label=${name}>${body()}</aside>`;
}

// DrawerHost opens the drawer for openMonitor() calls and closes it on
// Escape or navigation.
export function DrawerHost() {
  const [name, setName] = useState(null);
  useEffect(() => {
    const open = (e) => setName(e.detail);
    const key = (e) => { if (e.key === 'Escape') setName(null); };
    const nav = () => setName(null);
    addEventListener('unagikuma:monitor', open);
    addEventListener('keydown', key);
    addEventListener('hashchange', nav);
    return () => { removeEventListener('unagikuma:monitor', open); removeEventListener('keydown', key); removeEventListener('hashchange', nav); };
  }, []);
  return name ? html`<${Drawer} name=${name} onClose=${() => setName(null)} />` : null;
}
