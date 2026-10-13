// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider with none of its models picked serves agents its vendor's
// list (#614: the one model unticked and saved was still "1 model" on the
// card, and the editor showed it unticked as if left out). With no pick the
// models agents are served are drawn as served (dashed, .auto), their title
// says why, and the hint says how to use none, from the moment the editor
// opens. A served one clicked is picked alone (#681: it used to be left out,
// the rest picked in its place). Select none is saved as noModels and opens
// again as none (#1818: it was saved as no picks, and the card, the gateway
// and the auto groups all served 17). Kept for routing groups alone with
// none picked, the models the groups use are drawn dashed too (#1818: drawn
// as none while the groups used them). In every language, Chromium and
// WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const base = {
  icon: "generic", responses: "", anthropic: "", catalog: "",
  models: [{ id: "glm-5.3-free", name: "glm-5.3-free", on: true }, { id: "glm-5.2", name: "glm-5.2", on: true }], chosen: [], fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const relay = { ...base, id: "router", name: "Router", host: "router.example.com", chat: "https://router.example.com/v1" };
const watched = { ...base, id: "watched", name: "Watched", host: "watched.example.com", chat: "https://watched.example.com/v1", noModels: true, exposed: 0 };
const grouped = { ...base, id: "grouped", name: "Grouped", host: "grouped.example.com", chat: "https://grouped.example.com/v1", unlisted: true, groups: { "glm-5.2": ["group/auto-glm-5-2"], "glm-5.3-free": ["group/auto-glm-5-3-free"] } };

function serve(lang, saves) {
  const providers = { providers: [relay, watched, grouped], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/provider/save") { saves.push(req.postDataJSON()); return json(providers); }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// why: a dashed chip's title; hint: the hint with none picked; none: the
// hint after Select none; groupsWhy, groupsHint: those kept for routing
// groups alone
const words = {
  en: { save: "Save", selectNone: "Select none", why: "Agents see it: none are picked", hint: "or Select none to use none of them while its balance and usage still show", none: "None selected: no agent or routing group uses its models", groupsWhy: "Routing groups use it: none are picked", groupsHint: "the routing groups they are in use the vendor's list" },
  zh: { save: "保存", selectNone: "全不选", why: "Agent 能看到它：没有勾选任何模型时", hint: "点「全不选」则不使用任何模型，余额和用量照常显示", none: "全不选：Agent 和路由分组都不使用它的模型", groupsWhy: "路由分组会用到它", groupsHint: "它们所在的路由分组使用供应商的列表" },
  "zh-TW": { save: "儲存", selectNone: "全不選", why: "Agent 能看到它：沒有勾選任何模型時", hint: "點「全不選」則不使用任何模型，餘額和用量照常顯示", none: "全不選：Agent 和路由分組都不使用它的模型", groupsWhy: "路由分組會用到它", groupsHint: "它們所在的路由分組使用供應商的列表" },
  ja: { save: "保存", selectNone: "選択をすべて解除", why: "エージェントに表示されます：何も選択されていない", hint: "「選択をすべて解除」でどれも使わず", none: "選択なし：エージェントもルーティンググループも", groupsWhy: "ルーティンググループが使います", groupsHint: "所属するルーティンググループがベンダーのリスト" },
  de: { save: "Speichern", selectNone: "Keine auswählen", why: "Agenten sehen es: Nichts ist gewählt", hint: "„Keine auswählen“, um keines zu nutzen", none: "Keine ausgewählt: Weder Agenten noch Weiterleitungsgruppen", groupsWhy: "Weiterleitungsgruppen nutzen es", groupsHint: "Die Weiterleitungsgruppen, in denen sie sind, nutzen die Liste des Anbieters" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: with none picked, the models agents are served are drawn as served`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-none-picked.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const saves = [];
      await page.route("**/*", serve(lang, saves));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Router" }).click();
      const editor = page.locator("#modal:not([hidden]) .editor");
      const chip = (id) => editor.locator(".mchips .mchip", { hasText: id });
      await chip("glm-5.2").waitFor();
      const cls = (id) => chip(id).evaluate((c) => [c.classList.contains("on"), c.classList.contains("auto")]);

      // opened with none picked, both are drawn as served, not as picked
      // (#614: they were drawn ticked, and the next Save made them picks)
      for (const id of ["glm-5.3-free", "glm-5.2"]) {
        assert.deepEqual(await cls(id), [false, true], id + " drawn as served");
        assert.ok((await chip(id).getAttribute("title")).includes(w.why), id + " title");
      }
      assert.equal(await chip("glm-5.2").evaluate((c) => getComputedStyle(c).borderTopStyle), "dashed");
      await editor.getByText(w.hint, { exact: false }).waitFor();

      // a served one clicked is picked, alone (#681)
      await chip("glm-5.2").click();
      assert.deepEqual(await cls("glm-5.2"), [true, false]);
      assert.deepEqual(await cls("glm-5.3-free"), [false, false]);
      assert.equal(await editor.getByText(w.hint, { exact: false }).count(), 0);
      // and another clicked joins it
      await chip("glm-5.3-free").click();
      assert.deepEqual(await cls("glm-5.2"), [true, false]);
      assert.deepEqual(await cls("glm-5.3-free"), [true, false]);

      // Select none: none drawn as served, the hint says so, and the Save
      // keeps it (#1818: saved as no picks, which serves the vendor's list)
      await editor.getByRole("button", { name: w.selectNone, exact: true }).click();
      for (const id of ["glm-5.3-free", "glm-5.2"]) assert.deepEqual(await cls(id), [false, false], id + " after Select none");
      await editor.getByText(w.none, { exact: false }).waitFor();
      await editor.locator(".bar").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 60 && !saves.length; i++) await page.waitForTimeout(50);
      assert.equal(saves.length, 1, "saved");
      assert.equal(saves[0].noModels, true, "noModels sent");
      assert.deepEqual(saves[0].models || [], [], "no picks sent");
      await page.locator("#modal:not([hidden]) .editor").waitFor({ state: "detached" }).catch(() => {});

      // one saved so opens as none, and a pick then clears it
      await page.locator(".row.provider", { hasText: "Watched" }).click();
      await chip("glm-5.2").waitFor();
      for (const id of ["glm-5.3-free", "glm-5.2"]) assert.deepEqual(await cls(id), [false, false], id + " saved as none");
      await editor.getByText(w.none, { exact: false }).waitFor();
      await chip("glm-5.2").click();
      assert.deepEqual(await cls("glm-5.2"), [true, false]);
      assert.equal(await editor.getByText(w.none, { exact: false }).count(), 0);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: kept for routing groups with none picked, the models the groups use are drawn as served`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, []));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Grouped" }).click();
      const editor = page.locator("#modal:not([hidden]) .editor");
      const chip = (id) => editor.locator(".mchips .mchip", { hasText: id });
      await chip("glm-5.2").waitFor();
      for (const id of ["glm-5.3-free", "glm-5.2"]) {
        assert.deepEqual(await chip(id).evaluate((c) => [c.classList.contains("on"), c.classList.contains("auto")]), [false, true], id + " drawn as served");
        assert.ok((await chip(id).getAttribute("title")).includes(w.groupsWhy), id + " title");
      }
      await editor.getByText(w.groupsHint, { exact: false }).waitFor();
      assert.deepEqual(errors, []);
    });
  }
}
