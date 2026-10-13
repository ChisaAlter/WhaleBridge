'use strict';
window.WhaleBridgeAgents = (() => {
 let agents = [], selected = 'dsh', sequence = 0, pending = false, readError = '', material, modelData = {}, profiles = [], managedCount = 0, pluginErrors = {};
 const drafts = new Map(), modelDrafts = new Map(), queries = new Map(), notices = new Map();
 let profileName = '';
 const active = epoch => tab === 'agents' && epoch === renderEpoch;
 const draftKey = (id,key) => JSON.stringify([id,key]);
 const valueOf = field => String(field.value ?? '');
 const choicesOf = field => {
  const choices = [...(field.options || [])], value = valueOf(field);
  if(!choices.some(option => String(option.value ?? '') === ''))choices.unshift({value:'',label:'沿用 Agent 默认设置'});
  if(!choices.some(option => String(option.value ?? '') === value))choices.push({value,label:value || '未单独指定'});
  return choices;
 };
 const optionLabel = option => (option.label || option.value || '未单独指定') + (option.takes ? ` · ${option.takes}` : '');
 const labelOf = (field,value) => optionLabel(choicesOf(field).find(option => String(option.value ?? '') === value) || {value});
 const actionButton = (label,action,cls = '',disabled = false) => `<button type="button" class="${cls}" data-agent-action="${action}" ${disabled?'disabled':''}>${label}</button>`;
 const pageHeading = () => heading('Agent 接入','鲸屿默认自动接入。其他 Agent 按各自方式连接，查看选项不会修改配置。');
 const canEdit = agent => agent.id === 'dsh' || agent.managed && !agent.syncError;
 const currentModels = () => modelData.id === selected ? modelData.models || [] : [];
 function modelDraft() {
  if(!modelDrafts.has(selected))modelDrafts.set(selected,{shown:new Set(currentModels().filter(model=>!model.hidden || model.inUse).map(model=>model.id)),order:currentModels().map(model=>model.id),ordered:Boolean(modelData.ordered),only:Boolean(modelData.only),ownHidden:Boolean(modelData.ownHidden)});
  const draft=modelDrafts.get(selected), present=new Set(currentModels().map(model=>model.id));
  draft.order=draft.order.filter(id=>present.has(id));
  for(const model of currentModels())if(!draft.order.includes(model.id)){draft.order.push(model.id);if(model.inUse || !draft.only && !model.hidden)draft.shown.add(model.id);}
  return draft;
 }
 function fieldRow(agent,field,index){
  const key = draftKey(agent.id,field.key), saved = valueOf(field), value = drafts.get(key) ?? saved;
  const choices = choicesOf(field), picked = choices.find(option => String(option.value ?? '') === value), note = picked?.note;
  const disabled = pending || !canEdit(agent);
  const groups = new Map();
  choices.forEach(option=>{const group=option.group || '';if(!groups.has(group))groups.set(group,[]);groups.get(group).push(option);});
  const options=[...groups].map(([group,items])=>`${group?`<optgroup label="${escape(group)}">`:''}${items.map(option=>`<option value="${escape(option.value)}" ${String(option.value ?? '')===value?'selected':''}>${escape(optionLabel(option))}</option>`).join('')}${group?'</optgroup>':''}`).join('');
  const detail=[picked?.context?`上下文 ${Number(picked.context).toLocaleString()} Token`:'',picked?.free?'订阅免费':'',picked?.rate?`${picked.rate}× 用量`: ''].filter(Boolean).join(' · ');
  const follows=field.follows?(agent.fields.find(other=>other.key===field.follows)?.label || field.follows):'Agent 默认设置';
  return `<form class="agent-field" data-agent-field="${escape(field.key)}"><div class="agent-field-heading"><label for="agent-field-${index}">${escape(field.label || field.key)}</label><span class="caption">当前：${escape(labelOf(field,saved))}</span></div><div class="agent-field-control"><select id="agent-field-${index}" name="value" ${disabled?'disabled':''}>${options}</select><button type="submit" ${disabled || value===saved?'disabled':''}>应用</button></div><p class="agent-field-feedback caption" role="status">${value!==saved?'尚未应用；点击应用后写入此 Agent 的配置。':'修改后点击应用。'}</p>${field.quiet?`<p class="caption">未单独指定时沿用 ${escape(follows)}。</p>`:''}<p class="agent-option-note form-hint" ${note?'':'hidden'}>${escape(note || '')}</p>${detail?`<p class="caption">${escape(detail)}</p>`:''}</form>`;
 }
 function modelPanel(agent){
  const list=currentModels(), draft=modelDraft(), query=queries.get(selected) || '', byID=new Map(list.map(model=>[model.id,model]));
  const ordered=[...draft.order,...list.map(model=>model.id).filter(id=>!draft.order.includes(id))].map(id=>byID.get(id)).filter(Boolean);
  const found=ordered.filter(model=>`${model.name} ${model.id} ${model.group}`.toLowerCase().includes(query.toLowerCase()));
  const disabled=pending || !canEdit(agent);
  const rows=found.map(model=>`<div class="row agent-model-row" data-agent-model-row="${escape(model.id)}"><label class="agent-model-selection"><input type="checkbox" data-model-shown="${escape(model.id)}" ${draft.shown.has(model.id) || model.inUse?'checked':''} ${disabled || model.inUse?'disabled':''}><span><strong>${escape(model.name || model.id)}</strong><span class="caption">${escape(model.group)}${model.context?` · ${Number(model.context).toLocaleString()} Token`:''}${model.inUse?' · 正在使用':''}</span><code>${escape(model.id)}</code></span></label><div class="actions">${model.fastFor?`<label class="agent-fast-control"><input type="checkbox" data-model-fast="${escape(model.id)}" ${model.fast?'checked':''} ${disabled?'disabled':''}>Fast${model.ownFast?' · 供应商优先通道':''}</label>`:''}<button type="button" data-model-move="-1" data-id="${escape(model.id)}" ${disabled || query || ordered[0]?.id===model.id?'disabled':''}>上移</button><button type="button" data-model-move="1" data-id="${escape(model.id)}" ${disabled || query || ordered.at(-1)?.id===model.id?'disabled':''}>下移</button></div></div>`).join('');
  return `<section class="panel agent-model-tools"><div class="section-head"><div><h3>模型名单</h3><p>决定 ${escape(agent.name)} 列出哪些鲸桥模型；保存后同步到客户端。${modelData.for && modelData.for!==agent.id?`此客户端与 ${escape(modelData.for)} 共享名单。`:''}</p></div></div><form id="agent-model-form"><div class="agent-model-options"><label class="check"><input id="agent-model-only" type="checkbox" ${draft.only?'checked':''} ${disabled?'disabled':''}>只列出选中的模型，后续新模型需手动加入</label>${modelData.own?`<label class="check"><input id="agent-model-own-hidden" type="checkbox" ${draft.ownHidden?'checked':''} ${disabled?'disabled':''}>隐藏客户端自身模型，仅提供鲸桥模型</label>`:''}</div><label for="agent-model-query">搜索模型</label><input id="agent-model-query" type="search" value="${escape(query)}" placeholder="名称、供应商或模型 ID"><div class="actions-bar"><button type="button" data-model-list="all" ${disabled?'disabled':''}>全部显示</button><button type="button" data-model-list="none" ${disabled?'disabled':''}>全部隐藏</button><button type="button" data-model-list="reset-order" ${disabled?'disabled':''}>恢复默认排序</button></div><p class="caption">已选 ${list.filter(model=>draft.shown.has(model.id) || model.inUse).length} / ${list.length} 个；正在使用的模型始终保留。${query?'清空搜索后可调整排序。':''}</p><div class="agent-model-list">${rows || '<p class="muted">没有符合条件的模型。</p>'}</div><div class="actions-bar"><button type="submit" class="primary" ${disabled?'disabled':''}>保存模型名单</button><button type="button" data-model-list="cancel" ${pending?'disabled':''}>取消修改</button></div>${!canEdit(agent)?'<p class="form-hint">明确接入此 Agent 后，可管理它的模型名单。</p>':''}</form></section>`;
 }
 function profilePanel(){
  const rows=profiles.map(item=>`<div class="agent-profile"><div class="row"><div class="text"><h3>${escape(item.name)}</h3><p class="caption">${item.inUse?'与当前配置一致':item.applicable?'可应用到当前已接入的 Agent':'未包含当前已接入 Agent 的配置'}</p></div><div class="actions"><button type="button" data-profile-apply="${escape(item.name)}" ${pending || !item.applicable?'disabled':''}>应用方案</button><button type="button" class="danger" data-profile-delete="${escape(item.name)}" ${pending?'disabled':''}>删除</button></div></div><details class="agent-profile-details"><summary>查看保存的配置</summary>${(item.details || []).map(group=>`<div><h4>${escape(group.name)}</h4><dl>${group.fields.map(field=>`<dt>${escape(field.label || field.key)}</dt><dd>${field.hidden?'已隐藏的凭据':field.follows?`沿用 ${escape(field.follows)}`:escape(field.value || '沿用默认设置')}</dd>`).join('')}</dl>${group.models?`<p class="caption">模型名单：${group.models.only?`仅选中 ${group.models.picked} 个`:`隐藏 ${group.models.hidden} 个`}</p>`:''}</div>`).join('') || '<p class="muted">应用时仅处理当前已明确接入的外部 Agent。</p>'}</details></div>`).join('');
  return `<section class="panel agent-profiles"><div class="section-head"><div><h3>接入方案</h3><p>保存和切换已明确接入的外部 Agent 设置及模型名单。鲸屿默认连接由启动器管理。未接入客户端及资料库内容不会应用。</p></div></div><form id="agent-profile-save"><label for="agent-profile-name">方案名称</label><input id="agent-profile-name" name="name" value="${escape(profileName)}" placeholder="例如 日常开发" ${pending || !managedCount?'disabled':''}><div class="actions-bar"><button type="submit" ${pending || !managedCount?'disabled':''}>保存当前方案</button></div><p class="caption">当前管理 ${managedCount} 个外部 Agent。保存同名方案将替换原方案。</p></form><div class="agent-profile-list">${rows || '<p class="muted">还没有保存的接入方案。</p>'}</div></section>`;
 }
 function agentCard(agent){
  const whale=agent.id==='dsh', notice=notices.get(agent.id) || agent.notice;
  const status=agent.syncError?'接入异常':whale?'已自动接入':agent.importable?agent.added?'已导入':'待导入':agent.connected?agent.launchable?'已配置':'已接入':agent.managed?'有接入记录':'未接入';
  const failure=agent.syncError?`<div class="agent-sync-error" role="alert"><strong>接入配置异常</strong><p>${escape(agent.syncError)}</p></div>`:'';
  const details=whale?'鲸屿始终自动接入鲸桥。模型管理与供应商配置保存后自动同步，当前默认模型保持原设置。':agent.importable?`生成并复制导入链接后，在 ${escape(agent.name)} 中完成导入；移除连接也需在目标应用中操作。`:agent.launchable?'接入会保存模型配置。生成启动命令后，复制到自己的终端运行，此 Agent 才会通过鲸桥发起请求。':agent.detected?'已检测到此 Agent。接入后可配置模型，修改需要点击应用。':'未检测到此 Agent。接入会保存连接配置；请先自行安装并运行此 Agent。';
  const actions=whale?actionButton(`${icon('refresh')}重新同步鲸屿`,'sync','',pending):agent.importable?actionButton('生成导入链接','import','primary',pending):(agent.connected?actionButton('断开接入','disconnect','danger',pending):actionButton('接入鲸桥','connect','primary',pending)+(agent.managed?actionButton('断开接入','disconnect','danger',pending):''))+(agent.launchable?actionButton('生成启动命令','launch','',pending || !agent.connected):'');
  const managedNotice=!whale && !agent.importable && agent.managed && !agent.connected && !agent.syncError?'<p class="agent-notice form-hint">此 Agent 已切换到自身模型。断开接入会清理鲸桥配置，保留当前所选模型。</p>':'';
  const generated=material?.id===agent.id?`<div class="agent-material"><label for="agent-material-value">${material.type==='import'?'导入链接':'启动命令'}</label><textarea id="agent-material-value" readonly spellcheck="false">${escape(material.value)}</textarea><div class="actions-bar">${actionButton('复制','copy-material','',pending)}</div><p class="form-hint">${material.type==='import'?`复制后在 ${escape(agent.name)} 中导入确认。`:'复制后在终端运行；模型配置更改后请重新生成。'}此内容包含连接凭据，请只用于所选 Agent。</p></div>`:'';
  return `<section class="panel agent-card" aria-labelledby="agent-name"><div class="agent-card-head"><div class="agent-identity">${whale?'<img src="/whale-head.png" alt="" width="32" height="32">':icon('account')}<div><h2 id="agent-name">${escape(agent.name)}</h2><p class="caption">${whale?'默认 Agent':escape(agent.id)}${agent.plugin?' · 插件提供':''}</p></div></div><span class="status-badge ${agent.syncError?'error':whale || agent.connected || agent.added?'success':'muted'}">${status}</span></div><p class="agent-description">${details}</p>${agent.path?`<dl class="agent-path"><dt>配置位置</dt><dd><code>${escape(agent.path)}</code></dd></dl>`:''}${notice?`<p class="agent-notice form-hint" role="status">${escape(notice)}</p>`:''}${managedNotice}${failure}<div class="actions-bar">${actions}</div>${generated}${!whale && (agent.fields || []).length?`<div class="agent-fields"><div class="section-head"><div><h3>模型与行为</h3><p>读取当前配置；选择后点击应用。</p></div></div>${agent.fields.map((field,index)=>fieldRow(agent,field,index)).join('')}</div>`:''}</section>`;
 }
 function page(){
  const agent=agents.find(agent=>agent.id===selected);
  if(!agent)return pageHeading()+empty('没有可用的 Agent','点击刷新重新读取支持的 Agent。',actionButton('刷新检测','refresh'),'account');
  const errors=Object.entries(pluginErrors).map(([spec,error])=>`<p>${escape(spec)}：${escape(error)}</p>`).join('');
  return pageHeading()+`<div id="agents-page"><div class="agent-selector"><label for="agent-target">选择 Agent</label><select id="agent-target" ${pending?'disabled':''}>${agents.map(agent=>`<option value="${escape(agent.id)}" ${agent.id===selected?'selected':''}>${escape(agent.name)} · ${agent.id==='dsh'?'默认自动接入':agent.detected?'已检测':'未检测'}${agent.id!=='dsh' && agent.connected?' · 已接入':''}</option>`).join('')}</select><p class="form-hint">切换这里只查看配置，不会接入或修改 Agent。</p></div>${errors?`<details class="agent-sync-error"><summary>插件 Agent 加载异常</summary>${errors}</details>`:''}${agentCard(agent)}${modelPanel(agent)}${profilePanel()}</div>`;
 }
 async function render(epoch){
  const request=++sequence;
  try{
   const data=await api('agents');
   if(request!==sequence || !active(epoch))return '';
   agents=[...data.agents].sort((a,b)=>a.id==='dsh'?-1:b.id==='dsh'?1:0);pluginErrors=data.pluginErrors || {};
   if(!agents.some(agent=>agent.id===selected))selected='dsh';
   const [models,saved]=await Promise.all([api(`agent-models/${encodeURIComponent(selected)}`),api('agent-profiles')]);
   if(request!==sequence || !active(epoch))return '';
   modelData=models;profiles=saved.profiles || [];managedCount=saved.managed || 0;
   readError='';return page();
  }catch(error){
   if(request!==sequence || !active(epoch))return '';
   readError=error.message;
   return pageHeading()+empty('暂时无法读取 Agent 配置',escape(error.message),actionButton('重新读取','refresh'),'info');
  }
 }
 async function refreshPage(epoch){
  const html=await render(epoch);
  if(!active(epoch) || !html)return false;
  $('#content').innerHTML=html;$('#content').setAttribute('aria-busy','false');bind(epoch);
  return !readError;
 }
 function redraw(epoch){if(active(epoch)){$('#content').innerHTML=page();bind(epoch);}}
 async function reread(epoch,id=selected){
  if(pending || !active(epoch))return;
  const root=$('#agents-page') || $('#content'), controls=[...root.querySelectorAll('button,select,input')].map(control=>[control,control.disabled]);
  pending=true;controls.forEach(([control])=>control.disabled=true);root.setAttribute('aria-busy','true');selected=id;
  try{const read=await refreshPage(epoch);pending=false;if(read && active(epoch))redraw(epoch);}
  finally{pending=false;if(root.isConnected){controls.forEach(([control,disabled])=>control.disabled=disabled);root.removeAttribute('aria-busy');}else if(tab==='agents' && epoch!==renderEpoch && $('#agents-page'))redraw(renderEpoch);}
 }
 async function write(action,body,button,epoch,key,id){
  if(pending || id!==selected || !active(epoch) || !button.isConnected)return;
  const agent=agents.find(agent=>agent.id===id);if(!agent)return;
  const name=agent.name, root=$('#agents-page');
  const controls=[...root.querySelectorAll('button,select,input')].map(control=>[control,control.disabled]);
  const generating=action==='import' || action==='launch';
  const busy=button.tagName==='BUTTON'?startButtonBusy(button,generating?'生成中':action==='disconnect'?'断开中':action==='connect'?'接入中':action==='sync'?'同步中':action==='profile-save' || action==='models'?'保存中':'应用中'):{finish(){}};
  controls.forEach(([control])=>control.disabled=true);root.setAttribute('aria-busy','true');pending=true;++sequence;
  try{
   const endpoint=action==='sync'?'dsh/sync':action==='models'?`agent-models/${encodeURIComponent(id)}`:action==='fast'?'agent-fast':action.startsWith('profile-')?'agent-profiles/'+action.slice(8):`agent/${action}`;
   const response=await api(endpoint,body), generatedValue=action==='import'?response.link:response.command;
   if(generating && !generatedValue)throw new Error('未收到生成的连接内容，请重新生成。');
   material=generating && active(epoch) && selected===id?{id,type:action,value:generatedValue}:undefined;
   if(key)drafts.delete(key);
   if(action==='models')modelDrafts.delete(id);
   if(response.notice)notices.set(id,response.notice);else notices.delete(id);
   if(tab!=='agents')return;
   const currentEpoch=renderEpoch, read=await refreshPage(currentEpoch);
   pending=false;
   if(!active(currentEpoch))return;
   if(!read){message(`配置已保存，但读取当前状态失败：${readError}`,true);return;}
   redraw(currentEpoch);
   const text=action==='import'?`${name} 导入链接已生成，请在该应用中确认`:action==='launch'?`${name} 启动命令已生成，请复制到终端运行`:action==='connect'?`${name} 已接入鲸桥`:action==='disconnect'?`${name} 已断开鲸桥接入`:action==='sync'?'模型已重新同步到鲸屿':action==='models'?`${name} 模型名单已保存`:action==='fast'?'Fast 模式已更新':action==='profile-save'?'接入方案已保存':action==='profile-delete'?'接入方案已删除':action==='profile-apply'?`接入方案已应用，更新 ${response.changed || 0} 项配置`:`${name} 配置已应用`;
   message(response.notice?`${text}。${response.notice}`:text);
  }catch(error){pending=false;if(tab==='agents'){if(!root.isConnected)redraw(renderEpoch);$('#content').setAttribute('aria-busy','false');message(error.message,true);}}
  finally{pending=false;busy.finish();if(root.isConnected){controls.forEach(([control,disabled])=>control.disabled=disabled);root.removeAttribute('aria-busy');}}
 }
 function bind(epoch){
  const target=$('#agent-target');
  const root=$('#agents-page') || $('#content'), id=selected, agent=agents.find(agent=>agent.id===id);
  const live=()=>active(epoch) && root.isConnected && selected===id && !pending;
  target?.addEventListener('change',async event=>{if(!live())return;material=undefined;message('');await reread(epoch,event.target.value);});
  root.querySelectorAll('[data-agent-action]').forEach(button=>button.addEventListener('click',async()=>{
   if(!live())return;
   const action=button.dataset.agentAction;
   if(action==='copy-material'){if(material?.id!==id)return;const busy=startButtonBusy(button,'复制中');try{await copyText(material.value);}catch(error){message(error.message,true);}finally{busy.finish();}return;}
   if(action==='refresh'){await reread(epoch);return;}
   if(id==='dsh' && action!=='sync')return;
   await write(action,action==='sync'?{}:{id},button,epoch,undefined,id);
  }));
  root.querySelectorAll('[data-agent-field]').forEach(form=>{
   const field=agent.fields.find(field=>field.key===form.dataset.agentField), key=draftKey(id,field.key), input=form.querySelector('select'), submit=form.querySelector('button');
   input.addEventListener('change',()=>{if(!live())return;if(input.value===valueOf(field))drafts.delete(key);else drafts.set(key,input.value);submit.disabled=!canEdit(agent) || input.value===valueOf(field);form.querySelector('.agent-field-feedback').textContent=input.value===valueOf(field)?'修改后点击应用。':'尚未应用；点击应用后写入此 Agent 的配置。';const note=choicesOf(field).find(option=>String(option.value ?? '')===input.value)?.note, hint=form.querySelector('.agent-option-note');hint.textContent=note || '';hint.hidden=!note;});
   form.addEventListener('submit',async event=>{event.preventDefault();if(!live() || submit.disabled || id==='dsh')return;await write('set',{id,key:field.key,value:input.value},submit,epoch,key,id);});
  });
  root.querySelector('#agent-model-query')?.addEventListener('input',event=>{if(!live())return;const input=event.target,start=input.selectionStart;queries.set(id,input.value);redraw(epoch);const fresh=$('#agent-model-query');fresh?.focus({preventScroll:true});fresh?.setSelectionRange(start,start);});
  root.querySelector('#agent-model-only')?.addEventListener('change',event=>{if(live())modelDraft().only=event.target.checked;});
  root.querySelector('#agent-model-own-hidden')?.addEventListener('change',event=>{if(live())modelDraft().ownHidden=event.target.checked;});
  root.querySelectorAll('[data-model-shown]').forEach(input=>input.addEventListener('change',()=>{if(!live())return;const shown=modelDraft().shown;if(input.checked)shown.add(input.dataset.modelShown);else shown.delete(input.dataset.modelShown);}));
  root.querySelectorAll('[data-model-list]').forEach(button=>button.addEventListener('click',()=>{if(!live())return;const action=button.dataset.modelList,draft=modelDraft();if(action==='cancel')modelDrafts.delete(id);else if(action==='reset-order'){draft.order=currentModels().map(model=>model.id);draft.ordered=false;}else{draft.shown=new Set(currentModels().filter(model=>action==='all' || model.inUse).map(model=>model.id));}redraw(epoch);}));
  root.querySelectorAll('[data-model-move]').forEach(button=>button.addEventListener('click',()=>{if(!live())return;const draft=modelDraft(),i=draft.order.indexOf(button.dataset.id),j=i+Number(button.dataset.modelMove);if(i>=0 && j>=0 && j<draft.order.length){[draft.order[i],draft.order[j]]=[draft.order[j],draft.order[i]];draft.ordered=true;redraw(epoch);}}));
  root.querySelectorAll('[data-model-fast]').forEach(input=>input.addEventListener('change',()=>{if(!live())return;const model=currentModels().find(model=>model.id===input.dataset.modelFast);if(model)write('fast',{id,for:model.fastFor,ref:model.id,fast:input.checked},input,epoch,undefined,id);}));
  root.querySelector('#agent-model-form')?.addEventListener('submit',async event=>{event.preventDefault();if(!live() || !canEdit(agent))return;const draft=modelDraft();await write('models',{shown:[...draft.shown],order:draft.ordered?draft.order:[],only:draft.only,ownHidden:draft.ownHidden},event.target.querySelector('[type=submit]'),epoch,undefined,id);});
  root.querySelector('#agent-profile-name')?.addEventListener('input',event=>{if(live())profileName=event.target.value;});
  root.querySelector('#agent-profile-save')?.addEventListener('submit',async event=>{event.preventDefault();if(!live())return;const name=profileName.trim();if(!name){message('请填写方案名称',true);return;}if(profiles.some(profile=>profile.name===name) && !await ask('替换接入方案',`将以当前已接入 Agent 的配置替换“${name}”。`,'替换'))return;if(live())await write('profile-save',{name},event.target.querySelector('[type=submit]'),epoch,undefined,id);});
  root.querySelectorAll('[data-profile-apply]').forEach(button=>button.addEventListener('click',async()=>{if(live() && await ask('应用接入方案',`将应用“${button.dataset.profileApply}”中当前已接入 Agent 的设置和模型名单。`,'应用') && live())await write('profile-apply',{name:button.dataset.profileApply},button,epoch,undefined,id);}));
  root.querySelectorAll('[data-profile-delete]').forEach(button=>button.addEventListener('click',async()=>{if(live() && await ask('删除接入方案',`删除“${button.dataset.profileDelete}”，当前 Agent 配置保留。`,'删除') && live())await write('profile-delete',{name:button.dataset.profileDelete},button,epoch,undefined,id);}));
 }
 function leave(){material=undefined;++sequence;}
 return {render,bind,leave};
})();
