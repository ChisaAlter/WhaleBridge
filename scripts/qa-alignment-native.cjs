'use strict';
const {app,BrowserWindow,nativeTheme}=require('electron');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {startFixture}=require('./lib/ui-native-fixture.cjs');
const {inspectAlignment}=require('./lib/ui-alignment.cjs');
const repo=path.resolve(__dirname,'..'),out=path.resolve(process.env.WHALEBRIDGE_QA_ALIGN||path.join(repo,'.tmp/alignment-review')),payload=path.resolve(process.env.WHALEBRIDGE_QA_PACKAGE||path.join(repo,'.tmp/whalebridge-package'));
fs.mkdirSync(out,{recursive:true});app.setPath('userData',path.join(out,'electron-profile'));app.disableHardwareAcceleration();app.on('window-all-closed',()=>{});
const report={kind:'native-control-alignment',started:new Date().toISOString(),views:[],failures:[],errors:[]};let win,fixture;
async function run(){
 await app.whenReady();fixture=await startFixture({repo,payload,root:path.join(out,'native-fixture')});
 win=new BrowserWindow({show:false,width:1120,height:780,webPreferences:{contextIsolation:true,nodeIntegration:false,backgroundThrottling:false}});win.showInactive();
 win.webContents.on('console-message',e=>{if(e.level==='error')report.errors.push(e.message)});
 const js=s=>win.webContents.executeJavaScript(s),wait=async expr=>{const end=Date.now()+20000;while(!await js('Boolean('+expr+')')){if(Date.now()>end)throw Error('Alignment view timeout: '+expr);await new Promise(r=>setTimeout(r,25))}},settle=async()=>{await wait(`document.querySelector('#content').getAttribute('aria-busy')==='false'`);await js(`Promise.all(document.getAnimations().filter(a=>a.effect.getTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})))`);await js('document.fonts.ready');await js('new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))')},snap=async(name,crop)=>{await settle();const alignment=await js(`(${inspectAlignment.toString()})()`);report.views.push({name,...alignment});if(alignment.issues.length)report.failures.push({name,issues:alignment.issues});await js(`document.querySelector('#main').scrollTop=0`);await new Promise(r=>setTimeout(r,90));fs.writeFileSync(path.join(out,name+'.png'),(await win.webContents.capturePage()).toPNG());if(crop){const rect=await js(`(()=>{const r=document.querySelector(${JSON.stringify(crop)}).getBoundingClientRect();return {x:Math.max(0,Math.floor(r.x)),y:Math.max(0,Math.floor(r.y)),width:Math.ceil(r.width),height:Math.ceil(r.height)}})()`);fs.writeFileSync(path.join(out,name+'-tabs.png'),(await win.webContents.capturePage(rect)).toPNG())}};
 await win.loadURL(fixture.url);await settle();
 const controls=process.env.WHALEBRIDGE_QA_ALIGN_FOCUS==='controls';
  const conditions=[[1120,780,'dark'],[1120,780,'light'],[820,700,'light'],[390,760,'dark'],[390,520,'light'],[1120,480,'dark']];
  if(controls)conditions.push([640,700,'light'],[640,480,'dark']);
  for(const [width,height,theme] of conditions){
  win.setContentSize(width,height);nativeTheme.themeSource=theme;await wait(`innerWidth===${width}&&innerHeight===${height}`);
  const id=width+'-'+height+'-'+theme;
  if(controls){
    await js(`tab='settings';globalSection='privacy';render()`);await settle();await js(`document.querySelector('[data-global="add-rule"]').click()`);await snap(id+'-redaction-rule');
    const provider=fixture.seed.provider;
    await js(`openKeys(${JSON.stringify(provider)})`);await wait(`editor?.type==='keys'&&document.querySelector('#editor').open`);await snap(id+'-key-filter');await js(`document.querySelector('[data-action="key-edit"]').click()`);await wait(`editor?.type==='key-edit'`);await js(`activateEditorSection('key-routing');document.querySelectorAll('#fields details:not(.menu)').forEach(e=>e.open=true)`);await snap(id+'-key-routing');await js(`document.querySelector('.editor-content').scrollTop=document.querySelector('.editor-content').scrollHeight`);await snap(id+'-key-routing-bottom');await js(`document.querySelector('#editor').close()`);
    await js(`openProvider(${JSON.stringify(provider)})`);await wait(`editor?.type==='provider'&&document.querySelector('#editor').open`);await js(`activateEditorSection('models');document.querySelector('[data-action="configure-provider-model"]').click()`);await settle();await js(`document.querySelector('[data-pref="api"]').scrollIntoView({block:'center'})`);await snap(id+'-model-select');await js(`document.querySelector('#editor').close()`);
    await js(`openQuotaHistory()`);await wait(`editor?.type==='quota-history'&&document.querySelector('#quota-history-chart').getAttribute('aria-busy')==='false'`);await js(`(()=>{const e=document.querySelector('#f-historyDays');e.value='custom';e.dispatchEvent(new Event('change'));document.querySelector('.editor-content').scrollTop=0})()`);await snap(id+'-history-fields');
    const grid=await js(`(()=>{const e=document.querySelector('.editor-shell');return {hidden:document.querySelector('#editor-nav').hidden,rows:getComputedStyle(e).gridTemplateRows.split(' ').length,groups:document.querySelectorAll('#history-custom>.form-field').length}})()`);assert.equal(grid.hidden,true);assert.equal(grid.rows,1,'hidden dialog navigation leaves one content row');assert.equal(grid.groups,2,'date labels and controls stay paired');await js(`document.querySelector('#editor').close()`);
    await js(`tab='usage';usageSection='ledger';render()`);await snap(id+'-ledger-controls');
    await js(`tab='settings';globalSection='connection';render()`);await settle();await js(`(()=>{const busy=startButtonBusy(document.querySelector('#global-form [type=submit]'),'保存中');window.__finishAlignmentBusy=()=>busy.finish()})()`);await snap(id+'-busy-button');await js(`window.__finishAlignmentBusy()`);
    await js(`openAuthImport('codex')`);await settle();await js(`(()=>{const busy=startButtonBusy(document.querySelector('.file-button>span'),'读取中');window.__finishAlignmentBusy=()=>busy.finish()})()`);await snap(id+'-busy-file-label');await js(`window.__finishAlignmentBusy();document.querySelector('#editor').close()`);
    continue;
   }
   for(const [tab,section] of [['usage','summary'],['usage','ledger'],['usage','quotas'],['usage','routing'],['settings','connection'],['settings','tools'],['settings','privacy'],['settings','telemetry'],['settings','backup'],['models','']]){
   await js(`tab=${JSON.stringify(tab)};${tab==='settings'?`globalSection=${JSON.stringify(section)};`:tab==='usage'?`usageSection=${JSON.stringify(section)};`:''}render()`);await snap(id+'-'+tab+'-'+(section||'catalog'),tab==='models'?null:'.page-segments');
  }
 }
 report.status=report.failures.length||report.errors.length?'issues-found':'passed';console.log('WHALEBRIDGE_ALIGNMENT_RESULT:'+JSON.stringify({status:report.status,views:report.views.length,failures:report.failures.length,errors:report.errors.length}));
 if(report.status!=='passed'&&process.env.WHALEBRIDGE_QA_ALLOW_ISSUES!=='1')process.exitCode=1;
}
run().catch(e=>{report.status='failed';report.fatal=e.stack;console.error(e.stack);process.exitCode=1}).finally(async()=>{if(win&&!win.isDestroyed())win.destroy();if(fixture)await fixture.stop().catch(e=>{report.stopError=e.message;process.exitCode=1});report.finished=new Date().toISOString();fs.writeFileSync(path.join(out,'alignment-report.json'),JSON.stringify(report,null,2)+'\n');app.exit(process.exitCode||0)});
