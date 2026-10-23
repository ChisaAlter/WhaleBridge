'use strict';
const { app, BrowserWindow, nativeTheme } = require('electron');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
app.setPath('userData', process.argv[2]);
const assets = path.resolve(__dirname, '../../internal/whalebridge/assets');
const names = new Set(fs.readdirSync(assets));
const fixture = {
 id:'relay',name:'Fixture Relay',chat:'https://fixture.test/v1',models:['claude-only'],chosen:['claude-only'],defaultModels:['chat-only','unknown'],speaks:['chat'],
 available:[{id:'claude-only',name:'Claude (Messages only)',auto:['anthropic'],noAPI:true,efforts:['low','high'],price:{input:1,output:5}},{id:'chat-only',name:'Chat model',auto:['chat'],free:true,efforts:['low','high'],serviceTiers:['priority']},{id:'unknown',name:'Model with unknown API'}]
};
let providerFixture=structuredClone(fixture),unmoved=false,installed=false,dropUnselected=false,newCatalog=false,ruleAdd=false;
const writes=[];
const subscriptions=()=>installed?[{id:'cursor',pid:'cursor',name:'Cursor',retired:true,plugin:true,methods:[{type:'api',label:'Use native login',keyless:true},{type:'api',label:'API key'}]}]:[{id:'cursor',pid:'cursor',name:'Cursor',retired:true,plugin:false,package:'@fixture/cursor'}];
const server=http.createServer(async(req,res)=>{
 const url=new URL(req.url,'http://localhost');
 const json=body=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(body));};
 if(url.pathname.startsWith('/api/')){
  let text='';for await(const chunk of req)text+=chunk;const body=text?JSON.parse(text):undefined;
  writes.push({path:url.pathname,body});
  if(url.pathname==='/api/state')return json({version:'test',providers:[providerFixture],presets:[],models:[],hidden:[],groups:[],unmoved:unmoved?[{id:'cursor',name:'Cursor',package:'@fixture/cursor',accounts:1,why:{code:'native'},error:'Native login stays at source'}]:[]});
  if(url.pathname==='/api/subscriptions')return json(subscriptions());
  if(url.pathname==='/api/subscription/adapter'){installed=true;return json({ok:true});}
  if(url.pathname==='/api/subscription/prompt')return json({prompt:null,inputs:body.inputs || {}});
  if(url.pathname==='/api/signin')return json({state:'done'});
  if(url.pathname==='/api/subscription/adapters')return json({installed:[],available:[],moves:[{id:'cursor',package:'@fixture/cursor',moved:true,retired:true,state:'plugin'},{id:'zed',package:'@fixture/zed',signedIn:true,accounts:1,retired:true,nativeSkipped:true,canMove:false,error:'Native login stays at source',why:{code:'native'}}]});
  if(url.pathname==='/api/provider/models'){
   if(ruleAdd){const ruled={...providerFixture,models:['a','new-rule','new-messages'],chosen:['a','new-rule','new-messages'],delisted:[],available:[{id:'a',name:'A',auto:['chat']},{id:'b',name:'B',auto:['chat']},{id:'new-rule',name:'New rule match',auto:['chat']},{id:'new-messages',name:'Incompatible rule match',auto:['anthropic']}]};providerFixture=ruled;return json({provider:ruled,dropped:[],taken:['new-rule','new-messages'],count:4});}
    if(newCatalog){const expanded={...providerFixture,models:['a','b'],chosen:['a','b','new','messages'],delisted:[],available:[{id:'a',name:'A',auto:['chat']},{id:'b',name:'B',auto:['chat']},{id:'new',name:'New',auto:['chat']},{id:'messages',name:'Messages',auto:['anthropic']}]};providerFixture=expanded;return json({provider:expanded,dropped:[],count:4});}
   if(dropUnselected){const shorter={...providerFixture,models:['a'],chosen:['a'],delisted:['b'],available:[{id:'a',name:'A',auto:['chat']}]};providerFixture=shorter;return json({provider:shorter,dropped:['b'],count:1});}
   const restored={...providerFixture,delisted:[],models:['returned'],chosen:['returned'],available:[{id:'returned',name:'Restored model',auto:['chat']},{id:'available',name:'Unselected model',auto:['chat']}]};
   providerFixture=restored;return json({provider:restored,dropped:[],count:2});
  }
  if(url.pathname==='/api/provider'||url.pathname==='/api/provider/save')return json({ok:true,id:body.id});
  if(url.pathname==='/api/quotas')return json([]);
  if(url.pathname==='/api/upstream')return json({providers:{},vendors:[]});
  if(url.pathname==='/api/lanes')return json({});
  if(url.pathname==='/api/global')return json({settings:{},models:[],searchVendors:[],searches:[],fx:{rate:7},sync:{}});
  return json({ok:true});
 }
 const file=url.pathname==='/'?'index.html':decodeURIComponent(url.pathname.slice(1));
 if(!names.has(file)){res.writeHead(404);return res.end();}
 res.setHeader('Content-Type',({'js':'text/javascript','css':'text/css','html':'text/html','svg':'image/svg+xml'})[file.split('.').at(-1)] || 'application/octet-stream');res.end(fs.readFileSync(path.join(assets,file)));
});
(async()=>{
 await app.whenReady();await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const win=new BrowserWindow({width:1120,height:820,show:false,webPreferences:{contextIsolation:true,nodeIntegration:false,backgroundThrottling:false}});
 const js=source=>win.webContents.executeJavaScript(source);
 const click=selector=>js(`document.querySelector(${JSON.stringify(selector)}).click()`);
 const wait=async expr=>{const end=Date.now()+5000;while(!await js(`Boolean(${expr})`)){if(Date.now()>end)throw Error('DOM timeout '+expr);await new Promise(r=>setTimeout(r,20));}};
 try{
  await win.loadURL(`http://127.0.0.1:${server.address().port}`);await wait(`document.querySelector('#content').getAttribute('aria-busy')==='false'`);
  await js(`openProvider('relay');activateEditorSection('models')`);
  assert.equal(await js(`document.querySelector('[data-provider-model-pick="claude-only"]').checked`),true,'old unusable choice stays selected');
  assert.match(await js(`document.querySelector('[data-provider-model="claude-only"]').textContent`),/缺少兼容协议地址/);
  await click('[data-action="models-all"]');
  assert.deepEqual(await js(`lines(document.querySelector('#f-models').value)`),['claude-only','chat-only','unknown'],'all selects usable while retaining old incompatible pick');
  await click('[data-action="models-none"]');await click('[data-action="models-all"]');
  assert.deepEqual(await js(`lines(document.querySelector('#f-models').value)`),['chat-only','unknown'],'fresh all skips incompatible model');
  await js(`document.querySelector('#f-anthropic').value='https://fixture.test/messages';document.querySelector('#f-anthropic').dispatchEvent(new Event('input',{bubbles:true}))`);
  assert.equal(await js(`document.querySelector('[data-provider-model="claude-only"]').classList.contains('no-api')`),false,'unsaved endpoint is used immediately');
  await click('[data-action="models-all"]');assert.equal(await js(`document.querySelector('[data-provider-model-pick="claude-only"]').checked`),true);
  await js(`document.querySelector('#f-anthropic').value='';document.querySelector('#f-anthropic').dispatchEvent(new Event('input',{bubbles:true}))`);
  await click('[data-action="configure-provider-model"][data-id="claude-only"]');
  await js(`document.querySelector('[data-pref="api"]').value='chat';document.querySelector('[data-pref="api"]').dispatchEvent(new Event('input',{bubbles:true}))`);
  assert.equal(await js(`document.querySelector('[data-provider-model="claude-only"]').classList.contains('no-api')`),false,'API override draft wins');
  await click('[data-pref="reasonless"]');assert.equal(await js(`document.querySelector('[data-pref="efforts"]').disabled`),true);
  await js(`document.querySelector('[data-pref="name"]').value='Keep draft';document.querySelector('[data-pref="name"]').dispatchEvent(new Event('input',{bubbles:true}));document.querySelector('#provider-model-search').value='Chat';document.querySelector('#provider-model-search').dispatchEvent(new Event('input',{bubbles:true}))`);
  assert.equal(await js(`editor.modelPrefs['claude-only'].reasonless`),true);
  await js(`document.querySelector('#provider-model-search').value='';document.querySelector('#provider-model-search').dispatchEvent(new Event('input',{bubbles:true}))`);
  await click('[data-action="configure-provider-model"][data-id="claude-only"]');
  assert.equal(await js(`document.querySelector('[data-pref="reasonless"]').checked`),true);
  await click('#save');await wait(`!document.querySelector('#editor').open`);
  const saved=writes.findLast(x=>x.path==='/api/provider').body;
  assert.equal(saved.modelPrefs['claude-only'].reasonless,true);assert.equal(saved.modelPrefs['claude-only'].name,'Keep draft');
  providerFixture={id:'relay',name:'Waiting Relay',chat:'https://fixture.test/v1',models:[],chosen:[],delisted:['returned'],defaultModels:['available'],available:[{id:'available',name:'Unselected model',auto:['chat']}]};
  await js(`load()`);await js(`openProvider('relay');activateEditorSection('models')`);
  assert.equal(await js(`configuredProviderModelIds().length`),0,'all waiting does not become default selection');
  assert.match(await js(`document.querySelector('#model-selection-summary').textContent`),/暂时下架/);
  assert.equal(await js(`document.querySelector('[data-provider-model-pick="returned"]').disabled`),true);
  await click('#save');await wait(`!document.querySelector('#editor').open`);
  assert.equal(writes.findLast(x=>x.path==='/api/provider').body.clearDelisted,false,'unrelated save preserves waiting picks');
  await js(`openProvider('relay');activateEditorSection('models')`);await click('[data-action="fetch-edit-models"]');await wait(`document.querySelector('[data-provider-model-pick="returned"]')?.checked`);
  assert.equal(await js(`editor.delisted.length`),0,'refresh merges restored server choice');
  providerFixture={id:'relay',name:'Waiting Relay',chat:'https://fixture.test/v1',models:[],chosen:[],delisted:['returned'],defaultModels:['available'],available:[{id:'available',name:'Unselected model',auto:['chat']}]};
  await js(`document.querySelector('#editor').close();load()`);await js(`openProvider('relay');activateEditorSection('models')`);await click('[data-action="models-default"]');await click('#save');await wait(`!document.querySelector('#editor').open`);
  assert.equal(writes.findLast(x=>x.path==='/api/provider').body.clearDelisted,true,'explicit defaults clears waiting picks');
  providerFixture={id:'relay',name:'Edited choices',chat:'https://fixture.test/v1',models:['a','b'],chosen:['a','b'],available:[{id:'a',name:'A',auto:['chat']},{id:'b',name:'B',auto:['chat']}]};
  await js(`load()`);await js(`openProvider('relay');activateEditorSection('models')`);
  await click('[data-provider-model-pick="b"]');dropUnselected=true;
  await click('[data-action="fetch-edit-models"]');await wait(`editor.removeDelisted?.includes('b')`);
  assert.deepEqual(await js(`editor.delisted`),[],'refresh must not restore a choice unticked in this draft');
  await click('#save');await wait(`!document.querySelector('#editor').open`);
  assert.deepEqual(writes.findLast(x=>x.path==='/api/provider').body.removeDelisted,['b'],'only discarded waiting choices are removed on save');
  providerFixture={id:'relay',name:'All selected',chat:'https://fixture.test/v1',models:['a','b'],chosen:['a','b'],pickedFrom:['a','b'],available:[{id:'a',name:'A',auto:['chat']},{id:'b',name:'B',auto:['chat']}]};
  await js(`load()`);await js(`openProvider('relay');activateEditorSection('models')`);newCatalog=true;
  await click('[data-action="fetch-edit-models"]');await wait(`editor.available.length===4`);
  assert.deepEqual(await js(`lines(document.querySelector('#f-models').value)`),['a','b','new'],'all-compatible selection accepts new usable model and leaves incompatible one out');
  await click('#save');await wait(`!document.querySelector('#editor').open`);
  assert.deepEqual(writes.findLast(x=>x.path==='/api/provider').body.models,['a','b','new']);
  providerFixture={id:'relay',name:'Rule selection',chat:'https://fixture.test/v1',models:['a'],chosen:['a'],pickedFrom:['a','b'],autoInclude:['new-*'],available:[{id:'a',name:'A',auto:['chat']},{id:'b',name:'B',auto:['chat']}]};
   await js(`load()`);await js(`openProvider('relay');activateEditorSection('models')`);ruleAdd=true;
   await click('[data-action="fetch-edit-models"]');await wait(`editor.available.length===4`);
   assert.deepEqual(await js(`lines(document.querySelector('#f-models').value)`),['a','new-rule'],'partial selection takes new rule matches but keeps existing deselections and skips incompatible protocols');
   await click('#save');await wait(`!document.querySelector('#editor').open`);
   assert.deepEqual(writes.findLast(x=>x.path==='/api/provider').body.models,['a','new-rule'],'saving the refreshed draft retains rule additions');
   await js(`openProvider('relay');activateEditorSection('models')`);await click('[data-action="models-none"]');
   await click('[data-action="fetch-edit-models"]');await wait(`!document.querySelector('[data-action="fetch-edit-models"]').disabled`);
   assert.equal(await js(`editor.modelSelection`),'none','rule additions do not undo an unsaved all-none choice');
   assert.deepEqual(await js(`configuredProviderModelIds()`),[]);
   await click('#save');await wait(`!document.querySelector('#editor').open`);
   assert.equal(writes.findLast(x=>x.path==='/api/provider').body.noModels,true);
   ruleAdd=false;
   unmoved=true;await js(`load()`);await js(`tab='providers';render()`);assert.match(await js(`document.querySelector('#content').textContent`),/自动迁移已跳过/);
  await js(`openSubscription('cursor')`);assert.equal(await js(`document.querySelector('#save').hidden`),true,'retired placeholder cannot call builtin login');
  await click('[data-action="install-adapter"]');await wait(`document.querySelector('#f-method')`);
  assert.equal(await js(`document.querySelector('#f-key')===null`),true,'keyless method asks for no key');
  await click('#save');await wait(`editor?.type==='connection-success'`);
  const sign=writes.findLast(x=>x.path==='/api/signin').body;assert.equal(sign.plugin,true);assert.equal(sign.method,0);assert.ok(!sign.key);
  await js(`openAdapters('moves')`);assert.equal(await js(`document.querySelector('[data-action="adapter-moveback"]')===null`),true,'retired builtin is not offered as rollback');
  for(const width of [1120,390]){win.setSize(width,820);nativeTheme.themeSource=width===390?'dark':'light';await new Promise(r=>setTimeout(r,80));const layout=await js(`({page:document.documentElement.scrollWidth<=innerWidth+1,dialog:document.querySelector('#editor').scrollWidth<=document.querySelector('#editor').clientWidth+1})`);assert.ok(layout.page&&layout.dialog,JSON.stringify(layout));}
  console.log('WHALEBRIDGE_CATALOG_RESULT:PASS');
 }finally{win.destroy();server.close();}
})().then(()=>app.exit(0),error=>{console.error(error);server.close();app.exit(1)});
