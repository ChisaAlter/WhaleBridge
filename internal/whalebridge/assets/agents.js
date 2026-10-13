'use strict';
window.WhaleBridgeAgents = (() => {
 let agents = [], selected = 'dsh', sequence = 0, pending = false, readError = '', material;
 const drafts = new Map(), notices = new Map();
 const active = epoch => tab === 'agents' && epoch === renderEpoch;
 const draftKey = (id,key) => JSON.stringify([id,key]);
 const valueOf = field => String(field.value ?? '');
 const choicesOf = field => {
  const choices = [...(field.options || [])], value = valueOf(field);
  if(!choices.some(option => String(option.value ?? '') === ''))choices.unshift({value:'',label:'沿用 Agent 默认设置'});
  if(!choices.some(option => String(option.value ?? '') === value))choices.push({value,label:value || '未单独指定'});
  return choices;
 };
 const labelOf = (field,value) => choicesOf(field).find(option => String(option.value ?? '') === value)?.label || value || '未单独指定';
 const actionButton = (label,action,cls = '',disabled = false) => `<button type="button" class="${cls}" data-agent-action="${action}" ${disabled?'disabled':''}>${label}</button>`;
 const pageHeading = () => heading('Agent 接入','鲸屿默认自动接入。其他 Agent 按各自方式连接，查看选项不会修改配置。');

 function fieldRow(agent,field,index){
  const key = draftKey(agent.id,field.key), saved = valueOf(field), value = drafts.get(key) ?? saved;
  const choices = choicesOf(field), note = choices.find(option => String(option.value ?? '') === value)?.note;
  const disabled = pending || !agent.connected;
  return `<form class="agent-field" data-agent-field="${escape(field.key)}"><div class="agent-field-heading"><label for="agent-field-${index}">${escape(field.label || field.key)}</label><span class="caption">当前：${escape(labelOf(field,saved))}</span></div><div class="agent-field-control"><select id="agent-field-${index}" name="value" ${disabled?'disabled':''}>${choices.map(option=>`<option value="${escape(option.value)}" ${String(option.value ?? '')===value?'selected':''}>${escape(option.label || option.value || '未单独指定')}</option>`).join('')}</select><button type="submit" ${disabled || value===saved?'disabled':''}>应用</button></div><p class="agent-field-feedback caption" role="status">${value!==saved?'尚未应用；点击应用后写入此 Agent 的配置。':'修改后点击应用。'}</p>${field.quiet?'<p class="caption">未单独指定时沿用 Agent 默认设置。</p>':''}<p class="agent-option-note form-hint" ${note?'':'hidden'}>${escape(note)}</p></form>`;
 }
 function agentCard(agent){
  const whale = agent.id === 'dsh', notice = notices.get(agent.id) || agent.notice;
  const status = agent.syncError?'接入异常':whale?'已自动接入':agent.importable?agent.added?'已导入':'待导入':agent.connected?agent.launchable?'已配置':'已接入':agent.managed?'有接入记录':'未接入';
  const failure = agent.syncError?`<div class="agent-sync-error" role="alert"><strong>接入配置异常</strong><p>${escape(agent.syncError)}</p></div>`:'';
  const details = whale?'鲸屿始终自动接入鲸桥。模型管理与供应商配置保存后自动同步，当前默认模型保持原设置。':agent.importable?`此 Agent 通过导入链接接入。生成并复制链接后，请在 ${escape(agent.name)} 中完成导入；导入后以该应用的确认和模型来源显示为准。移除连接也需在该应用中操作。`:agent.launchable?'接入会保存模型配置。生成启动命令后，复制到你自己的终端运行，此 Agent 才会通过鲸桥发起请求。':agent.detected?'已检测到此 Agent。接入后可在下方配置模型；修改需要逐项点击应用。':'未检测到此 Agent。接入将按上游写入连接配置；若尚未安装，请先安装并运行此 Agent。';
  const actions = whale?actionButton(`${icon('refresh')}重新同步鲸屿`,'sync','',pending):agent.importable?actionButton('生成导入链接','import','primary',pending):(agent.connected?actionButton('断开接入','disconnect','danger',pending):actionButton('接入鲸桥','connect','primary',pending)+(agent.managed?actionButton('断开接入','disconnect','danger',pending):''))+(agent.launchable?actionButton('生成启动命令','launch','',pending || !agent.connected):'');
  const managedNotice = !whale && !agent.importable && agent.managed && !agent.connected && !agent.syncError?'<p class="agent-notice form-hint">此 Agent 已切换到自身模型。断开接入会清理鲸桥配置，保留当前所选模型。</p>':'';
  const generated = material?.id===agent.id?`<div class="agent-material"><label for="agent-material-value">${material.type==='import'?'导入链接':'启动命令'}</label><textarea id="agent-material-value" readonly spellcheck="false">${escape(material.value)}</textarea><div class="actions-bar">${actionButton('复制','copy-material','',pending)}</div><p class="form-hint">${material.type==='import'?`复制后在 ${escape(agent.name)} 中导入；导入完成后刷新查看状态。`:'复制后在你自己的终端运行；模型配置更改后请重新生成。'}此内容包含连接凭据，请只用于所选 Agent。</p></div>`:'';
  return `<section class="panel agent-card" aria-labelledby="agent-name"><div class="agent-card-head"><div class="agent-identity">${whale?'<img src="/whale-head.png" alt="" width="32" height="32">':icon('account')}<div><h2 id="agent-name">${escape(agent.name)}</h2><p class="caption">${whale?'默认 Agent':escape(agent.id)}</p></div></div><span class="status-badge ${agent.syncError?'error':whale || agent.connected || agent.added?'success':'muted'}">${status}</span></div><p class="agent-description">${details}</p>${agent.path?`<dl class="agent-path"><dt>配置位置</dt><dd><code>${escape(agent.path)}</code></dd></dl>`:''}${notice?`<p class="agent-notice form-hint" role="status">${escape(notice)}</p>`:''}${managedNotice}${failure}<div class="actions-bar">${actions}</div>${generated}${!whale && (agent.fields || []).length?`<div class="agent-fields"><div class="section-head"><div><h3>模型与行为</h3><p>${agent.connected?'读取此 Agent 当前配置；选项由上游适配提供。':'接入后可应用下方配置。'}</p></div></div>${agent.fields.map((field,index)=>fieldRow(agent,field,index)).join('')}</div>`:''}</section>`;
 }
 function page(){
  const agent = agents.find(agent => agent.id === selected);
  if(!agent)return pageHeading()+empty('没有可用的 Agent','点击刷新重新读取支持的 Agent。',actionButton('刷新检测','refresh'),'account');
  return pageHeading()+`<div id="agents-page"><div class="agent-selector"><label for="agent-target">选择 Agent</label><select id="agent-target" ${pending?'disabled':''}>${agents.map(agent=>`<option value="${escape(agent.id)}" ${agent.id===selected?'selected':''}>${escape(agent.name)} · ${agent.id==='dsh'?'默认自动接入':agent.detected?'已检测':'未检测'}${agent.id!=='dsh' && agent.connected?' · 已接入':''}</option>`).join('')}</select><p class="form-hint">切换这里只查看配置，不会接入或修改 Agent。</p></div>${agentCard(agent)}</div>`;
 }
 async function render(epoch){
  const request = ++sequence;
  try{
   const data = await api('agents');
   if(request!==sequence || !active(epoch))return '';
   agents = [...data.agents].sort((a,b)=>a.id==='dsh'?-1:b.id==='dsh'?1:0);
   if(!agents.some(agent=>agent.id===selected))selected='dsh';
   readError='';return page();
  }catch(error){
   if(request!==sequence || !active(epoch))return '';
   readError=error.message;
   return pageHeading()+empty('暂时无法读取 Agent 配置',escape(error.message),actionButton('重新读取','refresh'),'info');
  }
 }
 async function refreshPage(epoch){
  const html = await render(epoch);
  if(!active(epoch) || !html)return false;
  $('#content').innerHTML=html;$('#content').setAttribute('aria-busy','false');bind(epoch);
  return !readError;
 }
 function redraw(epoch){
  if(!active(epoch))return;
  $('#content').innerHTML=page();bind(epoch);
 }
 async function write(action,body,button,epoch,key){
  if(pending)return;
  const agent = agents.find(agent=>agent.id===selected);
  if(!agent)return;
  const name = agent.name, root = $('#agents-page');
  const controls = [...root.querySelectorAll('button,select')].map(control=>[control,control.disabled]);
  const generating = action==='import' || action==='launch';
  const busy = startButtonBusy(button,generating?'生成中':action==='disconnect'?'断开中':action==='connect'?'接入中':action==='sync'?'同步中':'应用中');
  controls.forEach(([control])=>control.disabled=true);root.setAttribute('aria-busy','true');
  pending=true;++sequence;
  try{
   const response = await api(action==='sync'?'dsh/sync':`agent/${action}`,body);
   const generatedValue=action==='import'?response.link:response.command;
   if(generating && !generatedValue)throw new Error('未收到生成的连接内容，请重新生成。');
   material=generating && active(epoch) && selected===agent.id?{id:agent.id,type:action,value:generatedValue}:undefined;
   if(key)drafts.delete(key);
   if(response.notice)notices.set(agent.id,response.notice);else notices.delete(agent.id);
   pending=false;
   if(tab!=='agents')return;
   const currentEpoch=renderEpoch, read = await refreshPage(currentEpoch);
   if(!active(currentEpoch))return;
   if(!read){message(`${generating?'连接内容已生成':'配置已保存'}，但读取当前状态失败：${readError}`,true);return;}
   const text = action==='import'?`${name} 导入链接已生成，请在该应用中导入确认`:action==='launch'?`${name} 启动命令已生成，请复制到终端运行`:action==='connect'?`${name} 已${agent.launchable?'配置鲸桥连接':'接入鲸桥'}`:action==='disconnect'?`${name} 已断开鲸桥接入`:action==='sync'?'模型已重新同步到鲸屿':`${name} 配置已应用`;
   message(response.notice?`${text}。${response.notice}`:text);
  }catch(error){pending=false;if(tab==='agents'){if(!root.isConnected)redraw(renderEpoch);$('#content').setAttribute('aria-busy','false');message(error.message,true);}}
  finally{
   pending=false;busy.finish();
   if(root.isConnected){controls.forEach(([control,disabled])=>control.disabled=disabled);root.removeAttribute('aria-busy');}
  }
 }
 function bind(epoch){
  const target=$('#agent-target');
  target?.addEventListener('change',event=>{selected=event.target.value;material=undefined;message('');redraw(epoch);$('#agent-target')?.focus({preventScroll:true});});
  const root=$('#agents-page') || $('#content');
  root.querySelectorAll('[data-agent-action]').forEach(button=>button.addEventListener('click',async()=>{
   const action=button.dataset.agentAction;
   if(action==='copy-material'){
    if(material?.id!==selected)return;const busy=startButtonBusy(button,'复制中');try{await copyText(material.value);}catch(error){message(error.message,true);}finally{busy.finish();}return;
   }
   if(action==='refresh'){
    const busy=startButtonBusy(button,'读取中');try{await refreshPage(epoch);}finally{busy.finish();}return;
   }
   if(selected==='dsh' && action!=='sync')return;
   await write(action,action==='sync'?{}:{id:selected},button,epoch);
  }));
  root.querySelectorAll('[data-agent-field]').forEach(form=>{
   const agent=agents.find(agent=>agent.id===selected), field=agent.fields.find(field=>field.key===form.dataset.agentField), key=draftKey(agent.id,field.key), input=form.querySelector('select'), submit=form.querySelector('button');
   input.addEventListener('change',()=>{
    if(input.value===valueOf(field))drafts.delete(key);else drafts.set(key,input.value);
    submit.disabled=pending || !agent.connected || input.value===valueOf(field);
    form.querySelector('.agent-field-feedback').textContent=input.value===valueOf(field)?'修改后点击应用。':'尚未应用；点击应用后写入此 Agent 的配置。';
    const note=choicesOf(field).find(option=>String(option.value ?? '')===input.value)?.note, hint=form.querySelector('.agent-option-note');hint.textContent=note || '';hint.hidden=!note;
   });
   form.addEventListener('submit',async event=>{
    event.preventDefault();if(submit.disabled || agent.id==='dsh')return;
    await write('set',{id:agent.id,key:field.key,value:input.value},submit,epoch,key);
   });
  });
 }
 function leave(){material=undefined;++sequence;}
 return {render,bind,leave};
})();
