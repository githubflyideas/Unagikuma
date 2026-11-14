// Alerts and notifications › Time periods: the weekly hours a channel can
// be limited to (PERIOD records), made from days and times instead of
// written by hand.
import { html, useState } from '../lib.js';
import { t } from '../i18n.js';
import { get, post, del, me } from '../api.js';
import { useFetch, Loading, ErrorBox, Empty, Field, Modal, toast, toastError, errText, confirmDialog } from '../ui.js';

const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];

// daysText writes the ticked days the way a rule does: runs of three or
// more as a range (mon-fri), the rest as a list.
function daysText(on) {
  if (on.every(Boolean)) return 'day';
  const parts = [];
  for (let i = 0; i < 7;) {
    if (!on[i]) { i++; continue; }
    let j = i;
    while (j + 1 < 7 && on[j + 1]) j++;
    parts.push(j - i >= 2 ? DAYS[i] + '-' + DAYS[j] : DAYS.slice(i, j + 1).join(','));
    i = j + 1;
  }
  return parts.join(',');
}

// parseDays reads a rule's days back into seven ticks; null when it is
// written in a way the form cannot show.
function parseDays(s) {
  s = s.trim().toLowerCase();
  if (s === 'day' || s === 'daily') return DAYS.map(() => true);
  const on = DAYS.map(() => false);
  for (const p of s.split(',')) {
    const [a, b] = p.trim().split('-');
    const from = DAYS.indexOf(a), to = b === undefined ? from : DAYS.indexOf(b);
    if (from < 0 || to < 0) return null;
    for (let d = from; ; d = (d + 1) % 7) { on[d] = true; if (d === to) break; }
  }
  return on;
}

function parseRule(src) {
  const rows = [];
  for (const part of src.split(';').map(x => x.trim()).filter(Boolean)) {
    const [days, span = '00:00-24:00'] = part.split(/\s+/, 2);
    const on = parseDays(days);
    const [from, to] = span.split('-');
    if (!on || !to) return null;
    rows.push({ on, from, to });
  }
  return rows.length ? rows : null;
}

const ruleOf = (rows) => rows.filter(r => r.on.some(Boolean)).map(r => daysText(r.on) + ' ' + r.from + '-' + r.to).join('; ');

function PeriodForm({ initial, onClose, onSaved }) {
  const [name, setName] = useState(initial ? initial.name : '');
  const parsed = initial ? parseRule(initial.rule) : [{ on: [true, true, true, true, true, false, false], from: '09:00', to: '18:00' }];
  const [rows, setRows] = useState(parsed || []);
  const [raw, setRaw] = useState(parsed ? '' : initial.rule);
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const setRow = (i, r) => setRows(rows.map((x, j) => j === i ? { ...x, ...r } : x));
  const rule = raw || ruleOf(rows);
  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await post('/periods', { name, rule });
      toast(t('ui.saved'));
      onSaved();
    } catch (e2) { setErr(e2); }
    setBusy(false);
  };
  return html`<${Modal} title=${initial ? t('ui.per.edit', { name }) : t('ui.per.add')} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      <${Field} label=${t('ui.name')} hint=${t('ui.per.name_hint')}><input type="text" class="ltr" value=${name} onInput=${(e) => setName(e.target.value)} required disabled=${!!initial} pattern="[A-Za-z0-9][A-Za-z0-9_.\\-]{0,62}" placeholder="office" /><//>
      ${raw ? html`<${Field} label=${t('ui.per.rule')}><input type="text" class="ltr" value=${raw} onInput=${(e) => setRaw(e.target.value)} /><//>`
        : html`${rows.map((r, i) => html`<div class="period-row">
            <div class="seg sm" role="group" aria-label=${t('ui.per.days')}>${DAYS.map((d, k) => html`<button type="button" aria-pressed=${String(r.on[k])} onClick=${() => setRow(i, { on: r.on.map((v, j) => j === k ? !v : v) })}>${t('ui.per.day.' + d)}</button>`)}</div>
            <input type="time" value=${r.from} onInput=${(e) => setRow(i, { from: e.target.value })} aria-label=${t('ui.per.from')} required />
            <span>–</span>
            <input type="time" value=${r.to === '24:00' ? '23:59' : r.to} onInput=${(e) => setRow(i, { to: e.target.value })} aria-label=${t('ui.per.to')} required />
            ${rows.length > 1 && html`<button type="button" class="btn sm ghost" onClick=${() => setRows(rows.filter((_, j) => j !== i))} aria-label=${t('ui.delete')}>✕</button>`}
          </div>`)}
          <div><button type="button" class="btn sm" onClick=${() => setRows([...rows, { on: [false, false, false, false, false, true, false], from: '10:00', to: '14:00' }])}>${t('ui.per.add_row')}</button></div>`}
      <p class="small muted" style=${{ margin: 0 }}>${t('ui.per.overnight')} <code class="ltr">${rule}</code></p>
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary" disabled=${busy || !rule}>${t('ui.save')}</button></div>
    </form>
  <//>`;
}

// humanRule shows a rule with the days in the reader's language.
function humanRule(src) {
  const rows = parseRule(src);
  if (!rows) return src;
  return rows.map(r => {
    const on = r.on;
    const days = on.every(Boolean) ? t('ui.per.every_day') : daysText(on).split(',').map(p => p.split('-').map(d => t('ui.per.day.' + d)).join('–')).join(', ');
    return days + ' ' + r.from + '–' + r.to;
  }).join('; ');
}

export function PeriodsTab() {
  const list = useFetch(() => get('/periods'), []);
  const [edit, setEdit] = useState(null);
  const admin = me.role === 'admin';
  const remove = async (p) => {
    if (!await confirmDialog(t('ui.per.delete_confirm', { name: p.name }), { danger: true, ok: t('ui.delete') })) return;
    try { await del('/periods/' + encodeURIComponent(p.name)); list.reload(); } catch (e) { toastError(e); }
  };
  if (!list.data) return list.error ? html`<${ErrorBox} error=${list.error} />` : html`<${Loading} />`;
  return html`<div class="col" style=${{ gap: '12px' }}>
    <div class="row"><p class="muted small spacer" style=${{ margin: 0 }}>${t('ui.per.intro')}</p>
      ${admin && html`<button class="btn primary" onClick=${() => setEdit({})}>${t('ui.per.add')}</button>`}</div>
    <div class="card"><div class="bd flush tbl-wrap">
      ${!list.data.items.length ? html`<${Empty} title=${t('ui.per.none')} />` : html`<table class="tbl">
        <thead><tr><th>${t('ui.name')}</th><th>${t('ui.per.rule')}</th><th></th></tr></thead>
        <tbody>${list.data.items.map(p => html`<tr>
          <td class="name ltr">${p.name}</td>
          <td>${humanRule(p.rule)}<div class="small muted ltr">${p.rule}</div></td>
          <td class="right nowrap">${admin && html`<button class="btn sm" onClick=${() => setEdit(p)}>${t('ui.edit')}</button>
            <button class="btn sm danger" onClick=${() => remove(p)}>${t('ui.delete')}</button>`}</td>
        </tr>`)}</tbody></table>`}
    </div></div>
    ${edit && html`<${PeriodForm} initial=${edit.name ? edit : null} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); list.reload(); }} />`}
  </div>`;
}
