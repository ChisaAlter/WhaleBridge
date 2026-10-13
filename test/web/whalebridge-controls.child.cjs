'use strict';
const { app, BrowserWindow, nativeTheme } = require('electron');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
app.disableHardwareAcceleration();
app.setPath('userData', process.argv[2]);
app.on('window-all-closed', () => {});
const assets = path.resolve(__dirname, '../../internal/whalebridge/assets');
const calls = [];
let installed = false, releaseInstall, releaseStage, releaseCatalog, catalogHeld = false, failing = false;
let globalSettings = {}, modelPrices = {'*/fixture-b':{input:11,output:12,cache_read:13,cache_write:14,cache_write_1h:15,tiers:[{above:65536,input:21,output:22,cache_read:23,cache_write:24,cache_write_1h:25}]}}, syncSettings = {};
let heldPath, releaseRequest, rejectRequest=false, failureText='requested operation failed', loginStatus="installing";
const loginIds=[];
const provider = { id:'qa', name:'QA supplier', modelCount:1, models:['qa-model'], chat:'http://127.0.0.1:9999/v1' };
const sourcePresets=[
 {id:'deepseek',name:'DeepSeek',kind:'vendor',chat:'https://api.deepseek.com',note:'官方 API'},
 {id:'clinepass',name:'ClinePass',kind:'relay',chat:'https://api.cline.bot/api/v1',note:'open coding models, $9.99/month',only:'cline-pass/',models:['cline-pass/qa-model']},
 {id:'qa-plan',name:'QA Cloud',kind:'vendor',regions:[{id:'api',name:'Pay as you go',chat:'https://qa.invalid/v1'},{id:'coding',name:'Coding Plan',chat:'https://qa.invalid/coding/v1'}]},
 {id:'google-vertex',name:'Google Vertex AI',kind:'vendor',note:'Google Cloud project and credentials',headerHints:['X-Vertex-AI-LLM-Request-Type','X-Vertex-AI-LLM-Shared-Request-Type']},
 {id:'qa-local',name:'Local QA model service',kind:'local',noKey:true,note:'运行在本机的模型服务'},
 {id:'qa-sponsored',name:'QA upstream sponsor',kind:'partner',chat:'https://sponsor.invalid/v1',notes:{zh:'中文赞助目录说明',en:'Sponsored vendor note'}},
];
const catalog = () => installed
 ? [{id:'cursor-adapter',pid:'cursor',name:'Cursor',plugin:true,methods:[{type:'oauth',label:'OAuth'}]}]
 : [{id:'claude',name:'Claude'},{id:'cursor',name:'Cursor',package:'@fixture/cursor'}];
const server = http.createServer(async (req,res) => {
 const u = new URL(req.url,'http://127.0.0.1'), json = value => { res.writeHead(200,{'Content-Type':'application/json'});res.end(JSON.stringify(value)); };
 if (!u.pathname.startsWith('/api/')) {
  const name = u.pathname==='/'?'index.html':u.pathname.slice(1);
  if (!fs.readdirSync(assets).includes(name)) { res.writeHead(404);res.end();return; }
  res.setHeader('Content-Type', {'.js':'text/javascript','.css':'text/css','.svg':'image/svg+xml','.html':'text/html'}[path.extname(name)]||'application/octet-stream');
  res.end(fs.readFileSync(path.join(assets,name)));return;
 }
 let bytes='';for await (const chunk of req)bytes+=chunk;
 const body=bytes?JSON.parse(bytes):undefined;calls.push({path:u.pathname,body});
 if(u.pathname===heldPath){await new Promise(resolve=>releaseRequest=resolve);if(rejectRequest){res.writeHead(500);res.end(failureText);return;}}
 if(u.pathname==='/api/state')return json({version:'controls-test',providers:[provider,{...provider,id:'qa2',name:'Second supplier',account:true}],models:[{id:'qa/qa-model',name:'QA'}],hidden:[],groups:[{id:'qa-group',name:'QA group',members:['qa/qa-model'],routing:'order'}],presets:sourcePresets});
 if(u.pathname==='/api/provider/presets')return json(sourcePresets);
 if(u.pathname==='/api/subscriptions') { if(catalogHeld){releaseCatalog=()=>json(catalog());return;}return json(catalog()); }
 if(u.pathname==='/api/subscription/adapter') {
  assert.equal(req.headers.accept,'application/x-ndjson');res.writeHead(200,{'Content-Type':'application/x-ndjson'});
  res.write(JSON.stringify({phase:'downloading'})+'\n');releaseStage=()=>res.write(JSON.stringify({phase:'installing'})+'\n');
  releaseInstall=()=>{if(failing)res.end(JSON.stringify({error:'installation rejected'})+'\n');else{installed=true;res.end(JSON.stringify({ok:true})+'\n');}};return;
 }
 if(u.pathname==='/api/subscription/adapters')return json({installed:[{id:'qa-adapter',package:'@fixture/qa',name:'QA adapter',version:'1.0.0',enabled:true}],available:[],moves:[],bun:true});
 if(u.pathname==='/api/subscription/adapters/page')return json({readme:'QA usage instructions'});
 if(u.pathname==='/api/subscription/adapters/check')return json({plugins:[{package:'@fixture/qa',status:'unknown',error:'registry offline'}]});
 if(u.pathname==='/api/signin'){const id=loginIds.length?'fixture-login-'+(loginIds.length+1):'fixture-login';loginIds.push(id);return json({id,state:loginStatus,url:loginStatus==='waiting'?'https://example.com/login':undefined});}
 if(/^\/api\/signin\/[^/]+$/.test(u.pathname))return json({id:u.pathname.split('/').at(-1),state:loginStatus,error:'authorization rejected',url:loginStatus==='waiting'?'https://example.com/login':undefined});
 if(/^\/api\/signin\/[^/]+\/cancel$/.test(u.pathname))return json({ok:true});
 if(u.pathname==='/api/accounts/qa2')return json([{user:'Fixture account',active:true}]);
 if(u.pathname==='/api/accounts/qa2/usage')return json({'Fixture account':{balance:'15'}});
 if(u.pathname==='/api/usage')return json({});
 if(u.pathname==='/api/routes')return json({routes:[]});
 if(u.pathname==='/api/routes/history')return json({days:[{day:'2026-10-08'}],routes:[]});
 if(u.pathname==='/api/subscription/settings')return json({});
 if(u.pathname==='/api/global')return json({settings:globalSettings,modelPrices,sync:syncSettings});
 if(u.pathname==='/api/global/settings'){Object.assign(globalSettings,body);return json({ok:true});}
 if(u.pathname==='/api/model-price'){modelPrices[body.id]=body.price;return json({ok:true});}
 if(u.pathname==='/api/sync/save'){syncSettings={...body,on:true};return json({ok:true});}
 if(u.pathname==='/api/sync/auto'){syncSettings.autoEvery=body.minutes;return json({ok:true});}
 if(u.pathname==='/api/sync/now')return json({ok:true});
 if(u.pathname==='/api/quotas')return json([{provider:'codex',name:'Fixture Codex',user:'Fixture Codex account',windows:[]}]);
 if(u.pathname==='/api/accounts/settings')return json({ok:true});
 if(u.pathname==='/api/quotas/history')return json([]);
 if(u.pathname==='/api/provider/test')return json({results:[{ok:true,protocol:'chat',model:'qa-model',ms:3}]});
 if(u.pathname==='/api/provider/live')return json({providers:[]});
 if(u.pathname==='/api/subscriptions/alerts')return json({sequence:0,alerts:[]});
 if(u.pathname==='/api/provider'||u.pathname==='/api/provider/delete'||u.pathname==='/api/provider/fetch'||u.pathname==='/api/provider/order')return json({ok:true});
 res.writeHead(404);res.end('unknown fixture route');
});
async function run(){
 await app.whenReady();await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const win=new BrowserWindow({width:1120,height:780,show:false,webPreferences:{contextIsolation:true,nodeIntegration:false,backgroundThrottling:false}});
 win.showInactive();
 win.webContents.on('console-message',details=>{if(details.level==='error')console.error(details.message);});
 const js=async s=>{try{return await win.webContents.executeJavaScript(s);}catch(error){throw Error(`${error.message}\nCommand: ${s}`);}},click=s=>js(`document.querySelector(${JSON.stringify(s)}).click()`);
 const wait=async s=>{const end=Date.now()+7000;while(Date.now()<end){if(await js(`Boolean(${s})`))return;await new Promise(r=>setTimeout(r,20));}throw Error('Unsettled: '+s);};
 const screenshot=async name=>{if(!process.env.WHALEBRIDGE_QA_SCREENSHOTS)return;await js(`Promise.all(document.getAnimations().filter(a=>a.effect.getTiming().iterations!==Infinity).map(a=>a.finished))`);await js(`new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))`);fs.mkdirSync(process.env.WHALEBRIDGE_QA_SCREENSHOTS,{recursive:true});fs.writeFileSync(path.join(process.env.WHALEBRIDGE_QA_SCREENSHOTS,name+'.png'),(await win.webContents.capturePage()).toPNG());};
 const resize=async(width,height)=>{win.setContentSize(width,height);try{await wait(`innerWidth===${width}&&innerHeight===${height}`);}catch(error){const observed={requested:{width,height},contentBounds:win.getContentBounds(),windowBounds:win.getBounds(),renderer:await js(`({width:innerWidth,height:innerHeight,dpr:devicePixelRatio})`)};if(process.env.WHALEBRIDGE_QA_SCREENSHOTS){fs.writeFileSync(path.join(process.env.WHALEBRIDGE_QA_SCREENSHOTS,'resize-unsettled.json'),JSON.stringify(observed,null,2));await screenshot('resize-unsettled');}throw Error(error.message+' '+JSON.stringify(observed));}};
 const close=()=>js(`new Promise(r=>{const d=document.querySelector('#editor');d.addEventListener('close',r,{once:true});d.close();})`);
 const addProvider=async()=>{await click('[data-action="add-provider"]');await wait(`document.querySelector('#editor').open&&document.querySelector('[data-source-command="custom"]')`);await click('[data-source-command="custom"]');await wait(`editor?.type==='provider'&&!editor.closed`);};
 const subscribe=async()=>{releaseInstall=undefined;await click('[data-action="subscription"]');await wait(`document.querySelector('#editor').open&&!editor.loading&&document.querySelector('[data-source-key="subscription:cursor"]')`);await click('[data-source-key="subscription:cursor"]');await wait(`editor?.type==='subscription'&&!editor.closed&&document.querySelector('#f-agent option[value="cursor"]')`);};
 const installStarted=async()=>{const end=Date.now()+7000;while(!releaseInstall&&Date.now()<end)await new Promise(r=>setTimeout(r,20));assert.ok(releaseInstall,'installation request reached backend');};
 try{
  await win.loadURL(`http://127.0.0.1:${server.address().port}/`);await wait(`document.querySelector('#content').getAttribute('aria-busy')==='false'`);
  await js(`(()=>{const actual=api;window.__qaApiSettled=Object.create(null);api=async function(...args){const key='/api/'+args[0].split('?')[0],row=window.__qaApiSettled[key]??={fulfilled:0,rejected:0,settled:0};try{const result=await actual.apply(this,args);row.fulfilled++;return result;}catch(error){row.rejected++;throw error;}finally{row.settled++;}};})()`);
  const focused=['ownership-and-messages','messages-only'].includes(process.env.WHALEBRIDGE_QA_FOCUS),messagesOnly=process.env.WHALEBRIDGE_QA_FOCUS==='messages-only';
  if(!focused){
   const writesBefore=calls.filter(c=>c.body!==undefined).length;
   const search=async value=>{await js(`(()=>{const q=document.querySelector('#source-search');q.value=${JSON.stringify(value)};q.dispatchEvent(new Event('input',{bubbles:true}));})()`);};
   const scope=async value=>{await js(`(()=>{const s=document.querySelector('#source-scope');s.value=${JSON.stringify(value)};s.dispatchEvent(new Event('change',{bubbles:true}));})()`);};
   await click('[data-action="add-provider"]');await wait(`editor?.type==='sources'&&!editor.loading`);
   assert.equal(await js(`document.querySelector('#source-scope').value`),'all');
   assert.equal(await js(`document.querySelector('#save').hidden`),true);
   assert.equal(await js(`document.activeElement.id`),'source-search');
   assert.equal(await js(`document.querySelectorAll('[data-source-pick]').length`),sourcePresets.length+catalog().length,'all actual catalog entries are reachable');
   await search('中文赞助');assert.equal(await js(`document.querySelectorAll('[data-source-pick]').length`),1);assert.match(await js(`document.querySelector('#source-results').textContent`),/上游赞助供应商.*由供应商赞助的目录条目.*中文赞助目录说明/s);
   await search('Sponsored vendor note');assert.equal(await js(`document.querySelectorAll('[data-source-pick]').length`),1,'English note is searchable while Chinese is displayed');
   await search('本机');assert.equal(await js(`document.querySelectorAll('[data-source-pick]').length`),1);assert.match(await js(`document.querySelector('#source-results').textContent`),/API 密钥可选/);
   await search('nothing-matches-this-source');assert.match(await js(`document.querySelector('#source-results').textContent`),/没有匹配.*自定义接口.*适配器/s);
   assert.equal(await js(`document.activeElement.id`),'source-search','typing preserves keyboard focus');
   await search('ClinePass');await js(`document.querySelector('#source-search').dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true,cancelable:true}))`);assert.equal(await js(`document.activeElement.dataset.sourceKey`),'preset:clinepass');
   await click('[data-source-key="preset:clinepass"]');await wait(`editor?.type==='provider'`);
   assert.equal(await js(`document.querySelector('#f-preset').value`),'clinepass');assert.equal(await js(`document.querySelector('#f-chat').value`),'https://api.cline.bot/api/v1');assert.equal(await js(`document.querySelector('#f-key').disabled`),false);assert.equal(await js(`document.querySelector('#f-agent')`),null,'a ClinePass key does not open a pretend OAuth login');
   await click('[data-source-command="back"]');await wait(`editor?.type==='sources'&&!editor.loading`);assert.equal(await js(`document.querySelector('#source-search').value`),'ClinePass');
   await search('Vertex');assert.match(await js(`document.querySelector('#source-results').textContent`),/Google Cloud 凭据/);await click('[data-source-key="preset:google-vertex"]');await wait(`editor?.type==='provider'`);
   assert.equal(await js(`document.querySelector('#f-vertexProject').required&&!document.querySelector('#f-vertexProject').disabled`),true);assert.equal(await js(`document.querySelector('#f-vertexLocation').value`),'global');assert.equal(await js(`document.querySelector('#f-key').disabled&&document.querySelector('#provider-api-auth').hidden`),true);
   await click('#save');assert.equal(await js(`document.querySelector('#f-vertexProject').checkValidity()`),false,'a project is required before saving Vertex');
   await js(`document.querySelector('#f-vertexProject').value='qa-vertex-project';document.querySelector('#f-vertexLocation').value='us-central1';document.querySelector('#f-vertexCredentials').value='C:/qa-only/dummy-vertex-credentials.json'`);
   await click('#editor-nav [data-section-target="headers"]');await click('[data-action="add-header"]');
   await js(`document.querySelector('#header-list .header-row:last-child .header-name').value='X-QA-Required'`);
   await click('#save');await wait(`!document.querySelector('#form-error').hidden&&!document.querySelector('#save').disabled`);
   assert.equal(await js(`document.querySelector('#form-error').textContent`),'请填写 HTTP Header X-QA-Required 的值');
   assert.equal(calls.filter(c=>c.body!==undefined).length,writesBefore,'a manually added named header with no value still blocks the original save');
   await click('#header-list .header-row:last-child [data-action="remove-header"]');
   assert.deepEqual(await js(`[...document.querySelectorAll('#header-list .header-name')].map(i=>i.value)`),sourcePresets.find(p=>p.id==='google-vertex').headerHints,'both official optional header hints remain in the form');
   await click('#editor-nav [data-section-target="connection"]');await click('#save');await wait(`editor?.type==='connection-success'&&!document.querySelector('#save').disabled`);
   const vertexSaves=calls.filter(c=>c.path==='/api/provider'&&c.body!==undefined);assert.equal(vertexSaves.length,1,'valid Vertex settings reach the actual provider POST once');
   assert.equal(vertexSaves[0].body.preset,'google-vertex');assert.deepEqual(vertexSaves[0].body.vertex,{project:'qa-vertex-project',location:'us-central1',credentials:'C:/qa-only/dummy-vertex-credentials.json',impersonate:''});assert.deepEqual(vertexSaves[0].body.headers,{},'blank official header hints are omitted from the saved headers');
   assert.equal(calls.filter(c=>c.body!==undefined).length,writesBefore+1,'only the explicit valid Vertex save writes');
   await close();await click('[data-action="add-provider"]');await wait(`editor?.type==='sources'&&!editor.loading`);await search('Vertex');await click('[data-source-key="preset:google-vertex"]');await wait(`editor?.type==='provider'`);
   await js(`document.querySelector('#f-preset').value='deepseek';document.querySelector('#f-preset').dispatchEvent(new Event('change',{bubbles:true}))`);assert.equal(await js(`document.querySelector('#f-key').disabled||document.querySelector('#provider-api-auth').hidden||document.querySelector('#provider-api-endpoints').hidden`),false);assert.equal(await js(`document.querySelector('#f-vertexProject').required`),false);assert.equal(await js(`document.querySelector('#f-vertexProject').disabled`),true);
   await close();await click('[data-action="subscription"]');await wait(`editor?.type==='sources'&&!editor.loading`);assert.equal(await js(`document.querySelector('#source-scope').value`),'plans');assert.ok(await js(`document.querySelector('[data-source-key="preset:clinepass"]')&&document.querySelector('[data-source-key="preset:qa-plan"]')&&document.querySelector('[data-source-key="subscription:claude"]')`));assert.equal(await js(`!!document.querySelector('[data-source-key="preset:deepseek"]')`),false);
   await scope('all');assert.ok(await js(`document.querySelector('[data-source-key="preset:deepseek"]')`));
   await click('[data-source-command="adapters"]');await wait(`editor?.type==='adapters'`);assert.equal(await js(`document.querySelector('#adapter-search-query').closest('.editor-section').hidden`),false,'the more-sources action opens actual adapter discovery');
   await click('[data-source-command="back"]');await wait(`editor?.type==='sources'&&!editor.loading`);await click('[data-source-key="subscription:claude"]');await wait(`editor?.type==='subscription'&&document.querySelector('#f-agent')`);assert.equal(await js(`document.querySelector('#f-agent').value`),'claude');
   assert.equal(calls.filter(c=>c.body!==undefined).length,writesBefore+1,'browsing and choosing a source add no writes after the one explicit Vertex save');
   for(const [action,expectedScope] of [['add-provider','all'],['subscription','plans']]){
    const presetReads=calls.filter(c=>c.path==='/api/provider/presets').length,subscriptionReads=calls.filter(c=>c.path==='/api/subscriptions').length;
    await click('#cancel-editor');
    assert.equal(await js(`document.querySelector('#editor').open`),false,'the original close click closes the dialog synchronously');
    await click(`[data-action="${action}"]`);
    await wait(`document.querySelector('#editor').open&&editor?.type==='sources'&&!editor.closed&&!editor.loading&&document.querySelector('[data-source-key="preset:clinepass"]')`);
    assert.equal(await js(`document.querySelector('#source-scope').value`),expectedScope,'immediate reopen retains the original button scope');
    assert.equal(calls.filter(c=>c.path==='/api/provider/presets').length,presetReads+1,'the reopened directory completes its actual preset GET');
    assert.equal(calls.filter(c=>c.path==='/api/subscriptions').length,subscriptionReads+1,'the reopened directory completes its actual subscriptions GET');
    assert.equal(calls.filter(c=>c.body!==undefined).length,writesBefore+1,'closing and immediately reopening sources add no writes or sign-in after the one explicit Vertex save');
   }
   await close();
   if(process.env.WHALEBRIDGE_QA_FOCUS==='sources-only'){console.log('WHALEBRIDGE_CONTROLS_RESULT:'+JSON.stringify({sourceCatalog:true,sourceSearch:true,sourceKeyboard:true,sourceScopes:true,sourceClinePass:true,sourceVertex:true,sourceVertexSave:true,sourceRequiredHeaderValidation:true,sourceSponsor:true,sourceAdapters:true,sourceNoAutomaticWrites:true,sourceImmediateReopen:true}));return;}
  }
  const changeProxy=mode=>js(`(()=>{const p=document.querySelector('#global-form [name="proxyMode"]');p.value=${JSON.stringify(mode)};p.dispatchEvent(new Event('change',{bubbles:true}));})()`);
  let heldApiBaseline=0;
  const held=async()=>{const end=Date.now()+7000;while(!releaseRequest&&Date.now()<end)await new Promise(r=>setTimeout(r,20));assert.ok(releaseRequest,'request reached backend');heldApiBaseline=await js(`window.__qaApiSettled[${JSON.stringify(heldPath)}]?.settled||0`);};
  const release=()=>{const observed={path:heldPath,baseline:heldApiBaseline};heldPath=undefined;const resolve=releaseRequest;releaseRequest=undefined;resolve();return observed;};
  if(!focused){
  await subscribe();await click('[data-action="install-adapter"]');await wait(`document.querySelector('[data-action="install-adapter"] .button-spinner')`);
  assert.equal(await js(`document.querySelector('#f-agent').disabled&&document.querySelector('#save').disabled`),true);
  assert.equal(await js(`document.querySelector('#adapter-progress progress')`),null,'no progress bar');
  assert.equal(await js(`document.querySelector('[data-action="install-adapter"]').textContent`),'下载中');
  await screenshot('downloading-button');await installStarted();releaseStage();await wait(`document.querySelector('[data-action="install-adapter"]').textContent==='安装中'`);
  await screenshot('installing-button');releaseInstall();await wait(`document.querySelector('#f-agent').value==='cursor-adapter'&&!document.querySelector('#adapter-progress').hidden`);
  assert.match(await js(`document.querySelector('#adapter-progress').textContent`),/已安装/);await close();
  installed=false;failing=true;await subscribe();await click('[data-action="install-adapter"]');await wait(`document.querySelector('[data-action="install-adapter"] .button-spinner')`);await installStarted();releaseInstall();
  await wait(`!document.querySelector('#form-error').hidden`);assert.match(await js(`document.querySelector('#form-error').textContent`),/installation rejected/);
  assert.equal(await js(`document.querySelector('#f-agent').disabled||document.querySelector('#save').disabled`),false);await close();
  failing=false;await subscribe();await click('[data-action="install-adapter"]');await wait(`document.querySelector('[data-action="install-adapter"] .button-spinner')`);await installStarted();releaseStage();catalogHeld=true;releaseInstall();await wait(`document.querySelector('[data-action="install-adapter"]').textContent==='安装中'`);
  while(!releaseCatalog)await new Promise(r=>setTimeout(r,20));
  const completeInstallCatalog=releaseCatalog;catalogHeld=false;await close();await addProvider();completeInstallCatalog();
  await wait(`document.querySelector('#editor-title').textContent==='添加供应商'`);assert.equal(await js(`document.querySelector('#adapter-progress').hidden`),true);await close();
  await click('[data-tab="providers"]');await wait(`document.querySelector('details.menu summary')`);
  for(const scheme of ['light','dark']){nativeTheme.themeSource=scheme;
   for(const [width,height] of [[1120,780],[380,520]]){
    win.setContentSize(width,height);
    await wait(`innerWidth===${width}&&innerHeight===${height}`);
    await js(`document.querySelector('.management-table-wrap').scrollLeft=9999`);
    await js(`new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))`);
    await click('details.menu summary');await wait(`document.querySelector('.menu-content:popover-open')`);
    await screenshot(`menu-${scheme}-${width}`);
    const geometry=await js(`(()=>{const m=document.querySelector('.menu-content:popover-open');if(!m)return {closed:true};const r=m.getBoundingClientRect(),hit=document.elementFromPoint(r.left+10,r.top+10);return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,width:innerWidth,height:innerHeight,hit:hit?.outerHTML,hitMenu:hit?.closest('.menu-content')===m}})()`);
    assert.ok(!geometry.closed&&geometry.left>=0&&geometry.right<=geometry.width&&geometry.top>=0&&geometry.bottom<=geometry.height&&geometry.hitMenu,JSON.stringify({scheme,width,geometry}));
    await js(`document.querySelector('.menu-content:popover-open').dispatchEvent(new Event('scroll'))`);
    assert.equal(await js(`!!document.querySelector('.menu-content:popover-open')`),true,'menu own scroll does not close');
    await js(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))`);
    assert.equal(await js(`!!document.querySelector('.menu-content:popover-open')`),false);assert.equal(await js(`document.activeElement.matches('details.menu summary')`),true);
   }
  }
  win.setContentSize(1120,780);await click('details.menu summary');await click('.menu-content [data-action="toggle-provider"]');await wait(`document.querySelector('#message-text').textContent.includes('已更新')&&!document.querySelector('#refresh').disabled`);
  assert.ok(calls.some(c=>c.path==='/api/provider'&&c.body.off===true));
  await click('[data-action="manage-adapters"]');await wait(`document.querySelector('[data-action="adapter-page"]')`);await click('[data-action="adapter-page"]');await wait(`editor?.type==='adapter-readme'`);await click('#fields [data-action="manage-adapters"]');await wait(`editor?.type==='adapters'`);
  await click('[data-action="adapter-check"]');await wait(`!document.querySelector('#form-error').hidden`);assert.match(await js(`document.querySelector('#form-error').textContent`),/registry offline/);await close();
  await click('[data-tab="routing"]');await wait(`document.querySelector('[data-action="remove-selected-groups"]')`);await click('[data-action="remove-selected-groups"]');await wait(`!document.querySelector('#message').hidden`);
  assert.match(await js(`document.querySelector('#message-text').textContent`),/请先选择/);assert.equal(await js(`document.querySelector('#message').dataset.error`),'true');
  assert.ok(!calls.some(c=>c.path==='/api/group/delete'));
  await click('[data-tab="settings"]');await wait(`document.querySelector('#global-form [name="proxyMode"]')`);
  await changeProxy('custom');assert.equal(await js(`!document.querySelector('#content #proxy-address').hidden&&document.querySelector('#content [name="proxy"]').required`),true);
  await js(`document.querySelector('#content [name="proxy"]').value='http://127.0.0.1:7890'`);await click('#global-form [type="submit"]');await wait(`!document.querySelector('#refresh').disabled&&document.querySelector('#message-text').textContent==='设置已保存'`);
  assert.equal(globalSettings.proxy,'http://127.0.0.1:7890');await changeProxy('direct');assert.equal(await js(`document.querySelector('#content #proxy-address').hidden&&!document.querySelector('#content [name="proxy"]').required`),true);
  await click('#global-form [type="submit"]');await wait(`!document.querySelector('#refresh').disabled&&globalData.settings.proxy==='direct'`);assert.equal(globalSettings.proxy,'direct');
  // Hold real HTTP responses: feedback must stay in the originating control.
  await click('[data-tab="providers"]');await wait(`document.querySelector('details.menu summary')`);
  heldPath='/api/provider/fetch';await click('details.menu summary');await click('.menu-content [data-action="fetch-provider"]');await held();
  assert.equal(await js(`!document.querySelector('.menu-content:popover-open')&&!!document.querySelector('details.menu summary .button-spinner')`),true,'closed menu retains spinner in visible trigger');
  await screenshot('menu-refresh-busy');release();await wait(`!document.querySelector('#refresh').disabled&&!document.querySelector('details.menu summary .button-spinner')`);
  await click('[data-action="edit-provider"]');await wait(`editor?.type==='provider'`);
  heldPath='/api/provider/test';rejectRequest=true;await click('[data-action="test-provider"]');await held();
  assert.equal(await js(`document.querySelector('[data-action="test-provider"]').textContent`),'测试中');await screenshot('provider-test-busy');
  release();await wait(`!document.querySelector('#form-error').hidden`);assert.match(await js(`document.querySelector('#form-error').textContent`),/requested operation failed/);
  assert.equal(await js(`document.querySelector('[data-action="test-provider"]').disabled||!!document.querySelector('[data-action="test-provider"] .button-spinner')`),false,'failed test restores button');rejectRequest=false;await close();
  await click('[data-tab="settings"]');await wait(`document.querySelector('#global-form')`);
  heldPath='/api/global/settings';await click('#global-form [type="submit"]');await held();
  assert.equal(await js(`document.querySelector('#global-form [type="submit"]').textContent`),'保存中');for(const scheme of ['light','dark']){nativeTheme.themeSource=scheme;await screenshot('settings-save-busy-'+scheme);const colors=await js(`(()=>{const c=getComputedStyle(document.querySelector('#global-form [type=submit]'));return {text:c.color,background:c.backgroundColor}})()`);assert.notEqual(colors.text,colors.background,'busy primary has visible foreground');}
  release();await wait(`!document.querySelector('#refresh').disabled&&document.querySelector('#global-form [type="submit"]').textContent==='保存设置'`);
  await click('[data-tab="providers"]');await wait(`document.querySelector('[data-action="manage-adapters"]')`);await click('[data-action="manage-adapters"]');await wait(`editor?.type==='adapters'`);
  heldPath='/api/subscription/adapters/check';await click('[data-action="adapter-check"]');await held();
  assert.equal(await js(`document.querySelector('[data-action="adapter-check"]').textContent`),'检查中');await screenshot('adapter-check-busy');release();await wait(`!document.querySelector('#form-error').hidden`);await close();
  heldPath='/api/state';await click('#refresh');await held();assert.equal(await js(`!!document.querySelector('#refresh .button-spinner')`),true);release();await wait(`!document.querySelector('#refresh').disabled`);
  heldPath='/api/accounts/qa2/usage';await click('[data-action="accounts"][data-id="qa2"]');
  await wait(`editor?.type==='accounts'`);await held();
  assert.equal(await js(`document.querySelector('[data-action="accounts-refresh"]').textContent`),'刷新中');
  assert.equal(await js(`document.querySelector('[data-account-quota]').textContent`),'');await screenshot('account-quota-busy');win.setContentSize(380,520);await wait('innerWidth===380');await screenshot('account-quota-busy-narrow');win.setContentSize(1120,780);await wait('innerWidth===1120');
  release();await wait(`document.querySelector('[data-account-quota]').textContent==='15'&&!document.querySelector('[data-action="accounts-refresh"]').disabled`);await close();
  installed=false;await subscribe();await js(`document.querySelector('#f-agent').value='claude';document.querySelector('#f-agent').dispatchEvent(new Event('change'))`);await click('#save');
  await wait(`document.querySelector('#save').textContent==='准备中'`);assert.equal(await js(`!document.querySelector('#save').hidden&&!!document.querySelector('#save .button-spinner')`),true);await screenshot('auth-tool-busy');
  loginStatus='waiting';await wait(`document.querySelector('#fields a')`);assert.equal(await js(`document.querySelector('#save').textContent`),'等待授权');
  loginStatus='failed';await wait(`!document.querySelector('#form-error').hidden`);assert.equal(await js(`document.querySelector('#save').disabled||!!document.querySelector('#save .button-spinner')`),false);await close();
  await click('[data-tab="usage"]');await wait(`document.querySelector('[data-global-section="routing"]')`);await click('[data-global-section="routing"]');await wait(`document.querySelector('#route-day')`);
  heldPath='/api/routes';await click('[data-global="live-routes"]');await held();const releaseLive=releaseRequest;
  heldPath='/api/routes/history';releaseRequest=undefined;await js(`document.querySelector('#route-day').value='2026-10-08';document.querySelector('#route-day').dispatchEvent(new Event('change'))`);await held();
  releaseLive();await js(`new Promise(r=>setTimeout(r,100))`);assert.equal(await js(`document.querySelector('[data-global="live-routes"]').disabled`),true,'old operation must not unlock newer wait');release();await wait(`!document.querySelector('[data-global="live-routes"]').disabled`);
  }
  // Delayed replies are real fetches against the shipped renderer, not copies of its callbacks.
  const type=async(selector,value)=>js(`(()=>{const i=document.querySelector(${JSON.stringify(selector)});i.value=${JSON.stringify(value)};i.dispatchEvent(new Event('input',{bubbles:true}));})()`);
  const settledReply=async observed=>{await wait(`(window.__qaApiSettled[${JSON.stringify(observed.path)}]?.settled||0)>${observed.baseline}`);await js(`new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))`);};
  const screenshotPair=async(name,selector)=>{for(const [width,height,label] of [[1120,780,'normal'],[380,520,'narrow']]){await resize(width,height);if(selector)await js(`document.querySelector(${JSON.stringify(selector)}).scrollIntoView({block:'center'})`);await screenshot(name+'-'+label);}await resize(1120,780);};
  const section=async key=>{await click(`[data-global-section="${key}"]`);await wait(`document.querySelector('[data-global-section="${key}"]').getAttribute('aria-selected')==='true'&&document.querySelector('#content').getAttribute('aria-busy')==='false'`);};
  if(!messagesOnly){
  const providerDraft=async(name,id)=>{await click('[data-tab="providers"]');await wait(`document.querySelector('[data-action="add-provider"]')`);await addProvider();await wait(`document.querySelector('#f-name')&&document.querySelector('#f-id')`);await type('#f-name',name);await type('#f-id',id);};
  const assertProviderDraft=async(name,id)=>assert.deepEqual(await js(`({open:document.querySelector('#editor').open,title:document.querySelector('#editor-title').textContent,name:document.querySelector('#f-name')?.value,id:document.querySelector('#f-id')?.value,enabled:!document.querySelector('#save').disabled,errorHidden:document.querySelector('#form-error').hidden})`),{open:true,title:'添加供应商',name,id,enabled:true,errorHidden:true});
  const quotas=async()=>{await click('[data-tab="usage"]');await wait(`document.querySelector('[data-global-section="quotas"]')`);await click('[data-global-section="quotas"]');await wait(`document.querySelector('[data-action="quota-settings"]')`);};
  for(const [action,savePath,name,id] of [['subscription-settings','/api/subscription/settings','Subscription return draft','subscription-return'],['quota-settings','/api/accounts/settings','Account return draft','account-return']]){
   await quotas();await click(`[data-action="${action}"]`);await wait(`document.querySelector('#editor').open&&!document.querySelector('#save').disabled`);
   heldPath=savePath;await click('#save');await held();assert.equal(await js(`document.querySelector('#save').disabled`),true);
   await close();await providerDraft(name,id);await settledReply(release());await assertProviderDraft(name,id);
   if(action==='quota-settings')await screenshotPair('maintenance-return-provider-draft');await close();
  }
  const startWaitingLogin=async()=>{loginStatus='waiting';await subscribe();await js(`document.querySelector('#f-agent').value='claude';document.querySelector('#f-agent').dispatchEvent(new Event('change'))`);await click('#save');await wait(`document.querySelector('#editor').open&&document.querySelector('#save').textContent==='等待授权'&&document.querySelector('#fields a')`);return loginIds.at(-1);};
  const oldProviderLogin=await startWaitingLogin();heldPath='/api/signin/'+oldProviderLogin;rejectRequest=true;await held();await close();await providerDraft('Login return draft','login-return');await settledReply(release());await assertProviderDraft('Login return draft','login-return');rejectRequest=false;await close();
  const oldLogin=await startWaitingLogin();heldPath='/api/signin/'+oldLogin;rejectRequest=true;await held();await close();const newerLogin=await startWaitingLogin();assert.notEqual(newerLogin,oldLogin);
  await settledReply(release());rejectRequest=false;
  const continued=Date.now()+7000;while(calls.filter(c=>c.path==='/api/signin/'+newerLogin).length<2&&Date.now()<continued)await new Promise(r=>setTimeout(r,20));
  assert.ok(calls.filter(c=>c.path==='/api/signin/'+newerLogin).length>=2,'new login continues polling after old GET failure');
  assert.equal(calls.some(c=>c.path==='/api/signin/'+newerLogin+'/cancel'),false,'old failure does not cancel new login');
  assert.deepEqual(await js(`({open:document.querySelector('#editor').open,waiting:document.querySelector('#save').textContent,busy:!!document.querySelector('#save .button-spinner'),errorHidden:document.querySelector('#form-error').hidden})`),{open:true,waiting:'等待授权',busy:true,errorHidden:true});
  await screenshotPair('old-login-failure-new-login');await close();
  const setProxy=async value=>{await changeProxy('custom');await type('#content [name="proxy"]',value);};
  await click('[data-tab="settings"]');await wait(`document.querySelector('[data-global-section="connection"]')`);await section('connection');
  await setProxy('http://submitted-cross-section.invalid');heldPath='/api/global/settings';await click('#global-form [type="submit"]');await held();await section('privacy');await type('#global-form [name="redactWords"]','PRIVACY DRAFT AFTER NAVIGATION');await settledReply(release());
  assert.equal(await js(`document.querySelector('[data-global-section="privacy"]').getAttribute('aria-selected')`),'true');
  assert.equal(await js(`document.querySelector('#global-form [name="redactWords"]').value`),'PRIVACY DRAFT AFTER NAVIGATION');assert.equal(await js(`document.querySelector('#global-form [type="submit"]').disabled`),false);
  await screenshotPair('global-save-other-section-draft','#global-form [name="redactWords"]');
  await section('connection');await setProxy('http://submitted-before-navigation-completes.invalid');heldPath='/api/global/settings';await click('#global-form [type="submit"]');await held();
  const releaseSavedPost=releaseRequest,savedPostObservation={path:heldPath,baseline:heldApiBaseline},readsBeforeNavigation=calls.filter(c=>c.path==='/api/global').length;releaseRequest=undefined;heldPath='/api/global';
  await js(`void(window.__qaOriginForm=document.querySelector('#global-form'))`);await click('[data-global-section="privacy"]');await held();assert.equal(await js(`window.__qaOriginForm.isConnected&&globalSection==='privacy'`),true,'navigation awaits data while old form stays connected');
  releaseSavedPost();await settledReply(savedPostObservation);assert.equal(calls.filter(c=>c.path==='/api/global').length,readsBeforeNavigation+1,'old save does not start an unrelated page load');assert.equal(await js(`window.__qaOriginForm.isConnected`),true);
  await settledReply(release());await wait(`document.querySelector('[data-global-section="privacy"]').getAttribute('aria-selected')==='true'&&document.querySelector('#content').getAttribute('aria-busy')==='false'`);assert.equal(await js(`document.querySelector('#global-form [name="redactWords"]').value`),'PRIVACY DRAFT AFTER NAVIGATION','old connected form cannot overwrite target-section draft while navigation is waiting');
  await section('connection');await setProxy('http://submitted-before-return.invalid');heldPath='/api/global/settings';await click('#global-form [type="submit"]');await held();await section('privacy');await section('connection');await setProxy('http://new-returned-draft.invalid');await settledReply(release());
  assert.equal(await js(`document.querySelector('#global-form [name="proxy"]').value`),'http://new-returned-draft.invalid');assert.equal(await js(`document.querySelector('#global-form [type="submit"]').disabled`),false);
  await section('privacy');await section('connection');assert.equal(await js(`document.querySelector('#global-form [name="proxy"]').value`),'http://new-returned-draft.invalid','new draft survives another section round trip');
  await setProxy('http://submitted-same-form.invalid');heldPath='/api/global/settings';await click('#global-form [type="submit"]');await held();await setProxy('http://edited-while-saving.invalid');release();await wait(`document.querySelector('#global-form [type="submit"]').textContent==='保存设置'&&!document.querySelector('#global-form [type="submit"]').disabled`);
  assert.equal(await js(`document.querySelector('#global-form [name="proxy"]').value`),'http://edited-while-saving.invalid');assert.equal(calls.filter(c=>c.path==='/api/global/settings').at(-1).body.proxy,'http://submitted-same-form.invalid');
  await type('#uniform-price-form [name="model"]','fixture-price');for(const [name,value] of [['input','1'],['output','2'],['cache_read','3'],['cache_write','4']])await type(`#uniform-price-form [name="${name}"]`,value);
  heldPath='/api/model-price';await click('#uniform-price-form [type="submit"]');await held();await type('#uniform-price-form [name="input"]','7');release();await wait(`!document.querySelector('#uniform-price-form [type="submit"]').disabled`);
  assert.equal(await js(`document.querySelector('#uniform-price-form [name="input"]').value`),'7');assert.equal(calls.filter(c=>c.path==='/api/model-price').at(-1).body.price.input,1);
  await section('privacy');await section('connection');assert.equal(await js(`document.querySelector('#uniform-price-form [name="input"]').value`),'7','price draft survives round trip');assert.equal(await js(`document.querySelector('#global-form [name="proxy"]').value`),'http://edited-while-saving.invalid','saving price retains companion settings draft');
  const priceB=()=>js(`({values:[...document.querySelector('#uniform-price-form').elements].filter(i=>i.name).map(i=>[i.name,i.value]),tiers:[...document.querySelector('#uniform-price-tiers').querySelectorAll('input')].map(i=>i.value)})`);
  const expectedB={values:[['model','fixture-b'],['input','11'],['output','12'],['cache_read','13'],['cache_write','14'],['cache_write_1h','15']],tiers:['65536','21','22','23','24','25']};
  heldPath='/api/model-price';await click('#uniform-price-form [type="submit"]');await held();await click('[data-global="edit-price"][data-id="*/fixture-b"]');assert.deepEqual(await priceB(),expectedB);
  await settledReply(release());await wait(`!document.querySelector('#uniform-price-form [type="submit"]').disabled`);assert.deepEqual(await priceB(),expectedB,'programmatically selected B survives completion of A');assert.equal(calls.filter(c=>c.path==='/api/model-price').at(-1).body.id,'*/fixture-price');
  await section('privacy');await section('connection');assert.deepEqual(await priceB(),expectedB,'selected B and all price tiers survive section round trip');
  await click('[data-global="add-uniform-tier"]');for(const [selector,value] of [['[data-tier-above]','131072'],['[data-tier-price="input"]','31'],['[data-tier-price="output"]','32'],['[data-tier-price="cache_read"]','33'],['[data-tier-price="cache_write"]','34'],['[data-tier-price="cache_write_1h"]','35']])await type('#uniform-price-tiers .price-tier:last-child '+selector,value);
  heldPath='/api/model-price';await click('#uniform-price-form [type="submit"]');await held();assert.equal(await js(`[...document.querySelectorAll('#uniform-price-tiers [data-global="remove-uniform-tier"]')].every(b=>b.disabled)`),true);await type('#uniform-price-form [name="input"]','17');await settledReply(release());await wait(`document.querySelector('#uniform-price-form [name="input"]').value==='17'&&[...document.querySelectorAll('#uniform-price-tiers [data-global="remove-uniform-tier"]')].every(b=>!b.disabled)`);
  assert.equal(await js(`document.querySelectorAll('#uniform-price-tiers .price-tier').length`),2);await click('#uniform-price-tiers .price-tier:last-child [data-global="remove-uniform-tier"]');assert.equal(await js(`document.querySelectorAll('#uniform-price-tiers .price-tier').length`),1,'restored tier remove button actually removes the edited draft row');
  await section('privacy');await click('[data-global="add-rule"]');await type('#redact-rules [data-rule="value"]','fixture-secret-prefix-');heldPath='/api/global/settings';await click('#global-form [type="submit"]');await held();assert.equal(await js(`document.querySelector('#redact-rules [data-global="remove-rule"]').disabled`),true);await type('#global-form [name="redactWords"]','PRIVACY EDIT DURING RULE SAVE');await settledReply(release());await wait(`document.querySelector('#global-form [name="redactWords"]').value==='PRIVACY EDIT DURING RULE SAVE'&&!document.querySelector('#redact-rules [data-global="remove-rule"]').disabled`);
  assert.equal(await js(`document.querySelector('#redact-rules [data-rule="value"]').value`),'fixture-secret-prefix-');await click('#redact-rules [data-global="remove-rule"]');assert.equal(await js(`document.querySelectorAll('#redact-rules .redact-rule-row').length`),0,'restored rule remove button actually removes the draft rule');
  await section('connection');assert.equal(await js(`document.querySelector('#uniform-price-form [name="input"]').value`),'17');assert.equal(await js(`document.querySelectorAll('#uniform-price-tiers .price-tier').length`),1);
  await section('backup');await wait(`document.querySelector('#sync-form')`);await type('#sync-form [name="url"]','https://fixture.invalid/sync');await type('#sync-form [name="user"]','submitted-sync-user');heldPath='/api/sync/save';await click('#sync-form [type="submit"]');await held();await type('#sync-form [name="user"]','edited-sync-draft');release();await wait(`!document.querySelector('#sync-form [type="submit"]').disabled`);
  assert.equal(await js(`document.querySelector('#sync-form [name="user"]').value`),'edited-sync-draft');assert.equal(calls.filter(c=>c.path==='/api/sync/save').at(-1).body.user,'submitted-sync-user');
  await section('connection');await section('backup');assert.equal(await js(`document.querySelector('#sync-form [name="user"]').value`),'edited-sync-draft','sync draft survives round trip');await screenshotPair('sync-save-new-draft','#sync-form [name="user"]');
  }else{syncSettings={on:true,url:'https://fixture.invalid/sync',user:'edited-sync-draft',auto:3};await click('[data-tab="settings"]');await wait(`document.querySelector('[data-global-section="privacy"]')`);}
  // A real success/error message must leave the affected controls visible and clickable.
  nativeTheme.themeSource='light';
  const hitClick=selector=>js(`(()=>{const b=document.querySelector(${JSON.stringify(selector)}),r=b.getBoundingClientRect(),hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);if(hit!==b&&!b.contains(hit))throw Error('Target covered: '+${JSON.stringify(selector)});hit.closest('button').click();})()`);
  const assertMessageTargets=async selectors=>{const geometry=await js(`(()=>{const m=document.querySelector('#message'),r=m.getBoundingClientRect(),bounds=e=>{const q=e.getBoundingClientRect();return {left:q.left,right:q.right,top:q.top,bottom:q.bottom};};return {visible:!m.hidden,message:bounds(m),width:innerWidth,height:innerHeight,targets:${JSON.stringify(selectors)}.map(selector=>{const b=document.querySelector(selector),q=b.getBoundingClientRect(),hit=document.elementFromPoint(q.x+q.width/2,q.y+q.height/2);return {selector,...bounds(b),enabled:!b.disabled,hit:hit===b||b.contains(hit),intersects:r.left<q.right&&r.right>q.left&&r.top<q.bottom&&r.bottom>q.top};})};})()`);assert.ok(geometry.visible,JSON.stringify(geometry));for(const target of geometry.targets)assert.ok(target.enabled&&target.hit&&!target.intersects&&target.left>=0&&target.right<=geometry.width&&target.top>=0&&target.bottom<=geometry.height,JSON.stringify(geometry));};
  for(const [width,height,label] of [[1120,780,'normal'],[380,520,'narrow']]){
   await resize(width,height);await section('privacy');await click('#global-form [type="submit"]');await wait(`document.querySelector('#message-text').textContent==='设置已保存'&&!document.querySelector('#global-form [type="submit"]').disabled`);
   await js(`document.querySelector('#global-form .page-save-bar').scrollIntoView({block:'center'})`);await assertMessageTargets(['#global-form [type="submit"]','#global-form [data-global="cancel-settings"]']);await screenshot('privacy-success-controls-'+label);
   await hitClick('#dismiss-message');await wait(`document.querySelector('#message').hidden`);assert.equal(await js(`getComputedStyle(document.querySelector('#message')).display`),'none');
   await section('backup');await click('[data-global="sync-now"]');await wait(`document.querySelector('#message-text').textContent==='同步操作已完成'&&!document.querySelector('[data-global="sync-now"]').disabled`);
   await js(`document.querySelector('#sync-form .actions-bar').scrollIntoView({block:'center'})`);const syncTargets=['#sync-form [type="submit"]','[data-global="sync-now"]','[data-global="sync-restore"]','[data-global="sync-undo"]','[data-global="sync-off"]'];await assertMessageTargets(syncTargets);await screenshot('sync-success-controls-'+label);
   for(const action of ['sync-restore','sync-undo']){await hitClick(`[data-global="${action}"]`);await wait(`document.querySelector('#confirmation').open`);assert.equal(await js(`document.querySelector('#confirm-title').textContent`),'同步操作');await click('#cancel-confirm');await wait(`!document.querySelector('#confirmation').open&&!document.querySelector('[data-global="${action}"]').disabled`);}
   await hitClick('#dismiss-message');await wait(`document.querySelector('#message').hidden`);await js(`document.querySelector('#sync-form .actions-bar').scrollIntoView({block:'center'})`);for(const selector of syncTargets)assert.equal(await js(`(()=>{const b=document.querySelector(${JSON.stringify(selector)}),r=b.getBoundingClientRect(),hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return !b.disabled&&(hit===b||b.contains(hit));})()`),true,'dismiss restores usable controls');
   heldPath='/api/sync/now';rejectRequest=true;failureText='同步服务器返回了错误：连接已关闭，请检查服务器地址、账号权限与远端配置。'+('服务器验证失败，远端请求未完成，当前本机配置和编辑内容已保留。请检查服务地址、账号权限以及远端备份状态，再决定是否重试同步。').repeat(5)+'错误详情：https://fixture.invalid/a-very-long-server-error-path-without-spaces-for-wrapping-and-reachable-dismiss-control';await click('[data-global="sync-now"]');await held();await settledReply(release());await wait(`document.querySelector('#message').dataset.error==='true'&&!document.querySelector('[data-global="sync-now"]').disabled`);rejectRequest=false;
   await js(`document.querySelector('#sync-form [name="user"]').scrollIntoView({block:'center'})`);await screenshot('sync-long-error-input-'+label);const inputBounds=await js(`(()=>{const i=document.querySelector('#sync-form [name="user"]'),r=i.getBoundingClientRect(),m=document.querySelector('#main').getBoundingClientRect(),hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return {inside:r.top>=m.top&&r.bottom<=m.bottom&&r.left>=0&&r.right<=innerWidth,hit:hit===i};})()`);assert.deepEqual(inputBounds,{inside:true,hit:true});
   await js(`document.querySelector('#sync-form .actions-bar').scrollIntoView({block:'center'})`);await screenshot('sync-long-error-controls-'+label);await assertMessageTargets(syncTargets);const errorBounds=await js(`(()=>{const m=document.querySelector('#message'),t=document.querySelector('#message-text'),d=document.querySelector('#dismiss-message'),r=m.getBoundingClientRect(),q=d.getBoundingClientRect(),hit=document.elementFromPoint(q.x+q.width/2,q.y+q.height/2);return {wrap:t.getBoundingClientRect().height>parseFloat(getComputedStyle(t).lineHeight),scrollable:t.scrollHeight>t.clientHeight,complete:t.textContent===${JSON.stringify(failureText)},fits:t.scrollWidth<=t.clientWidth&&r.left>=0&&r.right<=innerWidth&&r.top>=0&&r.bottom<=innerHeight,dismiss:hit===d||d.contains(hit)};})()`);assert.deepEqual(errorBounds,{wrap:true,scrollable:true,complete:true,fits:true,dismiss:true});await js(`document.querySelector('#message-text').scrollTop=document.querySelector('#message-text').scrollHeight;void 0`);await js(`new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))`);assert.equal(await js(`(()=>{const t=document.querySelector('#message-text');return t.scrollTop>0&&t.scrollTop+t.clientHeight>=t.scrollHeight-1;})()`),true,'full error can be scrolled to the last line');await hitClick('#dismiss-message');await wait(`document.querySelector('#message').hidden`);
  }
  const settlements=await js(`window.__qaApiSettled`);if(process.env.WHALEBRIDGE_QA_SCREENSHOTS)fs.writeFileSync(path.join(process.env.WHALEBRIDGE_QA_SCREENSHOTS,'api-settlements.json'),JSON.stringify(settlements,null,2));console.log('WHALEBRIDGE_API_SETTLEMENTS:'+JSON.stringify(settlements));
  console.log('WHALEBRIDGE_CONTROLS_RESULT:'+JSON.stringify({...(!focused?{progress:true,failure:true,staleCompletion:true,menu:true,keyboard:true,returnButton:true,updateFailure:true,groupFeedback:true,globalProxy:true,buttonFeedback:true,authPreparation:true,accountQuota:true,busyOwnership:true}:{}),...(!messagesOnly?{maintenanceOwnership:true,loginOwnership:true,globalDraftOwnership:true,navigationOwnership:true,priceDraftOwnership:true,priceSelectionOwnership:true,dynamicDraftControls:true,syncDraftOwnership:true}:{}),messagePlacement:true,messageDismiss:true,longErrorWrap:true}));
 }finally{win.destroy();server.closeAllConnections();await new Promise(r=>server.close(r));}
}
run().then(()=>app.exit(0)).catch(error=>{console.error(error.stack);app.exit(1);});
