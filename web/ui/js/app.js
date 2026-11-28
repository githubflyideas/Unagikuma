// Application shell: session, navigation and routing.
import { html, render, useState, useEffect, useRef } from './lib.js';
import { t, tn, setLang, detectLang, loadLanguages, langList, currentLang, fmtTime, detailText } from './i18n.js';
import { get, post, del, setCSRF, setMe, me, onUnauthorized } from './api.js';
import { startLive, stopLive, onConnection } from './live.js';
import { useRoute, href, Toasts, ConfirmHost, useFetch, useLive, errText, Field, replaceRoute, monitorsPath, MON_SORTS, PRODUCT, Modal } from './ui.js';
import { Overview } from './views/overview.js';
import { Monitors } from './views/monitors.js';
import { MonitorDetail } from './views/monitor.js';
import { MonitorEditor } from './views/editor.js';
import { Problems, setupIssues } from './views/problems.js';
import { Expiry } from './views/expiry.js';
import { Events } from './views/events.js';
import { ConfigView } from './views/config.js';
import { Alerts } from './views/alerts.js';
import { Notices } from './views/announce.js';
import { Settings, ADVANCED_TABS, Account } from './views/settings.js';
import { Nodes } from './views/nodes.js';
import { IncidentReport } from './views/incident.js';
import { DrawerHost } from './views/drawer.js';
import { PaletteHost, openPalette } from './views/palette.js';
import { loadBrand, loadMyStyle, applyCachedStyle, onStyleLoaded, signedOut, currentStyle, CHROME, setPhone } from './styles.js';
import { storageGet, storageSet } from './storage.js';

// Layouts are chosen in Settings, Style and branding.
// Colour comes from five shared palettes, cycled by clicking the logo.
const PALETTES = ['calm', 'perfume', 'country', 'industrial', 'passion'];
const OLD_PALETTES = { light: 'calm', paper: 'calm', dark: 'industrial', ink: 'industrial',
  mist: 'calm', sport: 'calm', slate: 'industrial', graphite: 'industrial', sky: 'calm',
  violet: 'perfume', brand: 'calm', auto: 'calm', a: 'calm', b: 'country', c: 'industrial' };
function paletteOf(v) {
  v = OLD_PALETTES[v] || v;
  return PALETTES.includes(v) ? v : 'calm';
}
function applyPalette(v) {
  document.documentElement.dataset.theme = paletteOf(v);
}
function nextPalette(v) {
  const cur = paletteOf(v);
  return PALETTES[(PALETTES.indexOf(cur) + 1) % PALETTES.length];
}

// The interface can be shown larger, as a whole: text, icons, charts and
// tables keep their proportions.
const ZOOMS = [100, 125];
function zoomOf(v) { const n = Number(v); return ZOOMS.includes(n) ? n : 100; }
// Scaling the page with CSS zoom leaves the layout's width breakpoints at
// the window's real width; scale them too, so a larger interface switches
// to the narrower layouts the way the browser's own zoom does.
const mediaText = new WeakMap();
function scaleBreakpoints(f) {
  for (const sheet of document.styleSheets) {
    let rules;
    try { rules = sheet.cssRules; } catch { continue; } // another origin
    for (const r of rules) {
      if (!(r instanceof CSSMediaRule)) continue;
      if (!mediaText.has(r)) mediaText.set(r, r.media.mediaText);
      const orig = mediaText.get(r);
      if (!/width/.test(orig)) continue;
      r.media.mediaText = orig.replace(/(\d+(?:\.\d+)?)px/g, (_, n) => Math.round(Number(n) * f) + 'px');
    }
  }
}
function zoomFactor() { return zoomOf(storage('unagikuma.zoom')) / 100; }
onStyleLoaded(() => scaleBreakpoints(zoomFactor()));
function applyZoom(v) {
  const z = zoomOf(v);
  document.documentElement.style.zoom = z === 100 ? '' : String(z / 100);
  // Viewport units do not shrink with the zoom: sizes that must fit the
  // window divide by it.
  document.documentElement.style.setProperty('--zoom', String(z / 100));
  scaleBreakpoints(z / 100);
}

function ZoomSelect() {
  const [z, setZ] = useState(zoomOf(storage('unagikuma.zoom')));
  const change = (v) => { setZ(zoomOf(v)); storage('unagikuma.zoom', String(v)); applyZoom(v); };
  return html`<select class="zoom" aria-label=${t('ui.zoom')} title=${t('ui.zoom')} value=${String(z)} onChange=${(e) => change(e.target.value)}>
    ${ZOOMS.map(n => html`<option value=${String(n)}>${n}%</option>`)}
  </select>`;
}

function storage(k, v) {
  if (v === undefined) return storageGet(k);
  storageSet(k, v);
  return null;
}

function localZone() {
  try { return Intl.DateTimeFormat('en').resolvedOptions().timeZone; } catch { return ''; }
}

function Logo() { return html`<img src="logo.png" alt="" />`; }

function LangSelect({ onChange }) {
  return html`<select aria-label=${t('ui.language')} value=${currentLang()} onChange=${(e) => onChange(e.target.value)}>
    ${langList().map(l => html`<option value=${l.code}>${l.name}</option>`)}
  </select>`;
}

// ---- setup and login ----

function Setup({ local, onDone, onLang }) {
  const [f, setF] = useState({ username: 'admin', password: '', confirm: '', token: '' });
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async (e) => {
    e.preventDefault();
    if (f.password.length < 8) return setErr(t('ui.setup.short'));
    if (f.password !== f.confirm) return setErr(t('ui.setup.mismatch'));
    setBusy(true);
    try {
      const r = await post('/setup', { username: f.username, password: f.password, token: f.token, lang: currentLang(), timezone: localZone() });
      onDone(r);
    } catch (e2) {
      // The only thing this form can be refused for is the token: say so,
      // instead of the general "no permission".
      setErr(e2 && e2.code === 'forbidden' ? t('ui.setup.bad_token') : errText(e2));
    }
    setBusy(false);
  };
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  return html`<div class="auth"><div>
    <div class="logo"><${Logo} /> ${PRODUCT}</div>
    <form class="card" onSubmit=${submit}>
      <div class="hd"><h2>${t('ui.setup.title')}</h2><div class="spacer"></div><${LangSelect} onChange=${onLang} /></div>
      <div class="bd form">
        <p class="muted small">${t('ui.setup.intro')}</p>
        ${!local && html`<${Field} label=${t('ui.setup.token')} hint=${t('ui.setup.token_hint')}><input type="text" class="ltr" value=${f.token} onInput=${set('token')} required autocomplete="off" /><//>`}
        <${Field} label=${t('ui.username')}><input type="text" value=${f.username} onInput=${set('username')} required autocomplete="username" /><//>
        <${Field} label=${t('ui.password')} hint=${t('ui.setup.short')}><input type="password" value=${f.password} onInput=${set('password')} required autocomplete="new-password" /><//>
        <${Field} label=${t('ui.setup.confirm')}><input type="password" value=${f.confirm} onInput=${set('confirm')} required autocomplete="new-password" /><//>
        ${err && html`<div class="banner error">${err}</div>`}
        <button class="btn primary" disabled=${busy}>${t('ui.setup.create')}</button>
      </div>
    </form>
  </div></div>`;
}

function Login({ onDone, onLang }) {
  const [f, setF] = useState({ username: '', password: '' });
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    try {
      onDone(await post('/session', f));
    } catch (e2) {
      setErr(e2.status === 401 ? t('ui.login.wrong') : errText(e2));
    }
    setBusy(false);
  };
  return html`<div class="auth"><div>
    <div class="logo"><${Logo} /> ${PRODUCT}</div>
    <form class="card" onSubmit=${submit}>
      <div class="hd"><h2>${t('ui.login.title')}</h2><div class="spacer"></div><${LangSelect} onChange=${onLang} /></div>
      <div class="bd form">
        <${Field} label=${t('ui.username')}><input type="text" value=${f.username} onInput=${(e) => setF({ ...f, username: e.target.value })} required autofocus autocomplete="username" /><//>
        <${Field} label=${t('ui.password')}><input type="password" value=${f.password} onInput=${(e) => setF({ ...f, password: e.target.value })} required autocomplete="current-password" /><//>
        ${err && html`<div class="banner error">${err}</div>`}
        <button class="btn primary" disabled=${busy}>${t('ui.login.submit')}</button>
        <p class="muted small">${t('ui.login.forgot')} <code class="ltr">unagikuma reset-password</code></p>
      </div>
    </form>
  </div></div>`;
}

// ---- shell ----

const NAV = [
  ['/', 'ui.nav.overview', '◎'],
  ['/monitors', 'ui.nav.monitors', '☰'],
  ['/problems', 'ui.nav.problems', '⚠'],
  ['/events', 'ui.nav.events', '≡'],
  ['/alerts', 'ui.nav.alerts', '✉'],
  ['/notices', 'ui.nav.notices', '⚑'],
  ['/config', 'ui.nav.config', '{}'],
  ['/settings', 'ui.nav.settings', '⚙'],
  ['/advanced', 'ui.nav.advanced', '⚒'],
  ['/nodes', 'ui.nav.runmode', '⛭'],
];

// A phone shows a short menu: monitors, alerts and notifications, settings.
// Any other address opens the monitor list there.
const PHONE_NAV = new Set(['/monitors', '/alerts', '/settings']);
function isPhone() { return matchMedia(`(max-width: ${Math.round(860 * zoomFactor())}px)`).matches; }
function usePhone() {
  const [phone, set] = useState(() => { const p = isPhone(); setPhone(p); return p; });
  useEffect(() => {
    const f = () => { const p = isPhone(); setPhone(p); set(p); };
    addEventListener('resize', f);
    return () => removeEventListener('resize', f);
  }, []);
  return phone;
}

const LIMITED_NAV = new Set(['/', '/monitors', '/problems', '/events']);

const OLD_ALERT_TABS = { notices: 'pages', report: 'report' };

function Shell({ user, onLogout, onLang }) {
  const route = useRoute();
  const phone = usePhone();
  const [open, setOpen] = useState(false);
  const [live, setLive] = useState(false);
  const [palette, setPalette] = useState(paletteOf(storage('unagikuma.palette') || storage('unagikuma.theme')));
  const [styleRev, setStyleRev] = useState(0);
  const styleId = currentStyle();
  const chrome = phone ? 'side' : CHROME[styleId] || 'side';
  useEffect(() => {
    const v = storage('unagikuma.palette') || storage('unagikuma.theme');
    if (v && paletteOf(v) && v !== paletteOf(v)) storage('unagikuma.palette', paletteOf(v));
  }, []);
  useEffect(() => onStyleLoaded(() => setStyleRev(n => n + 1)), []);
  const [mini, setMini] = useState(storage('unagikuma.side') === 'mini');
  const [acct, setAcct] = useState(false);
  const ov = useFetch(() => get('/overview'), []);
  useLive(['incident', 'state', 'serial', 'banner', 'directory'], ov.reload, 1500);
  useEffect(() => onConnection(setLive), []);
  useEffect(() => setOpen(false), [route.path]);
  const unhandled = ov.data ? ov.data.unhandled : 0;
  const scriptProblems = ov.data ? ov.data.script_problems || 0 : 0;
  const contactIssues = ov.data ? ov.data.contact_issues || 0 : 0;
  const setup = setupIssues(ov.data);
  const first = route.parts[0] === 'settings' && ADVANCED_TABS.includes(route.parts[1]) ? '/advanced'
    : route.parts[0] === 'alerts' && route.parts[1] === 'changes' ? '/notices' : '/' + (route.parts[0] || '');
  const changePalette = (v) => { setPalette(v); storage('unagikuma.palette', v); applyPalette(v); };
  const cyclePalette = (e) => { e.preventDefault(); e.stopPropagation(); changePalette(nextPalette(palette)); };
  const paletteTip = t('ui.palette.cycle', { name: t('ui.theme.' + palette) });
  const logoBtn = html`<button type="button" class="logo-cycle" title=${paletteTip} aria-label=${paletteTip} onClick=${cyclePalette}><${Logo} /></button>`;
  const edition = html`<div class="edition small muted">${me.registered ? t('ui.edition.registered') : t('ui.edition.unregistered')}${me.version ? ' · ' + me.version : ''}</div>`;
  const brandBlock = (cls) => html`<div class=${cls}>${logoBtn}<div><a href="#/">${PRODUCT}${edition}</a></div></div>`;
  const nodeName = ov.data && ov.data.instance ? html`<div class="host ellipsis" title=${ov.data.instance}>${ov.data.instance}</div>` : null;
  const runMode = html`<${RunMode} ov=${ov.data} live=${live} />`;
  const accountDock = html`<div class="account-dock">
      ${chrome !== 'side' && nodeName}
      ${chrome !== 'side' && runMode}
      <div class="row nowrap lang-zoom"><${LangSelect} onChange=${onLang} /><${ZoomSelect} /></div>
      <div class="row"><button type="button" class="linkish ellipsis" title=${t('ui.acc.open')} onClick=${() => setAcct(true)}>${user}</button><div class="spacer"></div><button class="btn sm ghost" onClick=${onLogout}>${t('ui.logout')}</button></div>
    </div>`;
  // On a wide screen the button folds the sidebar to icons; on a phone it
  // slides the sidebar in.
  const toggleSide = () => {
    if (matchMedia(`(max-width: ${Math.round(860 * zoomFactor())}px)`).matches) return setOpen(!open);
    storage('unagikuma.side', mini ? 'full' : 'mini');
    setMini(!mini);
  };

  const phoneAway = phone && !PHONE_NAV.has(first);
  useEffect(() => { if (phoneAway) replaceRoute('/monitors'); }, [phoneAway]);
  let view;
  const p = route.parts;
  if (phoneAway) view = html`<${Monitors} query=${new URLSearchParams()} />`;
  else switch (p[0]) {
    case undefined: view = html`<${Overview} />`; break;
    case 'problems': view = p[1] === 'expiry' ? html`<${Expiry} />` : html`<${Problems} tab=${p[1] || 'unhandled'} />`; break;
    case 'monitors':
      if (p[1] === 'new') view = html`<${MonitorEditor} />`;
      else if (p[1] && p[2] === 'edit') view = html`<${MonitorEditor} name=${p[1]} />`;
      else if (p[1]) view = html`<${MonitorDetail} name=${p[1]} />`;
      else view = html`<${Monitors} query=${route.query} />`;
      break;
    case 'events': view = html`<${Events} tab=${p[1] || 'events'} />`; break;
    case 'expiry': view = html`<${Expiry} />`; break;
    case 'incidents': view = p[1] ? html`<${IncidentReport} id=${p[1]} />` : html`<${Problems} tab="all" />`; break;
    // Changes, status pages and the report used to live under Alerting;
    // their old addresses still open them.
    case 'alerts': view = OLD_ALERT_TABS[p[1]] || p[1] === 'changes' ? html`<${Notices} tab=${OLD_ALERT_TABS[p[1]] || 'pages'} />` : html`<${Alerts} tab=${['contacts', 'periods'].includes(p[1]) ? p[1] : 'channels'} />`; break;
    // Change notices moved to Alerts and notifications; the old address opens them there.
    case 'notices': view = html`<${Notices} tab=${p[1] === 'changes' ? 'pages' : p[1] || 'pages'} />`; break;
    case 'config': view = html`<${ConfigView} tab=${p[1] || 'import'} />`; break;
    // Nodes used to be a Settings tab; its old address opens Run mode.
    case 'settings': view = p[1] === 'nodes' ? html`<div class="page"><${Nodes} /></div>`
      : p[1] === 'users' ? html`<${Alerts} tab="contacts" />` // logins are in the user list now
      : ADVANCED_TABS.includes(p[1]) ? html`<${Settings} advanced tab=${p[1]} user=${user} />` // old addresses of the advanced tabs
      : html`<${Settings} tab=${p[1] || 'general'} user=${user} phone=${phone} />`; break;
    case 'advanced': view = html`<${Settings} advanced tab=${p[1] || ADVANCED_TABS[0]} user=${user} />`; break;
    case 'nodes': view = html`<div class="page"><${Nodes} /></div>`; break;
    default: view = html`<div class="page"><div class="empty"><b>404</b></div></div>`;
  }
  const banner = ov.data && ov.data.banner;
  // Run mode (single machine, two machines or a cluster, and its nodes)
  // has its own place in the sidebar.
  // An account limited to groups works with their monitors only.
  // Everyone has Settings for their own account (password).
  const nav = NAV.filter(([path]) => (!phone || PHONE_NAV.has(path)) && (!me.groups || LIMITED_NAV.has(path)) && ((path !== '/advanced' && path !== '/settings') || me.role === 'admin'));
  const navLinks = nav.map(([path, key, ico]) => html`<a href=${href(path)} class=${first === path ? 'on' : ''}>
          <span class="ico" title=${mini ? t(key) : ''}>${ico}</span><span class="lbl">${t(key)}</span>
          ${path === '/problems' && setup.length > 0 && html`<span class="rdot setup" style=${{ marginTop: 0, marginInlineStart: 'auto' }} title=${setup.map(x => x.n ? tn(x.key, x.n) : t(x.key)).join('\n')}></span>`}
          ${path === '/problems' && unhandled > 0 && html`<span class="count">${unhandled}</span>`}
          ${path === '/settings' && scriptProblems > 0 && html`<span class="count" title=${t('ui.scripts.badge', { n: scriptProblems })}>${scriptProblems}</span>`}
          ${path === '/alerts' && contactIssues > 0 && html`<span class="rdot" style=${{ marginTop: 0, marginInlineStart: 'auto' }} title=${t('ui.dir.nav_dot', { n: contactIssues })}></span>`}
        </a>`);
  return html`<div class=${'shell chrome-' + chrome + (mini ? ' mini' : '')} data-style-rev=${styleRev}>
    <aside class=${'side' + (open ? ' open' : '')}>
      ${brandBlock('brand')}
      <nav class="nav">${navLinks}</nav>
      <div class="foot">
        ${nodeName}
        ${runMode}
        ${chrome === 'side' && accountDock}
      </div>
      ${acct && html`<${Modal} title=${t('ui.acc.title')} onClose=${() => setAcct(false)}><${Account} user=${user} embedded /><//>`}
    </aside>
    <div class="main">
      <header class="top">
        <button class="btn sm" onClick=${toggleSide} aria-label=${mini ? t('ui.side.expand') : t('ui.side.collapse')} title=${mini ? t('ui.side.expand') : t('ui.side.collapse')}>☰</button>
        ${chrome !== 'side' && brandBlock('brand top-brand')}
        <div class="crumbs">${t((NAV.find(n => n[0] === (first === '/expiry' ? '/monitors' : first)) || NAV[0])[1])}</div>
        <${Search} route=${route} />
        ${chrome === 'top' && html`<nav class="top-nav">${navLinks}</nav>`}
      </header>
      ${banner && html`<div class="page" style=${{ paddingBottom: 0 }}><${ConfigBanner} banner=${banner} /></div>`}
      ${ov.data && ov.data.storage && ov.data.storage.failing && html`<div class="page" style=${{ paddingBottom: 0 }}><div class="banner error">
        <div class="spacer">${t('ui.storage_failing', { since: fmtTime(ov.data.storage.since), lost: ov.data.storage.lost })}
          ${ov.data.storage.error && html`<div class="small ltr">${ov.data.storage.error}</div>`}</div></div></div>`}
      ${me.member && me.role === 'admin' && html`<div class="page" style=${{ paddingBottom: 0 }}><div class="banner info">
        <div class="spacer">${t('ui.member.banner', { name: me.masterName })}</div>
        ${me.masterURL && html`<a class="btn sm" href=${me.masterURL} target="_blank" rel="noopener">${t('ui.member.open_master')}</a>`}
        <a class="btn sm" href="#/nodes">${t('ui.member.local')}</a></div></div>`}
      ${ov.data && ov.data.superseded && html`<div class="page" style=${{ paddingBottom: 0 }}><div class="banner error"><div class="spacer">
        ${t('ui.ha.superseded', { term: ov.data.superseded.term, by: ov.data.superseded.by || '?', at: fmtTime(ov.data.superseded.at) })}</div></div></div>`}
      ${ov.data && ov.data.monitor_over && html`<div class="page" style=${{ paddingBottom: 0 }}><div class="banner warning">
        <div class="spacer">${t('ui.cap.over', { used: ov.data.monitor_over.used, limit: ov.data.monitor_over.limit })}${ov.data.monitor_over.registered ? '' : ' ' + t('ui.reg.contact')}</div>
        <a class="btn sm" href="#/settings/registration">${t('ui.set.tab.registration')}</a></div></div>`}
      ${ov.data && ov.data.node_over && html`<div class="page" style=${{ paddingBottom: 0 }}><div class="banner warning">
        <div class="spacer">${t('ui.cap.nodes_over', { used: ov.data.node_over.used, limit: ov.data.node_over.limit })}${ov.data.node_over.registered ? '' : ' ' + t('ui.reg.contact')}</div>
        <a class="btn sm" href="#/settings/registration">${t('ui.set.tab.registration')}</a></div></div>`}
      ${view}
      ${chrome === 'top' && html`<footer class="page-foot">${accountDock}</footer>`}
      ${chrome === 'bottom' && html`<div class="bottom-wrap">
        <nav class="bottom-bar"><div class="bottom-nav">${navLinks}</div></nav>
        <div class="below-bar">${accountDock}</div>
      </div>`}
    </div>
  </div>`;
}

// RunMode names how this installation runs and opens the run mode page.
// The dot is green while live updates arrive and grey while they are
// paused; the pages refresh on their own meanwhile, so the label stays.
function RunMode({ ov, live }) {
  const layout = (ov && ov.layout) || 'single';
  const paths = (ov && ov.paths) || [];
  const label = layout === 'cluster'
    ? t('ui.top.mode.cluster', { online: paths.filter(p => p.online).length, n: paths.length })
    : t('ui.top.mode.' + (layout === 'replica' ? 'replica' : 'single'));
  return html`<a class=${'live' + (live ? ' on' : '')} href="#/nodes" title=${live ? t('ui.live.on') : t('ui.live.off')}>
    <i></i><span class="hide-sm">${label}</span></a>`;
}

// Search filters the monitor list, with the list's own query syntax
// (name, address, type:http, tag:, -group:, status:) and order; typing
// on another page opens the list. Ctrl+K still opens the command palette.
function termOf(q, k) {
  const m = q.match(new RegExp('(?:^|\\s)' + k + ':(\\S+)'));
  return m ? m[1] : '';
}
function setTerm(q, k, val) {
  return (q.replace(new RegExp('(?:^|\\s)' + k + ':\\S+', 'g'), ' ') + (val ? ' ' + k + ':' + val : '')).replace(/\s+/g, ' ').trim();
}

function Search({ route }) {
  const onList = route.parts[0] === 'monitors' && !route.parts[1];
  const q = onList ? route.query.get('q') || '' : '';
  const sort = onList ? route.query.get('sort') || 'status' : 'status';
  const [v, setV] = useState(q);
  const [groups, setGroups] = useState([]);
  useEffect(() => setV(q), [q]);
  useEffect(() => { get('/monitors?sort=name').then(d => {
    const g = [...new Set((d.items || []).map(r => r.group).filter(Boolean))].sort();
    setGroups(g);
  }).catch(() => {}); }, []);
  const show = (nq, ns) => {
    const path = monitorsPath(nq.trim(), ns);
    if (onList) replaceRoute(path);
    else location.hash = '#' + path;
  };
  const setFilter = (k, val) => { const next = setTerm(v, k, val); setV(next); show(next, sort); };
  // The filters live in one panel behind a button in the search box, so
  // there is one search, not a row of extra fields.
  const [filtersOpen, setFiltersOpen] = useState(false);
  const box = useRef(null);
  useEffect(() => {
    if (!filtersOpen) return undefined;
    const close = (e) => { if (box.current && !box.current.contains(e.target)) setFiltersOpen(false); };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [filtersOpen]);
  const active = [termOf(v, 'group'), termOf(v, 'failures') || termOf(v, 'failed'), termOf(v, 'retry') || termOf(v, 'retry_s')].filter(Boolean).length;
  return html`<div class="search" role="search" ref=${box}><span aria-hidden="true">⌕</span>
    <input type="search" class="spacer" placeholder=${t('ui.mon.search')} aria-label=${t('ui.mon.search')} value=${v}
      onInput=${(e) => { setV(e.target.value); show(e.target.value, sort); }} />
    <button type="button" class=${'filter-btn' + (active ? ' on' : '')} aria-expanded=${String(filtersOpen)} onClick=${() => setFiltersOpen(!filtersOpen)}>${t('ui.mon.filter.button')}${active ? ' ' + active : ''} ▾</button>
    ${filtersOpen && html`<div class="search-filters">
      <label>${t('ui.mon.filter.group')}<select value=${termOf(v, 'group')} onChange=${(e) => setFilter('group', e.target.value)}>
        <option value="">${t('ui.mon.all_groups')}</option>
        ${groups.map(g => html`<option value=${g}>${g}</option>`)}
      </select></label>
      <label>${t('ui.mon.filter.failures')}<input type="text" class="ltr" value=${termOf(v, 'failures') || termOf(v, 'failed')}
        onInput=${(e) => { const cur = setTerm(v, 'failed', ''); const next = setTerm(cur, 'failures', e.target.value); setV(next); show(next, sort); }} /></label>
      <label>${t('ui.mon.filter.retry')}<input type="text" class="ltr" value=${termOf(v, 'retry') || termOf(v, 'retry_s')}
        onInput=${(e) => { const cur = setTerm(v, 'retry_s', ''); const next = setTerm(cur, 'retry', e.target.value); setV(next); show(next, sort); }} /></label>
    </div>`}
    <select class="sort" value=${sort.replace(/^-/, '')} onChange=${(e) => show(v, e.target.value)} aria-label=${t('ui.mon.sort')}>
      ${MON_SORTS.map(k => html`<option value=${k}>${t('ui.mon.sort.' + k)}</option>`)}
    </select>
    <button type="button" class="kbd" onClick=${openPalette} title=${t('ui.top.search')}><kbd>Ctrl K</kbd></button></div>`;
}

export function ConfigBanner({ banner }) {
  const k = 'ui.banner.' + banner.code;
  const msg = t(k, { serial: banner.serial });
  return html`<div class=${'banner ' + banner.kind}>
    <div class="spacer">${msg === k ? banner.message : msg}
      ${banner.diagnostics && banner.diagnostics.length > 0 && html`<ul class="small">${banner.diagnostics.slice(0, 5).map(d => html`<li class="ltr">${d.pos.line}:${d.pos.col} ${detailText(d.message)}</li>`)}</ul>`}
    </div>
    <a class="btn sm" href="#/config/edit">${t('ui.banner.open')}</a>
  </div>`;
}

// ---- root ----

function Root() {
  const [st, setSt] = useState({ phase: 'loading' });
  const [, setLangTick] = useState(0);
  const changeLang = async (code) => {
    await setLang(code, true);
    setLangTick(x => x + 1);
  };
  const check = async () => {
    try {
      const s = await get('/session');
      setCSRF(s.csrf);
      setMe(s);
      await loadMyStyle();
      startLive();
      setSt({ phase: 'in', user: s.user });
    } catch (e) {
      stopLive();
      if (e.code === 'setup_required') {
        let local = true;
        try { local = (await get('/setup/status')).local; } catch { /* keep default */ }
        setSt({ phase: 'setup', local });
      } else if (e.status === 401) {
        setSt({ phase: 'login' });
      } else {
        setSt({ phase: 'error', error: e });
      }
    }
  };
  useEffect(() => {
    onUnauthorized(() => { stopLive(); setSt({ phase: 'login' }); });
    check();
  }, []);
  const signedIn = async (r) => {
    setCSRF(r.csrf);
    try { setMe(await get('/session')); } catch { setMe(null); }
    await loadMyStyle();
    startLive();
    setSt({ phase: 'in', user: r.user });
    location.hash = '#/';
  };
  const logout = async () => {
    try { await del('/session'); } catch { /* ignore */ }
    stopLive();
    setCSRF('');
    signedOut();
    setSt({ phase: 'login' });
  };
  let body;
  switch (st.phase) {
    case 'loading': body = html`<div class="boot">${PRODUCT}</div>`; break;
    case 'setup': body = html`<${Setup} local=${st.local} onDone=${signedIn} onLang=${changeLang} />`; break;
    case 'login': body = html`<${Login} onDone=${signedIn} onLang=${changeLang} />`; break;
    case 'error': body = html`<div class="auth"><div class="banner error">${errText(st.error)} <button class="btn sm" onClick=${check}>${t('ui.retry')}</button></div></div>`; break;
    default: body = html`<${Shell} user=${st.user} onLogout=${logout} onLang=${changeLang} />`;
  }
  return html`${body}${st.phase === 'in' && html`<${DrawerHost} /><${PaletteHost} />`}<${Toasts} /><${ConfirmHost} />`;
}

(async () => {
  applyPalette(storage('unagikuma.palette') || storage('unagikuma.theme'));
  applyZoom(storage('unagikuma.zoom'));
  applyCachedStyle();
  await Promise.all([loadLanguages(), loadBrand()]);
  // The browser's own message for a value of the wrong form is in its
  // language and says nothing useful; say what the field takes instead.
  document.addEventListener('invalid', (e) => {
    const el = e.target, v = el.validity;
    if (!v || !el.setCustomValidity) return;
    if (v.patternMismatch) el.setCustomValidity(t(/^\[a-z0-9\]/.test(el.getAttribute('pattern') || '') ? 'ui.err.pattern_handle' : 'ui.err.pattern_name'));
    else if (v.valueMissing) el.setCustomValidity(t('ui.err.required'));
  }, true);
  for (const ev of ['input', 'change']) document.addEventListener(ev, (e) => { if (e.target.setCustomValidity) e.target.setCustomValidity(''); }, true);
  await setLang(detectLang(), false);
  render(html`<${Root} />`, document.getElementById('app'));
})();
