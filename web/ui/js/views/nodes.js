// Nodes: the machines of a cluster, their connection state, configuration
// sync and tokens. Machines are added with a join code.
import { html, useState } from '../lib.js';
import { t, fmtTime } from '../i18n.js';
import { get, post, put, del, ifMatch } from '../api.js';
import { useFetch, useInterval, Loading, ErrorBox, Field, Modal, Copy, Ago, toast, toastError, confirmDialog, useDocTitle, Dot, msText } from '../ui.js';

export function Nodes({ embedded }) {
  useDocTitle(t(embedded ? 'ui.nav.settings' : 'ui.nav.runmode'));
  const list = useFetch(() => get('/nodes'), []);
  useInterval(() => list.reload(), 10000);
  const [joinCode, setJoinCode] = useState(false);
  const [joining, setJoining] = useState(false);
  const [editing, setEditing] = useState(null);
  if (!list.data) return list.error ? html`<${ErrorBox} error=${list.error} retry=${list.reload} />` : html`<${Loading} />`;
  const d = list.data;
  if (d.agent && d.agent.member) return html`<${Membership} state=${d.agent} d=${d} reload=${list.reload} />`;

  const revoke = async (n) => {
    if (!await confirmDialog(t('ui.node.revoke_confirm', { node: n.name }), { danger: true, ok: t('ui.node.revoke') })) return;
    try { await del('/nodes/' + encodeURIComponent(n.name) + '/token'); list.reload(); } catch (e) { toastError(e); }
  };
  const handover = async (n) => {
    if (!await confirmDialog(t('ui.ha.handover_confirm', { node: n.name, addr: n.standby }), { danger: true, ok: t('ui.ha.handover') })) return;
    try { const r = await post('/nodes/' + encodeURIComponent(n.name) + '/handover', {}); toast(t('ui.ha.handover_started', { node: n.name, term: r.term })); list.reload(); } catch (e) { toastError(e); }
  };
  const remove = async (n) => {
    if (!await confirmDialog(t('ui.node.delete_confirm', { node: n.name }), { danger: true, ok: t('ui.delete') })) return;
    try { await del('/nodes/' + encodeURIComponent(n.name), ifMatch(n.etag)); list.reload(); } catch (e) { toastError(e); }
  };

  return html`<div class="col" style=${{ gap: '12px' }}>
    <div class="row">
      <p class="muted small spacer" style=${{ margin: 0 }}>${t(d.layout === 'single' ? 'ui.node.intro_single' : 'ui.node.intro')}</p>
      ${d.layout === 'single' && html`<button class="btn" onClick=${() => setJoining(true)}>${t('ui.join.join')}</button>`}
      <button class="btn primary" onClick=${() => setJoinCode(true)}>${t('ui.join.code')}</button>
    </div>
    <div class="card"><div class="bd flush tbl-wrap"><table class="tbl">
      <thead><tr>
        <th>${t('ui.name')}</th><th>${t('ui.status')}</th><th>${t('ui.node.config')}</th><th>${t('ui.node.rtt')}</th>
        <th>${t('ui.node.clock')}</th><th>${t('ui.node.version')}</th><th>${t('ui.node.token')}</th><th></th>
      </tr></thead>
      <tbody>${d.nodes.map(n => html`<${NodeRow} n=${n} onRevoke=${revoke} onRemove=${remove} onEdit=${setEditing} onHandover=${handover} />`)}</tbody>
    </table></div></div>
    <p class="faint small ltr" style=${{ margin: 0 }}>${t('ui.node.fingerprint')}: <span class="mono">${d.fingerprint}</span></p>
    <${Diverged} />
    <${SiteLocal} single=${d.layout === 'single'} />
    ${editing && html`<${NodeEdit} n=${editing} onClose=${() => { setEditing(null); list.reload(); }} />`}
    ${joinCode && html`<${JoinCode} server=${d.server} onClose=${() => { setJoinCode(false); list.reload(); }} />`}
    ${joining && html`<${JoinForm} d=${d} onClose=${() => { setJoining(false); list.reload(); }} />`}
  </div>`;
}

// NodeEdit changes a node's record in monitors.conf.
function NodeEdit({ n, onClose }) {
  const [f, setF] = useState({ title: n.title || '', region: n.region || '', probe: n.probe, policy: n.self ? '' : (n.policy === 'system' ? '' : n.policy), anchors: n.anchors_text || '', timeout: '', standby: n.standby || '' });
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const save = async (e) => {
    e.preventDefault();
    try {
      await put('/nodes/' + encodeURIComponent(n.name), { title: f.title.trim(), region: f.region.trim(), probe: f.probe, policy: f.policy.trim(), anchors: f.anchors.trim(), node_timeout: f.timeout.trim(), standby: f.standby.trim() }, ifMatch(n.etag));
      onClose();
    } catch (e2) { toastError(e2); }
  };
  return html`<${Modal} title=${n.name} onClose=${onClose}>
    <form class="form" onSubmit=${save}>
      <div class="two">
        <${Field} label=${t('ui.node.title')}><input type="text" value=${f.title} onInput=${set('title')} /><//>
        <${Field} label=${t('ui.node.region')}><input type="text" value=${f.region} onInput=${set('region')} /><//>
      </div>
      <label class="switch"><input type="checkbox" checked=${f.probe} onChange=${(e) => setF({ ...f, probe: e.target.checked })} /> ${t('ui.node.probes')}</label>
      <${Field} label=${t('ui.node.anchors')} hint=${t('ui.node.anchors_hint')}><input type="text" class="ltr mono" value=${f.anchors} onInput=${set('anchors')} placeholder="lan:10.0.0.1;wan:223.5.5.5" /><//>
      ${!n.self && html`<div class="two">
        <${Field} label=${t('ui.node.policy')}><input type="text" class="ltr" value=${f.policy} onInput=${set('policy')} placeholder="system" /><//>
        <${Field} label=${t('ui.node.timeout')}><input type="text" class="ltr" value=${f.timeout} onInput=${set('timeout')} placeholder=${n.timeout_s ? n.timeout_s + 's' : '60s'} /><//>
      </div>
      <${Field} label=${t('ui.ha.standby_addr')} hint=${t('ui.ha.standby_hint')}><input type="url" class="ltr" value=${f.standby} onInput=${set('standby')} placeholder="https://10.0.0.2:8077" /><//>`}
      <div class="row"><div class="spacer"></div><button class="btn primary">${t('ui.save')}</button></div>
    </form>
  <//>`;
}

// JoinCode issues a one-time code another machine pastes to join.
function JoinCode({ server, onClose }) {
  const [addr, setAddr] = useState(server);
  const [res, setRes] = useState(null);
  const make = async (e) => {
    if (e) e.preventDefault();
    try { setRes(await post('/cluster/join-codes', { server: addr })); } catch (e2) { toastError(e2); }
  };
  return html`<${Modal} wide title=${t('ui.join.code')} onClose=${onClose}>
    <form class="form" onSubmit=${make}>
      <p class="small muted" style=${{ margin: 0 }}>${t('ui.join.code_intro')}</p>
      <${Field} label=${t('ui.node.server')} hint=${t('ui.node.server_hint')}>
        <div class="row nowrap"><input type="url" class="ltr" value=${addr} onInput=${(e) => setAddr(e.target.value.trim())} />
          <button class="btn primary">${t('ui.join.generate')}</button></div><//>
      ${res && html`<div class="col" style=${{ gap: '8px' }}>
        <div class="row"><span class="small muted spacer">${t('ui.join.expires', { time: fmtTime(res.expires) })}</span><${Copy} text=${res.code} /></div>
        <pre class="code" tabindex="0" style=${{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>${res.code}</pre>
      </div>`}
    </form>
  <//>`;
}

// JoinForm turns this standalone server into a node of a cluster.
function JoinForm({ d, onClose }) {
  const [f, setF] = useState({ code: '', node: (d.instance || '').toLowerCase().replace(/[^a-z0-9_.-]/g, '-').slice(0, 63), title: '', region: '', keep: 'clear' });
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(null);
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const submit = async (e) => {
    e.preventDefault();
    if (d.monitors > 0 && f.keep === 'clear' && !await confirmDialog(t('ui.join.clear_confirm', { n: d.monitors }), { danger: true, ok: t('ui.join.join') })) return;
    setBusy(true);
    try {
      setDone(await post('/cluster/join', { code: f.code.trim(), node: f.node.trim(), title: f.title.trim(), region: f.region.trim(), import: f.keep === 'import' }));
    } catch (e2) { toastError(e2); }
    setBusy(false);
  };
  if (done) {
    return html`<${Modal} title=${t('ui.join.join')} onClose=${onClose} footer=${html`<button class="btn primary" onClick=${onClose}>${t('ui.ok')}</button>`}>
      <div class="banner ok"><div class="spacer">${t('ui.join.joined', { node: done.node })}</div></div>
      ${done.imported && done.imported.length > 0 && html`<p class="small">${t('ui.join.imported', { n: done.imported.length, node: done.node })}</p>`}
      ${done.skipped && html`<div class="banner warning"><div class="spacer">${t('ui.join.skipped')}<div class="small ltr">${done.skipped}</div></div></div>`}
    <//>`;
  }
  return html`<${Modal} wide title=${t('ui.join.join')} onClose=${onClose}>
    <form class="form" onSubmit=${submit}>
      <p class="small muted" style=${{ margin: 0 }}>${t('ui.join.intro')}</p>
      <${Field} label=${t('ui.join.paste')}><textarea class="ltr mono" rows="3" required value=${f.code} onInput=${set('code')} placeholder="nkjoin1.…"></textarea><//>
      <div class="two">
        <${Field} label=${t('ui.name')} hint=${t('ui.node.name_hint')}><input type="text" class="ltr" required pattern="[A-Za-z0-9][A-Za-z0-9_.\\-]{0,62}" value=${f.node} onInput=${set('node')} /><//>
        <${Field} label=${t('ui.node.region')}><input type="text" value=${f.region} onInput=${set('region')} /><//>
      </div>
      <${Field} label=${t('ui.node.title')}><input type="text" value=${f.title} onInput=${set('title')} /><//>
      ${d.monitors > 0 && html`<${Field} label=${t('ui.join.local', { n: d.monitors })}>
        <div class="col">
          <label class="switch"><input type="radio" name="keep" checked=${f.keep === 'clear'} onChange=${() => setF({ ...f, keep: 'clear' })} /> <b>${t('ui.join.clear')}</b> <span class="small muted">${t('ui.join.clear_hint')}</span></label>
          <label class="switch"><input type="radio" name="keep" checked=${f.keep === 'import'} onChange=${() => setF({ ...f, keep: 'import' })} /> <b>${t('ui.join.import')}</b> <span class="small muted">${t('ui.join.import_hint')}</span></label>
        </div><//>`}
      <div class="row"><div class="spacer"></div><button class="btn primary" disabled=${busy}>${busy ? html`<span class="spin"></span>` : t('ui.join.join')}</button></div>
    </form>
  <//>`;
}

// Membership is the page of a server that joined a cluster.
function Membership({ state, d, reload }) {
  const m = state.member, s = state.status || {};
  const sb = useFetch(() => get('/cluster/agent'), []);
  const copy = sb.data && sb.data.standby;
  const promote = async () => {
    const age = copy && copy.fetched ? Math.max(0, Math.round((Date.now() - new Date(copy.fetched)) / 1000)) : 0;
    const msg = t('ui.ha.promote_confirm', { master: m.server }) + ' ' + t('ui.ha.promote_age', { s: age, at: copy ? fmtTime(copy.fetched) : '–' }) + ' '
      + t(s.connected ? 'ui.ha.promote_sync' : 'ui.ha.promote_nosync');
    if (!await confirmDialog(msg, { danger: true, ok: t('ui.ha.promote') })) return;
    const go = async (force) => { const r = await post('/cluster/promote', force ? { force: true } : {}); toast(t('ui.ha.promoting', { term: r.term })); };
    try { await go(false); } catch (e) {
      // The master still answers: taking over would make two masters. Say
      // so, and go ahead only if the administrator insists (a broken master).
      if (e.code !== 'master_alive') return toastError(e);
      if (!await confirmDialog(t('ui.ha.master_alive', { master: e.body.master || m.server }), { danger: true, ok: t('ui.ha.promote_force') })) return;
      try { await go(true); } catch (e2) { toastError(e2); }
    }
  };
  const leave = async () => {
    if (!await confirmDialog(t('ui.join.leave_confirm', { master: m.server }), { danger: true, ok: t('ui.join.leave') })) return;
    try { await del('/cluster/agent'); reload(); } catch (e) { toastError(e); }
  };
  return html`<div class="card"><div class="hd"><h2>${t('ui.join.member')}</h2><div class="spacer"></div>
      <button class="btn danger" onClick=${leave}>${t('ui.join.leave')}</button></div>
    <div class="bd"><dl class="kv">
      <dt>${t('ui.node.server')}</dt><dd class="mono ltr">${m.server}</dd>
      <dt>${t('ui.name')}</dt><dd class="mono">${m.node}</dd>
      <dt>${t('ui.status')}</dt><dd><${Dot} status=${s.connected ? 'ok' : 'critical'} /> ${s.connected ? t('ui.node.online') : t('ui.node.offline')}
        ${s.since && html` · <${Ago} at=${s.since} />`}</dd>
      ${s.last_error && html`<dt>${t('ui.join.last_error')}</dt><dd class="small ltr err-text">${s.last_error}</dd>`}
      <dt>${t('ui.node.config')}</dt><dd><span class="mono">${s.serial || '–'}</span> · ${t('ui.join.monitors', { n: s.monitors || 0 })}</dd>
      <dt>${t('ui.node.fingerprint')}</dt><dd class="mono ltr small">${m.fingerprint}</dd>
      <dt>${t('ui.join.joined_at')}</dt><dd>${fmtTime(m.joined)}</dd>
      ${copy && copy.size > 0 && html`<dt>${t('ui.ha.copy')}</dt><dd>${fmtTime(copy.fetched)} (<${Ago} at=${copy.fetched} />) · ${t('ui.ha.term', { n: copy.term })}${copy.error ? html` <span class="err-text small">${copy.error}</span>` : ''}</dd>`}
      ${d.replica && d.replica.replica && html`<dt>${t('ui.rep.mode')}</dt><dd>${d.replica.local
        ? html`<span class="tag warn">${t('ui.rep.local')}</span> <span class="small muted">${t('ui.rep.local_hint', { n: d.replica.monitors })}</span>`
        : html`<span class="tag">${t('ui.rep.follow')}</span>`}</dd>`}
      <dt>${t('ui.gw.title')}</dt><dd><${Gateway} g=${d.gateway} /></dd>
    </dl>
    ${d.peers && d.peers.length > 0 && html`<h3 style=${{ margin: '14px 0 6px' }}>${t('ui.rep.peers')}</h3>
      <table class="tbl"><tbody>${d.peers.map(p => {
        const ln = (d.local_nodes || []).find(x => x.node === p.name);
        return html`<tr><td class="mono">${p.name}</td><td class="mono ltr small">${p.addr}</td>
          <td><${Dot} status=${p.ok ? 'ok' : 'critical'} /> ${p.ok ? t('ui.rep.reachable') : t('ui.rep.unreachable')}</td>
          <td>${ln && ln.verdict && html`<${Verdict} v=${ln.verdict} w=${ln.witnesses} />`}</td></tr>`;
      })}</tbody></table>`}
    ${copy && copy.size > 0 && html`<div class="row" style=${{ marginTop: '12px' }}><p class="small muted spacer" style=${{ margin: 0 }}>${t('ui.ha.promote_hint')}</p>
      <button class="btn danger" onClick=${promote}>${t('ui.ha.promote')}</button></div>`}
    </div></div>
    <${SiteLocal} />`;
}

// SiteLocal edits this site's own configuration: checked and alerted on
// here only, never synced, never overwritten by the master. On a single
// machine it is the same as the main configuration, so it stays out of the
// way there unless it already holds something.
const SL_EXAMPLE = 'router IN PING 192.168.1.1\nprinter IN TCP 192.168.1.20:9100\nnas IN HTTP http://192.168.1.30:5000/';

function SiteLocal({ single }) {
  const r = useFetch(() => get('/cluster/site-local'), []);
  const [text, setText] = useState(null);
  const [res, setRes] = useState(null);
  const [busy, setBusy] = useState(false);
  if (!r.data) return null;
  if (single && !r.data.text && text === null) return null;
  const cur = text === null ? r.data.text : text;
  const save = async () => {
    setBusy(true);
    try {
      const x = await put('/cluster/site-local', { text: cur });
      if (x.text !== undefined && x.text !== cur) setText(x.text);
      if (x.ok) { toast(t('ui.sl.saved')); setRes(null); setText(null); r.reload(); } else setRes(x);
    } catch (e) { toastError(e); }
    setBusy(false);
  };
  const lines = cur.split('\n');
  const replace = (list) => {
    const next = [...lines];
    for (const sg of list) if (next[sg.line - 1] !== undefined && next[sg.line - 1].trim() === sg.from) next[sg.line - 1] = sg.text;
    setText(next.join('\n')); setRes(null);
  };
  const msg = (d) => d.code === 'syntax' && /^expected "<name>/.test(d.message || d.msg || '') ? t('ui.sl.err_record') : (d.message || d.msg);
  const sugg = (res && res.suggestions) || [];
  return html`<div class="card"><div class="hd"><h2>${t('ui.sl.title')}</h2><span class="tag mono ltr" title=${t('ui.sl.file_hint')}>${r.data.file}</span></div><div class="bd form">
    <p class="small muted" style=${{ margin: 0 }}>${t('ui.sl.hint')}</p>
    <div class="small" style=${{ margin: 0 }}>
      <div>${t('ui.sl.howto')}</div>
      <pre class="mono ltr small" style=${{ margin: '6px 0 0' }}>${SL_EXAMPLE}</pre>
      <div class="muted">${t('ui.sl.howto_more')}</div>
    </div>
    <textarea class="ltr mono" rows="6" value=${cur} onInput=${(e) => setText(e.target.value)} aria-label=${t('ui.sl.title')}
      placeholder=${SL_EXAMPLE}></textarea>
    ${res && res.diagnostics && html`<ul class="small err-text">${res.diagnostics.map(d => html`<li>${d.pos && d.pos.line ? t('ui.sl.line', { n: d.pos.line }) + ' ' : ''}${msg(d)}</li>`)}</ul>`}
    ${sugg.length > 0 && html`<div class="small">
      <div>${t('ui.sl.suggest')}</div>
      <ul style=${{ margin: '4px 0 0' }}>${sugg.map(sg => html`<li class="row" style=${{ gap: '8px' }}>
        <span>${t('ui.sl.line', { n: sg.line })}</span><code class="ltr">${sg.text}</code>
        <button class="btn sm" onClick=${() => replace([sg])}>${t('ui.sl.replace')}</button></li>`)}</ul>
      ${sugg.length > 1 && html`<button class="btn" style=${{ marginTop: '6px' }} onClick=${() => replace(sugg)}>${t('ui.sl.replace_all')}</button>`}
    </div>`}
    <div class="row">
      ${!cur.trim() && html`<button class="btn" onClick=${() => setText(SL_EXAMPLE + '\n')}>${t('ui.sl.insert_example')}</button>`}
      <div class="spacer"></div><button class="btn primary" disabled=${busy || text === null} onClick=${save}>${t('ui.save')}</button></div>
  </div></div>`;
}

function NodeRow({ n, onRevoke, onRemove, onEdit, onHandover }) {
  const offline = !n.self && !n.online;
  return html`<tr>
    <td>
      <div class="name">${n.name} ${n.self && html`<span class="tag">${t('ui.node.self')}</span>`}</div>
      <div class="sub">${[n.title, n.region].filter(Boolean).join(' · ')}${!n.probe && html` <span class="tag weak">${t('ui.node.probe_no')}</span>`}
        ${n.standby && html` <span class="tag" title=${n.standby}>${t('ui.ha.standby')}</span>`}</div>
    </td>
    <td class="nowrap">
      <${Dot} status=${n.online ? 'ok' : (n.last_seen ? 'critical' : 'unknown')} />
      ${' '}${n.self ? t('ui.node.online') : n.online ? t('ui.node.online') : n.last_seen ? t('ui.node.offline') : t('ui.node.never')}
      ${!n.self && n.since && html`<div class="sub"><${Ago} at=${n.since} /></div>`}
      ${!n.self && n.verdict && n.verdict !== 'ok' && html`<div class="sub"><${Verdict} v=${n.verdict} w=${n.witnesses} /></div>`}
      ${n.gateway && n.gateway.length > 0 && html`<div class="sub"><${Gateway} g=${n.gateway} /></div>`}
    </td>
    <td class="nowrap">
      ${n.serial ? html`<span class="mono ltr">${n.serial}</span>` : html`<span class="faint">–</span>`}
      ${!n.self && n.serial > 0 && html` <span class=${'tag' + (n.synced ? '' : ' warn')}>${t(n.synced ? 'ui.node.synced' : 'ui.node.behind')}</span>`}
    </td>
    <td class="nowrap">${n.self ? html`<span class="faint">–</span>` : msText(n.rtt_ms)}</td>
    <td class="nowrap">${n.self || offline ? html`<span class="faint">–</span>`
      : n.clock_ok ? html`<span class="ok-text">✓</span>` : html`<span class="warn-text">${t('ui.node.clock_off', { ms: Math.round(n.skew_ms) })}</span>`}</td>
    <td class="small">${n.version || ''}<div class="sub ltr">${n.platform || ''}</div>${n.unsupported && html`<div class="sub warn-text" title=${n.unsupported.join(', ')}>${t('ui.node.needs_upgrade', { n: n.unsupported.length })}</div>`}</td>
    <td class="small">
      ${n.self ? '' : n.token ? html`<span class="mono ltr">${n.token.prefix}…</span><div class="sub"><${Ago} at=${n.token.last_used} />${n.token.last_addr ? ' · ' + n.token.last_addr : ''}</div>`
        : html`<span class="faint">${t('ui.node.no_token')}</span>`}
    </td>
    <td class="right nowrap">${n.standby && n.online && html`<button class="btn sm" onClick=${() => onHandover(n)}>${t('ui.ha.handover')}</button> `}<button class="btn sm" onClick=${() => onEdit(n)}>${t('ui.edit')}</button>${!n.self && html`
      ${n.token && html` <button class="btn sm" onClick=${() => onRevoke(n)}>${t('ui.node.revoke')}</button>`}
      ${' '}<button class="btn sm danger" onClick=${() => onRemove(n)}>${t('ui.delete')}</button>`}</td>
  </tr>`;
}

// Verdict is the majority judgement of a node that cannot be reached.
function Verdict({ v, w }) {
  const cls = v === 'down' ? 'tag err' : v === 'unknown' ? 'tag weak' : 'tag warn';
  return html`<span class=${cls} title=${t('ui.verdict.' + v + '_hint')}>${t('ui.verdict.' + v)}</span>${v === 'down' && w > 0 ? html` <span class="small muted">${t('ui.verdict.witnesses', { n: w })}</span>` : ''}`;
}

// Gateway shows whether the node's last notifications went out.
function Gateway({ g }) {
  if (!g || g.length === 0) return html`<span class="faint small">${t('ui.gw.none')}</span>`;
  return html`${g.slice(0, 4).map(x => html`<span class=${'tag' + (x.ok ? '' : ' err')} title=${(x.error || '') + ' · ' + fmtTime(x.at)}>${x.ok ? '✓' : '!'} ${x.channel} (${x.kind})</span> `)}`;
}

// Diverged lists configurations a replaced master kept changing after the
// copy this master took over with: shown for review, never applied.
function Diverged() {
  const r = useFetch(() => get('/cluster/diverged'), []);
  const [open, setOpen] = useState(null);
  if (!r.data || !r.data.items.length) return null;
  const view = async (d) => {
    try { const x = await get('/cluster/diverged?text=1'); setOpen(x.items.find(y => y.id === d.id)); } catch (e) { toastError(e); }
  };
  const dismiss = async (d) => {
    try { await del('/cluster/diverged/' + d.id); r.reload(); } catch (e) { toastError(e); }
  };
  return html`<div class="card"><div class="hd"><h2>${t('ui.dv.title')}</h2></div><div class="bd">
    <p class="small muted" style=${{ margin: '0 0 8px' }}>${t('ui.dv.hint')}</p>
    <table class="tbl"><tbody>${r.data.items.map(d => html`<tr>
      <td class="mono">${d.site}</td>
      <td>${t('ui.dv.versions', { v: d.version, term: d.term, base: d.base_version })}</td>
      <td class="small">${fmtTime(d.at)}</td>
      <td class="right nowrap"><button class="btn sm" onClick=${() => view(d)}>${t('ui.dv.view')}</button> <button class="btn sm" onClick=${() => dismiss(d)}>${t('ui.dv.dismiss')}</button></td>
    </tr>`)}</tbody></table>
    ${open && html`<${Modal} title=${t('ui.dv.title')} onClose=${() => setOpen(null)}><pre class="ltr mono small" style=${{ maxHeight: '60vh', overflow: 'auto' }}>${open.text}</pre><//>`}
  </div></div>`;
}
