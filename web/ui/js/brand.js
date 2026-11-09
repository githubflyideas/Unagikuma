// The active theme pack: its colours, and on a registered installation its
// product name and logo (white label). Loaded once before the first page
// is drawn, from a public endpoint so the login page looks right too.
let B = {};
let applied = [];

export const brand = () => B;
export const product = () => B.product || 'Unagikuma';
// Changes with the pack, so the browser fetches the new logo.
export const logoURL = () => '/logo.png' + (B.logo ? '?v=' + encodeURIComponent(B.id) : '');

export async function loadBrand() {
  try {
    const r = await fetch('/api/v1/brand', { credentials: 'same-origin' });
    if (r.ok) B = await r.json();
  } catch { /* the built-in look */ }
  const icon = document.querySelector('link[rel="icon"]');
  if (icon && B.favicon) icon.href = '/favicon.png?v=' + encodeURIComponent(B.id);
  return B;
}

// brandColors puts the pack's colours on the page (on) or takes them off.
// The names were checked by the server against the console's tokens.
export function brandColors(on) {
  const st = document.documentElement.style;
  for (const k of applied) st.removeProperty('--' + k);
  applied = [];
  if (!on || !B.colors) return;
  for (const [k, v] of Object.entries(B.colors)) {
    if (/^[a-z0-9-]+$/.test(k) && /^#[0-9a-f]{6}$/i.test(v)) {
      st.setProperty('--' + k, v);
      applied.push(k);
    }
  }
}
