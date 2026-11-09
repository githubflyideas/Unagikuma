// monitors.conf: the file (read-only), its history and imports.
import { html, useState } from '../lib.js';
import { t, tn, fmtTime, detailText } from '../i18n.js';
import { get, post, getCSRF } from '../api.js';
import { useFetch, useLive, Loading, ErrorBox, Empty, Field, Modal, toast, toastError, confirmDialog, useDocTitle } from '../ui.js';

// diffRows turns the line diff ("  " same, "- " old only, "+ " new only)
// into two-column rows: a run of removed lines is paired with the added
// lines that follow it.
function diffRows(text) {
  const lines = text.replace(/\n$/, '').split('\n');
  const rows = [];
  let ln = 0, rn = 0, i = 0;
  while (i < lines.length) {
    const l = lines[i];
    if (!l.startsWith('- ') && !l.startsWith('+ ')) {
      ln++; rn++;
      rows.push({ kind: 'same', l: l.slice(2), r: l.slice(2), ln, rn });
      i++;
      continue;
    }
    const del = [], add = [];
    while (i < lines.length && lines[i].startsWith('- ')) del.push(lines[i++].slice(2));
    while (i < lines.length && lines[i].startsWith('+ ')) add.push(lines[i++].slice(2));
    for (let k = 0; k < Math.max(del.length, add.length); k++) {
      const row = { kind: 'change' };
      if (k < del.length) { row.l = del[k]; row.ln = ++ln; }
      if (k < add.length) { row.r = add[k]; row.rn = ++rn; }
      if (k >= del.length) row.kind = 'add';
      else if (k >= add.length) row.kind = 'del';
      rows.push(row);
    }
  }
  return rows;
}

// onlyChanges keeps the changed rows and a little context around them;
// a gap stands for the unchanged lines left out.
function onlyChanges(rows, context = 2) {
  const keep = rows.map(() => false);
  rows.forEach((r, i) => {
    if (r.kind === 'same') return;
    for (let k = Math.max(0, i - context); k <= Math.min(rows.length - 1, i + context); k++) keep[k] = true;
  });
  const out = [];
  rows.forEach((r, i) => {
    if (keep[i]) out.push(r);
    else if (out.length && !out[out.length - 1].gap) out.push({ gap: true });
  });
  if (out.length && out[out.length - 1].gap) out.pop();
  return out;
}

function SplitDiff({ rows }) {
  return html`<div class="diff split"><table><tbody>${rows.map(r => r.gap
    ? html`<tr class="gap"><td colspan="4">⋯</td></tr>`
    : html`<tr class=${r.kind}>
      <td class="no">${r.ln || ''}</td><td class=${r.l !== undefined && r.kind !== 'same' ? 'del' : ''}>${r.l !== undefined ? r.l || ' ' : ''}</td>
      <td class="no">${r.rn || ''}</td><td class=${r.r !== undefined && r.kind !== 'same' ? 'add' : ''}>${r.r !== undefined ? r.r || ' ' : ''}</td>
    </tr>`)}</tbody></table></div>`;
}

// Diff shows two versions of the file: side by side (the default), only
// the changed lines side by side, or as one list of lines.
export function Diff({ text }) {
  const [mode, setMode] = useState('split');
  if (!text) return html`<p class="muted small">${t('ui.config.no_diff')}</p>`;
  const rows = diffRows(text);
  const changed = rows.some(r => r.kind !== 'same');
  return html`<div class="col" style=${{ gap: '8px' }}>
    <div class="seg" role="tablist">${[['split', 'ui.config.diff_split'], ['changes', 'ui.config.diff_changes'], ['unified', 'ui.config.diff_unified']].map(([k, label]) =>
      html`<button type="button" role="tab" aria-selected=${mode === k} class=${'btn sm' + (mode === k ? ' primary' : '')} onClick=${() => setMode(k)}>${t(label)}</button>`)}</div>
    ${!changed ? html`<p class="muted small">${t('ui.config.no_diff')}</p>`
      : mode === 'unified' ? html`<div class="diff">${text.split('\n').map(l => {
        const c = l.startsWith('+ ') ? 'add' : l.startsWith('- ') ? 'del' : '';
        return html`<div class=${c}>${l || ' '}</div>`;
      })}</div>`
      : html`<${SplitDiff} rows=${mode === 'changes' ? onlyChanges(rows) : rows} />`}
  </div>`;
}

// FileView shows monitors.conf read-only, with line numbers and the
// lines that have problems marked. Changes are made in the forms of the
// other pages, or in the file itself on the server.
function FileView({ value, diags }) {
  const marks = {};
  for (const d of diags || []) {
    const l = d.pos && d.pos.line;
    if (l) marks[l] = d.severity === 'error' ? 'bad' : (marks[l] || 'warn');
  }
  const lines = value.split('\n');
  return html`<div class="editor readonly">
    <div class="gutter">${lines.map((_, i) => html`<div class=${marks[i + 1] || ''}>${i + 1}</div>`)}</div>
    <pre class="file" aria-label="monitors.conf">${value}</pre>
  </div>`;
}

function EditTab() {
  const cur = useFetch(() => get('/config'), []);
  useLive(['serial'], cur.reload, 500);
  if (!cur.data) return cur.error ? html`<${ErrorBox} error=${cur.error} retry=${cur.reload} />` : html`<${Loading} />`;
  const diags = cur.data.diagnostics || [];
  const errors = diags.filter(d => d.severity === 'error');
  const copy = async () => {
    try { await navigator.clipboard.writeText(cur.data.text); toast(t('ui.copied')); } catch (e) { toastError(e); }
  };
  return html`<div class="col" style=${{ gap: '12px' }}>
    <div class="row">
      <span class="muted small ltr">${cur.data.path} · ${t('ui.config.serial')} ${cur.data.serial}</span>
      <div class="spacer"></div>
      <button class="btn" onClick=${copy}>${t('ui.config.copy')}</button>
      <button class="btn" onClick=${cur.reload}>${t('ui.config.reload')}</button>
    </div>
    <div class="banner info small">${t('ui.config.readonly')}</div>
    ${diags.length > 0 && html`<div class=${'banner ' + (errors.length ? 'error' : 'warning')}><ul class="small" style=${{ margin: 0, paddingInlineStart: '18px' }}>
      ${diags.slice(0, 20).map(d => html`<li class="ltr">${t('ui.config.line', { line: d.pos.line })}: ${detailText(d.message)}</li>`)}
    </ul></div>`}
    <${FileView} value=${cur.data.text} diags=${diags} />
    <p class="small muted">${t('ui.config.help_readonly')}</p>
  </div>`;
}

function HistoryTab() {
  const h = useFetch(() => get('/config/history?limit=100'), []);
  const [diff, setDiff] = useState(null);
  const show = async (serial) => {
    try {
      const r = await get(`/config/diff?from=${serial}&to=current`);
      setDiff({ serial, text: r.unified });
    } catch (e) { toastError(e); }
  };
  const rollback = async (serial) => {
    if (!await confirmDialog(t('ui.config.rollback_confirm', { serial }), { danger: true })) return;
    try {
      const r = await post('/config/rollback', { serial });
      toast(t('ui.config.saved', { serial: r.serial }));
      setDiff(null);
      h.reload();
    } catch (e) { toastError(e); }
  };
  if (!h.data) return h.error ? html`<${ErrorBox} error=${h.error} />` : html`<${Loading} />`;
  if (!h.data.items.length) return html`<${Empty}>${t('ui.config.no_history')}<//>`;
  return html`<div class="card"><div class="bd flush tbl-wrap"><table class="tbl">
    <thead><tr><th>${t('ui.config.serial')}</th><th>${t('ui.time')}</th><th>${t('ui.config.source')}</th><th>${t('ui.config.message')}</th><th></th></tr></thead>
    <tbody>${h.data.items.map((s, i) => html`<tr>
      <td class="mono">${s.serial}${s.term > 0 ? html` <span class="tag" title=${t('ui.config.term_hint')}>${t('ui.ha.term', { n: s.term })}</span>` : ''}${i === 0 ? html` <span class="tag">${t('ui.config.current')}</span>` : ''}</td>
      <td class="nowrap small">${fmtTime(s.at)}</td>
      <td class="small">${t('ui.config.src.' + s.source)}${s.author ? ' · ' + s.author : ''}</td>
      <td class="small">${s.message || ''}${s.summary ? html`<div class="muted">${Object.entries(s.summary).filter(([, v]) => v).map(([k, v]) => `${t('ui.change.' + k)} ${v}`).join(' · ')}</div>` : ''}</td>
      <td class="right nowrap">${i > 0 && html`<button class="btn sm" onClick=${() => show(s.serial)}>${t('ui.config.diff')}</button> <button class="btn sm" onClick=${() => rollback(s.serial)}>${t('ui.config.rollback')}</button>`}</td>
    </tr>`)}</tbody>
  </table></div>
  ${diff && html`<${Modal} wide title=${t('ui.config.diff_title', { serial: diff.serial })} onClose=${() => setDiff(null)} footer=${html`
      <button class="btn" onClick=${() => setDiff(null)}>${t('ui.close')}</button>
      <button class="btn danger" onClick=${() => rollback(diff.serial)}>${t('ui.config.rollback')}</button>`}>
    <${Diff} text=${diff.text} />
  <//>`}
  </div>`;
}

// ImportTab converts another tool's monitors: an Uptime Kuma backup, Nagios object files, a Checkmate export or a Gatus file:
// preview first, then add.
function ImportTab() {
  const [source, setSource] = useState('kuma');
  const [file, setFile] = useState(null);
  const [prev, setPrev] = useState(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(null);
  const [area, setArea] = useState('');
  const [role, setRole] = useState('operator');
  const [manager, setManager] = useState('');
  const users = useFetch(() => get('/users').catch(() => ({ items: [] })), []);
  const mons = useFetch(() => get('/monitors'), []);
  const areas = [...new Set(((mons.data && mons.data.items) || []).map(m => m.area).filter(Boolean))].sort();
  const reset = () => { setPrev(null); setDone(null); };
  const send = async (apply) => {
    setBusy(true);
    try {
      const q = new URLSearchParams({ area: area.trim(), role, manager: source === 'kuma' || source === 'nagios' ? manager : '' });
      if (apply) q.set('apply', '1');
      const r = await fetch('/api/v1/import/' + source + '?' + q, { method: 'POST', credentials: 'same-origin', headers: { 'X-Unagikuma-CSRF': getCSRF() }, body: file });
      const body = await r.json();
      if (!r.ok) throw new Error(body.message || 'HTTP ' + r.status);
      if (apply) {
        // E-mail and SMS go only to people in the contact directory: offer
        // the addresses the imported channels use.
        let unknown = 0;
        try { const d = await get('/people'); unknown = (d.unknown.email || []).length + (d.unknown.phone || []).length; } catch { /* only advice */ }
        setDone({ ...body, unknown }); setPrev(null); toast(t('ui.saved'));
      } else setPrev(body);
    } catch (e) { toastError(e); }
    setBusy(false);
  };
  const addContacts = async () => {
    try {
      const r = await post('/people/import', {});
      toast(tn('ui.dir.imported', r.created));
      setDone({ ...done, unknown: 0 });
    } catch (e) { toastError(e); }
  };
  const userList = (prev && prev.users) || [];
  return html`<div class="col" style=${{ gap: '12px' }}>
    <div class="card"><div class="bd form">
      <${Field} label=${t('ui.import.source')}>
        <div class="src-cards" role="radiogroup">${[['kuma', 'Uptime Kuma', 'ui.import.src_kuma'], ['nagios', 'Nagios', 'ui.import.src_nagios'], ['checkmate', 'Checkmate', 'ui.import.src_checkmate'], ['gatus', 'Gatus', 'ui.import.src_gatus']].map(([k, name, hint]) => html`
          <button type="button" role="radio" aria-checked=${source === k} class=${'src-card' + (source === k ? ' on' : '')} onClick=${() => { setSource(k); setFile(null); reset(); }}>
            <b>${name}</b><span class="small muted">${t(hint)}</span></button>`)}</div><//>
      <p class="small muted" style=${{ margin: 0 }}>${t(source === 'kuma' ? 'ui.import.intro' : source === 'nagios' ? 'ui.import.nagios_intro' : 'ui.import.adapter_intro')}</p>
      ${source !== 'nagios'
        ? html`<input key=${source} type="file" accept=${source === 'kuma' ? '.db,.json,application/json,application/vnd.sqlite3' : source === 'checkmate' ? '.json,application/json' : '.yaml,.yml,text/yaml'} onChange=${(e) => { setFile(e.target.files[0] || null); reset(); }} />`
        : html`<input key="nagios" type="file" multiple accept=".cfg,text/plain" onChange=${(e) => {
          // Several object files are simply joined, as Nagios reads them.
          const fs = [...e.target.files];
          setFile(fs.length ? new Blob(fs.flatMap(f => [f, '\n']), { type: 'text/plain' }) : null);
          reset();
        }} />`}
      <div class="two">
        <${Field} label=${t('ui.import.area')} hint=${t('ui.import.area_hint')}>
          <input type="text" class="ltr" list="import-areas" value=${area} placeholder="region:north" onInput=${(e) => { setArea(e.target.value); reset(); }} />
          <datalist id="import-areas">${areas.map(a => html`<option value=${a} />`)}</datalist><//>
        <${Field} label=${t('ui.import.manager')} hint=${t('ui.import.manager_hint')}>
          <select value=${manager} disabled=${!area.trim() || source === 'checkmate' || source === 'gatus'} onChange=${(e) => setManager(e.target.value)}>
            <option value="">—</option>
            ${((users.data && users.data.items) || []).map(u => html`<option value=${u.username}>${u.username} (${t('ui.user.role.' + u.role)})</option>`)}
          </select><//>
      </div>
      ${source === 'kuma' && html`<${Field} label=${t('ui.import.role')} hint=${t('ui.import.role_hint')}>
        <select value=${role} onChange=${(e) => setRole(e.target.value)}>
          ${['operator', 'viewer', 'admin'].map(r => html`<option value=${r}>${t('ui.user.role.' + r)}</option>`)}
        </select><//>`}
      <div class="row"><div class="spacer"></div>
        <button class="btn" disabled=${!file || busy} onClick=${() => send(false)}>${t('ui.import.preview')}</button></div>
    </div></div>
    ${done && html`<div class="banner ok"><div class="spacer">${t('ui.import.done', { monitors: done.monitors, channels: done.channels })}
      ${done.users && done.users.length > 0 && html`<ul class="small">${done.users.map(u => html`<li><span class="ltr">${u.username}</span>: ${t('ui.import.user.' + u.result)}</li>`)}</ul>`}</div></div>`}
    ${done && done.unknown > 0 && html`<div class="banner warning row"><span class="rdot"></span><span class="spacer">${tn('ui.dir.unknown_banner', done.unknown)}</span>
      <button class="btn sm primary" onClick=${addContacts}>${t('ui.dir.import')}</button></div>`}
    ${prev && html`<div class="card"><div class="hd"><h2>${t('ui.import.summary', { monitors: prev.monitors, channels: prev.channels })}${userList.length > 0 ? ' · ' + t('ui.import.accounts', { n: userList.length }) : ''}</h2><div class="spacer"></div>
        <button class="btn primary" disabled=${busy || prev.blocked} onClick=${() => send(true)}>${t('ui.import.apply')}</button></div>
      <div class="bd col" style=${{ gap: '10px' }}>
        ${prev.notes && prev.notes.length > 0 && html`<div class="banner warning"><div class="spacer"><b>${t('ui.import.notes')}</b>
          <ul class="small">${prev.notes.map(n => html`<li>${n}</li>`)}</ul></div></div>`}
        ${userList.length > 0 && html`<p class="small" style=${{ margin: 0 }}>${t('ui.import.users_will', { role: t('ui.user.role.' + role), area: area.trim() || '*' })}
          <span class="ltr"> ${userList.map(u => u.username + (u.active ? '' : ' ✕')).join(', ')}</span></p>`}
        <pre class="code">${prev.text}</pre>
      </div></div>`}
  </div>`;
}

export function ConfigView({ tab }) {
  useDocTitle(t('ui.nav.config'));
  return html`<div class="page">
    <div class="tabs">
      <a href="#/config/edit" class=${tab === 'edit' ? 'on' : ''}>monitors.conf</a>
      <a href="#/config/history" class=${tab === 'history' ? 'on' : ''}>${t('ui.config.history')}</a>
      <a href="#/config/import" class=${tab === 'import' ? 'on' : ''}>${t('ui.import.tab')}</a>
    </div>
    ${tab === 'history' ? html`<${HistoryTab} />` : tab === 'import' ? html`<${ImportTab} />` : html`<${EditTab} />`}
  </div>`;
}
