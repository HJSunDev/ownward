import {query,text} from './api.js';
import {el,button,row,documentView,tag,statusName,date,renderMatches,renderDocument,matchRanges,appearanceControls,immersionButton,setImmersive,notice} from './ui.js';

// One bounded reader for the index, focused reading and graph citations.
// No document text or per-document position is persisted in browser storage.
export function createReader({asset,first,source,valid,onEdit,onRelations,onForget,onBack,onReload=onBack,basis=null,position=0,onPosition=()=>{}}){
  let alive=true,mode='content',body=first.text.text,page=first.text,loading=false,ticket=0,match=0,updated=false,failed=false;
  const ok=()=>alive&&valid(),originalSource=source;
  const status=tag(statusName(asset.state),asset.state),content=documentView(body,'reader-body');
  const progress=el('progress',{max:1,value:0,'aria-label':'当前已加载内容的阅读位置'});
  const find=el('input',{type:'search',placeholder:'在正文中查找','aria-label':'在正文中查找'}),count=el('span',{class:'find-count',role:'status'});
  const edit=button('编辑',()=>{if(ok()&&!updated)return onEdit();},'text-link'),relations=button('查看关联',()=>{if(ok()&&!updated)return onRelations();},'text-link');
  const remove=button('删除资料',async()=>{if(updated)return;const all=await text('content',asset.handle,ok);if(ok()&&!updated)return onForget(all);},'danger-quiet');
  const sourceText=el('div'),sourcePanel=el('aside',{class:'reader-notes','aria-label':'来源与状态'},el('h3',{},'来源'),sourceText,status,el('time',{},date(asset.updated_at)),edit,relations,remove);
  function showSource(value){sourceText.replaceChildren(el('p',{},value?.authored?'我创建的':value?.actor||'未注明来源'),...(value?.ref?[el('p',{class:'source-ref'},value.ref)]:[]));}
  showSource(source);
  const notes=button('来源',()=>{const open=getComputedStyle(sourcePanel).display==='none';node.classList.toggle('notes-open',open);node.classList.toggle('notes-closed',!open);notes.setAttribute('aria-expanded',String(open));},'quiet',{'aria-expanded':'false'});
  sourcePanel.prepend(button('收起来源',()=>{node.classList.remove('notes-open');node.classList.add('notes-closed');notes.setAttribute('aria-expanded','false');notes.focus();},'notes-close'));
  const load=button('继续阅读',()=>next(true),'read-continuation'),end=el('div',{class:'reading-sentinel'},load);
  const changed=button('这份资料已更新 · 重新打开',onReload,'reading-update');changed.hidden=true;
  const jump=button(basis?'继续定位引用':'继续到上次阅读的位置',()=>restorePosition(),'text-link');jump.hidden=true;
  const selectionText=()=>{const s=window.getSelection();return mode==='content'&&!updated&&s&&content.contains(s.anchorNode)&&content.contains(s.focusNode)?s.toString().trim():'';};
  const selected=button('查看所选文字的关联',()=>{const value=selectionText();if(ok()&&value)onRelations(value);},'selection-action');selected.hidden=true;
  const originalNote=el('p',{class:'original-note'},'原件保留为来源证据，不代表当前内容。');originalNote.hidden=true;
  const modes=row();
  if(asset.has_original)for(const [value,label]of [['content','当前'],['original','原件']])modes.append(button(label,()=>switchMode(value),'mode-tab',{'aria-pressed':String(value==='content')}));
  const findNext=button('下一处',async()=>{let n=matchRanges(body,find.value).length;if(n&&match<n-1)match++;else if(page.more){await next(true);match=Math.min(match+1,Math.max(0,matchRanges(body,find.value).length-1));}else match=0;paint(true);},'quiet');
  const node=el('section',{class:'reading-surface'},el('header',{class:'reader-toolbar'},button('返回',onBack,'back-link'),row(modes,notes,immersionButton())),
    el('div',{class:'reader-find'},find,count,findNext),el('div',{class:'reading-layout'},el('article',{class:'reader-sheet'},changed,jump,originalNote,content,selected,end),sourcePanel),progress,
    el('details',{class:'reading-preferences'},el('summary',{},'阅读设置'),appearanceControls()));
  function paint(scroll=false){
    findNext.disabled=!find.value;
    const n=renderMatches(content,body,find.value,match);count.textContent=find.value?`${page.more?'已加载内容找到':'本文'} ${n} 处${n?' · 第 '+(Math.min(match,n-1)+1)+' 处':''}`:'';
    if(!find.value&&basis&&mode==='content'){
      const chars=Array.from(body),a=basis.start_rune,b=basis.end_rune;
      if(Number.isInteger(a)&&Number.isInteger(b)&&a>=0&&b>a&&a<chars.length)renderDocument(content,body,[{start:chars.slice(0,a).join('').length,end:chars.slice(0,b).join('').length,class:'citation'}]);
    }
    if(scroll)content.querySelector('.current-match')?.scrollIntoView({block:'center',behavior:'auto'});
    load.hidden=!page.more||updated;end.hidden=!page.more||updated;
    load.classList.toggle('continuation-failed',failed);
  }
  async function next(explicit=false){
    if(loading||!page.more||updated||!ok()||failed&&!explicit)return;
    const at=ticket,offset=page.next_offset;loading=true;load.textContent='正在读取…';
    try{const nextPage=await query({view:mode,handle:asset.handle,offset});if(!ok()||at!==ticket)return;
      if(nextPage.text.more&&!(nextPage.text.next_offset>offset))throw new Error('正文暂时无法继续读取，请重新打开资料。');
      body+=nextPage.text.text;page=nextPage.text;failed=false;paint();
    }catch(error){if(ok()&&at===ticket){failed=true;load.hidden=false;load.classList.add('continuation-failed');load.textContent='读取未完成 · 重试';if(explicit)notice(error.message,true);}}
    finally{if(at===ticket){loading=false;if(load.textContent==='正在读取…')load.textContent='继续阅读';}}
  }
  async function switchMode(value){
    if(mode===value||!ok()||updated)return;const at=++ticket;loading=true;
    try{const [fresh,details]=await Promise.all([query({view:value,handle:asset.handle}),value==='original'?text('original_details',asset.handle,ok):Promise.resolve(null)]);if(!ok()||at!==ticket)return;
      mode=value;body=fresh.text.text;page=fresh.text;match=0;failed=false;find.value='';selected.hidden=true;originalNote.hidden=value!=='original';showSource(details?JSON.parse(details).source:originalSource);
      for(const b of modes.children)b.setAttribute('aria-pressed',String(b.textContent===(value==='content'?'当前':'原件')));paint();node.scrollTop=0;
    }finally{if(at===ticket)loading=false;}
  }
  find.addEventListener('input',()=>{match=0;paint();});find.addEventListener('keydown',event=>{if(event.key==='Enter')findNext.click();});
  let lastScroll=0;
  node.addEventListener('scroll',()=>{const range=node.scrollHeight-node.clientHeight;progress.value=range?node.scrollTop/range:0;if(mode==='content')onPosition(node.scrollTop,progress.value);if(node.scrollTop<lastScroll-35)setImmersive(false);lastScroll=node.scrollTop;if(range-node.scrollTop<320)next();},{passive:true});
  content.addEventListener('pointerup',()=>{selected.hidden=!selectionText();});
  content.addEventListener('keyup',()=>{selected.hidden=!selectionText();});
  node.addEventListener('keydown',event=>{if(event.key==='Escape'&&node.classList.contains('notes-open')){node.classList.remove('notes-open');notes.setAttribute('aria-expanded','false');notes.focus();}});
  function needsPosition(){return page.more&&(basis?Array.from(body).length<basis.end_rune:position>Math.max(0,node.scrollHeight-node.clientHeight));}
  async function restorePosition(){const at=ticket;for(let i=0;i<4&&ok()&&at===ticket&&!updated&&needsPosition();i++){await next(true);if(failed)break;}
    if(!ok()||at!==ticket)return;jump.hidden=!needsPosition()||updated;if(basis)content.querySelector('.citation')?.scrollIntoView({block:'center'});else node.scrollTop=position;
  }
  paint();if(basis||position)queueMicrotask(restorePosition);
  return {node,status,markUpdated:()=>{updated=true;ticket++;changed.hidden=false;load.hidden=true;jump.hidden=true;selected.hidden=true;for(const b of [edit,relations,remove,...modes.children])b.disabled=true;},destroy:()=>{alive=false;ticket++;content.replaceChildren();sourceText.replaceChildren();body='';},next,search:()=>({value:body,more:page.more})};
}
