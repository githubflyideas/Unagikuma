// Contacts: the directory of people who may receive alerts. E-mail and SMS
// go only to its active people: addresses of people on leave, transferred
// or gone, ones that keep bouncing and unknown ones are left out (red dot).
import { html, useState } from '../lib.js';
import { t, tn } from '../i18n.js';
import { get, post, put, del, me } from '../api.js';
import { useFetch, useLive, Loading, ErrorBox, Empty, Ago, Field, Modal, toast, toastError, errText, confirmDialog } from '../ui.js';

const ADDRESS_KEYS = { email: 'to' };
const STATUSES = ['active', 'leave', 'transferred', 'left'];
// addressKey is the channel option holding people's addresses, if any.
export function addressKey(kind) {
  if (ADDRESS_KEYS[kind]) return ADDRESS_KEYS[kind];
  return /-(sms|voice)$/.test(kind) ? 'phones' : '';
}

export function issueText(i) {
  return t('ui.dir.issue.' + i.code, { address: i.address || '', person: i.person ? '@' + i.person : '', n: i.count || 0 });
}

// IssueDot is the red dot of a channel or policy with recipient problems;
// its title says what is wrong.
export function IssueDot({ issues, text }) {
  if (!issues || !issues.length) return null;
  const title = text || issues.map(issueText).join('\n');
  return html`<span class="rdot" title=${title} aria-label=${title}></span>`;
}

function slug(s) {
  return String(s || '').toLowerCase().split('@')[0].replace(/[^a-z0-9._-]+/g, '-').replace(/^[-._]+|[-._]+$/g, '');
}

// handleFrom suggests an @handle: from the name when it is in Latin
// letters, else from the e-mail address or the phone number, so that
// people with names in other scripts do not all become "contact".
function handleFrom(name, emails, phones) {
  const n = slug(name);
  if (n && /[a-z]/.test(n) && n.length >= String(name).replace(/\s+/g, '').length / 2) return n;
  return slug(list(emails)[0]) || (list(phones)[0] ? 'p' + list(phones)[0].replace(/\D+/g, '').slice(-8) : '') || n || 'contact';
}

// viaOf is how a channel reaches a person: e-mail, a text message or a call.
function viaOf(c) {
  if (c.contact === 'email') return 'email';
  return /-sms$/.test(c.kind || '') ? 'sms' : 'phone';
}

function list(v) { return String(v || '').split(',').map(x => x.trim()).filter(Boolean); }

// PersonForm edits a contact: who they are, at least one address, and the
// channels through which the system reaches them directly.
function PersonForm({ initial, channels, onClose, onSaved }) {
  const p = initial || {};
  const [f, setF] = useState({ handle: p.handle || '', name: p.name || '', emails: (p.emails || []).join(', '), phones: (p.phones || []).join(', '),
    username: p.username || '', note: p.note || '', notify: p.notify || [], status: p.status || 'active', report: !p.no_report });
  const has = { email: list(f.emails).length > 0, phone: list(f.phones).length > 0 };
  // Until the channels are picked by hand, they follow the addresses: an
  // e-mail address selects the e-mail channels, a phone number the SMS and
  // voice ones.
  const [picked, setPicked] = useState(!!(p.notify && p.notify.length));
  const auto = channels.filter(c => has[c.contact]).map(c => c.name);
  const notify = picked ? f.notify : auto;
  const toggle = (name) => { setPicked(true); setF({ ...f, notify: notify.includes(name) ? notify.filter(n => n !== name) : [...notify, name] }); };
  const [err, setErr] = useState(null);
  // Logins that can be linked, so a person who leaves loses theirs too.
  const users = useFetch(() => (me.role === 'admin' ? get('/users') : Promise.resolve(null)), []);
  const logins = users.data ? (users.data.items || users.data).map(u => u.username).filter(Boolean) : [];
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const save = async (e) => {
    e.preventDefault();
    setErr(null);
    if (!has.email && !has.phone) { setErr({ message: t('ui.dir.need_address') }); return; }
    if (channels.length && !notify.length) { setErr({ message: t('ui.dir.need_notify') }); return; }
    const body = { handle: f.handle.trim() || handleFrom(f.name, f.emails, f.phones), name: f.name.trim(), emails: list(f.emails), phones: list(f.phones),
      username: f.username.trim(), note: f.note.trim(), notify };
    try {
      let id = p.id;
      if (id) await put('/people/' + id, body);
      else id = (await post('/people', body)).id;
      // Status and the daily report have their own requests: leaving also
      // stops the linked login, which is audited on its own.
      if (f.status !== (p.status || 'active')) await post('/people/' + id + '/status', { status: f.status });
      if (f.report !== !p.no_report) await post('/people/' + id + '/report', { receive: f.report });
      toast(t('ui.saved'));
      onSaved();
    } catch (e2) { setErr(e2); }
  };
  return html`<${Modal} title=${p.id ? t('ui.dir.edit', { name: p.name }) : t('ui.dir.add')} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      <div class="two">
        <${Field} label=${t('ui.dir.name')}><input type="text" value=${f.name} onInput=${set('name')} required /><//>
        <${Field} label=${t('ui.dir.status')} hint=${t('ui.dir.status_hint')}><select value=${f.status} onChange=${set('status')}>
          ${STATUSES.map(st => html`<option value=${st}>${t('ui.dir.status.' + st)}</option>`)}</select><//>
      </div>
      <${Field} label=${t('ui.dir.emails')} hint=${t('ui.dir.list_hint')}><input type="text" class="ltr" value=${f.emails} onInput=${set('emails')} placeholder="li@example.com" /><//>
      <${Field} label=${t('ui.dir.phones')} hint=${t('ui.dir.list_hint')}><input type="text" class="ltr" value=${f.phones} onInput=${set('phones')} placeholder="+8613800000000" /><//>
      <label class="switch"><input type="checkbox" checked=${f.report} onChange=${(e) => setF({ ...f, report: e.target.checked })} /> ${t('ui.report.receives')}</label>
      <p class="small muted" style=${{ margin: 0 }}>${!channels.length ? t('ui.dir.notify_none')
        : notify.length ? t('ui.dir.notify_auto', { list: notify.join(', ') }) : t('ui.dir.notify_auto_none')}</p>
      <details open=${!!(p.username || p.note || picked)}>
        <summary class="small">${t('ui.dir.more')}</summary>
        <div class="col" style=${{ gap: '10px', marginTop: '10px' }}>
          ${channels.length > 0 && html`<${Field} label=${t('ui.dir.notify')} hint=${t('ui.dir.notify_hint')}>
            <div class="col">${channels.map(c => html`<label class="switch">
              <input type="checkbox" checked=${notify.includes(c.name)} disabled=${!has[c.contact] && !notify.includes(c.name)} onChange=${() => toggle(c.name)} />
              <b class="ltr">${c.name}</b> <span class="small muted">${t('ui.dir.via.' + viaOf(c))}</span></label>`)}</div><//>`}
          <div class="two">
            <${Field} label=${t('ui.dir.note')} hint=${t('ui.dir.note_hint')}><input type="text" value=${f.note} onInput=${set('note')} /><//>
            <${Field} label=${t('ui.dir.username')} hint=${t('ui.dir.username_hint')}>
              <select value=${f.username} onChange=${set('username')}>
                <option value="">${t('ui.dir.no_login')}</option>
                ${[...new Set([...logins, ...(f.username ? [f.username] : [])])].map(u => html`<option value=${u}>${u}</option>`)}</select><//>
          </div>
          <${Field} label=${t('ui.dir.handle')} hint=${t('ui.dir.handle_hint')}><input type="text" class="ltr" value=${f.handle} onInput=${set('handle')} placeholder=${handleFrom(f.name, f.emails, f.phones)} pattern="[a-z0-9][a-z0-9._\\-]{0,62}" /><//>
        </div>
      </details>
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary">${t('ui.save')}</button></div>
    </form>
  <//>`;
}

export function ContactsTab() {
  const d = useFetch(() => get('/people'), []);
  const tun = useFetch(() => get('/tunables'), []);
  const [edit, setEdit] = useState(null);
  useLive(['directory'], d.reload, 500);
  if (!d.data) return d.error ? html`<${ErrorBox} error=${d.error} retry=${d.reload} />` : html`<${Loading} />`;
  const { people, bounces, unknown, threshold } = d.data;
  const personal = d.data.personal_channels || [];
  const nUnknown = (unknown.email || []).length + (unknown.phone || []).length;
  const importAll = async () => {
    try {
      const r = await post('/people/import', {});
      toast(tn('ui.dir.imported', r.created));
      if (r.errors.length) toast(r.errors.join('; '), 'error');
      d.reload();
    } catch (e) { toastError(e); }
  };
  const setStatus = async (p, status) => {
    if (status === 'left' && !await confirmDialog(t('ui.dir.left_confirm', { name: p.name }), { danger: true, ok: t('ui.dir.mark_left') })) { d.reload(); return; }
    try { await post('/people/' + p.id + '/status', { status }); d.reload(); } catch (e) { toastError(e); d.reload(); }
  };
  // The daily status report can be switched off person by person.
  const setReport = async (p, receive) => {
    try { await post('/people/' + p.id + '/report', { receive }); d.reload(); } catch (e) { toastError(e); d.reload(); }
  };
  // Optionally, a team's administrators hear of recipients left out.
  const tellKey = 'contacts.notify-admins';
  const tell = tun.data && tun.data.find(x => x.key === tellKey);
  const setTell = async (on) => {
    try { await put('/tunables', { set: { [tellKey]: on ? 'on' : 'off' }, unset: [] }); tun.reload(); } catch (e) { toastError(e); }
  };
  const remove = async (p) => {
    if (!await confirmDialog(t('ui.dir.delete_confirm', { name: p.name }), { danger: true, ok: t('ui.delete') })) return;
    try { await del('/people/' + p.id); d.reload(); } catch (e) { toastError(e); }
  };
  const clearBounce = async (b) => {
    try { await del('/bounces?kind=' + encodeURIComponent(b.kind) + '&address=' + encodeURIComponent(b.address)); d.reload(); } catch (e) { toastError(e); }
  };
  return html`<div class="col" style=${{ gap: '12px' }}>
    <div class="row"><p class="muted small spacer" style=${{ margin: 0 }}>${t('ui.dir.intro')}</p>
      <button class="btn primary" onClick=${() => setEdit({})}>${t('ui.dir.add')}</button></div>
    ${nUnknown > 0 && html`<div class="banner warning row">
      <span class="rdot"></span><span class="spacer">${tn('ui.dir.unknown_banner', nUnknown)}
        <span class="small muted ltr"> ${[...(unknown.email || []), ...(unknown.phone || [])].slice(0, 6).join(', ')}${nUnknown > 6 ? ' …' : ''}</span></span>
      <button class="btn sm primary" onClick=${importAll}>${t('ui.dir.import')}</button></div>`}
    <div class="card"><div class="bd flush tbl-wrap">
      ${!people.length ? html`<${Empty} title=${t('ui.dir.none')}>${t('ui.dir.none_hint')}<//>` : html`<table class="tbl">
        <thead><tr><th>${t('ui.dir.name')}</th><th>${t('ui.dir.emails')}</th><th>${t('ui.dir.phones')}</th><th>${t('ui.dir.notify')}</th><th>${t('ui.dir.channels')}</th><th>${t('ui.dir.status')}</th><th>${t('ui.report.tab')}</th><th></th></tr></thead>
        <tbody>${people.map(p => html`<tr class=${p.status !== 'active' ? 'muted' : ''}>
          <td><span class="name">${p.name}</span> <span class="small muted ltr">@${p.handle}</span>
            ${p.note && html`<div class="sub small">${p.note}</div>`}
            ${p.username && html`<div class="sub small ltr">${t('ui.dir.login')}: ${p.username}</div>`}</td>
          <td class="small ltr">${p.emails.join(', ')}</td>
          <td class="small ltr">${p.phones.join(', ')}</td>
          <td class="small">${p.reach && p.reach !== 'left' && p.reach !== 'inactive' ? html`<span class="rdot" title=${t('ui.dir.reach.' + p.reach)}></span> <span class="err-text">${t('ui.dir.reach.' + p.reach)}</span>` : html`<span class="ltr">${(p.notify || []).join(', ')}</span>`}</td>
          <td class="small">${p.channels.join(', ')}</td>
          <td><select value=${p.status} onChange=${(e) => setStatus(p, e.target.value)} aria-label=${t('ui.dir.status')}>
            ${STATUSES.map(st => html`<option value=${st}>${t('ui.dir.status.' + st)}</option>`)}</select></td>
          <td><label class="switch" title=${t('ui.report.person_hint')}><input type="checkbox" checked=${!p.no_report} onChange=${(e) => setReport(p, e.target.checked)}
            aria-label=${t('ui.report.receives_person', { name: p.name })} /></label></td>
          <td class="right nowrap">
            <button class="btn sm" onClick=${() => setEdit(p)}>${t('ui.edit')}</button>
            <button class="btn sm danger" onClick=${() => remove(p)}>${t('ui.delete')}</button>
          </td>
        </tr>`)}</tbody>
      </table>`}
    </div></div>
    ${bounces.length > 0 && html`<div class="card"><div class="hd"><h3>${t('ui.dir.bounces')}</h3></div>
      <div class="bd"><p class="small muted" style=${{ marginTop: 0 }}>${threshold > 0 ? tn('ui.dir.bounce_rule', threshold) : t('ui.dir.bounce_off')}</p></div>
      <div class="bd flush tbl-wrap"><table class="tbl">
        <thead><tr><th>${t('ui.dir.address')}</th><th class="right">${t('ui.dir.count')}</th><th>${t('ui.dir.reason')}</th><th>${t('ui.dir.last')}</th><th></th></tr></thead>
        <tbody>${bounces.map(b => html`<tr>
          <td class="ltr">${threshold > 0 && b.count >= threshold && html`<span class="rdot" title=${t('ui.dir.skipped')}></span> `}${b.address}</td>
          <td class="right">${b.count}</td>
          <td class="small muted ltr">${b.reason}</td>
          <td class="small"><${Ago} at=${b.last_at} /></td>
          <td class="right"><button class="btn sm" onClick=${() => clearBounce(b)} title=${t('ui.dir.clear_hint')}>${t('ui.dir.clear')}</button></td>
        </tr>`)}</tbody>
      </table></div></div>`}
    ${tell && me.role === 'admin' && html`<label class="switch"><input type="checkbox" checked=${['on', 'yes', 'true', '1'].includes(String(tell.value).toLowerCase())} onChange=${(e) => setTell(e.target.checked)} /> ${t('ui.dir.tell_admins')}</label>`}
    ${edit && html`<${PersonForm} channels=${personal} initial=${edit.id ? edit : null} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); d.reload(); }} />`}
  </div>`;
}

// NewAddresses asks, before a channel is saved, whether the addresses the
// directory does not know should be added to it (each with a name).
// onAdd gets the people to add; the caller saves the channel first, so that
// they can be reached through it.
export function NewAddresses({ kind, addresses, onAdd, onSkip, onClose }) {
  const [names, setNames] = useState(addresses.map(a => a.includes('@') ? a.split('@')[0] : ''));
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const add = async () => {
    setBusy(true);
    setErr(null);
    try {
      const people = addresses.map((raw, i) => {
        const a = raw.replace(/^.*<([^>]+)>\s*$/, '$1').trim();
        return { name: names[i].trim() || a, handle: handleFrom(names[i].trim(), kind === 'email' ? a : '', kind === 'phone' ? a : ''), emails: kind === 'email' ? [a] : [], phones: kind === 'phone' ? [a] : [] };
      });
      await onAdd(people);
    } catch (e) { setErr(e); setBusy(false); }
  };
  return html`<${Modal} title=${t('ui.dir.new_title')} onClose=${onClose}>
    <div class="col" style=${{ gap: '10px' }}>
      <p class="small" style=${{ margin: 0 }}><span class="rdot"></span> ${t('ui.dir.new_intro')}</p>
      ${addresses.map((a, i) => html`<div class="two">
        <div class="ltr" style=${{ alignSelf: 'center' }}>${a}</div>
        <input type="text" value=${names[i]} placeholder=${t('ui.dir.name')} onInput=${(e) => { const n = names.slice(); n[i] = e.target.value; setNames(n); }} />
      </div>`)}
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div>
        <button type="button" class="btn" onClick=${onSkip}>${t('ui.dir.save_anyway')}</button>
        <button type="button" class="btn primary" onClick=${add} disabled=${busy}>${t('ui.dir.add_and_save')}</button></div>
    </div>
  <//>`;
}
