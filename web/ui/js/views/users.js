// Logins: the account part of a person in the user list — the name they
// sign in with, the password, the role, the teams and, for operators, the
// parts of the configuration they may edit.
import { html, useState } from '../lib.js';
import { t } from '../i18n.js';
import { get, post, put, del } from '../api.js';
import { useFetch, Field, Modal, toast, toastError, confirmDialog, teamTitle, Pick } from '../ui.js';

const ROLES = ['admin', 'operator', 'viewer'];
const splitList = (v) => String(v || '').split(/[\s,;]+/).filter(Boolean);

// LoginForm creates a login (initial.id unset; initial.username may be
// suggested) or changes one; onDeleted is offered for an existing one.
export function LoginForm({ initial, onClose, onSaved, onDeleted, channels }) {
  const teams = useFetch(() => get('/teams'), []);
  const regions = useFetch(() => get('/regions').catch(() => ({ items: [] })), []);
  const nodes = useFetch(() => get('/nodes').catch(() => ({ items: [] })), []);
  const init = { username: '', password: '', role: 'operator', areas: [], teams: [], name: '', emails: '', phones: '', ...initial };
  if (!Array.isArray(init.areas)) init.areas = String(init.areas || '').split(/[\s,]+/).filter(Boolean);
  const [form, setForm] = useState(init);
  // The parts an operator may be limited to: every region and every node.
  const areaOptions = [
    ...((regions.data && regions.data.items) || []).filter(r => !r.builtin).map(r => ({ value: 'region:' + r.name, label: t('ui.user.area_region', { name: r.name }) })),
    ...((nodes.data && (nodes.data.items || nodes.data.nodes)) || []).map(n => n.name).filter(Boolean).map(n => ({ value: n, label: t('ui.user.area_node', { name: n }) })),
  ];
  const teamItems = (teams.data && teams.data.items) || [];
  const multi = teamItems.length > 1;
  const save = async (e) => {
    e.preventDefault();
    if (multi && !form.teams.length) { toastError(new Error(t('ui.user.teams_required'))); return; }
    const body = { role: form.role, areas: form.role === 'operator' ? form.areas : [] };
    if (multi) body.teams = form.teams;
    if (form.password) body.password = form.password;
    let filled = false;
    try {
      if (form.id) await put('/users/' + form.id, body);
      else {
        await post('/users', { ...body, username: form.username.trim(), person: form.person || 0 });
        // A new login that is nobody yet gets a contact of its own; the
        // name and addresses given here fill it in at once.
        const emails = splitList(form.emails), phones = splitList(form.phones);
        if (!form.person && (emails.length || phones.length)) {
          const p = ((await get('/people')).people || []).find(x => x.username === form.username.trim());
          const has = { email: emails.length > 0, phone: phones.length > 0 };
          if (p) await put('/people/' + p.id, { handle: p.handle, name: form.name.trim() || p.name, emails, phones, username: p.username, note: p.note || '',
            notify: (channels || []).filter(c => has[c.contact]).map(c => c.name) });
          filled = emails.length > 0 || phones.length > 0;
        }
      }
      toast(t('ui.saved'));
      onSaved(form.username.trim(), filled);
    } catch (e2) { toastError(e2); }
  };
  const remove = async () => {
    if (!await confirmDialog(t('ui.user.delete_login_confirm', { name: form.username }), { danger: true, ok: t('ui.user.delete_login') })) return;
    try { await del('/users/' + form.id); onDeleted(); } catch (e2) { toastError(e2); }
  };
  return html`<${Modal} title=${form.id ? t('ui.user.login_of', { name: form.username }) : t('ui.user.add')} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      ${!form.id && html`<${Field} label=${t('ui.user.username')} hint=${t('ui.user.username_hint')}><input type="text" class="ltr" required value=${form.username} autocomplete="off" onInput=${(e) => setForm({ ...form, username: e.target.value })} /><//>`}
      ${!form.id && !form.person && html`<${Field} label=${t('ui.dir.name')}><input type="text" value=${form.name} onInput=${(e) => setForm({ ...form, name: e.target.value })} placeholder=${form.username} /><//>
        <div class="two">
          <${Field} label=${t('ui.dir.emails')}><input type="text" class="ltr" value=${form.emails} onInput=${(e) => setForm({ ...form, emails: e.target.value })} placeholder="li@example.com" /><//>
          <${Field} label=${t('ui.dir.phones')}><input type="text" class="ltr" value=${form.phones} onInput=${(e) => setForm({ ...form, phones: e.target.value })} placeholder="+8613800000000" /><//>
        </div>
        <p class="small muted" style=${{ margin: '-6px 0 0' }}>${t('ui.user.contact_optional')}</p>`}
      <${Field} label=${t('ui.user.password')} hint=${form.id ? t('ui.user.password_keep') : t('ui.setup.short')}>
        <input type="password" autocomplete="new-password" minlength="8" required=${!form.id} value=${form.password} onInput=${(e) => setForm({ ...form, password: e.target.value })} /><//>
      <${Field} label=${t('ui.user.role')} hint=${t('ui.user.role.' + form.role + '_hint')}>
        <select value=${form.role} onChange=${(e) => setForm({ ...form, role: e.target.value })}>
          ${ROLES.map(r => html`<option value=${r}>${t('ui.user.role.' + r)}</option>`)}</select><//>
      ${multi ? html`<${Field} label=${t('ui.user.teams')} hint=${t('ui.user.teams_hint')}>
        <${Pick} options=${teamItems.map(tm => ({ value: tm.name, label: teamTitle(tm) }))} value=${form.teams} onChange=${(v) => setForm({ ...form, teams: v })} /><//>`
        : html`<p class="small muted">${t('ui.user.admin_team_only')}</p>`}
      ${form.role === 'operator' && (areaOptions.length > 1 || form.areas.length > 0) && html`<${Field} label=${t('ui.user.areas')} hint=${t('ui.user.areas_pick_hint')}>
        <${Pick} options=${areaOptions} value=${form.areas} onChange=${(v) => setForm({ ...form, areas: v })} none=${t('ui.user.all')} /><//>`}
      <div class="row">
        ${form.id && onDeleted && html`<button type="button" class="btn danger" onClick=${remove}>${t('ui.user.delete_login')}</button>`}
        <div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary">${t('ui.save')}</button></div>
    </form>
  <//>`;
}
