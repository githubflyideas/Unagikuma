// Public status page: titles, statuses and uptime only.
import { html, render, useState, useEffect } from './lib.js';
import { t, setLang, detectLang, loadLanguages, fmtTime } from './i18n.js';
import { Beats, pct, useInterval } from './ui.js';

const SEV = { info: 'info', minor: 'warning', major: 'error', maintenance: 'info' };
const api = (name, what) => '/api/v1/status/' + encodeURIComponent(name) + (what ? '/' + what : '');
const postJSON = async (url, body) => {
  const r = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  if (!r.ok) { let m = 'HTTP ' + r.status; try { m = (await r.json()).message || m; } catch { /* not JSON */ } throw new Error(m); }
  return r.json();
};

// Notice is one announcement; past ones are folded to a line.
function Notice({ a }) {
  if (a.resolved_at) {
    return html`<li><b>${a.title}</b> <span class="small muted">${t('ui.pub.resolved', { time: fmtTime(a.resolved_at) })}</span>
      ${a.body && html`<div class="small pre">${a.body}</div>`}</li>`;
  }
  return html`<div class=${'banner notice ' + (SEV[a.severity] || 'info')}><div class="spacer">
    <div class="row"><b>${a.title}</b><div class="spacer"></div><span class="small muted">${t('ui.pub.sev.' + a.severity)} · ${fmtTime(a.updated_at)}</span></div>
    ${a.body && html`<div class="pre">${a.body}</div>`}</div></div>`;
}

function Subscribe({ name }) {
  const [email, setEmail] = useState('');
  const [state, setState] = useState(null);
  const send = async (e) => {
    e.preventDefault();
    setState({ busy: true });
    try { await postJSON(api(name, 'subscribe'), { email }); setState({ ok: true }); } catch (e2) { setState({ err: e2.message }); }
  };
  if (state && state.ok) return html`<div class="banner ok">${t('ui.pub.subscribe_sent', { email })}</div>`;
  return html`<form class="sub row" onSubmit=${send}>
    <input type="email" required placeholder="you@example.com" title=${t('ui.pub.subscribe_hint')} aria-label=${t('ui.pub.subscribe_hint')} value=${email} onInput=${(e) => setEmail(e.target.value)} />
    <button class="btn primary" disabled=${state && state.busy}>${t('ui.pub.subscribe')}</button>
    ${state && state.err && html`<span class="small err-text">${state.err}</span>`}
  </form>`;
}

// The server renders the first screen and embeds its data.
function initialData() {
  const el = document.getElementById('page-data');
  if (!el) return null;
  try { return JSON.parse(el.textContent); } catch { return null; }
}

function Page({ name }) {
  const [d, setD] = useState(initialData);
  const [err, setErr] = useState(false);
  const [link, setLink] = useState(null);
  useEffect(() => {
    // Links from confirmation and announcement e-mails.
    const q = new URLSearchParams(location.search);
    const what = q.has('confirm') ? 'confirm' : q.has('unsubscribe') ? 'unsubscribe' : '';
    if (!what) return;
    history.replaceState(null, '', location.pathname);
    postJSON(api(name, what), { token: q.get(what) })
      .then(() => setLink({ ok: t(what === 'confirm' ? 'ui.pub.confirmed' : 'ui.pub.unsubscribed') }))
      .catch(() => setLink({ err: t('ui.pub.link_invalid') }));
  }, []);
  const load = async () => {
    try {
      const r = await fetch(api(name));
      if (!r.ok) throw new Error(r.status);
      const j = await r.json();
      setD(j);
      setErr(false);
    } catch {
      setErr(true);
    }
  };
  useEffect(() => { if (!d) load(); }, []);
  useEffect(() => {
    if (!d) return;
    document.title = d.title;
    if (d.theme === 'dark' || d.theme === 'light') document.documentElement.dataset.theme = d.theme;
  }, [d]);
  useInterval(load, 60000);
  if (!d) return html`<div class="pub">${err ? html`<div class="empty"><b>404</b>${t('ui.pub.not_found')}</div>` : ''}</div>`;
  const groups = [];
  for (const m of d.monitors) {
    let g = groups.find(x => x.name === (m.group || ''));
    if (!g) groups.push(g = { name: m.group || '', items: [] });
    g.items.push(m);
  }
  const win = d.windows.includes('90d') ? '90d' : d.windows[d.windows.length - 1];
  const open = (d.announcements || []).filter(a => !a.resolved_at);
  const past = (d.announcements || []).filter(a => a.resolved_at);
  return html`<div class="pub">
    <div class="head">${d.logo && html`<img src=${d.logo} alt="" />`}<h1>${d.title}</h1></div>
    ${link && html`<div class=${'banner ' + (link.ok ? 'ok' : 'error')}>${link.ok || link.err}</div>`}
    <div class=${'overall ' + d.status}>${d.status === 'ok' ? '✓ ' + t('ui.pub.all_ok') : d.status === 'degraded' ? '! ' + t('ui.pub.degraded') : '✗ ' + t('ui.pub.outage')}</div>
    ${open.map(a => html`<${Notice} a=${a} />`)}
    ${(d.maintenance || []).length > 0 && html`<section class="card"><div class="hd"><h2>${t('ui.pub.maintenance')}</h2></div>
      <div class="bd col">${d.maintenance.map(m => html`<div><b>${m.title}</b>
        <span class="small muted"> · ${m.open ? t('ui.pub.maint_now') : t('ui.pub.maint_at', { time: fmtTime(m.start) })}${m.end ? ' – ' + fmtTime(m.end) : ''}</span>
        <div class="small muted">${m.monitors.join(', ')}</div></div>`)}</div></section>`}
    ${groups.map(g => html`<section class="card">
      ${g.name && html`<div class="hd"><h2>${g.name}</h2></div>`}
      <div class="bd flush">${g.items.map(m => html`<div class="mon">
        <div class="row"><i class=${'dot ' + m.status}></i><b>${m.title}</b><div class="spacer"></div>
          <span class="small muted">${d.windows.map(w => `${w} ${pct(m.uptime[w])}`).join(' · ')}</span></div>
        <${Beats} cells=${m.cells} />
        <div class="row small faint"><span>${win}</span><div class="spacer"></div><span>${t('ui.pub.today')}</span></div>
      </div>`)}</div>
    </section>`)}
    ${past.length > 0 && html`<section class="card"><div class="hd"><h2>${t('ui.pub.past_notices')}</h2></div>
      <div class="bd"><ul class="past">${past.map(a => html`<${Notice} a=${a} />`)}</ul></div></section>`}
    ${err && html`<div class="banner warning">${t('ui.pub.stale')}</div>`}
    ${d.subscribe && html`<div class="row subs"><${Subscribe} name=${name} /></div>`}
    <footer>${d.footer && html`<div>${d.footer}</div>`}${t('ui.pub.updated', { time: fmtTime(d.updated_at) })}${d.support ? html` · ${d.support}` : d.powered !== false && html` · ${t('ui.pub.powered')} <a href="https://github.com/githubflyideas/Unagikuma">Unagikuma</a>`}</footer>
  </div>`;
}

(async () => {
  await loadLanguages();
  await setLang(detectLang(), false);
  // On its own host name the page is served at "/" and named "-".
  const own = !location.pathname.startsWith('/status/');
  const name = own ? '-' : decodeURIComponent(location.pathname.replace(/^\/status\//, '').replace(/\/$/, '')) || 'default';
  const app = document.getElementById('app');
  app.textContent = ''; // the server's first screen
  render(html`<${Page} name=${name} />`, app);
})();
