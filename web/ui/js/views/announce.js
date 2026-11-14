// Notices and status: announced changes, the status pages and the daily
// status report: what is told to people, apart from the alerts themselves.
import { html, useState } from '../lib.js';
import { t } from '../i18n.js';
import { get, post, put, me, ifMatch } from '../api.js';
import { NoticesTab } from './notices.js';
import { useFetch, Loading, ErrorBox, toast, toastError, useDocTitle, Copy, Field } from '../ui.js';

// viaText names where a report goes: a team's channels or the system ones
// (a policy name only for a file written before teams had channels).
function viaText(policy) {
  if (!policy || policy === 'system' || policy === 'default') return t('ui.report.via_system');
  if (policy.startsWith('team:')) return t('ui.report.via_team', { team: policy.slice(5) });
  return policy;
}

// ReportSettings sets when the report goes out, to which teams, and
// through which of a team's channels; a person can still switch it off for
// themselves in the user list.
function ReportSettings({ r, onChange }) {
  const tun = useFetch(() => get('/tunables'), []);
  const tms = useFetch(() => get('/teams'), []);
  const chs = useFetch(() => get('/channels'), []);
  if (!tun.data || !tms.data || !chs.data) return null;
  const val = (k) => (tun.data.find(x => x.key === k) || {}).value || '';
  const setTun = async (set) => {
    try { await put('/tunables', { set, unset: [] }); tun.reload(); onChange(); } catch (e) { toastError(e); }
  };
  const teams = r.teams || [];
  const receives = teams.filter(x => x.receives).map(x => x.name);
  const setReceives = (name, on) => setTun({ 'notify.daily-report-teams': teams.filter(x => (x.name === name ? on : x.receives)).map(x => x.name).join(',') });
  const team = (tms.data.items || []).find(x => x.name === r.team);
  const chans = ((team && team.channels) || []).map(n => chs.data.find(c => c.name === n)).filter(Boolean);
  const takes = (c) => /-(sms|voice)$/.test(c.kind) ? c.opts.report === 'yes' : c.opts.report !== 'no';
  const setTakes = async (c, on) => {
    const opts = { ...c.opts };
    if (/-(sms|voice)$/.test(c.kind)) { if (on) opts.report = 'yes'; else delete opts.report; } else if (on) delete opts.report; else opts.report = 'no';
    try {
      await post('/channels', { name: c.name, kind: c.opts.preset ? 'webhook:' + c.opts.preset : c.kind, opts }, ifMatch(c.etag));
      chs.reload(); onChange();
    } catch (e) { toastError(e); }
  };
  return html`<div class="card"><div class="hd"><h3>${t('ui.report.settings')}</h3></div><div class="bd form">
    <div class="row" style=${{ gap: '16px', flexWrap: 'wrap' }}>
      <label class="switch"><input type="checkbox" checked=${val('notify.daily-report') !== 'off'} onChange=${(e) => setTun({ 'notify.daily-report': e.target.checked ? 'on' : 'off' })} /> ${t('ui.report.enabled')}</label>
      <label class="small">${t('ui.report.at')} <input type="time" value=${val('notify.daily-report-at')} onChange=${(e) => e.target.value && setTun({ 'notify.daily-report-at': e.target.value })} style=${{ width: 'auto' }} /></label>
    </div>
    <${Field} label=${t('ui.report.teams')}>
      <div class="row" style=${{ gap: '14px', flexWrap: 'wrap' }}>${teams.map(x => html`<label class="switch"><input type="checkbox" checked=${receives.includes(x.name)} onChange=${(e) => setReceives(x.name, e.target.checked)} /> ${x.title}</label>`)}</div><//>
    <${Field} label=${t('ui.report.channels', { team: (teams.find(x => x.name === r.team) || {}).title || r.team })} hint=${chans.length ? t('ui.report.channels_hint') : ''}>
      ${chans.length ? html`<div class="row" style=${{ gap: '14px', flexWrap: 'wrap' }}>${chans.map(c => html`<label class="switch"><input type="checkbox" checked=${takes(c)} onChange=${(e) => setTakes(c, e.target.checked)} /> <span class="ltr">${c.name}</span></label>`)}</div>`
        : html`<span class="small muted">${t('ui.report.channels_none')}</span>`}<//>
  </div></div>`;
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
    ${me.role === 'admin' && html`<${ReportSettings} r=${r} onChange=${rep.reload} />`}
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

// LinkCard opens a read-only page (the wall screen or the managers'
// summary) here, or, with share, makes a link with a read-only token for
// someone or something that does not sign in.
export function LinkCard({ page, k, share = true }) {
  const [link, setLink] = useState('');
  const make = async () => {
    try {
      const day = new Date().toISOString().slice(0, 10);
      const r = await post('/tokens', { name: t('ui.look.' + k + '_token', { date: day }), scopes: ['read'] });
      setLink(location.origin + '/' + page + '#token=' + encodeURIComponent(r.token));
    } catch (err) { toastError(err); }
  };
  return html`<div class="card"><div class="hd"><h2>${t('ui.look.' + k)}</h2></div><div class="bd col" style=${{ gap: '10px' }}>
    <p class="small muted" style=${{ margin: 0 }}>${t('ui.look.' + k + '_hint')}</p>
    <div class="row">
      <a class="btn" href=${'/' + page} target="_blank" rel="noopener">${t('ui.look.' + k + '_open')}</a>
      ${share && html`<button class="btn" onClick=${make}>${t('ui.look.' + k + '_link')}</button>`}
    </div>
    ${link && html`<div class="banner ok"><div class="spacer col" style=${{ gap: '6px' }}>
      <span>${t('ui.look.' + k + '_link_done')}</span><code class="ltr small" style=${{ wordBreak: 'break-all' }}>${link}</code></div><${Copy} text=${link} /></div>`}
  </div></div>`;
}
