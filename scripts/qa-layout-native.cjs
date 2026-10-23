'use strict';
// Run with Electron. Every application API and asset comes from the built Go
// component; the fixture supplies only local upstream data and an isolated home.
const { app, BrowserWindow, nativeTheme } = require('electron');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { startFixture } = require('./lib/ui-native-fixture.cjs');
const { inspectAlignment } = require('./lib/ui-alignment.cjs');
app.disableHardwareAcceleration();
const repo = path.resolve(__dirname, '..');
const evidence = path.resolve(process.env.WHALEBRIDGE_QA_LAYOUT || path.join(repo, '.tmp/ui-layout-audit'));
fs.mkdirSync(evidence, { recursive: true });
app.setPath('userData', path.join(evidence, 'electron-profile'));
app.on('window-all-closed', () => {});
const payload = path.resolve(process.env.WHALEBRIDGE_QA_PACKAGE || path.join(repo, '.tmp/ui-audit-package'));
const report = { kind: 'native-component-ui-layout', started: new Date().toISOString(), views: [], failures: [], runtimeErrors: [], coverage: {}, conditions: [] };
let win, fixture;
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const js = async source => {
  try { return await win.webContents.executeJavaScript(source); }
  catch (error) { throw new Error(`${error.message}\n${source.slice(0, 250)}`); }
};
async function wait(expression, label = expression) {
  const until = Date.now() + (/agents-page|agent-target|data-profile-delete/.test(expression)?45000:18000);
  while (Date.now() < until) {
    if (await js(`Boolean(${expression})`)) return;
    await sleep(30);
  }
  report.unsettled=await js(`({label:${JSON.stringify(label)},page:tab,editor:editor?.type,contentBusy:document.querySelector('#content').getAttribute('aria-busy'),agentBusy:document.querySelector('#agents-page')?.getAttribute('aria-busy'),message:document.querySelector('#message-text').textContent,requests:performance.getEntriesByType('resource').filter(e=>e.name.includes('/api/')).slice(-12).map(e=>({path:new URL(e.name).pathname,ms:e.duration}))})`);
  throw new Error('Unsettled ' + label);
}
async function settle() {
  await wait(`document.querySelector('#content').getAttribute('aria-busy') === 'false'`);
  await js(`Promise.all(document.getAnimations().filter(a=>a.effect.getTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})))`);
  await js('new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))');
}
async function click(selector) {
  await wait(`document.querySelector(${JSON.stringify(selector)})`);
  await js(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});if(e.disabled)throw Error('Disabled '+${JSON.stringify(selector)});for(let p=e.parentElement;p;p=p.parentElement)if(p.tagName==='DETAILS'&&!p.classList.contains('menu'))p.open=true;e.scrollIntoView({block:'center',inline:'nearest'});})()`);
  await js('new Promise(r=>requestAnimationFrame(r))');
  const point = await js(`(()=>{const e=document.querySelector(${JSON.stringify(selector)}),r=e.getBoundingClientRect(),x=r.x+r.width/2,y=r.y+r.height/2,hit=document.elementFromPoint(x,y);if(!r.width||!r.height||!hit||!(e===hit||e.contains(hit)))throw Error('Unreachable '+${JSON.stringify(selector)});return {x:Math.round(x),y:Math.round(y)}})()`);
  win.webContents.sendInputEvent({type:'mouseDown',button:'left',clickCount:1,...point});
  win.webContents.sendInputEvent({type:'mouseUp',button:'left',clickCount:1,...point});
  await js('new Promise(r=>requestAnimationFrame(r))');
}
async function input(selector, value, event = 'input') {
  await js(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});if(!e)throw Error('Missing input');e.value=${JSON.stringify(value)};e.dispatchEvent(new Event(${JSON.stringify(event)},{bubbles:true}));})()`);
}
async function close() {
  await js(`(()=>{document.querySelectorAll('.menu-content:popover-open').forEach(e=>e.hidePopover());const c=document.querySelector('#confirmation');if(c.open)c.close();const d=document.querySelector('#editor');if(d.open)d.close();message('');})()`);
  await settle();
}
async function page(tab) {
  await close();
  await input('#narrow-page', tab, 'change');
  await wait(`tab===${JSON.stringify(tab)} && document.querySelector('#content').getAttribute('aria-busy')==='false'`);
  await js(`document.querySelector('#main').scrollTo(0,0)`);
  await settle();
}
async function section(name) {
  await click(`[data-global-section="${name}"]`);
  await settle();
  await js(`document.querySelector('#main').scrollTo(0,0)`);
}
async function editorSection(name) {
  await click(`#editor-nav [data-section-target="${name}"]`);
  await settle();
}
async function screenshot(name) {
  fs.writeFileSync(path.join(evidence, name + '.png'), (await win.webContents.capturePage()).toPNG());
}
function geometry() {
  const visible=e=>e.getClientRects().length&&getComputedStyle(e).visibility!=='hidden';
  const selector=e=>e.id?'#'+e.id:e.tagName.toLowerCase()+(typeof e.className==='string'&&e.className.trim()?'.'+e.className.trim().split(/\s+/).join('.'):'');
  const localScroll=e=>{for(let p=e.parentElement;p&&p!==document.body;p=p.parentElement){const s=getComputedStyle(p);if(['auto','scroll'].includes(s.overflowX)&&p.scrollWidth>p.clientWidth+2)return true;}return false;};
  const clipped=[];
  const roots=[document.querySelector('#main'),...document.querySelectorAll('dialog[open]')];
  for(const root of roots){if(root.scrollWidth>root.clientWidth+2)clipped.push({kind:'root-horizontal',element:selector(root),scroll:root.scrollWidth,width:root.clientWidth});}
  const ids=new Map(), activeDialog=document.querySelector('#confirmation[open]')||document.querySelector('#editor[open]');
  for(const e of (activeDialog||document).querySelectorAll('[id]'))if(visible(e)){const id=e.id;if(ids.has(id))clipped.push({kind:'duplicate-id',element:'#'+id});else ids.set(id,e);}
  for(const e of document.querySelectorAll('pre,dd,.route-record>summary,.model-supplier,.source-choice,.toolbar,.brand,.quota-window>div,.source-choice-head,.notice,.agent-profile>.row')){
    if(!visible(e))continue;
    const s=getComputedStyle(e),r=e.getBoundingClientRect();
    if(e.scrollWidth>e.clientWidth+3&&!['auto','scroll'].includes(s.overflowX)&&s.textOverflow!=='ellipsis'&&!localScroll(e))clipped.push({kind:'text-clipped',element:selector(e),scroll:e.scrollWidth,width:e.clientWidth,overflow:s.overflowX,whiteSpace:s.whiteSpace,rect:{x:r.x,y:r.y,w:r.width,h:r.height}});
  }
  const d=document.querySelector('#confirmation[open]')||document.querySelector('#editor[open]');
  let dialog=null;
  if(d){const r=d.getBoundingClientRect();dialog={width:r.width,height:r.height};if(r.left<0||r.right>innerWidth+1||r.top<0||r.bottom>innerHeight+1)clipped.push({kind:'dialog-outside',rect:r.toJSON()});for(const e of d.querySelectorAll('.dialog-head .icon-button,.dialog-actions>button')){if(!visible(e))continue;const r=e.getBoundingClientRect(),hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);if(r.y<0||r.bottom>innerHeight+1||!hit||!(hit===e||e.contains(hit)))clipped.push({kind:'dialog-action-covered',element:selector(e)});}}
  const main=document.querySelector('#main');
  return {width:innerWidth,height:innerHeight,dpr:devicePixelRatio,theme:matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light',page:document.querySelector('#page-label').textContent,dialog,main:{width:main.clientWidth,height:main.clientHeight,scrollHeight:main.scrollHeight,scrollWidth:main.scrollWidth},issues:clipped};
}
async function snapshot(){return js(`(()=>{const layout=(${geometry.toString()})();const alignment=(${inspectAlignment.toString()})();return {...layout,alignment,issues:[...layout.issues,...alignment.issues]};})()`);}
let condition;
async function audit(name, { shot = true, bottom = false, expand = false } = {}) {
  await settle();
  console.log('LAYOUT_VIEW '+condition.name+' '+name);
  if(expand)await js(`document.querySelectorAll('#content details:not(.menu),#fields details:not(.menu)').forEach(e=>e.open=true)`);
  const g = await snapshot();
  const id=`${condition.name}-${name}`;
  report.views.push({id,...g});
  if(g.issues.length){report.failures.push({id,issues:g.issues});console.log('LAYOUT_ISSUES '+id+' '+JSON.stringify(g.issues));}
  report.coverage[name]=(report.coverage[name]||0)+1;
  fs.writeFileSync(path.join(evidence,'layout-report.json'),JSON.stringify(report,null,2)+'\n');
  if(shot && (condition.shots || g.issues.length))await screenshot(id);
  if(bottom){await js(`(()=>{const e=document.querySelector('#editor[open] .editor-content')||document.querySelector('#main');e.scrollTop=e.scrollHeight;})()`);await settle();const b=await snapshot();report.views.push({id:id+'-bottom',...b});if(b.issues.length)report.failures.push({id:id+'-bottom',issues:b.issues});if(shot&&condition.shots)await screenshot(id+'-bottom');}
}
async function optional(name, work) {
  try { await work(); }
  catch(error) { report.failures.push({id:condition.name+'-'+name,error:error.message}); await screenshot(condition.name+'-'+name+'-error'); await close(); }
}
async function runViews() {
  await page('overview'); await audit('overview',{bottom:true,expand:true});
  await js(`(()=>{const e=document.querySelector('#quota-notices');e.hidden=false;e.textContent='提醒 · 60';})()`);await audit('quota-notice-toolbar');
  await click('#quota-notices');await audit('quota-notices-empty');await close();await js(`document.querySelector('#quota-notices').hidden=true`);
  await page('agents'); await audit('agents-dsh',{bottom:true});
  const targets=await js(`[...document.querySelector('#agent-target').options].map(o=>o.value)`);
  for(const target of ['codex','claude','cline','cherry-studio'].filter(t=>targets.includes(t))){await input('#agent-target',target,'change');await wait(`document.querySelector('#agent-target').value===${JSON.stringify(target)}&&!document.querySelector('#agents-page')?.hasAttribute('aria-busy')&&document.querySelector('#agent-name')?.textContent!=='鲸屿'`);await settle();await audit('agents-'+target,{bottom:true,expand:true});}
  const agentRows=(await fixture.api('/api/agents')).agents;
  const pickAgent=async id=>{await wait(`!document.querySelector('#agent-target').disabled&&!document.querySelector('#agents-page')?.hasAttribute('aria-busy')`);await input('#agent-target',id,'change');await wait(`document.querySelector('#agent-target').value===${JSON.stringify(id)}&&!document.querySelector('#agent-target').disabled&&!document.querySelector('#agents-page')?.hasAttribute('aria-busy')`);await settle();};
  await pickAgent('codex');
  if(await js(`!!document.querySelector('[data-agent-action="connect"]')`)){await click('[data-agent-action="connect"]');await wait(`!document.querySelector('#agents-page')?.hasAttribute('aria-busy')&&!document.querySelector('[data-agent-action="connect"]')`);}
  await audit('agents-connected',{bottom:true});
  await input('#agent-model-query','no-layout-agent-model');await audit('agent-models-empty');await input('#agent-model-query','');
  const launch=agentRows.find(a=>a.id==='agy'),importable=agentRows.find(a=>a.importable);
  if(launch){await pickAgent(launch.id);if(await js(`!!document.querySelector('[data-agent-action="connect"]')`)){await click('[data-agent-action="connect"]');await wait(`!document.querySelector('#agents-page')?.hasAttribute('aria-busy')&&!document.querySelector('[data-agent-action="connect"]')`);}await click('[data-agent-action="launch"]');await wait(`document.querySelector('#agent-material-value')`);await audit('agent-launch-command',{bottom:true});}
  if(importable){await pickAgent(importable.id);await click('[data-agent-action="import"]');await wait(`document.querySelector('#agent-material-value')`);await audit('agent-import-link',{bottom:true});}
  await pickAgent('codex');
  const profileName='本地审查方案 · Long native profile name';
  if(!await js(`!!document.querySelector('[data-profile-delete]')`)){await input('#agent-profile-name',profileName);await click('#agent-profile-save [type="submit"]');await wait(`document.querySelector('[data-profile-delete]')&&!document.querySelector('#agents-page')?.hasAttribute('aria-busy')`);}
  await audit('agent-profiles',{bottom:true,expand:true});
  await click('[data-profile-apply]');await wait(`document.querySelector('#confirmation').open`);await audit('agent-profile-apply-confirm');await click('#cancel-confirm');
  await click('[data-profile-delete]');await wait(`document.querySelector('#confirmation').open`);await audit('agent-profile-delete-confirm');await click('#cancel-confirm');
  await page('providers'); await audit('providers',{bottom:true});
  await optional('provider-menu',async()=>{await click('.provider-table details.menu summary');await wait(`document.querySelector('.menu-content:popover-open')`);await audit('provider-menu');await js(`document.querySelector('.menu-content:popover-open').hidePopover()`);});
  await input('#provider-search','no-matching-provider-layout');await settle();await audit('providers-empty-search');await input('#provider-search','');
  await click('[data-action="add-provider"]');await wait(`editor?.type==='sources'&&!editor.loading`);await audit('sources',{bottom:true});
  for(const scope of ['plans','api','plugins','local']){await input('#source-scope',scope,'change');await audit('sources-'+scope);}
  await input('#source-search','no-matching-layout-source');await audit('sources-empty');await input('#source-search','');
  await click('[data-source-command="custom"]');await wait(`editor?.type==='provider'`);await audit('new-provider',{bottom:true});await close();
  const providerID=await js(`state.providers.find(p=>!p.account&&!p.off)?.id`);assert.ok(providerID,'API provider fixture required');
  await js(`openProvider(${JSON.stringify(providerID)})`);
  const providerSections=await js(`[...document.querySelectorAll('#editor-nav [data-section-target]')].map(b=>b.dataset.sectionTarget)`);
  for(const name of providerSections){await editorSection(name);await audit('provider-'+name,{bottom:true});}
  await editorSection('models');
  await click('[data-action="configure-provider-model"]');await audit('model-parameters',{bottom:true,expand:true});
  await click('[data-pref="customPrice"]');
  await audit('model-custom-prices',{bottom:true});
  await click('[data-action="add-price-tier"]');await audit('model-price-tiers',{bottom:true});
  await input('#provider-model-search','no-layout-model');await js(`document.querySelector('.editor-content').scrollTop=0`);await audit('provider-models-empty');await close();
  await js(`openKeys(${JSON.stringify(providerID)})`);await wait(`editor?.type==='keys'`);await audit('keys',{bottom:true});
  await click('[data-action="key-fold"]');await audit('keys-expanded',{bottom:true});
  await editorSection('new-key');await audit('new-key');await editorSection('keys');
  await input('#f-keySearch','no-matching-key-layout');await audit('keys-empty-search');await input('#f-keySearch','');
  await optional('key-edit',async()=>{await click('[data-action="key-edit"]');await wait(`editor?.type==='key-edit'`);for(const name of ['key-basic','key-routing']){await editorSection(name);await audit(name,{bottom:true,expand:true});}});
  await close();await js(`openKeyImport(${JSON.stringify(providerID)})`);await audit('key-import');await close();
  await optional('accounts',async()=>{const id=await js(`state.providers.find(p=>p.account)?.id`);assert.ok(id,'local subscription account fixture');await js(`openAccounts(${JSON.stringify(id)})`);await wait(`editor?.type==='accounts'&&!document.querySelector('[data-action="accounts-refresh"]')?.disabled`);await audit('accounts',{bottom:true});await click('[data-action="account-edit"]');await wait(`editor?.type==='account-edit'`);for(const name of ['account-identity','account-models','account-proxy']){await editorSection(name);await audit(name,{bottom:true,expand:true});}await close();await js(`openAuthImport('codex')`);await audit('auth-import');await close();});
  await js(`openSubscription(${JSON.stringify((await fixture.api('/api/subscriptions')).find(s=>s.plugin&&s.pid==='uiaudit')?.id)})`);await wait(`editor?.type==='subscription'`);await audit('subscription-login-form',{bottom:true});await close();
  await js(`showConnectionSuccess('本地审查接入完成','此卡片验证连接完成后的操作布局，未访问任何外部供应商。',{name:'本地订阅账号 · Long name for success layout',user:'offline-team-member@fixture.invalid',kind:'订阅账号'})`);await audit('connection-success');await close();
  await page('models');await audit('models',{bottom:true});await input('#model-search','no-matching-model-layout');await audit('models-empty-search');await input('#model-search','');
  await page('routing');await audit('routing-groups',{bottom:true});
  const groupID=await js(`state.groups[0]?.id`);assert.ok(groupID,'routing fixture');await js(`openGroup(${JSON.stringify(groupID)})`);
  for(const name of ['members','routing','conditions','levels']){await editorSection(name);await audit('group-'+name,{bottom:true,expand:true});}
  await close();
  await page('settings');
  for(const name of ['connection','tools','privacy','telemetry','backup']){await section(name);await audit('settings-'+name,{bottom:true,expand:true});if(name==='tools'){await click('[data-global="edit-search"]');await audit('search-editor',{bottom:true});await optional('search-close',()=>click('#close-search-edit'));}if(name==='backup'){for(const kind of ['s3','github','webdav']){await input('#sync-form [name="syncKind"]',kind,'change');await audit('sync-'+kind,{bottom:true});}report.coverage['backup-label-targets']=await js(`(()=>{const forms=[...document.querySelectorAll('#backup-export-form,#backup-import-form')];return forms.every(f=>[...f.querySelectorAll('label[for]')].every(l=>l.control&&f.contains(l.control)));})()`);}}
  await section('privacy');await click('[data-global="add-rule"]');await audit('redaction-rule-editor',{bottom:true});
  await page('usage');
  for(const name of ['summary','ledger','quotas','routing']){await section(name);if(name==='quotas')await wait(`document.querySelector('#quota-section')&&document.querySelector('#quota-section').getAttribute('aria-busy')!=='true'`);await audit('usage-'+name,{bottom:true,expand:true});}
  await section('ledger');
  const archiveID=report.gatewayResults.find(r=>r.status===200)?.archive;assert.ok(archiveID);
  await click('[data-global="view-archive"][data-id$="/'+archiveID+'"]');await wait(`document.querySelector('#editor').open`);await audit('request-archive',{bottom:true});
  report.coverage['actual-archive-long-body']=await js(`document.querySelector('.request-archive').textContent.includes('request-metadata-')&&document.querySelector('.request-archive').textContent.includes('response-metadata-')`);await close();
  await click('[data-global="show-route"]');await wait(`document.querySelector('#editor').open`);await audit('route-detail-dialog',{bottom:true,expand:true});await close();
  await section('quotas');
  await click('[data-action="subscription-settings"]');await wait(`editor?.type==='subscription-settings'`);for(const name of ['warmup','checkins','alerts','connection']){await editorSection(name);await audit('subscription-'+name,{bottom:true});}await close();
  await click('[data-action="quota-history"]');await wait(`editor?.type==='quota-history'&&document.querySelector('#quota-history-chart').getAttribute('aria-busy')!=='true'`);await audit('quota-history',{bottom:true,expand:true});await input('#f-historyDays','custom','change');await audit('quota-history-custom');await close();
  await page('providers');await click('[data-action="manage-adapters"]');await wait(`editor?.type==='adapters'`);for(const name of ['installed','discover','ownership','moves']){await editorSection(name);await audit('adapters-'+name,{bottom:true,expand:true});}await close();
  await js(`openAdapters('installed')`);await click('[data-action="adapter-options"]');await wait(`editor?.type==='adapter-options'`);await audit('adapter-options');await input('#f-options','{invalid-json');await click('#save');await wait(`document.querySelector('#message').dataset.error==='true'`);await audit('adapter-options-invalid');await close();
  await js(`openAdapters('installed')`);await click('[data-action="adapter-page"]');await wait(`editor?.type==='adapter-readme'`);await audit('adapter-readme',{bottom:true});await close();
  await click('[data-action="provider-health"]');await wait(`editor?.type==='health'`);await audit('provider-health',{bottom:true});await close();
  await click('[data-action="import-provider"]');await audit('provider-import');await close();
  await js(`exportProvider(${JSON.stringify(providerID)})`);await wait(`editor?.type==='export'&&document.querySelector('#editor').open`);await audit('provider-export');await close();
  await page('help');await audit('help',{bottom:true,expand:true});
  await js(`void ask('删除名称很长的路由组及其配置确认','仅用于本地布局审查，取消不会改变任何配置。'.repeat(20),'确认删除')`);await audit('confirmation-long');await close();
  await js(`message('本地测试错误：'+('REQUEST_METADATA_'.repeat(80)),true)`);await audit('long-message');await js(`message('')`);
}
async function runSupplementViews() {
  await page('providers');
  await click('.provider-table details.menu summary');await wait(`document.querySelector('.menu-content:popover-open')`);await audit('provider-menu-retest');await js(`document.querySelector('.menu-content:popover-open').hidePopover()`);await close();
  await page('agents');await input('#agent-target','codex','change');await wait(`document.querySelector('#agent-name')?.textContent==='Codex'&&!document.querySelector('#agents-page')?.hasAttribute('aria-busy')`);await settle();
  if(await js(`!!document.querySelector('[data-agent-action="connect"]')`)){await click('[data-agent-action="connect"]');await wait(`!document.querySelector('#agents-page')?.hasAttribute('aria-busy')&&!document.querySelector('[data-agent-action="connect"]')`);}
  const selector=await js(`(()=>{const e=document.querySelector('[data-agent-field] select');return {id:'#'+e.id,value:[...e.options].find(o=>o.value!==e.value)?.value};})()`);assert.notEqual(selector.value,undefined);await input(selector.id,selector.value,'change');await audit('agent-unapplied-field',{bottom:true});
  await click('[data-model-list="none"]');await audit('agent-model-selection-draft',{bottom:true});await click('[data-model-list="cancel"]');
  await page('providers');await js(`openSubscription(${JSON.stringify((await fixture.api('/api/subscriptions')).find(s=>s.plugin&&s.pid==='uiaudit')?.id)})`);await wait(`editor?.type==='subscription'`);
  await wait(`document.querySelector('#f-method')?.options.length===2`);await audit('subscription-login-form-retest',{bottom:true});
  await input('#f-method','1','change');await click('#save');await wait(`document.querySelector('#f-answer')?.tagName==='SELECT'`);await audit('subscription-prompt-select');
  await click('#save');await wait(`document.querySelector('#f-answer')?.tagName==='INPUT'`);await audit('subscription-prompt-text');
  await input('#f-answer','offline-team-long-account-workspace');await click('#save');await wait(`document.querySelector('#f-callback')`);await audit('subscription-waiting',{bottom:true});
  await input('#f-callback','offline-good');await click('[data-action="signin-callback"]');await wait(`editor?.type==='connection-success'`);await audit('subscription-real-success',{bottom:true});await close();
  await js(`openSubscription('copilot')`);await audit('subscription-enterprise');await close();
  await js(`openSubscription('zcode')`);await audit('subscription-retired');await close();
  await js(`openAuthImport('codex')`);await input('#f-source',JSON.stringify([{email:'offline-import-account-with-long-display-name@fixture.invalid',access_token:'offline-access-without-refresh'},{email:'other-offline-account@fixture.invalid',access_token:'offline-access-without-refresh'}]));await click('#save');await wait(`editor?.type==='import-result'`);await audit('account-import-results',{bottom:true});await close();
  await page('overview');await js(`quotaNotices=Array.from({length:60},(_,i)=>({at:new Date().toISOString(),alert:{name:'本地额度提醒长名称 '+i+' · Long account name for quota notice layout',user:'offline-team-account-with-long-name@fixture.invalid',used:95,window:'每周额度',provider:'audit-offline'}}));document.querySelector('#quota-notices').hidden=false`);await click('#quota-notices');await audit('quota-notices-full',{bottom:true});await close();
}
async function runToolbarViews() {
  for(const name of ['overview','agents','providers','models','routing','usage','settings','help']){await page(name);await js(`(()=>{const reminder=document.querySelector('#quota-notices');reminder.hidden=false;reminder.textContent='提醒 · 60';})()`);await audit('toolbar-'+name);}
  await page('providers');await js(`document.querySelector('#quota-notices').hidden=false`);await click('.provider-table details.menu summary');await wait(`document.querySelector('.menu-content:popover-open')`);await audit('provider-menu-toolbar');await close();
}
async function run() {
  await app.whenReady();
  fixture=await startFixture({repo,payload,root:path.join(evidence,'native-fixture')});
  report.fixture=fixture.stats;
  win=new BrowserWindow({width:1120,height:820,show:false,webPreferences:{contextIsolation:true,nodeIntegration:false,backgroundThrottling:false}});
  win.showInactive();
  win.webContents.on('render-process-gone',(_e,details)=>report.runtimeErrors.push(details));
  win.webContents.on('console-message',event=>{if(event.level==='error')report.runtimeErrors.push({message:String(event.message).slice(0,500)});});
  win.webContents.setWindowOpenHandler(()=>({action:'deny'}));
  await win.loadURL(fixture.url);await settle();
  if(process.env.WHALEBRIDGE_QA_SKIP_ROUTES!=='1')report.gatewayResults=await fixture.generateRoutes();
  if(process.env.WHALEBRIDGE_QA_EXPECT_NAME!=='legacy'){
    assert.equal(await js(`document.querySelector('.brand strong').textContent`),'鲸Bridge');
    assert.match(await js('document.title'),/^鲸Bridge/);
  }
  const conditions=[{name:'desktop-light',width:1120,height:780,theme:'light',shots:true},{name:'desktop-dark',width:1120,height:780,theme:'dark',shots:false},{name:'tablet-light',width:820,height:700,theme:'light',shots:false},{name:'narrow-dark',width:390,height:760,theme:'dark',shots:true},{name:'narrow-short-light',width:390,height:520,theme:'light',shots:false},{name:'desktop-short-dark',width:1120,height:480,theme:'dark',shots:false}];
  const selected=process.env.WHALEBRIDGE_QA_LAYOUT_CONDITIONS?.split(',');
  for(condition of conditions.filter(c=>!selected||selected.includes(c.name))){
    if(['supplement','toolbar'].includes(process.env.WHALEBRIDGE_QA_LAYOUT_MODE))condition.shots=true;
    nativeTheme.themeSource=condition.theme;win.setContentSize(condition.width,condition.height);
    await wait(`innerWidth===${condition.width}&&innerHeight===${condition.height}`);
    report.conditions.push(condition);
    console.log('LAYOUT_CONDITION '+condition.name);
    if(process.env.WHALEBRIDGE_QA_LAYOUT_MODE==='supplement')await runSupplementViews();else if(process.env.WHALEBRIDGE_QA_LAYOUT_MODE==='toolbar')await runToolbarViews();else await runViews();
    fs.writeFileSync(path.join(evidence,'layout-report.json'),JSON.stringify(report,null,2)+'\n');
  }
  if(report.coverage['backup-label-targets']===false)report.failures.push({id:'backup-label-targets',error:'Password label targets a different form'});
  if(!['supplement','toolbar'].includes(process.env.WHALEBRIDGE_QA_LAYOUT_MODE)){
    assert.equal(report.coverage['actual-archive-long-body'],true,'Real archived local request and response must be rendered');
    assert.ok(report.fixture.s3Reads>0,'Actual archive GET must reach the local S3 backend');
  }
  report.finished=new Date().toISOString();
  report.status=report.failures.length||report.runtimeErrors.length?'issues-found':'passed';
  console.log('WHALEBRIDGE_NATIVE_LAYOUT_RESULT:'+JSON.stringify({status:report.status,views:report.views.length,surfaces:Object.keys(report.coverage).length,failures:report.failures.length,runtimeErrors:report.runtimeErrors.length}));
}
run().catch(async error=>{report.fatal=error.stack;report.status='failed';console.error(error.stack);process.exitCode=1;if(win&&!win.isDestroyed())await screenshot('fatal').catch(()=>{});}).finally(async()=>{
  if(win&&!win.isDestroyed())win.destroy();
  if(fixture)await fixture.stop().catch(error=>{report.stopError=error.message;process.exitCode=1;});
  fs.writeFileSync(path.join(evidence,'layout-report.json'),JSON.stringify(report,null,2)+'\n');
  app.exit(process.exitCode||0);
});
