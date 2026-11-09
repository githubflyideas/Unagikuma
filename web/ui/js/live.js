// Live updates over server-sent events. The server replays missed events
// on reconnect or sends "reset" when it cannot; listeners then refetch.
const listeners = new Map();
const stateListeners = new Set();
let es = null;
let connected = false;

function emit(type, data) {
  for (const f of listeners.get(type) || []) {
    try { f(data); } catch (e) { console.error(e); }
  }
  for (const f of listeners.get('*') || []) {
    try { f(type, data); } catch (e) { console.error(e); }
  }
}

function setConnected(v) {
  connected = v;
  for (const f of stateListeners) f(v);
}

export function startLive() {
  if (es) return;
  es = new EventSource('/api/v1/stream');
  es.onopen = () => setConnected(true);
  es.onerror = () => setConnected(false);
  for (const type of ['state', 'incident', 'event', 'heartbeat', 'reset', 'serial', 'banner']) {
    es.addEventListener(type, (ev) => {
      let data = null;
      try { data = JSON.parse(ev.data); } catch { /* ignore */ }
      emit(type, data);
    });
  }
}

export function stopLive() {
  if (es) es.close();
  es = null;
  setConnected(false);
}

export function onLive(type, f) {
  if (!listeners.has(type)) listeners.set(type, new Set());
  listeners.get(type).add(f);
  return () => listeners.get(type).delete(f);
}

export function onConnection(f) {
  stateListeners.add(f);
  f(connected);
  return () => stateListeners.delete(f);
}
