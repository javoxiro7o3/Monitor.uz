const { chromium } = require('playwright');
(async () => {
  const out = process.argv[2];
  const pages = (process.argv[3] || '').split(',');
  const b = await chromium.launch();
  const errors = [];
  const ctx = await b.newContext({ viewport: { width: 1440, height: 900 }, ignoreHTTPSErrors: true });
  await ctx.addInitScript(() => localStorage.setItem('theme', 'dark'));
  const p = await ctx.newPage();
  p.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  p.on('pageerror', (e) => errors.push('PAGEERR ' + e.message));
  p.on('request', (r) => { if (!r.url().startsWith('https://127.0.0.1:18443')) errors.push('EXTERNAL ' + r.url()); });
  await p.goto('https://127.0.0.1:18443/');
  await p.waitForSelector('#login-form input[name=username]');
  await p.screenshot({ path: `${out}/login.png` });
  await p.fill('input[name=username]', 'admin');
  await p.fill('input[name=password]', 'Monitor-2026!');
  await p.click('#login-form button');
  await p.waitForSelector('#nav a');
  for (const name of pages) {
    const [hash, file, h, theme, w] = name.split('|');
    if (theme) await p.evaluate((t) => { localStorage.setItem('theme', t); }, theme);
    if (w) await p.setViewportSize({ width: +w, height: 900 });
    await p.goto('https://127.0.0.1:18443/' + hash);
    if (theme) await p.reload();
    await p.waitForTimeout(3500);
    await p.screenshot({ path: `${out}/${file}.png`, fullPage: h !== 'short' });
  }
  console.log('errors:', JSON.stringify(errors));
  await b.close();
})();
