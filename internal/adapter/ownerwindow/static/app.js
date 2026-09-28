import {createGraph} from './graph.js';
import {query, act, resolve, text, request, logout, operationID, invalidate, scope} from './api.js';
import {Editor, rescuedInput, rescueNeedsWindow, rescueCleanupPending, retryRescueCleanup, clearRescue, retainReceipt} from './editor.js';
import {el, button, row, heading, empty, prose, tag, date, notice, clearNotice, statusName, permissionName, dialog, confirm, download, errorMessage} from './ui.js';

const main=document.getElementById('main');
const state={surface:'assets',epoch:0,graph:null,graphView:null,cursor:'',editor:null,selection:null,active:true,polling:false,after:'',search:'',filter:'',exiting:false,bound:false,run:0,pendingDirty:true};
const surfaces=[['assets','资料'],['relations','关联'],['drafts','文稿'],['events','动态'],['control','设置']];
const eventName={create:'添加了资料',created:'添加了资料',update:'更新了资料',updated:'更新了资料',correct:'更正了资料',correction:'更正了资料',draft_published:'保存了文稿',forget:'删除资料',permissions:'更新访问权限',enrollment:'应用连接申请',handoff:'资料库迁移',access:'访问权限变更'};
const valid=epoch=>state.active&&epoch===state.epoch;
const section=(title,...content)=>el('section',{class:'section'},el('div',{class:'section-title'},el('h2',{},title)),...content);
const sourceLabel=s=>s?.authored?'我创建的':s?.actor||s?.ref||'未注明来源';
const titleOf=value=>value.trim().split(/\r?\n/)[0].slice(0,110)||'未命名资料';
// A short, separated first line is already displayed as the reading heading.
const readingBody=value=>/^.{1,110}\r?\n\s*\r?\n/.test(value)?value.replace(/^.{1,110}\r?\n\s*\r?\n/,''):value;
const connectionLabel=person=>person.distinction?.replace(/^第\s*(\d+)\s*个登记的连接$/,'连接 $1')||'';
function assetLink(handle,epoch){
  const link=button('正在读取资料名称…',()=>openAsset({handle}),'text-link'),current=scope();
  query({view:'content',handle}).then(page=>{if(current()&&valid(epoch))link.textContent=titleOf(page.text.text);}).catch(()=>{if(current()&&valid(epoch))link.textContent='查看资料';});
  return link;
}
const pendingWork=editor=>!!editor&&(editor.dirty||editor.conflict||editor.publishID||editor.rebase||editor.discardPending||editor.refreshPending||editor.pendingSave!==undefined);
const clearReceiptNotice=()=>{state.receiptNotice?.();state.receiptNotice=null;};
const pendingReceiptNotice=retained=>{clearReceiptNotice();state.receiptNotice=notice(retained?'尚未确认上次是否保存成功，草稿已无法打开。请先查看动态，避免重复添加。':'尚未确认上次是否保存成功，草稿已无法打开。请保留此页面，并查看动态，避免重复添加。',true);};

export async function start(health){
  clearReceiptNotice();
  state.pendingNotice?.();state.pendingNotice=null;
  const run=++state.run;clearTimeout(state.pollTimer);state.polling=false;state.active=true;state.exiting=false;state.pendingDirty=true;
  retryRescueCleanup();
  state.cursor=health.cursor;
  document.getElementById('entry').hidden=true;document.getElementById('shell').hidden=false;
  if(!state.bound){state.bound=true;
  const nav=document.getElementById('navigation');
  for(const [id,label] of surfaces)nav.append(button(label,()=>navigate(id),'nav-item',{'data-surface':id,'aria-label':label}));
  document.getElementById('pending-entry').onclick=()=>navigate('control').catch(error=>notice(error.message,true));
  document.getElementById('logout').onclick=async()=>{
    try{
      const exit=async()=>{
        if(state.exiting)return;state.exiting=true;
        lock('正在结束会话。',false);
        let message='已退出。';
        try{await logout();}catch(error){if(error.status!==401)message='已退出此页面；连接中断，尚未确认是否已退出服务。';}
        if(!state.active)document.getElementById('status').textContent=message;
      };
      const rescue=rescuedInput(),input=state.editor?.value??rescue?.text;
      if(pendingWork(state.editor)||rescue?.reference){await confirm('退出前保留文字',el('div',{},el('p',{},'还有尚未完成核对的输入或操作。确认退出会清除窗口内的暂存，不会删除已保存的文稿，也不会撤销已提交的操作。'),typeof input==='string'?button('下载当前文字',()=>download(new Blob([input],{type:'text/plain;charset=utf-8'}),'未完成的文稿.txt')):null),'清除暂存并退出',exit,true);}else await exit();
    }catch(error){notice(errorMessage(error),true);}
  };
  window.addEventListener('owner-auth-lost',()=>lock('连接已过期，请重新打开 Ownward。未保存的文字仍保留在此页面。'));
  window.addEventListener('beforeunload',event=>{retryRescueCleanup();if(pendingWork(state.editor)||state.editor?.busy||rescuedInput()?.reference||rescueNeedsWindow()){state.editor?.persist();event.preventDefault();event.returnValue='';}});
  window.addEventListener('online',()=>poll());
  document.addEventListener('visibilitychange',()=>{if(!document.hidden){retryRescueCleanup();showStorageCleanup();poll();}});
  }
  await render();
  if(!state.active||run!==state.run)return;
  try{await refreshPending();}catch(error){if(state.active&&run===state.run&&error.status!==-1)state.pendingNotice=notice('暂时无法读取待确认事项，稍后会重试。',true);}
  if(!state.active||run!==state.run)return;
  const rescue=rescuedInput();
  if(rescue?.reference){
    try{await openDraft(rescue.reference);}catch(error){if(state.active&&run===state.run){notice('上次的文字还未恢复，请保留此页面，点击“恢复文稿”重试。',true);showResume();}}
  }
  if(state.active&&run===state.run)state.pollTimer=setTimeout(poll,2200);
}
function lock(message,preserveInput=!state.exiting){
  state.libraryRefresh=null;
  releaseGraph();
  clearReceiptNotice();
  invalidate();
  state.active=false;state.epoch++;state.run++;clearTimeout(state.pollTimer);state.polling=false;
  if(preserveInput)state.editor?.persist();state.editor?.destroy();state.editor=null;state.selection=null;
  if(!preserveInput)clearRescue();
  document.querySelectorAll('dialog').forEach(d=>d.remove());main.replaceChildren();
  document.getElementById('shell').hidden=true;document.getElementById('entry').hidden=false;
  document.getElementById('status').textContent=message+(rescueCleanupPending()?' 浏览器暂存尚待清理，请勿刷新或关闭。':rescueNeedsWindow()?' 暂存仅在当前窗口，请勿刷新或关闭；重新验证会在此窗口继续核对。':'');
  showStorageCleanup();
}
async function navigate(surface){
  if(!state.active)return;releaseGraph();state.graphView=null;
  state.editor?.suspend();
  invalidate();
  state.surface=surface;state.selection=null;state.after='';clearNotice();await render();
}
function navState(){document.querySelectorAll('[data-surface]').forEach(b=>{b.setAttribute('aria-current',b.dataset.surface===state.surface?'page':'false');});}
function releaseGraph(remember=false){if(state.graph){state.graphView=remember?state.graph.capture():null;state.graph.destroy();state.graph=null;}}
async function render(){
  state.libraryRefresh=null;
  releaseGraph(state.surface==='relations');
  const epoch=++state.epoch;navState();
  main.replaceChildren(el('p',{class:'loading',role:'status'},'正在读取…'));
  try{
    const node=await ({drafts:draftsPage,assets:assetsPage,relations:relationsPage,events:eventsPage,control:controlPage}[state.surface])(epoch);
    if(valid(epoch)){main.replaceChildren(node);showResume();main.focus({preventScroll:true});}
  }catch(error){if(valid(epoch)){
    if(error.status===409&&state.after){state.after='';notice('资料有变化，已返回当前筛选的第一页。');return render();}
    main.replaceChildren(empty('暂时无法读取',errorMessage(error),button('重试',()=>{state.after='';return render();})));showResume();
  }}
}
function showResume(){
  document.querySelectorAll('.resume-bar').forEach(n=>n.remove());
  showStorageCleanup();
  if(!state.active)return;
  if(state.editor?.live&&!main.contains(state.editor.node))main.prepend(el('div',{class:'resume-bar'},el('span',{},pendingWork(state.editor)?'这篇文稿还有未完成的操作。':''),button('继续编辑',()=>resumeEditor()),rescuedInput()||pendingWork(state.editor)?button('处理未保存内容',manageRescue):null));
  else if(!state.editor){const rescue=rescuedInput();if(rescue?.reference)main.prepend(el('div',{class:'resume-bar'},el('span',{},rescue.publishID?'尚未确认上次是否已加入资料。':'上次的文字还在，可以继续恢复。'),button(rescue.publishID?'确认保存结果':'恢复文稿',()=>openDraft(rescue.reference)),button('处理未保存内容',manageRescue)));}
}
function showStorageCleanup(){
  document.querySelectorAll('.rescue-cleanup').forEach(n=>n.remove());
  if(rescueCleanupPending())(state.active?main:document.getElementById('entry')).prepend(el('div',{class:'rescue-cleanup resume-bar',role:'status'},el('span',{},'文字已从页面移除，但浏览器中的副本未能清除。请保留页面并重试。'),button('重新清除',()=>{retryRescueCleanup();showStorageCleanup();})));
}
async function manageRescue(){
  const editor=state.editor,current=scope();
  const idle=()=>!editor||editor.live&&!editor.busy&&!editor.finishing&&!editor.composing;
  if(!idle())throw new Error('这篇文稿的操作仍在进行，请稍后核对。');
  editor?.persist();
  const rescue=rescuedInput();if(!rescue&&!editor)return;
  const editorState=()=>editor?JSON.stringify({snapshot:editor.snapshot(),rebase:editor.rebase,discardPending:editor.discardPending,conflict:editor.conflict}):null;
  const snapshot=JSON.stringify(rescue),editorSnapshot=editorState(),input=editor?.value??rescue?.text;
  const pending=editor?.publishID||editor?.rebase||editor?.discardPending||rescue?.publishID||rescue?.rebase||rescue?.discardPending;
  await confirm('未保存的内容',el('div',{},el('p',{},pending?'上次操作的结果尚不确定。放弃恢复会清除页面中未保存的文字，已提交的操作和已保存的草稿仍会保留。':'这些文字还未保存。可以先下载一份；放弃后，已保存的草稿仍会保留。'),typeof input==='string'?button('下载暂存文字',()=>download(new Blob([input],{type:'text/plain;charset=utf-8'}),'尚未同步的文稿.txt')):null),pending?'放弃继续恢复':'放弃未保存内容',async()=>{
    if(!current()||state.editor!==editor||!idle()||editorState()!==editorSnapshot||JSON.stringify(rescuedInput())!==snapshot)throw new Error('待处理内容已有变化，请重新核对。');
    clearReceiptNotice();clearRescue(editor?.meta.reference||rescue.reference);editor?.destroy();state.editor=null;invalidate();state.epoch++;
    if(editor){state.selection=null;await render();}else showResume();notice(rescueCleanupPending()?'窗口已停止使用这份暂存，浏览器副本仍待清理。':'已按你的选择清除这份窗口暂存。',rescueCleanupPending());
  },true);
}
async function resumeEditor(){
  state.libraryRefresh=null;
  releaseGraph();
  const editor=state.editor;if(!editor?.live)return;
  invalidate();
  state.epoch++;state.surface='drafts';state.selection=null;navState();main.replaceChildren(editor.node);
  await editor.resume();if(editor.live&&!editor.quarantined)editor.input.focus();
}
async function loadPage(input){return query({limit:12,...input});}
function pager(page,run,label='继续查看'){
  return page.next?el('div',{class:'pagination'},button(label,()=>run(page.next))):null;
}
async function assetCard(asset,epoch,open=openAsset){
  if(asset.state==='stopped')return el('article',{class:'asset-card stopped'},el('span',{class:'asset-title'},'正在删除的资料'),tag('正在清理'));
  const title=el('span',{class:'asset-title'},'正在读取资料…'),excerpt=el('span',{class:'asset-excerpt'},''),source=el('span',{class:'source'},'');
  const entry=button('',()=>open(asset),'asset-open');entry.append(el('span',{class:'document-symbol','aria-hidden':'true'},'↗'),el('span',{class:'asset-body'},title,excerpt));
  const node=el('article',{class:'asset-card'},entry,source,tag(statusName(asset.state),asset.state),el('time',{},date(asset.updated_at)));
  query({view:'content',handle:asset.handle}).then(page=>{if(valid(epoch)){title.textContent=titleOf(page.text.text);excerpt.textContent=page.text.text.trim().split(/\r?\n/).slice(1).join(' ').trim().slice(0,120);}}).catch(()=>{if(valid(epoch))title.textContent='打开资料';});
  query({view:'source',handle:asset.handle}).then(page=>{if(valid(epoch))source.textContent=sourceLabel(page.source);}).catch(()=>{if(valid(epoch))source.textContent='来源暂不可用';});
  return node;
}
async function draftsPage(epoch){
  const [drafts,assets,recent]=await Promise.all([loadPage({view:'drafts',after:state.after}),loadPage({view:'continuable',limit:4}),loadPage({view:'recent',limit:4})]);
  const list=el('div',{class:'draft-list'});
  for(const draft of drafts.drafts||[]){
    const title=el('span',{},'读取文稿…');
    list.append(button('',()=>openDraft(draft.reference),'draft-row'));
    list.lastChild.append(el('span',{class:'draft-row-body'},title,draft.target?el('span',{class:'subtle'},'修改尚未生效'):null),el('time',{},date(draft.updated_at)));
    query({view:'draft_content',handle:draft.handle}).then(p=>{if(valid(epoch))title.textContent=p.text.text.trim().split(/\r?\n/)[0].slice(0,90)||'空白文稿';}).catch(()=>{if(valid(epoch))title.textContent='继续文稿';});
  }
  const cards=el('div',{class:'asset-grid'});for(const asset of assets.assets||[])cards.append(await assetCard(asset,epoch));
  return el('div',{class:'page drafts-page'},heading('文稿','',button('新建文稿',newDraft,'primary')),
    el('div',{class:'writing-layout'},section('正在写',list.childElementCount?list:empty('从一篇文稿开始','文字会自动保存，随时可以回来继续。',button('新建文稿',newDraft,'primary')),pager(drafts,async after=>{state.after=after;await render();})),
      el('aside',{class:'writing-side'},section('最近保存',cards.childElementCount?cards:empty('还没有保存的资料',''),cards.childElementCount?button('查看全部资料',()=>navigate('assets'),'text-link'):null),section('最近动态',activityList(recent.activity||[],epoch),button('查看全部动态',()=>navigate('events'),'text-link')))));

}
// An unmounted rescue still owns the single writing workspace. Flush only
// releases it after both the content and any pending operation are settled.
async function prepareDraft(reference){
  const current=scope(),editor=state.editor;
  if(editor){await editor.flush();if(!current()||state.editor!==editor)return false;editor.persist();}
  const rescue=rescuedInput();
  if(pendingWork(editor)||rescue?.reference&&rescue.reference!==reference){showResume();throw new Error('请先恢复或处理上次文稿，再开始另一篇。');}
  return state.active&&current();
}
async function newDraft(){if(!await prepareDraft())return;const result=await act({action:'create_draft',text:''});await openDraft(result.reference);}
async function editAsset(asset){if(!await prepareDraft())return;const result=await act({action:'create_draft',target:asset.handle});await openDraft(result.reference);}
async function openDraft(reference){
  if(state.editor?.meta.reference===reference){return resumeEditor();}
  if(!await prepareDraft(reference))return;
  state.libraryRefresh=null;
  const saved=rescuedInput(),rescue=saved?.reference===reference?saved:null;
  state.editor?.destroy();state.editor=null;
  invalidate();
  const epoch=++state.epoch,page=await resolve(reference);
  if(!valid(epoch))return;
  if(page.unavailable){
    if(rescue?.publishID){
      // A missing draft is also how discard/forget becomes visible. Preserve
      // only the operation locator before awaiting its outcome explanation.
      const retained=retainReceipt(rescue);
      const recovered=await query({view:'publish_receipt',operation_id:rescue.publishID});
      if(!valid(epoch))return;
      if(['completed','changed'].includes(recovered.publication.state)){clearReceiptNotice();clearRescue(reference);notice('已确认保存成功。');return openAsset({handle:recovered.publication.asset});}
      if(recovered.publication.state==='unavailable'){clearReceiptNotice();clearRescue(reference);notice('上次存入的资料已不可用，相关暂存已清除。');return navigate('drafts');}
      pendingReceiptNotice(retained);return render();
    }
    clearRescue(reference);notice('这篇文稿已不可用，相关窗口副本已清除。');await navigate('drafts');return;
  }
  releaseGraph();
  const meta=page.drafts[0],content=await text('draft_content',meta.handle,()=>valid(epoch));
  if(!valid(epoch))return;
  const editor=installEditor(meta,content,rescue);
  if(editor.discardPending||editor.publishID||editor.refreshPending)await editor.reconcile();
}
function installEditor(meta,content,rescue,show=true){
  if(show){state.surface='drafts';state.selection=null;navState();}
  const editor=new Editor(meta,content,{
    leave:()=>navigate('drafts'),open:reference=>openDraft(reference),grant:grantDraft,
    rebased:async(meta,body,rescue)=>{const visible=releaseEditor(editor);if(visible!==null)installEditor(meta,body,rescue,visible);},
    discarded:async()=>{const visible=releaseEditor(editor);if(visible)await navigate('drafts');},
    published:async handle=>{const visible=releaseEditor(editor);if(visible)await openAsset({handle});else if(visible===false)notice('文稿已加入资料。');},
    pendingReceipt:async retained=>{const visible=releaseEditor(editor);if(visible===null)return;if(visible)await navigate('drafts');else showResume();pendingReceiptNotice(retained);},
    unavailable:()=>{const visible=releaseEditor(editor);if(visible===null)return;notice('资料或文稿已不可用，相关窗口副本已清除。');if(visible){document.querySelectorAll('dialog').forEach(d=>d.remove());navigate('drafts');}}
  },rescue);
  state.editor=editor;
  if(show){main.replaceChildren(editor.node);editor.input.focus();}else{editor.suspend();showResume();}
  return editor;
}
function releaseEditor(editor){if(state.editor!==editor)return null;const visible=main.contains(editor.node);state.editor=null;document.querySelectorAll('.resume-bar').forEach(n=>n.remove());return visible;}
async function assetsPage(epoch){
  const input=el('input',{type:'search',placeholder:'搜索资料','aria-label':'查找资料',value:state.search});
  const filter=el('select',{'aria-label':'资料状态'},...Object.entries({'':'全部状态',ready:'已整理',pending:'待整理',stopped:'已停止使用'}).map(([value,label])=>el('option',{value,selected:state.filter===value},label)));
  const search=async()=>{state.search=input.value;state.filter=filter.value;state.after='';state.libraryReference=null;await render();};
  input.addEventListener('keydown',event=>{if(event.key==='Enter')search();});filter.addEventListener('change',search);
  const page=await loadPage({view:'assets',query:state.search,state:state.filter,after:state.after});if(!valid(epoch))return;
  state.selection=null;
  let assets=page.assets||[],selection=0,nextPage=page.next;
  const entries=new Map(),previews=new Map(),versions=new Map(),current=scope(),pagination=el('div');
  const list=el('div',{class:'library-index','aria-label':'资料目录'}),reader=el('section',{class:'library-reader','aria-label':'阅读资料'});
  const layout=el('div',{class:'library-layout'},el('aside',{class:'library-browser'},
    el('header',{class:'library-heading'},el('h1',{},'资料'),button('+',newDraft,'new-document',{'aria-label':'新建文稿',title:'新建文稿'})),
    el('div',{class:'library-search'},input,button('查找',search,'search-button')),
    el('div',{class:'library-filter'},filter),list,
    pagination,state.after?button('回到第一页',async()=>{state.after='';await render();},'text-link'):null),reader);
  const alive=()=>valid(epoch)&&current();
  function makeEntry(asset){
    const identity=JSON.stringify([asset.version,asset.state]);
    if(entries.has(asset.reference)&&versions.get(asset.reference)===identity)return entries.get(asset.reference);
    const title=el('span',{class:'index-title'},asset.state==='stopped'?'正在删除的资料':'正在读取…'),excerpt=el('span',{class:'index-excerpt'});
    const entry=button('',()=>select(asset,true),'index-entry');entry.append(title,excerpt);entries.set(asset.reference,entry);versions.set(asset.reference,identity);previews.delete(asset.reference);
    if(asset.state==='stopped'){entry.disabled=true;return entry;}
    const preview=query({view:'content',handle:asset.handle});previews.set(asset.reference,preview);
    preview.then(p=>{if(!alive())return;title.textContent=titleOf(p.text.text);excerpt.textContent=readingBody(p.text.text).trim().replace(/\s+/g,' ').slice(0,140);}).catch(()=>{if(alive())title.textContent='打开资料';});
    return entry;
  }
  function populate(page){
    assets=page.assets||[];nextPage=page.next;const ids=new Set(assets.map(a=>a.reference));
    for(const id of entries.keys())if(!ids.has(id)){entries.delete(id);previews.delete(id);versions.delete(id);}
    list.replaceChildren(...assets.map(makeEntry));
    for(const [id,entry]of entries)entry.setAttribute('aria-current',String(id===state.libraryReference));
    if(!assets.length)list.append(empty(state.search?'没有找到资料':'这里还没有资料',''));
    pagination.replaceChildren(pager(page,async after=>{state.after=after;await render();},'下一页')||'');
  }
  populate(page);
  state.libraryRefresh=async()=>{
    if(!alive())return;const fresh=await loadPage({view:'assets',query:state.search,state:state.filter,after:state.after});if(!alive())return;
    const signature=items=>JSON.stringify((items||[]).map(a=>[a.reference,a.version,a.state]));
    if(signature(fresh.assets)!==signature(assets)||fresh.next!==nextPage)populate(fresh);
  };
  async function select(asset,explicit){
    const at=++selection,active=()=>alive()&&at===selection,cachedPreview=previews.get(asset.reference);state.libraryReference=asset.reference;state.selection=null;
    for(const [id,entry]of entries)entry.setAttribute('aria-current',String(id===asset.reference));
    if(explicit)layout.classList.add('is-reading');
    reader.replaceChildren(el('div',{class:'reader-loading',role:'status'},'正在打开资料…'));
    try{
      const resolved=await resolve(asset.reference,asset.handle);if(!active())return;
      if(resolved.unavailable){reader.replaceChildren(empty('这份资料已不可用','请选择其他资料。'));return;}
      const item=resolved.assets[0];
      const [first,sourcePage]=await Promise.all([item.version===asset.version?cachedPreview:query({view:'content',handle:item.handle}),query({view:'source',handle:item.handle})]);if(!active())return;
      const source=sourcePage.source||{},body=prose(readingBody(first.text.text),'reader-body');let bodyPage=first.text;
      const continueReading=button('继续阅读',async()=>{const next=await query({view:'content',handle:item.handle,offset:bodyPage.next_offset});if(!active())return;body.textContent+=next.text.text;bodyPage=next.text;continueReading.hidden=!bodyPage.more;},'read-continuation');continueReading.hidden=!bodyPage.more;
      const back=button('返回目录',()=>{layout.classList.remove('is-reading');entries.get(item.reference)?.focus();},'reader-back');
      const actions=el('details',{class:'reader-actions'},el('summary',{'aria-label':'资料操作'},'•••'),el('div',{class:'reader-menu'},button('展开阅读',()=>openAsset(item),'text-link'),button('删除资料',async()=>{const all=await text('content',item.handle,active);if(active())return forget(item,all);},'danger-quiet')));
      const status=tag(statusName(item.state),item.state);
      const metadata=el('div',{class:'reader-byline'},el('span',{},sourceLabel(source)),el('time',{},date(item.updated_at)),status);
      const origin=source.ref||item.has_original?el('footer',{class:'reader-source'},el('span',{class:'reader-source-label'},'来源'),source.ref?el('span',{},source.ref):null,item.has_original?button('查看原始来源',async()=>{const original=await text('original',item.handle,active),details=JSON.parse(await text('original_details',item.handle,active));if(active())dialog('原始来源',el('div',{},el('p',{class:'source'},sourceLabel(details.source)),details.source?.ref?prose(details.source.ref,'source-ref'):null,prose(original)));},'text-link'):null):null;
      reader.replaceChildren(el('header',{class:'reader-toolbar'},back,row(button('编辑',()=>editAsset(item),'reader-edit'),button('查看关联',()=>showRelations(item),'reader-relations'),actions)),el('article',{class:'reader-sheet'},el('header',{class:'reader-title'},el('h2',{},titleOf(first.text.text)),metadata),body,continueReading,origin));
      state.selection=item;state.selectionStatus=status;
    }catch(error){if(active())reader.replaceChildren(empty('暂时无法打开',errorMessage(error),button('重试',()=>select(asset,explicit))));}
  }
  const initial=assets.find(a=>a.reference===state.libraryReference&&a.state!=='stopped')||assets.find(a=>a.state!=='stopped');
  if(initial)select(initial,false);else reader.append(empty(assets.length?'资料正在清理':'还没有可阅读的资料',state.search?'可以换个关键词再试。':'新建一篇文稿，或通过已连接的应用保存资料。',button('新建文稿',newDraft,'primary')));
  return layout;
}
async function openAsset(asset){
  state.libraryRefresh=null;
  releaseGraph();
  state.editor?.suspend();
  invalidate();
  const epoch=++state.epoch;main.replaceChildren(el('p',{class:'loading',role:'status'},'正在打开资料…'));
  const page=await resolve(asset.reference,asset.handle);if(!valid(epoch))return;
  if(page.unavailable){state.selection=null;notice('这份资料已不可用。');return navigate('assets');}
  asset=page.assets[0];
  const [body,sourcePage]=await Promise.all([text('content',asset.handle,()=>valid(epoch)),query({view:'source',handle:asset.handle})]);
  if(!valid(epoch))return;
  state.surface='assets';state.selection=asset;navState();
  const content=prose(readingBody(body)),source=sourcePage.source;
  const switchOriginal=button('查看原始来源',async()=>{
    const original=await text('original',asset.handle,()=>valid(epoch));
    const details=JSON.parse(await text('original_details',asset.handle,()=>valid(epoch)));
    if(valid(epoch))dialog('原始来源',el('div',{},el('p',{class:'source'},sourceLabel({actor:details.source?.actor,ref:details.source?.ref})),details.source?.ref?prose(details.source.ref,'source-ref'):null,prose(original)));
  });
  state.selectionStatus=tag(statusName(asset.state),asset.state);
  main.replaceChildren(el('article',{class:'reading'},el('div',{class:'reading-toolbar'},button('返回资料',()=>navigate('assets'),'back-link'),row(state.selectionStatus,button('编辑',()=>editAsset(asset),'primary'),button('查看关联',()=>showRelations(asset)))) ,
    el('header',{class:'reading-heading'},el('h1',{},body.trim().split(/\r?\n/)[0].slice(0,110)||'资料'),row(el('span',{class:'source'},sourceLabel(source)),el('time',{},date(asset.updated_at))),source.ref?prose(source.ref,'source-ref'):null),
    content,el('footer',{class:'reading-footer'},asset.has_original?switchOriginal:null,button('删除资料',()=>forget(asset,body),'danger-quiet'))));showResume();
}
async function forget(asset,body){
  const operation=operationID();
  await confirm('删除这份资料？',el('div',{},el('p',{},'这份资料、保留的原始内容，以及基于它编辑的草稿都会删除，无法在这里撤销。已导出的备份和其他应用保存的副本不受影响。'),prose(body)), '删除资料',async()=>{
    const result=await act({action:'forget',handle:asset.handle,operation_id:operation});
    invalidate();state.selection=null;state.epoch++;document.querySelectorAll('dialog').forEach(d=>d.remove());
    if(state.editor?.meta.target_reference===asset.reference){clearRescue(state.editor.meta.reference);state.editor.destroy();state.editor=null;}
    const rescue=rescuedInput();if(rescue?.target_reference===asset.reference)clearRescue(rescue.reference);
    main.replaceChildren();notice(statusName(result.state));state.surface='control';state.after='';await render();
  },true);
}
function activityList(items,epoch){
  if(!items.length)return empty('暂无动态','');
  const list=el('ol',{class:'activity-list'});
  for(const item of items){
    const invalid=item.invalidated_relations;
    const reasons=invalid?[invalid.quote_missing?`${invalid.quote_missing} 条关联的引文不再匹配`:'',invalid.quote_ambiguous?`${invalid.quote_ambiguous} 条关联的引文无法唯一定位`:'',invalid.target_unavailable?`${invalid.target_unavailable} 条关联的目标不可用`:''].filter(Boolean).join('；'):'';
    list.append(el('li',{},el('time',{},date(item.at)),el('div',{},el('strong',{},eventName[item.kind]||'资料发生变化'),item.unavailable?el('p',{class:'subtle'},'资料已删除'):item.asset?assetLink(item.asset,epoch):null,item.subject?el('p',{},[item.subject,connectionLabel(item)].filter(Boolean).join(' · ')):null,item.state&&item.state!=='completed'?el('p',{class:'subtle'},statusName(item.state)):null,reasons?el('p',{},reasons):null)));
  }return list;
}
async function eventsPage(epoch){
  const page=await loadPage({view:'recent',after:state.after});
  return el('div',{class:'page events-page'},heading('动态','资料的更新与访问变更。'),activityList(page.activity||[],epoch),pager(page,async after=>{state.after=after;await render();},'查看更早'),state.after?button('回到最近',async()=>{state.after='';await render();}):null);
}
async function openGraphAsset(asset,neighborhood=false,basis=null){
  const epoch=state.epoch,current=scope(),page=await resolve(asset.reference,asset.handle);if(!valid(epoch)||!current())return;
  if(page.unavailable){notice('这份资料已不可用。');return;}
  const item=page.assets[0];if(neighborhood)return showRelations(item);
  const [body,sourcePage]=await Promise.all([text('content',item.handle,()=>valid(epoch)&&current()),query({view:'source',handle:item.handle})]);if(!valid(epoch)||!current())return;
  const source=sourcePage.source,reading=prose(readingBody(body));
  if(basis){const chars=Array.from(body);reading.replaceChildren(chars.slice(0,basis.start_rune).join(''),el('mark',{},chars.slice(basis.start_rune,basis.end_rune).join('')),chars.slice(basis.end_rune).join(''));}
  const contents=el('div',{class:'graph-reading'},el('p',{class:'source'},sourceLabel(source)),source?.ref?prose(source.ref,'source-ref'):null,reading);
  const actions=[{label:'编辑资料',style:'primary',run:async close=>{if(!close.current())return;close();await editAsset(item);}}];
  if(item.has_original)actions.unshift({label:'查看原始来源',run:async close=>{const original=await text('original',item.handle,()=>valid(epoch)&&current()&&close.current()),details=JSON.parse(await text('original_details',item.handle,()=>valid(epoch)&&current()&&close.current()));if(valid(epoch)&&current()&&close.current())dialog('原始来源',el('div',{},el('p',{class:'source'},sourceLabel(details.source)),details.source?.ref?prose(details.source.ref,'source-ref'):null,prose(original)));}});
  const opened=dialog(titleOf(body),contents,actions);if(basis)opened.modal.querySelector('mark')?.scrollIntoView({block:'center'});
}
function graphPage(epoch,assets,page,center=null,relations=null){
  releaseGraph(true);
  const graph=createGraph({assets,next:page.next||'',organization:page.organization,center,relations,valid:()=>valid(epoch),snapshot:state.graphView,onOpen:openGraphAsset,onOverview:()=>navigate('relations')});state.graph=graph;
  return el('div',{class:'page graph-page'},heading('关联图','选择资料查看联系，选择连线查看依据。'),graph.node);
}
async function relationsPage(epoch){
  const page=await loadPage({view:'overview',after:state.after});if(!valid(epoch))return;
  return graphPage(epoch,page.assets||[],page);
}
async function showRelations(asset,after=''){
  releaseGraph();
  state.editor?.suspend();invalidate();
  const epoch=++state.epoch,page=after?{assets:[asset]}:await resolve(asset.reference,asset.handle);if(!valid(epoch))return;
  if(page.unavailable){notice('这份资料已不可用。');return navigate('relations');}
  asset=page.assets[0];const node=await relationView(asset,after,epoch);if(!valid(epoch))return;
  state.surface='relations';state.selection=asset;navState();main.replaceChildren(node);showResume();
}
async function relationView(asset,after,epoch){
  const relations=await loadPage({view:'relations',handle:asset.handle,after});
  if(!valid(epoch))return;
  return graphPage(epoch,[asset],{organization:relations.organization},asset,relations);
}
async function refreshPending(){
  state.pendingDirty=true;
  const page=await loadPage({view:'pending',limit:10});if(!state.active)return;
  document.getElementById('pending-count').textContent=String((page.decisions||[]).length)+(page.next?'+':'');
  document.getElementById('pending-entry').hidden=!page.next&&!page.decisions?.length;
  document.getElementById('pending-entry').classList.toggle('has-pending',!!page.decisions?.length);
  state.pendingDirty=false;
  state.pendingNotice?.();state.pendingNotice=null;
}
function decisionCard(d,historical=false){
  const node=el('article',{class:'decision-card'},row(el('h3',{},d.kind==='enrollment'?'连接应用':d.kind==='handoff'?'迁移资料库':d.kind==='forget'?'删除资料':'更改访问权限'),tag(statusName(d.state))),el('p',{},[d.subject,connectionLabel(d)].filter(Boolean).join(' · ')),el('p',{},d.consequence),d.verification?el('p',{class:'verification'},d.verification):null,
    d.permissions?.length?el('p',{class:'subtle'},d.permissions.map(permissionName).join('、')):null,historical?el('time',{},date(d.at)):null,
    ...(d.targets||[]).map(target=>assetLink(target,state.epoch)));
  if(!historical&&d.state==='awaiting_approval'||!historical&&d.kind==='enrollment'&&d.state==='pending'||!historical&&d.kind==='handoff'&&d.state==='pending'){
    for(const [accept,label] of [[false,'拒绝'],[true,d.kind==='enrollment'?'允许连接':d.kind==='handoff'?'同意迁移':'确认']])node.append(button(label,async()=>{
      await confirm(label,el('div',{},el('h3',{},[d.subject,connectionLabel(d)].filter(Boolean).join(' · ')),el('p',{},(d.permissions||[]).map(permissionName).join('、')),d.verification?el('p',{class:'verification'},d.verification):null,el('p',{},accept?d.consequence:'拒绝后，这次申请不会继续执行。')),label,async()=>{
        await act({action:'decide',handle:d.handle,accept});state.after='';await refreshPending();await render();
      });
    },accept?'primary':'quiet'));
  }
  return node;
}
async function controlPage(epoch){
  const [pending,connections,health,history,grants]=await Promise.all([loadPage({view:'pending',after:state.after}),loadPage({view:'connections'}),query({view:'health'}),loadPage({view:'history',limit:8}),loadPage({view:'draft_grants'})]);
  const decisions=el('div',{class:'decision-list'},...(pending.decisions||[]).map(d=>decisionCard(d)));
  const people=el('div',{class:'connection-list'});
  for(const person of (connections.connections||[]).filter(p=>!p.owner))people.append(el('article',{class:'connection-card'},el('div',{},el('h3',{},person.name),el('p',{class:'subtle'},connectionLabel(person)),el('p',{},person.owner?'你':person.permissions?.length?person.permissions.map(permissionName).join(' · '):'未授予资料访问权限')),person.owner?tag('你'):button('设置权限',()=>permissions(person))));
  const archive=el('div',{class:'archive-grid'},el('div',{},el('h3',{},'下载备份'),el('p',{},'包含资料、草稿和访问权限。请妥善保管备份文件。'),button('下载备份',async()=>{const blob=await request('backup',{}, {blob:true,timeout:180000});download(blob,'ownward-backup.zip');notice('备份已交给浏览器保存。');},'primary')),
    el('div',{},el('h3',{},'从备份恢复'),el('p',{},'从备份创建一份资料库，当前资料不变。'),button('选择备份',restoreArchive)));
  const older=el('details',{},el('summary',{},'查看处理记录'),...(history.decisions||[]).map(d=>decisionCard(d,true)),!history.decisions?.length?el('p',{class:'subtle'},'暂无处理记录。'):null,pager(history,after=>historyDialog(after),'查看更多处理记录'));
  return el('div',{class:'page settings-page'},heading('设置','管理访问权限与资料备份。'),
    section('待确认',decisions.childElementCount?decisions:empty('暂无待确认事项',''),pager(pending,async after=>{state.after=after;await render();}),older),
    section('已连接的应用',people.childElementCount?people:el('p',{class:'subtle'},'暂无应用连接。'),pager(connections,after=>connectionsDialog(after))),
    section('草稿编辑权限',...(grants.grants||[]).map(g=>row(el('span',{},`${g.connection.name} · ${connectionLabel(g.connection)} · 有效至 ${date(g.expires_at)}`),button('取消编辑权限',async()=>{await act({action:'revoke_grant',handle:g.handle});await render();}))),!grants.grants?.length?el('p',{class:'subtle'},'暂无应用获准编辑草稿。'):null,pager(grants,after=>grantsDialog(after))),
    section('备份',health.health!=='normal'?el('p',{class:'callout'},'资料库有未完成的操作，请查看待确认事项。'):null,archive));
}
async function pagedDialog(title,view,after,items,limit){
  const body=el('div',{}),d=dialog(title,body),current=scope();
  async function load(next){
    try{
      const p=await loadPage({view,after:next,...(limit?{limit}:{})});
      if(current()&&d.close.current())body.replaceChildren(...items(p),pager(p,load));
    }catch(error){
      if(!current()||!d.close.current())return;
      if(error.status===409&&next)return load('');
      notice(errorMessage(error),true);
    }
  }
  await load(after);
}
async function historyDialog(after){return pagedDialog('处理记录','history',after,p=>(p.decisions||[]).map(d=>decisionCard(d,true)),8);}
async function connectionsDialog(after){return pagedDialog('更多应用','connections',after,p=>(p.connections||[]).filter(c=>!c.owner).map(c=>row(el('span',{},`${c.name} · ${connectionLabel(c)}`),c.owner?tag('你'):button('设置权限',()=>permissions(c)))));}
async function grantsDialog(after){return pagedDialog('草稿编辑权限','draft_grants',after,p=>(p.grants||[]).map(g=>row(el('span',{},`${g.connection.name} · ${date(g.expires_at)}`),button('撤销',async()=>{await act({action:'revoke_grant',handle:g.handle});await render();}))));}
async function permissions(person){
  const checks=['read','maintain','manage'].map(value=>el('label',{class:'check-row'},el('input',{type:'checkbox',value,checked:(person.permissions||[]).includes(value)}),permissionName(value)));
  const panel=el('div',{},el('p',{},`${person.name} · ${connectionLabel(person)}`),el('p',{},'取消勾选即可收回对应权限。全部取消后，这个应用将不能访问资料。'),...checks);
  const op=operationID();dialog('设置访问权限',panel,[{label:'取消',run:close=>close()},{label:'保存权限',style:'primary',run:async close=>{
    const selected=checks.map(c=>c.querySelector('input')).filter(c=>c.checked).map(c=>c.value);
    const result=await act({action:'permissions',handle:person.handle,permissions:selected,operation_id:op});if(!close.current())return;close();state.after='';notice(statusName(result.state));await render();
  }}]);
}
async function grantDraft(editor,after=''){
  const current=scope();await editor.flush();if(!current()||!editor.live)return;const p=await loadPage({view:'connections',after}),list=el('div',{},el('p',{},'选择一个应用，允许它在一小时内查看和编辑这篇草稿。加入资料或删除草稿后，权限自动结束；对方不能代你确认加入资料。'));
  for(const person of p.connections||[]){if(person.owner)continue;list.append(button(`${person.name} · ${connectionLabel(person)}`,async()=>{
    const result=await act({action:'grant_draft',handle:editor.meta.handle,target:person.handle,seconds:3600});
    // Work-item handles are protocol inputs for the external agent, not owner credentials.
    const instruction=`请继续这篇 Ownward 文稿。使用 ownward_draft_work 先读取当前内容，再按当前版本提交。draft=${result.handle}\ngrant=${result.grant}`;
    dialog('已允许编辑这篇草稿',el('div',{},el('p',{},'复制编辑邀请，粘贴到所选应用的对话中。对方的修改会显示在这篇草稿里。'),button('复制编辑邀请',async()=>{await navigator.clipboard.writeText(instruction);notice('编辑邀请已复制，可粘贴到所选应用的对话中。');})));
  }));}
  if(!(p.connections||[]).some(c=>!c.owner))list.append(el('p',{},'暂无可选择的应用。连接应用后，可以邀请它协助编辑。'));
  list.append(pager(p,next=>grantDraft(editor,next))||'');dialog('让应用协助编辑',list);
}
async function restoreArchive(){
  const file=el('input',{type:'file','aria-label':'选择 Ownward 备份'});
  dialog('恢复备份',el('div',{},el('p',{},'选择 Ownward 备份文件。恢复后的资料会保存到新目录，当前资料不变。'),file),[{label:'开始恢复',style:'primary',run:async close=>{
    if(!file.files[0])throw new Error('请先选择备份文件。');
    const result=await request('restore',file.files[0],{raw:true,type:'application/octet-stream',timeout:180000});if(!close.current())return;close();
    const quoted="'"+result.data_dir.replaceAll("'",navigator.platform.startsWith('Win')?"''":"'\\''")+"'";
    dialog('备份已恢复',el('div',{},el('p',{},'资料已恢复到新目录。复制并在终端运行下方的打开命令，即可使用；当前资料保持不变。'),button('复制打开命令',async()=>{await navigator.clipboard.writeText(`ownward owner-window --data-dir ${quoted}`);notice('已复制打开命令。');})));
  }}]);
}
async function poll(){
  if(!state.active||state.polling)return;
  const run=state.run;
  clearTimeout(state.pollTimer);
  state.polling=true;
  try{
    if(rescueCleanupPending()){retryRescueCleanup();showStorageCleanup();}
    const page=await query({view:'changes',cursor:state.cursor});
    if(!state.active||run!==state.run)return;
    document.getElementById('connection-state').hidden=true;
    document.getElementById('connection-state').textContent='';
    if(page.changed){
      if(page.reset){
        invalidate();state.epoch++;state.after='';
        const currentScope=scope();
        document.querySelectorAll('dialog').forEach(d=>d.remove());
        const visibleEditor=state.editor&&main.contains(state.editor.node);
        state.editor?.quarantine();main.replaceChildren(el('p',{class:'loading'},'正在核对资料是否仍可使用…'));
        if(state.editor){await state.editor.reconcile(true);}
        if(!state.active||!currentScope())return;
        const rescue=rescuedInput();if(rescue?.reference){const current=await resolve(rescue.reference);if(!state.active||!currentScope())return;if(current.unavailable){if(rescue.publishID)pendingReceiptNotice(retainReceipt(rescue));else clearRescue(rescue.reference);}}
        if(visibleEditor&&state.editor?.live){main.replaceChildren(state.editor.node);}
        else{state.selection=null;await render();}
        showResume();
      }else{
        // The retained draft and the visible reading/list surface are separate
        // obligations. Neither may consume the other's change checkpoint.
        if(state.editor)await state.editor.reconcile();
        if(!state.active||run!==state.run)return;
        if(state.selection){
          const selected=state.selection,epoch=state.epoch,current=await resolve(selected.reference);
          if(!valid(epoch)||state.selection!==selected)return;
          if(current.unavailable){state.selection=null;await render();}
          else if(current.assets[0].version!==selected.version){state.selection=null;notice('内容有变化，请重新打开核对。');await render();}
          else{
            const asset=current.assets[0];
            // Body equality says nothing about organization or relations.
            // Refresh only the visible projection, without navigation or
            // invalidating an editor's in-flight operation.
            if(state.surface==='relations'){
              const node=await relationView(asset,'',epoch);
              if(!valid(epoch)||state.selection!==selected)return;
              main.replaceChildren(node);showResume();
            }else if(state.surface==='assets'&&main.contains(state.selectionStatus)){
              state.selectionStatus.textContent=statusName(asset.state);state.selectionStatus.className=`tag ${asset.state}`;
              await state.libraryRefresh?.();
              if(!valid(epoch)||state.selection!==selected)return;
            }
            state.selection=asset;
          }
        }
        else if(!state.editor||!main.contains(state.editor.node)){
          if(main.contains(document.activeElement)&&['INPUT','SELECT'].includes(document.activeElement.tagName)){await refreshPending();return;}
          state.after='';await render();
        }
      }
      if(!state.active||run!==state.run)return;
      await refreshPending();
      if(!state.active||run!==state.run)return;
      state.cursor=page.cursor;
    }else if(state.pendingDirty)await refreshPending();
  }catch(error){if(state.active&&run===state.run&&error.status!==-1){document.getElementById('connection-state').hidden=false;document.getElementById('connection-state').textContent=error.status===429?'更新稍有延迟，稍后自动重试。':'暂时无法更新，正在重试。';}}
  finally{if(run===state.run){state.polling=false;if(state.active)state.pollTimer=setTimeout(poll,document.hidden?12000:2200);}}
}
