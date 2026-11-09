// Command palette (Ctrl+K): type an address to add it, "host:?" for the
// UDP keywords, "ack NAME", "silence NAME 1h" or "check NAME", or part of a
// name to open a monitor.
import { html, useState, useEffect, useLayoutEffect, useRef } from '../lib.js';
import { t, currentLang } from '../i18n.js';
import { get, post } from '../api.js';
import { Pill, openMonitor, enc, toast, toastError } from '../ui.js';

export function openPalette() { dispatchEvent(new CustomEvent('unagikuma:palette')); }

const DUR = { m: 60, h: 3600, d: 86400 };

function parseDur(s) {
  const m = /^(\d+)([mhd])$/.exec(s || '');
  return m ? +m[1] * DUR[m[2]] : 0;
}

// keywordQuery matches "host:" or "host:prefix" or "host:?" (not URLs or
// numeric ports).
function keywordQuery(q) {
  if (/^[a-z]+:\/\//i.test(q)) return null;
  const m = /^([^\s:]+):([a-z0-9-]*)(\?)?$/i.exec(q);
  if (!m || /^\d+$/.test(m[2])) return null;
  return { host: m[1], prefix: m[2].toLowerCase() };
}

// looksLikeTarget guesses that the text is something to monitor rather
// than a search.
function looksLikeTarget(q) {
  return /^(https?:\/\/|(tcp|udp|ping|dns|ntp|snmp|syslog|http|https)\s)/i.test(q) || /[.:]/.test(q.split(/\s/)[0]);
}

function Palette({ onClose }) {
  const [q, setQ] = useState('');
  const [idx, setIdx] = useState(0);
  const [rows, setRows] = useState([]);
  const [kws, setKws] = useState(null);
  const input = useRef(null);
  // Focus before the next keystroke arrives, so nothing typed is lost.
  useLayoutEffect(() => { if (input.current) input.current.focus(); }, []);
  useEffect(() => { get('/monitors').then(d => setRows(d.items), () => {}); }, []);
  const text = q.trim();
  const kq = keywordQuery(text);
  useEffect(() => { if (kq && !kws) get('/keywords').then(setKws, () => setKws([])); }, [!!kq]);
  useEffect(() => setIdx(0), [q]);

  const find = (name) => rows.find(r => r.name === name) || rows.find(r => r.name.toLowerCase() === (name || '').toLowerCase());
  const incidentOf = (name) => { const r = find(name); return r && r.incident; };
  const run = (f) => async () => {
    try { await f(); onClose(); } catch (e) { toastError(e); }
  };
  const add = async () => {
    const p = await post('/quickadd/parse', { text });
    if (p.errors.length) throw new Error(p.errors[0].msg);
    let n = 0;
    for (const r of p.records) {
      await post('/monitors', { record: { name: r.name, type: r.type, target: r.target, opts: r.opts }, message: 'quick add' });
      n++;
    }
    toast(t('ui.pal.added', { n, what: p.records.map(r => r.text).join(' · ') }));
    if (p.records.length === 1) openMonitor(p.records[0].name);
  };

  // Build the list of choices for the current text.
  let items = [];
  const [cmd, arg, extra] = text.split(/\s+/);
  if (kq) {
    const list = (kws || []).filter(k => !kq.prefix || k.keyword.startsWith(kq.prefix) || String(k.port).startsWith(kq.prefix) || (k.aliases || []).some(a => a.startsWith(kq.prefix)));
    items = list.slice(0, 40).map(k => ({
      key: 'kw' + k.keyword,
      kw: k,
      act: () => { setQ(kq.host + ':' + k.keyword); input.current && input.current.focus(); },
    }));
  } else if (/^(ack|silence|check|unack)$/i.test(cmd || '') && arg) {
    const c = cmd.toLowerCase();
    const r = find(arg);
    if (r) {
      const inc = incidentOf(arg);
      if (c === 'check') items.push({ key: 'check', icon: '↻', label: t('ui.pal.check', { name: r.title }), act: run(async () => { await post(`/monitors/${enc(r.name)}/check`); toast(t('ui.mon.check_queued')); }) });
      else if (!inc) items.push({ key: 'none', icon: '·', label: t('ui.pal.no_incident', { name: r.title }) });
      else if (c === 'ack') items.push({ key: 'ack', icon: '✓', label: t('ui.pal.ack', { name: r.title }), act: run(async () => { await post(`/incidents/${inc.id}/ack`, {}); toast(t('ui.pal.acked', { name: r.title })); }) });
      else if (c === 'silence') {
        const s = parseDur(extra || '1h') || 3600;
        items.push({ key: 'sil', icon: '⏸', label: t('ui.pal.silence', { name: r.title, dur: extra || '1h' }), act: run(async () => { await post(`/incidents/${inc.id}/silence`, { duration_s: s }); toast(t('ui.inc.silenced')); }) });
      }
    } else {
      items = rows.filter(x => x.name.toLowerCase().startsWith(arg.toLowerCase())).slice(0, 8)
        .map(x => ({ key: 'c' + x.name, icon: '→', label: `${c} ${x.name}`, act: () => setQ(`${c} ${x.name}${extra ? ' ' + extra : ''}`) }));
    }
  } else if (text) {
    const lower = text.toLowerCase();
    const hits = rows.filter(r => (r.name + ' ' + r.title + ' ' + r.target).toLowerCase().includes(lower)).slice(0, 8);
    if (looksLikeTarget(text)) items.push({ key: 'add', icon: '+', label: t('ui.pal.add', { what: text }), hint: t('ui.pal.add_hint'), act: run(add) });
    for (const r of hits) items.push({ key: 'm' + r.name, row: r, act: () => { onClose(); openMonitor(r.name); } });
    items.push({ key: 'search', icon: '⌕', label: t('ui.pal.search', { what: text }), act: () => { onClose(); location.hash = '#/monitors?q=' + enc(text); } });
  } else {
    items = [
      { key: 'h1', icon: '+', label: t('ui.pal.help_add'), act: () => setQ('') },
      { key: 'h2', icon: '?', label: t('ui.pal.help_kw') },
      { key: 'h3', icon: '✓', label: t('ui.pal.help_ack') },
      { key: 'h4', icon: '⏸', label: t('ui.pal.help_silence') },
    ];
  }

  const key = (e) => {
    if (e.key === 'ArrowDown') { setIdx(Math.min(items.length - 1, idx + 1)); e.preventDefault(); }
    else if (e.key === 'ArrowUp') { setIdx(Math.max(0, idx - 1)); e.preventDefault(); }
    else if (e.key === 'Enter') { const it = items[idx]; if (it && it.act) it.act(); e.preventDefault(); }
    else if (e.key === 'Escape') { onClose(); }
  };
  const zh = currentLang() === 'zh';
  return html`<div class="scrim" onClick=${onClose}></div>
    <div class="pal" role="dialog" aria-modal="true" aria-label=${t('ui.top.search')}>
      <input ref=${input} class="ltr" value=${q} onInput=${(e) => setQ(e.target.value)} onKeyDown=${key}
        placeholder=${t('ui.pal.placeholder')} autocomplete="off" spellcheck="false" />
      <div class="res">
        ${kq && html`<div class="small muted pal-note">${t('ui.pal.kw_head', { host: kq.host })}</div>`}
        ${items.map((it, i) => it.kw ? html`<div class=${'it' + (i === idx ? ' on' : '')} onMouseEnter=${() => setIdx(i)} onClick=${it.act}>
            <b>${it.kw.keyword}</b><span>${it.kw.port}</span><span class=${it.kw.effective_depth === 'L3' ? 'ok-text' : 'warn-text'}>${it.kw.effective_depth}</span>
            <span class="muted">${zh ? it.kw.service_zh || it.kw.service : it.kw.service}${it.kw.effective_depth === 'L1' ? ' · ' + t('ui.pal.l1') : ''}</span></div>`
          : it.row ? html`<div class=${'act' + (i === idx ? ' on' : '')} onMouseEnter=${() => setIdx(i)} onClick=${it.act}>
            <span class="ico">→</span><span class="ellipsis"><b>${it.row.title}</b> <span class="mono muted ltr">${it.row.target}</span></span><${Pill} status=${it.row.status} /></div>`
          : html`<div class=${'act' + (i === idx ? ' on' : '') + (it.act ? '' : ' static')} onMouseEnter=${() => setIdx(i)} onClick=${it.act}>
            <span class="ico">${it.icon}</span><span>${it.label}</span><span class="muted small">${it.hint || ''}</span></div>`)}
      </div>
      <div class="hint"><span><kbd>?</kbd> ${t('ui.pal.k_kw')}</span><span><kbd>↑↓</kbd> ${t('ui.pal.k_move')}</span><span><kbd>Enter</kbd> ${t('ui.pal.k_run')}</span><span><kbd>Esc</kbd> ${t('ui.close')}</span></div>
    </div>`;
}

// PaletteHost opens the palette on Ctrl+K or openPalette().
export function PaletteHost() {
  const [on, setOn] = useState(false);
  useEffect(() => {
    const open = () => setOn(true);
    const key = (e) => { if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setOn(true); } };
    addEventListener('unagikuma:palette', open);
    addEventListener('keydown', key);
    return () => { removeEventListener('unagikuma:palette', open); removeEventListener('keydown', key); };
  }, []);
  return on ? html`<${Palette} onClose=${() => setOn(false)} />` : null;
}

