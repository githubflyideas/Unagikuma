// Browser tests of the web UI against a running server.
// Usage: node e2e/ui.mjs http://127.0.0.1:PORT OUTDIR NAGIOS_CFG
// Run through `go test -tags e2e ./e2e/`, which starts the server.
import { createRequire } from 'module';
const require = createRequire(import.meta.url);
const { chromium } = require('playwright');

const [base, out, nagiosCfg] = process.argv.slice(2);
// UNAGIKUMA_E2E_CHROME names a Chrome already installed (as on CI
// runners), so the tests need no browser download.
const exe = process.env.UNAGIKUMA_E2E_CHROME;
const browser = await chromium.launch(exe ? { executablePath: exe } : {});
const errors = [];
let failed = 0;
let ctx = await browser.newContext({ viewport: { width: 1366, height: 900 }, locale: 'en-US' });
let page = await ctx.newPage();
const watch = (p) => p.on('pageerror', (e) => errors.push(e.message));
watch(page);

async function step(name, fn) {
  try {
    await fn();
    console.log('ok   ' + name);
  } catch (e) {
    failed++;
    console.log('FAIL ' + name + ': ' + e.message.split('\n')[0]);
    await page.screenshot({ path: `${out}/${name.replace(/\W+/g, '-')}.png`, fullPage: true }).catch(() => {});
  }
}
const api = (path) => page.evaluate(async (p) => (await fetch('/api/v1' + p)).json(), path);
async function until(what, cond, ms = 15000) {
  const end = Date.now() + ms;
  while (!(await cond())) {
    if (Date.now() > end) throw new Error('timed out: ' + what);
    await page.waitForTimeout(250);
  }
}

await step('first run creates the admin', async () => {
  await page.goto(base);
  await page.fill('input[autocomplete=username]', 'admin');
  await page.locator('input[autocomplete=new-password]').nth(0).fill('ui-test-pass-1');
  await page.locator('input[autocomplete=new-password]').nth(1).fill('ui-test-pass-1');
  await page.click('.auth .btn.primary');
  await page.waitForSelector('nav.nav');
});

await step('overview shows the first-run guide', async () => {
  await page.waitForSelector('.ob');
});

await step('command palette adds a monitor', async () => {
  await page.keyboard.press('Control+k');
  await page.waitForSelector('.pal input');
  await page.keyboard.type('127.0.0.1:1');
  await page.waitForTimeout(300);
  await page.keyboard.press('Enter');
  await page.waitForSelector('.drawer', { timeout: 10000 });
  await page.keyboard.press('Escape');
  await until('the new monitor to fail', async () => {
    const r = await api('/monitors');
    return r.items.some((m) => m.target === '127.0.0.1:1' && m.status === 'critical');
  });
});

await step('pages render with delivery statistics', async () => {
  await until('a notification to be delivered', async () => (await api('/overview')).channels.some((c) => c.sent_24h > 0));
  for (const [route, sel] of [['#/', '.fp-scroll'], ['#/monitors', '.mon-list, .tbl, .hive'], ['#/problems', '.page']]) {
    await page.goto(base + '/' + route);
    await page.waitForSelector(sel, { timeout: 8000 });
  }
  if (errors.length) throw new Error(errors.join(' | '));
});

await step('monitor list and detail open', async () => {
  await page.goto(base + '/#/monitors');
  const name = (await api('/monitors')).items.find((m) => m.target === '127.0.0.1:1').name;
  // Clicking a row opens the monitor's page.
  await page.click(`tr:has(a.name[href="#/monitors/${encodeURIComponent(name)}"]) td:nth-child(3)`);
  await page.waitForSelector('.mon-head');
});

await step('ticked monitors are deleted together', async () => {
  await page.goto(base + '/#/monitors');
  await page.click('button:has-text("Quick add monitors")');
  await page.fill('.modal textarea', '127.0.0.1:2\n127.0.0.1:3');
  await page.click('.modal button:has-text("Preview")');
  await page.click('.modal .btn.primary:has-text("Add")');
  await until('two new monitors', async () => (await api('/monitors')).items.filter((m) => /^127\.0\.0\.1:[23]$/.test(m.target)).length === 2);
  const names = (await api('/monitors')).items.filter((m) => /^127\.0\.0\.1:[23]$/.test(m.target)).map((m) => m.name);
  await page.goto(base + '/#/monitors?q=' + encodeURIComponent('127.0.0.1:'));
  for (const n of names) await page.check(`tr:has(a.name[href="#/monitors/${encodeURIComponent(n)}"]) td.sel input`);
  await until('the bulk menu counts both', async () => /2/.test(await page.textContent('select.bulk option[value=""]')));
  await page.selectOption('select.bulk', 'delete');
  await page.click('.modal .btn.danger');
  await until('both monitors deleted', async () => !(await api('/monitors')).items.some((m) => names.includes(m.name)));
  if (!(await api('/monitors')).items.some((m) => m.target === '127.0.0.1:1')) throw new Error('an unticked monitor was deleted');
});

await step('a second monitor of the same check offers to share it', async () => {
  await page.goto(base + '/#/monitors/new');
  await page.selectOption('form select', 'TCP');
  await page.fill('form input[pattern]', 'same-check-b');
  await page.fill('form input[placeholder="db.example.lan"]', '127.0.0.1');
  await page.fill('form input[type=number]', '1');
  await page.waitForSelector('.banner.warning:has-text("already check exactly this")', { timeout: 8000 });
  await page.check('.banner.warning input[type=checkbox]');
  await page.click('form button.primary');
  await until('the sharing monitor to be saved', async () => (await api('/monitors')).items.some((m) => m.name === 'same-check-b'));
  const text = (await api('/config')).text || '';
  if (!/same-check-b\s.*share=yes/.test(text)) throw new Error('share=yes not written: ' + text.split('\n').filter((l) => l.includes('same-check-b')).join(' '));
  if (errors.length) throw new Error(errors.join(' | '));
});

await step('a team is added and owns a monitor', async () => {
  await page.goto(base + '/#/settings/teams');
  await page.click('button:has-text("Add team")');
  await page.fill('.modal input[pattern]', 'webteam');
  await page.locator('.modal label.switch').filter({ hasText: /^\s*admin\s*$/ }).locator('input').check();
  await page.click('.modal .btn.primary:has-text("Save")');
  await page.waitForSelector('td:has-text("webteam")');
  await page.goto(base + '/#/monitors/same-check-b/edit');
  await page.waitForSelector('form select >> nth=1');
  const sel = page.locator('.field:has(label:text-is("Team")) select');
  await sel.selectOption('webteam');
  await page.click('form button.primary');
  await until('the monitor to join the team', async () => (await api('/monitors')).items.some((m) => m.name === 'same-check-b' && m.team === 'webteam'));
  await page.goto(base + '/#/monitors?q=' + encodeURIComponent('team:webteam'));
  await page.waitForSelector('.tag:has-text("webteam")');
  if (errors.length) throw new Error(errors.join(' | '));
});

await step('a team is notified through its channels, and a monitor can be muted', async () => {
  await page.goto(base + '/#/alerts');
  await page.waitForSelector('.tabs');
  if (await page.locator('a[href="#/alerts/policies"]').count()) throw new Error('the policies tab is still there');
  await page.goto(base + '/#/settings/teams');
  await page.locator('tr:has(td:has-text("webteam")) button:has-text("Edit")').click();
  await page.click('.modal .chip:has-text("+ alerts")');
  await page.click('.modal .btn.primary:has-text("Save")');
  await until('the team to have its channel', async () => (await api('/teams')).items.some((tm) => tm.name === 'webteam' && tm.channels.join() === 'alerts'));
  await page.waitForSelector('td:has-text("alerts")');
  await page.goto(base + '/#/monitors/same-check-b/edit');
  await page.locator('label.switch:has-text("Mute") input').check();
  await page.click('form button.primary');
  await until('mute=yes to be written', async () => /same-check-b\s.*mute=yes/.test((await api('/config')).text || ''));
  if (errors.length) throw new Error(errors.join(' | '));
});

await step('a team member sees only their team', async () => {
  // The administrator adds an operator account in webteam.
  const ok = await page.evaluate(async () => {
    const csrf = (await (await fetch('/api/v1/session')).json()).csrf;
    const h = { 'Content-Type': 'application/json', 'X-Unagikuma-CSRF': csrf };
    const none = await fetch('/api/v1/users', { method: 'POST', headers: h, body: JSON.stringify({ username: 'teamop', password: 'team-pass-1', role: 'operator' }) });
    const u = await fetch('/api/v1/users', { method: 'POST', headers: h, body: JSON.stringify({ username: 'teamop', password: 'team-pass-1', role: 'operator', teams: ['webteam'] }) });
    return none.status === 400 && u.ok;
  });
  if (!ok) throw new Error('could not set up the team member');
  const tctx = await browser.newContext({ viewport: { width: 1366, height: 900 }, locale: 'en-US' });
  const tp = await tctx.newPage();
  const terr = [];
  tp.on('pageerror', (e) => terr.push(e.message));
  await tp.goto(base);
  await tp.fill('input[autocomplete=username]', 'teamop');
  await tp.fill('input[autocomplete=current-password]', 'team-pass-1');
  await tp.click('.auth .btn.primary');
  await tp.waitForSelector('nav.nav');
  const nav = await tp.$$eval('nav.nav a', (as) => as.map((a) => a.getAttribute('href')));
  // Settings is there for their own password: the Account tab only.
  if (nav.join(' ') !== '#/ #/monitors #/problems #/events #/settings') throw new Error('menu: ' + nav.join(' '));
  await tp.goto(base + '/#/settings');
  await tp.waitForSelector('.tabs a');
  const tabs = await tp.$$eval('.tabs a', (as) => as.map((a) => a.getAttribute('href')));
  if (tabs.join(' ') !== '#/settings/account') throw new Error('settings tabs: ' + tabs.join(' '));
  const mons = await tp.evaluate(async () => (await (await fetch('/api/v1/monitors')).json()).items.map((m) => m.name));
  if (mons.join(',') !== 'same-check-b') throw new Error('monitors: ' + mons.join(','));
  for (const [route, sel] of [['#/', '.fp-scroll'], ['#/monitors', '.mon-list, .tbl, .hive'], ['#/problems', '.page'], ['#/events', '.page'], ['#/monitors/same-check-b', '.mon-head']]) {
    await tp.goto(base + '/' + route);
    try {
      await tp.waitForSelector(sel, { timeout: 8000 });
    } catch (e) {
      await tp.screenshot({ path: `${out}/team-member-${route.replace(/\W+/g, '-')}.png`, fullPage: true }).catch(() => {});
      throw new Error(route + ': ' + e.message.split('\n')[0] + (terr.length ? ' — script errors: ' + terr.join(' | ') : ''));
    }
  }
  if ((await tp.content()).includes('127.0.0.1-1')) throw new Error('another monitor is shown');
  // Teams, channels and contacts are for administrators.
  const r = await tp.evaluate(async () => {
    const csrf = (await (await fetch('/api/v1/session')).json()).csrf;
    const h = { 'Content-Type': 'application/json', 'X-Unagikuma-CSRF': csrf };
    const list = (await (await fetch('/api/v1/teams')).json()).items.map((t) => t.name).join(',');
    const save = (await fetch('/api/v1/teams', { method: 'POST', headers: h, body: JSON.stringify({ name: 'webteam', title: 'Web', members: ['teamop'] }) })).status;
    return { list, save };
  });
  if (r.list !== 'webteam') throw new Error('teams listed: ' + r.list);
  if (r.save !== 403) throw new Error('a member saving a team: ' + r.save);
  await tctx.close();
  if (terr.length) throw new Error(terr.join(' | '));
});

await step('configuration editor shows monitors.conf', async () => {
  await page.goto(base + '/#/config/edit');
  // Polled with evaluate: waitForFunction needs eval, which the UI's CSP forbids.
  await until('monitors.conf in the editor', () => page.evaluate(() =>
    /IN SOA|\$FILE/.test(document.body.innerText) || [...document.querySelectorAll('textarea')].some((t) => /IN SOA|\$FILE/.test(t.value))));
});

await step('Nagios import previews', async () => {
  await page.goto(base + '/#/config/import');
  await page.click('.src-card:has-text("Nagios")');
  await page.setInputFiles('input[type=file]', nagiosCfg);
  await page.click('button:has-text("Preview")');
  await page.waitForSelector('pre.code:has-text("IN PING")');
});

await step('a channel is added', async () => {
  await page.goto(base + '/#/alerts/channels');
  await page.click('button:has-text("Add channel")');
  await page.selectOption('.modal select', 'log');
  await page.fill('.modal input[pattern]', 'ops-log');
  await page.click('.modal .btn.primary');
  await page.waitForSelector('td:has-text("ops-log")');
});

await step('an e-mail channel shows the fields of its way of sending', async () => {
  await page.goto(base + '/#/alerts/channels');
  await page.click('button:has-text("Add channel")');
  await page.selectOption('.modal select', 'email');
  const labels = async () => (await page.$$eval('.modal .field > label', (ls) => ls.map((l) => l.textContent))).join('|');
  let l = await labels();
  if (!l.includes('SMTP server *') || l.includes('sendmail command')) throw new Error('SMTP form: ' + l);
  await page.selectOption('.modal .field:has(label:text-is("Send through")) select', 'sendmail');
  l = await labels();
  if (l.includes('SMTP server') || !l.includes('sendmail command') || l.includes('From *')) throw new Error('sendmail form: ' + l);
  await page.click('.modal button:has-text("Cancel")');
});

await step('a new e-mail address is offered for the contact directory', async () => {
  await page.goto(base + '/#/alerts/channels');
  await page.click('button:has-text("Add channel")');
  await page.selectOption('.modal select', 'email');
  await page.fill('.modal input[pattern]', 'ops-mail');
  const field = (label) => `.modal .field:has(label:text-is("${label}")) input`;
  await page.fill(field('SMTP server *'), '127.0.0.1');
  await page.fill(field('From *'), 'mon@example.com');
  await page.fill(field('To *'), 'li@example.com');
  await page.click('.modal .btn.primary:has-text("Save")');
  await page.waitForSelector('.modal h2:has-text("New addresses")');
  await page.fill('.modal:has(h2:has-text("New addresses")):not(:has(form)) input', 'Li Lei');
  await page.click('button:has-text("Add and save")');
  await page.waitForSelector('td:has-text("ops-mail")');
  if (await page.$('tr:has-text("ops-mail") .rdot')) throw new Error('a registered address has a red dot');
  const li = (await api('/people')).people.find((p) => p.emails.includes('li@example.com'));
  if (!li || li.notify.join(',') !== 'ops-mail' || li.reach) throw new Error('the new contact is not reached through ops-mail: ' + JSON.stringify(li));
  // Li goes on leave: the channel gets a red dot with the reason.
  await page.goto(base + '/#/alerts/contacts');
  await page.waitForSelector('td:has-text("Li Lei")');
  await page.selectOption('tr:has-text("Li Lei") select', 'leave');
  await until('Li to be on leave', async () => (await api('/people')).people.some((p) => p.id === li.id && p.status === 'leave'));
  await page.goto(base + '/#/alerts/channels');
  await page.waitForSelector('tr:has-text("ops-mail") .rdot');
  // Then leaves, which asks first.
  await page.goto(base + '/#/alerts/contacts');
  await page.waitForSelector('td:has-text("Li Lei")');
  await page.selectOption('tr:has-text("Li Lei") select', 'left');
  await page.click('.modal .btn.danger');
  await until('Li to have left', async () => (await api('/people')).people.some((p) => p.id === li.id && p.status === 'left'));
  // It is a problem too: listed on the Problems page, whose menu entry gets a red dot.
  await page.goto(base + '/#/problems');
  await page.waitForSelector('.setup-issues a[href="#/alerts/contacts"]');
  await page.waitForSelector('.nav a[href="#/problems"] .rdot');
  if (errors.length) throw new Error(errors.join(' | '));
});

await step('a change is announced and closed with a receipt', async () => {
  await page.goto(base + '/#/notices/changes');
  await page.click('button:has-text("Announce a change")');
  await page.fill('.modal .field:has(label:text-is("Title")) input', 'Core switch upgrade');
  await page.fill('.modal .field:has(label:text-is("Impact")) input', 'VPN down for 10 minutes');
  await page.click('.modal .btn.primary:has-text("Announce")');
  await page.waitForSelector('td:has-text("Core switch upgrade")');
  await page.click('tr:has-text("Core switch upgrade") button:has-text("Done")');
  await page.fill('.modal textarea', 'Upgraded to 17.9, links checked');
  await page.click('.modal .btn.primary');
  await page.waitForSelector('tr:has-text("Core switch upgrade") .tag:has-text("Done")');
  await until('the notices to be delivered', async () => (await api('/notifications')).items.filter((n) => n.kind === 'CHANGE').length >= 2);
  if (errors.length) throw new Error(errors.join(' | '));
});

await step('the daily report is chosen per team and can be switched off per person', async () => {
  await page.goto(base + '/#/notices/report');
  await page.waitForSelector('.card pre.report');
  const rep = await api('/reports/daily');
  if (rep.team !== 'admin' || !rep.teams.some((x) => x.name === 'admin' && x.receives)) throw new Error('the admin team does not receive it: ' + JSON.stringify(rep.teams));
  const other = rep.teams.find((x) => x.name !== 'admin');
  if (other) {
    await page.selectOption('label:has-text("Team") select', other.name);
    await page.waitForSelector(`.card h3:has-text("[${other.name}]")`);
    await page.check('label.switch:has-text("This team receives the daily report") input');
    await until('the team to receive it', async () => (await api('/reports/daily?team=' + other.name)).teams.some((x) => x.name === other.name && x.receives));
  }
  await page.goto(base + '/#/alerts/contacts');
  await page.waitForSelector('td:has-text("Li Lei")');
  await page.uncheck('tr:has-text("Li Lei") label.switch input');
  await until('Li to have switched it off', async () => (await api('/people')).people.some((p) => p.name === 'Li Lei' && p.no_report));
  if (errors.length) throw new Error(errors.join(' | '));
});

await step('an announcement reaches the public status page', async () => {
  await page.goto(base + '/#/notices/pages');
  await page.click('button:has-text("New announcement")');
  await page.fill('.modal input[type=text]', 'Planned database upgrade');
  await page.click('.modal .btn.primary');
  await page.waitForSelector('td:has-text("Planned database upgrade")');
  const pub = await browser.newPage();
  watch(pub);
  await pub.goto(base + '/status/public');
  await pub.waitForSelector('.notice:has-text("Planned database upgrade")');
  await pub.close();
});

await step('the language can be switched', async () => {
  await page.goto(base + '/#/');
  await page.selectOption('select:has(option[value="zh"])', 'zh');
  await page.waitForSelector('nav.nav:has-text("监控")');
  await page.selectOption('select:has(option[value="en"])', 'en');
  await page.waitForSelector('nav.nav:has-text("Monitors")');
});

await step('phones get no sideways scrolling', async () => {
  const state = await ctx.storageState();
  const phone = await browser.newContext({ viewport: { width: 390, height: 844 }, locale: 'en-US', storageState: state, isMobile: true });
  const p = await phone.newPage();
  watch(p);
  const mon = (await api('/monitors')).items.find((m) => m.target === '127.0.0.1:1').name;
  for (const route of ['#/', '#/monitors', '#/monitors/' + encodeURIComponent(mon), '#/alerts/channels', '#/alerts/contacts', '#/notices/changes', '#/notices/pages', '#/settings']) {
    await p.goto(base + '/' + route);
    await p.waitForTimeout(800);
    const w = await p.evaluate(() => document.documentElement.scrollWidth);
    if (w > 392) {
      // Name the widest elements so the fix is easy to find.
      const wide = await p.evaluate(() => [...document.querySelectorAll('body *')]
        .filter((e) => e.getBoundingClientRect().right > 392)
        .slice(0, 6).map((e) => `${e.tagName.toLowerCase()}.${[...e.classList].join('.')} ${Math.round(e.getBoundingClientRect().right)}`).join(', '));
      throw new Error(`${route} is ${w}px wide: ${wide}`);
    }
  }
  await phone.close();
});

await step('no script errors', async () => {
  if (errors.length) throw new Error(errors.join(' | '));
});

await browser.close();
console.log(failed ? `${failed} failed` : 'all passed');
process.exit(failed ? 1 : 0);
