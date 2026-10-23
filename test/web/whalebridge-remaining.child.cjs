'use strict';
const { app, BrowserWindow, nativeTheme } = require('electron');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
app.setPath('userData', process.argv[2]);
const assets = path.resolve(__dirname, '../../internal/whalebridge/assets');
const names = new Set(fs.readdirSync(assets));
const writes = [], external = [];
const regions = ['us-east-1','us-east-2','us-west-2','eu-central-1','eu-central-2','eu-north-1','eu-south-1','eu-west-1','eu-west-2','ap-northeast-1','ap-northeast-2','ap-south-1','ap-southeast-1','ap-southeast-2','ap-southeast-3','ap-southeast-4','sa-east-1'].map(id => ({id,name:id,chat:`https://bedrock-mantle.${id}.api.aws/v1`,responses:`https://bedrock-mantle.${id}.api.aws/v1`,anthropic:`https://bedrock-mantle.${id}.api.aws/anthropic`}));
const presets = [
 {id:'nous',name:'Nous Portal',chat:'https://inference-api.nousresearch.com/v1',website:'https://portal.nousresearch.com',keysUrl:'https://portal.nousresearch.com'},
 {id:'siliconflow',name:'SiliconFlow',chat:'https://api.siliconflow.cn/v1',website:'https://cloud.siliconflow.cn',keysUrl:'https://cloud.siliconflow.cn/account/ak',regions:[{id:'cn',name:'China',chat:'https://api.siliconflow.cn/v1'},{id:'intl',name:'Global',chat:'https://api.siliconflow.com/v1',website:'https://cloud.siliconflow.com',keysUrl:'https://cloud.siliconflow.com/account/ak'}]},
 {...regions[0],id:'bedrock-mantle',name:'Amazon Bedrock (Mantle)',regions}
];
let providers = [{id:'relay',name:'Local fixture',chat:'https://fixture.test/v1',models:['m'],chosen:['m'],available:[{id:'m',name:'Model'}],balanceURL:'https://fixture.test/balance',balanceMethod:''}];
const secrets = new Map([['you','fixture-you'],['tavily','fixture-tavily']]);
const data = {settings:{},models:[],modelPrices:{},searchers:[],visionModels:[],drawingModels:[],fx:{rate:7},sync:{},searchVendors:[{id:'tavily',name:'Tavily',base:'https://api.tavily.test',keysURL:'https://keys.test'},{id:'you',name:'You.com',keyOptional:true},{id:'deepseek',name:'DeepSeek Search',base:'https://api.deepseek.test'}],searches:[{vendor:'you',keySet:true,keyMasked:'***you',ready:true,pick:'turns'},{vendor:'tavily',keySet:true,keyMasked:'***tavily',ready:true,canQuota:true,pick:'turns'}]};
const server = http.createServer(async(req,res) => {
 try {
  const url=new URL(req.url,'http://localhost');
  const json=value=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(value));};
  if(url.pathname.startsWith('/api/')) {
   let raw='';for await(const chunk of req)raw+=chunk;const body=raw?JSON.parse(raw):undefined;
   if(req.method==='POST')writes.push({path:url.pathname,body});
   if(url.pathname==='/api/state')return json({version:'remaining-qa',providers,presets,models:[],hidden:[],groups:[]});
   if(url.pathname==='/api/global')return json(data);
   if(url.pathname==='/api/provider') {const id=body.id||body.preset;const old=providers.find(p=>p.id===id)||{};providers=providers.filter(p=>p.id!==id).concat({...old,...body,id});return json({id,ok:true});}
   if(url.pathname==='/api/search/switch'){data.searches.find(s=>s.vendor===body.vendor).off=!body.on;return json({ok:true});}
   if(url.pathname==='/api/search/order'){data.searches=body.vendors.map(v=>data.searches.find(s=>s.vendor===v));return json({ok:true});}
   if(url.pathname==='/api/search/save'){let saved=data.searches.find(s=>s.vendor===body.vendor);if(!saved){saved={vendor:body.vendor};data.searches.push(saved);}if(body.key)secrets.set(body.vendor,body.key);Object.assign(saved,{url:body.url||'',pick:body.pick,keySet:secrets.has(body.vendor),keyMasked:'***fixture',ready:true});return json({ok:true});}
   if(url.pathname==='/api/search/quota')return json({keys:[{key:'***one',known:false,used:0,limit:0,plan:'<img src=x onerror="window.quotaInjected=1">'},{key:'***two',known:false,err:'<svg onload="window.quotaInjected=2">quota refused</svg>',plan:'<b>external plan</b>'}]});
   if(url.pathname==='/api/quotas'||url.pathname==='/api/subscriptions')return json([]);
   if(url.pathname==='/api/tools/efforts')return json([]);
   if(url.pathname==='/api/subscriptions/alerts')return json({sequence:0,alerts:[]});
   if(url.pathname==='/api/upstream')return json({providers:{},vendors:[]});
   return json({ok:true});
  }
  const name=url.pathname==='/'?'index.html':decodeURIComponent(url.pathname.slice(1));
  if(!names.has(name)){res.writeHead(404);return res.end();}
  res.setHeader('Content-Type',({'.js':'text/javascript','.css':'text/css','.html':'text/html','.svg':'image/svg+xml'})[path.extname(name)]||'application/octet-stream');res.end(fs.readFileSync(path.join(assets,name)));
 }catch(error){res.writeHead(500);res.end(error.stack);}
});
(async()=>{
 await app.whenReady();await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const win=new BrowserWindow({width:1120,height:820,show:false,webPreferences:{contextIsolation:true,nodeIntegration:false,backgroundThrottling:false}});
 const origin=`http://127.0.0.1:${server.address().port}`;
 win.webContents.session.webRequest.onBeforeRequest((details,callback)=>{const local=details.url.startsWith(origin+'/')||details.url.startsWith('data:');if(!local)external.push(details.url);callback({cancel:!local});});
 const js=source=>win.webContents.executeJavaScript(source).catch(e=>{throw Error(source+': '+e.message);});
 const wait=async expr=>{const end=Date.now()+5000;while(!await js(`Boolean(${expr})`)){if(Date.now()>end)throw Error('DOM timeout '+expr+' '+JSON.stringify(await js(`({message:document.querySelector('#message-text')?.textContent,invalid:[...document.querySelectorAll(':invalid')].map(e=>({id:e.id,value:e.value,message:e.validationMessage}))})`)));await new Promise(r=>setTimeout(r,20));}};
 const click=selector=>js(`document.querySelector(${JSON.stringify(selector)}).click()`);
 const fill=(selector,value,event='change')=>js(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});e.value=${JSON.stringify(value)};e.dispatchEvent(new Event(${JSON.stringify(event)},{bubbles:true}));})()`);
 const ready=()=>wait(`document.querySelector('#content').getAttribute('aria-busy')==='false'`);
 const reloaded=expr=>wait(`state!==window.remainingStateBeforeSave&&(${expr})&&document.querySelector('#content').getAttribute('aria-busy')==='false'&&document.querySelector('#refresh').getAttribute('aria-busy')!=='true'`);
 const close=()=>js(`document.querySelector('#editor').close()`);
 const rows=()=>js(`[...document.querySelectorAll('[data-search-api]')].map(e=>e.dataset.searchApi)`);
 const action=(verb,id)=>click(`[data-global="${verb}"][data-id="${id}"]`);
 const resize=async width=>{win.setContentSize(width,820);await wait(`innerWidth===${width}&&innerHeight===820`);await js(`new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`);};
 const screenshot=async name=>{if(!process.env.WHALEBRIDGE_QA_SCREENSHOTS)return;await js(`Promise.all(document.getAnimations().filter(a=>a.effect.getTiming().iterations!==Infinity).map(a=>a.finished))`);await js(`new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`);fs.mkdirSync(process.env.WHALEBRIDGE_QA_SCREENSHOTS,{recursive:true});fs.writeFileSync(path.join(process.env.WHALEBRIDGE_QA_SCREENSHOTS,name+'.png'),(await win.webContents.capturePage()).toPNG());};
 try {
  await win.loadURL(origin);await ready();
  // Each HTTP method survives a real submit, state reload, and reopen.
  for(const method of ['POST','']){
   await js(`openProvider('relay');activateEditorSection('balance')`);
   await fill('#f-balanceMethod',method);await js(`window.remainingStateBeforeSave=state`);await click('#save');await wait(`!document.querySelector('#editor').open`);
   await reloaded(`state.providers.find(p=>p.id==='relay')?.balanceMethod===${JSON.stringify(method)}`);
   assert.equal(writes.findLast(w=>w.path==='/api/provider').body.balanceMethod,method);
   await js(`openProvider('relay');activateEditorSection('balance')`);
   assert.equal(await js(`document.querySelector('#f-balanceMethod').value`),method);await close();
  }
  await js(`openProvider('','new','nous')`);
  assert.deepEqual(await js(`['chat','responses','anthropic','gemini'].map(k=>document.querySelector('#f-'+k).value)`),[presets[0].chat,'','','']);
  assert.equal(await js(`document.querySelector('#f-key').value`),'');await close();
  await js(`openProvider('','new','siliconflow')`);
  for(const region of ['cn','intl','cn']){
   await fill('#f-region',region);
   const suffix=region==='cn'?'cn':'com';
   assert.deepEqual(await js(`['chat','website','keysUrl','key'].map(k=>document.querySelector('#f-'+k).value)`),[`https://api.siliconflow.${suffix}/v1`,`https://cloud.siliconflow.${suffix}`,`https://cloud.siliconflow.${suffix}/account/ak`,'']);
   assert.equal(await js(`document.querySelectorAll('#provider-links a').length`),2);
  }
  await close();await js(`openProvider('','new','bedrock-mantle')`);
  assert.equal(await js(`document.querySelector('#f-region').options.length`),18,'17 Mantle regions plus custom');
  await fill('#f-region','sa-east-1');await js(`activateEditorSection('compatibility')`);
  assert.equal(await js(`document.querySelector('#f-authHeader').disabled&&document.querySelector('#custom-auth-header').hidden&&!document.querySelector('#fixed-bedrock-auth').hidden`),true);
  assert.match(await js(`document.querySelector('#fixed-bedrock-auth').textContent`),/x-api-key.*Bearer/);
  await js(`activateEditorSection('connection')`);assert.equal(await js(`document.querySelector('#f-region').value`),'sa-east-1');
  assert.equal(await js(`document.querySelector('#f-chat').value`),regions.at(-1).chat);
  await js(`window.remainingStateBeforeSave=state`);await click('#save');await wait(`editor?.type==='connection-success'`);
  await reloaded(`state.providers.find(p=>p.id==='bedrock-mantle')?.chat===${JSON.stringify(regions.at(-1).chat)}`);await close();
  await js(`openProvider('bedrock-mantle')`);assert.equal(await js(`document.querySelector('#f-region').value`),'sa-east-1');await close();
  await js(`openProvider('','new','bedrock-mantle')`);await fill('#f-preset','nous');
  assert.equal(await js(`!document.querySelector('#f-authHeader').disabled&&!document.querySelector('#custom-auth-header').hidden&&document.querySelector('#fixed-bedrock-auth').hidden`),true);await close();
  await js(`go('settings');globalSection='tools';render()`);await ready();
  assert.deepEqual(await rows(),['you','tavily','deepseek'],'saved order wins over vendor catalog order');
  assert.equal(writes.filter(w=>w.path==='/api/search/quota').length,0,'opening settings does not read quota');
  await action('switch-search','you');await wait(`document.querySelector('[data-global="switch-search"][data-id="you"]').getAttribute('aria-pressed')==='false'`);
  assert.deepEqual(writes.findLast(w=>w.path==='/api/search/switch').body,{vendor:'you',on:false});assert.equal(secrets.get('you'),'fixture-you');
  await action('switch-search','you');await wait(`document.querySelector('[data-global="switch-search"][data-id="you"]').getAttribute('aria-pressed')==='true'`);
  assert.equal(secrets.get('you'),'fixture-you');
  await action('move-search-up','tavily');await wait(`document.querySelector('[data-search-api]').dataset.searchApi==='tavily'`);
  assert.deepEqual(writes.findLast(w=>w.path==='/api/search/order').body.vendors,['tavily','you']);
  await action('move-search-down','tavily');await wait(`document.querySelector('[data-search-api]').dataset.searchApi==='you'`);
  assert.deepEqual(writes.findLast(w=>w.path==='/api/search/order').body.vendors,['you','tavily']);
  for(const pick of ['order','turns']){
   await action('edit-search','tavily');await fill('#search-edit-form [name=pick]',pick);
   assert.equal(await js(`document.querySelector('#search-edit-form [name=key]').value`),'');
   await click('#search-edit-form [type=submit]');await wait(`document.querySelector('#search-editor').hidden`);
   const saved=writes.findLast(w=>w.path==='/api/search/save').body;assert.equal(saved.pick,pick);assert.equal(saved.key,'');assert.equal(secrets.get('tavily'),'fixture-tavily');
   await action('edit-search','tavily');assert.equal(await js(`document.querySelector('#search-edit-form [name=pick]').value`),pick);await click('#close-search-edit');
  }
  await action('edit-search','deepseek');await fill('#search-edit-form [name=key]','fixture-deepseek');await click('#search-edit-form [type=submit]');await wait(`document.querySelector('[data-global="remove-search"][data-id="deepseek"]')`);
  assert.equal(writes.findLast(w=>w.path==='/api/search/save').body.vendor,'deepseek');
  assert.equal(writes.filter(w=>w.path==='/api/search/quota').length,0);
  await action('search-quota','tavily');await wait(`!document.querySelector('[data-search-quota="tavily"]').hidden`);
  assert.deepEqual(writes.findLast(w=>w.path==='/api/search/quota').body,{vendor:'tavily'});
  assert.equal(writes.filter(w=>w.path==='/api/search/quota').length,1);
  const cells=await js(`[...document.querySelectorAll('[data-search-quota="tavily"] tbody tr')].map(r=>[...r.cells].map(c=>c.textContent))`);
  assert.equal(cells[0][1],'未报告');assert.match(cells[1][1],/quota refused/);assert.match(cells[0][2],/<img/);assert.match(cells[1][2],/<b>/);
  assert.equal(await js(`document.querySelector('[data-search-quota="tavily"]').querySelector('img,svg,b')===null&&!window.quotaInjected`),true,'quota plan/errors are escaped');
  for(const width of [1120,390])for(const theme of ['light','dark']){
   nativeTheme.themeSource=theme;await resize(width);
   const page=await js(`({width:innerWidth,scroll:document.documentElement.scrollWidth})`);assert.ok(page.scroll<=page.width+1,JSON.stringify({width,theme,...page}));
   if(width===390){
    const quota=await js(`(()=>{const root=document.querySelector('[data-search-quota="tavily"]'),wrap=root.querySelector('.table-wrap');return {wrap:{client:wrap.clientWidth,scroll:wrap.scrollWidth},headerHidden:getComputedStyle(root.querySelector('thead')).display==='none',cells:[...root.querySelectorAll('td')].map(c=>({label:c.dataset.label,before:getComputedStyle(c,'::before').content,display:getComputedStyle(c).display,width:c.getBoundingClientRect().width,rowWidth:c.parentElement.getBoundingClientRect().width,client:c.clientWidth,scroll:c.scrollWidth}))};})()`);
    assert.ok(quota.headerHidden,`${theme}: narrow quota uses labels instead of a squeezed table header`);
    assert.ok(quota.wrap.scroll<=quota.wrap.client+1,`${theme}: quota container has no horizontal overflow: ${JSON.stringify(quota)}`);
    assert.deepEqual(quota.cells.map(c=>c.label),['密钥','已用 / 上限','套餐与状态','密钥','已用 / 上限','套餐与状态']);
    for(const cell of quota.cells){
     assert.equal(cell.display,'block',`${theme}: quota values stack vertically`);
     assert.ok(cell.before.includes(cell.label),`${theme}: each value keeps its visible label`);
     assert.ok(cell.width>=240&&Math.abs(cell.width-cell.rowWidth)<=1,`${theme}: each quota value gets readable full-row width: ${JSON.stringify(cell)}`);
     assert.ok(cell.scroll<=cell.client+1,`${theme}: quota value wraps without clipping: ${JSON.stringify(cell)}`);
    }
   }
   if(width===1120&&theme==='light'||width===390&&theme==='dark'){await js(`document.querySelector('[data-search-api="${width===390?'tavily':'you'}"]').scrollIntoView({block:'start'})`);await screenshot(width===1120?'search-management-light':'search-management-narrow');}
   await js(`openProvider('bedrock-mantle');activateEditorSection('compatibility')`);
   assert.equal(await js(`document.querySelector('#editor').scrollWidth<=document.querySelector('#editor').clientWidth+1`),true,`${width}/${theme} provider editor overflow`);await close();
  }
  assert.deepEqual(external,[],'all API/assets stayed local');
  console.log('WHALEBRIDGE_REMAINING_RESULT:PASS');
 }finally{win.destroy();server.close();}
})().then(()=>app.exit(0),error=>{console.error(error);server.close();app.exit(1)});
