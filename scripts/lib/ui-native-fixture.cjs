'use strict';
// Real packaged component + isolated disk seed + loopback vendor/S3. Never mocks /api.
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const net = require('node:net');
const crypto = require('node:crypto');
const { spawn, execFileSync } = require('node:child_process');
const sleep = ms => new Promise(r => setTimeout(r, ms));
const writeJSON = (p, value) => { fs.mkdirSync(path.dirname(p), { recursive: true }); fs.writeFileSync(p, JSON.stringify(value, null, 2)); };
const listen = server => new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', () => resolve(`http://127.0.0.1:${server.address().port}`)); });
const close = server => new Promise(resolve => { server.close(resolve); server.closeAllConnections?.(); });
const request = (url, options = {}) => new Promise((resolve, reject) => {
  const req = http.request(url, { ...options, timeout: 12000 }, res => {
    const chunks = []; res.on('data', b => chunks.push(b)); res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, text: Buffer.concat(chunks).toString() }));
  });
  req.on('error', reject); req.on('timeout', () => req.destroy(new Error('Local native request timed out')));
  if (options.body !== undefined) req.write(options.body); req.end();
});

async function startFixture({ repo, payload, root }) {
  repo = path.resolve(repo); payload = path.resolve(payload); root = path.resolve(root);
  if (root === path.parse(root).root || root === repo || root === payload || fs.existsSync(path.join(root, 'fixture-owned.json'))) throw new Error('Use a fresh dedicated fixture root');
  fs.mkdirSync(root, { recursive: true });
  const data = path.join(root, 'component'), profile = path.join(root, 'electron-profile'), home = path.join(root, 'native'), dsh = path.join(root, 'dsh');
  for (const p of [data, profile, home, dsh, path.join(home, 'bin'), path.join(home, 'tmp'), path.join(dsh, 'profiles/web'), path.join(data, 'cache/models')]) fs.mkdirSync(p, { recursive: true });
  writeJSON(path.join(root, 'fixture-owned.json'), { root, kind: 'offline-native-ui-fixture', created: new Date().toISOString() });
  const stats = { providers: 0, groups: 0, models: 0, accounts: 0, keys: 0, usageSeed: 50, vendorRequests: 0, s3Reads: 0, s3Writes: 0, blockedExternal: 0, routeRequests: 0 };
  const objects = new Map();
  const vendor = http.createServer(async (req, res) => {
    try {
      const url = new URL(req.url, 'http://127.0.0.1');
      const json = (status, value) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); };
      if (url.pathname.startsWith('/ui-audit-bucket/')) {
        const key = decodeURIComponent(url.pathname), old = objects.get(key);
        if (req.method === 'PUT') { const chunks = []; for await (const b of req) chunks.push(b); const body = Buffer.concat(chunks); objects.set(key, body); stats.s3Writes++; res.setHeader('ETag', '"' + crypto.createHash('md5').update(body).digest('hex') + '"'); res.writeHead(200); return res.end(); }
        if (req.method === 'DELETE') { objects.delete(key); res.writeHead(204); return res.end(); }
        if (old) { stats.s3Reads++; res.setHeader('Content-Type', 'application/json'); res.setHeader('Content-Length', old.length); res.setHeader('ETag', '"' + crypto.createHash('md5').update(old).digest('hex') + '"'); res.writeHead(200); return res.end(req.method === 'HEAD' ? undefined : old); }
        res.writeHead(404, { 'Content-Type': 'application/xml' }); return res.end('<Error><Code>NoSuchKey</Code></Error>');
      }
      if (url.pathname.endsWith('/models')) return json(200, { object: 'list', data: Array.from({length:30}, (_,i)=>({id:`audit-model-${String(i+1).padStart(2,'0')}-long-context-reasoning-2026`,object:'model',owned_by:'local-audit'})) });
      if (req.method !== 'POST') return json(404, { error: { message: 'Loopback fixture endpoint not found' } });
      const chunks = []; for await (const b of req) chunks.push(b); const b = JSON.parse(Buffer.concat(chunks).toString() || '{}'); stats.vendorRequests++;
      if (b.messages?.some(m => String(m.content).includes('fixture-force-error'))) return json(400, {error:{type:'invalid_request_error',message:'本地审查失败记录 · Deliberately long offline error with context for layout review; no external request occurred.'}});
      res.setHeader('X-UI-Audit-Metadata', 'X'.repeat(1200));
      const value = { id: 'chatcmpl-ui-audit-' + stats.vendorRequests, object:'chat.completion', created:Math.floor(Date.now()/1000), model:b.model, choices:[{index:0,message:{role:'assistant',content:'本地审查回复 · Offline native fixture response '+ 'response-metadata-'.repeat(80)},finish_reason:'stop'}],usage:{prompt_tokens:128,completion_tokens:24,total_tokens:152} };
      if (b.stream) { res.writeHead(200,{'Content-Type':'text/event-stream'}); res.end('data: '+JSON.stringify({id:value.id,object:'chat.completion.chunk',model:b.model,choices:[{index:0,delta:{role:'assistant',content:value.choices[0].message.content},finish_reason:null}]})+'\n\ndata: '+JSON.stringify({id:value.id,object:'chat.completion.chunk',choices:[{index:0,delta:{},finish_reason:'stop'}],usage:value.usage})+'\n\ndata: [DONE]\n\n'); return; }
      return json(200,value);
    } catch { res.writeHead(500); res.end('Local fixture could not read request'); }
  });
  const vendorOrigin = await listen(vendor);
  // The child's configured proxy refuses every non-loopback destination.
  const proxy = http.createServer((req,res) => { stats.blockedExternal++; res.writeHead(502); res.end('Offline UI audit denies external network'); });
  proxy.on('connect', (req,socket) => { stats.blockedExternal++; socket.end('HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n'); });
  const proxyOrigin = await listen(proxy);
  let child, logFD, closed, stopping = false;
  const stop = async () => {
    if (stopping) return closed ? closed.then(()=>{}) : undefined;
    stopping = true;
    try {
      if (child?.pid && child.exitCode === null && child.signalCode === null) {
        if (process.platform === 'win32') {
          const killer = path.join(process.env.SystemRoot || 'C:\\Windows', 'System32/taskkill.exe');
          try { execFileSync(killer, ['/PID',String(child.pid),'/T','/F'], {stdio:'ignore',windowsHide:true}); } catch (e) { try { process.kill(child.pid,0); throw e; } catch (probe) { if(probe.code!=='ESRCH') throw probe; } }
        } else child.kill('SIGTERM');
      }
      if (closed) await Promise.race([closed, sleep(5000).then(()=>{ if(child.exitCode===null && child.signalCode===null) throw new Error('Owned native PID did not exit'); })]);
    } finally { if(logFD!==undefined){fs.closeSync(logFD);logFD=undefined;} await Promise.all([close(proxy),close(vendor)]); }
  };
  try {
    const now = new Date(), providers = [], groups = [];
    for (let n=0;n<20;n++) {
      const id = 'audit-vendor-'+String(n+1).padStart(2,'0')+'-long-provider-identifier';
      const count=20+(n%5)*20, models=Array.from({length:count},(_,i)=>'audit-model-'+String(i+1).padStart(2,'0')+'-long-context-reasoning-2026');
      providers.push({id,name:`审查供应商 ${n+1} · Long international provider display name for clipping review`,chat:vendorOrigin+'/v1',proxy:'direct',key:'offline-vendor-key-'+n+'-00000000',keyName:'生产主密钥 · Local fixture primary',keys:n===0?Array.from({length:11},(_,i)=>({key:'offline-secondary-key-'+i+'-00000000',name:`备用团队密钥 ${i+1} · Long key label`,weight:i%4+1,off:i===10})):[],models,routing:['','order','rotate','usage','weight','free'][n%6],affinity:'off',off:n===19,unlisted:n===18,notes:'这是离线原生 UI 审查夹具。所有上游请求仅发送到本机回环服务。'});
      writeJSON(path.join(data,'cache/models',id+'.json'),{base:vendorOrigin+'/v1',fetched:now.toISOString(),models:models.map((ID,i)=>({ID,Name:`模型 ${i+1} · Extended reasoning and very long context display name`,Context:131072,Output:8192,Images:i%2===0,Efforts:['low','medium','high'],Price:{input:0.2,output:1.2}}))});
    }
    for(let n=0;n<12;n++) {
      const members=providers.slice(n%8,n%8+4).map(p=>p.id+'/'+p.models[0]); if(n>0 && n%3===0)members.unshift('group/'+groups[n-1].id);
      groups.push({id:'audit-routing-group-'+String(n+1).padStart(2,'0')+'-long-identifier',name:`路由分组 ${n+1} · Nested reasoning and fallback policy with a long title`,members,routing:['','order','rotate','weight','free','manual'][n%6],affinity:'session',sink:true,patience:-1,context:131072,weights:Object.fromEntries(members.map((m,i)=>[m,i+1])),rules:[{use:members[1],tokens:16000},{use:members[0],images:true},{use:members[2],agents:['dsh','codex']}],disabled:n===11});
    }
    writeJSON(path.join(data,'providers.json'),{providers:[...providers,{id:'uiaudit',proxy:'direct'}],groups,noAutoGroups:true});
    writeJSON(path.join(data,'cache/models.json'),{}); writeJSON(path.join(data,'cache/models.dev.json'),{}); writeJSON(path.join(data,'cache/fxrate.json'),{rate:7,at:now.toISOString()});
    writeJSON(path.join(data,'settings.json'),{noAccountDiscovery:false,noWSLAgents:true,quotaReads:'asked',proxy:proxyOrigin,lan:false,otel:{enabled:false,sessions:false},gatewayConversations:true,requestArchive:true,modelNames:{[providers[0].id+'/'+providers[0].models[0]]:'超长别名 · Native offline model alias for checking actual editor layout'},fxRate:7});
    writeJSON(path.join(data,'sync.json'),{url:'s3://ui-audit-bucket/offline-layout-review',endpoint:vendorOrigin,pathStyle:true,region:'us-east-1',user:'offline-audit-access',password:'offline-audit-secret',passphrase:'offline-ui-audit-backup-passphrase',keys:false,agents:false,library:false,usage:false,autoEvery:-1});
    const gatewayToken=crypto.randomBytes(16).toString('hex'); fs.writeFileSync(path.join(data,'gateway.key'),gatewayToken);
    fs.writeFileSync(path.join(data,'plugins.yml'),'preset: all\nplugins:\n- id: otel\n  enabled: false\n');
    const spec=path.join(repo,'test/fixtures/ui-audit'), auth={};
    for(let i=0;i<10;i++)auth[i?'uiaudit#'+String(i).padStart(2,'0'):'uiaudit']={type:'api',key:'offline-plugin-account-'+i+'-00000000',accountId:`audit-member-${String(i+1).padStart(2,'0')}-long-team-account@fixture.invalid`};
    writeJSON(path.join(data,'plugins.json'),{plugins:[{spec,options:{base:vendorOrigin+'/v1'}}]}); writeJSON(path.join(data,'plugin-auth.json'),auth);
    fs.writeFileSync(path.join(data,'usage.jsonl'),Array.from({length:50},(_,i)=>JSON.stringify({t:new Date(Date.now()-i*600000).toISOString(),agent:['dsh','codex','claude'][i%3],provider:providers[i%20].id,model:providers[i%20].models[0],req:'group/'+groups[i%12].id,in:1250+i*349,out:500+i*23,cache_read:i*90,cache_write:i*12,ms:600+i*78,status:i%9===0?400:200,err:i%9===0?'离线错误说明 · Very long validation message for usage detail layout and wrapped session controls':'',session:'audit-session-'+i+'-deliberately-long-native-ledger-identifier-with-multiple-segments',cost:0.015+i*0.001,ep:'/v1/chat/completions'})).join('\n')+'\n');
    writeJSON(path.join(data,'caller-keys.json'),Array.from({length:12},(_,i)=>({id:'audit-caller-'+i,name:`审查调用密钥 ${i+1} · Long named team key for native access control`,secret:'offline-caller-key-'+i+'-00000000',off:i===11,models:i%2?[providers[0].id+'/*']:[]})));
    const manifest=JSON.parse(fs.readFileSync(path.join(payload,'WhaleBridge-component.json'),'utf8')), target=manifest.platforms[process.platform+'-'+process.arch];
    if(!target)throw new Error('Payload has no matching native executable');
    const binary=path.resolve(payload,target.asset), rel=path.relative(payload,binary); if(rel.startsWith('..')||path.isAbsolute(rel))throw new Error('Manifest executable escapes payload');
    const bytes=fs.readFileSync(binary); if(bytes.length!==target.size||crypto.createHash('sha256').update(bytes).digest('hex')!==target.sha256)throw new Error('Native package manifest hash mismatch');
    const reserve=net.createServer(), gatewayOrigin=await listen(reserve); await close(reserve);
    const env={...process.env};
    const vars=fs.readFileSync(path.join(repo,'internal/agentenv/agentenv.go'),'utf8').split('var Vars = []string{')[1].split('\n}')[0].match(/"[A-Z_]+"/g).map(x=>x.slice(1,-1));
    for(const name of Object.keys(env))if(vars.includes(name.toUpperCase())||/^(OTEL_|MAGPIE_|WHALEBRIDGE_|DSH_|ANTHROPIC_|OPENAI_|AZURE_|GOOGLE_|GEMINI_|ELECTRON_RUN_AS_NODE)/i.test(name)||/proxy/i.test(name))delete env[name];
    const bunCandidates=[process.env.WHALEBRIDGE_QA_BUN,process.env.MAGPIE_BUN,path.join(process.env.APPDATA||'','npm/node_modules/bun/bin/bun.exe')].filter(Boolean), bun=bunCandidates.find(p=>path.isAbsolute(p)&&fs.existsSync(p));
    if(!bun)throw new Error('A local absolute Bun executable is required; automatic download is disabled');
    Object.assign(env,{HOME:home,USERPROFILE:home,APPDATA:path.join(home,'AppData/Roaming'),LOCALAPPDATA:path.join(home,'AppData/Local'),XDG_CONFIG_HOME:path.join(home,'config'),XDG_DATA_HOME:path.join(home,'share'),XDG_STATE_HOME:path.join(home,'state'),XDG_CACHE_HOME:path.join(home,'cache'),TEMP:path.join(home,'tmp'),TMP:path.join(home,'tmp'),PATH:path.join(home,'bin'),Path:path.join(home,'bin'),ProgramFiles:path.join(home,'ProgramFiles'),ProgramW6432:path.join(home,'ProgramW6432'),'ProgramFiles(x86)':path.join(home,'ProgramFiles-x86'),LAUNCHER_COMPONENT_ID:'whalebridge',LAUNCHER_COMPONENT_DATA_DIR:data,WHALEBRIDGE_DSH_HOME:dsh,DSH_HOME:dsh,MAGPIE_ADDR:new URL(gatewayOrigin).host,MAGPIE_NO_ACCOUNTS:'0',MAGPIE_PLUGIN_MARKET:'off',MAGPIE_BUN:bun,HTTP_PROXY:proxyOrigin,HTTPS_PROXY:proxyOrigin,ALL_PROXY:proxyOrigin,NO_PROXY:'127.0.0.1,localhost,::1',MAGPIE_OTEL_ENABLED:'false',BUN_INSTALL:path.join(home,'bun')});
    logFD=fs.openSync(path.join(root,'native.log'),'a'); child=spawn(binary,['whalebridge'],{cwd:root,env,windowsHide:true,stdio:['ignore',logFD,logFD]});
    let launchError; closed=new Promise(resolve=>{child.once('error',e=>{launchError=e;resolve();});child.once('close',resolve);});
    let state; const deadline=Date.now()+30000;
    while(Date.now()<deadline){if(launchError||child.exitCode!==null)throw new Error('Native fixture exited before readiness; see isolated native.log');try{state=JSON.parse(fs.readFileSync(path.join(data,'state.json'),'utf8'));if(state.ready&&state.pid===child.pid)break;}catch{}await sleep(50);}
    if(!state?.ready)throw new Error('Native fixture readiness exceeded 30 seconds; see isolated native.log');
    const origin=new URL(state.url).origin, exchanged=await request(state.url), cookie=exchanged.headers['set-cookie']?.[0]?.split(';')[0]; if(exchanged.status!==303||!cookie)throw new Error('Native cookie exchange failed');
    const api=async(p,body)=>{if(!p.startsWith('/api/'))throw new Error('Native api() accepts /api/ paths only');const response=await request(origin+p,{method:body===undefined?'GET':'POST',headers:{Cookie:cookie,Origin:origin,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});if(response.status<200||response.status>=300)throw new Error(`Native API ${p.split('?')[0]} HTTP ${response.status}: ${response.text.slice(0,240)}`);try{return JSON.parse(response.text);}catch{return response.text;}};
    const generateRoutes=async()=>{
      const models=[providers[1].id+'/'+providers[1].models[0],providers[2].id+'/'+providers[2].models[0],'group/'+groups[5].id];
      const results=[];
      for(let i=0;i<6;i++){
        const started=Date.now();
        try {
          const response=await request(state.gateway+'/v1/chat/completions',{method:'POST',headers:{Authorization:'Bearer '+gatewayToken,'Content-Type':'application/json','User-Agent':'dsh-ui-audit/1.0','X-Magpie-Session':'ui-audit-long-native-session-'+i+'-deliberately-long-conversation-identifier-for-detail-layout'},body:JSON.stringify({model:models[i%3],messages:[{role:'user',content:i===5?'fixture-force-error':'Offline layout review '+i+' '+('request-metadata-'.repeat(80))}],stream:false})});stats.routeRequests++;results.push({status:response.status,archive:response.headers['x-magpie-archive-id']||null});
          fs.appendFileSync(path.join(root,'gateway-timing.jsonl'),JSON.stringify({index:i,model:models[i%3],ms:Date.now()-started,status:response.status})+'\n');
        } catch(e) { fs.appendFileSync(path.join(root,'gateway-timing.jsonl'),JSON.stringify({index:i,ms:Date.now()-started,failed:true})+'\n'); throw new Error('Native gateway fixture request '+i+': '+e.message); }
      }
      return results;
    };
    const checked = async p => { const started=Date.now(); try { const value=await api(p); fs.appendFileSync(path.join(root,'api-timing.jsonl'),JSON.stringify({path:p,ms:Date.now()-started,ok:true})+'\n'); return value; } catch (e) { fs.appendFileSync(path.join(root,'api-timing.jsonl'),JSON.stringify({path:p,ms:Date.now()-started,ok:false})+'\n'); throw new Error('Fixture readiness '+p+': '+e.message); } };
    const keys=await checked('/api/keys/'+encodeURIComponent(providers[0].id));
    const accounts=await checked('/api/accounts/'+encodeURIComponent('plugin:uiaudit'));
    stats.keys=Array.isArray(keys)?keys.length:keys.keys?.length||0;stats.accounts=Array.isArray(accounts)?accounts.length:0;
    writeJSON(path.join(root,'api-smoke.json'),{keys:stats.keys,accounts:stats.accounts});
    const stateJSON=await checked('/api/state'); await checked('/api/agents');
    stats.providers=stateJSON.providers?.length||0;stats.groups=stateJSON.groups?.length||0;stats.models=stateJSON.models?.length||stateJSON.providers?.reduce((n,p)=>n+(p.available?.length||p.models?.length||0),0)||0;
    const seed={provider:providers[0].id,model:providers[0].models[0],modelRef:providers[0].id+'/'+providers[0].models[0],group:groups[0].id,accountProvider:'plugin:uiaudit',accountUsers:Object.values(auth).map(a=>a.accountId),providers:providers.map(p=>p.id),groups:groups.map(g=>g.id)};
    stats.seedids={provider:seed.provider,group:seed.group,model:seed.model,modelRef:seed.modelRef,accountProvider:seed.accountProvider};
    stats.accountsConfirmed=stats.accounts===10;
    writeJSON(path.join(root,'seed.json'),seed);
    return {url:state.url,origin,gateway:state.gateway,data,profile,api,generateRoutes,stop,stats,seed};
  } catch(e) { await stop(); throw e; }
}
module.exports={startFixture};
