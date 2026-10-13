// Provider authentication remains Magpie's; this UI never switches another
// client's active account or edits that client's model configuration.
let subscriptions=[];
const subscriptionNames={claude:'Claude',codex:'Codex','chatgpt-api':'ChatGPT API',copilot:'GitHub Copilot',cursor:'Cursor',grok:'Grok',devin:'Devin',kiro:'Kiro',zcode:'ZCode',workbuddy:'WorkBuddy','workbuddy-ai':'WorkBuddy AI','commandcode-plan':'Command Code',qoder:'Qoder','qoder-cn':'Qoder 中国版',zed:'Zed',factory:'Factory','mimo-app':'小米 MiMo',gemini:'Google Gemini',antigravity:'Google Antigravity'};
const sourceGroups={subscription:['账号登录','用已有订阅账号授权接入'],plugin:['已安装的供应商适配器','由适配器提供认证和模型'],partner:['上游赞助供应商','由供应商赞助的目录条目'],vendor:['模型供应商','模型厂商的官方 API'],relay:['聚合服务','一个接口接入多种模型'],local:['本机服务','运行在这台计算机上的模型服务'],remote:['远程网关','连接另一台计算机上的网关']};
function sourceIsPlan(pr){
 const words=[pr.id,pr.name,pr.short,pr.note,...Object.values(pr.notes || {}),pr.only,...(pr.regions || []).flatMap(r=>[r.id,r.name,r.chat,r.responses,r.anthropic,r.decide])].filter(Boolean).join(' ');
 return /\b(?:coding|token|agent|step)[\s_-]*plan\b|\bsubscription\b|\bmembership\b|\bcline[\s_-]*pass\b|\bopencode[\s_-]*go\b|套餐|订阅|会员/i.test(words);
}
function sourceRows(rows){
 const accounts=rows.map(s=>{
  const name=s.plugin?s.name:s.id==='codex'?'ChatGPT / Codex':subscriptionNames[s.id] || s.name;
  const existing=state.providers.find(p=>p.id===s.id || p.account?.agent===s.id || s.pid&&p.account?.agent===s.pid);
  const methods=(s.methods || []).map(m=>m.label).join(' · '),apiOnly=s.plugin&&s.methods?.length&&s.methods.every(m=>m.type==='api');
  return {type:'subscription',id:s.id,name,group:s.plugin?'plugin':'subscription',plan:true,auth:apiOnly?'适配器 · API 登录密钥':s.plugin?'适配器认证':'账号登录',description:[s.hint,methods,s.package?'支持供应商适配器':'',s.riskNote?'登录前请查看供应商使用说明':''].filter(Boolean).join(' · '),search:[s.name,s.id,s.pid,s.package,methods,s.riskNote,'订阅 登录 subscription account',s.id==='codex'?'ChatGPT Plus Pro Business':s.id==='zcode'?'GLM 智谱 Z.ai Coding Plan':s.id==='workbuddy'?'CodeBuddy':''].filter(Boolean).join(' '),existing};
 });
 const presets=state.presets.map(pr=>{
  const group=sourceGroups[pr.kind]?pr.kind:'vendor',plan=sourceIsPlan(pr);
  const existing=state.providers.find(p=>!p.account&&(p.preset===pr.id || p.id===pr.id));
  const regions=pr.regions || [],regionSummary=regions.length>3?`${regions.slice(0,3).map(r=>r.name).join(' / ')} · 共 ${regions.length} 个选项`:regions.map(r=>r.name).join(' / ');
  const note=pr.notes?.zh || pr.notes?.['zh-CN'] || pr.notes?.en || pr.note;
  return {type:'preset',id:pr.id,name:pr.name,group,plan,auth:pr.id==='google-vertex'?'Google Cloud 凭据':pr.noKey?'API 密钥可选':plan?'API Key 套餐':'API 密钥',description:[note,regionSummary].filter(Boolean).join(' · '),search:[pr.short,pr.id,pr.note,...Object.values(pr.notes || {}),pr.only,pr.chat,pr.responses,pr.anthropic,pr.decide,pr.website,...regions.flatMap(r=>[r.id,r.name,r.chat,r.responses,r.anthropic,r.decide]),plan?'套餐 订阅 会员 coding token plan':'','API 接口 key'].filter(Boolean).join(' '),existing};
 });
 return [...accounts,...presets];
}
function drawSourceRows(){
 if(editor?.type!=='sources')return;
 const focusKey=document.activeElement?.closest('[data-source-key]')?.dataset.sourceKey;
 const words=editor.query.trim().toLowerCase().split(/\s+/).filter(Boolean),scope=editor.scope;
 const visible=editor.rows.map((row,index)=>({row,index})).filter(({row})=>scope==='all'||scope==='plans'&&row.plan||scope==='api'&&row.type==='preset'||scope==='plugins'&&row.group==='plugin'||scope==='local'&&['local','remote'].includes(row.group)).filter(({row})=>words.every(word=>[row.name,row.id,row.auth,row.description,row.search,...sourceGroups[row.group]].join(' ').toLowerCase().includes(word)));
 $('#source-count').textContent=`${visible.length} 个匹配来源${editor.loading?' · 正在刷新来源目录…':''}`;
 $('#source-results').innerHTML=Object.entries(sourceGroups).map(([group,[title,hint]])=>{
  const picks=visible.filter(({row})=>row.group===group);if(!picks.length)return '';
  return `<section class="source-group"><header><h3>${escape(title)}</h3><p class="caption">${escape(hint)}</p></header><div class="source-grid">${picks.map(({row,index})=>`<button type="button" class="source-choice" data-source-pick="${index}" data-source-key="${escape(row.type+':'+row.id)}"><span class="source-choice-head"><strong>${escape(row.name)}</strong>${row.existing?'<span class="source-added">已添加</span>':''}</span><span class="source-auth">${escape(row.auth)}</span>${row.description?`<span class="source-description">${escape(row.description)}</span>`:''}<span class="source-id">${escape(row.id)}</span></button>`).join('')}</div></section>`;
 }).join('') || `<div class="source-empty"><h3>${editor.loading?'正在读取来源目录':'没有匹配的模型来源'}</h3><p class="form-hint">${editor.loading?'完整供应商与账号登录目录正在读取。':'试试供应商名称、ID 或「套餐」「登录」「本机」等类别；也可以添加自定义接口或查找供应商适配器。'}</p>${scope!=='all'?'<button type="button" data-source-command="all">查看全部来源</button>':''}</div>`;
 if(focusKey){const next=[...$('#source-results').querySelectorAll('[data-source-key]')].find(b=>b.dataset.sourceKey===focusKey);(next || $('#source-search')).focus({preventScroll:true});}
}
async function openSourcePicker(scope='all',query=''){
 editor={type:'sources',scope,query,rows:sourceRows([]),loading:true};const current=editor;
 $('#editor-title').textContent='添加模型来源';
 $('#fields').innerHTML=`<p class="form-hint">账号登录和 API Key 套餐均可在这里查找。选择来源后再配置认证；保存或完成授权后才接入鲸桥。</p><div class="source-toolbar"><div><label for="source-search">查找供应商或订阅</label><div class="search">${icon('search')}<input id="source-search" type="search" value="${escape(query)}" placeholder="名称、ID、套餐或说明" aria-controls="source-results"></div></div><div><label for="source-scope">来源范围</label><select id="source-scope">${[['all','全部来源'],['plans','账号登录 / API Key 套餐'],['api','API 供应商'],['plugins','已安装适配器'],['local','本机 / 远程服务']].map(([id,label])=>`<option value="${id}" ${id===scope?'selected':''}>${label}</option>`).join('')}</select></div></div><div class="actions-bar source-shortcuts" aria-label="更多模型来源"><button type="button" data-source-command="custom">自定义接口</button><button type="button" data-source-command="adapters">查找供应商适配器</button></div><p class="form-hint" id="source-count" role="status" aria-live="polite"></p><div id="source-results"></div><p class="form-hint">未列出的订阅可能由社区适配器提供。进入适配器目录查看说明后，可自行选择安装。</p>${removedSubscriptions()}`;
 drawSourceRows();showEditor();$('#save').hidden=true;$('#source-search').focus();
 $('#source-search').oninput=e=>{current.query=e.target.value;drawSourceRows();};
 $('#source-scope').onchange=e=>{current.scope=e.target.value;drawSourceRows();};
 $('#source-search').onkeydown=e=>{if(e.key==='ArrowDown'){const first=$('#source-results [data-source-pick]');if(first){e.preventDefault();first.focus();}}else if(e.key==='Escape'&&e.target.value){e.preventDefault();e.stopPropagation();e.target.value=current.query='';drawSourceRows();}};
 try{const [presets,rows]=await Promise.all([api('provider/presets'),api('subscriptions')]);if(current!==editor||current.closed)return;state.presets=presets;current.loading=false;current.rows=sourceRows(rows);drawSourceRows();}
 catch(error){if(current===editor&&!current.closed){current.loading=false;drawSourceRows();formError(error);}}
 return current;
}
function attachSourceReturn(context){
 editor.sourcePicker=context;
 $('#fields').insertAdjacentHTML('afterbegin','<div class="source-back"><button type="button" class="text-button" data-source-command="back">'+icon('arrow')+'返回来源目录</button></div>');
}
$('#fields').addEventListener('click',async e=>{
 const pick=e.target.closest('[data-source-pick]'),command=e.target.closest('[data-source-command]');if(!pick&&!command)return;
 const b=pick || command;if(b.disabled)return;const current=editor,context=current.type==='sources'?{scope:current.scope,query:current.query}:current.sourcePicker;
 let busy;
 try{
  if(command){
   const action=command.dataset.sourceCommand;
   if(action==='all'){$('#source-scope').value=current.scope='all';drawSourceRows();$('#source-search').focus();return;}
   if(action==='back'){await openSourcePicker(context.scope,context.query);return;}
   if(action==='custom'){openProvider();attachSourceReturn(context);return;}
   if(action==='adapters'){busy=startButtonBusy(b,'加载中');const next=await openAdapters('discover',current);if(next===editor&&!next.closed)attachSourceReturn(context);return;}
  }
  if(pick&&current.type==='sources'){
   const row=current.rows[Number(pick.dataset.sourcePick)];if(!row)return;
   if(row.type==='preset'){row.existing?openProvider(row.existing.id):openProvider('','edit',row.id);attachSourceReturn(context);return;}
   busy=startButtonBusy(b,'加载中');const next=await openSubscription(row.id,false,current);if(next===editor&&!next.closed)attachSourceReturn(context);
  }
 }catch(error){if(current===editor&&!current.closed)formError(error);}finally{busy?.finish();}
});
async function openSubscription(adapterId='', preferAdapter=false, expected,relogin){
 const rows=await api('subscriptions');
 if(expected&&(expected!==editor||expected.closed))return;
 subscriptions=rows.map(s=>({...s,name:s.plugin?s.name:(subscriptionNames[s.id] || s.name)}));editor={type:'subscription'};
 $('#editor-title').textContent=relogin?'重新登录订阅账号':'添加订阅账号';
 $('#fields').innerHTML=`<p class="form-hint">将已有订阅连接到鲸屿。请选择你的供应商，再按对应的登录流程完成授权。</p><label for="f-agent">订阅供应商</label><select name="agent" id="f-agent">${subscriptions.map(s=>`<option value="${escape(s.id)}">${escape(s.name)}</option>`).join('')}</select><div id="signin-options"></div><p class="muted">部分订阅需要供应商的认证工具或适配器，登录时会显示准备进度。模型范围和使用额度由供应商决定。</p>${removedSubscriptions()}`;
 if(adapterId)$('#f-agent').value=preferAdapter?(subscriptions.find(s=>s.plugin&&s.pid===adapterId)?.id || adapterId):(subscriptions.find(s=>s.id===adapterId)?.id || subscriptions.find(s=>s.pid===adapterId)?.id || adapterId);
 if(relogin)editor.relogin={...relogin,agent:$('#f-agent').value};
 $('#f-agent').onchange=()=>{if(editor.relogin?.agent!==$('#f-agent').value)editor.relogin=null;$('#editor-title').textContent=editor.relogin?'重新登录订阅账号':'添加订阅账号';subscriptionOptions();};subscriptionOptions();showEditor();$('#save').textContent='开始登录';
 return editor;
}
function removedSubscriptions(){const removed=state.removed || [],row=p=>`<div class="row account-row"><div class="text"><strong>${escape(p.name || p.id)}</strong><p class="caption">已从鲸桥移除 · 凭据保留${p.quiet?' · 已关闭恢复提醒':''}${p.tucked?' · 已从添加列表隐藏':''}</p></div><div class="actions">${button('恢复接入','removed-show',p.id)}${p.tucked?button('在添加列表显示','removed-untuck',p.id):button('从添加列表隐藏','removed-tuck',p.id)}${p.quiet?'':button('关闭恢复提醒','removed-quiet',p.id)}${button('彻底移除鲸桥凭据','removed-forget',p.id,'danger')}</div></div>`;return removed.length?`<details class="advanced"><summary>已移除的订阅（${removed.length}）</summary><div class="advanced-body"><p class="form-hint">恢复后保留先前模型和账号设置，无需重新授权。</p><div class="panel">${removed.filter(p=>!p.tucked).map(row).join('') || '<p class="empty-inline">所有已移除订阅均已隐藏。</p>'}</div>${removed.some(p=>p.tucked)?`<details class="advanced"><summary>查看隐藏的订阅</summary><div class="panel">${removed.filter(p=>p.tucked).map(row).join('')}</div></details>`:''}</div></details>`:'';}
function subscriptionOptions(){
 const s=subscriptions.find(s=>s.id===$('#f-agent').value);if(!s)return;
 $('#signin-options').innerHTML=(editor.relogin?'<p class="form-hint">更新账号 '+escape(editor.relogin.user)+' 的授权；未单独指定代理时沿用该账号的代理设置。</p>':'')+(s.plugin?`<label for="f-method">登录方式</label><select name="method" id="f-method">${(s.methods || []).map((m,i)=>`<option value="${i}">${escape(m.label)}</option>`).join('')}</select><div id="subscription-key"></div>`:s.id==='zcode'?'<label for="f-site">登录站点</label><select name="site" id="f-site"><option value="">供应商默认</option><option value="zai">Z.ai 全球站</option><option value="bigmodel">智谱中国站</option></select>':s.id==='copilot'?field('site','GitHub Enterprise Cloud 登录域名',editor.relogin?.site || '','text','可选，例如 company.ghe.com；留空使用 github.com')+'<p class="form-hint">仅具有数据驻留要求的 GHE.com 企业账号需要填写。</p>':'<p class="form-hint">点击「开始登录」后，按供应商页面或认证工具的指引完成授权。</p>')+(s.package?`<p class="muted">此订阅支持社区供应商适配器。</p><div class="subscription-adapter-actions">${button('安装供应商适配器','install-adapter',s.id)}</div>`:'')+(['codex','claude','gemini','antigravity','factory'].includes(s.id)?`<div class="actions-bar">${button('导入已有账号凭据','auth-import',s.id)}</div>`:'');
 if(s.riskNote)$('#signin-options').innerHTML+= '<p class="form-hint">'+escape(s.riskNote)+'</p>';
 if(s.plugin){$('#f-method').onchange=subscriptionKey;subscriptionKey();}
}
function subscriptionKey(){
 const s=subscriptions.find(s=>s.id===$('#f-agent').value),method=s.methods?.[Number($('#f-method').value)];
 $('#subscription-key').innerHTML=method?.type==='api'?field('key','API 登录密钥','','password',method.placeholder || '填写供应商密钥'):'';
 if($('#f-key'))$('#f-key').required=true;
}
async function beginSubscription(data){
 const current=editor;
 current.request ||= {...data,...(current.relogin&&current.relogin.agent===data.agent?{user:current.relogin.user}:{})};
 const request=current.request, s=subscriptions.find(s=>s.id===request.agent);
 current.inputs ||= {};
 if(s.plugin){
  const result=await api('subscription/prompt',{id:s.pid || s.id,method:Number(request.method || 0),inputs:current.inputs,...(current.prompt?{key:current.prompt.key,value:data.answer}:{})});
  if(current!==editor || current.closed)return;
  if(result.error)throw new Error(result.error);
  current.inputs=result.inputs;
  const question=result.prompt;current.prompt=question;
  if(question){$('#fields').innerHTML=`<p class="muted">${escape(s.name)}</p>`+(question.type==='select'?`<label for="f-answer">${escape(question.message)}</label><select id="f-answer" name="answer">${(question.options || []).map(o=>`<option value="${escape(o.value)}">${escape(o.label)}</option>`).join('')}</select>`:field('answer',escape(question.message),'','text',question.placeholder || ''));$('#save').textContent='继续';return;}
 }
 const st=await api('signin',{agent:s.pid || request.agent,site:request.site,plugin:s.plugin,method:Number(request.method || 0),key:request.key,inputs:current.inputs,...(request.user!==undefined?{user:request.user}:{}),...(Object.hasOwn(request,'proxy')?{proxy:request.proxy}:{})});
 if(current!==editor || current.closed){if(st.id)await api(`signin/${encodeURIComponent(st.id)}/cancel`,{});return;}
 if(st.state==='done'){await completeSubscription(st);return;}
 loginFlow=st.id;showLogin(st);pollLogin();
}
async function completeSubscription(st){
 editor.loginBusy?.finish();editor.loginBusy=null;
 const s=subscriptions.find(s=>s.id===editor.request.agent);
 await showConnectionSuccess(st.again?'订阅账号授权已更新':'订阅账号接入成功',`${s.name} ${st.again?'登录授权已更新':'订阅账号已添加'}，模型已同步到鲸屿。`,{name:s.name,user:st.user,kind:'订阅账号',updated:st.again});
}
function showLogin(st){
 $('#save').hidden=false;const label=st.state==='installing'?'准备中':'等待授权';if(editor.loginBusy)editor.loginBusy.update(label);else editor.loginBusy=startButtonBusy($('#save'),label);$('#fields').innerHTML=`<div class="login-progress">${st.instructions?`<p class="muted">${escape(st.instructions)}</p>`:''}${st.url&&/^https?:\/\//.test(st.url)?`<p><a href="${escape(st.url)}" target="_blank" rel="noreferrer">打开供应商登录页面 ↗</a></p>`:''}${st.code?`<p>验证码：<code>${escape(st.code)}</code></p>`:''}</div>${st.pasteCallback||st.pasteCode||st.pasteKey?field('callback','完成登录后粘贴回调地址、验证码或密钥')+`<div class="actions-bar">${button('提交','signin-callback')}</div>`:''}<p class="muted">关闭对话框会取消尚未完成的登录。</p>`;
}
function pollLogin(){
 clearTimeout(loginTimer);const id=loginFlow,current=editor;if(!id)return;
 loginTimer=setTimeout(async()=>{
  try{const st=await api(`signin/${encodeURIComponent(id)}`);if(loginFlow!==id||current!==editor||current.closed)return;
   if(st.state==='done'){await completeSubscription(st);return;}
   if(st.state==='failed'||st.state==='canceled'){loginFlow=null;editor.loginBusy?.finish();editor.loginBusy=null;$('#form-error').textContent=st.error || '登录已取消';$('#form-error').hidden=false;return;}
   // Do not replace an input while the user is pasting a callback.
   if(!$('#f-callback') || st.url!==$('#fields a')?.getAttribute('href'))showLogin(st);
   pollLogin();
  }catch(e){if(loginFlow!==id||current!==editor||current.closed)return;loginFlow=null;editor.loginBusy?.finish();editor.loginBusy=null;$('#form-error').textContent=e.message;$('#form-error').hidden=false;}
 },1500);
}
$('#editor').addEventListener('beforetoggle',event=>{if(event.newState!=='closed')return;accountViewSequence++;if(editor){editor.closed=true;editor.loginBusy?.finish();editor.loginBusy=null;}clearTimeout(loginTimer);if(loginFlow){const id=loginFlow;loginFlow=null;api(`signin/${encodeURIComponent(id)}/cancel`,{}).catch(e=>message(e.message));}});

let accountViewSequence=0;
async function openAccounts(id,expected=editor){
 const sequence=++accountViewSequence,closed=!!expected?.closed;
 const rows=await api('accounts/'+encodeURIComponent(id));
 if(sequence!==accountViewSequence||expected!==editor||!!expected?.closed!==closed)return;
 const p=state.providers.find(p=>p.id===id);
 editor={type:'accounts',data:{id},rows};const current=editor;
 $('#editor-title').textContent=(p?.name || '订阅')+' · 账号管理';
 const actions=button(icon('plus')+' 添加账号','add-account',id)+(['codex','claude','gemini','antigravity','factory'].includes(id)?button('导入账号','auth-import',id):'')+button('刷新额度','accounts-refresh',id)+button('查看完整额度','accounts-quota',id);
 $('#fields').innerHTML='<section class="editor-section" data-section="accounts" data-title="账号"><div class="toolbar">'+actions+'</div><p class="form-hint">共 '+rows.length+' 个账号。优先顺序仅影响鲸桥；移除借用账号会停止在鲸桥使用，原客户端保持登录。</p><div class="management-table-wrap"><table class="management-table"><thead><tr><th>账号与套餐</th><th>状态与额度</th><th>路由设置</th><th>操作</th></tr></thead><tbody>'+rows.map((a,i)=>{
 const ref=JSON.stringify({id,user:a.user}),on=!a.lapsed&&!a.paused&&(a.active||a.on),single=id==='cursor',alias=a.alias || accountMap(p,'accountNames',a.user) || '';
 const toggle=button(on?'停用':'启用','account-on',JSON.stringify({id,user:a.user,on:!on}));
 return '<tr><td><div class="table-primary">'+escape(alias || a.user)+'</div>'+(alias?'<div class="table-meta">'+escape(a.user)+'</div>':'')+'<div class="table-meta">'+escape(a.plan || '套餐未报告')+(a.own?' · 借用客户端登录':' · 鲸桥保存授权')+'</div></td><td><span class="status-badge '+(a.lapsed?'error':'')+'">'+(a.lapsed?'需要重新登录':on?'已启用':'已停用')+'</span>'+(i===0?'<span class="status-badge">优先顺序第一</span>':'')+(a.lapsed?'<p class="inline-error">'+escape(a.lapsed)+'</p>':'')+(a.checkinKey?'<p class="table-meta">'+(a.checkinOff?'此账号不参与每日签到':'参与每日签到；跟随供应商开关')+'</p>':'')+'<div class="table-meta" data-account-quota="'+escape(a.user)+'" aria-live="polite">正在读取额度…</div></td><td>'+accountSummary(p,a.user)+'</td><td><div class="actions">'+button('设置','account-edit',ref)+(a.lapsed?toggle.replace('<button ','<button disabled title="重新登录后才能启用此账号" '):toggle)+(i&&!single?button('优先使用','account-first',ref):'')+button('重新登录','account-relogin',ref)+button('移除','account-remove',ref,'danger')+'</div><div class="actions">'+(i&&!single?button('向前','account-up',ref,'text-button'):'')+(i<rows.length-1&&!single?button('向后','account-down',ref,'text-button'):'')+'</div></td></tr>';
 }).join('')+'</tbody></table></div>'+(!rows.length?'<p class="empty-inline">尚未保存账号。添加或导入已有订阅授权后即可使用。</p>':'')+'</section>';
 showEditor();$('#save').hidden=true;
 void refreshAccountQuotas(current);
}
async function refreshAccountQuotas(current,explicit=false){
 const sequence=current.quotaSequence=(current.quotaSequence || 0)+1;
 const target=$('#fields [data-action="accounts-refresh"]'),busy=target?startButtonBusy(target,'刷新中'):null;
 try{
  const qs=await api('accounts/'+encodeURIComponent(current.data.id)+'/usage'+(explicit?'?refresh=true':''));
  if(current!==editor||current.closed||sequence!==current.quotaSequence)return;
  current.quotas=qs;
  for(const node of $('#fields').querySelectorAll('[data-account-quota]')){
   const q=accountQuotaFor(qs,node.dataset.accountQuota);
   node.innerHTML=accountQuotaDetails(q,current.data.id);
  }
  if(current.type==='account-edit')populateAccountWindows(current,state.providers.find(p=>p.id===current.data.id),accountQuotaFor(qs,current.data.user));
 }catch(error){if(current===editor&&!current.closed&&sequence===current.quotaSequence){for(const node of $('#fields').querySelectorAll('[data-account-quota]'))node.textContent='额度读取失败：'+error.message;const hint=$('#account-window-hint');if(hint){hint.hidden=false;hint.textContent='窗口读取失败：'+error.message+'。已保存的独立上限仍可修改。';}}}
 finally{busy?.finish();}
}

const accountQuotaFor=(quotas,user)=>Object.entries(quotas || {}).find(([ref])=>ref.toLowerCase()===String(user).toLowerCase())?.[1];
function quotaAccountText(q){
 return [q.error,q.balance,...(q.windows || []).map(w=>w.name+'：'+(w.unlimited?'不限':w.display || (100-Number(w.used)).toFixed(1)+'% 剩余')+(w.resetsAt?' · 重置于 '+dateText(w.resetsAt):'')),q.asOf?'显示 '+dateText(q.asOf)+' 的上次成功读数':q.readAt?'读取于 '+dateText(q.readAt):'' ].filter(Boolean).join(' · ') || '供应商未报告额度';
}
function accountQuotaDetails(q,id){
 if(!q)return '供应商未报告此账号额度';
 const resets=q.resets,checkin=q.checkin;
 const checkinResult=checkin?({claimed:'已领取',done:'已签到',failed:'签到失败',inactive:'此账号没有签到活动',ineligible:'此账号不符合签到条件',captcha:'需要在供应商页面完成验证码','own-app':'需要在供应商应用签到'})[checkin.outcome] || checkin.outcome:'';
 return `<p class="${q.error?'inline-error':'table-meta'}">${escape(quotaAccountText(q))}</p>`+
  (q.seat?`<p class="table-meta">同一席位：${escape(q.seat)} · 额度与此套餐共用</p>`:'')+
  (q.lastServedAt?`<p class="table-meta">最近成功请求：${escape(dateText(q.lastServedAt))}${q.last?' · 最近使用的账号':''}</p>`:'')+
  (q.until?`<p class="table-meta">套餐${q.renew==='auto'?'自动续期于':q.renew==='off'?'到期于':'有效至'} ${escape(dateText(q.until))}</p>`:'')+
  (q.held?'<p class="table-meta">账号当前暂停发送请求</p>':'')+
  (q.checkins?`<p class="table-meta">${q.checkinOff?'已关闭此账号的每日签到':checkin?`${escape(checkin.day)} · ${escape(checkinResult)}${checkin.credit?' · 领取 '+Number(checkin.credit):''}${checkin.streak?' · 连续 '+Number(checkin.streak)+' 天':''}${checkin.msg?' · '+escape(checkin.msg):''}`:'此账号参与每日签到；尚无记录'}</p>`:'')+
  (resets?`<p class="table-meta">${resets.byWindow?`可用重置：5 小时 ${Number(resets.fiveHour || 0)} 次 · 每周 ${Number(resets.weekly || 0)} 次`:`可用重置：${Number(resets.count || 0)} 次`}${resets.until?' · 最早到期 '+escape(dateText(resets.until)):''}</p>`:'')+
  (resets?.each?.length?`<details class="advanced"><summary>重置到期时间</summary>${resets.each.map(r=>`<p class="table-meta">${r.window?escape(({fiveHour:'5 小时',weekly:'每周'})[r.window] || r.window)+' · ':''}${r.until?'到期于 '+escape(dateText(r.until)):'无到期时间'}</p>`).join('')}</details>`:'')+
  (q.resetsUsed?.length?`<details class="advanced"><summary>重置使用记录</summary>${q.resetsUsed.map(r=>`<p class="table-meta">${escape(dateText(r.at))} · ${escape(({week:'每周额度耗尽时自动使用',expiring:'到期前自动使用',hand:'手动使用'})[r.why] || r.why)}${r.windows?' · 重置 '+Number(r.windows)+' 个窗口':''}</p>`).join('')}</details>`:'')+
  (q.needsAccessKey?`<div class="actions">${button('配置套餐访问凭据','account-access-key',q.provider || id)}</div>`:'');
}
async function openKeys(id,expected=editor){
 const sequence=++accountViewSequence,closed=!!expected?.closed;
 const rows=await api('keys/'+encodeURIComponent(id));
 if(sequence!==accountViewSequence||expected!==editor||!!expected?.closed!==closed)return;
 const p=state.providers.find(p=>p.id===id);
 editor={type:'keys',data:{id},rows,expanded:localStorage.getItem('whalebridge.keys.expanded.'+id)==='1'};$('#editor-title').textContent=(p?.name || '供应商')+' · 密钥管理';
 $('#fields').innerHTML='<section class="editor-section" data-section="keys" data-title="现有密钥"><div class="toolbar">'+button('批量导入密钥','key-import',id)+(rows.length?button('移除已选密钥','key-remove-selected',id,'danger'):'')+'</div><div class="toolbar">'+field('keySearch','查找密钥','','search','名称、脱敏密钥或协议')+selectField('keyStatus','密钥状态',[['all','全部状态'],['usable','可请求'],['rest','请求暂停'],['off','已停用']],'all')+button('展开全部密钥','key-fold',id,'text-button')+'</div><p id="key-filter-count" class="form-hint" aria-live="polite"></p><div class="management-table-wrap"><table class="management-table"><thead><tr><th>选择</th><th>名称与密钥</th><th>状态与协议</th><th>路由设置</th><th>操作</th></tr></thead><tbody>'+rows.map((k,i)=>{
 const ref=JSON.stringify({id,ref:k.id});
 return '<tr data-key-row="'+i+'"><td><input type="checkbox" data-key-pick="'+escape(k.id)+'" aria-label="选择 '+escape(k.name || k.masked)+'"></td><td><div class="table-primary">'+escape(k.name || '未命名密钥')+'</div><div class="table-meta">'+escape(k.masked)+'</div></td><td><span class="status-badge">'+(k.active?'主密钥':k.on?'已启用':'已停用')+'</span><div class="table-meta">'+escape(protocolName(k.protocol))+'</div>'+(k.rest?'<p class="inline-error">'+escape(k.rest.why)+(k.rest.status?' · HTTP '+k.rest.status:'')+' · 暂停到 '+escape(dateText(k.rest.until))+'</p>'+(k.rest.key?button('恢复请求','gateway-unrest',JSON.stringify({id,key:k.rest.key})):'' ):'')+'</td><td><div class="table-meta">权重 '+(k.weight || 1)+'</div>'+accountSummary(p,k.id)+'</td><td><div class="actions">'+button('设置','key-edit',ref)+(k.active?'':button('设为主密钥','key-use',ref)+button(k.on?'停用':'启用','key-on',JSON.stringify({id,ref:k.id,on:!k.on})))+button('移除','key-remove',ref,'danger')+'</div><div class="actions">'+(i?button('向前','key-up',ref,'text-button'):'')+(i<rows.length-1?button('向后','key-down',ref,'text-button'):'')+'</div></td></tr>';
 }).join('')+'</tbody></table></div><p id="key-filter-empty" class="empty-inline" hidden>没有符合条件的密钥。</p>'+(!rows.length?'<p class="empty-inline">尚未添加 API 密钥。</p>':'')+'</section><section class="editor-section" data-section="new-key" data-title="添加密钥"><h3>添加 API 密钥</h3>'+field('name','名称')+field('key','API 密钥','','password')+selectField('protocol','协议',protocolOptions,'')+'</section>';
 showEditor();$('#save').textContent='添加密钥';$('#f-key').required=true;syncKeyList();
}
function syncKeyList(){
 if(editor?.type!=='keys')return;
 const query=$('#f-keySearch').value.trim().toLowerCase(),status=$('#f-keyStatus').value;
 const matches=editor.rows.map((k,i)=>({k,i})).filter(({k})=>[k.name,k.masked,k.protocol,protocolName(k.protocol)].filter(Boolean).join(' ').toLowerCase().includes(query)&&(status==='all'||status==='usable'&&k.on&&!k.rest||status==='rest'&&!!k.rest||status==='off'&&!k.on));
 const visible=new Set((editor.expanded?matches:matches.slice(0,5)).map(({i})=>i));
 for(const row of $('#fields').querySelectorAll('[data-key-row]')){row.hidden=!visible.has(Number(row.dataset.keyRow));if(row.hidden)row.querySelector('[data-key-pick]').checked=false;}
 const fold=$('#fields').querySelector('[data-action="key-fold"]');fold.hidden=matches.length<=5;fold.textContent=editor.expanded?'折叠密钥':`展开全部 ${matches.length} 个密钥`;fold.setAttribute('aria-expanded',String(editor.expanded));
 $('#key-filter-count').textContent=`显示 ${visible.size} / ${matches.length} 个匹配密钥，共 ${editor.rows.length} 个`;
 $('#key-filter-empty').hidden=matches.length>0||editor.rows.length===0;
}
const protocolOptions=[['','全部协议'],['chat','Chat Completions'],['responses','Responses'],['anthropic','Anthropic Messages']];
const protocolName=id=>protocolOptions.find(([v])=>v===id)?.[1] || id || '全部协议';
const accountMap=(p,key,ref)=>p?.[key]?.[String(ref).toLowerCase()];
function accountSummary(p,ref){const allow=accountMap(p,'accountModels',ref),cap=accountMap(p,'accountCaps',ref),limit=accountMap(p,'accountConcurrency',ref),windows=Object.keys(accountMap(p,'accountWindowCaps',ref) || {}).length;return escape(`${allow?.length?`${allow.length} 个指定模型`:'全部供应商模型'}${cap?` · 额度上限 ${cap}%`:''}${windows?` · ${windows} 个窗口独立上限`:''}${limit===undefined?'':` · 并发 ${limit || '不限'}`}${accountMap(p,'accountProxies',ref)?' · 独立代理':''}`);}
function accountModelFields(p,ref){return text('allow','此账号 / 密钥允许的模型',(accountMap(p,'accountModels',ref) || []).join('\n'),'留空使用供应商的全部模型；每行一个 ID')+`<details class="advanced"><summary>从供应商模型中选择</summary><div class="checks">${availableModels(p).map(m=>button(escape(m.name || m.id),'pick-account-model',m.id)).join('')}</div></details>`+integerField('limit','此账号 / 密钥最大并发',accountMap(p,'accountConcurrency',ref) ?? '',0,null,'留空跟随供应商；0 为不限');}
function openKeyEdit(id,ref){const p=state.providers.find(p=>p.id===id),k=editor.rows.find(k=>k.id===ref);editor={type:'key-edit',data:{id,ref},key:k};$('#editor-title').textContent='密钥设置';$('#fields').innerHTML='<section class="editor-section" data-section="key-basic" data-title="基本设置">'+field('name','名称',k.name || '')+selectField('protocol','密钥协议',protocolOptions,k.protocol || '')+integerField('weight','路由权重',k.weight || 1,1,null)+'</section><section class="editor-section" data-section="key-routing" data-title="模型与并发">'+accountModelFields(p,ref)+'</section>';showEditor();syncAccountModelChoices();}
function openAccountEdit(id,user){
 const p=state.providers.find(p=>p.id===id),account=editor.rows.find(a=>a.user===user),quotas=editor.quotas;
 editor={type:'account-edit',data:{id,user},account,quotas,windowCaps:new Map()};const current=editor;
 $('#editor-title').textContent='订阅账号设置';
 $('#fields').innerHTML='<p class="form-hint">'+escape(p.name)+' · '+escape(user)+'</p><section class="editor-section" data-section="account-identity" data-title="名称与维护">'+field('alias','账号别名',account.alias || accountMap(p,'accountNames',user) || '','text','例如个人账号、团队账号；留空恢复原名称').replace('<input ','<input maxlength="64" ')+(account.checkinKey?checkField('checkin','此账号参与每日签到',!account.checkinOff,'需同时打开供应商或适配器的每日签到开关。保存设置不会立即签到。'):'')+'</section><section class="editor-section" data-section="account-models" data-title="模型与额度">'+accountModelFields(p,user)+integerField('cap','额度使用上限（%）',accountMap(p,'accountCaps',user) || '',1,99,'留空为不限；如 90 表示使用到每个窗口的 90% 后切换')+'<h3>独立窗口上限</h3><p class="form-hint">留空或 0 跟随账号上限；1–99 为此窗口的百分比上限；100 表示此窗口不限。</p><div id="account-window-caps"><p id="account-window-hint" class="form-hint">正在读取此账号的额度窗口…</p></div></section><section class="editor-section" data-section="account-proxy" data-title="代理与项目">'+proxyFields(accountMap(p,'accountProxies',user) || '').replace('跟随全局代理','跟随供应商代理')+(['gemini','antigravity'].includes(id)?field('project','Google Cloud project ID','','text','留空保持当前 Cloud project'):'')+'</section>';
 $('#f-proxyMode').onchange=e=>{$('#proxy-address').hidden=e.target.value!=='custom';$('#f-proxy').required=e.target.value==='custom';};
 showEditor();syncAccountModelChoices();populateAccountWindows(current,p,accountQuotaFor(quotas,user));
 if(!quotas)void refreshAccountQuotas(current);
 if($('#f-project')){const input=$('#f-project'),busy=startButtonBusy($('#save'),'加载中');input.disabled=true;api(`accounts/${encodeURIComponent(id)}/project?user=${encodeURIComponent(user)}`).then(r=>{if(current===editor&&!current.closed)input.value=r.project || '';}).catch(e=>{if(current===editor&&!current.closed)formError(e);}).finally(()=>{if(current===editor&&!current.closed){busy.finish();input.disabled=false;}});}
}
function populateAccountWindows(current,p,quota){
 if(current!==editor||current.closed)return;
 const own=accountMap(p,'accountWindowCaps',current.data.user) || {},windows=new Map();
 for(const w of quota?.windows || [])if(w.capId&&!w.tiers)windows.set(w.capId,w.name || w.capId);
 for(const id of Object.keys(own))if(!windows.has(id))windows.set(id,id);
 const hint=$('#account-window-hint');
 for(const [id,name] of windows){
  if(current.windowCaps.has(id)){$('#account-window-caps label[for="f-'+current.windowCaps.get(id)+'"]').textContent=name+' 使用上限（%）';continue;}
  const key='windowCap'+current.windowCaps.size;current.windowCaps.set(id,key);
  hint.insertAdjacentHTML('beforebegin','<div class="form-section">'+integerField(key,escape(name)+' 使用上限（%）',own[id] || '',0,100,'跟随账号上限')+'</div>');
 }
 hint.hidden=current.windowCaps.size>0;
 if(current.quotas!==undefined&&!current.windowCaps.size)hint.textContent=quota?.error?'未能读取可设置的窗口：'+quota.error:'供应商未报告可独立设置上限的窗口。';
}
function syncAccountModelChoices(){const allow=new Set(lines($('#f-allow')?.value || ''));for(const b of $('#fields').querySelectorAll('[data-action="pick-account-model"]'))b.setAttribute('aria-pressed',String(allow.has(b.dataset.id)));}
function openKeyImport(id){editor={type:'key-import',data:{id}};$('#editor-title').textContent='批量导入密钥';$('#fields').innerHTML=text('key','API 密钥','','每行一个，也可使用逗号或空格分隔')+selectField('protocol','密钥协议',protocolOptions,'')+'<p class="form-hint">已有密钥保持原设置；重复项会跳过。</p>';showEditor();$('#f-key').required=true;$('#save').textContent='导入密钥';}
function openAuthImport(id){editor={type:'auth-import',data:{id},files:[]};$('#editor-title').textContent='导入订阅账号';$('#fields').innerHTML=`<p class="form-hint">${escape(subscriptionNames[id] || id)} · 导入你导出的账号凭据，先由供应商验证，再保存在鲸桥。</p>${['codex','claude'].includes(id)?'<p class="inline-error">导入时会向供应商刷新登录授权，消耗来源文件的 refresh token。来源工具需要重新登录后才能继续使用该账号。</p>':''}<label class="file-button"><span>选择凭据文件</span><input id="auth-files" type="file" accept=".json,.txt" multiple></label><p id="auth-file-names" class="form-hint"></p>`+text('source','或粘贴一个凭据文件的内容','','auth.json、账号导出 JSON；Factory 可以直接粘贴 fk- API 密钥');showEditor();$('#save').textContent='验证并导入';$('#auth-files').onchange=async e=>{const current=editor,input=e.target,files=Array.from(input.files),busy=startButtonBusy(input.parentElement.querySelector('span'),'读取中'),save=$('#save'),disabled=save.disabled;input.disabled=true;save.disabled=true;try{const contents=await Promise.all(files.map(f=>f.text()));if(current===editor&&!current.closed){current.files=contents;$('#auth-file-names').textContent=files.map(f=>f.name).join('、');}}catch(error){if(current===editor&&!current.closed)formError(error);}finally{busy.finish();input.disabled=false;if(current===editor&&!current.closed)save.disabled=disabled;}};}
async function saveAccountEditor(data,current){const{id,ref,user}=current.data;
 if(current.type==='adapter-options'){let options;try{options=JSON.parse(data.options);}catch{throw new Error('适配器选项必须是有效的 JSON');}if(!options||Array.isArray(options)||typeof options!=='object')throw new Error('适配器选项必须是 JSON 对象');await api('subscription/adapter',{...current.data,action:'options',options});if(current!==editor||current.closed)return;await load();if(current!==editor||current.closed)return;await openAdapters();message('供应商适配器配置已保存');return;}
 if(current.type==='key-import'){const r=await api('keys',{id,action:'import',key:data.key,protocol:data.protocol});if(current!==editor||current.closed)return;await load();if(current!==editor||current.closed)return;await openKeys(id,current);message(`密钥导入完成${r.added!==undefined?`：新增 ${r.added}，已有 ${r.had || 0}`:''}`);return;}
 if(current.type==='auth-import'){const files=[...current.files,...(data.source.trim()?[data.source]:[])];if(!files.length)throw new Error('请先选择凭据文件或粘贴内容');const r=await api('accounts/import',{id,agent:id,files});if(current!==editor||current.closed)return;await load();if(current!==editor||current.closed)return;editor={type:'import-result',data:{id}};$('#editor-title').textContent='账号导入结果';$('#fields').innerHTML=`<div class="panel">${(r.results || []).map(a=>`<div class="row"><div class="text"><strong>${escape(a.user || a.name || '账号')}</strong><p class="${a.error?'inline-error':'caption'}">${escape(a.error || ({added:'已添加',updated:'已更新授权',exists:'已存在',failed:'验证失败'})[a.status] || a.status || '已验证')}</p></div></div>`).join('')}</div><div class="actions-bar">${button('继续添加账号','add-account',id)}</div>`;showEditor();$('#save').hidden=true;return;}
 const applied=[];try{
  if(current.type==='key-edit'){for(const [action,values,label] of [['rename',{name:data.name},'名称'],['protocol',{protocol:data.protocol},'协议'],['weight',{weight:Number(data.weight)},'权重'],['models',{allow:lines(data.allow)},'模型范围'],['concurrency',{limit:data.limit===''?null:Number(data.limit)},'并发']]){await api('keys',{id,ref,action,...values});applied.push(label);}}
  if(current.type==='account-edit'){
   const caps=[...current.windowCaps].map(([window,key])=>({window,cap:Number(data[key] || 0)}));
   if(caps.some(({cap})=>!Number.isInteger(cap)||cap<0||cap>100))throw new Error('窗口上限需填写 0–100 的整数');
   for(const [action,values,label] of [['name',{alias:data.alias},'账号别名'],['models',{allow:lines(data.allow)},'模型范围'],['cap',{cap:Number(data.cap || 0)},'额度上限'],['concurrency',{limit:data.limit===''?null:Number(data.limit)},'并发'],['proxy',{proxy:data.proxyMode==='direct'?'direct':data.proxyMode==='custom'?data.proxy:''},'代理']]){await api(`accounts/${action}`,{id,user,...values});applied.push(label);}
   for(const values of caps){await api('accounts/windowcap',{id,user,...values});applied.push('窗口上限');}
   if(current.account.checkinKey){await api('accounts/checkin',{key:current.account.checkinKey,on:!!data.checkin});applied.push('账号签到开关');}
   if(data.project!==undefined){await api('accounts/project',{id,user,project:data.project});applied.push('Cloud project');}
  }
 }catch(error){throw new Error(`${applied.length?`已保存${applied.join('、')}；其余设置未完成。`:''}${error.message}`);}
 if(current!==editor||current.closed)return;await load();if(current!==editor||current.closed)return;if(current.type==='key-edit')await openKeys(id);else await openAccounts(id,current);message('设置已保存，已同步到鲸屿');
}
$('#fields').addEventListener('click',async e=>{
 const b=e.target.closest('[data-action]');if(!b||b.disabled||editor?.adapterPending)return;const{action,id}=b.dataset,current=editor;
 if(!['signin-callback','install-adapter','add-account','account-on','account-remove','account-edit','account-first','account-up','account-down','account-relogin','account-access-key','accounts-quota','accounts-refresh','auth-import','key-on','key-remove','key-weight','key-edit','key-import','key-use','key-up','key-down','key-remove-selected','key-fold','pick-account-model','gateway-unrest','removed-show','removed-quiet','removed-tuck','removed-untuck','removed-forget'].includes(action))return;
 let busy;const waiting={'signin-callback':'提交中','add-account':'加载中','account-relogin':'加载中','gateway-unrest':'恢复中','account-on':'保存中','account-first':'保存中','account-up':'保存中','account-down':'保存中','key-on':'保存中','key-use':'保存中','key-up':'保存中','key-down':'保存中','removed-show':'恢复中','removed-quiet':'保存中','removed-tuck':'保存中','removed-untuck':'保存中'};if(waiting[action])busy=startButtonBusy(b,waiting[action]);b.disabled=true;
 try{
  if(action==='key-fold'){editor.expanded=!editor.expanded;localStorage.setItem('whalebridge.keys.expanded.'+id,editor.expanded?'1':'0');syncKeyList();return;}
  if(action==='signin-callback'){await api(`signin/${encodeURIComponent(loginFlow)}/callback`,{url:$('#f-callback').value});return;}
  if(action==='install-adapter'){
   const current=editor,progress=startAdapterBusy(b);
   try{
    await installAdapter({id},progress);
    if(current!==editor||current.closed)return;
    progress.update('installing');
    const installedEditor=await openSubscription(id,true,current);
    if(!installedEditor||installedEditor!==editor||installedEditor.closed)return;
    $('#adapter-progress').hidden=false;$('#adapter-progress').setAttribute('aria-busy','false');
    $('#adapter-progress').innerHTML='<p role="status">供应商适配器已安装，可以开始登录。</p>';
    message('供应商适配器已安装');
   }catch(error){if(current===editor&&!current.closed)formError(error);return;}
   finally{progress.finish();}
   return;
  }
  if(action==='add-account'){await openSubscription(id,false,current);return;}
  if(action==='account-relogin'){const data=JSON.parse(id),site=data.id==='copilot'&&data.user.includes('@')?data.user.split('@').pop():'';await openSubscription(data.id,false,current,{user:data.user,site});return;}
  if(action==='accounts-refresh'){await refreshAccountQuotas(current,true);return;}
  if(action==='account-access-key'){openProvider(id);return;}
  if(action==='accounts-quota'){$('#editor').close();await go('usage');return;}
  if(action==='auth-import'){openAuthImport(id);return;}
  if(action==='key-import'){openKeyImport(id);return;}
  if(action==='pick-account-model'){const allow=new Set(lines($('#f-allow').value));if(allow.has(id))allow.delete(id);else allow.add(id);$('#f-allow').value=[...allow].join('\n');syncAccountModelChoices();return;}
  if(action.startsWith('removed-')){if(action==='removed-forget'&&!await ask('彻底移除鲸桥保存的订阅凭据？','鲸桥保存的授权将被删除，重新接入时需要登录。借用其他客户端的登录仍在原客户端保留。','彻底移除'))return;if(action==='removed-forget')busy=startButtonBusy(b,'移除中');await api(`provider/${action.slice(8)}`,{id});if(current!==editor||current.closed)return;await load();if(current!==editor||current.closed)return;await openSubscription('',false,current);message(action==='removed-show'?'订阅已恢复，模型已同步':'已移除订阅的设置已更新');return;}
  if(action==='gateway-unrest'){const data=JSON.parse(id),r=await api('gateway/unrest',{key:data.key});if(current!==editor||current.closed)return;message(r.lifted?'已恢复此密钥的请求':'此密钥已不在暂停状态');await load();if(current!==editor||current.closed)return;await openKeys(data.id,current);return;}
  if(action==='key-remove-selected'){const refs=[...$('#fields').querySelectorAll('[data-key-pick]:checked')].map(c=>c.dataset.keyPick);if(!refs.length)throw new Error('请先选择要移除的密钥');if(!await ask('移除已选密钥？',`将从鲸桥移除 ${refs.length} 个密钥。供应商控制台中的密钥不受影响。`,'移除密钥'))return;busy=startButtonBusy(b,'移除中');await api('keys',{id,action:'remove-many',refs});if(current!==editor||current.closed)return;await load();if(current!==editor||current.closed)return;await openKeys(id,current);return;}
  const data=JSON.parse(id);
  if(action==='key-edit'){openKeyEdit(data.id,data.ref);return;}
  if(action==='account-edit'){openAccountEdit(data.id,data.user);return;}
  if(action==='account-on')await api('accounts/on',data);
  if(action==='account-remove'){if(!await ask('移除订阅账号？','移除这个保存在鲸桥中的账号，不会删除供应商的订阅。','移除账号'))return;busy=startButtonBusy(b,'移除中');await api('accounts/remove',data);}
  if(['account-first','account-up','account-down'].includes(action)){const order=editor.rows.map(a=>a.user),at=order.indexOf(data.user),to=action==='account-first'?0:at+(action==='account-up'?-1:1);if(at<0||to<0||to>=order.length)return;order.splice(at,1);order.splice(to,0,data.user);await api('accounts/order',{id:data.id,order});}
  if(action==='key-on')await api('keys',{...data,action:'on'});
  if(action==='key-use')await api('keys',{...data,action:'use'});
  if(action==='key-up'||action==='key-down'){const order=editor.rows.map(k=>k.id),at=order.indexOf(data.ref),to=at+(action==='key-up'?-1:1);if(at<0||to<0||to>=order.length)return;[order[at],order[to]]=[order[to],order[at]];await api('keys',{id:data.id,action:'arrange',order});}
  if(action==='key-remove'){if(!await ask('移除密钥？','此密钥将不再用于鲸桥请求。如需撤销密钥，请到供应商控制台操作。','移除密钥'))return;busy=startButtonBusy(b,'移除中');await api('keys',{...data,action:'remove'});}
  if(action==='key-weight'){const value=await ask('设置密钥权重','权重越高，在权重路由中分配到的请求越多。请输入正整数。','保存权重',data.weight);if(value===false)return;busy=startButtonBusy(b,'保存中');await api('keys',{...data,action:'weight',weight:value});}
  if(current!==editor||current.closed)return;
  await load();
  if(current!==editor||current.closed)return;
  if(action.startsWith('account-'))await openAccounts(data.id,current);else await openKeys(data.id,current);
 }catch(e){if(current===editor&&!current.closed)formError(e);}finally{busy?.finish();if(b.getAttribute('aria-busy')!=='true')b.disabled=false;}
});
$('#fields').addEventListener('input',e=>{if(e.target.id==='f-allow')syncAccountModelChoices();if(e.target.id==='f-keySearch')syncKeyList();});
$('#fields').addEventListener('change',e=>{if(e.target.id==='f-keyStatus')syncKeyList();});
