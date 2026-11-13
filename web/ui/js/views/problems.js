// Problems: open incidents split by whether someone is on them.
import { Expiry } from './expiry.js';
import { html, useState } from '../lib.js';
import { t, tn, dur, detailText } from '../i18n.js';
import { get, post, del } from '../api.js';
import { useFetch, useLive, Loading, ErrorBox, Empty, Ago, Dot, Modal, Field, toast, toastError, reasonText, enc, href, useDocTitle } from '../ui.js';

import { HistorySite } from './history-site.js';

// AckTrail lists every acknowledgement of an incident, oldest first: a
// second ACK adds a line instead of replacing the first one's note.
function AckTrail({ inc, tag, cls }) {
  const acks = inc.acks || [];
  if (!acks.length) return inc.ack_comment ? html`<${tag} class=${cls}>💬 ${inc.ack_comment}<//>` : null;
  return acks.map((a, i) => html`<${tag} key=${i} class=${cls}>
    ${a.action === 'unack' ? '↩' : '✓'} ${a.by} · <${Ago} at=${a.at} />${a.via === 'api' ? ' · API' : ''}${a.action === 'unack' ? ' · ' + t('ui.inc.unacked') : ''}${a.comment ? html` — 💬 ${a.comment}` : ''}
  <//>`);
}

const SILENCE = [[3600, '1h'], [4 * 3600, '4h'], [24 * 3600, '24h'], [7 * 24 * 3600, '7d']];

function AckDialog({ inc, onClose, onDone }) {
  const [comment, setComment] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    try {
      await post(`/incidents/${inc.id}/ack`, { comment });
      toast(t('ui.inc.acked'));
      onDone();
    } catch (e) { toastError(e); }
    setBusy(false);
  };
  return html`<${Modal} title=${t('ui.inc.ack_title', { title: inc.title })} onClose=${onClose} footer=${html`
      <button class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
      <button class="btn primary" disabled=${busy} onClick=${submit}>${t('ui.inc.ack')}</button>`}>
    <div class="form">
      <p class="muted small">${t('ui.inc.ack_hint')}</p>
      <${Field} label=${t('ui.inc.comment')}><textarea rows="3" value=${comment} onInput=${(e) => setComment(e.target.value)} placeholder=${t('ui.inc.comment_ph')}></textarea><//>
    </div>
  <//>`;
}

function SilenceMenu({ inc, onDone }) {
  const silence = async (s) => {
    try {
      await post(`/incidents/${inc.id}/silence`, { duration_s: s });
      toast(s ? t('ui.inc.silenced') : t('ui.inc.unsilenced'));
      onDone();
    } catch (e) { toastError(e); }
  };
  const until = inc.state.silenced_until && new Date(inc.state.silenced_until) > new Date();
  return html`<select class="btn sm" value="" onChange=${(e) => { const v = +e.target.value; e.target.value = ''; silence(v); }} aria-label=${t('ui.inc.silence')}>
    <option value="">${t('ui.inc.silence')}…</option>
    ${SILENCE.map(([s, label]) => html`<option value=${s}>${label}</option>`)}
    ${until && html`<option value="0">${t('ui.inc.unsilence')}</option>`}
  </select>`;
}

function statusOf(inc) {
  if (inc.state.resolved_at) return 'ok';
  return inc.state.low ? 'degraded' : 'critical';
}

export function IncidentList({ items, reload, compact, selectable, selected, setSelected, empty }) {
  const [acking, setAcking] = useState(null);
  if (!items.length) {
    if (empty) return html`<${Empty}>${empty}<//>`;
    return html`<${Empty} title=${t('ui.inc.none')}>${t('ui.inc.none_hint')}<//>`;
  }
  const unack = async (inc) => {
    try { await del(`/incidents/${inc.id}/ack`); reload(); } catch (e) { toastError(e); }
  };
  const toggle = (id) => {
    const s = new Set(selected);
    s.has(id) ? s.delete(id) : s.add(id);
    setSelected(s);
  };
  // The compact list fits a side column: a severity bar, what is wrong,
  // and the action, without table columns.
  if (compact) {
    return html`<div>${items.map(inc => {
      const st = inc.state;
      const open = !st.resolved_at;
      const link = inc.monitor && !inc.system ? href('/monitors/' + enc(inc.monitor)) : null;
      const meta = [open ? t('ui.inc.open_for') + ' ' + dur((Date.now() - new Date(st.opened_at)) / 1000) : '', tn('ui.inc.notified', st.notify_count || 0),
        st.acked ? t('ui.inc.acked_by', { who: st.acked_by }) : ''].filter(Boolean).join(' · ');
      return html`<div class="inc">
        <i class=${'sev ' + (inc.system ? 'sys' : statusOf(inc))}></i>
        <div class="body">
          ${link ? html`<a class="name" href=${link}>${inc.title}</a>` : html`<span class="name">${inc.title}</span>`}
          ${inc.global_id && html`<span class="tag ltr">${inc.global_id}</span>`}
 ${inc.history_only && html`<span class="tag warn">${t('ui.inc.archive')}</span>`}
 ${inc.system && html` <span class="tag">${t('ui.inc.system')}</span>`}
          ${inc.site && html` <span class="tag" title=${t('ui.inc.site_hint')}>${t('ui.inc.site', { site: inc.site })}</span>`}
          <p>${reasonText(inc.reason, inc.detail)}</p>
          <${AckTrail} inc=${inc} tag="p" />
          <p class="faint">${meta} · <a href=${href('/incidents/' + inc.id)}>${t('ui.inc.report')}</a></p>
        </div>
        ${open && html`<div class="acts">
          ${!st.acked ? html`<button class="btn sm primary" onClick=${() => setAcking(inc)}>${t('ui.inc.ack')}</button>`
            : html`<button class="btn sm" onClick=${() => unack(inc)}>${t('ui.inc.unack')}</button>`}
          <${SilenceMenu} inc=${inc} onDone=${reload} />
        </div>`}
      </div>`;
    })}
    ${acking && html`<${AckDialog} inc=${acking} onClose=${() => setAcking(null)} onDone=${() => { setAcking(null); reload(); }} />`}
    </div>`;
  }
  return html`<div class="tbl-wrap"><table class="tbl">
    <tbody>${items.map(inc => {
      const st = inc.state;
      const open = !st.resolved_at;
      const silenced = st.silenced_until && new Date(st.silenced_until) > new Date();
      const link = inc.monitor && !inc.system ? href('/monitors/' + enc(inc.monitor)) : null;
      return html`<tr class=${open ? statusOf(inc) : ''}>
        ${selectable && html`<td style=${{ width: '28px' }}><input type="checkbox" checked=${selected.has(inc.id)} onChange=${() => toggle(inc.id)} aria-label=${inc.title} /></td>`}
        <td style=${{ width: '18px' }}><${Dot} status=${statusOf(inc)} pulse=${open && !st.acked && !silenced} /></td>
        <td>
          <div class="row">${link ? html`<a class="name" href=${link}>${inc.title}</a>` : html`<span class="name">${inc.title}</span>`}
            ${inc.global_id && html`<span class="tag ltr">${inc.global_id}</span>`}
 ${inc.history_only && html`<span class="tag warn">${t('ui.inc.archive')}</span>`}
 ${inc.system && html`<span class="tag">${t('ui.inc.system')}</span>`}
            ${inc.site && html`<span class="tag" title=${t('ui.inc.site_hint')}>${t('ui.inc.site', { site: inc.site })}</span>`}
            ${st.acked && html`<span class="tag">${t('ui.inc.acked_by', { who: st.acked_by })}</span>`}
            ${silenced && html`<span class="tag">${t('ui.inc.silenced_until')} <${Ago} at=${st.silenced_until} /></span>`}
            ${st.capped && html`<span class="tag warn">${t('ui.inc.capped')}</span>`}
          </div>
          <div class="sub">${reasonText(inc.reason, inc.detail)}${inc.detail && inc.reason && inc.reason !== 'ok' ? html` · <span>${detailText(inc.detail)}</span>` : ''}</div>
          <${AckTrail} inc=${inc} tag="div" cls="sub" />
        </td>
        <td class="nowrap small muted hide-sm">
          ${open ? html`${t('ui.inc.open_for')} ${dur((Date.now() - new Date(st.opened_at)) / 1000)}` : html`${t('ui.inc.lasted')} ${dur((new Date(st.resolved_at) - new Date(st.opened_at)) / 1000)}`}
          <div>${tn('ui.inc.notified', st.notify_count || 0)}</div>
          <div><a href=${href('/incidents/' + inc.id)}>${t('ui.inc.report')}</a></div>
        </td>
        ${!compact || open ? html`<td class="right nowrap">
          ${open && !st.acked && html`<button class="btn sm primary" onClick=${() => setAcking(inc)}>${t('ui.inc.ack')}</button>`}
          ${open && st.acked && html`<button class="btn sm" onClick=${() => unack(inc)}>${t('ui.inc.unack')}</button>`}
          ${open && ' '}
          ${open && html`<${SilenceMenu} inc=${inc} onDone=${reload} />`}
        </td>` : html`<td></td>`}
      </tr>`;
    })}</tbody>
  </table>
  ${acking && html`<${AckDialog} inc=${acking} onClose=${() => setAcking(null)} onDone=${() => { setAcking(null); reload(); }} />`}
  </div>`;
}

// setupIssues lists what keeps alerts from reaching anyone: no channel,
// a default policy without channels, recipients missing from the contact
// directory, broken check scripts. They are problems too, so the Problems
// page lists them and its menu entry gets a red dot.
export function setupIssues(ov) {
  if (!ov) return [];
  const out = [];
  const nch = (ov.channels || []).length;
  if (!nch) out.push({ key: 'ui.cfgissue.no_channel', link: '#/alerts/channels', act: 'ui.cfgissue.add_channel' });
  else if (!ov.alerting_configured) out.push({ key: 'ui.cfgissue.no_policy', link: '#/advanced/teams', act: 'ui.cfgissue.fix' });
  if (ov.contact_issues) out.push({ key: 'ui.cfgissue.contacts', n: ov.contact_issues, link: '#/alerts/contacts', act: 'ui.cfgissue.fix' });
  if (ov.script_problems) out.push({ key: 'ui.cfgissue.scripts', n: ov.script_problems, link: '#/settings/scripts', act: 'ui.cfgissue.fix' });
  return out;
}

function SetupIssues() {
  const ov = useFetch(() => get('/overview'), []);
  useLive(['serial', 'directory'], ov.reload, 800);
  const list = setupIssues(ov.data);
  if (!list.length) return null;
  return html`<div class="card setup-issues"><div class="hd"><h3><span class="rdot"></span> ${t('ui.cfgissue.title')}</h3></div>
    <div class="bd flush">${list.map(x => html`<div class="row setup-issue">
      <span class="spacer">${x.n ? tn(x.key, x.n) : t(x.key)}</span>
      <a class="btn sm primary" href=${x.link}>${t(x.act)}</a>
    </div>`)}</div></div>`;
}

const TABS = [['unhandled', 'ui.inc.tab.unhandled'], ['handled', 'ui.inc.tab.handled'], ['resolved', 'ui.inc.tab.resolved'], ['all', 'ui.inc.tab.all'], ['expiry', 'ui.exp.title']];

const ProblemTabs = ({ tab }) => html`<div class="tabs">${TABS.map(([k, label]) => html`<a href=${'#/problems/' + k} class=${tab === k ? 'on' : ''}>${t(label)}</a>`)}</div>`;

// Certificates and domains that run out are problems in waiting, so their
// list sits beside the problems rather than in the monitors' toolbar.
export function ProblemsExpiry() {
  useDocTitle(t('ui.exp.title'));
  return html`<div class="page list"><${ProblemTabs} tab="expiry" /><${Expiry} embedded /></div>`;
}

export function Problems({ tab }) {
  useDocTitle(t('ui.nav.problems'));
  const [selected, setSelected] = useState(new Set());
 const [site,setSite]=useState('');
  const list = useFetch(() => get('/incidents?state=' + tab + '&limit=200&site='+encodeURIComponent(site)), [tab,site]);
  useLive(['incident', 'state'], list.reload, 800);
  const bulk = async (action) => {
    try {
      const r = await post('/incidents/bulk', { ids: [...selected], action, duration_s: 3600 });
      toast(tn('ui.inc.bulk_done', r.done));
      if (Object.keys(r.failed || {}).length) toastError({ message: detailText(Object.values(r.failed)[0]) });
      setSelected(new Set());
      list.reload();
    } catch (e) { toastError(e); }
  };
  const selectable = tab === 'unhandled' || tab === 'handled';
  return html`<div class="page list">
    ${(tab==='all'||tab==='resolved') && html`<${HistorySite} value=${site} onChange=${s=>{setSite(s);setSelected(new Set());}} />`}
    <${ProblemTabs} tab=${tab} />
    ${(tab === 'unhandled' || tab === 'all') && html`<${SetupIssues} />`}
    ${selectable && selected.size > 0 && html`<div class="row card" style=${{ padding: '8px 12px' }}>
      <span>${tn('ui.inc.selected', selected.size)}</span><div class="spacer"></div>
      <button class="btn sm primary" onClick=${() => bulk('ack')}>${t('ui.inc.ack')}</button>
      <button class="btn sm" onClick=${() => bulk('silence')}>${t('ui.inc.silence')} 1h</button>
    </div>`}
    <div class="card scroll"><div class="bd flush">
      <${ErrorBox} error=${list.error} retry=${list.reload} />
      ${list.data ? html`<${IncidentList} items=${list.data.items} reload=${list.reload} selectable=${selectable} selected=${selected} setSelected=${setSelected} />` : html`<${Loading} />`}
    </div></div>
  </div>`;
}
