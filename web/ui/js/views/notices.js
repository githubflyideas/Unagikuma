// The single announcement board: one list of notices, a markdown editor
// with a live preview, severity, schedule and pinning. Public status
// pages (if any are defined in the configuration) show the same board.
import { html, useState, useMemo } from '../lib.js';
import { t, fmtTime } from '../i18n.js';
import { get, post, del, put, me } from '../api.js';
import { useFetch, Loading, ErrorBox, Empty, Field, Modal, toast, toastError, errText, confirmDialog, Copy } from '../ui.js';

const SEVERITIES = ['info', 'minor', 'major', 'maintenance'];

const pad = (n) => String(n).padStart(2, '0');
function toLocal(d) {
  if (!d) return '';
  d = new Date(d);
  if (Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
function toISO(v) {
  if (!v) return '';
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? '' : d.toISOString();
}

// markdownHTML renders a small, escaped markdown subset for the preview.
export function markdownHTML(src) {
  const esc = (s) => String(s).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  const inline = (s) => esc(s)
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/\*([^*]+)\*/g, '<em>$1</em>')
    .replace(/\[([^\]]+)\]\((https?:[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
  const lines = String(src || '').replace(/\r\n/g, '\n').split('\n');
  const out = [];
  let list = null;
  const flush = () => { if (list) { out.push(`<ul>${list}</ul>`); list = null; } };
  for (const line of lines) {
    const m = /^(#{1,3})\s+(.*)$/.exec(line);
    if (m) { flush(); out.push(`<h${m[1].length}>${inline(m[2])}</h${m[1].length}>`); continue; }
    if (/^>\s?/.test(line)) { flush(); out.push(`<blockquote>${inline(line.replace(/^>\s?/, ''))}</blockquote>`); continue; }
    if (/^[-*]\s+/.test(line)) { list = (list || '') + `<li>${inline(line.replace(/^[-*]\s+/, ''))}</li>`; continue; }
    if (!line.trim()) { flush(); continue; }
    flush();
    out.push(`<p>${inline(line)}</p>`);
  }
  flush();
  return out.join('') || `<p class="muted">${esc('')}</p>`;
}

function wrapSel(el, before, after) {
  const start = el.selectionStart, end = el.selectionEnd;
  const v = el.value;
  const sel = v.slice(start, end) || 'text';
  const next = v.slice(0, start) + before + sel + after + v.slice(end);
  el.value = next;
  const from = start + before.length, to = from + sel.length;
  el.setSelectionRange(from, to);
  el.focus();
  return next;
}

function NoticeForm({ initial, onClose, onSaved }) {
  const a = initial || {};
  const [f, setF] = useState({
    title: a.title || '', body: a.body || '', severity: a.severity || 'info',
    resolved: !!a.resolved_at, notify: false, pinned: !!a.pinned,
    starts_at: toLocal(a.starts_at), ends_at: toLocal(a.ends_at),
  });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const [tab, setTab] = useState('write');
  const set = (k) => (e) => setF({ ...f, [k]: e.target.type === 'checkbox' ? e.target.checked : e.target.value });
  const tool = (before, after) => (e) => {
    const ta = e.currentTarget.closest('form').querySelector('textarea[name="body"]');
    if (!ta) return;
    setF({ ...f, body: wrapSel(ta, before, after) });
  };
  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    const body = {
      title: f.title, body: f.body, severity: f.severity, resolved: f.resolved,
      notify: f.notify, pinned: f.pinned,
      starts_at: toISO(f.starts_at), ends_at: toISO(f.ends_at),
    };
    try {
      if (a.id) await put('/announcements/' + a.id, body);
      else await post('/announcements', body);
      toast(t('ui.saved'));
      onSaved();
    } catch (e2) { setErr(e2); }
    setBusy(false);
  };
  const preview = useMemo(() => markdownHTML(f.body), [f.body]);
  return html`<${Modal} title=${a.id ? t('ui.notice.edit') : t('ui.notice.add')} onClose=${onClose} wide>
    <form class="form notice-editor" onSubmit=${save}>
      <div class="two">
        <${Field} label=${t('ui.notice.title')}><input type="text" value=${f.title} maxlength="200" required onInput=${set('title')} /><//>
        <${Field} label=${t('ui.notice.severity')}><select value=${f.severity} onChange=${set('severity')}>
          ${SEVERITIES.map(s => html`<option value=${s}>${t('ui.pub.sev.' + s)}</option>`)}</select><//>
      </div>
      <div class="two">
        <${Field} label=${t('ui.notice.starts')}><input type="datetime-local" value=${f.starts_at} onInput=${set('starts_at')} /><//>
        <${Field} label=${t('ui.notice.ends')}><input type="datetime-local" value=${f.ends_at} onInput=${set('ends_at')} /><//>
      </div>
      <${Field} label=${t('ui.notice.body')} hint=${t('ui.notice.body_hint')}>
        <div class="md-toolbar" role="toolbar" aria-label=${t('ui.notice.format')}>
          <button type="button" class="btn sm" title="Bold" onClick=${tool('**', '**')}>B</button>
          <button type="button" class="btn sm" title="Italic" onClick=${tool('*', '*')}><i>I</i></button>
          <button type="button" class="btn sm" title="Heading" onClick=${tool('## ', '')}>H</button>
          <button type="button" class="btn sm" title="List" onClick=${tool('- ', '')}>•</button>
          <button type="button" class="btn sm" title="Link" onClick=${tool('[', '](https://)')}>🔗</button>
          <button type="button" class="btn sm" title="Code" onClick=${tool('`', '`')}>{}</button>
          <button type="button" class="btn sm" title="Quote" onClick=${tool('> ', '')}>“</button>
          <div class="spacer"></div>
          <div class="seg md-tabs">
            <button type="button" class=${tab === 'write' ? 'on' : ''} onClick=${() => setTab('write')}>${t('ui.notice.write')}</button>
            <button type="button" class=${tab === 'preview' ? 'on' : ''} onClick=${() => setTab('preview')}>${t('ui.notice.preview')}</button>
          </div>
        </div>
        ${tab === 'write'
          ? html`<textarea name="body" rows="10" value=${f.body} onInput=${set('body')}></textarea>`
          : html`<div class="md-preview" dangerouslySetInnerHTML=${{ __html: preview }}></div>`}
      <//>
      <div class="row" style=${{ gap: '16px', flexWrap: 'wrap' }}>
        <label class="switch"><input type="checkbox" checked=${f.pinned} onChange=${set('pinned')} /> ${t('ui.notice.pinned')}</label>
        <label class="switch"><input type="checkbox" checked=${f.resolved} onChange=${set('resolved')} /> ${t('ui.notice.resolved')}</label>
        <label class="switch"><input type="checkbox" checked=${f.notify} onChange=${set('notify')} /> ${t('ui.notice.notify_board')}</label>
      </div>
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary" disabled=${busy}>${t('ui.save')}</button></div>
    </form>
  <//>`;
}

// SupportFooter sets the text at the foot of every status page in place of
// "Powered by Unagikuma": a registered installation only.
export function SupportFooter() {
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

// PublicPages lists the public status pages with their addresses to copy.
function PublicPages() {
  const pages = useFetch(() => get('/status-pages'), []);
  if (!pages.data) return null;
  return html`<div class="card"><div class="hd"><h3>${t('ui.notice.pages_title')}</h3></div>
    <div class="bd">${pages.data.length ? html`<div class="col" style=${{ gap: '8px' }}>${pages.data.map(p => html`<div class="row">
        <b>${p.title}</b>
        <a class="ltr mono small" href=${p.url} target="_blank" rel="noopener">${p.url}</a>
        <div class="spacer"></div><${Copy} text=${p.url} />
      </div>`)}</div>` : html`<p class="muted small" style=${{ margin: 0 }}>${t('ui.notice.no_pages_hint')}</p>`}</div></div>`;
}

export function NoticesTab() {
  const list = useFetch(() => get('/announcements'), []);
  const [edit, setEdit] = useState(null);
  const remove = async (a) => {
    if (!await confirmDialog(t('ui.notice.delete_confirm', { title: a.title }), { danger: true, ok: t('ui.delete') })) return;
    try { await del('/announcements/' + a.id); list.reload(); } catch (e) { toastError(e); }
  };
  const items = list.data || [];
  return html`<div class="col board" style=${{ gap: '12px' }}>
    <div class="row"><p class="muted small spacer" style=${{ margin: 0 }}>${t('ui.notice.intro')}</p>
      ${me.role !== 'viewer' && html`<button class="btn primary" onClick=${() => setEdit({})}>${t('ui.notice.add')}</button>`}</div>
    <${PublicPages} />
    ${!list.data ? (list.error ? html`<${ErrorBox} error=${list.error} />` : html`<${Loading} />`)
      : !items.length ? html`<${Empty} title=${t('ui.notice.none')}>${t('ui.notice.none_hint')}<//>`
      : html`<div class="card"><div class="bd flush tbl-wrap"><table class="tbl">
        <thead><tr>
          <th>${t('ui.notice.title')}</th><th>${t('ui.notice.severity')}</th>
          <th>${t('ui.notice.schedule')}</th><th>${t('ui.time')}</th><th></th>
        </tr></thead>
        <tbody>${items.map(a => html`<tr>
          <td><b>${a.title}</b>
            ${a.pinned ? html` <span class="tag">${t('ui.notice.pinned_tag')}</span>` : ''}
            ${a.resolved_at ? html` <span class="tag">${t('ui.notice.resolved_tag')}</span>` : ''}</td>
          <td class="small">${t('ui.pub.sev.' + a.severity)}</td>
          <td class="small nowrap">${a.starts_at || a.ends_at ? [a.starts_at ? fmtTime(a.starts_at) : '…', a.ends_at ? fmtTime(a.ends_at) : '…'].join(' – ') : '—'}</td>
          <td class="small nowrap">${fmtTime(a.updated_at)}</td>
          <td class="right nowrap">${me.role !== 'viewer' && html`<button class="btn sm" onClick=${() => setEdit(a)}>${t('ui.edit')}</button>
            <button class="btn sm danger" onClick=${() => remove(a)}>${t('ui.delete')}</button>`}</td>
        </tr>`)}</tbody></table></div></div>`}
    ${edit && html`<${NoticeForm} initial=${edit.id ? edit : null} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); list.reload(); }} />`}
  </div>`;
}
