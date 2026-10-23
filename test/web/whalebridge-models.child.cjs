'use strict';
const { app, BrowserWindow, nativeTheme } = require('electron');
const { inspectAlignment } = require('../../scripts/lib/ui-alignment.cjs');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
app.setPath('userData', process.argv[2]);
const assets = path.resolve(__dirname, '../../internal/whalebridge/assets');
const names = new Set(fs.readdirSync(assets));
const rows = Array.from({ length: 48 }, (_, i) => ({ id: `channel/model-${String(i).padStart(2, '0')}`, name: `Model ${i}`, channelId: 'channel', channelName: 'QA Channel', supplierId: i < 24 ? 'openai' : 'anthropic', supplierName: i < 24 ? 'OpenAI' : 'Anthropic', context: 272000, efforts: ['low', 'high'] }));
rows.push({ id: 'other/keep', name: 'Keep', channelId: 'other', channelName: 'Other Channel', supplierId: 'other', supplierName: 'Other' });
const hidden = new Set(['other/keep']);
let reads = 0, fail = false;
const writes = [];
let providerFixture;
const providerWrites = [], importWrites = [], settingWrites = [], groupWrites = [];
let groupFixture;
const historyReads=[];
let quotaReads=0;
const fixturePrice = { input: 1, output: 5, cache_read: 0.25, cache_write: 1.25, cache_write_1h: 2, tiers: [{ above: 200000, input: 2, output: 7.5, cache_read: 0.5, cache_write: 2.5, cache_write_1h: 4 }] };
const subscriptionSettings = { pluginCheckins: { inactive: false }, pluginCheckinsEffective: { daily: true } };
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  const json = body => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(body)); };
  if (url.pathname === '/api/state') {
    reads++;
    return json({ version: 'test', providers: providerFixture ? [providerFixture] : [], presets: [], models: rows.filter(m => !hidden.has(m.id)), hidden: rows.filter(m => hidden.has(m.id)), groups: groupFixture ? (Array.isArray(groupFixture)?groupFixture:[groupFixture]) : [] });
  }
  if (url.pathname === '/api/group') {
    let text = ''; for await (const chunk of req) text += chunk;
    groupWrites.push(JSON.parse(text)); return json({ ok: true });
  }
  if (url.pathname === '/api/provider' || url.pathname === '/api/provider/import' || (url.pathname === '/api/subscription/settings' && req.method === 'POST')) {
    let text = ''; for await (const chunk of req) text += chunk;
    const body = JSON.parse(text);
    if (url.pathname === '/api/provider') { providerWrites.push(body); return json({ ok: true }); }
    if (url.pathname === '/api/subscription/settings') { settingWrites.push(body); return json({ ok: true }); }
    importWrites.push(body);
    return json(body.preview ? { providers: [{ id: 'imported', name: 'Imported API', chat: 'https://example.test/v1', models: ['long'], keySet: false, keyOptional: false }] } : { ok: true, added: ['imported'] });
  }
  if (url.pathname === '/api/provider/models') return json({provider:providerFixture});
  if (url.pathname === '/api/upstream') return json({ vendors: [], providers: {} });
  if (url.pathname === '/api/global') return json({settings:{},models:[],searchVendors:[],searches:[],fx:{rate:7},sync:{}});
  if (url.pathname === '/api/lanes') return json({});
  if (url.pathname === '/api/usage') return json({ calls: 0, models: [] });
  if (url.pathname === '/api/quotas') {quotaReads++;if(quotaReads===1)res.setHeader('X-Magpie-Reading','1');return json([]);}
  if (url.pathname === '/api/quotas/history') {historyReads.push(url.searchParams.get('days'));return json([{provider:'qa',user:'qa-user',lines:[{name:'Weekly window',points:[{at:new Date(Date.now()-86400000).toISOString(),left:80},{at:new Date().toISOString(),left:65}]}]}]);}
  if (url.pathname === '/api/keys/qa') return json(Array.from({length:7},(_,i)=>({id:'key-'+i,name:i===1?'Paused key':i===2?'Disabled key':'QA key '+i,masked:'qa-***-'+i,active:i===0,on:i!==2,...(i===1?{rest:{why:'429 fixture',until:'2099-01-01T00:00:00Z'}}:{})})));
  if (url.pathname === '/api/subscription/settings') return json(subscriptionSettings);
  if (url.pathname === '/api/subscriptions') return json([{ id: 'daily-adapter', pid: 'daily', name: 'Daily API', plugin: true, checkin: true }]);
  if (url.pathname === '/api/models/hidden') {
    let text = ''; for await (const chunk of req) text += chunk;
    const body = JSON.parse(text); writes.push(body);
    await new Promise(resolve => setTimeout(resolve, 60));
    if (fail) { res.writeHead(400); return res.end('QA save failed'); }
    for (const id of body.ids) { if (body.hidden) hidden.add(id); else hidden.delete(id); }
    return json({ ok: true });
  }
  const name = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
  if (!names.has(name)) { res.writeHead(404); return res.end(); }
  res.setHeader('Content-Type', ({ '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png' })[path.extname(name)]);
  res.end(fs.readFileSync(path.join(assets, name)));
});
async function run() {
  await app.whenReady();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const win = new BrowserWindow({ width: 1120, height: 800, show: false, webPreferences: { contextIsolation: true, nodeIntegration: false, backgroundThrottling: false } });
  win.webContents.on('console-message', (_event, _level, message) => console.error(message));
  const js = source => win.webContents.executeJavaScript(source).catch(error => { throw new Error(`${source}: ${error.message}`); });
  const wait = async expr => {
    const end = Date.now() + 5000;
    while (!await js(`Boolean(${expr})`)) {
      if (Date.now() > end) throw new Error(`DOM timeout: ${expr}`);
      await new Promise(resolve => setTimeout(resolve, 20));
    }
  };
  const click = selector => js(`document.querySelector(${JSON.stringify(selector)}).click()`);
  try {
    await win.loadURL(`http://127.0.0.1:${server.address().port}`);
    await wait(`document.querySelector('#content').getAttribute('aria-busy')==='false'`);
    await click('[data-tab="models"]');
    assert.equal(await js(`document.querySelectorAll('.model-channel').length`), 2);
    assert.equal(await js(`document.querySelectorAll('.model-supplier').length`), 3, 'every channel keeps its supplier divider, including single-supplier channels');
    assert.equal(await js(`document.querySelectorAll('input[type=checkbox]').length`), 0);
    const rowHeight = await js(`document.querySelector('.model-row').getBoundingClientRect().height`);
    assert.ok(rowHeight <= 76, `compact row height ${rowHeight}`);
    assert.equal(await js(`document.querySelector('.model-meta .caption').textContent`), '思考：low / high');
    const header = await js(`(() => {const toolbar=document.querySelector('.toolbar').getBoundingClientRect(),title=document.querySelector('.top').getBoundingClientRect(),list=document.querySelector('#model-list').getBoundingClientRect();return {toolbar:toolbar.y,title:title.y,list:list.y};})()`);
    assert.ok(header.toolbar <= 20 && header.title <= 80 && header.list <= 190, JSON.stringify(header));
    assert.equal(await js(`document.querySelector('#main').contains(document.querySelector('.toolbar'))`), false, 'the title row stays outside list scrolling');
    assert.ok(await js(`document.querySelector('#main').getBoundingClientRect().y>=48`), 'model switches cannot scroll into the native control strip');
    assert.equal(await js(`getComputedStyle(document.querySelector('.toolbar')).getPropertyValue('-webkit-app-region')`), 'drag', 'empty title-row space moves the window');
    assert.equal(await js(`getComputedStyle(document.querySelector('#refresh')).getPropertyValue('-webkit-app-region')`), 'no-drag', 'refresh remains clickable within the draggable title row');
    const before = await js(`(() => { document.querySelector('#main').scrollTop=900; globalThis.row=document.querySelector('[data-action="hide-model"][data-id="channel/model-12"]'); globalThis.search=document.querySelector('#model-search'); return {scroll:document.querySelector('#main').scrollTop,y:row.getBoundingClientRect().y}; })()`);
    await click('[data-action="hide-model"][data-id="channel/model-12"]');
    await wait(`!document.querySelector('[data-action="hide-model"][data-id="channel/model-12"]').disabled`);
    const after = await js(`({scroll:document.querySelector('#main').scrollTop,y:row.getBoundingClientRect().y,sameRow:row===document.querySelector('[data-id="channel/model-12"][data-action]'),sameSearch:search===document.querySelector('#model-search'),message:document.querySelector('#message').hidden})`);
    assert.equal(after.scroll, before.scroll); assert.equal(after.y, before.y);
    assert.ok(after.sameRow && after.sameSearch); assert.equal(after.message, false, 'single toggles confirm the saved model visibility'); assert.equal(reads, 1);
    assert.equal(await js(`document.querySelector('[data-scope="supplier"]').getAttribute('aria-checked')`), 'mixed');
    await js(`document.querySelector('[data-action="hide-model"][data-id="channel/model-13"]').click();document.querySelector('[data-action="hide-model"][data-id="channel/model-14"]').click()`);
    await wait(`visibilityPending.size===0`);
    assert.ok(hidden.has('channel/model-13') && hidden.has('channel/model-14'));
    // Supplier switch: mixed -> show all in that supplier only.
    await click('[data-scope="supplier"]'); await wait(`visibilityPending.size===0`);
    assert.equal(writes.at(-1).hidden, false); assert.equal(writes.at(-1).ids.length, 3); assert.ok(hidden.has('other/keep'));
    assert.equal(await js(`document.querySelector('[data-scope="supplier"]').getAttribute('aria-checked')`), 'true');
    // Search-scoped select all.
    await js(`(() => {const input=document.querySelector('#model-search');input.value='Model 1';input.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    assert.equal(await js(`document.querySelector('[data-count="all"]').textContent`), '已显示 11 / 11');
    assert.equal(await js(`document.querySelectorAll('.model-supplier').length`), 1, 'search keeps the remaining supplier label');
    assert.equal(await js(`document.querySelector('.model-supplier').textContent.includes('OpenAI')`), true);
    assert.equal(await js(`document.querySelector('[data-action="fold-channel"]').getAttribute('aria-expanded')`), 'true');
    await click('[data-action="fold-channel"]');
    assert.equal(await js(`document.querySelector('.model-channel-body').hidden`), true, 'search result collapses on the first click');
    await click('[data-action="fold-channel"]');
    await click('[data-scope="all"]'); await wait(`visibilityPending.size===0`);
    assert.equal(writes.at(-1).hidden, true); assert.equal(writes.at(-1).ids.length, 11); assert.ok(!hidden.has('channel/model-20'));
    await click('[data-scope="all"]'); await wait(`visibilityPending.size===0`);
    assert.equal(writes.at(-1).hidden, false); assert.equal(writes.at(-1).ids.length, 11); assert.ok(hidden.has('other/keep'));
    await js(`(() => {const input=document.querySelector('#model-search');input.value='';input.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    // Channel fold keeps other channels in place.
    await click('[data-action="fold-channel"][data-id="channel"]');
    assert.equal(await js(`document.querySelector('.model-channel-body').hidden`), true);
    await click('[data-action="fold-channel"][data-id="channel"]');
    fail = true;
    await click('[data-action="hide-model"][data-id="channel/model-20"]'); await wait(`visibilityPending.size===0`);
    assert.equal(await js(`document.querySelector('[data-action="hide-model"][data-id="channel/model-20"]').getAttribute('aria-checked')`), 'true');
    assert.equal(await js(`document.querySelector('#message-text').textContent`), 'QA save failed');
    assert.ok(await js(`(() => {
      const message = document.querySelector('#message');
      const rect = message.getBoundingClientRect();
      const scrollViewport = document.querySelector('#main').getBoundingClientRect();
      const close = message.querySelector('button').getBoundingClientRect();
      return !message.hidden && rect.width > 0 && rect.height > 0
        && rect.left >= 0 && rect.right <= innerWidth && rect.top >= scrollViewport.top && rect.bottom <= innerHeight
        && close.left >= rect.left && close.right <= rect.right
        && close.top >= rect.top && close.bottom <= rect.bottom;
    })()`), 'save errors remain visible in the feedback popover with a reachable close control');
    fail = false;
    await click('[data-filter="shown"]');
    await click('[data-action="hide-model"][data-id="channel/model-20"]'); await wait(`visibilityPending.size===0`);
    assert.equal(await js(`document.querySelector('[data-action="hide-model"][data-id="channel/model-20"]')`), null);
    win.setSize(700, 780);
    await js(`new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`);
    assert.equal(await js(`document.documentElement.scrollWidth<=window.innerWidth`), true);
    assert.ok(await js(`document.querySelector('.toolbar-actions').getBoundingClientRect().right<=window.innerWidth-112`), 'narrow desktop header clears window controls');
    win.setSize(390, 844);
    await js(`new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`);
    assert.equal(await js(`document.documentElement.scrollWidth<=window.innerWidth`), true);
    // Provider edits exercise the rendered form and its submitted API payload.
    providerFixture = { id: 'qa', name: 'QA API', chat: 'https://example.test/v1', keySet: true, keyMasked: 'qa…key', models: [], defaultModels: ['long'], modelCount: 1, available: [{ id: 'long', name: 'Long Context', price: fixturePrice, list: fixturePrice, ownPrice: true, efforts: ['low','high'], canFast: true }], modelPrices: { long: fixturePrice } };
    win.setSize(900, 780);
    await click('#refresh'); await wait(`!document.querySelector('#refresh').disabled`);
    await click('[data-tab="providers"]');
    await click('[data-action="edit-provider"][data-id="qa"]');
    await js(`activateEditorSection('models')`);
    await js(`(() => {const input=document.querySelector('[data-price="output"]');input.value='9';input.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    await js(`(() => {const input=document.querySelector('[data-price="input"]');input.value='';input.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    await js(`(() => {const input=document.querySelector('#provider-model-search');input.value='Long';input.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    assert.equal(await js(`document.querySelector('[data-price="output"]').value`), '9', 'redrawing the model list retains edited prices');
    assert.equal(await js(`document.querySelector('[data-price="input"]').value`), '', 'redrawing retains a blank official-price inheritance choice');
    await click('[data-action="add-price-tier"]');
    await click('[data-action="remove-price-tier"]');
    await click('#save'); await wait(`!document.querySelector('#editor').open`);
    assert.deepEqual(providerWrites.at(-1).modelPrefs.long.price, { ...fixturePrice, output: 9, tiers: [{ above: 300000, input: 1, output: 9, cache_read: 0.25, cache_write: 1.25, cache_write_1h: 2 }] }, 'editing one price and replacing a tier retains all five price components');
    assert.deepEqual(providerWrites.at(-1).models, [], 'editing default-model parameters keeps the default model selection');
    await click('[data-action="edit-provider"][data-id="qa"]'); await js(`activateEditorSection('models')`);
    await js(`(() => {const input=document.querySelector('[data-pref="name"]');input.value='Cancelled name';input.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    await click('#cancel-editor'); await wait(`!document.querySelector('#editor').open`);
    await click('[data-action="edit-provider"][data-id="qa"]'); await js(`activateEditorSection('models')`);
    assert.equal(await js(`document.querySelector('[data-pref="name"]').value`), '', 'cancel discards the model parameter draft');
    await click('#cancel-editor'); await wait(`!document.querySelector('#editor').open`);
    // The supplier model browser keeps long names readable and selection separate from editing.
    providerFixture = { ...providerFixture, models: [], defaultModels: ['long'], available: [providerFixture.available[0], ...Array.from({length:36},(_,i)=>({id:`vendor/model-${i}-long-identifier`,name:i===0?'可识别的模型名称 / Long readable model name':'Catalog Model '+i,context:128000,images:i%2===0,price:i===2?{input:0,output:0}:{input:0.5,output:2},...(i===2?{list:{input:0,output:0}}:{})}))] };
    await click('#refresh'); await wait(`!document.querySelector('#refresh').disabled`);
    await wait(`state.providers.find(p=>p.id==='qa')?.available.length===37`);
    await click('[data-action="edit-provider"][data-id="qa"]'); await js(`activateEditorSection('models')`);
    assert.equal(await js(`document.querySelectorAll('.provider-model-row').length`),37);
    await wait(`document.querySelector('[data-pref="api"]').parentElement.classList.contains('select-control')`);
    assert.deepEqual((await js(`(${inspectAlignment.toString()})()`)).issues.filter(i=>i.kind==='select-icon-center'),[],'model parameter arrows align with their actual select controls');
    assert.equal(await js(`document.querySelector('.provider-model-row strong').textContent`),'Long Context');
    assert.ok(await js(`(()=>{const e=document.querySelector('.provider-model-row strong'),s=getComputedStyle(e);return parseFloat(s.fontSize)>=14&&s.color===getComputedStyle(document.body).color;})()`),'model names use readable primary text');
    assert.ok(await js(`(()=>{const e=document.querySelector('#provider-model-list');return e.scrollHeight>e.clientHeight&&e.clientHeight<=360;})()`),'the catalog has a bounded scrolling viewport');
    await js(`(()=>{const n=document.querySelector('[data-pref="name"]');n.value='Draft kept';n.dispatchEvent(new Event('input',{bubbles:true}));const p=document.querySelector('[data-price="output"]');p.value='12';p.dispatchEvent(new Event('input',{bubbles:true}));globalThis.modelParameter=p;})()`);
    const filterModels = async (query,filter='all') => js(`(()=>{const q=document.querySelector('#provider-model-search'),f=document.querySelector('#provider-model-filter');q.value=${JSON.stringify(query)};f.value=${JSON.stringify(filter)};q.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    await filterModels('Long readable');
    assert.equal(await js(`document.querySelectorAll('.provider-model-row').length`),1);
    assert.equal(await js(`modelParameter===document.querySelector('[data-price="output"]')`),true,'searching leaves the price draft mounted');
    await click('[data-provider-model-pick="vendor/model-0-long-identifier"]');
    assert.equal(await js(`modelParameter===document.querySelector('[data-price="output"]')`),true,'selection leaves parameter fields mounted');
    await filterModels('', 'picked');
    assert.equal(await js(`document.querySelectorAll('.provider-model-row').length`),2);
    await filterModels('', 'free');
    assert.equal(await js(`document.querySelectorAll('.provider-model-row').length`),1);
    assert.match(await js(`document.querySelector('.provider-model-row').textContent`),/Catalog Model 2/);
    await filterModels('no matching model');
    assert.match(await js(`document.querySelector('.provider-model-empty').textContent`),/没有匹配/);
    await filterModels('');
    await click('[data-action="configure-provider-model"][data-id="vendor/model-0-long-identifier"]');
    assert.equal(await js(`document.querySelector('.model-setting').dataset.model`),'vendor/model-0-long-identifier');
    assert.deepEqual(await js(`configuredProviderModelIds()`),['long','vendor/model-0-long-identifier'],'opening parameters does not change selection');
    await click('[data-action="reset-model-pref"][data-id="vendor/model-0-long-identifier"]');
    assert.match(await js(`document.querySelector('[data-provider-model="vendor/model-0-long-identifier"] .provider-model-price').textContent`),/输入 \$0\.5输出 \$2/,'reset inherits the catalog price even without a separate list-price field');
    await click('[data-action="configure-provider-model"][data-id="long"]');
    assert.equal(await js(`document.querySelector('[data-price="output"]').value`),'12');
    assert.equal(await js(`document.querySelector('[data-pref="name"]').value`),'Draft kept','switching models retains parameter drafts');
    await click('[data-action="reset-model-pref"][data-id="long"]');
    assert.equal(await js(`document.querySelector('[data-pref="name"]').value`),'');
    assert.equal(await js(`document.querySelector('[data-pref="customPrice"]').checked`),false,'reset remounts the selected model defaults');
    providerFixture.available[0]={...providerFixture.available[0],price:{input:2,output:10},list:{input:2,output:10},ownPrice:false};
    await click('[data-action="fetch-edit-models"]');await wait(`!document.querySelector('[data-action="fetch-edit-models"]').disabled`);
    assert.equal(await js(`document.querySelector('[data-price="input"]').dataset.inherit`),'2','refreshing the active model updates inherited prices');
    await click('[data-pref="customPrice"]');
    await js(`(()=>{const p=document.querySelector('[data-price="output"]');p.value='12';p.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    assert.match(await js(`document.querySelector('[data-provider-model="long"] .provider-model-price').textContent`),/输入 \$2输出 \$12/,'the row price follows the live parameter draft');
    assert.equal(await js(`editor.modelPrefs.long.price.input`),2,'a blank input inherits the refreshed official price');
    await js(`(()=>{const p=document.querySelector('[data-price="input"]');p.value='-1';p.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    await filterModels('Long Context');
    assert.match(await js(`document.querySelector('[data-provider-model="long"] .provider-model-price').textContent`),/价格待完善/,'redrawing the list retains the invalid-price state');
    await js(`(()=>{const p=document.querySelector('[data-price="input"]');p.value='';p.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    await filterModels('');
    await click('[data-pref="customPrice"]');
    assert.match(await js(`document.querySelector('[data-provider-model="long"] .provider-model-price').textContent`),/输入 \$2输出 \$10/,'turning off the override restores the official row price');
    const screenshot=async name=>{if(!process.env.WHALEBRIDGE_QA_SCREENSHOTS)return;await js(`Promise.all(document.getAnimations().filter(a=>a.effect.getTiming().iterations!==Infinity).map(a=>a.finished))`);await js(`new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))`);fs.mkdirSync(process.env.WHALEBRIDGE_QA_SCREENSHOTS,{recursive:true});fs.writeFileSync(path.join(process.env.WHALEBRIDGE_QA_SCREENSHOTS,name+'.png'),(await win.webContents.capturePage()).toPNG());};
    win.setContentSize(1120,800); await wait(`innerWidth===1120`); await js(`document.querySelector('.editor-content').scrollTop=0`);
    await screenshot('provider-models-light');
    nativeTheme.themeSource='dark'; await wait(`document.documentElement.hasAttribute('data-ds-dark-theme')`); await screenshot('provider-models-dark');
    await click('[data-action="configure-provider-model"][data-id="long"]');await screenshot('provider-model-parameters-dark');await js(`document.querySelector('.editor-content').scrollTop=0`);
    win.setContentSize(390,844); await wait(`innerWidth===390`);
    assert.ok(await js(`(()=>{const dialog=document.querySelector('#editor'),content=document.querySelector('.editor-content');return dialog.scrollWidth<=dialog.clientWidth&&content.scrollWidth<=content.clientWidth;})()`),'the narrow model editor has no horizontal overflow');
    assert.ok(await js(`[...document.querySelectorAll('.provider-model-row')].every(row=>{const a=row.querySelector('label').getBoundingClientRect(),b=row.querySelector('button').getBoundingClientRect();return a.right<=b.left;})`),'long identities leave the parameter action clear');
    await screenshot('provider-models-narrow');
    nativeTheme.themeSource='light'; win.setContentSize(900,780); await wait(`innerWidth===900`);
    await click('#cancel-editor'); await wait(`!document.querySelector('#editor').open`);
    groupFixture = { id:'route',name:'QA route',members:['qa/long'],routing:'manual',pick:'qa/long',matched:[] };
    await click('#refresh'); await wait(`!document.querySelector('#refresh').disabled`); await click('[data-tab="routing"]');
    await click('[data-action="edit-group"][data-id="route"]');
    await js(`(() => {const input=document.querySelector('[data-member-action="effort"]');input.value='high';input.dispatchEvent(new Event('change',{bubbles:true}));})()`);
    await click('[data-member-action="fast"]');
    await js(`(() => {activateEditorSection('routing');document.querySelector('#f-routing').value='order';document.querySelector('#f-affinity').value='turn';document.querySelector('#f-sink').checked=true;activateEditorSection('conditions');document.querySelector('[data-action="group-rule-add"]').click();document.querySelector('[data-rule="tokens"]').value='64000';document.querySelector('[data-rule="images"]').checked=true;document.querySelector('[data-rule="intent"]').value='代码调试';document.querySelector('#f-classifier').value='qa/long';document.querySelector('#f-effort').value='auto';activateEditorSection('levels');document.querySelector('#f-contextMode').value='custom';document.querySelector('#f-contextMode').dispatchEvent(new Event('change'));document.querySelector('#f-contextValue').value='128k';document.querySelector('#f-levelsMode').value='own';document.querySelector('#f-levelsMode').dispatchEvent(new Event('change'));document.querySelector('[data-group-level="high"]').checked=true;})()`);
    await click('#save'); await wait(`!document.querySelector('#editor').open`);
    assert.deepEqual(groupWrites.at(-1).members, ['qa/long:high']);
    assert.equal(groupWrites.at(-1).pick, 'qa/long:high', 'fixed member level changes retain the manual pick');
    assert.deepEqual(groupWrites.at(-1).fast, ['qa/long:high']);
    assert.equal(groupWrites.at(-1).context,128000); assert.deepEqual(groupWrites.at(-1).levels,['high']);
    assert.equal(groupWrites.at(-1).classifier,'qa/long'); assert.equal(groupWrites.at(-1).effort,'auto');
    assert.equal(groupWrites.at(-1).affinity,'turn'); assert.equal(groupWrites.at(-1).sink,true);
    assert.deepEqual(groupWrites.at(-1).rules,[{use:'qa/long:high',tokens:64000,effort:'',images:true,compact:false,intent:'代码调试',agents:[],folders:[],exceptFolders:[],pause:false,time:null}]);
    // Saving closes the editor before its state reload completes. A click on
    // the still-disabled refresh button would not request the next fixture.
    await wait(`!document.querySelector('#refresh').disabled && document.querySelector('#content').getAttribute('aria-busy')==='false'`);
    groupFixture=[groupFixture,{id:'outer',name:'Outer route',members:['group/route']}];
    await click('#refresh'); await wait(`!document.querySelector('#refresh').disabled`);
    await click('[data-action="edit-group"][data-id="outer"]'); await click('[data-action="group-edit-inner"]');
    await wait(`document.querySelector('#confirmation').open`); await click('#accept-confirm');
    await wait(`editor.data.id==='route'`);
    assert.equal(await js(`!!editor.closed`),false,'nested-group navigation waits for the previous dialog close before opening the next editor');
    await click('#cancel-editor'); await wait(`!document.querySelector('#editor').open`);
    await click('[data-tab="providers"]');
    await js(`openKeys('qa')`); await wait(`document.querySelector('#f-keySearch')`);
    assert.equal(await js(`[...document.querySelectorAll('[data-key-row]')].filter(row=>!row.hidden).length`),5,'long key lists start folded at five visible identities');
    await click('[data-key-pick="key-0"]');
    await js(`document.querySelector('#f-keySearch').value='Paused key';document.querySelector('#f-keySearch').dispatchEvent(new Event('input',{bubbles:true}))`);
    assert.equal(await js(`[...document.querySelectorAll('[data-key-row]')].filter(row=>!row.hidden).length`),1);
    assert.equal(await js(`document.querySelector('[data-key-pick="key-0"]').checked`),false,'filtering out a selected key removes it from bulk deletion');
    await js(`document.querySelector('#f-keySearch').value='';document.querySelector('#f-keySearch').dispatchEvent(new Event('input',{bubbles:true}))`);
    await click('[data-action="key-fold"]');
    assert.equal(await js(`[...document.querySelectorAll('[data-key-row]')].filter(row=>!row.hidden).length`),7,'expanding exposes every key');
    await js(`document.querySelector('#f-keyStatus').value='off';document.querySelector('#f-keyStatus').dispatchEvent(new Event('change',{bubbles:true}))`);
    assert.deepEqual(await js(`[...document.querySelectorAll('[data-key-row]')].filter(row=>!row.hidden).map(row=>row.querySelector('.table-primary').textContent)`),['Disabled key']);
    await js(`document.querySelector('#f-keyStatus').value='rest';document.querySelector('#f-keyStatus').dispatchEvent(new Event('change',{bubbles:true}))`);
    assert.deepEqual(await js(`[...document.querySelectorAll('[data-key-row]')].filter(row=>!row.hidden).map(row=>row.querySelector('.table-primary').textContent)`),['Paused key']);
    await click('#cancel-editor'); await wait(`!document.querySelector('#editor').open`);
    await click('[data-action="import-provider"]');
    await js(`document.querySelector('#f-source').value='{"name":"Imported API","chat":"https://example.test/v1"}'`);
    await click('#save'); await wait(`document.querySelector('#f-importKey')`);
    assert.equal(importWrites.length, 1, 'preview does not save a supplier');
    assert.equal(importWrites[0].preview, true);
    await js(`document.querySelector('#f-importKey').value='qa-import-key'`);
    await click('#save'); await wait(`!document.querySelector('#editor').open`);
    assert.equal(importWrites.length, 2);
    assert.equal(importWrites[1].key, 'qa-import-key', 'the separate password field supplies credentials only after preview');
    assert.equal(importWrites[1].text, importWrites[0].text);
    await click('[data-tab="usage"]'); await wait(`document.querySelector('[data-global-section="quotas"]')`);
    await click('[data-global-section="quotas"]'); await wait(`document.querySelector('[data-action="subscription-settings"]')`);
    await wait(`document.querySelector('#quota-section').getAttribute('aria-busy')==='false'`);
    assert.ok(quotaReads>=2,'the X-Magpie-Reading response follows background quota reads until they finish');
    await click('[data-action="quota-history"]'); await wait(`document.querySelectorAll('[data-history-point]').length===2`);
    assert.equal(historyReads.at(-1),'35','history starts with the upstream 35-day range');
    assert.equal(await js(`document.querySelector('.quota-history-line svg .trend-line').tagName.toLowerCase()`),'polyline');
    await js(`document.querySelector('[data-history-point]').dispatchEvent(new Event('pointerenter'))`);
    assert.ok(await js(`document.querySelector('[data-history-readout]').textContent.includes('80.0%')`),'hover updates the selected quota readout');
    await js(`document.querySelector('#f-historyDays').value='7';document.querySelector('#f-historyDays').dispatchEvent(new Event('change'))`);
    await wait(`document.querySelector('#quota-history-chart').getAttribute('aria-busy')==='false'`);
    assert.equal(historyReads.at(-1),'7');
    await click('#cancel-editor'); await wait(`!document.querySelector('#editor').open`);
    await click('[data-action="subscription-settings"]'); await wait(`document.querySelector('[data-plugin-checkin="daily"]')`);
    assert.equal(await js(`document.querySelector('[data-plugin-checkin="daily"]').checked`), true, 'the adapter default comes from the effective setting');
    await click('#save'); await wait(`!document.querySelector('#editor').open`);
    assert.equal(settingWrites.at(-1).pluginCheckins.daily, true, 'saving other settings preserves the effective adapter check-in default');
    assert.equal(settingWrites.at(-1).pluginCheckins.inactive, false, 'saving does not erase the preference of a temporarily disabled adapter');
    console.log('WHALEBRIDGE_MODELS_RESULT:PASS grouped models, scoped visibility, stable position, pricing tiers, import preview and subscription settings');
  } finally { win.destroy(); await new Promise(resolve => server.close(resolve)); }
}
run().then(() => app.exit(0), error => { console.error(error); app.exit(1); });
