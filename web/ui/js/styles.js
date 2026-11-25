// Styles and branding: the console's layout (classic, office, ...), the
// installation's default for it, each account's own choice, and the
// product name to show.
import { get, put } from './api.js';
import { setProduct } from './ui.js';

const CACHE = 'unagikuma.style';
export const brand = { name: 'Unagikuma', style: 'classic', styles: [{ id: 'classic', allowed: true }] };
let mine = ''; // the account's choice; '' follows the default
let loaded = () => {};

function storage(v) {
  try {
    if (v === undefined) return localStorage.getItem(CACHE) || '';
    localStorage.setItem(CACHE, v);
  } catch { /* unavailable */ }
  return '';
}

// onStyleLoaded runs f once a style sheet has loaded (the layout's
// breakpoints may need adjusting to it).
export function onStyleLoaded(f) { loaded = f; }

// applyStyle shows the page in a style; the last one used is remembered so
// the next visit opens in it without a flash of the other.
export function applyStyle(id) {
  const root = document.documentElement;
  const known = brand.styles.find(s => s.id === id && s.allowed);
  if (!known) id = 'classic';
  storage(id);
  let link = document.getElementById('ui-style');
  if (id === 'classic') {
    delete root.dataset.style;
    if (link) link.remove();
    return;
  }
  const href = 'styles/' + id + '.css';
  if (!link) {
    link = document.createElement('link');
    link.id = 'ui-style';
    link.rel = 'stylesheet';
    document.head.appendChild(link);
  }
  if (link.getAttribute('href') !== href) {
    link.onload = () => loaded();
    link.setAttribute('href', href);
  }
  root.dataset.style = id;
}

// The style last used in this browser, until the server says otherwise.
export function applyCachedStyle() {
  const v = storage();
  if (v && v !== 'classic') {
    brand.styles = [...brand.styles, { id: v, allowed: true }];
    applyStyle(v);
  }
}

// loadBrand reads the name and the default style; no account is needed.
export async function loadBrand() {
  try {
    const b = await get('/brand');
    Object.assign(brand, b);
    setProduct(b.name);
  } catch { /* keep the defaults */ }
  applyStyle(currentStyle());
}

// loadMyStyle reads the signed-in account's own choice.
export async function loadMyStyle() {
  try {
    const p = await get('/preferences');
    mine = (p && typeof p.style === 'string') ? p.style : '';
  } catch { mine = ''; }
  applyStyle(currentStyle());
}

export function myStyle() { return mine; }

export function currentStyle() {
  const ok = (id) => id && brand.styles.some(s => s.id === id && s.allowed);
  return ok(mine) ? mine : ok(brand.style) ? brand.style : 'classic';
}

// setMyStyle keeps the account's choice ('' to follow the default).
export async function setMyStyle(id) {
  const p = await get('/preferences').catch(() => ({}));
  const next = { ...(p || {}) };
  delete next.user;
  if (id) next.style = id; else delete next.style;
  await put('/preferences', next);
  mine = id;
  applyStyle(currentStyle());
}

// setDefaultStyle changes the installation's default (administrators).
export function defaultChanged(id) {
  brand.style = id;
  applyStyle(currentStyle());
}

export function signedOut() {
  mine = '';
  applyStyle(currentStyle());
}
