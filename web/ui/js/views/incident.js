// Incident report: the story of one incident in four parts, whether
// monitoring itself was running, and what the check covers. The text is
// written by the server in the reader's language; this page lays it out
// and offers it as a file to keep.
import { html } from '../lib.js';
import { t, currentLang, fmtTime } from '../i18n.js';
import { get } from '../api.js';
import { useFetch, Loading, ErrorBox, useDocTitle } from '../ui.js';

export function IncidentReport({ id }) {
  useDocTitle(t('ui.inc.report') + ' #' + id);
  const lang = encodeURIComponent(currentLang());
  const d = useFetch(() => get(`/incidents/${encodeURIComponent(id)}/report?lang=${lang}`), [id]);
  if (!d.data) return html`<div class="page">${d.error ? html`<${ErrorBox} error=${d.error} retry=${d.reload} />` : html`<${Loading} />`}</div>`;
  const r = d.data;
  const file = (f) => `/api/v1/incidents/${encodeURIComponent(id)}/report?format=${f}&download=1&lang=${lang}`;
  const at = (s) => s && !s.startsWith('0001-') ? fmtTime(s) : '—';
  const facts = [
    ['pm.f.failed', r.failed_at], ['pm.f.detected', r.detected_at], ['pm.f.notified', r.notified_at],
    ['pm.f.acked', r.acked_at], ['pm.f.resolved', r.resolved_at],
  ];
  return html`<div class="page pm">
    <div class="row"><a href="#/problems/all" class="muted">← ${t('ui.nav.problems')}</a></div>
    <div class="row wrap">
      <h1 class="spacer">${t('pm.title', { id:r.incident.global_id||id, name: r.incident.monitor || r.incident.title })}</h1>
      <a class="btn" href=${file('md')} download>${t('ui.pm.download_md')}</a>
      <a class="btn" href=${file('html')} download>${t('ui.pm.download_html')}</a>
      <a class="btn" href=${file('json')} download>${t('ui.pm.download_json')}</a>
    </div>
    ${r.incident.history_only && html`<p class="warn-text">${t('ui.inc.archive')}</p>`}
 <div class="card"><div class="bd"><p class="pm-concl">${r.conclusion}</p></div></div>
    <div class="card"><div class="bd flush tbl-wrap"><table class="tbl">
      <tbody>${facts.map(([k, v]) => html`<tr><td class="muted">${t(k)}</td><td>${at(v)}</td></tr>`)}
      ${(r.recipients || []).length > 0 && html`<tr><td class="muted">${t('pm.f.recipients')}</td><td>${r.recipients.join(', ')}</td></tr>`}</tbody>
    </table></div></div>
    ${r.segments.map((s, i) => html`<div class="card"><div class="hd"><h3>${i + 1}. ${s.title}</h3></div>
      <div class="bd"><ul class="pm-list">${s.entries.map(e => html`<li class=${e.mark === 'warn' ? 'warn-text' : ''}>
        ${e.at && !e.at.startsWith('0001-') ? html`<time class="muted small">${fmtTime(e.at)}</time> ` : ''}${e.what}</li>`)}</ul></div></div>`)}
    ${[['pm.liveness', r.liveness_text], ['pm.coverage', r.coverage_text]].map(([k, lines]) => html`<div class="card"><div class="hd"><h3>${t(k)}</h3></div>
      <div class="bd"><ul class="pm-list">${(lines || []).map(l => html`<li>${l}</li>`)}</ul></div></div>`)}
  </div>`;
}
