// On call: who is on call now in each rotation, a month or week calendar of
// the shifts (stand-ins and swaps applied), and how fast incidents were
// acknowledged and resolved. A rotation is a contact group channel with
// rotate= set: each member is one person's channel (their SMS, their chat);
// it notifies the member on call, and the others only if that one cannot be
// reached.
import { html, useState, useMemo } from '../lib.js';
import { t, dur, fmtNum, currentLang } from '../i18n.js';
import { get, post, del } from '../api.js';
import { useFetch, useLive, Loading, ErrorBox, Empty, Field, Modal, Card, Seg, toast, toastError, errText, confirmDialog, useDocTitle, teamTitle } from '../ui.js';

const pad = (n) => String(n).padStart(2, '0');
function toLocal(d) {
  d = new Date(d);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
function when(d) {
  d = new Date(d);
  return d.toLocaleDateString(currentLang(), { month: 'short', day: 'numeric', weekday: 'short' }) + ' ' + d.toLocaleTimeString(currentLang(), { hour: '2-digit', minute: '2-digit' });
}
const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
// handoffText shows "mon 09:00" with the day in the UI language.
function handoffText(h) {
  const [d, time] = h.split(' ');
  return time && DAYS.includes(d.slice(0, 3).toLowerCase()) ? t('ui.oc.day.' + d.slice(0, 3).toLowerCase()) + ' ' + time : h;
}

// who names a member: the people its channel reaches, else the channel.
function who(rot, member) {
  const m = rot.members.find(x => x.channel === member);
  return m && m.people.length ? m.people.join(', ') : member;
}

function NewRotation({ channels, onClose, onSaved }) {
  const today = new Date();
  const [f, setF] = useState({ name: 'oncall', members: [], every: 'weekly', day: 'mon', time: '09:00',
    start: `${today.getFullYear()}-${pad(today.getMonth() + 1)}-${pad(today.getDate())}`, tz: browserZone() });
  const [err, setErr] = useState(null);
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const toggle = (name) => setF({ ...f, members: f.members.includes(name) ? f.members.filter(x => x !== name) : [...f.members, name] });
  const save = async (e) => {
    e.preventDefault();
    setErr(null);
    const opts = { members: f.members.join(','), rotate: f.every, handoff: (f.every === 'weekly' ? f.day + ' ' : '') + f.time, start: f.start };
    if (f.tz.trim()) opts.tz = f.tz.trim();
    try {
      await post('/channels', { name: f.name.trim(), kind: 'group', opts });
      toast(t('ui.saved'));
      onSaved();
    } catch (e2) { setErr(e2); }
  };
  const usable = channels.filter(c => c.kind !== 'group');
  return html`<${Modal} title=${t('ui.oc.new')} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      <${Field} label=${t('ui.oc.name')} hint=${t('ui.oc.name_hint')}><input type="text" class="ltr" value=${f.name} onInput=${set('name')} required pattern="[A-Za-z0-9][A-Za-z0-9._\\-]*" /><//>
      <${Field} label=${t('ui.oc.members')} hint=${t('ui.oc.members_hint')}>
        ${usable.length ? html`<div class="col" style=${{ gap: '4px' }}>${usable.map(c => html`<label class="switch">
          <input type="checkbox" checked=${f.members.includes(c.name)} onChange=${() => toggle(c.name)} />
          <span class="ltr">${c.name}</span> <span class="small muted">${c.kind}${f.members.includes(c.name) ? ' · #' + (f.members.indexOf(c.name) + 1) : ''}</span></label>`)}</div>`
        : html`<p class="small warn-text">${t('ui.oc.no_channels')}</p>`}<//>
      <div class="two">
        <${Field} label=${t('ui.oc.every')}><select value=${f.every} onChange=${set('every')}>
          <option value="daily">${t('ui.oc.daily')}</option><option value="weekly">${t('ui.oc.weekly')}</option></select><//>
        <${Field} label=${t('ui.oc.handoff')}><div class="row nowrap">
          ${f.every === 'weekly' && html`<select value=${f.day} onChange=${set('day')} style=${{ width: 'auto' }}>${DAYS.map(d => html`<option value=${d}>${t('ui.oc.day.' + d)}</option>`)}</select>`}
          <input type="time" value=${f.time} onInput=${set('time')} required /></div><//>
      </div>
      <div class="two">
        <${Field} label=${t('ui.oc.start')} hint=${t('ui.oc.start_hint')}><input type="date" value=${f.start} onInput=${set('start')} required /><//>
        <${Field} label=${t('ui.field.tz')} hint=${t('ui.oc.tz_hint')}><input type="text" class="ltr" value=${f.tz} onInput=${set('tz')} placeholder="Asia/Shanghai" /><//>
      </div>
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary" disabled=${f.members.length < 2}>${t('ui.save')}</button></div>
    </form>
  <//>`;
}

function StandIn({ rot, seg, onClose, onSaved }) {
  const now = new Date();
  const end = rot.scheduled && new Date(rot.scheduled.until) > now ? new Date(rot.scheduled.until) : new Date(now.getTime() + 86400e3);
  // From a calendar shift: that shift, from now on when it has begun.
  const from = seg ? new Date(Math.max(now.getTime(), new Date(seg.from).getTime())) : now;
  const until = seg ? new Date(seg.until) : end;
  const busy = seg ? seg.member : rot.now;
  const others = rot.members.filter(m => m.channel !== busy);
  const [f, setF] = useState({ member: (others[0] || rot.members[0]).channel, from: toLocal(from), until: toLocal(until), note: '' });
  const [err, setErr] = useState(null);
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const save = async (e) => {
    e.preventDefault();
    setErr(null);
    try {
      await post('/oncall/overrides', { rotation: rot.name, member: f.member, from: new Date(f.from).toISOString(), until: new Date(f.until).toISOString(), note: f.note.trim() });
      toast(t('ui.saved'));
      onSaved();
    } catch (e2) { setErr(e2); }
  };
  return html`<${Modal} title=${t('ui.oc.standin_title', { name: rot.name })} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      <p class="small muted" style=${{ marginTop: 0 }}>${t('ui.oc.standin_intro')}</p>
      <${Field} label=${t('ui.oc.standin_who')}><select value=${f.member} onChange=${set('member')}>
        ${rot.members.map(m => html`<option value=${m.channel}>${who(rot, m.channel)}${m.people.length ? ' (' + m.channel + ')' : ''}</option>`)}</select><//>
      <div class="two">
        <${Field} label=${t('ui.oc.from')}><input type="datetime-local" value=${f.from} onInput=${set('from')} required /><//>
        <${Field} label=${t('ui.oc.until')}><input type="datetime-local" value=${f.until} onInput=${set('until')} required /><//>
      </div>
      <${Field} label=${t('ui.oc.note')}><input type="text" value=${f.note} onInput=${set('note')} placeholder=${t('ui.oc.note_ph')} /><//>
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary">${t('ui.save')}</button></div>
    </form>
  <//>`;
}

// Swap trades two shifts: each member covers the other's, as a pair of
// stand-ins; the order of the rotation stays.
function Swap({ rot, onClose, onSaved }) {
  const shifts = rot.shifts.filter(x => new Date(x.until) > new Date());
  const [a, setA] = useState(0);
  const [b, setB] = useState(Math.max(0, shifts.findIndex(x => x.member !== (shifts[0] || {}).member)));
  const [note, setNote] = useState('');
  const [err, setErr] = useState(null);
  const label = (x) => when(x.from) + ' – ' + when(x.until) + ' · ' + who(rot, x.member);
  const same = shifts[a] && shifts[b] && shifts[a].member === shifts[b].member;
  const save = async (e) => {
    e.preventDefault();
    setErr(null);
    try {
      await post('/oncall/swap', { rotation: rot.name, a: shifts[a].from, b: shifts[b].from, note: note.trim() });
      toast(t('ui.oc.swapped'));
      onSaved();
    } catch (e2) { setErr(e2); }
  };
  return html`<${Modal} title=${t('ui.oc.swap_title', { name: rot.name })} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      <p class="small muted" style=${{ marginTop: 0 }}>${t('ui.oc.swap_intro')}</p>
      <${Field} label=${t('ui.oc.swap_a')}><select value=${a} onChange=${(e) => setA(+e.target.value)}>
        ${shifts.map((x, i) => html`<option value=${i}>${label(x)}</option>`)}</select><//>
      <${Field} label=${t('ui.oc.swap_b')}><select value=${b} onChange=${(e) => setB(+e.target.value)}>
        ${shifts.map((x, i) => html`<option value=${i}>${label(x)}</option>`)}</select><//>
      <${Field} label=${t('ui.oc.note')}><input type="text" value=${note} onInput=${(e) => setNote(e.target.value)} placeholder=${t('ui.oc.note_ph')} /><//>
      ${same && html`<div class="banner warning small">${t('ui.oc.swap_same')}</div>`}
      ${err && html`<div class="banner error">${errText(err)}</div>`}
      <div class="row"><div class="spacer"></div>
        <button type="button" class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
        <button class="btn primary" disabled=${!shifts.length || same}>${t('ui.oc.swap')}</button></div>
    </form>
  <//>`;
}

function browserZone() {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch { return ''; }
}

// The calendar range: six weeks around a month, or one week, from Monday.
function calRange(mode, anchor) {
  const a = new Date(anchor.getFullYear(), anchor.getMonth(), mode === 'month' ? 1 : anchor.getDate());
  const start = new Date(a.getFullYear(), a.getMonth(), a.getDate() - ((a.getDay() + 6) % 7));
  const days = mode === 'month' ? 42 : 7;
  return { start, end: new Date(start.getFullYear(), start.getMonth(), start.getDate() + days), days };
}
function addDays(d, n) { return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n); }
const hm = (d) => d.toLocaleTimeString(currentLang(), { hour: '2-digit', minute: '2-digit' });

// Calendar shows, day by day, who is on call: one colour per member, a
// dashed edge on stand-ins, the handoff time where a shift begins mid-day.
function Calendar({ rot, cal, mode, anchor, onPick }) {
  const { start, days } = calRange(mode, anchor);
  const colour = (m) => 'oc-c' + (Math.max(0, rot.members.findIndex(x => x.channel === m)) % 8);
  const segs = (cal && cal.segments) || [];
  const today = new Date();
  const heads = Array.from({ length: 7 }, (_, i) => addDays(start, i).toLocaleDateString(currentLang(), { weekday: 'short' }));
  const cells = Array.from({ length: days }, (_, i) => {
    const ds = addDays(start, i), de = addDays(start, i + 1);
    // In the month view a shift carried over from the day before that
    // ends in the morning is left out: the day shows who takes over.
    const noon = new Date(ds.getFullYear(), ds.getMonth(), ds.getDate(), 12);
    const items = segs.filter(x => new Date(x.from) < de && new Date(x.until) > ds &&
      !(mode === 'month' && !x.stand_in && new Date(x.from) < ds && new Date(x.until) <= noon && segs.some(y => new Date(y.from) >= new Date(x.until) && new Date(y.from) < de)));
    const isToday = ds <= today && today < de;
    const other = mode === 'month' && ds.getMonth() !== anchor.getMonth();
    return html`<div class=${'oc-day' + (isToday ? ' today' : '') + (other ? ' other' : '')}>
      <div class="oc-date">${ds.toLocaleDateString(currentLang(), mode === 'week' ? { month: 'short', day: 'numeric' } : { day: 'numeric' })}</div>
      ${items.map(x => {
        const f = new Date(x.from), u = new Date(x.until);
        const startsHere = f > ds, endsHere = u < de;
        const time = mode === 'week' ? (startsHere || endsHere ? hm(f > ds ? f : ds) + '–' + (u < de ? hm(u) : '24:00') : '') : (startsHere ? hm(f) : '');
        const over = u <= today;
        const title = who(rot, x.member) + ' (' + x.member + ') · ' + when(x.from) + ' – ' + when(x.until) +
          (x.stand_in ? ' · ' + t('ui.oc.standing_in', { name: who(rot, x.scheduled) }) + (x.note ? ' · ' + x.note : '') : '');
        return html`<button type="button" class=${'oc-seg ' + colour(x.member) + (x.stand_in ? ' standin' : '') + (over ? ' over' : '')} title=${title} disabled=${over} onClick=${() => onPick(x)}>
          ${time && html`<span class="oc-time">${time}</span>`}<span class="ellipsis">${who(rot, x.member)}</span></button>`;
      })}
    </div>`;
  });
  return html`<div class=${'oc-cal ' + mode}>
    ${heads.map(h => html`<div class="oc-head">${h}</div>`)}
    ${cells}
  </div>
  <div class="oc-legend">${rot.members.map(m => html`<span><i class=${'oc-dot ' + colour(m.channel)}></i>${m.people.length ? html`${m.people.join(', ')} <span class="muted ltr">${m.channel}${m.kind ? ' · ' + m.kind : ''}</span>` : html`<span class="ltr">${m.channel}</span>${m.kind && html` <span class="muted">${m.kind}</span>`}`}</span>`)}
    <span><i class="oc-dot standin"></i>${t('ui.oc.standins')}</span></div>`;
}

function Rotation({ rot, reload, cal, mode, anchor }) {
  const [standIn, setStandIn] = useState(false);
  const [swap, setSwap] = useState(false);
  const cancel = async (o) => {
    if (!await confirmDialog(t('ui.oc.cancel_confirm', { name: who(rot, o.member) }), { danger: true, ok: t('ui.oc.cancel') })) return;
    try { await del('/oncall/overrides/' + o.id); reload(); } catch (e) { toastError(e); }
  };
  const until = rot.stand_in ? rot.stand_in.until : rot.scheduled.until;
  return html`<div class="card oc-rot">
    <div class="hd"><h3 class="ltr">${rot.name}</h3>
      <span class="small muted">${t('ui.oc.plan', { every: ['daily', 'weekly'].includes(rot.rotate) ? t('ui.oc.' + rot.rotate) : rot.rotate, handoff: handoffText(rot.handoff) })}${rot.tz ? ' · ' + rot.tz : ''}</span>
      <div class="spacer"></div>
      <button class="btn sm" onClick=${() => setStandIn(true)}>${t('ui.oc.standin')}</button>
      <button class="btn sm" onClick=${() => setSwap(true)} disabled=${rot.members.length < 2}>${t('ui.oc.swap')}</button>
      <a class="btn sm" href="#/alerts/channels">${t('ui.edit')}</a></div>
    <div class="bd">
      <div class="oc-now">
        <span class="small muted">${t('ui.oc.now')}</span>
        <b>${who(rot, rot.now)}</b>
        <span class="small muted">${t('ui.oc.until_when', { when: when(until) })}</span>
        ${rot.stand_in && html`<span class="tag warn">${t('ui.oc.standing_in', { name: who(rot, rot.scheduled.member) })}</span>`}
      </div>
      ${!rot.policies.length && html`<div class="banner warning small">${t('ui.oc.unused')}</div>`}
      ${rot.policies.length > 0 && html`<p class="small muted" style=${{ margin: '8px 0 0' }}>${t('ui.oc.used_by', { policies: rot.policies.map(p => (p.startsWith('team:') ? teamTitle({ name: p.slice(5) }) : p === 'system' ? t('ui.mon.route_system') : p)).join(', ') })}</p>`}
    </div>
    <div class="bd">
      <${Calendar} rot=${rot} cal=${cal} mode=${mode} anchor=${anchor} onPick=${(x) => setStandIn(x)} />
      <p class="small muted" style=${{ margin: '8px 0 0' }}>${t('ui.oc.cal_hint')}</p>
    </div>
    ${rot.overrides.length > 0 && html`<div class="bd"><h4 class="small" style=${{ margin: '0 0 6px' }}>${t('ui.oc.standins')}</h4>
      ${rot.overrides.map(o => html`<div class="row small" style=${{ padding: '4px 0' }}>
        <span class="spacer"><b>${who(rot, o.member)}</b> · ${when(o.from)} – ${when(o.until)}${o.note ? ' · ' + o.note : ''} <span class="muted">· ${o.by}</span></span>
        <button class="btn sm" onClick=${() => cancel(o)}>${t('ui.oc.cancel')}</button></div>`)}</div>`}
    ${standIn && html`<${StandIn} rot=${rot} seg=${standIn === true ? null : standIn} onClose=${() => setStandIn(false)} onSaved=${() => { setStandIn(false); reload(); }} />`}
    ${swap && html`<${Swap} rot=${rot} onClose=${() => setSwap(false)} onSaved=${() => { setSwap(false); reload(); }} />`}
  </div>`;
}

function secs(v, n) { return n ? dur(v) : '–'; }

function Stats({ stats, rots }) {
  const days = Math.round((new Date(stats.to) - new Date(stats.from)) / 86400e3);
  const a = stats.all;
  const rotOf = (name) => rots.find(r => r.name === name);
  return html`<${Card} title=${t('ui.oc.stats', { n: fmtNum(days) })} flush>
    <div class="oc-kpis">
      <div><b>${fmtNum(a.incidents)}</b><span>${t('ui.oc.incidents')}</span></div>
      <div><b>${secs(a.mtta_s, a.acked)}</b><span>MTTA · ${t('ui.oc.mtta')}</span></div>
      <div><b>${secs(a.mttr_s, a.resolved)}</b><span>MTTR · ${t('ui.oc.mttr')}</span></div>
    </div>
    ${stats.members.length > 0 && html`<div class="tbl-wrap"><table class="tbl">
      <thead><tr><th>${t('ui.oc.member')}</th><th class="right">${t('ui.oc.incidents')}</th><th class="right">${t('ui.oc.acked')}</th><th class="right">MTTA</th><th class="right">MTTR</th></tr></thead>
      <tbody>${stats.members.map(m => html`<tr>
        <td>${rotOf(m.rotation) ? who(rotOf(m.rotation), m.member) : m.member} <span class="small muted ltr">${m.rotation}</span></td>
        <td class="right">${fmtNum(m.incidents)}</td><td class="right">${fmtNum(m.acked)}</td>
        <td class="right">${secs(m.mtta_s, m.acked)}</td><td class="right">${secs(m.mttr_s, m.resolved)}</td>
      </tr>`)}</tbody></table></div>`}
    <p class="small muted" style=${{ margin: '8px 16px 12px' }}>${t('ui.oc.stats_hint')}</p>
  <//>`;
}

export function OnCall() {
  useDocTitle(t('ui.nav.oncall'));
  const d = useFetch(() => get('/oncall'), []);
  const chans = useFetch(() => get('/channels'), []);
  const [creating, setCreating] = useState(false);
  const [mode, setMode] = useState(() => { try { return localStorage.getItem('unagikuma.ocview') || 'month'; } catch { return 'month'; } });
  const [anchor, setAnchor] = useState(() => new Date());
  const range = useMemo(() => calRange(mode, anchor), [mode, anchor.getFullYear(), anchor.getMonth(), anchor.getDate()]);
  // From the day before, so a shift running into the first day shows as
  // carried over rather than starting there.
  const cal = useFetch(() => get('/oncall/calendar?from=' + encodeURIComponent(addDays(range.start, -1).toISOString()) + '&to=' + encodeURIComponent(range.end.toISOString())), [range.start.getTime(), range.end.getTime()]);
  const reloadAll = () => { d.reload(); cal.reload(); };
  useLive(['oncall', 'serial', 'directory', 'incident'], reloadAll, 800);
  const changeMode = (m) => { setMode(m); try { localStorage.setItem('unagikuma.ocview', m); } catch { /* unavailable */ } };
  const step = (n) => setAnchor(mode === 'month' ? new Date(anchor.getFullYear(), anchor.getMonth() + n, 1) : addDays(anchor, 7 * n));
  const title = mode === 'month' ? anchor.toLocaleDateString(currentLang(), { year: 'numeric', month: 'long' })
    : range.start.toLocaleDateString(currentLang(), { month: 'short', day: 'numeric' }) + ' – ' + addDays(range.end, -1).toLocaleDateString(currentLang(), { year: 'numeric', month: 'short', day: 'numeric' });
  const calOf = (name) => cal.data && cal.data.rotations.find(x => x.name === name);
  if (!d.data) return html`<div class="page">${d.error ? html`<${ErrorBox} error=${d.error} retry=${d.reload} />` : html`<${Loading} />`}</div>`;
  const rots = d.data.rotations;
  return html`<div class="page">
    <div class="row"><p class="muted small spacer" style=${{ margin: 0 }}>${t('ui.oc.intro')}</p>
      <button class="btn primary" onClick=${() => setCreating(true)} disabled=${!chans.data}>${t('ui.oc.new')}</button></div>
    ${!rots.length ? html`<div class="card"><${Empty} title=${t('ui.oc.none')}><p>${t('ui.oc.none_hint')}</p>
        <button class="btn primary" onClick=${() => setCreating(true)} disabled=${!chans.data}>${t('ui.oc.new')}</button><//></div>`
      : html`<div class="row oc-bar">
          <${Seg} value=${mode} onChange=${changeMode} options=${[['month', t('ui.oc.month')], ['week', t('ui.oc.week')]]} />
          <button class="btn sm" onClick=${() => step(-1)} aria-label=${t('ui.oc.prev')} title=${t('ui.oc.prev')}>‹</button>
          <button class="btn sm" onClick=${() => setAnchor(new Date())}>${t('ui.oc.today')}</button>
          <button class="btn sm" onClick=${() => step(1)} aria-label=${t('ui.oc.next')} title=${t('ui.oc.next')}>›</button>
          <b>${title}</b><div class="spacer"></div>
          <span class="small muted">${t('ui.oc.your_tz', { tz: browserZone() || '—' })}</span>
        </div>
        <${ErrorBox} error=${cal.error} retry=${cal.reload} />
        ${rots.map(r => html`<${Rotation} key=${r.name} rot=${r} reload=${reloadAll} cal=${calOf(r.name)} mode=${mode} anchor=${anchor} />`)}`}
    <${Stats} stats=${d.data.stats} rots=${rots} />
    ${creating && chans.data && html`<${NewRotation} channels=${chans.data} onClose=${() => setCreating(false)} onSaved=${() => { setCreating(false); d.reload(); chans.reload(); }} />`}
  </div>`;
}
