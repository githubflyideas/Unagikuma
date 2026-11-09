// REST client. Writes carry the session's CSRF token.
let csrf = '';
let onAuthLost = null;

export function setCSRF(v) { csrf = v || ''; }

// The signed-in account: teams is set for a member of one or more teams,
// who works within them (the menu and pages show only what they own).
export const me = { teams: null };
export function setMe(s) {
  me.teams = (s && s.teams) || null;
  me.role = (s && s.role) || '';
  me.version = (s && s.version) || '';
  me.registered = !!(s && s.registered);
  me.cluster = !!(s && s.cluster);
}
export function getCSRF() { return csrf; }
export function onUnauthorized(f) { onAuthLost = f; }

export class ApiError extends Error {
  constructor(status, body) {
    super((body && body.message) || 'HTTP ' + status);
    this.status = status;
    this.code = body && body.code;
    this.body = body || {};
  }
}

export async function api(method, path, body, headers) {
  const opt = { method, headers: { ...(headers || {}) }, credentials: 'same-origin' };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  if (method !== 'GET' && csrf) opt.headers['X-Unagikuma-CSRF'] = csrf;
  let r;
  try {
    r = await fetch('/api/v1' + path, opt);
  } catch (e) {
    throw new ApiError(0, { code: 'network', message: e.message });
  }
  if (r.status === 204) return null;
  const ct = r.headers.get('content-type') || '';
  const data = ct.includes('json') ? await r.json() : await r.text();
  if (!r.ok) {
    const err = new ApiError(r.status, typeof data === 'object' ? data : { message: String(data).slice(0, 200) });
    if (r.status === 401 && onAuthLost && !path.startsWith('/session') && !path.startsWith('/setup')) onAuthLost(err);
    throw err;
  }
  return data;
}

export const get = (p) => api('GET', p);
export const post = (p, b, h) => api('POST', p, b === undefined ? {} : b, h);
export const put = (p, b, h) => api('PUT', p, b, h);
export const del = (p, h) => api('DELETE', p, undefined, h);

// ifMatch sends a record's etag back so a save fails instead of
// overwriting what someone else changed in the meantime.
export const ifMatch = (etag) => (etag ? { 'If-Match': etag } : undefined);

export function qs(params) {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== '') u.set(k, v);
  const s = u.toString();
  return s ? '?' + s : '';
}

// newKey is an Idempotency-Key for one create: retrying it (double click,
// lost answer) cannot add the record twice. getRandomValues also works
// over plain HTTP, unlike randomUUID.
export function newKey() {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}
