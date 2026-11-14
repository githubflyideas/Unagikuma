// Monitor detail: state, availability, latency chart, evidence, history.
import { html, useState, useEffect, useRef } from '../lib.js';
import { t, tn, dur, fmtTime, fmtNum, currentLang, detailText, axisTimes } from '../i18n.js';
import { get, post, del, ifMatch } from '../api.js';
import { useFetch, useLive, Card, Loading, ErrorBox, Empty, Pill, Dot, Beats, Ago, Copy, Seg, pct, msText, reasonText, enc, go, toast, toastError, confirmDialog, useDocTitle, gwBad } from '../ui.js';
import { ObservationTable } from '../observations.js';
import { IncidentList } from './problems.js';
import { EventRows } from './events.js';

// routeText tells how a monitor's problems are sent: through its team's
// channels, the system channels, not at all (muted), or (files from before
// 2.6) a policy written in the configuration.
function routeText(policy) {
  if (!policy) return '–';
  if (policy.startsWith('team:')) return t('ui.mon.route_team');
  if (policy === 'mute') return t('ui.mon.route_mute');
  if (policy === 'system') return t('ui.mon.route_system');
  return policy;
}

// Path draws the way from this host to the target: the gateway, then for
// internet targets the internet exit, so a broken uplink is told apart
// from a broken target at a glance.
export function Path({ row, path, bare }) {
  const gw = path.gateway || {};
  const hops = [{ label: t('ui.path.host'), status: 'ok' }];
  hops.push({
    label: t('ui.health.gateway'),
    addr: gw.addr,
    status: gw.addr ? (gw.ok ? 'ok' : gwBad(gw) ? 'critical' : 'pending') : 'pending',
    note: gw.addr && gw.ok ? msText(gw.rtt_ms) : gw.note ? t('ui.health.gw_note.' + gw.note) : '',
  });
  const ex = path.exit;
  if (ex) {
    hops.push({ label: t('ui.path.exit'), addr: ex.addr, status: ex.ok ? 'ok' : 'critical', note: ex.ok ? msText(ex.rtt_ms) : t('reason.timeout'), hint: t('ui.path.exit_hint', { addr: ex.addr }) });
  }
  hops.push({ label: row.title, addr: row.target, status: row.status, note: row.status === 'ok' ? msText(row.latency_ms) : t('status.' + row.status) });
  // The link into the first failing hop is red; links past it say nothing.
  let bad = hops.findIndex(h => h.status === 'critical');
  if (bad < 0) bad = hops.length;
  const link = i => i < bad ? (hops[i].status === 'ok' ? 'ok' : 'pending') : i === bad ? 'critical' : 'pending';
  const hopsView = html`<div class="path">${hops.map((h, i) => html`
      ${i > 0 && html`<i class=${'path-link ' + link(i)}></i>`}
      <div class="path-hop" title=${h.hint || ''}><${Dot} status=${h.status} />
        <div><b>${h.label}</b>${h.addr && html`<span class="ltr mono">${h.addr}</span>`}${h.note && html`<span>${h.note}</span>`}</div>
      </div>`)}</div>`;
  return bare ? hopsView : html`<${Card} title=${t('ui.path.title')}>${hopsView}<//>`;
}

function css(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }

// uPlot is loaded on first use: it is large, and it evaluates the browser
// locale at load time, which throws for malformed tags (e.g. "en-US@posix").
let uplotLoad = null;
function loadUPlot() {
  if (!uplotLoad) uplotLoad = import('../../vendor/uplot/uPlot.esm.js').then(m => m.default, () => null);
  return uplotLoad;
}

// SimpleChart is the fallback when uPlot cannot load: a plain SVG line.
function SimpleChart({ points }) {
  const w = 1000, h = 200;
  const xs = points.map(p => new Date(p.t).getTime());
  const x0 = Math.min(...xs), x1 = Math.max(...xs, x0 + 1);
  const ymax = Math.max(1, ...points.map(p => p.avg)) * 1.15;
  const d = points.map((p, i) => `${i ? 'L' : 'M'}${((xs[i] - x0) / (x1 - x0) * w).toFixed(1)},${(h - p.avg / ymax * h).toFixed(1)}`).join(' ');
  return html`<svg viewBox=${`0 0 ${w} ${h}`} preserveAspectRatio="none" class="chart" role="img" aria-label=${t('ui.latency')}>
    <path d=${d} fill="none" stroke="currentColor" stroke-width="2" vector-effect="non-scaling-stroke" />
  </svg>
  <div class="row small muted"><span>${fmtTime(new Date(x0))}</span><div class="spacer"></div><span>${t('ui.chart.max')}: ${msText(Math.max(...points.map(p => p.max)))}</span></div>`;
}

export function LatencyChart({ name, range }) {
  const box = useRef(null);
  const plot = useRef(null);
  const [fallback, setFallback] = useState(false);
  const data = useFetch(() => get(`/monitors/${enc(name)}/latency?range=${range}`), [name, range]);
  useLive(['heartbeat'], data.reload, 10000);
  useEffect(() => {
    if (!box.current || !data.data || fallback) return;
    let cancelled = false;
    let ro = null;
    loadUPlot().then(uPlot => {
      if (cancelled || !box.current) return;
      if (!uPlot) { setFallback(true); return; }
      const pts = data.data.points || [];
      const xs = pts.map(p => new Date(p.t).getTime() / 1000);
      const avg = pts.map(p => p.avg);
      const mx = pts.map(p => p.max);
      const long = pts.length > 0 && pts.some(p => p.max !== p.avg);
      const accent = css('--accent') || '#1f7a4d';
      // A failed check has no latency: a red band marks when it happened.
      const fails = (data.data.fails || []).map(f => new Date(f).getTime() / 1000);
      const failColor = css('--critical') || '#c0392b';
      const drawFails = (u) => {
        if (!fails.length) return;
        const { ctx, bbox } = u;
        ctx.save();
        ctx.fillStyle = failColor + '55';
        ctx.strokeStyle = failColor;
        for (const f of fails) {
          const x = Math.round(u.valToPos(f, 'x', true));
          if (x < bbox.left || x > bbox.left + bbox.width) continue;
          ctx.fillRect(x - 3 * devicePixelRatio, bbox.top, 6 * devicePixelRatio, bbox.height);
          ctx.beginPath();
          ctx.moveTo(x, bbox.top);
          ctx.lineTo(x, bbox.top + bbox.height);
          ctx.stroke();
        }
        ctx.restore();
      };
      const opts = {
        hooks: { drawClear: [drawFails] },
        width: box.current.clientWidth || 600,
        height: 220,
        legend: { show: true },
        cursor: { drag: { x: true, y: false } },
        scales: { x: { time: true }, y: { range: (u, lo, hi) => [0, Math.max(1, hi * 1.15)] } },
        axes: [
          { stroke: css('--muted'), grid: { stroke: css('--border') }, ticks: { stroke: css('--border') }, values: (u, v) => axisTimes(v) },
          { stroke: css('--muted'), grid: { stroke: css('--border') }, ticks: { stroke: css('--border') }, values: (u, v) => v.map(x => fmtNum(x) + ' ms'), size: 64 },
        ],
        series: [
          { label: t('ui.chart.time'), value: (u, v) => v == null ? '–' : fmtTime(new Date(v * 1000)) },
          { label: t('ui.chart.avg'), stroke: accent, width: 1.5, fill: accent + '22', points: { show: pts.length < 80 }, value: (u, v) => v == null ? '–' : msText(v) },
          ...(long ? [{ label: t('ui.chart.max'), stroke: css('--degraded'), width: 1, dash: [4, 4], value: (u, v) => v == null ? '–' : msText(v) }] : []),
        ],
      };
      plot.current && plot.current.destroy();
      try {
        plot.current = new uPlot(opts, long ? [xs, avg, mx] : [xs, avg], box.current);
      } catch {
        setFallback(true);
        return;
      }
      ro = new ResizeObserver(() => plot.current && box.current && plot.current.setSize({ width: box.current.clientWidth, height: 220 }));
      ro.observe(box.current);
    });
    return () => { cancelled = true; ro && ro.disconnect(); plot.current && plot.current.destroy(); plot.current = null; };
  }, [data.data, currentLang(), fallback]);
  if (data.data && !(data.data.points || []).length) return html`<${Empty}>${t('ui.chart.empty')}<//>`;
  if (fallback && data.data) return html`<${SimpleChart} points=${data.data.points} />`;
  const nFails = data.data ? (data.data.fails || []).length : 0;
  return html`<div class="chart" ref=${box}>${!data.data && html`<${Loading} />`}</div>
    ${nFails > 0 && html`<p class="small muted chart-note"><span class="fail-key"></span>${tn('ui.chart.fails', nFails)}</p>`}`;
}

// fmtValue writes a value with its unit in a readable size (9.2 Gbps).
function fmtValue(v, unit) {
  const n = (x) => fmtNum(Math.round(x * 100) / 100);
  if (unit === 'bps') {
    for (const [u, f] of [['Tbps', 1e12], ['Gbps', 1e9], ['Mbps', 1e6], ['Kbps', 1e3]]) if (Math.abs(v) >= f) return n(v / f) + ' ' + u;
    return n(v) + ' bps';
  }
  if (unit === '%') return n(v) + '%';
  return unit ? n(v) + ' ' + unit : n(v);
}

// ValueCard shows the value the monitor's rules judge: now, against its
// reference, and the limits.
function ValueCard({ v }) {
  return html`<${Card} title=${t('ui.val.title')}>
    <div class="val-now">${v.sampled ? fmtValue(v.current, v.unit) : '–'}</div>
    <dl class="kv">
      ${v.has_ref && html`<dt>${t('ui.val.ref', { when: v.compare && v.compare !== 'previous' ? v.compare : t('ui.val.previous') })}</dt><dd>${fmtValue(v.ref, v.unit)}</dd>`}
      ${v.learning_s > 0 && html`<dt>${t('ui.val.learning')}</dt><dd>${t('ui.val.learning_left', { left: dur(Math.round(v.learning_s)) })}</dd>`}
      ${v.warn && html`<dt>${t('ui.val.warn')}</dt><dd class="ltr">${v.warn}</dd>`}
      ${v.crit && html`<dt>${t('ui.val.crit')}</dt><dd class="ltr">${v.crit}</dd>`}
      ${v.change && html`<dt>${t('ui.val.change')}</dt><dd class="ltr">${v.change}</dd>`}
    </dl>
    ${v.detail && html`<p class="small muted ltr" style=${{ margin: '6px 0 0' }}>${v.detail}</p>`}
  <//>`;
}

function PushBox({ name }) {
  const p = useFetch(() => get(`/monitors/${enc(name)}/push`), [name]);
  if (!p.data) return null;
  const url = location.origin + p.data.path;
  const rotate = async () => {
    if (!await confirmDialog(t('ui.push.rotate_confirm'), { danger: true })) return;
    try { p.setData(await post(`/monitors/${enc(name)}/push/rotate`)); toast(t('ui.push.rotated')); } catch (e) { toastError(e); }
  };
  return html`<${Card} title=${t('ui.push.title')}>
    <p class="muted small">${t('ui.push.hint')}</p>
    <div class="row nowrap"><code class="code ltr" style=${{ flex: 1 }}>${url}</code><${Copy} text=${url} /></div>
    <p class="small muted">${t('ui.push.example')}</p>
    <pre class="code ltr">curl -fsS "${url}?status=up&msg=OK&ping="</pre>
    <div class="row"><div class="spacer"></div><button class="btn sm danger" onClick=${rotate}>${t('ui.push.rotate')}</button></div>
  <//>`;
}

function Evidence({ items }) {
  if (!items || !items.length) return html`<${Empty}>${t('ui.ev.none')}<//>`;
  return html`<div class="tbl-wrap"><table class="tbl">
    <thead><tr><th>${t('ui.time')}</th><th>${t('ui.result')}</th><th class="right">${t('ui.latency')}</th><th>${t('ui.ev.details')}</th></tr></thead>
    <tbody>${items.map(e => {
      const r = e.result || {};
      const ev = r.evidence || {};
      const st = r.vote === 'ok' ? (r.degraded ? 'degraded' : 'ok') : r.vote === 'fail' ? 'critical' : 'unknown';
      return html`<tr>
        <td class="nowrap small">${fmtTime(e.at)}${e.node && e.node !== 'local' ? html`<div class="sub">${e.node}</div>` : ''}</td>
        <td><${Pill} status=${st} /> <span class="small">${reasonText(r.reason, '')}</span><div class="sub">${detailText(r.detail)}</div></td>
        <td class="right nowrap">${msText(r.latency_ms)}</td>
        <td class="small">
          ${ev.peer && html`<div class="ltr">${t('ui.ev.peer')}: ${ev.peer}</div>`}
          ${(ev.decoded || []).map(f => html`<div><span class="muted">${f.name}:</span> <span class="ltr">${f.value}</span></div>`)}
        </td>
      </tr>`;
    })}</tbody>
  </table></div>`;
}

export function MonitorDetail({ name }) {
  useDocTitle(name);
  const [range, setRange] = useState('24h');
  const d = useFetch(() => get('/monitors/' + enc(name)), [name]);
  const beats = useFetch(() => get(`/monitors/heartbeats?names=${enc(name)}&range=${range}&cells=72`), [name, range]);
  const tl = useFetch(() => get(`/monitors/${enc(name)}/timeline?limit=30`), [name]);
  useLive(['state', 'incident'], d.reload, 600);
  useLive(['heartbeat'], beats.reload, 4000);
  useLive(['event'], tl.reload, 1000);
  if (!d.data) return html`<div class="page">${d.error ? html`<${ErrorBox} error=${d.error} retry=${d.reload} />` : html`<${Loading} />`}</div>`;
  const { row, uptime } = d.data;
  const check = async () => {
    try { await post(`/monitors/${enc(name)}/check`); toast(t('ui.mon.check_queued')); } catch (e) { toastError(e); }
  };
  const pause = async () => {
    try {
      await post(`/monitors/${enc(name)}/pause`, { paused: !row.disabled }, ifMatch(row.etag));
      d.reload();
    } catch (e) {
      if (e.code !== 'scripts_missing') return toastError(e);
      const nodes = (e.body.nodes || []).map(n => n.node + ' (' + t('ui.scripts.state.' + n.state) + ')').join(', ');
      if (!await confirmDialog(t('ui.scripts.enable_confirm', { plugin: e.body.plugin, nodes }))) return;
      try {
        await post(`/monitors/${enc(name)}/pause`, { paused: false, force: true }, ifMatch(row.etag));
        d.reload();
      } catch (e2) { toastError(e2); }
    }
  };
  const remove = async () => {
    if (!await confirmDialog(t('ui.mon.delete_confirm', { name }), { danger: true, ok: t('ui.delete') })) return;
    try { await del('/monitors/' + enc(name), ifMatch(row.etag)); toast(t('ui.mon.deleted')); go('/monitors'); } catch (e) { toastError(e); }
  };
  const status = row.status;
  return html`<div class="page">
    <div class="row">
      <a href="#/monitors" class="muted">← ${t('ui.nav.monitors')}</a>
    </div>
    <div class="card"><div class="bd">
      <div class="row mon-head" style=${{ alignItems: 'flex-start' }}>
        <div class="col spacer" style=${{ gap: '4px', minWidth: 0 }}>
          <div class="row"><${Pill} status=${status} soft=${row.state_type === 'soft' && status !== 'ok'} /><h1 class="ellipsis">${row.title}</h1>
            ${row.title !== row.name && html`<span class="muted ltr">${row.name}</span>`}</div>
          <div class="row small"><span class="tag">${row.written_type}</span><span class="ltr mono">${row.target}</span>
            <span class="tag" title=${t('ui.depth.hint')}>${row.depth}</span>
            ${row.scope && html`<span class="tag">${row.scope}</span>`}
            ${row.group && html`<span class="tag">${row.group}</span>`}
            ${row.tags.map(tg => html`<span class="tag">${tg.k}${tg.v ? ':' + tg.v : ''}</span>`)}
            ${row.flags.includes('weak') && html`<span class="tag weak">${t('ui.flag.weak')}</span>`}</div>
          ${d.data.description && html`<p class="muted" style=${{ margin: '4px 0 0' }}>${d.data.description}</p>`}
        </div>
        <div class="row">
          <button class="btn" onClick=${check}>${t('ui.mon.check_now')}</button>
          <button class="btn" onClick=${pause}>${row.disabled ? t('ui.mon.resume') : t('ui.mon.pause')}</button>
          <a class="btn" href=${'#/monitors/' + enc(name) + '/edit'}>${t('ui.edit')}</a>
          <button class="btn danger" onClick=${remove}>${t('ui.delete')}</button>
        </div>
      </div>
      ${status !== 'ok' && status !== 'pending' && (row.reason || row.unknown_reason) && html`<div class=${'banner ' + (status === 'critical' ? 'error' : 'warning')} style=${{ marginTop: '12px' }}>
        <div><b>${reasonText(row.reason || row.unknown_reason, row.detail)}</b>
          ${row.detail && html`<div class="small">${detailText(row.detail)}</div>`}
          ${status === 'unreachable' && html`<div class="small">${t('ui.mon.unreachable_hint', { parent: (row.parents || []).join(', ') })}</div>`}
          ${row.state_type === 'soft' && html`<div class="small">${t('ui.mon.soft_hint', { attempt: row.attempt, max: row.max_attempts })}</div>`}
          ${row.incident && html`<div class="small">${tn('ui.inc.notified', row.incident.notify_count)}${row.incident.notify_max ? ' / ' + row.incident.notify_max : ''}${row.incident.acked ? ' · ' + t('ui.inc.acked_by', { who: row.incident.acked_by }) : ''}${row.status === 'paused' ? ' · ' + t('ui.inc.paused_quiet') : ''}</div>`}
        </div>
      </div>`}
    </div></div>

    <div class="tiles">
      ${['24h', '7d', '30d', '90d'].map(k => {
        // Too little of the window watched yet: the figure so far, and how
        // much of the window it covers.
        const cov = (d.data.coverage || {})[k];
        const short = uptime[k] == null && cov > 0;
        const so = short ? (d.data.uptime_partial || {})[k] : null;
        return html`<div class="tile" title=${short ? t(so != null ? 'ui.uptime.so_far' : 'ui.uptime.coverage', { pct: pct(cov, 0) }) : ''}><b class=${so != null ? 'muted' : ''}>${pct(uptime[k] != null ? uptime[k] : so)}</b><span>${t('ui.uptime')} ${k}${short ? html` <span class="muted">· ${t('ui.uptime.coverage_short', { pct: pct(cov, 0) })}</span>` : ''}</span></div>`;
      })}
      <div class="tile"><b>${msText(row.latency_ms)}</b><span>${t('ui.latency')}</span></div>
      <div class="tile"><b>${d.data.downtime_30d_s ? dur(d.data.downtime_30d_s) : '0'}</b><span>${t('ui.mon.downtime30d')}</span></div>
    </div>

    ${d.data.path && html`<${Path} row=${row} path=${d.data.path} />`}

    ${row.observations && html`<${ObservationTable} snapshot=${row.observations} />`}

    <${Card} title=${t('ui.mon.history')} actions=${html`<${Seg} value=${range} onChange=${setRange} options=${['1h', '6h', '24h', '7d', '30d'].map(r => [r, r])} />`}>
      <div class="col" style=${{ gap: '14px' }}>
        <${Beats} big cells=${beats.data && beats.data.items[name]} />
        <${LatencyChart} name=${name} range=${range} />
      </div>
    <//>

    <div class="grid g-side">
      <div class="col" style=${{ gap: '16px' }}>
        <${Card} title=${t('ui.mon.recent_checks')} flush><${Evidence} items=${d.data.evidence} /><//>
        <${Card} title=${t('ui.mon.incidents')} flush><${IncidentList} items=${d.data.incidents || []} reload=${d.reload} compact empty=${t('ui.mon.no_incidents')} /><//>
        <${Card} title=${t('ui.mon.timeline')} flush>${tl.data ? html`<${EventRows} items=${tl.data.items} hideMonitor />` : html`<${Loading} />`}<//>
      </div>
      <div class="col" style=${{ gap: '16px' }}>
        ${row.value && html`<${ValueCard} v=${row.value} />`}
        <${Card} title=${t('ui.mon.settings')}>
          <dl class="kv">
            <dt>${t('ui.interval')}</dt><dd>${dur(row.interval_s)}</dd>
            ${row.parents && html`<dt>${t('ui.mon.parents')}</dt><dd>${(d.data.parent_states || row.parents.map(p => ({ name: p, open: true }))).map((p, i) => html`${i ? ', ' : ''}${p.open
              ? html`<a href=${'#/monitors/' + enc(p.name)}>${p.name}</a>`
              : html`<span>${p.name}</span>`}${p.status ? html` <${Pill} status=${p.status} />` : ''}`)}</dd>`}
            ${d.data.timeout_s ? html`<dt>${t('ui.timeout')}</dt><dd>${dur(d.data.timeout_s)}</dd>` : ''}
            <dt>${t('ui.attempts')}</dt><dd>${row.max_attempts}</dd>
            <dt>${t('ui.mon.notify')}</dt><dd>${routeText(row.policy)}</dd>
            <dt>${t('ui.mon.last_check')}</dt><dd><${Ago} at=${row.last_check} /></dd>
            <dt>${t('ui.mon.next_check')}</dt><dd><${Ago} at=${row.next_check} /></dd>
            <dt>${t('ui.mon.changed')}</dt><dd><${Ago} at=${row.last_change} /></dd>
            ${Object.entries(row.metrics || {}).filter(([k]) => !k.startsWith('value.')).map(([k, v]) => html`<dt class="ltr">${k}</dt><dd>${fmtNum(Math.round(v * 100) / 100)}</dd>`)}
          </dl>
        <//>
        ${row.type === 'PUSH' && html`<${PushBox} name=${name} />`}
        ${d.data.record && html`<${Card} title=${t('ui.mon.record')} actions=${html`<a class="btn sm" href="#/config">${t('ui.mon.line', { line: d.data.record.line })}</a>`}>
          <pre class="code" tabindex="0">${d.data.record.text}</pre>
        <//>`}
      </div>
    </div>
  </div>`;
}
