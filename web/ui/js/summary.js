// Managers' summary: how available the services were, how often they
// failed, how fast they were fixed, what needs attention. Read-only, made
// to be read (and printed) by someone who does not run the monitoring.
// Opened signed in, or as /summary#token=uka_… with a read-only token.
import { html, render, useState, useEffect } from './lib.js';
import { t, setLang, detectLang, loadLanguages, fmtNum, fmtTime, dur, ago, currentLang } from './i18n.js';

const PRODUCT = 'Unagikuma';

const token = (() => {
  const m = /(?:^|[#&])token=([^&]+)/.exec(location.hash);
  return m ? decodeURIComponent(m[1]) : '';
})();

async function api(path) {
  const r = await fetch('/api/v1' + path, {
    credentials: token ? 'omit' : 'same-origin',
    headers: token ? { Authorization: 'Bearer ' + token } : {},
  });
  if (!r.ok) throw Object.assign(new Error('HTTP ' + r.status), { status: r.status });
  return r.json();
}

// pct shows enough digits to tell 99.9 % from 99.99 %.
function pct(r) {
  if (r == null) return '–';
  if (r >= 1) return '100%';
  const p = r * 100;
  return fmtNum(p, p >= 99.9 ? 3 : p >= 99 ? 2 : 1) + '%';
}
const band = (r) => (r == null ? 'none' : r >= 0.999 ? 'good' : r >= 0.99 ? 'fair' : 'poor');
const day = (d) => new Intl.DateTimeFormat(currentLang(), { month: 'short', day: 'numeric' }).format(new Date(d));

function Delta({ now, prev, better, fmt }) {
  if (now == null || prev == null) return html`<span class="delta none">${t('ui.sum.no_prev')}</span>`;
  const d = now - prev;
  if (Math.abs(d) < 1e-9) return html`<span class="delta same">${t('ui.sum.same')}</span>`;
  const good = better === 'up' ? d > 0 : d < 0;
  return html`<span class=${'delta ' + (good ? 'good' : 'bad')}>${d > 0 ? '▲' : '▼'} ${fmt(Math.abs(d))} <span class="vs">${t('ui.sum.vs_prev')}</span></span>`;
}

function Trend({ days }) {
  const vals = days.map(x => x.ratio).filter(x => x != null);
  if (!vals.length) return html`<p class="muted">${t('ui.sum.no_data')}</p>`;
  // Show the range that matters: from just under the worst day to 100 %.
  const floor = Math.max(0, Math.min(0.99, Math.floor(Math.min(...vals) * 200) / 200 - 0.005));
  const W = 1000, H = 220, pad = 70, bw = (W - pad) / days.length;
  const y = (r) => H - 22 - ((r - floor) / (1 - floor)) * (H - 40);
  const ticks = [floor, floor + (1 - floor) / 2, 1];
  return html`<svg class="trend" viewBox=${`0 0 ${W} ${H}`} role="img" aria-label=${t('ui.sum.trend')}>
    ${ticks.map(v => html`<g><line x1=${pad} x2=${W} y1=${y(v)} y2=${y(v)} class="grid" /><text x=${pad - 10} y=${y(v) + 4} class="axis" text-anchor="end">${pct(v)}</text></g>`)}
    ${days.map((x, i) => x.ratio == null ? null : html`<rect x=${pad + i * bw + bw * 0.18} width=${bw * 0.64} y=${y(x.ratio)} height=${Math.max(2, H - 22 - y(x.ratio))} rx="2" class=${'bar ' + band(x.ratio)}><title>${day(x.day)} · ${pct(x.ratio)}</title></rect>`)}
    ${[0, Math.floor(days.length / 2), days.length - 1].map(i => html`<text x=${pad + i * bw + bw / 2} y=${H - 4} class="axis" text-anchor="middle">${day(days[i].day)}</text>`)}
  </svg>`;
}

function Summary() {
  const [range, setRange] = useState('30d');
  const [d, setD] = useState(null);
  const [err, setErr] = useState(null);
  useEffect(() => {
    let live = true;
    const load = async () => {
      try { const r = await api('/reports/summary?range=' + range); if (live) { setD(r); setErr(null); } } catch (e) { if (live) setErr(e); }
    };
    load();
    const id = setInterval(load, 60000);
    return () => { live = false; clearInterval(id); };
  }, [range]);
  useEffect(() => { document.title = PRODUCT + ' · ' + t('ui.sum.title'); }, []);
  if (err && err.status === 401 && !d) return html`<div class="sum-msg"><h1>${t('ui.sum.need_link')}</h1><p>${t('ui.sum.need_link_hint')}</p></div>`;
  if (!d) return html`<div class="sum-msg">${err ? t('ui.wall.offline') : ''}</div>`;
  const n = d.now, bad = n.problems.length;
  const running = n.total - (n.counts.paused || 0);
  return html`<div class="sum">
    <header class="sum-head">
      <img src="/logo.png" alt="" />
      <div><b>${PRODUCT}</b><h1>${t('ui.sum.title')}</h1></div>
      <span class="spacer"></span>
      <div class="seg" role="tablist">${['7d', '30d', '90d'].map(k => html`<button role="tab" aria-selected=${range === k} class=${range === k ? 'on' : ''} onClick=${() => setRange(k)}>${t('ui.sum.range.' + k)}</button>`)}</div>
      <button class="btn print" onClick=${() => print()}>${t('ui.sum.print')}</button>
    </header>

    <section class=${'sum-now ' + (bad ? 'bad' : 'good')}>
      <span class="dot"></span>
      <div><h2>${bad ? t('ui.sum.now_bad', { n: bad, total: running }) : t('ui.sum.now_good', { total: running })}</h2>
        ${bad > 0 && html`<p>${n.problems.slice(0, 5).map((p, i) => html`${i ? ' · ' : ''}<b>${p.title}</b> ${p.since ? t('ui.sum.since', { ago: ago(p.since) }) : ''}`)}</p>`}</div>
      <span class="spacer"></span>
      <span class="small muted">${t('ui.sum.updated', { time: fmtTime(d.to) })}</span>
    </section>

    <section class="sum-kpis">
      <div class="kpi"><span class="lbl">${t('ui.sum.availability')}</span><b class=${'big ' + band(d.ratio)}>${pct(d.ratio)}</b>
        <${Delta} now=${d.ratio} prev=${d.prev_ratio} better="up" fmt=${(v) => fmtNum(v * 100, 3) + ' pt'} /></div>
      <div class="kpi"><span class="lbl">${t('ui.sum.incidents')}</span><b class="big">${fmtNum(d.incidents)}</b>
        <${Delta} now=${d.incidents} prev=${d.prev_incidents} better="down" fmt=${(v) => fmtNum(v)} /></div>
      <div class="kpi"><span class="lbl">${t('ui.sum.mttr')}</span><b class="big">${d.mttr_s ? dur(d.mttr_s) : '–'}</b>
        <span class="delta none">${d.mtta_s ? t('ui.sum.mtta', { time: dur(d.mtta_s) }) : t('ui.sum.mttr_hint')}</span></div>
      <div class="kpi"><span class="lbl">${t('ui.sum.downtime')}</span><b class="big">${d.down_s ? dur(d.down_s) : '0'}</b>
        <span class="delta none">${t('ui.sum.downtime_hint')}</span></div>
    </section>

    <section class="card sum-card"><h3>${t('ui.sum.trend')}</h3><${Trend} days=${d.days} />
      <div class="legend small muted"><span class="sw good"></span>≥ 99.9% <span class="sw fair"></span>≥ 99% <span class="sw poor"></span>${t('ui.sum.below')}</div></section>

    <div class="sum-cols">
      <section class="card sum-card"><h3>${t('ui.sum.attention')}</h3>
        ${d.attention.length ? html`<table class="sum-tbl">${d.attention.map(a => html`<tr>
          <td><b>${a.title}</b><div class="small muted">${t('ui.sum.att_detail', { n: a.incidents || 0, down: dur(a.down_s || 0) })}</div></td>
          <td class="meter"><span class=${band(a.ratio)} style=${{ width: Math.max(4, ((a.ratio - 0.9) / 0.1) * 100) + '%' }}></span></td>
          <td class=${'num ' + band(a.ratio)}>${pct(a.ratio)}</td></tr>`)}</table>`
          : html`<p class="muted">${t('ui.sum.att_none')}</p>`}
      </section>
      <section class="card sum-card"><h3>${t('ui.sum.recent')}</h3>
        ${d.recent.length ? html`<table class="sum-tbl">${d.recent.map(i => html`<tr>
          <td><b>${i.title}</b><div class="small muted">${fmtTime(i.opened)}</div></td>
          <td class="num">${i.resolved ? dur((new Date(i.resolved) - new Date(i.opened)) / 1000) : html`<span class="tag bad">${t('ui.sum.ongoing')}</span>`}</td></tr>`)}</table>`
          : html`<p class="muted">${t('ui.sum.recent_none')}</p>`}
      </section>
    </div>
  </div>`;
}

(async () => {
  document.documentElement.dataset.theme = 'calm';
  await loadLanguages();
  await setLang(detectLang(), false);
  render(html`<${Summary} />`, document.getElementById('app'));
})();
