// Expiry: every certificate and domain registration the checks watch,
// soonest first.
import { html } from '../lib.js';
import { t, tn, currentLang } from '../i18n.js';
import { get } from '../api.js';
import { useFetch, useLive, Loading, ErrorBox, Empty, Dot, enc, useDocTitle } from '../ui.js';

export function Expiry() {
  useDocTitle(t('ui.exp.title'));
  const d = useFetch(() => get('/expiry'), []);
  useLive(['state'], d.reload, 5000);
  if (!d.data) return html`<div class="page">${d.error ? html`<${ErrorBox} error=${d.error} retry=${d.reload} />` : html`<${Loading} />`}</div>`;
  const items = d.data.items;
  // Certificates are flagged inside the 62-day warning window, domain
  // registrations inside 30 days (the defaults of their checks).
  const cls = (days, what) => days < 7 ? 'err-text' : days < (what === 'cert' ? 62 : 30) ? 'warn-text' : '';
  const day = (days) => new Date(Date.now() + days * 864e5).toLocaleDateString(currentLang(), { year: 'numeric', month: 'short', day: 'numeric' });
  return html`<div class="page">
    <div class="row"><a href="#/monitors" class="muted">← ${t('ui.nav.monitors')}</a></div>
    <h1>${t('ui.exp.title')}</h1>
    <p class="muted small">${t('ui.exp.intro')}</p>
    <div class="card"><div class="bd flush tbl-wrap">
      ${!items.length ? html`<${Empty} title=${t('ui.exp.none')}>${t('ui.exp.none_hint')}<//>` : html`<table class="tbl">
        <thead><tr><th>${t('ui.exp.what')}</th><th>${t('ui.name')}</th><th>${t('ui.exp.expires')}</th><th class="right">${t('ui.exp.left')}</th></tr></thead>
        <tbody>${items.map(x => html`<tr class="click" onClick=${() => { location.hash = '#/monitors/' + enc(x.monitor); }}>
          <td><span class="tag">${t('ui.exp.kind.' + x.what)}</span></td>
          <td><${Dot} status=${x.status} /> <span class="name">${x.title}</span><div class="sub small muted ltr">${x.target}</div></td>
          <td class="nowrap">${day(x.days)}</td>
          <td class="right nowrap"><span class=${cls(x.days, x.what)}>${x.days < 0 ? t('ui.exp.expired') : tn('ui.ov.days_left', Math.floor(x.days))}</span></td>
        </tr>`)}</tbody>
      </table>`}
    </div></div>
  </div>`;
}
