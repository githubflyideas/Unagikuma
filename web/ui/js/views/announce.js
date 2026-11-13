// Notices and status: announced changes, the status pages and the daily
// status report: what is told to people, apart from the alerts themselves.
import { html, useState } from '../lib.js';
import { t } from '../i18n.js';
import { get, post, put, me } from '../api.js';
import { NoticesTab } from './notices.js';
import { LinkCard } from './appearance.js';
import { useFetch, Loading, ErrorBox, toast, toastError, useDocTitle } from '../ui.js';

// viaText names where a report goes: a team's channels or the system ones
// (a policy name only for a file written before teams had channels).
function viaText(policy) {
  if (!policy || policy === 'system' || policy === 'default') return t('ui.report.via_system');
  if (policy.startsWith('team:')) return t('ui.report.via_team', { team: policy.slice(5) });
  return policy;
}

// ReportTab previews each team's daily status report. Admins choose which
// teams receive one (the admin team's covers every monitor); people and
// channels can switch it off for themselves (Contacts, the channel form).
function ReportTab() {
  const [team, setTeam] = useState('admin');
  const rep = useFetch(() => get('/reports/daily?team=' + encodeURIComponent(team)), [team]);
  const [busy, setBusy] = useState(false);
  const send = async () => {
    setBusy(true);
    try { await post('/reports/daily/send', { team }); toast(t('ui.report.sent')); } catch (e) { toastError(e); }
    setBusy(false);
  };
  if (!rep.data) return rep.error ? html`<${ErrorBox} error=${rep.error} />` : html`<${Loading} />`;
  const r = rep.data;
  const teams = r.teams || [];
  const receives = teams.filter(x => x.receives).map(x => x.name);
  const setReceives = async (name, on) => {
    const next = teams.filter(x => (x.name === name ? on : x.receives)).map(x => x.name);
    try { await put('/tunables', { set: { 'notify.daily-report-teams': next.join(',') }, unset: [] }); rep.reload(); } catch (e) { toastError(e); }
  };
  const mine = teams.find(x => x.name === r.team);
  return html`<div class="col" style=${{ gap: '12px' }}>
    <div class="row"><p class="muted small spacer" style=${{ margin: 0 }}>${r.enabled
      ? t('ui.report.intro', { at: r.at, via: viaText(r.policy) })
      : html`<span class="warn-text">${t('ui.report.off', { where: t('ui.nav.advanced') + ' › ' + t('ui.set.tab.tunables') })}</span>`}</p>
      <button class="btn" onClick=${rep.reload}>${t('ui.report.refresh')}</button>
      <button class="btn primary" onClick=${send} disabled=${busy || !(mine && mine.receives)}>${t('ui.report.send')}</button></div>
    ${teams.length > 1 && html`<div class="row" style=${{ gap: '8px', flexWrap: 'wrap' }}>
      <label class="small">${t('ui.report.team')} <select value=${team} onChange=${(e) => setTeam(e.target.value)}>
        ${teams.map(x => html`<option value=${x.name}>${x.title}</option>`)}</select></label>
      ${me.role === 'admin' && mine && html`<label class="switch"><input type="checkbox" checked=${mine.receives} onChange=${(e) => setReceives(mine.name, e.target.checked)} /> ${t('ui.report.team_receives')}</label>`}
      <span class="small muted spacer">${receives.length ? t('ui.report.receiving', { teams: teams.filter(x => x.receives).map(x => x.title).join(', ') }) : t('ui.report.receiving_none')}</span>
    </div>`}
    <p class="small muted" style=${{ margin: 0 }}>${t('ui.report.opt_out_hint')}</p>
    <div class="card"><div class="hd"><h3 class=${r.ok ? '' : 'crit-text'}>${r.title}</h3></div>
      <div class="bd"><pre class="report">${r.body}</pre></div></div>
    <${LinkCard} page="summary" k="sum" share=${false} />
  </div>`;
}

// ScreensTab opens or shares the on-duty wall screen. The managers'
// summary is a page of the console (/summary), not something to cast.
function ScreensTab() {
  return html`<div class="links2"><${LinkCard} page="wall" k="wall" /></div>`;
}

// Change notices went to Alerts and notifications; this page is about
// what the public and the duty room see.
export const NOTICE_TABS = [['pages', 'ui.notice.tab'], ['report', 'ui.report.tab'], ['screens', 'ui.notice.screens_tab']];

export function Notices({ tab }) {
  useDocTitle(t('ui.nav.notices'));
  return html`<div class="page">
    <div class="tabs">${NOTICE_TABS.map(([k, label]) => html`<a href=${'#/notices/' + k} class=${tab === k ? 'on' : ''}>${t(label)}</a>`)}</div>
    ${tab === 'report' ? html`<${ReportTab} />` : tab === 'screens' ? html`<${ScreensTab} />` : html`<${NoticesTab} />`}
  </div>`;
}
