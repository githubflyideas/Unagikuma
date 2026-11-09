// Monitor list: dense table with heartbeat bars, or the honeycomb, with
// search and status chips. A row opens the monitor's page; ticked rows can
// be checked now, paused, resumed or deleted together.
import { html, useState, useEffect, useMemo } from '../lib.js';
import { t, tn, fmtNum } from '../i18n.js';
import { get, post, me } from '../api.js';
import { onLive } from '../live.js';
import { useFetch, useLive, Loading, ErrorBox, Empty, Pill, Beats, Ago, Seg, pct, msText, reasonText, enc, go, toast, toastError, confirmDialog, useDocTitle, replaceRoute, monitorsPath, Modal, Field, teamTitle, regionTitle } from '../ui.js';
import { QuickAdd } from './editor.js';
import { Hive, Legend } from './overview.js';

function savedView() {
  try { return localStorage.getItem('unagikuma.monview') || 'list'; } catch { return 'list'; }
}

const CHIPS = ['critical', 'degraded', 'unknown', 'ok', 'pending', 'paused'];

function splitStatus(q) {
  const m = q.match(/(^|\s)status:(\w+)/);
  return { status: m ? m[2] : '', rest: q.replace(/(^|\s)status:\w+/, '').trim() };
}

export function Monitors({ query }) {
  useDocTitle(t('ui.nav.monitors'));
  const [q, setQ] = useState(query.get('q') || '');
  const [sort, setSort] = useState(query.get('sort') || 'status');
  const [quick, setQuick] = useState(false);
  const [sel, setSel] = useState(new Set());
  const teams = useFetch(() => get('/teams'), []);
  const regions = useFetch(() => get('/regions'), []);
  const multiTeam = !!(teams.data && teams.data.items.length > 1);
  const [view, setView] = useState(savedView());
  const changeView = (v) => { setView(v); try { localStorage.setItem('unagikuma.monview', v); } catch { /* unavailable */ } };
  const list = useFetch(() => get('/monitors?sort=' + sort), [sort]);
  const beats = useFetch(() => get('/monitors/heartbeats?range=24h&cells=36'), []);
  useLive(['heartbeat'], beats.reload, 5000);
  useLive(['reset'], list.reload, 500);
  // State events carry the full row: patch it in place.
  useEffect(() => onLive('state', (row) => {
    if (!row || !row.name) return;
    list.setData(d => {
      if (!d) return d;
      const i = d.items.findIndex(x => x.name === row.name);
      if (i < 0) { list.reload(); return d; }
      const items = d.items.slice();
      items[i] = row;
      return { ...d, items };
    });
  }), []);
  // The search box and the order are in the top bar.
  useEffect(() => { setQ(query.get('q') || ''); }, [query.get('q')]);
  useEffect(() => { setSort(query.get('sort') || 'status'); }, [query.get('sort')]);

  const { status, rest } = splitStatus(q);
  const counts = useMemo(() => {
    const c = {};
    for (const r of (list.data ? list.data.items : [])) c[r.status] = (c[r.status] || 0) + 1;
    return c;
  }, [list.data]);
  const rows = useMemo(() => {
    if (!list.data) return [];
    const words = rest.toLowerCase().split(/\s+/).filter(Boolean);
    return list.data.items.filter(r => {
      if (status && r.status !== status) return false;
      return words.every(w => {
        const neg = w.startsWith('-') && w.length > 1;
        const term = neg ? w.slice(1) : w;
        const [k, v] = term.includes(':') ? term.split(/:(.*)/) : ['', term];
        let hit;
        switch (k) {
          case 'type': hit = r.type.toLowerCase() === v || r.written_type.toLowerCase() === v; break;
          case 'tag': hit = r.tags.some(tg => tg.k.toLowerCase() === v.split(':')[0] && (!v.includes(':') || (tg.v || '').toLowerCase() === v.split(':')[1])); break;
          case 'group': hit = (r.group || '').toLowerCase().includes(v); break;
          case 'depth': hit = r.depth.toLowerCase() === v; break;
          case 'policy': hit = (r.policy || '').toLowerCase() === v; break;
          case 'team': hit = (r.team || '').toLowerCase() === v; break;
          case 'region': hit = (r.region || '').toLowerCase() === v; break;
          case 'flag': hit = r.flags.includes(v); break;
          default: hit = [r.name, r.title, r.target, r.group, r.keyword].join(' ').toLowerCase().includes(term);
        }
        return hit !== neg;
      });
    });
  }, [list.data, status, rest]);

  // The team, region and tag filters are terms of the search.
  const termOf = (k) => { const m = q.match(new RegExp('(^|\\s)' + k + ':(\\S+)')); return m ? m[2] : ''; };
  const setTerm = (k, v) => {
    const next = (q.replace(new RegExp('(^|\\s)' + k + ':\\S+', 'g'), ' ') + (v ? ' ' + k + ':' + v : '')).replace(/\s+/g, ' ').trim();
    setQ(next);
    replaceRoute(monitorsPath(next, sort));
  };
  const tagKeys = useMemo(() => [...new Set(((list.data && list.data.items) || []).flatMap(r => r.tags.map(tg => tg.k)))].sort(), [list.data]);
  const [moving, setMoving] = useState(null);
  const move = async (e) => {
    e.preventDefault();
    try {
      const res = await post('/monitors/move', { names: picked.map(r => r.name), team: moving.team, region: moving.region });
      toast(tn('ui.mon.moved_n', res.moved));
      setMoving(null);
      setSel(new Set());
      list.reload();
    } catch (e2) { toastError(e2); }
  };

  const setStatus = (s) => {
    const next = (s === status ? rest : (rest + ' status:' + s)).trim();
    setQ(next);
    replaceRoute(monitorsPath(next, sort));
  };
  // Only ticked rows that are still listed count: a filter or a deletion
  // elsewhere must not leave hidden monitors selected.
  const picked = rows.filter(r => sel.has(r.name));
  const allPicked = rows.length > 0 && picked.length === rows.length;
  const toggle = (name) => {
    const n = new Set(sel);
    n.has(name) ? n.delete(name) : n.add(name);
    setSel(n);
  };
  const toggleAll = () => setSel(allPicked ? new Set() : new Set(rows.map(r => r.name)));
  const checkNow = async () => {
    let n = 0;
    for (const r of picked.slice(0, 50)) {
      try { await post('/monitors/' + enc(r.name) + '/check'); n++; } catch { /* rate limited */ }
    }
    toast(t('ui.mon.checks_queued', { n }));
  };
  // Pause or resume every ticked monitor in one change to the file.
  const pauseAll = async (paused) => {
    const names = picked.map(r => r.name);
    const etags = {};
    for (const r of picked) if (r.etag) etags[r.name] = r.etag;
    try {
      const res = await post('/monitors/bulk-pause', { names, paused, etags });
      toast(t(paused ? 'ui.mon.paused_n' : 'ui.mon.resumed_n', { n: fmtNum(res.changed) }));
      if (res.relayed) toast(t('ui.mon.pause_relayed'));
      if (res.skipped && res.skipped.length) toast(t('ui.mon.resume_skipped', { names: res.skipped.join(', ') }), 'error');
      list.reload();
    } catch (e) { toastError(e); }
  };
  const nPaused = picked.filter(r => r.status === 'paused').length;
  const remove = async () => {
    const names = picked.map(r => r.name);
    if (!await confirmDialog(tn('ui.mon.delete_selected_confirm', names.length, { names: names.slice(0, 8).join(', ') + (names.length > 8 ? ' …' : '') }), { danger: true, ok: t('ui.delete') })) return;
    const etags = {};
    for (const r of picked) if (r.etag) etags[r.name] = r.etag;
    try {
      await post('/monitors/bulk-delete', { names, etags });
      toast(tn('ui.mon.deleted_n', names.length));
      setSel(new Set());
      list.reload();
    } catch (e) { toastError(e); }
  };

  const total = list.data ? list.data.items.length : 0;
  return html`<div class="page list">
    <div class="row">
      <${Seg} value=${view} onChange=${changeView} options=${[['list', t('ui.mon.view_list')], ['hive', t('ui.mon.view_hive')]]} />
      <div class="spacer"></div>
      <button class="btn" onClick=${() => pauseAll(true)} disabled=${picked.length === nPaused} title=${picked.length ? '' : t('ui.mon.select_first')}>${t('ui.mon.pause')}${picked.length > nPaused ? ' (' + fmtNum(picked.length - nPaused) + ')' : ''}</button>
      <button class="btn" onClick=${() => pauseAll(false)} disabled=${!nPaused} title=${picked.length ? '' : t('ui.mon.select_first')}>${t('ui.mon.resume')}${nPaused ? ' (' + fmtNum(nPaused) + ')' : ''}</button>
      <a class="btn" href="#/expiry">${t('ui.exp.title')}</a>
      <a class="btn primary" href="#/monitors/new">${t('ui.mon.add')}</a>
      <button class="btn" onClick=${() => setQuick(true)}>${t('ui.mon.quick_add')}</button>
      <button class="btn" onClick=${checkNow} disabled=${!picked.length} title=${picked.length ? '' : t('ui.mon.select_first')}>${t('ui.mon.check_all')}${picked.length ? ' (' + fmtNum(picked.length) + ')' : ''}</button>
      <button class="btn danger" onClick=${remove} disabled=${!picked.length} title=${picked.length ? '' : t('ui.mon.select_first')}>${t('ui.delete')}${picked.length ? ' (' + fmtNum(picked.length) + ')' : ''}</button>
    </div>
    <div class="row filters">
      ${teams.data && teams.data.items.length > 1 && html`<select value=${termOf('team')} onChange=${(e) => setTerm('team', e.target.value)} aria-label=${t('ui.team.field')}>
        <option value="">${t('ui.mon.all_teams')}</option>
        ${teams.data.items.map(tm => html`<option value=${tm.name}>${teamTitle(tm)}</option>`)}</select>`}
      ${regions.data && regions.data.items.length > 1 && html`<select value=${termOf('region')} onChange=${(e) => setTerm('region', e.target.value)} aria-label=${t('ui.region.field')}>
        <option value="">${t('ui.mon.all_regions')}</option>
        ${regions.data.items.map(rg => html`<option value=${rg.name}>${regionTitle(rg.name)}</option>`)}</select>`}
      ${tagKeys.length > 0 && html`<select value=${termOf('tag')} onChange=${(e) => setTerm('tag', e.target.value)} aria-label=${t('ui.tags')}>
        <option value="">${t('ui.mon.all_tags')}</option>
        ${tagKeys.map(k => html`<option value=${k.toLowerCase()}>${k}</option>`)}</select>`}
      <div class="spacer"></div>
      ${me.role === 'admin' && html`<button class="btn" onClick=${() => setMoving({ team: '', region: '' })} disabled=${!picked.length} title=${picked.length ? '' : t('ui.mon.select_first')}>${t('ui.mon.move')}${picked.length ? ' (' + fmtNum(picked.length) + ')' : ''}</button>`}
    </div>
    ${moving && html`<${Modal} title=${tn('ui.mon.move_title', picked.length)} onClose=${() => setMoving(null)}>
      <form class="form" onSubmit=${move}>
        <${Field} label=${t('ui.team.field')}><select value=${moving.team} onChange=${(e) => setMoving({ ...moving, team: e.target.value })}>
          <option value="">${t('ui.mon.move_keep')}</option>
          ${((teams.data && teams.data.items) || []).map(tm => html`<option value=${tm.name}>${teamTitle(tm)}</option>`)}</select><//>
        <${Field} label=${t('ui.region.field')}><select value=${moving.region} onChange=${(e) => setMoving({ ...moving, region: e.target.value })}>
          <option value="">${t('ui.mon.move_keep')}</option>
          ${((regions.data && regions.data.items) || []).map(rg => html`<option value=${rg.name}>${regionTitle(rg.name)}</option>`)}</select><//>
        <div class="row"><div class="spacer"></div><button class="btn primary" disabled=${!moving.team && !moving.region}>${t('ui.mon.move')}</button></div>
      </form>
    <//>`}
    <div class="chips">
      <button class=${'chip' + (!status ? ' on' : '')} onClick=${() => setStatus(status)}>${t('ui.all')} <b>${fmtNum(total)}</b></button>
      ${CHIPS.filter(s => counts[s]).map(s => html`<button class=${'chip' + (status === s ? ' on' : '')} onClick=${() => setStatus(s)}>${t('status.' + s)} <b>${fmtNum(counts[s])}</b></button>`)}
    </div>
    <${ErrorBox} error=${list.error} retry=${list.reload} />
    <div class="card scroll"><div class="bd flush tbl-wrap">
      ${!list.data ? html`<${Loading} />` : !total ? html`<${Empty} title=${t('ui.mon.none')}><p>${t('ui.mon.none_hint')}</p><a class="btn primary" href="#/monitors/new">${t('ui.mon.add')}</a><//>` :
        !rows.length ? html`<${Empty} title=${t('ui.mon.no_match')} /> ` :
        view === 'hive' ? html`<div class="row" style=${{ padding: '12px 16px 0' }}><${Legend} /></div><${Hive} rows=${rows} />` :
        html`<table class="tbl">
          <thead><tr>
            <th class="sel"><input type="checkbox" checked=${allPicked} onChange=${toggleAll} aria-label=${t('ui.mon.select_all')} title=${t('ui.mon.select_all')} /></th>
            <th>${t('ui.status')}</th><th>${t('ui.monitor')}</th><th class="hide-sm">${t('ui.mon.last24h')}</th>
            <th class="right hide-sm">${t('ui.latency')}</th><th class="right">${t('ui.uptime24h')}</th><th class="hide-sm">${t('ui.mon.changed')}</th><th></th>
          </tr></thead>
          <tbody>${rows.map(r => html`<tr key=${r.name} class=${'click ' + r.status + (sel.has(r.name) ? ' picked' : '')} onClick=${() => go('/monitors/' + enc(r.name))}>
            <td class="sel" onClick=${(e) => e.stopPropagation()}><input type="checkbox" checked=${sel.has(r.name)} onChange=${() => toggle(r.name)} aria-label=${r.title} /></td>
            <td class="nowrap"><${Pill} status=${r.status} soft=${r.state_type === 'soft' && r.status !== 'ok' && r.status !== 'pending'} />
              ${r.state_type === 'soft' && r.attempt > 0 && r.status !== 'ok' && html` <span class="small muted">${r.attempt}/${r.max_attempts}</span>`}</td>
            <td style=${{ maxWidth: '420px' }}>
              <div class="row nowrap"><a class="name ellipsis" href=${'#/monitors/' + enc(r.name)} onClick=${(e) => e.stopPropagation()}>${r.title}</a>
                ${r.flags.includes('weak') && html`<span class="tag weak" title=${t('ui.flag.weak_hint')}>${t('ui.flag.weak')}</span>`}
                ${r.flags.includes('held') && html`<span class="tag" title=${t('ui.flag.held_hint')}>${t('ui.flag.held')}</span>`}
                ${r.incident && r.incident.acked && html`<span class="tag">${t('ui.inc.acked_short')}</span>`}
              </div>
              <div class="sub ellipsis"><span class="tag">${r.written_type}</span>${r.team && multiTeam ? html` <span class="tag" title=${t('ui.team.field')}>${r.team}</span>` : ''} <span class="ltr">${r.target}</span>
                ${r.status !== 'ok' && r.reason ? html` · <span class=${r.status === 'critical' ? 'err-text' : ''}>${reasonText(r.reason, r.detail)}</span>` : ''}</div>
            </td>
            <td class="hide-sm" style=${{ minWidth: '180px' }}><${Beats} cells=${beats.data && beats.data.items[r.name]} /></td>
            <td class="right nowrap hide-sm">${msText(r.latency_ms)}</td>
            <td class="right nowrap" title=${r.uptime_24h == null && r.coverage_24h > 0 ? t('ui.uptime.coverage', { pct: pct(r.coverage_24h, 0) }) : ''}>${pct(r.uptime_24h)}</td>
            <td class="nowrap small muted hide-sm"><${Ago} at=${r.last_change} /></td>
            <td class="right nowrap"><a class="btn sm" href=${'#/monitors/' + enc(r.name) + '/edit'} onClick=${(e) => e.stopPropagation()}>${t('ui.edit')}</a></td>
          </tr>`)}</tbody>
        </table>`}
    </div></div>
    <p class="small muted">${t('ui.mon.query_help')}</p>
    ${quick && html`<${QuickAdd} onClose=${() => setQuick(false)} onDone=${() => { setQuick(false); list.reload(); beats.reload(); }} team=${termOf('team')} region=${termOf('region')} />`}
  </div>`;
}
