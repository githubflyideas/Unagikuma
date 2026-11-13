// Overview: where a failure is on the way to the targets, every monitor at
// a glance, what needs attention and what was held back.
import { html, useState } from '../lib.js';
import { t, tn, dur, fmtNum } from '../i18n.js';
import { get, post, me } from '../api.js';
import { useFetch, useLive, Card, Loading, ErrorBox, Dot, Ago, Empty, Pill, Seg, msText, reasonText, quorumText, pct, openMonitor, enc, toast, toastError, useDocTitle, gwBad } from '../ui.js';
import { ObservationCell, observationSummary } from '../observations.js';
import { IncidentList } from './problems.js';


function uplinkStatus(u) {
  switch (u.state) {
    case 'ok': return 'ok';
    case 'wan_down': case 'lan_down': case 'no_route': return 'critical';
    default: return 'pending';
  }
}

// NodesCard lists the probe nodes, like a DNS secondary list: connected,
// and running the current configuration serial.
function NodesCard({ nodes }) {
  const remote = nodes.filter(n => !n.self);
  const online = remote.filter(n => n.online).length;
  return html`<${Card} title=${t('ui.ov.nodes')} flush actions=${html`<span class="small muted">${t('ui.ov.nodes_online', { online, n: remote.length })}</span>`}>
    <table class="tbl"><tbody>${nodes.map(n => html`<tr class="click" onClick=${() => { location.hash = '#/nodes'; }}>
      <td class="nowrap"><${Dot} status=${n.online ? 'ok' : (n.last_seen ? 'critical' : 'unknown')} /> <span class="name">${n.name}</span>
        ${n.self && html` <span class="tag">${t('ui.node.self')}</span>`}</td>
      <td class="small muted">${[n.title, n.region].filter(Boolean).join(' · ')}</td>
      <td class="nowrap"><span class="mono ltr small">${n.serial || '–'}</span>${!n.self && n.online && !n.synced && html` <span class="tag warn">${t('ui.node.behind')}</span>`}</td>
      <td class="right nowrap small">${n.self ? '' : msText(n.rtt_ms)}${!n.self && n.online && !n.clock_ok ? html` <span class="warn-text">${t('ui.node.clock')}</span>` : ''}</td>
    </tr>`)}</tbody></table>
  <//>`;
}

export function HealthCard({ health }) {
  const h = health;
  const up = h.uplink;
  const gw = h.gateway;
  const hb = h.heartbeat;
  const hbStatus = !hb.configured ? 'pending' : hb.last_error ? 'critical' : 'ok';
  return html`<${Card} title=${t('ui.health.title')} actions=${me.teams ? null : html`<a class="btn sm" href="#/settings/general">${t('ui.health.configure')}</a>`}>
    <div class="health">
      <div class="hitem"><${Dot} status=${uplinkStatus(up)} />
        <div><b>${t('ui.health.uplink')}</b>
          <span>${t('ui.health.uplink.' + up.state)}${up.suspected ? ' · ' + t('ui.health.suspected') : ''}</span>
          ${up.state === 'cannot_judge' && html`<span class="small"> · ${t('ui.health.need_anchors')}</span>`}
        </div></div>
      <div class="hitem"><${Dot} status=${gw.addr ? (gw.ok ? 'ok' : gwBad(gw) ? 'critical' : 'pending') : 'pending'} />
        <div><b>${t('ui.health.gateway')}</b>
          <span class="ltr">${gw.addr || '–'}</span>
          ${gw.addr && gw.ok ? html` · <span class="ltr">${msText(gw.rtt_ms)}</span>` : gw.note ? ' · ' + t('ui.health.gw_note.' + gw.note) : ''}
        </div></div>
      <div class="hitem"><${Dot} status=${h.dns_down ? 'critical' : 'ok'} />
        <div><b>${t('ui.health.dns')}</b><span>${h.dns_down ? t('ui.health.dns_down') : t('ui.health.dns_ok')}</span></div></div>
      <div class="hitem"><${Dot} status=${hbStatus} />
        <div><b>${t('ui.health.heartbeat')}</b>
          <span>${!hb.configured ? t('ui.health.hb_off') : hb.last_error ? hb.last_error : html`${t('ui.health.hb_ok')} <${Ago} at=${hb.last_ok} />`}</span>
        </div></div>
    </div>
    ${(h.anomalies || []).length > 0 && html`<div class="col" style=${{ marginTop: '12px' }}>
      ${h.anomalies.map(a => html`<div class="banner error">${t('sys.anomaly.' + a.kind + (a.suspected ? '_suspected' : ''), { gateway: a.detail })} · <${Ago} at=${a.since} /></div>`)}
    </div>`}
    ${h.interrupted && html`<p class="small muted" style=${{ marginBottom: 0 }}>${t('ui.health.interrupted', { duration: dur(h.interrupted.duration_s) })} · <${Ago} at=${h.interrupted.at} /></p>`}
  <//>`;
}

function Channels({ channels, alerting }) {
  if (!channels.length) {
    return html`<${Empty} title=${t('ui.ov.no_channels')}><p>${t('ui.ov.no_channels_hint')}</p><a class="btn primary" href="#/alerts/channels">${t('ui.ov.add_channel')}</a><//>`;
  }
  return html`<table class="tbl">
    <thead><tr><th>${t('ui.channel')}</th><th class="right">${t('ui.ov.sent24h')}</th><th class="right">${t('ui.ov.success')}</th></tr></thead>
    <tbody>${channels.map(c => html`<tr>
      <td><div class="name">${c.name}</div><div class="sub">${c.kind}${c.last_error ? html` · <span class="err-text">${c.last_error}</span>` : ''}</div></td>
      <td class="right">${fmtNum(c.sent_24h)}</td>
      <td class="right">${c.success_rate == null ? '–' : html`<span class=${c.success_rate < 0.95 ? 'err-text' : 'ok-text'}>${pct(c.success_rate, 1)}</span>`}</td>
    </tr>`)}</tbody>
  </table>
  ${!alerting && html`<div class="banner warning" style=${{ margin: '10px' }}>${t('ui.ov.no_default_chain')}</div>`}`;
}


// ---- where it breaks ----

const BAD_UPLINK = ['wan_down', 'lan_down', 'no_route'];

// hopStates reads one node's path: gateway, uplink, internet gateway.
// "dead" is a hop behind a broken one, "unk" one that cannot be judged,
// "idle" an internet gateway not asked because every internet target
// answers (the targets are checked first, the gateway only when one fails).
function hopStates(p) {
  if (!p.online) return { gw: 'dead', up: 'dead', ex: 'dead', off: true };
  const g = p.gateway || {};
  // A gateway that never answered ping (many routers ignore it) cannot be
  // judged: grey, not red. Red only when it answered before and stopped.
  const gw = !g.addr || g.note === 'container' ? 'unk' : g.ok ? 'ok' : gwBad(g) ? 'bad' : 'unk';
  const up = gw === 'bad' ? 'dead' : p.uplink === 'ok' ? 'ok' : BAD_UPLINK.includes(p.uplink) ? 'bad' : 'unk';
  const ex = up === 'dead' || up === 'bad' ? 'dead' : !p.exit ? 'none' : p.exit.idle ? 'idle' : p.exit.ok ? 'ok' : 'warn';
  return { gw, up, ex };
}

function countScope(rows) {
  const c = { ok: 0, warn: 0, bad: 0, acked: 0, held: 0 };
  for (const r of rows) {
    if (r.status === 'critical') r.incident && r.incident.acked ? c.acked++ : c.bad++;
    else if (r.status === 'degraded' || r.flags.includes('partial')) c.warn++;
    else if (r.status === 'unknown') c.held++;
    else if (r.status === 'ok') c.ok++;
  }
  return c;
}

const COLOR = { ok: 'var(--ok)', bad: 'var(--critical)', warn: 'var(--degraded)', dead: 'var(--border-strong)', unk: 'var(--faint)', none: 'var(--faint)', idle: 'color-mix(in srgb, var(--ok) 55%, transparent)' };

function Break({ x, y, label, warn, below }) {
  return html`<g class="fp-break"><circle cx=${x} cy=${y} r="12" fill=${warn ? 'var(--degraded)' : 'var(--critical)'} />
    ${warn ? html`<text x=${x} y=${y + 4} text-anchor="middle" style="font:700 13px var(--sans);fill:#1a1206">!</text>`
      : html`<path d=${`M${x - 5},${y - 5} l10,10 m0,-10 l-10,10`} stroke="#fff" stroke-width="2.4" />`}</g>
    <text class="fp-sub" x=${x} y=${below ? y + 30 : y - 18} text-anchor="middle" style=${{ fill: warn ? 'var(--degraded)' : 'var(--critical)' }}>${label}</text>`;
}

// exitText says what the internet gateway is doing.
function exitText(p, st) {
  if (!p.exit) return '';
  if (st === 'idle') return t('ui.ov.exit_idle');
  if (st === 'warn') return t('ui.ov.exit_down');
  if (st === 'ok') return msText(p.exit.rtt_ms);
  return '';
}

function FaultPath({ paths, rows }) {
  const lane = 64, top = 40;
  const need = top * 2 + lane * (paths.length - 1) + 64, H = Math.max(need, 250);
  const xN = 60, xG = 360, xU = 640, xB = 900, bw = 230;
  const laneY = i => top + 14 + i * lane + (H - need) / 2;
  const mid = H / 2 - 10, spread = Math.max(lane * 0.85, 50);
  const yLan = mid - spread, yWan = mid + spread;
  const curve = (x1, y1, x2, y2, cls) => html`<path class=${'fp-line ' + cls} d=${`M${x1},${y1} C${(x1 + x2) / 2},${y1} ${(x1 + x2) / 2},${y2} ${x2},${y2}`} />`;
  const line = (x1, x2, y, cls) => html`<line class=${'fp-line ' + cls} x1=${x1} y1=${y} x2=${x2} y2=${y} />`;
  const lan = rows.filter(r => r.scope === 'lan'), wan = rows.filter(r => r.scope !== 'lan');
  // A hop that cannot be judged (a container hides the gateway) is shown
  // by its targets: they answer, so the way to them works.
  const answer = (list) => countScope(list).ok > 0;
  const box = (y, label, list) => {
    const c = countScope(list);
    const stroke = c.bad ? 'var(--critical)' : c.warn ? 'var(--degraded)' : list.length && c.held === list.length ? 'var(--maint)' : 'var(--ok)';
    let x = xB + 140;
    const chips = [[c.ok, 'var(--ok)'], [c.warn, 'var(--degraded)'], [c.bad, 'var(--critical)'], [c.acked, 'color-mix(in srgb, var(--critical) 45%, transparent)'], [c.held, 'var(--maint)']].filter(v => v[0]);
    return html`<rect x=${xB} y=${y - 30} width=${bw} height="60" rx="14" fill="var(--panel-2)" stroke=${stroke} stroke-width="1.6" />
      <text class="fp-lbl" x=${xB + 18} y=${y - 6}>${label}</text><text class="fp-sub" x=${xB + 18} y=${y + 14}>${tn('ui.ov.n_targets', list.length)}</text>
      ${chips.map(([n, col]) => { const cx = x; x += 34; return html`<circle cx=${cx} cy=${y} r="5" fill=${col} /><text class="fp-sub" x=${cx + 8} y=${y + 4} style=${{ fill: col }}>${n}</text>`; })}`;
  };
  // The internet gateway sits under the internet targets: the master's
  // (or the first online node's) view of it.
  const exNode = paths.find(p => p.self && p.exit) || paths.find(p => p.exit && p.online);
  const exSt = exNode ? hopStates(exNode).ex : '';
  const yEx = yWan + 52, xEx = xB + 24;
  return html`<div class="fp-scroll"><svg viewBox=${`0 0 1160 ${H}`} preserveAspectRatio="xMidYMid meet" role="group" aria-label=${t('ui.ov.path_title')}>
    <text class="fp-sub" x=${xN} y="18" text-anchor="middle">${t('ui.ov.col_node')}</text>
    <text class="fp-sub" x=${xG} y="18" text-anchor="middle">${t('ui.health.gateway')}</text>
    <text class="fp-sub" x=${xU} y="18" text-anchor="middle">${t('ui.ov.col_uplink')}</text>
    <text class="fp-sub" x=${xB + bw / 2} y="18" text-anchor="middle">${t('ui.ov.col_targets')}</text>
    ${paths.map((p, i) => {
      const y = laneY(i), s = hopStates(p);
      const nodeCol = s.off ? 'var(--faint)' : s.gw === 'bad' || s.up === 'bad' ? 'var(--critical)' : s.ex === 'warn' ? 'var(--degraded)' : 'var(--ok)';
      const lanCls = s.off || s.gw === 'bad' ? 'dead' : s.gw === 'ok' || answer(lan) ? 'ok' : 'unk';
      const wanCls = s.off || s.up === 'bad' || s.up === 'dead' ? 'dead' : s.ex === 'warn' ? 'warn' : s.up === 'ok' || answer(wan) ? 'ok' : 'unk';
      return html`<g>
        ${line(xN + 16, xG - 12, y, s.gw === 'ok' ? 'ok' : s.gw === 'unk' && (answer(lan) || answer(wan)) ? 'ok' : s.gw)}
        ${line(xG + 12, xU - 12, y, s.up === 'ok' ? 'ok' : s.up === 'unk' && answer(wan) ? 'ok' : s.up)}
        ${curve(xG + 12, y, xB, yLan, lanCls)}
        ${curve(xU + 12, y, xB, yWan, wanCls)}
        ${s.gw === 'bad' && html`<${Break} below x=${(xN + xG) / 2} y=${y} label=${t('ui.ov.break_gateway')} />`}
        ${s.up === 'bad' && html`<${Break} x=${(xG + xU) / 2} y=${y} label=${t(p.self ? 'ui.ov.break_uplink_self' : 'ui.ov.break_uplink_node')} />`}
        ${s.ex === 'warn' && html`<${Break} warn x=${(xU + xB) / 2} y=${y + (yWan - y) / 2} label=${t('ui.ov.break_exit', { addr: p.exit.addr })} />`}
        <circle cx=${xN} cy=${y} r="15" fill="var(--panel)" stroke=${nodeCol} stroke-width="2.5" /><circle cx=${xN} cy=${y} r="5" fill=${nodeCol} />
        <text class="fp-lbl" x=${xN + 24} y=${y - 14}>${p.title || p.name}${p.self ? html` <tspan class="fp-sub">${t('ui.ov.master')}</tspan>` : ''}${s.off ? html` <tspan class="fp-sub" style="fill:var(--critical)">${t('ui.ov.node_offline')}</tspan>` : ''}</text>
        <rect x=${xG - 11} y=${y - 11} width="22" height="22" rx="6" fill="var(--panel)" stroke=${COLOR[s.gw]} stroke-width="2" />
        <text class="fp-sub" x=${xG} y=${y + 28} text-anchor="middle">${(p.gateway && p.gateway.addr) || '–'}</text>
        <path d=${`M${xU},${y - 12} l12,12 l-12,12 l-12,-12z`} fill="var(--panel)" stroke=${COLOR[s.up]} stroke-width="2" />
        <text class="fp-sub" x=${xU} y=${y + 28} text-anchor="middle">${p.region || ''}</text>
      </g>`;
    })}
    ${box(yLan, t('ui.ov.lan_targets'), lan)}
    ${box(yWan, t('ui.ov.wan_targets'), wan)}
    ${exNode && html`<g>
      <line class=${'fp-line ' + (exSt === 'warn' ? 'warn' : 'unk')} x1=${xEx} y1=${yWan + 30} x2=${xEx} y2=${yEx - 7} />
      <circle cx=${xEx} cy=${yEx} r="6" fill="var(--panel)" stroke=${COLOR[exSt] || 'var(--faint)'} stroke-width="2" />
      <a href="#/settings/general"><title>${t('ui.ov.exit_change')}</title>
        <text class="fp-sub fp-link" x=${xEx + 14} y=${yEx + 4}><tspan style=${{ fill: 'var(--text)' }}>${t('ui.ov.col_exit')}</tspan> ${exNode.exit.addr} ✎</text></a>
      <text class="fp-sub" x=${xEx + 14} y=${yEx + 19} style=${{ fill: exSt === 'warn' ? 'var(--degraded)' : 'var(--muted)' }}>${exitText(exNode, exSt)}</text>
    </g>`}
  </svg></div>
  <${PathList} paths=${paths} lan=${lan} wan=${wan} />`;
}

// PathList is the diagram for narrow screens: one line of hops per node.
const HOP_STATUS = { ok: 'ok', bad: 'critical', warn: 'degraded', dead: 'paused', unk: 'pending', none: 'pending', idle: 'ok' };

function PathList({ paths, lan, wan }) {
  const hop = (st, label, addr) => html`<span class=${'hop ' + st}><${Dot} status=${HOP_STATUS[st]} />${label}${addr && html` <span class="mono ltr">${addr}</span>`}</span>`;
  const sum = (label, list) => {
    const c = countScope(list);
    return html`<div class="fp-sum"><b>${label}</b>
      ${[['ok', c.ok], ['degraded', c.warn], ['critical', c.bad + c.acked], ['maintenance', c.held]].filter(x => x[1]).map(([s, n]) => html`<span class="nowrap"><${Dot} status=${s} /> ${n}</span>`)}</div>`;
  };
  return html`<div class="fp-list">
    ${paths.map(p => {
      const s = hopStates(p);
      return html`<div class="fp-node">
        <div class="row nowrap"><${Dot} status=${s.off ? 'paused' : s.gw === 'bad' || s.up === 'bad' ? 'critical' : 'ok'} /><b class="ellipsis">${p.title || p.name}</b>
          ${p.self && html`<span class="tag">${t('ui.ov.master')}</span>`}${s.off && html`<span class="tag">${t('ui.ov.node_offline')}</span>`}</div>
        <div class="hops">${hop(s.gw, t('ui.health.gateway'), p.gateway && p.gateway.addr)}<i>→</i>${hop(s.up, t('ui.ov.col_uplink'))}<i>→</i>${t('ui.ov.col_targets')}</div>
        ${p.exit && s.ex !== 'dead' && html`<div class="hops">${hop(s.ex, t('ui.ov.col_exit'), p.exit.addr)} <span class="muted">${exitText(p, s.ex)}</span></div>`}
        ${s.gw === 'bad' && html`<div class="small err-text">${t('ui.ov.break_gateway')}</div>`}
        ${s.up === 'bad' && html`<div class="small err-text">${t(p.self ? 'ui.ov.break_uplink_self' : 'ui.ov.break_uplink_node')}</div>`}
        ${s.ex === 'warn' && html`<div class="small warn-text">${t('ui.ov.break_exit', { addr: p.exit.addr })}</div>`}
      </div>`;
    })}
    <div class="fp-sums">${sum(t('ui.ov.lan_targets'), lan)}${sum(t('ui.ov.wan_targets'), wan)}</div>
  </div>`;
}

// storyOf says in one sentence what the diagram shows.
function storyOf(d, rows) {
  const health = d.health || {}, paths = d.paths || [];
  const held = rows.filter(r => r.status === 'unknown').length;
  if ((health.anomalies || []).length) {
    return { cls: 'held', icon: '⏸', title: t('ui.story.local', { n: held || rows.length }), text: t('ui.story.local_text'), saved: held };
  }
  const down = paths.find(p => !p.self && p.online && BAD_UPLINK.includes(p.uplink));
  if (down) {
    const n = rows.filter(r => r.scope !== 'lan').length;
    return { cls: 'held', icon: '⇄', title: t('ui.story.node_down', { node: down.title || down.name, n }), text: t('ui.story.node_down_text'), saved: n };
  }
  const crit = rows.filter(r => r.status === 'critical' && !(r.incident && r.incident.acked));
  if (crit.length === 1) {
    const r = crit[0];
    return { cls: 'critical', icon: '!', title: t('ui.story.crit_single', { name: r.title }), text: [reasonText(r.reason, r.detail), quorumText(r.quorum)].filter(Boolean).join(' · ') };
  }
  if (crit.length > 1) return { cls: 'critical', icon: '!', title: t('ui.story.crit_several', { n: crit.length }), text: t('ui.story.crit_text') };
  const partial = rows.filter(r => r.flags.includes('partial'));
  if (partial.length) return { cls: 'warn', icon: '◔', title: t('ui.story.partial', { n: partial.length }), text: t('ui.story.partial_text'), saved: partial.length };
  const slow = rows.filter(r => r.status === 'degraded');
  if (slow.length) return { cls: 'warn', icon: '◔', title: t('ui.story.slow', { n: slow.length }), text: t('ui.story.slow_text') };
  if (paths.length > 1) return { cls: 'ok', icon: '✓', title: t('ui.story.ok_cluster', { n: paths.filter(p => p.online).length }), text: t('ui.story.ok_cluster_text') };
  const g = (paths[0] && paths[0].gateway) || {};
  return { cls: 'ok', icon: '✓', title: t('ui.story.ok_single'), text: g.addr && g.ok ? t('ui.story.ok_single_text', { gateway: g.addr, rtt: msText(g.rtt_ms) }) : '' };
}

function Story({ s }) {
  return html`<div class=${'story ' + s.cls}><div class="icon">${s.icon}</div>
    <div><b>${s.title}</b><span class="muted">${s.text}</span></div>
    ${s.saved > 0 && html`<div class="saved"><strong>+${fmtNum(s.saved)}</strong><span>${t('ui.story.saved')}</span></div>`}
  </div>`;
}

// ---- every monitor ----

const R = 17, HW = Math.sqrt(3) * R, GAP = 3, PER = 11;

function hexPts(cx, cy, r) {
  const p = [];
  for (let i = 0; i < 6; i++) { const a = Math.PI / 180 * (60 * i - 90); p.push((cx + r * Math.cos(a)).toFixed(1) + ',' + (cy + r * Math.sin(a)).toFixed(1)); }
  return p.join(' ');
}

function slicePath(cx, cy, r, frac) {
  const a0 = -Math.PI / 2, a1 = a0 + frac * 2 * Math.PI, large = frac > .5 ? 1 : 0;
  return `M${cx},${cy} L${cx + r * Math.cos(a0)},${cy + r * Math.sin(a0)} A${r},${r} 0 ${large} 1 ${cx + r * Math.cos(a1)},${cy + r * Math.sin(a1)} Z`;
}

function hexClass(r) {
  const acked = r.status === 'critical' && r.incident && r.incident.acked;
  const st = { degraded: 'degraded', critical: 'critical', maintenance: 'maintenance', ok: 'ok' }[r.status]
    || (r.status === 'unknown' ? (r.unknown_reason === 'local_network' ? 'held' : 'unknown') : 'idle');
  return ['hex', st, acked ? 'acked' : '', r.flags.includes('weak') ? 'weak' : ''].join(' ');
}

export function Hive({ rows }) {
  const [tip, setTip] = useState(null);
  const groups = [];
  const by = {};
  for (const r of rows) {
    const g = r.group || r.area || '';
    if (!by[g]) { by[g] = []; groups.push(g); }
    by[g].push(r);
  }
  groups.sort((a, b) => (a === '') - (b === '') || a.localeCompare(b));
  return html`<div class="hive">${groups.map(g => {
    const list = by[g], n = Math.min(PER, list.length), lines = Math.ceil(list.length / PER);
    const w = n * (HW + GAP) + (lines > 1 ? HW / 2 : 0) + 6, h = lines * (R * 1.5 + GAP) + R * 0.6 + 6;
    const bad = list.filter(r => r.status === 'critical').length;
    return html`<div>
      <h4>${g || t('ui.ov.ungrouped')} <small>${list.length}${bad ? html` · <span class="err-text">${tn('ui.ov.n_down', bad)}</span>` : ''}</small></h4>
      <svg width=${w} height=${h} viewBox=${`0 0 ${w} ${h}`}>${list.map((r, i) => {
        const row = Math.floor(i / PER), c = i % PER;
        const cx = 3 + HW / 2 + c * (HW + GAP) + (row % 2 ? (HW + GAP) / 2 : 0), cy = 3 + R + row * (R * 1.5 + GAP);
        const q = r.quorum, partial = r.flags.includes('partial') && q && q.n;
        return html`<g class=${hexClass(r)} onClick=${() => { setTip(null); openMonitor(r.name); }}
          onMouseMove=${(e) => setTip({ r, x: Math.min(e.clientX + 16, innerWidth - 330), y: e.clientY + 16 })} onMouseLeave=${() => setTip(null)}>
          <polygon class="body" points=${hexPts(cx, cy, R)} />
          ${partial && html`<clipPath id=${'cp-' + i + '-' + g}><polygon points=${hexPts(cx, cy, R - 1)} /></clipPath>
            <path class="slice" clip-path=${`url(#cp-${i}-${g})`} d=${slicePath(cx, cy, R + 2, q.fail / q.n)} />`}
          ${r.status === 'critical' && r.incident && r.incident.acked && html`<text x=${cx} y=${cy + 3} text-anchor="middle">✓</text>`}
          ${r.status === 'unknown' && r.unknown_reason === 'local_network' && html`<text x=${cx} y=${cy + 3} text-anchor="middle">⏸</text>`}
        </g>`;
      })}</svg>
    </div>`;
  })}
  ${tip && html`<div class="hextip" style=${{ left: tip.x + 'px', top: tip.y + 'px' }}>
    <div class="row nowrap"><b class="ellipsis">${tip.r.title}</b><${Pill} status=${tip.r.status} /><span class="tag">${tip.r.depth}</span></div>
    <div class="mono muted ltr" style=${{ margin: '4px 0' }}>${tip.r.target}</div>
    ${tip.r.status !== 'ok' && (tip.r.reason || tip.r.unknown_reason) && html`<div>${reasonText(tip.r.reason || tip.r.unknown_reason, tip.r.detail)}</div>`}
    ${(tip.r.observations?.rows || []).map(v => html`<div class="row nowrap" style=${{ justifyContent: 'space-between' }}><span class="muted">${v.node_id}</span><${ObservationCell} value=${v} /></div>`)}
  </div>`}
  </div>`;
}

function Matrix({ rows, nodes }) {
 const list = rows.filter(r => r.observations?.rows?.length);
 if (!list.length) return html`<${Empty}>${t('ui.ov.matrix_none')}<//>`;
 const names = [...new Set(list.flatMap(r => r.observations.rows.map(o => o.node_id)))].sort();
 return html`<div class="tbl-wrap"><p class="small muted">${t('ui.node.intro')}</p><table class="tbl"><thead><tr><th>${t('ui.ov.matrix_monitor')}</th>${names.map(n => html`<th>${n}</th>`)}<th>${t('ui.obs.local')}</th></tr></thead>
 <tbody>${list.map(r => {
 const by = Object.fromEntries(r.observations.rows.map(o => [o.node_id, o]));
 return html`<tr class="click" onClick=${() => openMonitor(r.name)}><td>${r.title}<div class="small mono">${observationSummary(r.observations)}</div></td>${names.map(n => html`<td><${ObservationCell} value=${by[n]} /></td>`)}<td><${Pill} status=${r.status} /></td></tr>`;
 })}</tbody></table></div>`;
}

export function Legend() {
  const items = [['color-mix(in srgb, var(--ok) 60%, transparent)', 'status.ok'], ['var(--degraded)', 'ui.legend.partial'], ['var(--critical)', 'status.critical'],
    ['var(--maint)', 'ui.legend.held'], ['var(--faint)', 'ui.legend.idle']];
  return html`<div class="legend hide-sm">${items.map(([c, k]) => html`<span><i style=${{ background: c }}></i>${t(k)}</span>`)}</div>`;
}

// ---- first run ----

function onboardDismissed() { try { return localStorage.getItem('unagikuma.onboard') === 'done'; } catch { return false; } }

// Onboarding walks a new installation through its first monitors, a
// channel, the default policy, who receives alerts and a status page,
// ticking each step off as it is done.
function Onboarding({ d, reload }) {
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [hidden, setHidden] = useState(onboardDismissed());
  const steps = [(d.counts.total || 0) > 0, d.channels.length > 0, !!d.alerting_configured,
    (d.people || 0) > 0 && !d.contact_issues, (d.status_pages || 0) > 0];
  if (hidden || steps.every(Boolean)) return null;
  const add = async () => {
    setBusy(true);
    try {
      const p = await post('/quickadd/parse', { text });
      let n = 0;
      for (const r of p.records) {
        await post('/monitors', { record: { name: r.name, type: r.type, target: r.target, opts: r.opts }, message: 'first monitors' });
        n++;
      }
      if (p.errors.length) toast(t('ui.quick.line', { line: p.errors[0].line }) + ': ' + p.errors[0].msg, 'error');
      if (n) { toast(t('ui.quick.added', { n })); setText(''); reload(); }
    } catch (e) { toastError(e); }
    setBusy(false);
  };
  const dismiss = () => { try { localStorage.setItem('unagikuma.onboard', 'done'); } catch { /* unavailable */ } setHidden(true); };
  const step = (i, title, hint, body) => html`<div class=${'ob-step' + (steps[i] ? ' done' : '')}>
    <div class="ob-n" aria-label=${steps[i] ? t('ui.ob.done') : t('ui.ob.todo')} title=${steps[i] ? t('ui.ob.done') : t('ui.ob.todo')}>${steps[i] ? '✓' : '!'}</div>
    <div class="ob-b"><b>${title}</b><p class="muted small">${hint}</p>${!steps[i] && body}</div></div>`;
  return html`<${Card} title=${t('ui.ob.title')} actions=${html`<button class="btn sm ghost" onClick=${dismiss}>${t('ui.ob.hide')}</button>`}>
    <div class="ob">
      ${step(0, t('ui.ob.s1'), t('ui.ob.s1_hint'), html`<div class="col">
        <textarea class="ltr mono" rows="4" value=${text} onInput=${(e) => setText(e.target.value)} placeholder=${'https://www.example.com\n10.0.0.5:5432\n10.0.0.1:ntp\nping 192.168.1.1'}></textarea>
        <div class="row"><button class="btn primary" disabled=${busy || !text.trim()} onClick=${add}>${t('ui.ob.add')}</button>
          <a class="btn" href="#/config/import">${t('ui.ob.import')}</a></div></div>`)}
      ${step(1, t('ui.ob.s2'), t('ui.ob.s2_hint'), html`<a class="btn primary" href="#/alerts/channels">${t('ui.ov.add_channel')}</a>`)}
      ${step(2, t('ui.ob.s3'), t('ui.ob.s3_hint'), steps[1]
        ? html`<a class="btn" href="#/advanced/teams?edit=admin">${t('ui.ob.policy')}</a>`
        : html`<a class="btn" href="#/alerts/channels">${t('ui.ob.channel_first')}</a>`)}
      ${step(3, t('ui.ob.s4'), t('ui.ob.s4_hint'), html`<a class="btn" href="#/alerts/contacts">${t('ui.ob.contacts')}</a>`)}
      ${step(4, t('ui.ob.s5'), t('ui.ob.s5_hint'), html`<a class="btn" href="#/notices/pages">${t('ui.ob.status_page')}</a>`)}
    </div>
  <//>`;
}

// ---- side ----

const SAVED = [['outage_saved', 'ui.value.outage_saved'], ['soft_filtered', 'ui.value.soft_filtered'], ['outage_merged', 'ui.value.outage_merged'], ['cap_saved', 'ui.value.cap_saved']];

function Saved({ week }) {
  const parts = SAVED.map(([k, label]) => [t(label), week[k] || 0]);
  const total = parts.reduce((a, p) => a + p[1], 0);
  if (!total) return html`<p class="muted small" style=${{ margin: 0 }}>${t('ui.value.none')}</p>`;
  const mx = Math.max(...parts.map(p => p[1]));
  return html`<div class="bignum"><strong>${fmtNum(total)}</strong><span class="muted">${t('ui.ov.saved_unit')}</span></div>
    <div class="sbars">${parts.map(([l, n]) => html`<div class="sbar"><span>${l}</span><b class="mono">${fmtNum(n)}</b><div class="track"><i style=${{ width: (n / mx * 100) + '%' }}></i></div></div>`)}</div>`;
}

export function Overview() {
  useDocTitle(t('ui.nav.overview'));
  const [view, setView] = useState('hive');
  const ov = useFetch(() => get('/overview'), []);
  const mons = useFetch(() => get('/monitors'), []);
  const inc = useFetch(() => get('/incidents?state=unhandled'), []);
  useLive(['state', 'incident', 'event'], () => { ov.reload(); mons.reload(); inc.reload(); }, 800);
  if (!ov.data) return html`<div class="page">${ov.error ? html`<${ErrorBox} error=${ov.error} retry=${ov.reload} />` : html`<${Loading} />`}</div>`;
  const d = ov.data;
  const rows = mons.data ? mons.data.items : [];
  const cluster = d.layout === 'cluster';
  return html`<div class="page">
    <${Onboarding} d=${d} reload=${() => { ov.reload(); mons.reload(); }} />
    <${Card} title=${t('ui.ov.path_title')} flush actions=${html`<span class="small muted hide-sm">${t('ui.ov.path_hint')}</span>`}>
      <${FaultPath} paths=${d.paths || []} rows=${rows} />
      <${Story} s=${storyOf(d, rows)} />
    <//>
    <div class="grid g-side">
      <div class="col" style=${{ gap: '16px' }}>
        <${Card} title=${view === 'matrix' ? t('ui.ov.votes_title') : t('ui.ov.all_monitors')} flush actions=${html`<${Legend} />
          ${cluster && html`<${Seg} value=${view} onChange=${setView} options=${[['hive', t('ui.ov.view_hive')], ['matrix', t('ui.ov.view_matrix')]]} />`}`}>
          ${!mons.data ? html`<${Loading} />` : view === 'matrix' && cluster ? html`<${Matrix} rows=${rows} nodes=${d.paths || []} />` : html`<${Hive} rows=${rows} />`}
        <//>
        ${d.expiring.length > 0 && html`<${Card} title=${t('ui.ov.expiring')} flush actions=${html`<a class="btn sm" href="#/problems/expiry">${t('ui.exp.all')}</a>`}>
          <table class="tbl"><tbody>${d.expiring.map(x => html`<tr class="click" onClick=${() => { location.hash = '#/monitors/' + enc(x.monitor); }}>
            <td class="name">${x.title} <span class="tag">${t('ui.exp.kind.' + x.what)}</span></td><td class="right"><span class=${x.days < 7 ? 'err-text' : 'warn-text'}>${tn('ui.ov.days_left', Math.floor(x.days))}</span></td></tr>`)}</tbody></table>
        <//>`}
      </div>
      <div class="col" style=${{ gap: '16px' }}>
        <${Card} title=${t('ui.ov.todo')} flush actions=${html`<a class="btn sm" href="#/problems">${t('ui.ov.all_problems')}</a>`}>
          ${inc.data ? html`<${IncidentList} items=${inc.data.items} reload=${() => { inc.reload(); ov.reload(); }} compact />` : html`<${Loading} />`}
        <//>
        <${Card} title=${t('ui.ov.saved_title')}><${Saved} week=${d.value_week || {}} /><//>
        ${cluster && html`<${NodesCard} nodes=${d.nodes} serial=${d.serial} />`}
        <${HealthCard} health=${d.health} />
        <${Card} title=${t('ui.ov.channels')} flush><${Channels} channels=${d.channels} alerting=${d.alerting_configured} /><//>
        ${d.pending_notifications > 0 && html`<div class="banner info">${tn('ui.ov.pending_notifications', d.pending_notifications)}</div>`}
      </div>
    </div>
  </div>`;
}

export { reasonText };
