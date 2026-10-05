const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { test } = require('node:test');
const { chromium, webkit } = require('playwright');
const assets = path.resolve(__dirname, '../assets');

for (const engine of ['chromium', 'webkit']) {
  for (const lang of ['en', 'zh']) {
    test(`${engine} ${lang}: Aside provider connection is independent of model selection`, async (t) => {
      const browser = await (engine === 'webkit' ? webkit.launch() : chromium.launch({ channel: 'chromium' }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 980, height: 760 }, reducedMotion: 'reduce' });
      const posts = [], errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      const a = {
        id: 'aside', name: 'Aside', icon: 'aside', path: '~/.aside/u/0/settings.json',
        native: { provider: 'disconnected', runtime: 'applied', fields: { image: { value: 'magpie/art/gpt-image-1', status: 'unverified', detail: "Image generation is configured, but Aside's image provider support has not been verified" } } },
        fields: [
          { key: 'model', label: 'model', value: 'minimax/native', options: [{ value: 'minimax/native', label: 'Native MiniMax' }, { value: 'magpie/relay/m1', ref: 'relay/m1', label: 'Magpie model' }] },
          { key: 'image', label: 'image', value: 'magpie/art/gpt-image-1', options: [{ value: 'magpie/art/gpt-image-1', ref: 'art/gpt-image-1', label: 'GPT image' }] },
        ],
      };
      const state = () => ({ agents: [a], profiles: [], settings: { lang, theme: 'light' } });
      await page.route('**/*', async (route) => {
        const req = route.request(), url = new URL(req.url()), p = url.pathname;
        if (req.method() === 'POST') posts.push(p);
        if (p === '/boot.js') return route.fulfill({ contentType: 'text/javascript', body: `window.bootPrefs=${JSON.stringify({ lang, theme: 'light', web: true })}` });
        if (p === '/api/state') return route.fulfill({ json: state() });
        if (p === '/api/agents/connect/aside') { a.wired = true; a.native.provider = 'connected'; return route.fulfill({ json: { ...state(), connected: { how: 'joined' } } }); }
        if (p === '/api/agents/preview/aside') return route.fulfill({ json: { changes: [{ path: '~/.aside/u/0/models.json', lines: [{ op: '-', text: 'providers.magpie' }] }] } });
        if (p === '/api/agents/disconnect/aside') { a.wired = false; a.native.provider = 'disconnected'; return route.fulfill({ json: state() }); }
        if (p === '/api/providers') return route.fulfill({ json: { providers: [], presets: [], gateway: { running: true } } });
        if (p === '/api/groups') return route.fulfill({ json: { groups: [] } });
        if (p === '/api/plugins') return route.fulfill({ json: { plugins: [] } });
        if (p === '/api/usage/quotas') return route.fulfill({ json: [] });
        if (p === '/api/agents/cli') return route.fulfill({ json: { agents: {}, pending: false } });
        if (p.startsWith('/api/')) return route.fulfill({ json: {} });
        const file = path.join(assets, p === '/' ? 'index.html' : p);
        return route.fulfill({ body: await fs.readFile(file), contentType: { '.js': 'text/javascript', '.html': 'text/html', '.css': 'text/css', '.svg': 'image/svg+xml' }[path.extname(file)] });
      });
      await page.goto('http://magpie.test/?view=agents');
      const row = page.locator('.row.agent[data-id="aside"]');
      await row.locator('.ag-start').waitFor();
      assert.match(await row.locator('.ag-start').textContent(), /Native MiniMax/);
      await row.locator('.ag-conn').click();
      await page.waitForFunction(() => document.querySelector('[data-id="aside"] .ag-conn')?.getAttribute('aria-checked') === 'true');
      assert.match(await row.locator('.ag-start').textContent(), /Native MiniMax/);
      assert.equal(await row.locator('.ag-fix').count(), 0, 'image warning does not offer a provider reconnect');
      await row.locator('.ag-link').click();
      await row.locator('.ag-exp').waitFor();
      assert.match(await row.locator('.ag-exp').textContent(), lang === 'zh' ? /已配置图片生成/ : /Image generation is configured/);
      assert.deepEqual(posts, ['/api/agents/connect/aside'], 'opening details sends no mutation');
      await row.locator('.ag-conn').click();
      const ask = page.locator('.disconnect-ask');
      await ask.waitFor();
      await ask.locator('.bar button').last().click();
      await ask.waitFor({ state: 'detached' });
      assert.match(await row.locator('.ag-start').textContent(), /Native MiniMax/);
      assert.deepEqual(posts, ['/api/agents/connect/aside', '/api/agents/disconnect/aside']);
      assert.deepEqual(errors, []);
    });
  }
}
