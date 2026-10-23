'use strict';
// Real packaged component and authenticated management APIs. The loopback
// fixture supplies upstream replies, never the usage API or page assets.
const {app,BrowserWindow,nativeTheme}=require('electron');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path');
const {startFixture}=require('./lib/ui-native-fixture.cjs');
const repo=path.resolve(__dirname,'..'),evidence=path.resolve(process.env.WHALEBRIDGE_QA_USAGE||path.join(repo,'.tmp/usage-native-review'));
fs.mkdirSync(evidence,{recursive:true});app.setPath('userData',path.join(evidence,'electron-profile'));app.disableHardwareAcceleration();app.on('window-all-closed',()=>{});
const report={kind:'native-component-usage-review',started:new Date().toISOString(),views:[],errors:[]};
let win,fixture;
async function run(){
 await app.whenReady();fixture=await startFixture({repo,payload:process.env.WHALEBRIDGE_QA_PACKAGE||path.join(repo,'.tmp/whalebridge-package'),root:path.join(evidence,'native-fixture')});
 report.gateway=await fixture.generateRoutes();report.summary=await fixture.api('/api/usage?period=today');
 assert.ok(report.summary.calls>=50,'seed and real local requests are counted by the component');
 win=new BrowserWindow({show:false,width:1120,height:780,webPreferences:{contextIsolation:true,nodeIntegration:false,backgroundThrottling:false}});win.showInactive();
 win.webContents.on('console-message',e=>{if(e.level==='error')report.errors.push(e.message)});
 const js=source=>win.webContents.executeJavaScript(source),wait=async expression=>{const until=Date.now()+18000;while(!await js('Boolean('+expression+')')){if(Date.now()>until)throw Error('Native usage timeout: '+expression);await new Promise(r=>setTimeout(r,30))}},settle=async()=>{await wait(`document.querySelector('#content').getAttribute('aria-busy')==='false'`);await js('document.fonts.ready');await js('new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))');await new Promise(r=>setTimeout(r,120))},capture=async(name,selector)=>{await settle();if(selector)await js(`document.querySelector(${JSON.stringify(selector)}).scrollIntoView({block:'center'})`);else await js(`document.querySelector('#main').scrollTop=0`);await new Promise(r=>setTimeout(r,100));const layout=await js(`(()=>{const m=document.querySelector('#main');return {width:innerWidth,height:innerHeight,scroll:m.scrollWidth,client:m.clientWidth,metrics:[...document.querySelectorAll('.usage-metric')].map(e=>e.getBoundingClientRect().toJSON()),chart:!!document.querySelector('.usage-trend-panel'),empty:!!document.querySelector('.usage-empty'),distribution:(()=>{const e=document.querySelector('.usage-breakdown-scroll');return e?{width:e.clientWidth,scroll:e.scrollWidth}:null})(),toast:document.querySelector('#message-text').textContent};})()`);assert.ok(layout.scroll<=layout.client+2,JSON.stringify(layout));assert.notEqual(layout.toast,'[object Event]');if(layout.width<=600&&layout.distribution)assert.ok(layout.distribution.scroll<=layout.distribution.width+2,JSON.stringify(layout.distribution));fs.writeFileSync(path.join(evidence,name+'.png'),(await win.webContents.capturePage()).toPNG());report.views.push({name,...layout})};
 await win.loadURL(fixture.url);await settle();await js(`tab='usage';period='today';usageSection='summary';render()`);await settle();
 assert.equal(await js(`document.querySelectorAll('.usage-metric').length`),4);assert.equal(await js(`document.querySelector('.usage-metric:first-child>strong').textContent`),Number(report.summary.calls).toLocaleString('zh-CN'));assert.ok(await js(`!!document.querySelector('.usage-trend-panel svg')`));
 for(const [width,height,theme] of [[1120,780,'dark'],[1120,780,'light'],[820,700,'light'],[390,760,'dark'],[390,520,'light'],[1120,480,'dark']]){
  win.setContentSize(width,height);nativeTheme.themeSource=theme;await capture(`usage-${width}-${height}-${theme}`);await capture(`distribution-${width}-${height}-${theme}`,'.usage-breakdown-panel');
 }
 win.setContentSize(1120,780);nativeTheme.themeSource='dark';await js(`period='2000-01-01..2000-01-01';render()`);await settle();assert.ok(await js(`!!document.querySelector('.usage-empty')`));assert.equal(await js(`document.querySelectorAll('.usage-plot-point,.usage-breakdown-panel').length`),0);await capture('empty-desktop-dark');
 assert.deepEqual(report.errors,[]);report.status='passed';report.fixture=fixture.stats;
 console.log('WHALEBRIDGE_NATIVE_USAGE_RESULT:'+JSON.stringify({status:report.status,views:report.views.length,calls:report.summary.calls,errors:report.errors.length}));
}
run().catch(e=>{report.status='failed';report.fatal=e.stack;console.error(e.stack);process.exitCode=1}).finally(async()=>{if(win&&!win.isDestroyed())win.destroy();if(fixture)await fixture.stop().catch(e=>{report.stopError=e.message;process.exitCode=1});report.finished=new Date().toISOString();fs.writeFileSync(path.join(evidence,'usage-report.json'),JSON.stringify(report,null,2)+'\n');app.exit(process.exitCode||0)});
