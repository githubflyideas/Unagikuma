// Alerting: notification channels and the contact directory. Teams choose
// their channels (Settings > Teams).
import { html, useState, useEffect, useRef } from '../lib.js';
import { t, has, currentLang } from '../i18n.js';
import { get, post, del, put, ifMatch, me } from '../api.js';
import { ContactsTab, IssueDot, NewAddresses, addressKey } from './contacts.js';
import { PeriodsTab } from './periods.js';
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

// Channel kinds by type, for the kind menu. Presets carry their type
// (group); the built-in kinds are listed here.
const TYPE_ORDER = ['chat', 'push', 'sms', 'voice', 'email', 'whatsapp', 'incident', 'automation', 'custom'];
const NATIVE_TYPE = {
  feishu: 'chat', dingtalk: 'chat', wecom: 'chat', telegram: 'chat', slack: 'chat', discord: 'chat', email: 'email',
  'aliyun-sms': 'sms', 'tencent-sms': 'sms', 'twilio-sms': 'sms', 'custom-sms': 'sms',
  'aliyun-voice': 'voice', 'tencent-voice': 'voice', 'twilio-voice': 'voice', 'custom-voice': 'voice',
  webhook: 'custom', group: 'custom', log: 'custom',
};
const typeOf = (k) => NATIVE_TYPE[k.kind] || k.group || 'custom';
const nameOf = (k) => k.kind.replace(/^webhook:/, '');
// How widely used a kind is: the order of the default menu.
const COMMON = ['feishu', 'dingtalk', 'wecom', 'email', 'webhook', 'telegram', 'slack', 'discord', 'teams', 'aliyun-sms', 'aliyun-voice',
  'tencent-sms', 'tencent-voice', 'twilio-sms', 'twilio-voice', 'custom-sms', 'custom-voice', 'googlechat', 'mattermost', 'matrix', 'rocketchat',
  'line', 'signal', 'webex', 'zulip', 'pagerduty', 'opsgenie', 'datadog', 'grafanaoncall', 'splunk', 'gitlab', 'github', 'ntfy', 'gotify', 'bark',
  'pushover', 'pushbullet', 'apprise', 'homeassistant', 'serverchan', 'pushplus', 'wxpusher', 'onebot', 'kakaowork', 'chatwork', 'whatsapp', 'zapi',
  'zenvia', 'zapier', 'n8n', 'ifttt', 'sendgrid', 'resend', 'brevo', 'vonage', 'plivo', 'messagebird', 'infobip', 'telnyx', 'clicksend', 'unifonic'];
const LESS_COMMON = ['zohocliq', 'nextcloudtalk', 'clickup', 'threema', 'pumble', 'kook', 'pushdeer', 'flashduty', 'dooray', 'jandi', 'ilert',
  'incidentio', 'squadcast', 'signl4', 'alerta', 'goalert', 'newrelic', 'gitea', 'waha', 'evolution', 'callmebot', 'whapi', 'sevenio', 'taqnyat',
  'ooredoo', 'smsc', 'octopush', '46elks', 'freemobile', 'smseagle', 'smsgateway', 'vkteams', 'vk', 'bitrix24', 'gorush', 'group', 'log'];
const rank = (k) => { const n = nameOf(k), i = COMMON.indexOf(n), j = LESS_COMMON.indexOf(n); return i >= 0 ? i : j >= 0 ? 1000 + j : 2000; };
const labelOf = (k) => (k.preset ? k.label : kindLabel(k));

// kindSections is the kind menu: sorted by how common a kind is (the
// default), by name, or by type, and narrowed by a search. The kind in use
// always stays in the menu.
function kindSections(kinds, sort, query, current) {
  const q = query.trim().toLowerCase();
  const list = kinds.filter(k => k.kind === current || !q || (labelOf(k) + ' ' + nameOf(k) + ' ' + t('ui.ch.type.' + typeOf(k))).toLowerCase().includes(q));
  const byName = (a, b) => labelOf(a).localeCompare(labelOf(b));
  if (sort === 'name') return [['', [...list].sort(byName)]];
  if (sort === 'type') return TYPE_ORDER.map(ty => [t('ui.ch.type.' + ty), list.filter(k => typeOf(k) === ty).sort((a, b) => rank(a) - rank(b) || byName(a, b))]).filter(([, l]) => l.length);
  const sorted = [...list].sort((a, b) => rank(a) - rank(b) || byName(a, b));
  return [[t('ui.ch.pop.common'), sorted.filter(k => rank(k) < 1000)], [t('ui.ch.pop.less'), sorted.filter(k => rank(k) >= 1000 && rank(k) < 2000)],
    [t('ui.ch.pop.other'), sorted.filter(k => rank(k) >= 2000)]].filter(([, l]) => l.length);
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
  return html`<${Field} label=${label} hint=${periods.length ? t('ui.ch.period_hint2') : html`${t('ui.ch.period_none')} <a href="#/alerts/periods">${t('ui.per.add')}</a>`}>
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

// RequestPanel shows what the channel sends for an example problem, in
// the receiving service's own format (credentials masked), next to a link
// to that service's API documentation. Nothing is sent.
function RequestPanel({ kind, opts, docs }) {
  const [pv, setPv] = useState(null);
  const key = kind + JSON.stringify(opts);
  useEffect(() => {
    const id = setTimeout(async () => {
      try { setPv(await post('/channels/request-preview', { kind, opts, lang: currentLang() })); } catch (e) { setPv({ ok: false, error: errText(e) }); }
    }, 400);
    return () => clearTimeout(id);
  }, [key]);
  const p = pv && pv.preview;
  let text = '';
  if (p && pv.http) {
    text = p.method + ' ' + p.url + '\n'
      + (p.query || []).map(([k, v]) => '  ' + k + '=' + v).join('\n') + ((p.query || []).length ? '\n' : '')
      + (p.headers || []).map(([k, v]) => k + ': ' + v).join('\n') + '\n\n' + (p.body || '');
  }
  return html`<details class="req-preview" open>
    <summary>${t('ui.ch.req_title')}${docs && html` · <a href=${docs} target="_blank" rel="noopener noreferrer" onClick=${(e) => e.stopPropagation()}>${t('ui.ch.req_docs')}</a>`}</summary>
    ${!pv ? html`<div class="small muted">…</div>`
      : !pv.ok ? html`<div class="small err-text">${pv.error}</div>`
      : !pv.http ? html`<div class="small muted">${t(kind === 'email' ? 'ui.ch.req_email' : 'ui.ch.req_none')}</div>${p && html`<pre class="mono small">${p.title + '\n' + p.text}</pre>`}`
      : html`<pre class="mono small ltr">${text}</pre><div class="small muted">${t('ui.ch.req_hint')}</div>`}
  </details>`;
}

// Kinds that send to an address the user wrote take extra request headers.
const takesHeaders = (kind) => kind === 'webhook' || kind.startsWith('webhook:') || kind === 'custom-sms' || kind === 'custom-voice';

// HeadersField edits the header.<name> options, one "Name: value" a line.
// A credential header comes back masked (***) and stays as saved unless
// it is typed again.
function HeadersField({ opts, setOpts }) {
  const fromOpts = () => Object.entries(opts).filter(([k]) => k.startsWith('header.')).map(([k, v]) => k.slice(7) + ': ' + v).join('\n');
  const [text, setText] = useState(fromOpts);
  const change = (s) => {
    setText(s);
    const next = Object.fromEntries(Object.entries(opts).filter(([k]) => !k.startsWith('header.')));
    for (const line of s.split('\n')) {
      const i = line.indexOf(':');
      const k = line.slice(0, i).trim().toLowerCase();
      if (i > 0 && /^[a-z0-9-]+$/.test(k)) next['header.' + k] = line.slice(i + 1).trim();
    }
    setOpts(next);
  };
  return html`<${Field} label=${t('ui.ch.headers')} hint=${t('ui.ch.headers_hint')}>
    <textarea class="ltr mono" rows="3" value=${text} placeholder="Content-Type: application/json" onInput=${(e) => change(e.target.value)}></textarea><//>`;
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
  const [sort, setSort] = useState('common');
  const [query, setQuery] = useState('');
  const people = useFetch(() => get('/people').catch(() => ({ people: [] })), []);
  const periods = useFetch(() => get('/periods').catch(() => ({ items: [] })), []);
  const found = kinds.find(k => k.kind === kind);
  // E-mail goes to an SMTP server or through this machine's sendmail
  // command; each way shows only its own fields.
  const viaSendmail = kind === 'email' && opts.via === 'sendmail';
  const shown = (f) => kind !== 'email' || (viaSendmail ? !SMTP_ONLY.includes(f.key) : f.key !== 'program');
  // Any channel may be limited to a time period ("!name" = outside it).
  const info = { ...found, fields: [...((found && found.fields) || []).filter(shown), { key: 'period', type: 'text', placeholder: 'office-hours' }] };
  const required = (f) => f.required || (kind === 'email' && !viaSendmail && (f.key === 'host' || f.key === 'from'));
  const set = (k, v) => setOpts({ ...opts, [k]: v });
  const values = () => {
    // Options this form does not show (retries=, rate=, an option written
    // in the file) are kept as they are; only what the form edits changes.
    const o = {};
    if (editing) for (const [k, v] of Object.entries(initial.opts)) if (v !== '' && !k.startsWith('header.')) o[k] = v;
    for (const f of info.fields) {
      const v = opts[f.key] !== undefined ? opts[f.key] : '';
      if (v !== '') o[f.key] = v; else delete o[f.key];
    }
    delete o.system; delete o.report;
    if (opts.system === 'yes') o.system = 'yes';
    if (opts.report === 'no' || opts.report === 'yes') o.report = opts.report;
    for (const [k, v] of Object.entries(opts)) if (k.startsWith('header.') && v !== '') o[k] = v;
    return o;
  };
  // store saves the channel; then (optional) runs once it exists.
  const store = async (then) => {
    setBusy(true);
    setErr(null);
    try {
      await post('/channels', { name, kind, opts: values(), from: editing ? initial.name : '' }, ifMatch(initial && initial.etag));
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
      setTest({ res: await post('/channels/test', editing && Object.values(o).includes('***') ? { name: initial.name } : { name: name || 'test', kind: k, opts: o }) });
    } catch (e2) { setTest({ err: e2 }); }
  };
  return html`<${Modal} title=${editing ? t('ui.ch.edit', { name }) : t('ui.ch.add')} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      <div class="two">
        <${Field} label=${t('ui.ch.kind')}>
          ${!editing && html`<div class="kind-tools">
            <input type="search" value=${query} placeholder=${t('ui.ch.kind_search')} aria-label=${t('ui.ch.kind_search')} onInput=${(e) => setQuery(e.target.value)} />
            <div class="seg sm" role="group" aria-label=${t('ui.ch.sort')}>${['common', 'name', 'type'].map(v => html`<button type="button" aria-pressed=${String(sort === v)} onClick=${() => setSort(v)}>${t('ui.ch.sort.' + v)}</button>`)}</div>
          </div>`}
          <select value=${kind} onChange=${(e) => { setKind(e.target.value); setTest(null); }} disabled=${editing}>
            ${kindSections(kinds, sort, query, kind).map(([label, list]) => label
              ? html`<optgroup label=${label}>${list.map(k => html`<option value=${k.kind}>${labelOf(k)}</option>`)}</optgroup>`
              : list.map(k => html`<option value=${k.kind}>${labelOf(k)}</option>`))}
          </select><//>
        <${Field} label=${t('ui.name')} hint=${editing ? (name !== initial.name ? t('ui.ch.rename_hint') : '') : t('ui.ch.name_hint')}><input type="text" class="ltr" value=${name} onInput=${(e) => setName(e.target.value)} required pattern="[A-Za-z0-9][A-Za-z0-9_.\\-]{0,62}" placeholder=${t('ui.ch.name_ph')} /><//>
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
          // Ready bodies for common APIs: pick one, then change only the text.
          const pick = async (e) => {
            const ex = (f.examples || []).find(x => x.id === e.target.value);
            e.target.value = '';
            if (!ex || (v.trim() && !await confirmDialog(t('ui.ch.example_replace')))) return;
            setOpts({ ...opts, [f.key]: ex.body, 'content-type': 'json' });
          };
          const examples = f.examples && f.examples.length > 0 && html`<select value="" onChange=${pick} aria-label=${t('ui.ch.example_pick')} style=${{ marginBottom: '6px' }}>
            <option value="">${t('ui.ch.example_pick')}</option>
            ${f.examples.map(x => html`<option value=${x.id}>${t('ui.ch.example.' + x.id)}</option>`)}</select>`;
          return html`<${Field} label=${label} hint=${f.key === 'template' ? t('ui.ch.template_hint') : f.key === 'body' && kind.startsWith('custom-') ? t('ui.ch.custom_hint') : f.key === 'body' && kind === 'webhook' ? t('ui.ch.webhook_body_hint') : ''}>${examples || ''}<textarea class="ltr mono" rows="5" value=${f.key === 'template' ? v.replace(/\\n/g, '\n') : v} placeholder=${(f.placeholder || '').replace(/\\n/g, '\n')} onInput=${(e) => set(f.key, e.target.value)}></textarea><//>`;
        }
        const hint = f.type === 'secret' ? t('ui.ch.secret_hint') : f.type === 'email-list' ? t('ui.ch.email_list') : f.key === 'period' ? t('ui.ch.period_hint') : f.key === 'members' ? t('ui.ch.members_hint')
          : f.key === 'at' ? t('ui.ch.at_hint') : f.key === 'program' ? t('ui.ch.program_hint') : f.key === 'from' && viaSendmail ? t('ui.ch.from_sendmail_hint') : '';
        return html`<${Field} label=${label} hint=${hint}>
          <input type=${f.type === 'secret' ? 'password' : f.type === 'number' ? 'number' : 'text'} class="ltr" value=${v} required=${required(f) && !editing}
            placeholder=${f.type === 'secret' && editing ? '***' : f.placeholder || ''} autocomplete="off" onInput=${(e) => set(f.key, e.target.value)} /><//>`;
      })}
      ${takesHeaders(kind) && html`<${HeadersField} opts=${opts} setOpts=${setOpts} />`}
      ${kind !== 'group' && html`<${RequestPanel} kind=${kind} opts=${values()} docs=${found && found.docs} />`}
      <label class="switch"><input type="checkbox" checked=${opts.system === 'yes'} onChange=${(e) => set('system', e.target.checked ? 'yes' : '')} /> ${t('ui.ch.system')}</label>
      <p class="small muted" style=${{ margin: 0 }}>${t('ui.ch.system_hint')}</p>
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
    <${DeliveryRules} />
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
// DELIVERY_KEYS are the settings that govern how every channel delivers:
// timeout, retries, waits and the fallback to the next channel. They are
// tunables, edited here next to the channels they apply to.
export const DELIVERY_KEYS = ['notify.channel-timeout', 'notify.channel-retries', 'notify.retry-backoff',
  'notify.retry-after-max', 'notify.failed-retry', 'notify.channel-rate'];

function DeliveryRules() {
  const tun = useFetch(() => get('/tunables'), []);
  const [open, setOpen] = useState(false);
  const [vals, setVals] = useState({});
  const [busy, setBusy] = useState(false);
  if (!tun.data) return null;
  const defs = DELIVERY_KEYS.map(k => tun.data.find(x => x.key === k)).filter(Boolean);
  const zh = currentLang() === 'zh';
  const summary = defs.map(d => t('ui.dr.short.' + d.key.replace('notify.', '')) + ' ' + d.value).join(' · ');
  const start = () => { setVals(Object.fromEntries(defs.map(d => [d.key, d.overridden ? d.value : '']))); setOpen(true); };
  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      const set = {}, unset = [];
      for (const d of defs) {
        const v = (vals[d.key] || '').trim();
        if (!v || v === d.default) { if (d.overridden) unset.push(d.key); } else if (v !== d.value) set[d.key] = v;
      }
      if (Object.keys(set).length || unset.length) await put('/tunables', { set, unset });
      toast(t('ui.saved'));
      setOpen(false);
      tun.reload();
    } catch (e2) { toastError(e2); }
    setBusy(false);
  };
  return html`<div class="card"><div class="bd row small" style=${{ padding: '10px 14px' }}>
      <b>${t('ui.dr.title')}</b><span class="muted spacer">${summary}</span>
      ${me.role === 'admin' && html`<button class="btn sm" onClick=${start}>${t('ui.edit')}</button>`}</div>
    ${open && html`<${Modal} title=${t('ui.dr.title')} onClose=${() => setOpen(false)} wide>
      <form class="form" onSubmit=${save}>
        <p class="small muted" style=${{ margin: 0 }}>${t('ui.dr.intro')}</p>
        <div class="two">${defs.map(d => html`<${Field} label=${t('ui.dr.short.' + d.key.replace('notify.', ''))}
            hint=${(zh ? d.desc_zh : d.desc) + (d.min ? ' (' + d.min + ' – ' + d.max + ')' : '')}>
          <input type="text" class="ltr" value=${vals[d.key]} placeholder=${t('ui.dr.default', { v: d.default })}
            onInput=${(e) => setVals({ ...vals, [d.key]: e.target.value })} /><//>`)}</div>
        <div class="row"><div class="spacer"></div>
          <button type="button" class="btn" onClick=${() => setOpen(false)}>${t('ui.cancel')}</button>
          <button class="btn primary" disabled=${busy}>${t('ui.save')}</button></div>
      </form><//>`}
  </div>`;
}

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
      <a href="#/alerts/periods" class=${tab === 'periods' ? 'on' : ''}>${t('ui.per.tab')}</a>
    </div>
    ${tab === 'contacts' ? html`<${ContactsTab} />` : tab === 'periods' ? html`<${PeriodsTab} />` : html`<${ChannelsTab} />`}
  </div>`;
}

export { put };
