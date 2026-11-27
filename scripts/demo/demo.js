// The static demo: the console served without a server. Every GET the
// console makes is answered from data.json, recorded from a real
// three-site cluster; times in it move with the clock, so "2 min ago"
// stays true. Nothing is saved: a change is answered with a notice.
// Loaded before the console (see build.sh); not part of the product.
(() => {
  const realFetch = window.fetch.bind(window);
  let data = null, shift = 0;
  const ready = realFetch('demo/data.json').then(r => r.json()).then(d => {
    data = d.responses;
    shift = Date.now() - Date.parse(d.recorded_at);
  });
  const iso = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)$/;
  const moved = (v) => {
    if (typeof v === 'string' && iso.test(v) && !v.startsWith('0001')) return new Date(Date.parse(v) + shift).toISOString();
    if (Array.isArray(v)) return v.map(moved);
    if (v && typeof v === 'object') { const o = {}; for (const k in v) o[k] = moved(v[k]); return o; }
    return v;
  };
  const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const keyOf = (u) => {
    const url = new URL(u, location.href);
    const p = url.pathname.slice(url.pathname.indexOf('api/v1') + 6);
    const q = [...url.searchParams].filter(([k]) => k !== '_' && k !== 't').sort().map(([k, v]) => k + '=' + v).join('&');
    return [p + (q ? '?' + q : ''), p];
  };
  const NOT_SAVED = { code: 'demo', message: document.documentElement.lang.startsWith('zh') ? '这是演示站，修改不会保存。下载安装后即可使用全部功能。' : 'This is the demo: changes are not saved. Download Unagikuma to use it for real.' };
  window.fetch = async (input, init) => {
    const u = typeof input === 'string' ? input : input.url;
    if (!u.includes('api/v1/')) return realFetch(input, init);
    await ready;
    const method = ((init && init.method) || 'GET').toUpperCase();
    const [full, path] = keyOf(u);
    if (method === 'GET') {
      const hit = data[full] !== undefined ? data[full] : data[path];
      return hit !== undefined ? json(200, moved(hit)) : json(404, { code: 'not_found', message: 'not in the demo' });
    }
    // Look and language may be tried: they change in this tab only.
    if ((path === '/branding' || path === '/preferences') && init && init.body) {
      try {
        const b = JSON.parse(init.body);
        data[path] = { ...data[path], ...b };
        if (path === '/branding' && b.style) data['/brand'] = { ...data['/brand'], style: b.style };
        return json(200, data[path]);
      } catch { /* fall through */ }
    }
    if (path === '/session' || path === '/setup') return json(200, moved(data['/session']));
    if (path === '/quickadd/parse' || path.endsWith('/preview') || path === '/channels/request_preview') return json(200, data['POST ' + path] || { records: [], errors: [] });
    return json(403, NOT_SAVED);
  };
  // No live stream: the page refreshes from the recording.
  window.EventSource = class { constructor() { this.readyState = 1; } addEventListener() {} removeEventListener() {} close() {} };
  // A banner across the top.
  addEventListener('DOMContentLoaded', () => {
    const zh = (navigator.language || '').startsWith('zh');
    const b = document.createElement('div');
    b.className = 'demo-ribbon';
    b.innerHTML = zh
      ? '这是 Unagikuma 的在线演示（只读，数据为示例）。 <a href="https://github.com/githubflyideas/Unagikuma/releases/latest">下载安装</a> · <a href="https://github.com/githubflyideas/Unagikuma">GitHub</a>'
      : 'This is the Unagikuma demo (read-only, sample data). <a href="https://github.com/githubflyideas/Unagikuma/releases/latest">Download</a> · <a href="https://github.com/githubflyideas/Unagikuma">GitHub</a>';
    document.body.prepend(b);
  });
})();
