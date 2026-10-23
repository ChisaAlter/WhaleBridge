'use strict';
// Executed in the real component renderer. Measures layout boxes rather than
// assuming flex declarations guarantee that controls and their tracks align.
function inspectAlignment(){
 const visible=e=>{if(!e.getClientRects().length||e.closest('[hidden]'))return false;const s=getComputedStyle(e);return s.visibility!=='hidden'&&s.display!=='none'&&Number(s.opacity)!==0;};
 const round=n=>Math.round(n*100)/100;
 const box=e=>{const r=e.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height,right:r.right,bottom:r.bottom,cx:r.x+r.width/2,cy:r.y+r.height/2};};
 const describe=e=>e.id?'#'+e.id:e.tagName.toLowerCase()+(typeof e.className==='string'&&e.className.trim()?'.'+e.className.trim().split(/\s+/).join('.'):'');
 const issue=(kind,e,detail)=>({kind,element:describe(e),text:(e.textContent||'').trim().replace(/\s+/g,' ').slice(0,90),...detail});
 const issues=[],controls=[],tracks=[],selects=[],rows=[];
 const modal=document.querySelector('#confirmation[open]')||document.querySelector('#editor[open]'),root=modal||document;
 for(const track of root.querySelectorAll('.segments')){
  if(!visible(track))continue;
  const s=getComputedStyle(track),r=box(track),buttons=[...track.querySelectorAll(':scope > button')].filter(visible);if(!buttons.length)continue;
  const b=buttons.map(box),top=Math.min(...b.map(r=>r.y))-r.y,bottom=r.bottom-Math.max(...b.map(r=>r.bottom)),entry={element:describe(track),count:buttons.length,width:round(r.width),top:round(top),bottom:round(bottom),delta:round(Math.abs(top-bottom)),wrapped:new Set(b.map(r=>Math.round(r.y))).size>1,padding:[s.paddingTop,s.paddingRight,s.paddingBottom,s.paddingLeft]};tracks.push(entry);
  if(entry.delta>1)issues.push(issue('tab-track-insets',track,entry));
  for(let i=0;i<buttons.length;i++)if(Math.abs(b[i].height-b[0].height)>1)issues.push(issue('tab-button-height',buttons[i],{height:b[i].height,expected:b[0].height}));
 }
 const textBoxes=e=>{const found=[],walker=document.createTreeWalker(e,NodeFilter.SHOW_TEXT);let t;while(t=walker.nextNode()){
  if(!t.textContent.trim()||t.parentElement.closest('svg,[aria-hidden=true],script,style')||!visible(t.parentElement))continue;
  const from=t.textContent.search(/\S/),to=t.textContent.length-t.textContent.match(/\s*$/)[0].length,range=document.createRange();range.setStart(t,from);range.setEnd(t,to);for(const r of range.getClientRects())if(r.width&&r.height)found.push({x:r.x,y:r.y,right:r.right,bottom:r.bottom,height:r.height});
 }return found;};
 for(const e of root.querySelectorAll('button,.button,.file-button,span[aria-busy=true]')){
  if(!visible(e)||e.matches('.source-choice,.switch,[role=switch]')||e.closest('svg'))continue;
  const r=box(e),s=getComputedStyle(e),texts=textBoxes(e),text=texts.length?{x:Math.min(...texts.map(r=>r.x)),y:Math.min(...texts.map(r=>r.y)),right:Math.max(...texts.map(r=>r.right)),bottom:Math.max(...texts.map(r=>r.bottom))}:null;
  const icons=[...e.querySelectorAll(':scope > .icon,:scope > .button-spinner,:scope > span[aria-busy=true] > .button-spinner')].filter(visible).map(box),entry={element:describe(e),text:(e.textContent||'').trim().replace(/\s+/g,' ').slice(0,70),align:s.alignItems,justify:s.justifyContent,height:round(r.height),textDelta:text?round((text.y+text.bottom)/2-r.cy):null,iconDelta:icons.map(i=>round(i.cy-r.cy)),padding:[s.paddingTop,s.paddingRight,s.paddingBottom,s.paddingLeft]};
  controls.push(entry);
  if(text&&Math.abs(entry.textDelta)>2.1&&s.alignItems==='center')issues.push(issue('control-label-center',e,entry));
  for(const delta of entry.iconDelta)if(Math.abs(delta)>1.1)issues.push(issue('control-icon-center',e,entry));
  // The combined label/icon group should center horizontally only when the
  // control's design calls for it (navigation and menus intentionally align left).
  if(s.justifyContent==='center'&&text&&s.textAlign!=='left'&&s.textAlign!=='start'&&s.flexDirection!=='column'){
   const left=Math.min(text.x,...icons.map(i=>i.x)),right=Math.max(text.right,...icons.map(i=>i.right)),delta=(left+right)/2-r.cx;
   if(Math.abs(delta)>2.1)issues.push(issue('control-content-horizontal',e,{...entry,delta:round(delta)}));
  }
 }
 for(const wrapper of root.querySelectorAll('.select-control')){
  if(!visible(wrapper))continue;const select=wrapper.querySelector(':scope > select'),icon=wrapper.querySelector(':scope > .icon');if(!select||!icon||!visible(select))continue;
  const r=box(select),i=box(icon),entry={element:describe(select),height:round(r.height),iconDelta:round(i.cy-r.cy),rightInset:round(r.right-i.right)};selects.push(entry);
  if(Math.abs(entry.iconDelta)>1.1)issues.push(issue('select-icon-center',select,entry));
 }
 for(const parent of root.querySelectorAll('.actions,.actions-bar,.list-toolbar,.provider-model-toolbar,.warm-time-row,.agent-field-control,.credential-tools,.history-toolbar,.header-row,.key-filter-bar')){
  if(!visible(parent))continue;const items=[...parent.children].filter(e=>visible(e)&&e.matches('button,.button,input:not([type=checkbox]),.select-control,.search,.check-field,.form-field'));
  for(let i=0;i<items.length;i++)for(let j=i+1;j<items.length;j++){
   const a=box(items[i]),b=box(items[j]);if(Math.min(a.bottom,b.bottom)<=Math.max(a.y,b.y))continue;
   const s=getComputedStyle(parent),delta=s.alignItems==='flex-end'||s.alignItems==='end'?Math.abs(a.bottom-b.bottom):Math.abs(a.cy-b.cy),entry={element:describe(parent),first:describe(items[i]),second:describe(items[j]),align:s.alignItems,delta:round(delta)};rows.push(entry);
   if(delta>1.1)issues.push(issue('control-row-alignment',parent,entry));
  }
 }
 for(const parent of root.querySelectorAll('.redact-rule-row')){
  if(!visible(parent)||getComputedStyle(parent).gridTemplateColumns.split(' ').length<2)continue;
  const items=[...parent.querySelectorAll('input,select,button')].filter(visible).map(box);if(items.length<2)continue;
  const delta=Math.max(...items.map(r=>r.bottom))-Math.min(...items.map(r=>r.bottom));rows.push({element:describe(parent),align:'bottom',delta:round(delta)});if(delta>1.1)issues.push(issue('rule-controls-bottom',parent,{delta:round(delta)}));
 }
 for(const parent of root.querySelectorAll('.status-badges')){
  if(!visible(parent))continue;const badges=[...parent.children].filter(visible);for(let i=1;i<badges.length;i++){const a=box(badges[i-1]),b=box(badges[i]);if(Math.abs(a.y-b.y)<1&&b.x-a.right<3)issues.push(issue('status-badge-spacing',parent,{gap:round(b.x-a.right)}));}
 }
 return {tracks,controls:controls.slice(0,24),selects:selects.slice(0,24),rows:rows.slice(0,24),issues,counts:{tracks:tracks.length,controls:controls.length,selects:selects.length,rows:rows.length},maxLabelOffset:round(Math.max(0,...controls.filter(c=>c.align==='center').map(c=>Math.abs(c.textDelta||0)))),maxIconOffset:round(Math.max(0,...controls.flatMap(c=>c.iconDelta.map(Math.abs)),...selects.map(s=>Math.abs(s.iconDelta))))};
}
module.exports={inspectAlignment};
