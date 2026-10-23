'use strict';
const { app, BrowserWindow, nativeTheme } = require('electron');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
app.setPath('userData', process.argv[2]);
const assets = path.resolve(__dirname, '../../internal/whalebridge/assets');
const names = new Set(fs.readdirSync(assets));
const mutations = [], external = [], unexpected = [];
const secrets = new Map([['relay', 'synthetic-server-only-relay-key'], ['header', 'synthetic-server-only-header']]);
let revision = 0, stateReads = 0;
// Raw catalog facts stay separate from display overrides and multipliers.
const zeroPrice = { input: 0, output: 0, cache_read: 0, cache_write: 0, cache_write_1h: 0 };
const priceFacts = [
  ...['cache_read', 'cache_write', 'cache_write_1h'].map(field => ({ id: 'paid-' + field, price: zeroPrice, list: { ...zeroPrice, [field]: 0.1 }, expectedFree: false })),
  ...Object.keys(zeroPrice).map(field => ({ id: 'paid-tier-' + field, price: zeroPrice, list: { ...zeroPrice, tiers: [{ ...zeroPrice, above: 100000, [field]: 0.2 }] }, expectedFree: false })),
  { id: 'raw-zero-without-list', price: zeroPrice, expectedFree: false },
  { id: 'raw-zero-ownprice-false', price: zeroPrice, ownPrice: false, expectedFree: false },
  { id: 'override-zero-listed-paid', price: zeroPrice, list: { input: 1, output: 2 }, ownPrice: true, expectedFree: false },
  { id: 'override-zero-unknown-list', price: zeroPrice, ownPrice: true, expectedFree: false },
  { id: 'known-listed-zero', price: { input: 9, output: 18 }, list: zeroPrice, ownPrice: true, expectedFree: true },
  { id: 'all-tiers-zero', price: { input: 9, output: 18 }, list: { ...zeroPrice, tiers: [{ ...zeroPrice, above: 100000 }] }, expectedFree: true },
  { id: 'zero-rate-still-paid', price: zeroPrice, list: { input: 1, output: 2 }, rate: 0, expectedFree: false },
  { id: 'explicit-free-wins', price: { ...zeroPrice, cache_write_1h: 2, tiers: [{ ...zeroPrice, above: 100000, input: 3 }] }, list: { input: 1, output: 2 }, ownPrice: true, rate: 0, free: true, expectedFree: true }
];
let providers = [{
  id: 'relay', name: '本地路由测试来源', chat: 'https://relay.fixture.invalid/v1', routing: 'order', keySet: true, headerNames: ['X-Fixture-Secret'],
  models: ['free-model', 'paid-model', 'unknown-model'], chosen: ['free-model', 'paid-model', 'unknown-model'], modelCount: 3,
  available: [{ id: 'free-model', name: '已确认免费模型', free: true }, { id: 'paid-model', name: '已知付费模型', price: { input: 1, output: 2 } }, { id: 'unknown-model', name: '价格尚未报告的模型' }, ...priceFacts],
  fallback: ['relay/paid-model'], affinity: 'session'
}];
let groups = [{ id: 'daily', name: '日常路由组', routing: 'order', members: ['relay/free-model', 'relay/paid-model'], pick: 'relay/paid-model', affinity: 'session', weights: { 'relay/free-model': 2, 'relay/paid-model': 3 }, off: [], fast: [] }];
const globalData = { settings: {}, models: [], modelPrices: {}, searchers: [], visionModels: [], drawingModels: [], fx: { rate: 7 }, sync: {}, searchVendors: [], searches: [] };
const now = new Date().toISOString();
const candidate = (id, routing, free, extra = {}) => ({ id, who: id, provider: 'relay', name: '本地来源 ' + id, model: 'relay/' + id, routing, ...(free === undefined ? {} : { free }), ...extra });
const traceRoutes = [
  { id: 101, model: 'group/daily', provider: 'relay', served: 'free-model', time: now, done: true, status: 200, group: { id: 'daily', name: '日常免费优先组', routing: 'free' }, order: [candidate('confirmed-free', 'free', true), candidate('known-paid', 'free', false), candidate('price-unknown', 'free', undefined), candidate('legacy-inner', 'order', undefined), candidate('truthy-not-confirmed', 'free', 'true')], tries: [] },
  { id: 102, model: 'relay/direct-free-policy', provider: 'relay', served: 'paid-model', time: now, done: true, status: 200, order: [candidate('direct-paid', 'free', false), candidate('direct-free', 'free', true)], tries: [] },
  { id: 103, model: 'group/ordered', provider: 'relay', served: 'paid-model', time: now, done: true, status: 200, group: { id: 'ordered', name: '旧顺序组', routing: 'order' }, order: [candidate('ordered', 'order', undefined)], tries: [] }
];
const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url, 'http://localhost');
    const json = value => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(value)); };
    if (url.pathname.startsWith('/api/')) {
      let raw = ''; for await (const chunk of req) raw += chunk;
      const body = raw ? JSON.parse(raw) : undefined;
      if (req.method === 'POST') mutations.push({ path: url.pathname, body });
      if (url.pathname === '/api/state') {
        stateReads++;
        const snapshot = { version: 'freefirst-qa', fixtureRevision: revision, providers, presets: [], groups, models: providers.flatMap(p => p.available.map(m => ({ ...m, id: p.id + '/' + m.id }))), hidden: [] };
        return setTimeout(() => json(snapshot), 55);
      }
      if (url.pathname === '/api/global') return json(globalData);
      if (url.pathname === '/api/provider' && req.method === 'POST') {
        const old = providers.find(p => p.id === body.from || p.id === body.id);
        assert.ok(old, 'only the existing fake provider may be saved');
        providers = providers.map(p => p === old ? { ...old, ...body, chosen: body.models } : p);
        revision++;
        return json({ ok: true, id: body.id });
      }
      if (url.pathname === '/api/group' && req.method === 'POST') {
        const old = groups.find(g => g.id === body.from || g.id === body.id);
        assert.ok(old, 'only the existing fake group may be saved');
        groups = groups.map(g => g === old ? { ...old, ...body } : g);
        revision++;
        return json({ ok: true });
      }
      if (url.pathname === '/api/routes/history') return json({ days: [{ day: now.slice(0, 10) }] });
      if (url.pathname === '/api/routes') return json({ routes: traceRoutes });
      if (url.pathname === '/api/quotas' || url.pathname === '/api/subscriptions' || url.pathname === '/api/tools/efforts') return json([]);
      if (url.pathname === '/api/subscriptions/alerts') return json({ sequence: 0, alerts: [] });
      if (url.pathname === '/api/upstream') return json({ providers: {}, vendors: [] });
      if (url.pathname === '/api/lanes') return json({});
      unexpected.push(req.method + ' ' + url.pathname);
      res.writeHead(404); return res.end('Unknown fixture API');
    }
    const name = url.pathname === '/' ? 'index.html' : decodeURIComponent(url.pathname.slice(1));
    if (!names.has(name)) { res.writeHead(404); return res.end(); }
    res.setHeader('Content-Type', ({ '.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html', '.svg': 'image/svg+xml', '.png': 'image/png' })[path.extname(name)] || 'application/octet-stream');
    res.end(fs.readFileSync(path.join(assets, name)));
  } catch (error) { res.writeHead(500); res.end(error.stack); }
});

(async () => {
  await app.whenReady();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const win = new BrowserWindow({ width: 1120, height: 820, show: false, webPreferences: { contextIsolation: true, nodeIntegration: false, backgroundThrottling: false } });
  const origin = `http://127.0.0.1:${server.address().port}`;
  win.webContents.session.webRequest.onBeforeRequest((details, callback) => {
    const local = details.url.startsWith(origin + '/') || details.url.startsWith('data:');
    if (!local) external.push(details.url);
    callback({ cancel: !local });
  });
  const js = source => win.webContents.executeJavaScript(source).catch(error => { throw new Error(source + ': ' + error.message); });
  const wait = async expression => {
    const until = Date.now() + 5000;
    while (!await js(`Boolean(${expression})`)) {
      if (Date.now() > until) throw new Error('DOM timeout ' + expression + ' ' + JSON.stringify(await js(`({message:document.querySelector('#message-text')?.textContent,error:document.querySelector('#form-error')?.textContent,invalid:[...document.querySelectorAll(':invalid')].map(e=>({id:e.id,value:e.value,message:e.validationMessage}))})`)));
      await new Promise(resolve => setTimeout(resolve, 20));
    }
  };
  const click = selector => js(`document.querySelector(${JSON.stringify(selector)}).click()`);
  const fill = (selector, value, event = 'change') => js(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});e.value=${JSON.stringify(value)};e.dispatchEvent(new Event(${JSON.stringify(event)},{bubbles:true}));})()`);
  const ready = () => wait(`document.querySelector('#content').getAttribute('aria-busy')==='false'&&document.querySelector('#refresh').getAttribute('aria-busy')!=='true'`);
  const close = async () => { await click('#cancel-editor'); await wait(`!document.querySelector('#editor').open`); };
  const reload = async page => {
    await new Promise(resolve => { win.webContents.once('did-finish-load', resolve); win.reload(); });
    await ready(); await js(`go(${JSON.stringify(page)})`); await ready();
  };
  const openProvider = async () => { await js(`openProvider('relay');activateEditorSection('allocation')`); await wait(`document.querySelector('#editor').open&&editor.section==='allocation'`); };
  const openGroup = async () => { await js(`openGroup('daily');activateEditorSection('routing')`); await wait(`document.querySelector('#editor').open&&editor.section==='routing'`); };
  const save = async (type, expected) => {
    const before = revision;
    await js(`window.freefirstStateBeforeSave=state`);
    await click('#save');
    await wait(`!document.querySelector('#editor').open`);
    await wait(`state!==window.freefirstStateBeforeSave&&state.fixtureRevision===${before + 1}&&(${expected})&&document.querySelector('#content').getAttribute('aria-busy')==='false'&&document.querySelector('#refresh').getAttribute('aria-busy')!=='true'`);
    assert.equal(revision, before + 1);
    return mutations.findLast(row => row.path === '/api/' + type).body;
  };
  const expectHint = async visible => {
    assert.equal(await js(`(()=>{const h=document.querySelector('[data-free-first-hint]');return !!h&&!h.hidden&&getComputedStyle(h).display!=='none'})()`), visible);
    if (visible) {
      const text = await js(`document.querySelector('[data-free-first-hint]').textContent`);
      assert.match(text, /未知价格按付费处理/);
      assert.match(text, /回退到付费模型/);
      assert.match(text, /会话亲和.*额度限制.*冷却/);
    }
  };
  const resize = async width => {
    win.setContentSize(width, 820);
    await wait(`innerWidth===${width}&&innerHeight===820`);
    await js(`new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`);
  };
  const geometry = async label => {
    const bounds = await js(`(()=>{const targets=[document.documentElement,document.querySelector('#main'),...document.querySelectorAll('#editor[open],#editor[open] .editor-content,[data-free-first-hint]:not([hidden]),[data-free-first-trace]')].filter(e=>e&&e.getClientRects().length);return targets.map(e=>{const r=e.getBoundingClientRect();return {tag:e.id||e.className||e.tagName,client:e.clientWidth,scroll:e.scrollWidth,left:r.left,right:r.right,width:r.width,viewport:innerWidth}})})()`);
    for (const b of bounds) {
      assert.ok(b.scroll <= b.client + 1, label + ' horizontal overflow ' + JSON.stringify(b));
      assert.ok(b.left >= -1 && b.right <= b.viewport + 1, label + ' out of viewport ' + JSON.stringify(b));
    }
  };
  try {
    await win.loadURL(origin); await ready();
    await js(`go('providers')`); await ready();
    await openProvider();
    assert.equal(await js(`document.querySelector('#f-routing option[value="free"]').textContent`), '免费优先');
    await fill('#f-fallback', 'relay/unknown-model\nrelay/paid-model', 'input');
    await fill('#f-affinity', 'off');
    for (const policy of ['free', 'order', '', 'free']) {
      await fill('#f-routing', policy); await expectHint(policy === 'free');
      assert.equal(await js(`document.querySelector('#f-fallback').value`), 'relay/unknown-model\nrelay/paid-model');
      assert.equal(await js(`document.querySelector('#f-affinity').value`), 'off');
    }
    let saved = await save('provider', `state.providers.find(p=>p.id==='relay').routing==='free'`);
    assert.equal(saved.routing, 'free');
    assert.deepEqual(saved.fallback, ['relay/unknown-model', 'relay/paid-model']);
    assert.equal(saved.affinity, 'off');
    await reload('providers');
    await openProvider();
    assert.equal(await js(`document.querySelector('#f-routing').value`), 'free');
    assert.equal(await js(`document.querySelector('#f-fallback').value`), 'relay/unknown-model\nrelay/paid-model');
    await close();
    assert.match(await js(`document.querySelector('#content').textContent`), /免费优先/);
    for (const policy of ['order', '']) {
      await openProvider(); await fill('#f-routing', policy); await expectHint(false);
      saved = await save('provider', `state.providers.find(p=>p.id==='relay').routing===${JSON.stringify(policy)}`);
      assert.equal(saved.routing, policy);
      await openProvider(); assert.equal(await js(`document.querySelector('#f-routing').value`), policy); await close();
    }
    await js(`go('routing')`); await ready();
    await openGroup();
    assert.equal(await js(`document.querySelector('#f-routing option[value="free"]').textContent`), '免费优先');
    await js(`activateEditorSection('members')`);
    await fill('#f-members', 'relay/free-model\nrelay/unknown-model\nrelay/paid-model', 'input');
    await fill('#group-member-table [data-member="relay/free-model"] [data-member-action="weight"]', '7');
    await js(`activateEditorSection('routing')`);
    await fill('#f-firstToken', '17');
    await fill('#f-affinity', 'off');
    for (const policy of ['free', 'order', '', 'free']) {
      await fill('#f-routing', policy); await expectHint(policy === 'free');
      assert.equal(await js(`document.querySelector('#f-members').value`), 'relay/free-model\nrelay/unknown-model\nrelay/paid-model');
      assert.equal(await js(`document.querySelector('#f-pick').value`), 'relay/paid-model');
      assert.equal(await js(`document.querySelector('#f-firstToken').value`), '17');
    }
    saved = await save('group', `state.groups.find(g=>g.id==='daily').routing==='free'`);
    assert.deepEqual(saved.members, ['relay/free-model', 'relay/unknown-model', 'relay/paid-model']);
    assert.equal(saved.weights['relay/free-model'], 7);
    assert.equal(saved.pick, 'relay/paid-model');
    assert.equal(saved.firstToken, 17);
    assert.equal(saved.affinity, 'off');
    await reload('routing');
    await openGroup(); assert.equal(await js(`document.querySelector('#f-routing').value`), 'free');
    assert.equal(await js(`document.querySelector('#f-members').value`), 'relay/free-model\nrelay/unknown-model\nrelay/paid-model');
    assert.equal(await js(`document.querySelector('#f-firstToken').value`), '17'); await close();
    assert.match(await js(`document.querySelector('#content').textContent`), /免费优先/);
    for (const policy of ['order', '']) {
      await openGroup(); await fill('#f-routing', policy); await expectHint(false);
      saved = await save('group', `state.groups.find(g=>g.id==='daily').routing===${JSON.stringify(policy)}`);
      assert.equal(saved.routing, policy); assert.equal(saved.weights['relay/free-model'], 7);
      await openGroup(); assert.equal(await js(`document.querySelector('#f-routing').value`), policy); await close();
    }
    // Final page reload is separate from save's state refresh.
    await reload('overview');
    assert.equal(await js(`state.fixtureRevision`), 6);
    await openProvider(); assert.equal(await js(`document.querySelector('#f-routing').value`), ''); await close();
    await openGroup(); assert.equal(await js(`document.querySelector('#f-routing').value`), ''); await close();

    for (const width of [1120, 390]) for (const theme of ['light', 'dark']) {
      nativeTheme.themeSource = theme;
      await resize(width);
      await wait(`matchMedia('(prefers-color-scheme: dark)').matches===${theme === 'dark'}`);
      await js(`go('providers')`); await ready();
      await openProvider(); await fill('#f-routing', 'free'); await expectHint(true);
      await geometry(`${width}/${theme} provider`); await close();
      await js(`go('routing')`); await ready();
      await openGroup(); await fill('#f-routing', 'free'); await expectHint(true);
      await geometry(`${width}/${theme} group`); await close();
      await js(`usageSection='routing';go('usage')`); await ready();
      await wait(`document.querySelectorAll('.route-record').length===3`);
      await js(`[...document.querySelectorAll('.route-record')].forEach(e=>e.open=true)`);
      const records = await js(`[...document.querySelectorAll('.route-record')].map(e=>({model:e.querySelector('summary strong').textContent,routing:[...e.querySelectorAll('dt')].find(d=>d.textContent==='路由方式')?.nextElementSibling.textContent,hints:[...e.querySelectorAll('[data-free-first-trace]')].map(h=>h.textContent),rows:[...e.querySelectorAll('tbody tr')].map(r=>[...r.cells].map(c=>c.textContent))}))`);
      const grouped = records.find(r => r.model === 'group/daily');
      assert.match(grouped.routing, /免费优先.*日常免费优先组/);
      assert.equal(grouped.hints.length, 1); assert.match(grouped.hints[0], /未知价格按付费处理/);
      for (const row of grouped.rows) {
        const who = row[1], note = row[4];
        if (who.includes('confirmed-free')) assert.match(note, /免费候选/);
        else {
          assert.doesNotMatch(note, /免费候选/);
          if (!who.includes('legacy-inner')) assert.match(note, /付费.*未知价格候选/);
          else assert.doesNotMatch(note, /未知价格候选/);
        }
      }
      const direct = records.find(r => r.model === 'relay/direct-free-policy');
      assert.equal(direct.routing, '免费优先'); assert.equal(direct.hints.length, 1);
      const ordered = records.find(r => r.model === 'group/ordered');
      assert.match(ordered.routing, /按顺序.*旧顺序组/); assert.deepEqual(ordered.hints, []);
      await geometry(`${width}/${theme} trace`);
    }
    // Exercise the shared classifier and the actual catalog controls without
    // submitting this draft, so the six earlier routing mutations stay exact.
    const priceChecks = await js(`${JSON.stringify(priceFacts)}.map(m=>({id:m.id,free:freeProviderModel(m)}))`);
    assert.deepEqual(priceChecks, priceFacts.map(m => ({ id: m.id, free: m.expectedFree })), 'catalog facts, every cache/tier component and explicit free decide classification');
    await js(`go('providers')`); await ready();
    await openProvider(); await js(`activateEditorSection('models')`);
    await fill('#provider-model-filter', 'free');
    const expectedFree = ['free-model', ...priceFacts.filter(m => m.expectedFree).map(m => m.id)];
    const modelRows = () => js(`[...document.querySelectorAll('[data-provider-model]')].map(e=>e.dataset.providerModel)`);
    assert.deepEqual(await modelRows(), expectedFree, 'free filter excludes cache charges, paid tiers, unknown originals and zero display overrides');
    const freeBadges = await js(`[...document.querySelectorAll('[data-provider-model]')].map(e=>({id:e.dataset.providerModel,free:[...e.querySelectorAll('.provider-model-facts span')].some(s=>s.textContent==='免费')}))`);
    assert.ok(freeBadges.every(row => row.free), 'each filtered catalog row retains its free fact');
    await click('[data-action="models-free"]');
    assert.deepEqual(await js(`document.querySelector('#f-models').value.split('\\n').filter(Boolean)`), expectedFree, 'only-free selects the same catalog facts as the filter');
    assert.deepEqual(await js(`[...document.querySelectorAll('[data-provider-model-pick]:checked')].map(e=>e.dataset.providerModelPick)`), expectedFree);
    await fill('#provider-model-filter', 'all');
    assert.equal((await modelRows()).length, 3 + priceFacts.length);
    assert.deepEqual(await js(`[...document.querySelectorAll('[data-provider-model-pick]:checked')].map(e=>e.dataset.providerModelPick)`), expectedFree, 'switching back to all keeps only-free selection');
    await geometry('catalog price facts');
    await close();
    assert.deepEqual(providers[0].models, ['free-model', 'paid-model', 'unknown-model'], 'catalog-only checks never save their selection draft');
    assert.equal(mutations.length, 6);
    for (const row of mutations) {
      assert.ok(['/api/provider', '/api/group'].includes(row.path));
      const serialized = JSON.stringify(row.body);
      for (const value of secrets.values()) assert.ok(!serialized.includes(value));
      if (row.path === '/api/provider') {
        for (const key of ['key', 'balanceToken', 'secretAccessKey']) assert.ok(!Object.hasOwn(row.body, key), 'blank secret must be omitted: ' + key);
        assert.deepEqual(row.body.headers, {});
      }
    }
    assert.equal(secrets.get('relay'), 'synthetic-server-only-relay-key');
    assert.equal(secrets.get('header'), 'synthetic-server-only-header');
    assert.ok(stateReads >= 8, 'each save and page reload read the fake state');
    assert.deepEqual(unexpected, [], 'all requests use known local fixture endpoints');
    assert.deepEqual(external, [], 'no provider, paid model or external request was sent');
    console.log('WHALEBRIDGE_FREEFIRST_RESULT:PASS');
  } finally { win.destroy(); server.close(); }
})().then(() => app.exit(0), error => { console.error(error); server.close(); app.exit(1); });
