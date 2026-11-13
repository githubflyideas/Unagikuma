// Settings › Appearance: theme packs (colours, and on a registered
// installation the product name and logo). The wall screen and the
// managers' summary links are under Notices and status.
import { html, useState } from '../lib.js';
import { t } from '../i18n.js';
import { put, del, getCSRF, get, post } from '../api.js';
import { useFetch, Loading, ErrorBox, Empty, Copy, toast, toastError, confirmDialog } from '../ui.js';

const SWATCH = ['bg', 'side', 'panel', 'accent', 'ok', 'critical'];

function Swatches({ pack }) {
  const c = pack.colors || {};
  return html`<div class="swatches" aria-hidden="true">${SWATCH.map(k => html`<span title=${k} style=${{ background: c[k] || 'transparent' }} class=${c[k] ? '' : 'unset'}></span>`)}</div>`;
}

// LinkCard opens a read-only page (the wall screen or the managers'
// summary) here, or, with share, makes a link with a read-only token for
// someone or something that does not sign in.
export function LinkCard({ page, k, share = true }) {
  const [link, setLink] = useState('');
  const make = async () => {
    try {
      const day = new Date().toISOString().slice(0, 10);
      const r = await post('/tokens', { name: t('ui.look.' + k + '_token', { date: day }), scopes: ['read'] });
      setLink(location.origin + '/' + page + '#token=' + encodeURIComponent(r.token));
    } catch (err) { toastError(err); }
  };
  return html`<div class="card"><div class="hd"><h2>${t('ui.look.' + k)}</h2></div><div class="bd col" style=${{ gap: '10px' }}>
    <p class="small muted" style=${{ margin: 0 }}>${t('ui.look.' + k + '_hint')}</p>
    <div class="row">
      <a class="btn" href=${'/' + page} target="_blank" rel="noopener">${t('ui.look.' + k + '_open')}</a>
      ${share && html`<button class="btn" onClick=${make}>${t('ui.look.' + k + '_link')}</button>`}
    </div>
    ${link && html`<div class="banner ok"><div class="spacer col" style=${{ gap: '6px' }}>
      <span>${t('ui.look.' + k + '_link_done')}</span><code class="ltr small" style=${{ wordBreak: 'break-all' }}>${link}</code></div><${Copy} text=${link} /></div>`}
  </div></div>`;
}

export function Appearance() {
  const d = useFetch(() => get('/themes'), []);
  const [busy, setBusy] = useState(false);
  if (d.error) return html`<${ErrorBox} error=${d.error} />`;
  if (!d.data) return html`<${Loading} />`;
  const { packs, active, white_label: wl } = d.data;
  const upload = async (e) => {
    const f = e.target.files[0];
    e.target.value = '';
    if (!f) return;
    setBusy(true);
    try {
      const r = await fetch('/api/v1/themes', { method: 'POST', credentials: 'same-origin', headers: { 'X-Unagikuma-CSRF': getCSRF(), 'Content-Type': 'application/zip' }, body: f });
      const body = await r.json();
      if (!r.ok) throw Object.assign(new Error(body.message || r.statusText), body);
      toast(t('ui.look.added', { name: body.name }));
      d.reload();
    } catch (err) { toastError(err); }
    setBusy(false);
  };
  // A new look applies to the whole page, so it is simplest to reload.
  const use = async (id) => {
    try { await put('/themes/active', { id }); location.reload(); } catch (err) { toastError(err); }
  };
  const remove = async (p) => {
    if (!await confirmDialog(t('ui.look.delete_confirm', { name: p.name }))) return;
    try { await del('/themes/' + encodeURIComponent(p.id)); d.reload(); } catch (err) { toastError(err); }
  };
  const branded = packs.some(p => p.brand && (p.brand.product || p.has_logo || p.brand.hide_powered));
  return html`<div class="col" style=${{ gap: '12px' }}>
    <div class="card"><div class="bd col" style=${{ gap: '10px' }}>
      <p class="small muted" style=${{ margin: 0 }}>${t('ui.look.intro')}</p>
      <div class="row">
        <label class=${'btn primary' + (busy ? ' disabled' : '')}>${t('ui.look.upload')}
          <input type="file" accept=".zip,application/zip" hidden disabled=${busy} onChange=${upload} /></label>
        ${active && html`<button class="btn" onClick=${() => use('')}>${t('ui.look.builtin')}</button>`}
        <div class="spacer"></div>
        <a class="small" href="https://github.com/githubflyideas/Unagikuma/blob/main/docs/manual.md#theme-packs" target="_blank" rel="noopener">${t('ui.look.format')}</a>
      </div>
      ${branded && !wl && html`<div class="banner warning"><div class="spacer">${t('ui.look.brand_locked')}</div>
        <a class="btn sm" href="#/settings/registration">${t('ui.set.tab.registration')}</a></div>`}
    </div></div>
    ${!packs.length ? html`<${Empty} title=${t('ui.look.empty')} />` : html`<div class="packs">${packs.map(p => html`
      <div class=${'card pack' + (p.id === active ? ' on' : '')}><div class="bd col" style=${{ gap: '8px' }}>
        <div class="row" style=${{ gap: '10px' }}>
          ${p.has_logo ? html`<img class="pack-logo" src=${'/api/v1/themes/' + encodeURIComponent(p.id) + '/image/logo'} alt="" />` : html`<div class="pack-logo none"></div>`}
          <div class="col" style=${{ gap: '2px', minWidth: 0 }}>
            <b class="ellipsis">${p.name}</b>
            <span class="small muted ellipsis">${[p.author, p.version].filter(Boolean).join(' · ')}</span>
          </div>
        </div>
        <${Swatches} pack=${p} />
        <div class="small muted">
          ${p.brand && p.brand.product && html`<div>${t('ui.look.product')}: <b>${p.brand.product}</b></div>`}
          ${p.layout && html`<div>${t('ui.look.layout')}: ${t('ui.look.layout.' + p.layout)}</div>`}
          <div>${t(p.base === 'dark' ? 'ui.look.base_dark' : 'ui.look.base_light')}</div>
        </div>
        <div class="row" style=${{ gap: '6px' }}>
          ${p.id === active ? html`<span class="tag ok">${t('ui.look.in_use')}</span>` : html`<button class="btn sm primary" onClick=${() => use(p.id)}>${t('ui.look.use')}</button>`}
          <div class="spacer"></div>
          <a class="btn sm" href=${'/api/v1/themes/' + encodeURIComponent(p.id) + '/export'} download>${t('ui.look.export')}</a>
          ${p.id !== active && html`<button class="btn sm danger" onClick=${() => remove(p)}>${t('ui.look.delete')}</button>`}
        </div>
      </div></div>`)}</div>`}
  </div>`;
}
