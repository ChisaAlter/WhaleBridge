'use strict';
const { app, BrowserWindow } = require('electron');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
app.setPath('userData', process.argv[2]);
const assets = path.resolve(__dirname, '../../internal/whalebridge/assets');
const names = new Set(fs.readdirSync(assets));
const loaded = 'fixture-route/reverse', unloaded = 'removed-plugin/stable';
const friendly = 'Fixture <plugin> & source';
const mutations = [], external = [], unexpected = [];
let revision = 0;
let providers = [{ id: 'relay', name: 'Fixture relay', chat: 'https://fixture.invalid/v1', keySet: true, models: ['m'], chosen: ['m'], available: [{ id: 'm', name: 'Fixture model' }], modelCount: 1, routing: unloaded }];
let groups = [{ id: 'daily', name: 'Fixture group', members: ['relay/m'], routing: unloaded, off: [], fast: [], weights: {} }];
let runtimeRoutings = [{ id: loaded, name: 'reverse', plugin: friendly }];
const now = new Date().toISOString();
const records = [
  { id: 1, model: 'relay/m', provider: 'relay', time: now, done: true, status: 200, order: [{ id: 'relay', model: 'm', routing: loaded }], tries: [] },
  { id: 2, model: 'group/daily', provider: 'relay', time: now, done: true, status: 200, group: { id: 'daily', name: 'Fixture group', routing: unloaded }, order: [{ id: 'relay', model: 'm', routing: unloaded }], tries: [] }
];
const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url, 'http://localhost');
    const json = value => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(value)); };
    if (url.pathname.startsWith('/api/')) {
      let raw = ''; for await (const chunk of req) raw += chunk;
      const body = raw ? JSON.parse(raw) : undefined;
      if (req.method === 'POST') mutations.push({ path: url.pathname, body });
      if (url.pathname === '/api/state') return json({ version: 'runtime-routing-qa', fixtureRevision: revision, providers, presets: [], groups, runtimeRoutings, models: [], hidden: [] });
      if (url.pathname === '/api/provider' && req.method === 'POST') {
        providers = providers.map(p => p.id === body.from ? { ...p, ...body, chosen: body.models } : p);
        revision++; return json({ ok: true, id: body.id });
      }
      if (url.pathname === '/api/group' && req.method === 'POST') {
        groups = groups.map(g => g.id === body.from ? { ...g, ...body } : g);
        revision++; return json({ ok: true });
      }
      if (url.pathname === '/api/global') return json({ settings: {}, models: [], modelPrices: {}, searchers: [], visionModels: [], drawingModels: [], fx: { rate: 7 }, sync: {}, searchVendors: [], searches: [] });
      if (url.pathname === '/api/routes/history') return json({ days: [{ day: now.slice(0, 10) }] });
      if (url.pathname === '/api/routes') return json({ routes: records });
      if (['/api/quotas', '/api/subscriptions', '/api/tools/efforts'].includes(url.pathname)) return json([]);
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
    const deadline = Date.now() + 5000;
    while (!await js(`Boolean(${expression})`)) {
      if (Date.now() > deadline) throw new Error('DOM timeout ' + expression + ' ' + JSON.stringify(await js(`({message:document.querySelector('#message-text')?.textContent,error:document.querySelector('#form-error')?.textContent,invalid:[...document.querySelectorAll(':invalid')].map(e=>({id:e.id,value:e.value,message:e.validationMessage}))})`)));
      await new Promise(resolve => setTimeout(resolve, 20));
    }
  };
  const ready = () => wait(`document.querySelector('#content').getAttribute('aria-busy')==='false'&&document.querySelector('#refresh').getAttribute('aria-busy')!=='true'`);
  const click = selector => js(`document.querySelector(${JSON.stringify(selector)}).click()`);
  const fill = (selector, value) => js(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});e.value=${JSON.stringify(value)};e.dispatchEvent(new Event('change',{bubbles:true}));})()`);
  const close = async () => { await click('#cancel-editor'); await wait(`!document.querySelector('#editor').open`); };
  const open = async kind => { await js(kind === 'provider' ? `openProvider('relay');activateEditorSection('allocation')` : `openGroup('daily');activateEditorSection('routing')`); await wait(`document.querySelector('#editor').open`); };
  const save = async (kind, policy) => {
    const before = revision;
    await click('#save');
    await wait(`!document.querySelector('#editor').open&&state.fixtureRevision===${before + 1}`);
    await ready();
    const saved = mutations.findLast(m => m.path === '/api/' + kind).body;
    assert.equal(saved.routing, policy);
    assert.equal(revision, before + 1);
    return saved;
  };
  const selected = () => js(`({value:document.querySelector('#f-routing').value,label:document.querySelector('#f-routing').selectedOptions[0]?.textContent,hintHidden:document.querySelector('[data-unloaded-routing-hint]').hidden,options:[...document.querySelector('#f-routing').options].map(o=>({value:o.value,label:o.textContent}))})`);
  try {
    await win.loadURL(origin); await ready();
    for (const kind of ['provider', 'group']) {
      await open(kind);
      let control = await selected();
      assert.equal(control.value, unloaded, 'saved unavailable strategy stays selected');
      assert.equal(control.label, unloaded + '（暂未加载）');
      assert.equal(control.hintHidden, false);
      assert.ok(control.options.some(o => o.value === loaded && o.label === friendly + ' · reverse'));
      assert.equal(control.options.some(o => o.value === 'manual'), kind === 'group');
      assert.equal(await js(`document.querySelector('#fields plugin')===null`), true, 'module names remain escaped text');
      await js(`activateEditorSection(${JSON.stringify(kind === 'provider' ? 'connection' : 'members')})`);
      await fill('#f-name', 'Renamed ' + kind);
      assert.equal((await save(kind, unloaded)).name, 'Renamed ' + kind, 'saving an unrelated field preserves the unavailable strategy');
      await open(kind);
      assert.equal((await selected()).value, unloaded);
      await fill('#f-routing', loaded);
      assert.equal((await selected()).hintHidden, true);
      await fill('#f-routing', 'free');
      assert.equal(await js(`document.querySelector('[data-free-first-hint]').hidden`), false);
      await fill('#f-routing', loaded);
      assert.equal(await js(`document.querySelector('[data-free-first-hint]').hidden`), true);
      await save(kind, loaded);
      await open(kind);
      control = await selected();
      assert.equal(control.value, loaded);
      assert.equal(control.label, friendly + ' · reverse');
      await close();
    }
    await js(`go('providers')`); await ready();
    assert.ok((await js(`document.querySelector('#content').textContent`)).includes(friendly + ' · reverse'), 'provider row uses the active strategy label');
    assert.ok((await js(`(()=>{const node=document.createElement('div');node.innerHTML=routingSummary(state.providers[0]);return node.textContent})()`)).includes(friendly + ' · reverse'), 'account summary uses the same label');
    await js(`go('routing')`); await ready();
    assert.ok((await js(`document.querySelector('#content').textContent`)).includes(friendly + ' · reverse'), 'group row uses the active strategy label');
    await js(`usageSection='routing';go('usage')`); await ready();
    await wait(`document.querySelectorAll('.route-record').length===2`);
    const labels = await js(`[...document.querySelectorAll('.route-record')].map(r=>[...r.querySelectorAll('dt')].find(d=>d.textContent==='路由方式').nextElementSibling.textContent)`);
    assert.ok(labels.some(label => label.includes(friendly + ' · reverse')));
    assert.ok(labels.some(label => label.includes(unloaded + '（暂未加载）')));

    // A later catalog refresh drops a loaded strategy; its saved provider and
    // group choice still survives another real submit and reopen.
    runtimeRoutings = [];
    await js(`load()`); await ready();
    for (const kind of ['provider', 'group']) {
      await open(kind);
      assert.equal((await selected()).value, loaded);
      assert.equal((await selected()).label, loaded + '（暂未加载）');
      assert.equal((await selected()).hintHidden, false);
      await js(`activateEditorSection(${JSON.stringify(kind === 'provider' ? 'connection' : 'members')})`);
      await fill('#f-name', 'Still saved ' + kind);
      await save(kind, loaded);
      await open(kind); assert.equal((await selected()).value, loaded); await close();
    }
    assert.equal(mutations.length, 6, 'only the six intended provider/group saves occur');
    assert.deepEqual(unexpected, []);
    assert.deepEqual(external, [], 'all assets and API requests stayed local');
    console.log('WHALEBRIDGE_RUNTIME_ROUTING_RESULT:PASS');
  } finally { win.destroy(); server.close(); }
})().then(() => app.exit(0), error => { console.error(error); server.close(); app.exit(1); });
