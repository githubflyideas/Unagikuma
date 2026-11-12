// Alerting: notification channels and the contact directory. Teams choose
// their channels (Settings > Teams).
import { html, useState } from '../lib.js';
import { t, has } from '../i18n.js';
import { get, post, del, put, ifMatch } from '../api.js';
import { ContactsTab, IssueDot, NewAddresses, addressKey } from './contacts.js';
import { useFetch, Loading, ErrorBox, Empty, Field, Modal, toast, toastError, errText, confirmDialog, useDocTitle, teamTitle } from '../ui.js';

// kindLabel is a kind's name in the reader's language where it has one.
function kindLabel(k) {
  const key = 'ui.ch.kind.' + k.kind, v = t(key);
  return v === key ? k.label : v;
}

function fieldLabel(k) {
  const key = 'ui.field.' + k;
  return has(key) ? t(key) : k;
}

// Sections of the preset menu, in the order they are shown.
const PRESET_GROUPS = ['chat', 'asia', 'incident', 'sms', 'whatsapp', 'push', 'email', 'automation', 'ru'];

// The e-mail options that only an SMTP server uses.
const SMTP_ONLY = ['host', 'port', 'username', 'password', 'tls'];

function ChannelForm({ kinds, initial, onClose, onSaved }) {
  const editing = !!initial;
  const [kind, setKind] = useState(initial ? (initial.opts.preset ? 'webhook:' + initial.opts.preset : initial.kind) : 'feishu');
  const [name, setName] = useState(initial ? initial.name : '');
  const [opts, setOpts] = useState(initial ? { ...initial.opts } : {});
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const [test, setTest] = useState(null);
  const [unknown, setUnknown] = useState(null); // addresses to offer for the directory
  const found = kinds.find(k => k.kind === kind);
  // E-mail goes to an SMTP server or through this machine's sendmail
  // command; each way shows only its own fields.
  const viaSendmail = kind === 'email' && opts.via === 'sendmail';
  const smsOrVoice = /-(sms|voice)$/.test(kind);
  const shown = (f) => kind !== 'email' || (viaSendmail ? !SMTP_ONLY.includes(f.key) : f.key !== 'program');
  // Any channel may be limited to a time period ("!name" = outside it).
  const info = { ...found, fields: [...((found && found.fields) || []).filter(shown), { key: 'period', type: 'text', placeholder: 'office-hours' }] };
  const required = (f) => f.required || (kind === 'email' && !viaSendmail && (f.key === 'host' || f.key === 'from'));
  const set = (k, v) => setOpts({ ...opts, [k]: v });
  const values = () => {
    const o = {};
    for (const f of info.fields) {
      const v = opts[f.key] !== undefined ? opts[f.key] : '';
      if (v !== '') o[f.key] = v;
    }
    if (opts.system === 'yes') o.system = 'yes';
    if (opts.report === 'no') o.report = 'no';
    return o;
  };
  // store saves the channel; then (optional) runs once it exists.
  const store = async (then) => {
    setBusy(true);
    setErr(null);
    try {
      await post('/channels', { name, kind, opts: values() }, ifMatch(initial && initial.etag));
      if (then) await then();
      toast(t('ui.saved'));
      onSaved();
    } catch (e2) { setErr(e2); }
    setBusy(false);
  };
  // Recipients must be in the contact directory: new addresses are
  // offered for it before saving (saving without is allowed, flagged).
  const save = async (e) => {
    e.preventDefault();
    const key = addressKey(kind);
    const v = key && values()[key];
    if (v && !v.includes('${')) {
      try {
        const r = await post('/people/lookup', { kind, list: v });
        if (r.unknown.length) { setUnknown({ kind: r.kind, addresses: r.unknown }); return; }
      } catch { /* the check is advice: save anyway */ }
    }
    store();
  };
  const runTest = async () => {
    setTest({ busy: true });
    try {
      const k = kind.startsWith('webhook:') ? 'webhook' : kind;
      const o = values();
      if (kind.startsWith('webhook:')) o.preset = kind.slice(8);
      setTest({ res: await post('/channels/test', editing && Object.values(o).includes('***') ? { name } : { name: name || 'test', kind: k, opts: o }) });
    } catch (e2) { setTest({ err: e2 }); }
  };
  return html`<${Modal} title=${editing ? t('ui.ch.edit', { name }) : t('ui.ch.add')} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      <div class="two">
        <${Field} label=${t('ui.ch.kind')}>
          <select value=${kind} onChange=${(e) => { setKind(e.target.value); setTest(null); }} disabled=${editing}>
            <optgroup label=${t('ui.ch.native')}>${kinds.filter(k => !k.preset).map(k => html`<option value=${k.kind}>${kindLabel(k)}</option>`)}</optgroup>
            ${PRESET_GROUPS.map(g => html`<optgroup label=${t('ui.ch.group.' + g)}>${kinds.filter(k => k.preset && k.group === g).sort((a, b) => a.label.localeCompare(b.label)).map(k => html`<option value=${k.kind}>${k.label}</option>`)}</optgroup>`)}
          </select><//>
        <${Field} label=${t('ui.name')}><input type="text" class="ltr" value=${name} onInput=${(e) => setName(e.target.value)} required disabled=${editing} pattern="[A-Za-z0-9][A-Za-z0-9_.\\-]{0,62}" /><//>
      </div>
      ${info.fields.map(f => {
        const v = opts[f.key] !== undefined ? opts[f.key] : (editing ? '' : f.default || '');
        const label = fieldLabel(f.key) + (required(f) ? ' *' : '');
        if (f.type === 'select') {
          return html`<${Field} label=${label}><select value=${v} onChange=${(e) => set(f.key, e.target.value)}>${['', ...(f.options || [])].map(o => html`<option value=${o}>${o || '–'}</option>`)}</select><//>`;
        }
        if (f.type === 'textarea') {
          return html`<${Field} label=${label} hint=${f.key === 'template' ? t('ui.ch.template_hint') : f.key === 'body' && kind.startsWith('custom-') ? t('ui.ch.custom_hint') : ''}><textarea class="ltr mono" rows="5" value=${f.key === 'template' ? v.replace(/\\n/g, '\n') : v} placeholder=${(f.placeholder || '').replace(/\\n/g, '\n')} onInput=${(e) => set(f.key, e.target.value)}></textarea><//>`;
        }
        const hint = f.type === 'secret' ? t('ui.ch.secret_hint') : f.type === 'email-list' ? t('ui.ch.email_list') : f.key === 'period' ? t('ui.ch.period_hint') : f.key === 'members' ? t('ui.ch.members_hint')
          : f.key === 'at' ? t('ui.ch.at_hint') : f.key === 'program' ? t('ui.ch.program_hint') : f.key === 'from' && viaSendmail ? t('ui.ch.from_sendmail_hint') : '';
        return html`<${Field} label=${label} hint=${hint}>
          <input type=${f.type === 'secret' ? 'password' : f.type === 'number' ? 'number' : 'text'} class="ltr" value=${v} required=${required(f) && !editing}
            placeholder=${f.type === 'secret' && editing ? '***' : f.placeholder || ''} autocomplete="off" onInput=${(e) => set(f.key, e.target.value)} /><//>`;
      })}
      <label class="switch"><input type="checkbox" checked=${opts.system === 'yes'} onChange=${(e) => set('system', e.target.checked ? 'yes' : '')} /> ${t('ui.ch.system')}</label>
      <p class="small muted" style=${{ margin: 0 }}>${t('ui.ch.system_hint')}</p>
      <label class="switch"><input type="checkbox" checked=${smsOrVoice ? opts.report === 'yes' : opts.report !== 'no'} onChange=${(e) => set('report', smsOrVoice ? (e.target.checked ? 'yes' : '') : (e.target.checked ? '' : 'no'))} /> ${t('ui.ch.takes_report')}</label>
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      ${test && !test.busy && html`<div class=${'banner ' + (test.err || !test.res.ok ? 'error' : (test.res.warning ? 'warning' : 'ok'))}>
        ${test.err ? errText(test.err) : test.res.ok
          ? (test.res.warning === 'connection_only_success' ? t('ui.ch.test_ok_connection') : t('ui.ch.test_ok'))
          : html`${t('ui.ch.test_failed')}: <span class="ltr">${test.res.error || (test.res.status ? 'HTTP ' + test.res.status : '')}</span>`}
        ${test.res && test.res.diagnostic && test.res.diagnostic.filtered > 0 && html`<div class="small" style=${{ marginTop: '6px' }}>${t('ui.ch.test_filtered', { n: test.res.diagnostic.filtered, kept: test.res.diagnostic.kept || 0 })}</div>`}
        ${test.res && test.res.diagnostic && test.res.diagnostic.issues && test.res.diagnostic.issues.length > 0 && html`<div class="small" style=${{ marginTop: '4px' }}>${test.res.diagnostic.issues.map(i => html`<div><span class="rdot"></span> <${IssueLine} i=${i} /></div>`)}</div>`}
      </div>`}
      ${initial && initial.issues && initial.issues.length > 0 && html`<div class="banner warning">${initial.issues.map(i => html`<div><span class="rdot"></span> <${IssueLine} i=${i} /></div>`)}</div>`}
      <div class="row">
        <button type="button" class="btn" onClick=${runTest} disabled=${test && test.busy}>${test && test.busy ? html`<span class="spin"></span>` : ''} ${t('ui.ch.test')}</button>
        <div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary" disabled=${busy}>${t('ui.save')}</button>
      </div>
    </form>
  <//>
  ${unknown && html`<${NewAddresses} kind=${unknown.kind} addresses=${unknown.addresses} onClose=${() => setUnknown(null)}
    onSkip=${() => { setUnknown(null); store(); }}
    onAdd=${(people) => { setUnknown(null); return store(async () => { for (const p of people) await post('/people', { ...p, notify: [name] }); }); }} />`}`;
}

function IssueLine({ i }) {
  return html`<span>${t('ui.dir.issue.' + i.code, { address: i.address || '', person: i.person ? '@' + i.person : '', n: i.count || 0 })}</span>`;
}

function ChannelsTab() {
  const list = useFetch(() => get('/channels'), []);
  const kinds = useFetch(() => get('/channel-kinds'), []);
  const [edit, setEdit] = useState(null);
  const remove = async (c) => {
    if (!await confirmDialog(t('ui.ch.delete_confirm', { name: c.name }), { danger: true, ok: t('ui.delete') })) return;
    try { await del('/channels/' + encodeURIComponent(c.name), ifMatch(c.etag)); list.reload(); } catch (e) { toastError(e); }
  };
  const test = async (c) => {
    try {
      const r = await post('/channels/test', { name: c.name });
      if (r.ok) toast(t('ui.ch.test_ok'));
      else {
        let msg = t('ui.ch.test_failed') + ': ' + (r.error || '');
        if (r.diagnostic && r.diagnostic.filtered > 0) msg += ' (' + t('ui.ch.test_filtered', { n: r.diagnostic.filtered, kept: r.diagnostic.kept || 0 }) + ')';
        toast(msg, 'error');
      }
    } catch (e) { toastError(e); }
  };
  if (!list.data || !kinds.data) return list.error ? html`<${ErrorBox} error=${list.error} />` : html`<${Loading} />`;
  return html`<div class="col" style=${{ gap: '12px' }}>
    <div class="row"><p class="muted small spacer" style=${{ margin: 0 }}>${t('ui.ch.intro')}</p><button class="btn primary" onClick=${() => setEdit({})}>${t('ui.ch.add')}</button></div>
    <div class="card"><div class="bd flush tbl-wrap">
      ${!list.data.length ? html`<${Empty} title=${t('ui.ch.none')}>${t('ui.ch.none_hint')}<//>` : html`<table class="tbl">
        <thead><tr><th>${t('ui.name')}</th><th>${t('ui.ch.kind')}</th><th>${t('ui.ch.teams')}</th><th></th></tr></thead>
        <tbody>${list.data.map(c => html`<tr>
          <td class="name ltr">${c.name} <${IssueDot} issues=${c.issues} />${c.system && html` <span class="tag">${t('ui.ch.system_tag')}</span>`}</td>
          <td>${kindLabel(kinds.data.find(k => k.kind === (c.opts.preset ? 'webhook:' + c.opts.preset : c.kind)) || { kind: c.kind, label: c.kind })}</td>
          <td class="small">${c.teams.length || c.used_by.length || c.system ? [...c.teams.map(n => teamTitle({ name: n })), ...c.used_by].join(', ') : html`<span class="warn-text">${t('ui.ch.unused')}</span>`}</td>
          <td class="right nowrap">
            <button class="btn sm" onClick=${() => test(c)}>${t('ui.ch.test')}</button>
            <button class="btn sm" onClick=${() => setEdit(c)}>${t('ui.edit')}</button>
            <button class="btn sm danger" onClick=${() => remove(c)}>${t('ui.delete')}</button>
          </td>
        </tr>`)}</tbody>
      </table>`}
    </div></div>
    ${edit && html`<${ChannelForm} kinds=${kinds.data} initial=${edit.name ? edit : null} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); list.reload(); }} />`}
  </div>`;
}

// ChannelList edits an ordered list of channels: a team's primary channel
// first, then its backups.
export function ChannelList({ value, channels, onChange }) {
  const toggle = (c) => onChange(value.includes(c) ? value.filter(x => x !== c) : [...value, c]);
  const move = (i, d) => { const ch = value.slice(); [ch[i], ch[i + d]] = [ch[i + d], ch[i]]; onChange(ch); };
  return html`<div class="col">
    ${value.map((c, i) => html`<div class="row nowrap"><b class="mono">${i + 1}.</b><span class="spacer ltr">${c}</span>
      <button type="button" class="btn sm ghost" disabled=${i === 0} onClick=${() => move(i, -1)} aria-label="↑">↑</button>
      <button type="button" class="btn sm ghost" disabled=${i === value.length - 1} onClick=${() => move(i, 1)} aria-label="↓">↓</button>
      <button type="button" class="btn sm ghost" onClick=${() => toggle(c)} aria-label=${t('ui.remove')}>✕</button></div>`)}
    <div class="chips">${channels.filter(c => !value.includes(c.name)).map(c => html`<button type="button" class="chip" onClick=${() => toggle(c.name)}>+ ${c.name}</button>`)}</div>
  </div>`;
}

export function Alerts({ tab }) {
  useDocTitle(t('ui.nav.alerts'));
  return html`<div class="page">
    <div class="tabs">
      <a href="#/alerts/channels" class=${tab === 'channels' ? 'on' : ''}>${t('ui.ch.tab')}</a>
      <a href="#/alerts/contacts" class=${tab === 'contacts' ? 'on' : ''}>${t('ui.dir.tab')}</a>
    </div>
    ${tab === 'contacts' ? html`<${ContactsTab} />` : html`<${ChannelsTab} />`}
  </div>`;
}

export { put };
