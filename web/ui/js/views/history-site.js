import { html } from '../lib.js';
import { t } from '../i18n.js';
import { get, me } from '../api.js';
import { useFetch } from '../ui.js';

// HistorySite picks whose history to show; a single machine has only its
// own, so there is nothing to pick.
export function HistorySite({ value, onChange }) {
 const sites = useFetch(() => me.cluster ? get('/history/sites') : Promise.resolve([]), []);
 if (!me.cluster) return null;
 return html`<label class="row small">${t('ui.audit.site')} <select value=${value} onChange=${e => onChange(e.target.value)}>
 <option value="">${t('ui.obs.local')}</option>
 <option value="*">${t('ui.all')}</option>
 ${(sites.data || []).map(site => html`<option value=${site}>${site}</option>`)}
 </select></label>`;
}
