// Source-owned observations never decide another site's alert state.
import { html } from './lib.js';
import { t, fmtTime } from './i18n.js';
import { Card, Dot, msText, reasonText } from './ui.js';
export function observationSummary(snapshot) {
 const rows = snapshot?.rows || [];
 return rows.map(r => r.node_id + ' ' + (r.stale ? '~' : r.status === 'up' ? '○' : r.status === 'down' ? '×' : '?')).join(', ') + ', ' + rows.filter(r => !r.stale && r.status === 'up').length + '/' + rows.length;
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
 <td>${r.node_id}</td><td><${ObservationCell} value=${r} /></td>
 <td class="small">${r.checked_at && !r.checked_at.startsWith('0001-') ? fmtTime(r.checked_at) : '—'}</td>
 <td class="right">${r.status === 'up' ? msText(r.latency_ms) : reasonText(r.error || '')}</td>
 </tr>`)}</tbody></table><//>`;
}
