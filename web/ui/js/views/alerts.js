// Alerting: notification channels and the contact directory. Teams choose
// their channels (Settings > Teams).
import { html, useState, useEffect, useRef } from '../lib.js';
import { t, has, currentLang } from '../i18n.js';
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

// Sections of the built-in kinds. A kind not listed goes to the last one.
const NATIVE_GROUPS = [
  ['im', ['feishu', 'dingtalk', 'wecom', 'telegram', 'slack', 'discord']],
  ['email', ['email']],
  ['phone', ['aliyun-sms', 'aliyun-voice', 'tencent-sms', 'tencent-voice', 'twilio-sms', 'twilio-voice', 'custom-sms', 'custom-voice']],
  ['other', ['webhook', 'group', 'log']],
];
function nativeGroups(kinds) {
  const native = kinds.filter(k => !k.preset);
  const listed = new Set(NATIVE_GROUPS.flatMap(g => g[1]));
  return NATIVE_GROUPS.map(([g, names], i) => [g, native.filter(k => names.includes(k.kind) || (i === NATIVE_GROUPS.length - 1 && !listed.has(k.kind)))
    .sort((a, b) => (names.indexOf(a.kind) + 1 || 99) - (names.indexOf(b.kind) + 1 || 99))]);
}

// The names a message template can use, in the order offered.
const TEMPLATE_NAMES = ['Icon', 'KindText', 'Name', 'StatusText', 'ReasonText', 'Detail', 'Target', 'DurationText', 'Started', 'Site', 'Instance', 'DetailURL', 'AckURL'];
// Ready-made templates; the empty one is the built-in text.
const TEMPLATES = {
  short: '{{.Icon}} {{.Name}} {{.StatusText}}\n{{.ReasonText}}',
  detailed: '{{.Icon}} {{.KindText}}: {{.Name}} {{.StatusText}}\n{{.ReasonText}} — {{.Detail}}\n{{.Target}}\n{{.Started}} · {{.DurationText}}\n{{.DetailURL}}',
  sms: '{{.KindText}} {{.Name}} {{.StatusText}} {{.Started}}',
};

// TemplateField edits a message template: names are inserted at the
// cursor, a ready-made template can be picked, and the result for an
// example problem is shown as it is typed.
function TemplateField({ label, value, onChange }) {
  const ref = useRef(null);
  const [preview, setPreview] = useState(null);
  useEffect(() => {
    const id = setTimeout(async () => {
      try { setPreview(await post('/channels/preview', { template: value, lang: currentLang() })); } catch (e) { setPreview({ ok: false, error: errText(e) }); }
    }, 350);
    return () => clearTimeout(id);
  }, [value]);
  const insert = (name) => {
    const el = ref.current, tok = '{{.' + name + '}}';
    if (!el) return onChange(value + tok);
    const a = el.selectionStart ?? value.length, b = el.selectionEnd ?? value.length;
    onChange(value.slice(0, a) + tok + value.slice(b));
    requestAnimationFrame(() => { el.focus(); el.selectionStart = el.selectionEnd = a + tok.length; });
  };
  const preset = Object.keys(TEMPLATES).find(k => TEMPLATES[k].replace(/\\n/g, '\n') === value) || (value ? 'custom' : '');
  return html`<${Field} label=${label} hint=${t('ui.ch.tpl_hint')}>
    <div class="col" style=${{ gap: '8px' }}>
      <select value=${preset} onChange=${(e) => e.target.value !== 'custom' && onChange((TEMPLATES[e.target.value] || '').replace(/\\n/g, '\n'))} aria-label=${t('ui.ch.tpl_pick')}>
        <option value="">${t('ui.ch.tpl.builtin')}</option>
        ${Object.keys(TEMPLATES).map(k => html`<option value=${k}>${t('ui.ch.tpl.' + k)}</option>`)}
        ${preset === 'custom' && html`<option value="custom">${t('ui.ch.tpl.custom')}</option>`}
      </select>
      <div class="chips">${TEMPLATE_NAMES.map(n => html`<button type="button" class="btn sm" title=${'{{.' + n + '}}'} onClick=${() => insert(n)}>${t('ui.ch.var.' + n)}</button>`)}</div>
      <textarea ref=${ref} class="ltr mono" rows="5" value=${value} placeholder=${t('ui.ch.tpl_placeholder')} onInput=${(e) => onChange(e.target.value)}></textarea>
      ${preview && html`<div class=${'tpl-preview' + (preview.ok ? '' : ' err')}>
        <div class="small muted">${t('ui.ch.tpl_preview')}</div>
        ${preview.ok ? html`<b>${preview.title}</b><pre>${preview.body}</pre>` : html`<span class="err-text small">${preview.error}</span>`}
      </div>`}
    </div><//>`;
}

// AtField chooses whom a chat robot mentions on a problem: everyone, or
// people from the contact directory by their phone number.
function AtField({ label, kind, value, onChange, people }) {
  const parts = value.split(',').map(x => x.trim()).filter(Boolean);
  const all = parts.includes('all');
  const toggleAll = (on) => onChange((on ? ['all', ...parts.filter(x => x !== 'all')] : parts.filter(x => x !== 'all')).join(','));
  const add = (phone) => phone && !parts.includes(phone) && onChange([...parts, phone].join(','));
  const byPhone = kind === 'dingtalk' || kind === 'wecom';
  const pick = byPhone ? (people || []).filter(p => p.status === 'active').flatMap(p => (p.phones || []).map(ph => ({ name: p.name, phone: ph }))) : [];
  const nameOf = (x) => { const m = pick.find(p => p.phone === x); return m ? m.name : x; };
  return html`<${Field} label=${label} hint=${byPhone ? t('ui.ch.at_hint2') : t('ui.ch.at_hint_feishu')}>
    <div class="col" style=${{ gap: '8px' }}>
      <div class="row" style=${{ gap: '12px', flexWrap: 'wrap' }}>
        <label class="switch"><input type="checkbox" checked=${all} onChange=${(e) => toggleAll(e.target.checked)} /> ${t('ui.ch.at_all')}</label>
        ${byPhone && html`<select value="" onChange=${(e) => add(e.target.value)} aria-label=${t('ui.ch.at_pick')} style=${{ width: 'auto' }}>
          <option value="">${pick.length ? t('ui.ch.at_pick') : t('ui.ch.at_pick_none')}</option>
          ${pick.map(p => html`<option value=${p.phone} disabled=${parts.includes(p.phone)}>${p.name} · ${p.phone}</option>`)}</select>`}
      </div>
      ${parts.filter(x => x !== 'all').length > 0 && html`<div class="chips">${parts.filter(x => x !== 'all').map(x => html`<span class="tag">${nameOf(x)}${nameOf(x) !== x ? html` <span class="ltr">${x}</span>` : ''}
        <button type="button" class="linkbtn" aria-label=${t('ui.delete')} onClick=${() => onChange(parts.filter(y => y !== x).join(','))}>×</button></span>`)}</div>`}
      <input type="text" class="ltr" value=${value} placeholder=${byPhone ? 'all | +8613800000000' : 'all | ou_xxxxxxxx'} onInput=${(e) => onChange(e.target.value)} aria-label=${label} />
    </div><//>`;
}

// PrefixField puts a site mark in front of every text message, such as
// "【Beijing host1】", so the person reading it knows where it came from.
function PrefixField({ label, value, onChange }) {
  const ov = useFetch(() => get('/overview').catch(() => null), []);
  const host = (ov.data && ov.data.instance) || 'unagikuma';
  const suggested = t('ui.ch.prefix_default', { host });
  return html`<${Field} label=${label} hint=${t('ui.ch.prefix_hint')}>
    <div class="row" style=${{ gap: '8px' }}>
      <input type="text" value=${value} placeholder=${suggested} onInput=${(e) => onChange(e.target.value)} style=${{ flex: '1 1 200px' }} />
      ${value !== suggested && html`<button type="button" class="btn sm" onClick=${() => onChange(suggested)}>${t('ui.ch.prefix_use')}</button>`}
    </div><//>`;
}

// PeriodField limits a channel to a time period, or to the time outside it.
function PeriodField({ label, value, onChange, periods }) {
  const outside = value.startsWith('!');
  const name = value.replace(/^!/, '');
  const names = periods.map(p => p.name);
  const known = !name || names.includes(name);
  return html`<${Field} label=${label} hint=${periods.length ? t('ui.ch.period_hint2') : html`${t('ui.ch.period_none')} <a href="#/config">${t('ui.nav.config')}</a>`}>
    <div class="row" style=${{ gap: '8px', flexWrap: 'wrap' }}>
      <select value=${name} onChange=${(e) => onChange(e.target.value ? (outside ? '!' : '') + e.target.value : '')} style=${{ flex: '1 1 200px' }}>
        <option value="">${t('ui.ch.period_always')}</option>
        ${periods.map(p => html`<option value=${p.name}>${p.name} · ${p.rule}</option>`)}
        ${!known && html`<option value=${name}>${name} (${t('ui.ch.period_missing')})</option>`}
      </select>
      ${name && html`<select value=${outside ? 'out' : 'in'} onChange=${(e) => onChange((e.target.value === 'out' ? '!' : '') + name)} style=${{ width: 'auto' }}>
        <option value="in">${t('ui.ch.period_in')}</option><option value="out">${t('ui.ch.period_out')}</option></select>`}
    </div><//>`;
}

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
  const people = useFetch(() => get('/people').catch(() => ({ people: [] })), []);
  const periods = useFetch(() => get('/periods').catch(() => ({ items: [] })), []);
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
            ${nativeGroups(kinds).map(([g, list]) => list.length > 0 && html`<optgroup label=${t('ui.ch.ngroup.' + g)}>${list.map(k => html`<option value=${k.kind}>${kindLabel(k)}</option>`)}</optgroup>`)}
            ${PRESET_GROUPS.map(g => html`<optgroup label=${t('ui.ch.group.' + g)}>${kinds.filter(k => k.preset && k.group === g).sort((a, b) => a.label.localeCompare(b.label)).map(k => html`<option value=${k.kind}>${k.label}</option>`)}</optgroup>`)}
          </select><//>
        <${Field} label=${t('ui.name')}><input type="text" class="ltr" value=${name} onInput=${(e) => setName(e.target.value)} required disabled=${editing} pattern="[A-Za-z0-9][A-Za-z0-9_.\\-]{0,62}" /><//>
      </div>
      ${info.fields.map(f => {
        const v = opts[f.key] !== undefined ? opts[f.key] : (editing ? '' : f.default || '');
        const label = fieldLabel(f.key) + (required(f) ? ' *' : '');
        if (f.key === 'template') return html`<${TemplateField} label=${label} value=${v.replace(/\\n/g, '\n')} onChange=${(x) => set('template', x)} />`;
        if (f.key === 'at') return html`<${AtField} label=${label} kind=${kind} value=${v} onChange=${(x) => set('at', x)} people=${people.data && people.data.people} />`;
        if (f.key === 'prefix') return html`<${PrefixField} label=${label} value=${v} onChange=${(x) => set('prefix', x)} />`;
        if (f.key === 'period') return html`<${PeriodField} label=${label} value=${v} onChange=${(x) => set('period', x)} periods=${(periods.data && periods.data.items) || []} />`;
        if (f.type === 'select') {
          return html`<${Field} label=${label}><select value=${v} onChange=${(e) => set(f.key, e.target.value)}>${['', ...(f.options || [])].map(o => html`<option value=${o}>${o || '–'}</option>`)}</select><//>`;
        }
        if (f.type === 'textarea') {
          return html`<${Field} label=${label} hint=${f.key === 'template' ? t('ui.ch.template_hint') : f.key === 'body' && kind.startsWith('custom-') ? t('ui.ch.custom_hint') : f.key === 'body' && kind === 'webhook' ? t('ui.ch.webhook_body_hint') : ''}><textarea class="ltr mono" rows="5" value=${f.key === 'template' ? v.replace(/\\n/g, '\n') : v} placeholder=${(f.placeholder || '').replace(/\\n/g, '\n')} onInput=${(e) => set(f.key, e.target.value)}></textarea><//>`;
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
