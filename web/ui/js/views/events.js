// Event log and notification log.
import { html, useState } from '../lib.js';
import { t, fmtTime } from '../i18n.js';
import { get, qs, me } from '../api.js';
import { useFetch, useLive, Loading, ErrorBox, Empty, Dot, msText, enc, href, useDocTitle } from '../ui.js';

import { HistorySite } from './history-site.js';

const SEV = { critical: 'critical', warning: 'degraded', info: 'ok', ok: 'ok' };
const TYPES = ['STATE', 'INCIDENT', 'NOTIFY', 'ACK', 'SILENCE', 'CONFIG', 'SYSTEM', 'PARTIAL', 'DRILL'];

// eventText shows an event in the UI language from the code it was
// recorded with; events written before codes existed keep their text.
export function eventText(e) {
  const d = e.data || {};
  if (!d.msg) return e.message;
  if (d.msg === 'state') return t('status.' + d.from) + ' → ' + t('status.' + d.to);
  const kind = d.kind ? t('ev.kind.' + d.kind) : '';
  const params = { ...d, kind: kind === 'ev.kind.' + d.kind ? d.kind : kind };
  // System texts that take no arguments share the notification wording.
  for (const k of ['ev.' + d.msg, 'sys.' + d.msg]) {
    const s = t(k, params);
    if (s !== k) return s;
  }
  return e.message;
}

export function EventRows({ items, hideMonitor }) {
  if (!items.length) return html`<${Empty}>${t('ui.events.none')}<//>`;
  return html`<div class="tbl-wrap"><table class="tbl"><tbody>${items.map(e => html`<tr>
    <td style=${{ width: '18px' }}><${Dot} status=${SEV[e.severity] || 'pending'} /></td>
    <td class="nowrap small muted">${fmtTime(e.at)}</td>
    <td class="nowrap"><span class="tag">${e.type}</span></td>
    ${!hideMonitor && html`<td class="nowrap">${e.monitor ? html`<a href=${href('/monitors/' + enc(e.monitor))}>${e.monitor}</a>` : ''}</td>`}
    <td>${e.site && html`<span class="tag">${e.site}</span>`} ${eventText(e)}</td>
  </tr>`)}</tbody></table></div>`;
}

function EventsTab() {
 const [site,setSite]=useState('');
  const [type, setType] = useState('');
  const [sev, setSev] = useState('');
  const [monitor, setMonitor] = useState('');
  const [pages, setPages] = useState([]);
  const first = useFetch(() => get('/events' + qs({ site,type, severity: sev, monitor, limit: 100 })), [site,type, sev, monitor]);
  useLive(['event'], first.reload, 1000);
  const more = async () => {
    const last = pages.length ? pages[pages.length - 1] : first.data;
    if (!last || !last.next_cursor) return;
    const p = await get('/events' + qs({ site,type, severity: sev, monitor, limit: 100, before: last.next_cursor }));
    setPages([...pages, p]);
  };
  const items = first.data ? [first.data, ...pages].flatMap(p => p.items) : [];
  const last = pages.length ? pages[pages.length - 1] : first.data;
  return html`<div class="col" style=${{ gap: '12px' }}>
    <div class="row">
      <${HistorySite} value=${site} onChange=${s=>{setPages([]);setSite(s);}} />
 <select style=${{ width: 'auto' }} value=${type} onChange=${(e) => { setPages([]); setType(e.target.value); }} aria-label=${t('ui.type')}>
        <option value="">${t('ui.events.all_types')}</option>${TYPES.map(x => html`<option value=${x}>${x}</option>`)}
      </select>
      <select style=${{ width: 'auto' }} value=${sev} onChange=${(e) => { setPages([]); setSev(e.target.value); }} aria-label=${t('ui.events.severity')}>
        <option value="">${t('ui.events.all_severities')}</option>
        ${['critical', 'warning', 'info'].map(x => html`<option value=${x}>${t('ui.sev.' + x)}</option>`)}
      </select>
      <input type="search" style=${{ maxWidth: '240px' }} placeholder=${t('ui.monitor')} value=${monitor} onChange=${(e) => { setPages([]); setMonitor(e.target.value.trim()); }} />
    </div>
    <div class="card scroll"><div class="bd flush">
      <${ErrorBox} error=${first.error} retry=${first.reload} />
      ${first.data ? html`<${EventRows} items=${items} />` : html`<${Loading} />`}
    </div></div>
    ${last && last.next_cursor && html`<div class="row"><button class="btn" onClick=${more}>${t('ui.more')}</button></div>`}
  </div>`;
}

function NotificationsTab() {
 const [site,setSite]=useState('');
  const [filter, setFilter] = useState('');
  const list = useFetch(() => get('/notifications' + qs({site,filter, limit: 100 })), [site,filter]);
  useLive(['event'], list.reload, 1500);
  return html`<div class="col" style=${{ gap: '12px' }}>
    <${HistorySite} value=${site} onChange=${setSite} />
 <div class="chips">${['', 'failed', 'fallback', 'drill'].map(f => html`<button class=${'chip' + (filter === f ? ' on' : '')} onClick=${() => setFilter(f)}>${t('ui.notif.filter.' + (f || 'all'))}</button>`)}</div>
    <div class="card scroll"><div class="bd flush tbl-wrap">
      <${ErrorBox} error=${list.error} retry=${list.reload} />
      ${!list.data ? html`<${Loading} />` : !list.data.items.length ? html`<${Empty}>${t('ui.notif.none')}<//>` : html`<table class="tbl">
        <thead><tr><th></th><th>${t('ui.time')}</th><th>${t('ui.notif.kind')}</th><th>${t('ui.notif.attempts')}</th></tr></thead>
        <tbody>${list.data.items.map(d => html`<tr>
          <td style=${{ width: '18px' }}><${Dot} status=${d.ok ? 'ok' : 'critical'} /></td>
          <td class="nowrap small">${fmtTime(d.at)}${d.delayed_s > 0 ? html`<div class="sub">${t('ui.notif.delayed')}</div>` : ''}</td>
          <td class="nowrap">${d.site && html`<span class="tag">${d.site}</span>`} <span class="tag">${t('msg.kind.' + d.kind)}</span>${d.seq ? html` <span class="small muted">#${d.seq}</span>` : ''}
            ${d.fallback && html` <span class="tag warn">${t('ui.notif.fallback')}</span>`}${d.drill && html` <span class="tag">${t('ui.notif.drill')}</span>`}
            ${d.incident_id ? html`<div class="sub">${t('ui.notif.incident', { id: d.incident_id })}</div>` : ''}</td>
          <td>${(d.attempts || []).map(a => html`<div class="small">
            <span class=${a.ok ? 'ok-text' : 'err-text'}>${a.ok ? '✓' : '✗'}</span> <b>${a.channel}</b>
            <span class="muted">${msText(a.latency_ms)}${a.status ? ' · HTTP ' + a.status : ''}</span>
            ${a.error && html`<div class="err-text ltr">${a.error}</div>`}
          </div>`)}</td>
        </tr>`)}</tbody>
      </table>`}
    </div></div>
  </div>`;
}

export function Events({ tab }) {
  useDocTitle(t('ui.nav.events'));
  return html`<div class="page list">
    <div class="tabs">
      <a href="#/events/events" class=${tab === 'events' ? 'on' : ''}>${t('ui.events.tab')}</a>
      ${!me.teams && html`<a href="#/events/notifications" class=${tab === 'notifications' ? 'on' : ''}>${t('ui.notif.tab')}</a>`}
    </div>
    ${tab === 'notifications' && !me.teams ? html`<${NotificationsTab} />` : html`<${EventsTab} />`}
  </div>`;
}
