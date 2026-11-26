// Records what the console asks a running installation for, for the static
// demo: node record.js http://127.0.0.1:18981 user password data.json
// Visits every page and tab, and every monitor's page, keeping each GET
// answer of the API by its path and query.
const { chromium } = require('playwright');
const fs = require('fs');
const [base, user, pass, out] = process.argv.slice(2);
(async () => {
  const b = await chromium.launch();
  const p = await (await b.newContext({ viewport: { width: 1440, height: 900 } })).newPage();
  const responses = {};
  p.on('response', async (r) => {
    const u = new URL(r.url());
    if (!u.pathname.startsWith('/api/v1/') || r.request().method() !== 'GET' || !r.ok() || u.pathname === '/api/v1/stream') return;
    const key = u.pathname.slice(7) + ([...u.searchParams].length ? '?' + [...u.searchParams].filter(([k]) => k !== '_' && k !== 't').sort().map(([k, v]) => k + '=' + v).join('&') : '');
    try { responses[key.replace(/\?$/, '')] = await r.json(); } catch { /* not JSON */ }
  });
  const s = await p.request.post(base + '/api/v1/session', { data: { username: user, password: pass } });
  responses['/session'] = await s.json();
  const seen = new Set();
  const visit = async (hash) => {
    if (seen.has(hash)) return; seen.add(hash);
    await p.goto(base + '/' + hash); await p.waitForTimeout(1800);
  };
  for (const r of ['#/', '#/monitors', '#/problems', '#/events', '#/alerts', '#/notices', '#/config', '#/settings', '#/advanced', '#/nodes']) {
    await visit(r);
    const tabs = await p.$$eval('.tabs a', as => as.map(a => a.getAttribute('href')).filter(h => h && h.startsWith('#/')));
    for (const t of tabs) await visit(t);
  }
  const mons = (await (await p.request.get(base + '/api/v1/monitors?limit=500')).json()).items.map(m => m.name);
  for (const m of mons) await visit('#/monitors/' + encodeURIComponent(m));
  for (const v of ['#/monitors?view=hive', '#/monitors?status=critical', '#/wall', '#/summary']) await visit(v);
  fs.writeFileSync(out, JSON.stringify({ recorded_at: new Date().toISOString(), responses }));
  console.log(Object.keys(responses).length, 'answers from', seen.size, 'pages');
  await b.close();
})();
