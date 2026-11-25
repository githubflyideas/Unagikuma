// Logins: the account part of a person in the user list — the name they
// sign in with, the password, the role, the teams and, for operators, the
// parts of the configuration they may edit.
import { html, useState } from '../lib.js';
import { t } from '../i18n.js';
import { get, post, put, del } from '../api.js';
import { useFetch, Field, Modal, toast, toastError, confirmDialog, teamTitle } from '../ui.js';

const ROLES = ['admin', 'operator', 'viewer'];

// LoginForm creates a login (initial.id unset; initial.username may be
// suggested) or changes one; onDeleted is offered for an existing one.
export function LoginForm({ initial, onClose, onSaved, onDeleted }) {
  const teams = useFetch(() => get('/teams'), []);
  const [form, setForm] = useState({ username: '', password: '', role: 'operator', areas: '', teams: [], ...initial });
  const teamItems = (teams.data && teams.data.items) || [];
  const multi = teamItems.length > 1;
  const toggleTeam = (name) => setForm({ ...form, teams: form.teams.includes(name) ? form.teams.filter(x => x !== name) : [...form.teams, name] });
  const save = async (e) => {
    e.preventDefault();
    if (multi && !form.teams.length) { toastError(new Error(t('ui.user.teams_required'))); return; }
    const body = { role: form.role, areas: String(form.areas || '').split(/[\s,]+/).filter(Boolean) };
    if (multi) body.teams = form.teams;
    if (form.password) body.password = form.password;
    try {
      if (form.id) await put('/users/' + form.id, body);
      else await post('/users', { ...body, username: form.username.trim(), person: form.person || 0 });
      toast(t('ui.saved'));
      onSaved(form.username.trim());
    } catch (e2) { toastError(e2); }
  };
  const remove = async () => {
    if (!await confirmDialog(t('ui.user.delete_login_confirm', { name: form.username }), { danger: true, ok: t('ui.user.delete_login') })) return;
    try { await del('/users/' + form.id); onDeleted(); } catch (e2) { toastError(e2); }
  };
  return html`<${Modal} title=${form.id ? t('ui.user.login_of', { name: form.username }) : t('ui.user.add')} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      ${!form.id && html`<${Field} label=${t('ui.user.username')} hint=${t('ui.user.username_hint')}><input type="text" class="ltr" required value=${form.username} autocomplete="off" onInput=${(e) => setForm({ ...form, username: e.target.value })} /><//>`}
      <${Field} label=${t('ui.user.password')} hint=${form.id ? t('ui.user.password_keep') : t('ui.setup.short')}>
        <input type="password" autocomplete="new-password" minlength="8" required=${!form.id} value=${form.password} onInput=${(e) => setForm({ ...form, password: e.target.value })} /><//>
      <${Field} label=${t('ui.user.role')}>
        <div class="col">${ROLES.map(r => html`<label class="switch"><input type="radio" name="role" checked=${form.role === r} onChange=${() => setForm({ ...form, role: r })} />
          <b>${t('ui.user.role.' + r)}</b> <span class="small muted">${t('ui.user.role.' + r + '_hint')}</span></label>`)}</div><//>
      ${multi ? html`<${Field} label=${t('ui.user.teams')} hint=${t('ui.user.teams_hint')}>
        <div class="col">${teamItems.map(tm => html`<label class="switch"><input type="checkbox" checked=${form.teams.includes(tm.name)} onChange=${() => toggleTeam(tm.name)} /> ${teamTitle(tm)}</label>`)}</div><//>`
        : html`<p class="small muted">${t('ui.user.admin_team_only')}</p>`}
      ${form.role === 'operator' && html`<${Field} label=${t('ui.user.areas')} hint=${t('ui.user.areas_hint')}>
        <input type="text" class="ltr mono" value=${form.areas} onInput=${(e) => setForm({ ...form, areas: e.target.value })} placeholder="region:north, bj" /><//>`}
      <div class="row">
        ${form.id && onDeleted && html`<button type="button" class="btn danger" onClick=${remove}>${t('ui.user.delete_login')}</button>`}
        <div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary">${t('ui.save')}</button></div>
    </form>
  <//>`;
}
