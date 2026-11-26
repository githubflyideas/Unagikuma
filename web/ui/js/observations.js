// Source-owned observations never decide another site's alert state.
import { html } from './lib.js';
import { t, fmtTime } from './i18n.js';
import { Card, Dot, msText, reasonText } from './ui.js';
export function observationSummary(snapshot) {
 const rows = snapshot?.rows || [];
 return rows.map(r => r.node_id + ' ' + (r.stale ? '⊘' : r.status === 'up' ? '✓' : r.status === 'down' ? '✗' : '?')).join(', ');
}
// ObservationMarks is the compact form for a list row: each site with
// ✓ (normal), ✗ (failing) or ⊘ (no recent word), coloured.
// A long site name is cut short (the full one is in the tooltip); with
// more than MARKS_MAX sites the row shows how many see what instead.
const MARKS_MAX = 4;
const markOf = (r) => r.stale ? '⊘' : r.status === 'up' ? '✓' : r.status === 'down' ? '✗' : '?';
const clsOf = (r) => r.stale ? 'muted' : r.status === 'up' ? 'ok-text' : r.status === 'down' ? 'err-text' : 'muted';
export function ObservationMarks({ snapshot }) {
 const rows = snapshot?.rows || [];
 if (rows.length > MARKS_MAX) {
   const n = (m) => rows.filter(r => markOf(r) === m).length;
   return html`<span class="tag obs nowrap" title=${observationSummary(snapshot)}>${['✓', '✗', '⊘'].filter(m => n(m)).map((m, i) =>
     html`${i > 0 ? ' ' : ''}<b class=${m === '✓' ? 'ok-text' : m === '✗' ? 'err-text' : 'muted'}>${m}</b>${n(m)}`)}</span>`;
 }
 return html`<span class="tag obs nowrap" title=${observationSummary(snapshot)}>${rows.map((r, i) =>
   html`${i > 0 ? ' ' : ''}<span class="obs-site">${r.node_id}</span> <b class=${clsOf(r)}>${markOf(r)}</b>`)}</span>`;
}
export function ObservationCell({ value: r }) {
 if (!r) return html`<span class="muted">?</span>`;
 const status = r.stale ? 'unknown' : r.status === 'up' ? 'ok' : r.status === 'down' ? 'critical' : 'unknown';
 return html`<span class="nowrap" title=${(r.epoch || '') + ':' + (r.seq || 0)}><${Dot} status=${status} /> ${t('status.' + status)} ${r.stale && html`<span class="tag weak">${t('ui.obs.stale')}</span>`}</span>`;
}
export function ObservationTable({ snapshot }) {
 if (!snapshot?.rows?.length) return null;
 return html`<${Card} title=${t('ui.mon.votes')}><p class="small muted">${t('ui.node.intro')}</p><p class="mono">${observationSummary(snapshot)}</p>
 <table class="tbl"><tbody>${snapshot.rows.map(r => html`<tr>
 <td class="ltr" style=${{ wordBreak: 'break-all' }}>${r.node_id}</td><td><${ObservationCell} value=${r} /></td>
 <td class="small">${r.checked_at && !r.checked_at.startsWith('0001-') ? fmtTime(r.checked_at) : '—'}</td>
 <td class="right">${r.status === 'up' ? msText(r.latency_ms) : reasonText(r.error || '')}</td>
 </tr>`)}</tbody></table><//>`;
}
