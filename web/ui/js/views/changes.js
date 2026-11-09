// Changes: announced planned work. Who is told, reminders before it
// starts, a maintenance window for the monitors it touches, and the
// receipt (done, rolled back, cancelled) when it ends.
import { html, useState } from '../lib.js';
import { t, currentLang } from '../i18n.js';
import { get, post, put, qs } from '../api.js';
import { useFetch, useLive, Loading, ErrorBox, Empty, Field, Modal, toast, errText } from '../ui.js';

const pad = (n) => String(n).padStart(2, '0');
// toLocal formats a date for a datetime-local input.
function toLocal(d) {
  d = new Date(d);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
function when(c) {
  const s = new Date(c.start), e = new Date(c.end);
  const day = (d) => d.toLocaleDateString(currentLang(), { month: 'short', day: 'numeric' });
  const hm = (d) => d.toLocaleTimeString(currentLang(), { hour: '2-digit', minute: '2-digit' });
  return day(s) + ' ' + hm(s) + ' – ' + (s.toDateString() === e.toDateString() ? '' : day(e) + ' ') + hm(e);
}
const STATE_CLS = { planned: 'tag', running: 'tag warn-text', done: 'tag ok-text', rolled_back: 'tag err-text', cancelled: 'tag muted' };

function ChangeForm({ initial, onClose, onSaved }) {
  const c = initial || {};
  const pol = useFetch(() => get('/policies'), []);
  const ch = useFetch(() => get('/channels'), []);
  const pages = useFetch(() => get('/status-pages'), []);
  const now = new Date(Date.now() + 3600e3);
  now.setMinutes(0, 0, 0);
  const [f, setF] = useState({
    title: c.title || '', impact: c.impact || '', description: c.description || '', select: c.select || '',
    start: toLocal(c.start || now), end: toLocal(c.end || new Date(now.getTime() + 2 * 3600e3)),
    to: c.channels && c.channels.length ? 'channels' : 'policy', policy: c.policy || '', channels: c.channels || [],
    status_page: c.status_page || '', notify: true,
  });
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const set = (k) => (e) => setF({ ...f, [k]: e.target.type === 'checkbox' ? e.target.checked : e.target.value });
  const toggle = (n) => setF({ ...f, channels: f.channels.includes(n) ? f.channels.filter(x => x !== n) : [...f.channels, n] });
  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    const body = { title: f.title.trim(), impact: f.impact.trim(), description: f.description.trim(), select: f.select.trim(),
      start: new Date(f.start).toISOString(), end: new Date(f.end).toISOString(), status_page: f.status_page, notify: f.notify,
      policy: f.to === 'policy' ? f.policy : '', channels: f.to === 'channels' ? f.channels : [] };
    try {
      if (c.id) await put('/changes/' + c.id, body);
      else await post('/changes', body);
      toast(c.id ? t('ui.saved') : t('ui.chg.announced'));
      onSaved();
    } catch (e2) { setErr(e2); }
    setBusy(false);
  };
  return html`<${Modal} title=${c.id ? t('ui.chg.edit') : t('ui.chg.add')} onClose=${onClose} wide>
    <form class="form" onSubmit=${save}>
      <${Field} label=${t('ui.chg.title')}><input type="text" value=${f.title} onInput=${set('title')} required placeholder=${t('ui.chg.title_ph')} /><//>
      <div class="two">
        <${Field} label=${t('ui.chg.start')}><input type="datetime-local" value=${f.start} onInput=${set('start')} required /><//>
        <${Field} label=${t('ui.chg.end')}><input type="datetime-local" value=${f.end} onInput=${set('end')} required /><//>
      </div>
      <${Field} label=${t('ui.chg.impact')}><input type="text" value=${f.impact} onInput=${set('impact')} placeholder=${t('ui.chg.impact_ph')} /><//>
      <${Field} label=${t('ui.chg.description')}><textarea rows="3" value=${f.description} onInput=${set('description')}></textarea><//>
      <${Field} label=${t('ui.chg.select')} hint=${t('ui.chg.select_hint')}><input type="text" class="ltr" value=${f.select} onInput=${set('select')} placeholder="group:core tag:site:bj" /><//>
      <${Field} label=${t('ui.chg.tell')}>
        <div class="row" style=${{ gap: '12px', flexWrap: 'wrap' }}>
          <label><input type="radio" name="to" checked=${f.to === 'policy'} onChange=${() => setF({ ...f, to: 'policy' })} /> ${t('ui.chg.via_policy')}</label>
          <label><input type="radio" name="to" checked=${f.to === 'channels'} onChange=${() => setF({ ...f, to: 'channels' })} /> ${t('ui.chg.via_channels')}</label>
        </div>
        ${f.to === 'policy' ? html`<select value=${f.policy} onChange=${set('policy')}>
            <option value="">${t('ui.chg.default_policy')}</option>
            ${(pol.data || []).filter(p => !p.implicit).map(p => html`<option value=${p.name}>${p.name}</option>`)}</select>`
          : html`<div class="row" style=${{ gap: '10px', flexWrap: 'wrap' }}>${(ch.data || []).map(x => html`<label class="ltr"><input type="checkbox" checked=${f.channels.includes(x.name)} onChange=${() => toggle(x.name)} /> ${x.name}</label>`)}</div>`}
      <//>
      <div class="two">
        <${Field} label=${t('ui.chg.status_page')}><select value=${f.status_page} onChange=${set('status_page')}>
          <option value="">${t('ui.chg.no_page')}</option>${(pages.data || []).map(p => html`<option value=${p.name}>${p.title || p.name}</option>`)}</select><//>
        <${Field} label=" "><label><input type="checkbox" checked=${f.notify} onChange=${set('notify')} /> ${c.id ? t('ui.chg.notify_resched') : t('ui.chg.notify_now')}</label><//>
      </div>
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary" disabled=${busy}>${c.id ? t('ui.save') : t('ui.chg.announce')}</button></div>
    </form>
  <//>`;
}

function FinishForm({ change, state, onClose, onDone }) {
  const [note, setNote] = useState('');
  const [err, setErr] = useState(null);
  const save = async (e) => {
    e.preventDefault();
    try { await post('/changes/' + change.id + '/finish', { state, note: note.trim() }); onDone(); } catch (e2) { setErr(e2); }
  };
  return html`<${Modal} title=${t('ui.chg.finish.' + state, { title: change.title })} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      <${Field} label=${t('ui.chg.note')} hint=${t('ui.chg.note_hint')}><textarea rows="4" value=${note} onInput=${(e) => setNote(e.target.value)}></textarea><//>
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class=${'btn ' + (state === 'done' ? 'primary' : 'danger solid')}>${t('ui.chg.state.' + state)}</button></div>
    </form>
  <//>`;
}

function Templates({ onClose }) {
  const d = useFetch(() => get('/changes/templates'), []);
  const [edits, setEdits] = useState({});
  const [err, setErr] = useState(null);
  if (!d.data) return html`<${Modal} title=${t('ui.chg.templates')} onClose=${onClose}>${d.error ? html`<${ErrorBox} error=${d.error} />` : html`<${Loading} />`}<//>`;
  const cur = (ev) => edits[ev] || d.data.templates[ev];
  const set = (ev, k, v) => setEdits({ ...edits, [ev]: { ...cur(ev), [k]: v } });
  const save = async () => {
    setErr(null);
    const out = {};
    for (const ev of d.data.events) if (edits[ev] || d.data.custom[ev]) out[ev] = cur(ev);
    try { await put('/changes/templates', out); toast(t('ui.saved')); onClose(); } catch (e) { setErr(e); }
  };
  const reset = (ev) => setEdits({ ...edits, [ev]: { title: '', body: '' } });
  return html`<${Modal} title=${t('ui.chg.templates')} onClose=${onClose} wide>
    <div class="col" style=${{ gap: '12px' }}>
      <p class="small muted" style=${{ margin: 0 }}>${t('ui.chg.templates_hint')}</p>
      ${d.data.events.map(ev => html`<div class="card"><div class="bd col" style=${{ gap: '6px' }}>
        <div class="row"><b class="spacer">${t('ui.chg.ev.' + ev)}</b>${(d.data.custom[ev] || edits[ev]) && html`<button class="btn sm ghost" onClick=${() => reset(ev)}>${t('ui.chg.builtin')}</button>`}</div>
        <input type="text" value=${cur(ev).title} onInput=${(e) => set(ev, 'title', e.target.value)} placeholder=${t('ui.chg.builtin')} />
        <textarea rows="3" class="mono" value=${cur(ev).body} onInput=${(e) => set(ev, 'body', e.target.value)}></textarea>
      </div></div>`)}
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div><button class="btn" onClick=${onClose}>${t('ui.cancel')}</button><button class="btn primary" onClick=${save}>${t('ui.save')}</button></div>
    </div>
  <//>`;
}

export function ChangesTab() {
  const [filter, setFilter] = useState({ q: '', state: '', from: '', to: '' });
  const list = useFetch(() => get('/changes' + qs(filter)), [filter.q, filter.state, filter.from, filter.to]);
  const [edit, setEdit] = useState(null);
  const [finish, setFinish] = useState(null);
  const [tpl, setTpl] = useState(false);
  useLive(['changes'], list.reload, 500);
  const set = (k) => (e) => setFilter({ ...filter, [k]: e.target.value });
  return html`<div class="col" style=${{ gap: '12px' }}>
    <div class="row"><p class="muted small spacer" style=${{ margin: 0 }}>${t('ui.chg.intro')}</p>
      <button class="btn" onClick=${() => setTpl(true)}>${t('ui.chg.templates')}</button>
      <button class="btn primary" onClick=${() => setEdit({})}>${t('ui.chg.add')}</button></div>
    <div class="row" style=${{ gap: '8px', flexWrap: 'wrap' }}>
      <input type="search" value=${filter.q} onInput=${set('q')} placeholder=${t('ui.chg.search')} style=${{ flex: '1 1 220px', width: 'auto', minWidth: '180px' }} />
      <select value=${filter.state} onChange=${set('state')} style=${{ width: 'auto' }}>
        <option value="">${t('ui.chg.all')}</option><option value="open">${t('ui.chg.open')}</option>
        ${['done', 'rolled_back', 'cancelled'].map(s => html`<option value=${s}>${t('ui.chg.state.' + s)}</option>`)}</select>
      <input type="date" value=${filter.from} onInput=${set('from')} aria-label=${t('ui.chg.from')} style=${{ width: 'auto' }} />
      <input type="date" value=${filter.to} onInput=${set('to')} aria-label=${t('ui.chg.to')} style=${{ width: 'auto' }} />
      <a class="btn" href=${'/api/v1/changes' + qs({ ...filter, format: 'csv' })} download="changes.csv">CSV</a>
    </div>
    <div class="card"><div class="bd flush tbl-wrap">
      ${!list.data ? (list.error ? html`<${ErrorBox} error=${list.error} />` : html`<${Loading} />`)
        : !list.data.items.length ? html`<${Empty} title=${t('ui.chg.none')}>${t('ui.chg.none_hint')}<//>` : html`<table class="tbl">
        <thead><tr><th>${t('ui.chg.title')}</th><th>${t('ui.chg.when')}</th><th>${t('ui.chg.state_h')}</th><th class="right">${t('ui.monitors')}</th><th></th></tr></thead>
        <tbody>${list.data.items.map(c => html`<tr>
          <td><span class="name">${c.title}</span>${c.impact && html`<div class="sub small muted">${c.impact}</div>`}
            ${c.note && html`<div class="sub small">💬 ${c.note}</div>`}</td>
          <td class="small nowrap">${when(c)}<div class="sub muted">${c.created_by}</div></td>
          <td><span class=${STATE_CLS[c.state] || 'tag'}>${t('ui.chg.state.' + c.state)}</span></td>
          <td class="right">${c.monitors}</td>
          <td class="right nowrap">${(c.state === 'planned' || c.state === 'running') && html`
            <button class="btn sm" onClick=${() => setEdit(c)}>${t('ui.edit')}</button>
            <button class="btn sm" onClick=${() => setFinish({ change: c, state: 'done' })}>${t('ui.chg.state.done')}</button>
            ${c.state === 'running' ? html`<button class="btn sm danger" onClick=${() => setFinish({ change: c, state: 'rolled_back' })}>${t('ui.chg.state.rolled_back')}</button>`
              : html`<button class="btn sm danger" onClick=${() => setFinish({ change: c, state: 'cancelled' })}>${t('ui.chg.cancel')}</button>`}`}</td>
        </tr>`)}</tbody>
      </table>`}
    </div></div>
    ${edit && html`<${ChangeForm} initial=${edit.id ? edit : null} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); list.reload(); }} />`}
    ${finish && html`<${FinishForm} change=${finish.change} state=${finish.state} onClose=${() => setFinish(null)} onDone=${() => { setFinish(null); toast(t('ui.saved')); list.reload(); }} />`}
    ${tpl && html`<${Templates} onClose=${() => setTpl(false)} />`}
  </div>`;
}
