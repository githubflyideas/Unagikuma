// Status pages: announcements shown on the public pages, and who follows
// them by e-mail.
import { html, useState } from '../lib.js';
import { t, fmtTime } from '../i18n.js';
import { get, post, del, put, me } from '../api.js';
import { useFetch, Loading, ErrorBox, Empty, Field, Modal, toast, toastError, errText, confirmDialog } from '../ui.js';

const SEVERITIES = ['info', 'minor', 'major', 'maintenance'];

function NoticeForm({ page, initial, onClose, onSaved }) {
  const a = initial || {};
  const [f, setF] = useState({ title: a.title || '', body: a.body || '', severity: a.severity || 'minor', resolved: !!a.resolved_at, notify: !!page.subscribe });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const set = (k) => (e) => setF({ ...f, [k]: e.target.type === 'checkbox' ? e.target.checked : e.target.value });
  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      if (a.id) await put('/announcements/' + a.id, f);
      else await post('/status-pages/' + encodeURIComponent(page.name) + '/announcements', f);
      toast(t('ui.saved'));
      onSaved();
    } catch (e2) { setErr(e2); }
    setBusy(false);
  };
  return html`<${Modal} title=${a.id ? t('ui.notice.edit') : t('ui.notice.add')} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      <div class="two">
        <${Field} label=${t('ui.notice.title')}><input type="text" value=${f.title} maxlength="200" required onInput=${set('title')} /><//>
        <${Field} label=${t('ui.notice.severity')}><select value=${f.severity} onChange=${set('severity')}>
          ${SEVERITIES.map(s => html`<option value=${s}>${t('ui.pub.sev.' + s)}</option>`)}</select><//>
      </div>
      <${Field} label=${t('ui.notice.body')} hint=${t('ui.notice.body_hint')}><textarea rows="6" value=${f.body} onInput=${set('body')}></textarea><//>
      <label class="switch"><input type="checkbox" checked=${f.resolved} onChange=${set('resolved')} /> ${t('ui.notice.resolved')}</label>
      ${page.subscribe && html`<label class="switch"><input type="checkbox" checked=${f.notify} onChange=${set('notify')} /> ${t('ui.notice.notify', { n: page.subscribers })}</label>`}
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary" disabled=${busy}>${t('ui.save')}</button></div>
    </form>
  <//>`;
}

function PageNotices({ page }) {
  const list = useFetch(() => get('/status-pages/' + encodeURIComponent(page.name) + '/announcements'), [page.name]);
  const [edit, setEdit] = useState(null);
  const remove = async (a) => {
    if (!await confirmDialog(t('ui.notice.delete_confirm', { title: a.title }), { danger: true, ok: t('ui.delete') })) return;
    try { await del('/announcements/' + a.id); list.reload(); } catch (e) { toastError(e); }
  };
  return html`<div class="card">
    <div class="hd"><h2>${page.title}</h2>
      <a class="small ltr" href=${page.url} target="_blank" rel="noopener">${page.domain || page.url}</a>
      <div class="spacer"></div>
      <span class="small muted">${page.subscribe ? t('ui.notice.subscribers', { n: page.subscribers, pending: page.pending }) : t('ui.notice.no_subscribe')}</span>
      <button class="btn primary sm" onClick=${() => setEdit({})}>${t('ui.notice.add')}</button></div>
    <div class="bd flush tbl-wrap">
      ${!list.data ? (list.error ? html`<${ErrorBox} error=${list.error} />` : html`<${Loading} />`)
        : !list.data.length ? html`<${Empty}>${t('ui.notice.none')}<//>` : html`<table class="tbl">
        <thead><tr><th>${t('ui.notice.title')}</th><th>${t('ui.notice.severity')}</th><th>${t('ui.time')}</th><th></th></tr></thead>
        <tbody>${list.data.map(a => html`<tr>
          <td><b>${a.title}</b>${a.resolved_at ? html` <span class="tag">${t('ui.notice.resolved_tag')}</span>` : ''}</td>
          <td class="small">${t('ui.pub.sev.' + a.severity)}</td>
          <td class="small nowrap">${fmtTime(a.updated_at)}</td>
          <td class="right nowrap"><button class="btn sm" onClick=${() => setEdit(a)}>${t('ui.edit')}</button>
            <button class="btn sm danger" onClick=${() => remove(a)}>${t('ui.delete')}</button></td>
        </tr>`)}</tbody></table>`}
    </div>
    ${edit && html`<${NoticeForm} page=${page} initial=${edit.id ? edit : null} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); list.reload(); }} />`}
  </div>`;
}

// NewPage adds a status page (a STATUSPAGE record) for the monitors a
// filter selects.
function NewPage({ onDone }) {
  const [f, setF] = useState({ name: 'status', title: '', select: 'group:public' });
  const [err, setErr] = useState(null);
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const save = async (e) => {
    e.preventDefault();
    setErr(null);
    try {
      const r = await post('/status-pages', { name: f.name.trim(), title: f.title.trim(), select: f.select.trim() });
      toast(t('ui.notice.page_created', { url: r.url || '' }));
      onDone();
    } catch (e2) { setErr(e2); }
  };
  return html`<div class="card"><div class="hd"><h3>${t('ui.notice.new_page')}</h3></div><div class="bd">
    <form class="form" onSubmit=${save}>
      <div class="two">
        <${Field} label=${t('ui.notice.page_title')}><input type="text" value=${f.title} onInput=${set('title')} required placeholder=${t('ui.notice.page_title_ph')} /><//>
        <${Field} label=${t('ui.name')}><input type="text" class="ltr" value=${f.name} onInput=${set('name')} required pattern="[A-Za-z0-9][A-Za-z0-9_.\-]{0,62}" /><//>
      </div>
      <${Field} label=${t('ui.notice.page_select')} hint=${t('ui.notice.page_select_hint')}><input type="text" class="ltr" value=${f.select} onInput=${set('select')} required /><//>
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div><button class="btn primary">${t('ui.notice.create_page')}</button></div>
    </form></div></div>`;
}

// SupportFooter sets the text at the foot of every status page in place of
// "Powered by Unagikuma": a registered installation only.
function SupportFooter() {
  const d = useFetch(() => get('/status-support'), []);
  const [text, setText] = useState(null);
  if (!d.data) return null;
  const v = text === null ? d.data.text : text;
  const save = async (e) => {
    e.preventDefault();
    try { await put('/status-support', { text: v }); toast(t('ui.saved')); d.reload(); setText(null); } catch (err) { toastError(err); }
  };
  return html`<div class="card"><div class="hd"><h3>${t('ui.notice.support')}</h3></div><div class="bd">
    <form class="form" onSubmit=${save}>
      <${Field} label=${t('ui.notice.support_text')} hint=${t('ui.notice.support_hint')}>
        <input type="text" maxlength="120" value=${v} disabled=${!d.data.allowed} placeholder=${t('ui.pub.powered') + ' Unagikuma'} onInput=${(e) => setText(e.target.value)} /><//>
      ${d.data.allowed ? html`<div class="row"><div class="spacer"></div><button class="btn primary">${t('ui.save')}</button></div>`
        : html`<div class="banner warning"><div class="spacer">${t('ui.notice.support_locked')}</div><a class="btn sm" href="#/settings/registration">${t('ui.set.tab.registration')}</a></div>`}
    </form></div></div>`;
}

export function NoticesTab() {
  const pages = useFetch(() => get('/status-pages'), []);
  if (!pages.data) return pages.error ? html`<${ErrorBox} error=${pages.error} />` : html`<${Loading} />`;
  if (!pages.data.length) return html`<div class="col" style=${{ gap: '12px' }}>
    <${Empty} title=${t('ui.notice.no_pages')}>${t('ui.notice.no_pages_hint')}<//>
    <${NewPage} onDone=${pages.reload} />
  </div>`;
  return html`<div class="col" style=${{ gap: '12px' }}>
    <p class="muted small" style=${{ margin: 0 }}>${t('ui.notice.intro')}</p>
    ${pages.data.map(p => html`<${PageNotices} key=${p.name} page=${p} />`)}
    ${me.role === 'admin' && html`<${SupportFooter} />`}
  </div>`;
}
