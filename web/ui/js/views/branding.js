// Styles and branding: the default style, each account's own style, and
// the installation's name, logo and browser icon.
import { html, useState } from '../lib.js';
import { t } from '../i18n.js';
import { get, put, del, api } from '../api.js';
import { useFetch, Loading, ErrorBox, Field, Card, toast, toastError, setProduct } from '../ui.js';
import { brand, myStyle, setMyStyle, defaultChanged } from '../styles.js';
import { SupportFooter } from './notices.js';

function styleName(id) {
  const k = 'ui.style.' + id;
  const v = t(k);
  return v === k ? id : v;
}

function styleOptions(selected) {
  return brand.styles.map(s => html`<option value=${s.id} disabled=${!s.allowed}>${styleName(s.id)}</option>`);
}

// MyStyle lets anyone pick the style they see; it follows their account.
export function MyStyle({ label }) {
  const [v, setV] = useState(myStyle());
  const change = async (id) => {
    const old = v;
    setV(id);
    try { await setMyStyle(id); } catch (err) { setV(old); toastError(err); }
  };
  return html`<${Field} label=${label || t('ui.style')} hint=${t('ui.style.hint')}>
    <select value=${v} onChange=${(e) => change(e.target.value)}>
      <option value="">${t('ui.style.follow', { name: styleName(brand.style) })}</option>
      ${styleOptions(v)}
    </select><//>`;
}

function ImageField({ kind, has, label, hint, onDone, round }) {
  const [busy, setBusy] = useState(false);
  const upload = async (e) => {
    const f = e.target.files && e.target.files[0];
    e.target.value = '';
    if (!f) return;
    setBusy(true);
    try {
      await api('PUT', '/branding/' + kind, f, { 'Content-Type': f.type || 'application/octet-stream' });
      toast(t('ui.saved'));
      onDone();
    } catch (err) { toastError(err); }
    setBusy(false);
  };
  const remove = async () => {
    try { await del('/branding/' + kind); toast(t('ui.saved')); onDone(); } catch (err) { toastError(err); }
  };
  return html`<${Field} label=${label} hint=${hint}>
    <div class="row brand-img">
      ${has ? html`<img src=${'/api/v1/branding/' + kind + '/preview?r=' + round} alt="" />` : html`<span class="brand-none small muted">${t('ui.brand.none')}</span>`}
      <label class="btn sm">${t('ui.brand.upload')}<input type="file" accept="image/png,image/jpeg,image/gif" hidden disabled=${busy} onChange=${upload} /></label>
      ${has && html`<button type="button" class="btn sm ghost" onClick=${remove}>${t('ui.brand.remove')}</button>`}
    </div><//>`;
}

export function Branding() {
  const d = useFetch(() => get('/branding'), []);
  const [name, setName] = useState(null);
  const [round, setRound] = useState(0);
  if (!d.data) return d.error ? html`<${ErrorBox} error=${d.error} />` : html`<${Loading} />`;
  const b = d.data;
  const reload = () => { setRound(round + 1); d.reload(); };
  const setDefault = async (id) => {
    try { await put('/branding', { style: id }); defaultChanged(id); toast(t('ui.saved')); d.reload(); } catch (err) { toastError(err); }
  };
  const saveName = async (e) => {
    e.preventDefault();
    try {
      const r = await put('/branding', { name: name === null ? b.name : name });
      if (r.allowed) setProduct(r.name || r.product);
      toast(t('ui.saved')); setName(null); d.reload();
    } catch (err) { toastError(err); }
  };
  return html`<div class="col" style=${{ gap: '16px', maxWidth: '760px' }}>
    <${Card} title=${t('ui.brand.style_title')}>
      <div class="form">
        <${Field} label=${t('ui.brand.style_default')} hint=${t('ui.brand.style_hint')}>
          <select value=${b.style} onChange=${(e) => setDefault(e.target.value)}>${styleOptions(b.style)}</select><//>
        <${MyStyle} label=${t('ui.brand.style_mine')} />
        ${b.style_trial && b.style_trial.state === 'active' && html`<p class="small muted" style=${{ margin: 0 }}>${t('ui.style.trial', { days: b.style_trial.days_left })}</p>`}
        ${b.style_trial && b.style_trial.state === 'ended' && html`<p class="small warn-text" style=${{ margin: 0 }}>${t('ui.style.trial_ended')} ${t('ui.reg.contact')}</p>`}
      </div>
    <//>
    <${Card} title=${t('ui.brand.title')}>
      <form class="form" onSubmit=${saveName}>
        ${!b.allowed && html`<div class="banner warning"><div class="spacer">${t('ui.brand.locked')}</div><a class="btn sm" href="#/settings/registration">${t('ui.set.tab.registration')}</a></div>`}
        <${Field} label=${t('ui.brand.name')} hint=${t('ui.brand.name_hint')}>
          <div class="row nowrap"><input type="text" maxlength="40" class="spacer" value=${name === null ? b.name : name} placeholder=${b.product}
            onInput=${(e) => setName(e.target.value)} /><button class="btn primary">${t('ui.save')}</button></div><//>
        <${ImageField} kind="logo" has=${b.logo} label=${t('ui.brand.logo')} hint=${t('ui.brand.logo_hint')} onDone=${reload} round=${round} />
        <${ImageField} kind="icon" has=${b.icon} label=${t('ui.brand.icon')} hint=${t('ui.brand.icon_hint')} onDone=${reload} round=${round} />
      </form>
    <//>
    <${SupportFooter} />
  </div>`;
}
