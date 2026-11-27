// localStorage keys are scoped by installation so two consoles on the
// same host (different ports) keep their own language, style and view.
// A leftover unscoped key from before 3.0.1 is read once as a fallback.

function installPrefix() {
  const id = (document.documentElement.dataset.install || '').replace(/[^0-9A-Za-z_-]/g, '');
  return id;
}

export function storageKey(k) {
  const id = installPrefix();
  if (!id || !k.startsWith('unagikuma.')) return k;
  return 'unagikuma.' + id + k.slice('unagikuma'.length);
}

export function storageGet(k) {
  try {
    const sk = storageKey(k);
    let v = localStorage.getItem(sk);
    if (v == null && sk !== k) v = localStorage.getItem(k);
    return v;
  } catch { return null; }
}

export function storageSet(k, v) {
  try { localStorage.setItem(storageKey(k), v); } catch { /* unavailable */ }
}
