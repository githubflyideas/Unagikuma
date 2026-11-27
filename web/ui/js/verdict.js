// The verdict: the whole installation in one chip, with the counts next to
// it. It sits at the top of the Problems page.
import { html } from './lib.js';
import { t } from './i18n.js';
import { get } from './api.js';
import { useFetch, useLive } from './ui.js';
import { heldCount } from './views/overview.js';

// Verdict sums the whole installation up in one chip, with the counts
// next to it.
function VerdictChip({ ov }) {
  const paths = ov.paths || [];
  const anomalies = (ov.health && ov.health.anomalies) || [];
  const nodeDown = paths.some(p => !p.self && p.online && ['wan_down', 'lan_down', 'no_route'].includes(p.uplink));
  let cls = 'ok', text = t('ui.top.all_ok');
  if (anomalies.length || nodeDown) { cls = 'held'; text = t('ui.top.held'); }
  else if (ov.unhandled > 0) { cls = 'critical'; text = t('ui.top.unhandled', { n: ov.unhandled }); }
  else if (ov.degraded > 0) { cls = 'warn'; text = t('ui.top.warn'); }
  else if ((ov.counts.unknown || 0) > 0) { cls = 'unknown'; text = t('ui.top.unknown', { n: ov.counts.unknown }); }
  const week = ov.value_week || {};
  const saved = heldCount(week);
  // The chip counts problems still to handle; the line beside it says how
  // many monitors fail out of all, so the two numbers do not seem to clash.
  const down = (ov.counts.critical || 0) + (ov.counts.degraded || 0);
  const sub = [down ? t('ui.top.down_of', { down, n: ov.counts.total || 0 }) : t('ui.top.monitors', { n: ov.counts.total || 0 }),
    saved ? t('ui.top.saved', { n: saved }) : ''].filter(Boolean).join(' · ');
  return html`<a class=${'verdict ' + cls} href=${cls === 'critical' ? '#/problems' : '#/'}><i></i>${text}</a><span class="top-sub">${sub}</span>`;
}

export function Verdict() {
  const ov = useFetch(() => get('/overview'), []);
  useLive(['incident', 'state', 'serial'], ov.reload, 1500);
  return ov.data ? html`<div class="verdict-row"><${VerdictChip} ov=${ov.data} /></div>` : null;
}
