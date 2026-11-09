// Monitor editor and quick add. Both write records into monitors.conf.
import { html, useState, useEffect } from '../lib.js';
import { t, detailText } from '../i18n.js';
import { get, post, put, ifMatch, newKey, me } from '../api.js';
import { useFetch, Loading, ErrorBox, Field, Modal, Pill, msText, reasonText, enc, go, toast, toastError, errText, useDocTitle, teamTitle, regionTitle, writableTeams } from '../ui.js';

// Check types, grouped for the type menu.
const TYPE_GROUPS = [
  ['web', ['HTTP', 'TCP', 'PING', 'DOMAIN']],
  ['network', ['DNS', 'NTP', 'SNMP', 'SYSLOG', 'UDP']],
  ['service', ['REDIS', 'POSTGRES', 'MYSQL', 'GRPC', 'DOCKER']],
  ['other', ['EXEC', 'PUSH']],
];

// Types checked on a host and a port: the port has its own field and is
// left out of the target when it is the default (10.0.0.53, not
// 10.0.0.53:53). TCP has no default port.
const DEFAULT_PORT = { TCP: '', DNS: '53', NTP: '123', SNMP: '161', SYSLOG: '514', REDIS: '6379', POSTGRES: '5432', MYSQL: '3306', GRPC: '50051' };
const hasPort = (type) => type in DEFAULT_PORT;

// splitHostPort reads "host", "host:port" or "[v6]:port".
function splitHostPort(target) {
  const v6 = /^\[([^\]]+)\](?::(\d+))?$/.exec(target);
  if (v6) return [v6[1], v6[2] || ''];
  const m = /^([^:]+):(\d+)$/.exec(target);
  return m ? [m[1], m[2]] : [target, ''];
}
function joinHostPort(host, port) {
  if (!port) return host;
  return (host.includes(':') ? '[' + host + ']' : host) + ':' + port;
}

const DNS_TYPES = ['A', 'AAAA', 'CNAME', 'MX', 'NS', 'TXT', 'SOA', 'PTR', 'SRV', 'CAA'];
// Options with their own form fields, per type.
const OWN_FIELDS = { DNS: ['query', 'qtype', 'expect'] };

const PLACEHOLDER = {
  HTTP: 'https://example.com/health', TCP: 'db.example.lan:5432', PING: '192.168.1.1', UDP: '10.0.0.1:ntp',
  DNS: '192.168.1.1', NTP: 'pool.ntp.org', SNMP: '10.0.0.2', SYSLOG: '10.0.0.3', EXEC: 'check_disk -w 20% -c 10% -p /', PUSH: '',
  REDIS: '10.0.0.20:6379', POSTGRES: 'db.example.lan:5432', MYSQL: 'db.example.lan:3306', GRPC: 'api.example.lan:50051', DOCKER: 'nginx', DOMAIN: 'example.com',
};

const COMMON = ['timeout', 'retry', 'attempts', 'recover-attempts', 'degraded', 'scope', 'authorized', 'family', 'on-filtered', 'parent', 'cert-days', 'cert-critical-days', 'handler'];
const TYPE_OPTS = {
  HTTP: ['method', 'expect', 'keyword', 'keyword-regex', 'absent', 'json-path', 'json-expect', 'json-expect-num', 'body', 'host', 'basic', 'bearer', 'proxy', 'no-follow', 'insecure', 'user-agent', 'header.Authorization'],
  TCP: ['send', 'send-hex', 'expect', 'expect-hex', 'expect-regex', 'tls', 'servername', 'insecure'],
  PING: ['count', 'loss', 'rtt', 'size'],
  UDP: ['send', 'send-hex', 'expect', 'expect-hex', 'query', 'qtype', 'rcode', 'recurse', 'community', 'version', 'oid', 'expect-program', 'accept-from'],
  DNS: ['query', 'qtype', 'expect', 'expect-all', 'rcode', 'recurse'],
  REDIS: ['password', 'user', 'role', 'tls', 'insecure'],
  POSTGRES: ['user', 'password', 'database', 'ssl', 'insecure'],
  MYSQL: ['user', 'password', 'database', 'ssl', 'insecure'],
  GRPC: ['service', 'tls', 'servername', 'insecure'],
  DOCKER: ['socket'],
  DOMAIN: ['days', 'critical-days', 'rdap', 'whois'],
  NTP: ['expect'],
  SNMP: ['community', 'version', 'oid', 'expect', 'expect-num'],
  SYSLOG: [],
  EXEC: ['args', 'address'],
  PUSH: ['period', 'grace'],
};
// Options shown as their own form fields rather than in the table
// ("proto" is implied by the service types).
const META = new Set(['title', 'description', 'tags', 'group', 'policy', 'team', 'region', 'proto', 'mute', 'system-channel']);
const yes = (v) => ['yes', 'true', 'on', '1'].includes(String(v || '').toLowerCase());

function secondsToInterval(s) {
  if (!s) return '';
  if (s % 3600 === 0) return s / 3600 + 'h';
  if (s % 60 === 0) return s / 60 + 'm';
  return s + 's';
}

function ProbeResult({ r }) {
  const st = r.vote === 'ok' ? (r.degraded ? 'degraded' : 'ok') : r.vote === 'fail' ? 'critical' : 'unknown';
  const ev = r.evidence || {};
  return html`<div class=${'banner ' + (st === 'ok' ? 'ok' : st === 'critical' ? 'error' : 'warning')}>
    <div class="col" style=${{ gap: '4px' }}>
      <div class="row"><${Pill} status=${st} /> <b>${reasonText(r.reason, r.detail)}</b> <span class="muted">${msText(r.latency_ms)} · ${r.depth}</span></div>
      <div class="small">${detailText(r.detail)}</div>
      ${ev.peer && html`<div class="small ltr">${t('ui.ev.peer')}: ${ev.peer}</div>`}
      ${(ev.decoded || []).map(f => html`<div class="small"><span class="muted">${f.name}:</span> <span class="ltr">${f.value}</span></div>`)}
      ${r.metrics && Object.entries(r.metrics).map(([k, v]) => html`<div class="small ltr"><span class="muted">${k}:</span> ${v}</div>`)}
    </div>
  </div>`;
}

export function MonitorEditor({ name }) {
  useDocTitle(name ? t('ui.edit') + ' ' + name : t('ui.mon.add'));
  const editing = !!name;
  const teams = useFetch(() => get('/teams'), []);
  const regions = useFetch(() => get('/regions'), []);
  const mons = useFetch(() => get('/monitors'), []);
  const [f, setF] = useState(null);
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const [probe, setProbe] = useState(null);
  const [probing, setProbing] = useState(false);
  const [same, setSame] = useState(null);

  useEffect(() => {
    if (!editing) {
      setF({ key: newKey(), type: 'HTTP', name: '', title: '', target: '', port: '', interval: '', group: '', tags: '', policy: '', team: '', region: '', description: '', mute: false, sys: false, opts: [], message: '' });
      return;
    }
    get('/monitors/' + enc(name)).then(d => {
      const o = d.opts || {};
      const opts = Object.entries(o).filter(([k]) => !META.has(k)).map(([k, v]) => ({ k, v }));
      const type = (d.row.written_type || d.row.type).toUpperCase();
      let target = d.row.target, port = '';
      if (hasPort(type)) {
        [target, port] = splitHostPort(target);
        if (port === DEFAULT_PORT[type]) port = '';
      }
      setF({ etag: d.row.etag, type, name, title: o.title || '', target, port, interval: secondsToInterval(d.row.interval_s), group: o.group || '', tags: o.tags || '',
        policy: o.policy || '', team: o.team || 'admin', region: o.region || '', description: o.description || '',
        mute: yes(o.mute), sys: yes(o['system-channel']), opts, message: '' });
    }, setErr);
  }, [name]);

  // While the target is being written: monitors that already check exactly
  // the same thing (they can share one check), and how many watch the host.
  const draftKey = f ? JSON.stringify([f.type, f.target, f.port, f.opts]) : '';
  useEffect(() => {
    if (!f || f.type === 'PUSH' || !f.target.trim()) { setSame(null); return undefined; }
    const timer = setTimeout(() => {
      post('/monitors/same-check', { record: { ...record(), name: 'x' }, exclude: name || '' }).then(setSame, () => setSame(null));
    }, 400);
    return () => clearTimeout(timer);
  }, [draftKey]);

  // A new monitor goes to the only team the user can add to, or to the
  // admin team for an administrator; someone in several teams chooses.
  const writable = writableTeams(teams.data);
  useEffect(() => {
    // Updated from the current form: the user may have typed meanwhile.
    if (!editing && writable.length) setF((cur) => (cur && !cur.team ? { ...cur, team: me.teams ? (writable.length === 1 ? writable[0].name : '') : 'admin' } : cur));
  }, [teams.data, !!f]);

  if (!f) return html`<div class="page">${err ? html`<${ErrorBox} error=${err} />` : html`<${Loading} />`}</div>`;
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const setOpt = (i, k, v) => { const opts = f.opts.slice(); opts[i] = { ...opts[i], [k]: v }; setF({ ...f, opts }); };
  // Options with their own fields live in the same list as the others.
  const own = OWN_FIELDS[f.type] || [];
  const optVal = (k) => (f.opts.find(o => o.k === k) || {}).v || '';
  const setOwn = (k) => (e) => {
    const v = e.target.value;
    const opts = f.opts.filter(o => o.k !== k);
    setF({ ...f, opts: v ? [...opts, { k, v }] : opts });
  };
  function record() {
    const opts = {};
    for (const o of f.opts) if (o.k.trim()) opts[o.k.trim()] = o.v;
    for (const k of ['title', 'group', 'tags', 'policy', 'team', 'region', 'description']) if (f[k].trim()) opts[k] = f[k].trim();
    if (f.mute) opts.mute = 'yes';
    else if (f.sys) opts['system-channel'] = 'yes';
    let target = f.type === 'PUSH' ? '' : f.target.trim();
    if (hasPort(f.type)) target = joinHostPort(target, f.port.trim());
    return { name: f.name.trim(), type: f.type, target, interval: f.interval.trim(), opts };
  }
  const groups = [...new Set(((mons.data && mons.data.items) || []).map(r => r.group).filter(Boolean))].sort();
  const test = async () => {
    setProbing(true);
    setProbe(null);
    try { setProbe(await post('/probe', { record: { ...record(), name: record().name || 'probe-test' } })); } catch (e) { setProbe({ error: e }); }
    setProbing(false);
  };
  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const body = { record: record(), message: f.message };
      if (editing) await put('/monitors/' + enc(name), body, ifMatch(f.etag));
      else await post('/monitors', body, { 'Idempotency-Key': f.key });
      toast(t('ui.saved'));
      go('/monitors/' + enc(body.record.name));
    } catch (e2) { setErr(e2); }
    setBusy(false);
  };
  const suggestions = [...(TYPE_OPTS[f.type] || []), ...COMMON];
  const diags = err && err.body && err.body.diagnostics;
  return html`<div class="page">
    <div class="row"><a href=${editing ? '#/monitors/' + enc(name) : '#/monitors'} class="muted">← ${t('ui.back')}</a></div>
    <form class="card" onSubmit=${save}>
      <div class="hd"><h2>${editing ? t('ui.mon.edit_title', { name }) : t('ui.mon.add')}</h2></div>
      <div class="bd form">
        <${Field} label=${t('ui.type')} hint=${t('ui.mon.type_hint.' + f.type)}>
          <select value=${f.type} onChange=${(e) => setF({ ...f, type: e.target.value, port: '' })} disabled=${editing && f.type === 'PUSH'}>
            ${TYPE_GROUPS.map(([g, list]) => html`<optgroup label=${t('ui.mon.tgroup.' + g)}>${list.map(x => html`<option value=${x}>${x} — ${t('ui.mon.tname.' + x)}</option>`)}</optgroup>`)}
          </select><//>
        <div class="two">
          <${Field} label=${t('ui.name')} hint=${t('ui.mon.name_hint')}><input type="text" class="ltr" value=${f.name} onInput=${set('name')} required pattern="[A-Za-z0-9][A-Za-z0-9_.\\-]{0,62}" /><//>
          <${Field} label=${t('ui.title')}><input type="text" value=${f.title} onInput=${set('title')} placeholder=${f.name} /><//>
        </div>
        ${f.type !== 'PUSH' && !hasPort(f.type) && html`<${Field} label=${t('ui.target')} hint=${t('ui.mon.target_hint.' + f.type)}>
          <input type="text" class="ltr" value=${f.target} onInput=${set('target')} placeholder=${PLACEHOLDER[f.type]} required /><//>`}
        ${hasPort(f.type) && html`<div class="two">
          <${Field} label=${t('ui.mon.host')} hint=${t('ui.mon.host_hint')}>
            <input type="text" class="ltr" value=${f.target} onInput=${set('target')} placeholder=${splitHostPort(PLACEHOLDER[f.type])[0]} required /><//>
          <${Field} label=${t('ui.mon.port') + (DEFAULT_PORT[f.type] ? '' : ' *')} hint=${DEFAULT_PORT[f.type] ? t('ui.mon.port_default', { port: DEFAULT_PORT[f.type] }) : t('ui.mon.port_required')}>
            <input type="number" class="ltr" min="1" max="65535" value=${f.port} onInput=${set('port')} placeholder=${DEFAULT_PORT[f.type]} required=${!DEFAULT_PORT[f.type]} /><//>
        </div>`}
        ${same && same.same.length > 0 && html`<div class="banner warning"><div class="col" style=${{ gap: '6px' }}>
          <div>${t('ui.mon.same_check', { names: same.same.map(m => m.title || m.name).join(', ') })}</div>
          <label class="row nowrap"><input type="checkbox" checked=${optVal('share') === 'yes'} onChange=${(e) => setOwn('share')({ target: { value: e.target.checked ? 'yes' : '' } })} />
            <span>${t('ui.mon.share')}</span></label>
          <div class="small muted">${t('ui.mon.share_hint')}</div>
        </div></div>`}
        ${same && same.same.length === 0 && same.host > 0 && html`<div class="small muted">${t('ui.mon.same_host', { n: same.host })}</div>`}
        ${f.type === 'DNS' && html`<div class="col" style=${{ gap: '10px' }}>
          <div class="two">
            <${Field} label=${t('ui.mon.dns_query')} hint=${t('ui.mon.dns_query_hint')}>
              <input type="text" class="ltr" value=${optVal('query')} onInput=${setOwn('query')} placeholder="www.example.com" /><//>
            <${Field} label=${t('ui.mon.dns_qtype')} hint=${optVal('query') ? '' : t('ui.mon.dns_qtype_hint')}>
              <select value=${optVal('qtype') || 'A'} onChange=${setOwn('qtype')} disabled=${!optVal('query')}>${DNS_TYPES.map(x => html`<option value=${x}>${x}</option>`)}</select><//>
          </div>
          <${Field} label=${t('ui.mon.dns_expect')} hint=${t('ui.mon.dns_expect_hint')}>
            <input type="text" class="ltr" value=${optVal('expect')} onInput=${setOwn('expect')} disabled=${!optVal('query')} placeholder="203.0.113.10" /><//>
        </div>`}
        <div class="two">
          <${Field} label=${t('ui.interval')} hint=${t('ui.mon.interval_hint')}><input type="text" class="ltr" value=${f.interval} onInput=${set('interval')} placeholder="1m" /><//>
          <${Field} label=${t('ui.mon.notify')} hint=${f.mute ? t('ui.mon.mute_hint') : f.sys ? t('ui.mon.system_channel_hint') : t('ui.mon.notify_hint')}>
            <div class="col" style=${{ gap: '6px' }}>
              <label class="switch"><input type="checkbox" checked=${f.mute} onChange=${(e) => setF({ ...f, mute: e.target.checked, sys: e.target.checked ? false : f.sys })} /> ${t('ui.mon.mute')}</label>
              <label class="switch"><input type="checkbox" checked=${f.sys} onChange=${(e) => setF({ ...f, sys: e.target.checked, mute: e.target.checked ? false : f.mute })} /> ${t('ui.mon.system_channel')}</label>
            </div><//>
        </div>
        <div class="two">
          <${Field} label=${t('ui.group')} hint=${groups.length ? t('ui.mon.group_hint', { groups: groups.slice(0, 6).join(', ') + (groups.length > 6 ? ' …' : '') }) : t('ui.mon.group_hint_none')}>
            <input type="text" list="groups" value=${f.group} onInput=${set('group')} placeholder=${groups[0] || 'public'} autocomplete="off" />
            <datalist id="groups">${groups.map(g => html`<option value=${g} />`)}</datalist><//>
          <${Field} label=${t('ui.tags')} hint=${t('ui.mon.tags_hint')}><input type="text" class="ltr" value=${f.tags} onInput=${set('tags')} placeholder="env:prod,db" /><//>
        </div>
        <div class="two">
          ${teams.data && teams.data.items.length > 1 && html`<${Field} label=${t('ui.team.field')} hint=${t('ui.team.field_hint')}>
            <select value=${f.team} onChange=${set('team')} required>
              ${!f.team && html`<option value="">${t('ui.team.choose')}</option>`}
              ${(writable.some(tm => tm.name === f.team) || !f.team ? writable : [...writable, { name: f.team, title: f.team }]).map(tm => html`<option value=${tm.name}>${teamTitle(tm)}</option>`)}
            </select><//>`}
          ${regions.data && regions.data.items.length > 1 && html`<${Field} label=${t('ui.region.field')} hint=${t('ui.region.field_hint')}>
            <select value=${f.region || 'global'} onChange=${(e) => setF({ ...f, region: e.target.value === 'global' ? '' : e.target.value })}>
              ${regions.data.items.map(rg => html`<option value=${rg.name}>${regionTitle(rg.name)}</option>`)}
            </select><//>`}
        </div>
        <${Field} label=${t('ui.description')}><textarea rows="2" value=${f.description} onInput=${set('description')}></textarea><//>
        <${Field} label=${t('ui.mon.options')} hint=${t('ui.mon.options_hint')}>
          <datalist id="optkeys">${suggestions.map(k => html`<option value=${k} />`)}</datalist>
          <div class="col">
            ${f.opts.map((o, i) => own.includes(o.k) ? null : html`<div class="row nowrap">
              <input type="text" class="ltr" list="optkeys" value=${o.k} onInput=${(e) => setOpt(i, 'k', e.target.value)} placeholder=${t('ui.key')} style=${{ maxWidth: '220px' }} />
              <input type="text" class="ltr" value=${o.v} onInput=${(e) => setOpt(i, 'v', e.target.value)} placeholder=${t('ui.value')} />
              <button type="button" class="btn sm ghost" onClick=${() => setF({ ...f, opts: f.opts.filter((_, j) => j !== i) })} aria-label=${t('ui.remove')}>✕</button>
            </div>`)}
            <div><button type="button" class="btn sm" onClick=${() => setF({ ...f, opts: [...f.opts, { k: '', v: '' }] })}>+ ${t('ui.mon.add_option')}</button></div>
          </div>
        <//>
        ${editing && html`<${Field} label=${t('ui.config.message')}><input type="text" value=${f.message} onInput=${set('message')} placeholder=${t('ui.config.message_ph')} /><//>`}
        ${err && html`<div class="banner error"><div>${errText(err)}${diags && html`<ul class="small">${diags.map(d => html`<li class="ltr">${d.pos.line}:${d.pos.col} ${d.message}</li>`)}</ul>`}</div></div>`}
        ${probe && (probe.error ? html`<div class="banner error">${errText(probe.error)}</div>` : html`<${ProbeResult} r=${probe} />`)}
        <div class="row">
          ${f.type !== 'PUSH' && html`<button type="button" class="btn" onClick=${test} disabled=${probing || !f.target}>${probing ? html`<span class="spin"></span>` : ''} ${t('ui.mon.test_now')}</button>`}
          <div class="spacer"></div>
          <a class="btn" href=${editing ? '#/monitors/' + enc(name) : '#/monitors'}>${t('ui.cancel')}</a>
          <button class="btn primary" disabled=${busy}>${t('ui.save')}</button>
        </div>
      </div>
    </form>
  </div>`;
}

// QuickAdd adds monitors from lines of text. Their team and region are
// filled in: from the list's filter, else the user's only team, else the
// admin team and global.
export function QuickAdd({ onClose, onDone, team = '', region = '' }) {
  const teams = useFetch(() => get('/teams'), []);
  const regions = useFetch(() => get('/regions'), []);
  const writable = writableTeams(teams.data);
  const [where, setWhere] = useState({ team, region });
  useEffect(() => {
    if (writable.length) setWhere((cur) => (cur.team ? cur : { ...cur, team: me.teams ? (writable.length === 1 ? writable[0].name : '') : 'admin' }));
  }, [teams.data]);
  const [text, setText] = useState('');
  const [parsed, setParsed] = useState(null);
  const [pick, setPick] = useState(new Set());
  const [busy, setBusy] = useState(false);
  const parse = async () => {
    try {
      const r = await post('/quickadd/parse', { text });
      setParsed(r);
      setPick(new Set(r.records.map((_, i) => i)));
    } catch (e) { toastError(e); }
  };
  const add = async () => {
    setBusy(true);
    let n = 0;
    for (const [i, r] of parsed.records.entries()) {
      if (!pick.has(i)) continue;
      try {
        const opts = { ...r.opts };
        if (where.team) opts.team = where.team;
        if (where.region && where.region !== 'global') opts.region = where.region;
        await post('/monitors', { record: { name: r.name, type: r.type, target: r.target, opts }, message: 'quick add' });
        n++;
      } catch (e) { toast(r.name + ': ' + errText(e), 'error'); }
    }
    setBusy(false);
    toast(t('ui.quick.added', { n }));
    onDone();
  };
  return html`<${Modal} wide title=${t('ui.mon.quick_add')} onClose=${onClose} footer=${parsed ? html`
      <button class="btn" onClick=${() => setParsed(null)}>${t('ui.back')}</button>
      <button class="btn primary" disabled=${busy || !pick.size || (teams.data && teams.data.items.length > 1 && !where.team)} onClick=${add}>${t('ui.quick.add_n', { n: pick.size })}</button>` : html`
      <button class="btn" onClick=${onClose}>${t('ui.cancel')}</button>
      <button class="btn primary" disabled=${!text.trim()} onClick=${parse}>${t('ui.quick.preview')}</button>`}>
    ${!parsed ? html`<div class="form">
      <p class="muted small">${t('ui.quick.hint')}</p>
      <textarea class="ltr mono" rows="10" value=${text} onInput=${(e) => setText(e.target.value)} placeholder=${'https://example.com\n10.0.0.5:5432\nudp 10.0.0.1:ntp\nping 192.168.1.1'}></textarea>
    </div>` : html`<div class="col">
      <div class="two">
        ${teams.data && teams.data.items.length > 1 && html`<${Field} label=${t('ui.team.field')}>
          <select value=${where.team} onChange=${(e) => setWhere({ ...where, team: e.target.value })}>
            ${!where.team && html`<option value="">${t('ui.team.choose')}</option>`}
            ${writable.map(tm => html`<option value=${tm.name}>${teamTitle(tm)}</option>`)}</select><//>`}
        ${regions.data && regions.data.items.length > 1 && html`<${Field} label=${t('ui.region.field')}>
          <select value=${where.region || 'global'} onChange=${(e) => setWhere({ ...where, region: e.target.value })}>
            ${regions.data.items.map(rg => html`<option value=${rg.name}>${regionTitle(rg.name)}</option>`)}</select><//>`}
      </div>
      ${parsed.errors.map(e => html`<div class="banner error small">${t('ui.quick.line', { line: e.line })}: ${e.msg}</div>`)}
      <table class="tbl"><tbody>${parsed.records.map((r, i) => html`<tr>
        <td style=${{ width: '28px' }}><input type="checkbox" checked=${pick.has(i)} onChange=${() => { const s = new Set(pick); s.has(i) ? s.delete(i) : s.add(i); setPick(s); }} /></td>
        <td><code class="ltr">${r.text}</code>${r.warnings.map(w => html`<div class="small warn-text">${w}</div>`)}</td>
      </tr>`)}</tbody></table>
    </div>`}
  <//>`;
}
