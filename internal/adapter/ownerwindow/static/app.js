import {createReader} from './reading.js';
import {createGraph} from './graph.js';
import {query, act, resolve, text, request, quit, operationID, invalidate, scope} from './api.js';
import {Editor, rescuedInput, rescueNeedsWindow, rescueCleanupPending, retryRescueCleanup, clearRescue, retainReceipt} from './editor.js';
import {el, button, row, heading, empty, prose, tag, date, notice, clearNotice, statusName, permissionName, dialog, confirm, download, errorMessage, documentView, appearanceControls, applyAppearance, setImmersive, createSelect} from './ui.js';

const main=document.getElementById('main');
const state={surface:'home',reader:null,readPositions:new Map(),graphMem:new Map(),epoch:0,graph:null,graphView:null,cursor:'',editor:null,selection:null,active:true,polling:false,after:'',search:'',filter:'',exiting:false,bound:false,run:0,pendingDirty:true};
const surfaces=[['drafts','文稿'],['assets','资料'],['relations','关联'],['events','动态'],['control','设置']];
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
  applyAppearance();
  clearReceiptNotice();
  state.pendingNotice?.();state.pendingNotice=null;
  const run=++state.run;clearTimeout(state.pollTimer);state.polling=false;state.active=true;state.exiting=false;state.pendingDirty=true;
  retryRescueCleanup();
  state.cursor=health.cursor;
  document.getElementById('entry').hidden=true;document.getElementById('shell').hidden=false;
  if(!state.bound){state.bound=true;
  const nav=document.getElementById('navigation');
  for(const [id,label] of surfaces)nav.append(button(label,()=>navigate(id),'nav-item',{'data-surface':id,'aria-label':label}));
  document.getElementById('home-entry').onclick=()=>navigate('home');
  document.getElementById('pending-entry').onclick=()=>{const panel=document.getElementById('pending-panel');panel.hidden=!panel.hidden;document.getElementById('pending-entry').setAttribute('aria-expanded',String(!panel.hidden));};
  window.addEventListener('owner-auth-lost',()=>lock('从电脑中的 Ownward 入口重新打开，即可继续。未保存的文字仍保留在此页面。',true));
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
async function stopOwnward(){
  if(state.exiting||!state.active)return;
  try{
    const editor=state.editor,current=scope();
    const snapshot=()=>JSON.stringify([state.editor?.value,state.editor?.dirty,state.editor?.conflict,state.editor?.publishID,state.editor?.rebase,state.editor?.discardPending,state.editor?.refreshPending,state.editor?.pendingSave,rescuedInput()]);
    const shown=snapshot();
    const exit=async()=>{
      if(state.exiting)return;
      if(!state.active||!current()||state.editor!==editor||snapshot()!==shown)throw new Error('待处理内容已有变化，请重新核对后停止。');
      if(editor?.busy||editor?.finishing||editor?.composing)throw new Error('文稿操作仍在进行，请稍后停止。');
      state.exiting=true;state.run++;clearTimeout(state.pollTimer);state.polling=false;invalidate();
      stopFeedback(true);
      try{
        await quit();
        lock('可以关闭这个页面。再次点击电脑中的 Ownward，即可重新启动并打开，已保存的资料仍在。',false);
        document.getElementById('entry-title').textContent='Ownward 已停止';
        document.getElementById('entry-help').hidden=true;
        document.getElementById('entry').dataset.phase='closed';
      }catch(error){
        state.exiting=false;
        stopFeedback(false,error.status===401?'未能停止，请从电脑中的 Ownward 入口重新打开后再试。':'尚未确认停止，请重试。未保存的文字仍保留。');
        if(state.active)state.pollTimer=setTimeout(poll,2200);
      }
    };
    const rescue=rescuedInput(),input=state.editor?.value??rescue?.text;
    if(pendingWork(state.editor)||rescue?.reference){await confirm('停止前保留文字',el('div',{},el('p',{},'还有尚未完成核对的输入或操作。停止后会清除未保存的暂存文字；已保存的资料和草稿会保留，其他应用与这份资料的连接也会断开。'),typeof input==='string'?button('下载当前文字',()=>download(new Blob([input],{type:'text/plain;charset=utf-8'}),'未完成的文稿.txt')):null),'清除暂存并停止',exit,true);}else await exit();
  }catch(error){notice(errorMessage(error),true);}
}
function stopFeedback(stopping,message=''){
  const control=document.getElementById('stop-ownward'),feedback=document.getElementById('stop-feedback');
  if(control){control.disabled=stopping;control.textContent=stopping?'正在停止…':'停止 Ownward';}
  if(feedback){feedback.hidden=!stopping&&!message;feedback.textContent=stopping?'正在停止后台服务，请稍候。':message;}
}
function lock(message,preserveInput=!state.exiting){
  releaseReader();state.readPositions.clear();state.graphMem.clear();state.pendingSignature=null;document.getElementById('pending-panel').replaceChildren();document.getElementById('pending-panel').hidden=true;
  state.libraryRefresh=null;
  releaseGraph();
  clearReceiptNotice();
  invalidate();
  state.active=false;state.epoch++;state.run++;clearTimeout(state.pollTimer);state.polling=false;
  if(preserveInput)state.editor?.persist();state.editor?.destroy();state.editor=null;state.selection=null;
  if(!preserveInput)clearRescue();
  document.querySelectorAll('dialog').forEach(d=>d.remove());main.replaceChildren();
  document.getElementById('shell').hidden=true;document.getElementById('entry').hidden=false;
  document.getElementById('entry').dataset.phase='waiting';
  document.getElementById('entry-title').textContent=state.exiting?'Ownward 已停止':'请重新打开 Ownward';
  document.getElementById('entry-help').hidden=false;document.getElementById('entry-retry').hidden=true;
  document.getElementById('status').textContent=message+(rescueCleanupPending()?' 浏览器暂存尚待清理，请勿刷新或关闭。':rescueNeedsWindow()?' 暂存仅在当前窗口，请勿刷新或关闭；重新验证会在此窗口继续核对。':'');
  showStorageCleanup();
}
async function navigate(surface){
  if(!state.active||state.exiting)return;releaseReader();setImmersive(false);releaseGraph(true);state.graphView=null;
  state.editor?.suspend();
  invalidate();
  state.surface=surface;state.selection=null;state.after='';clearNotice();await render();
}
function navState(){document.querySelectorAll('[data-surface]').forEach(b=>{b.setAttribute('aria-current',b.dataset.surface===state.surface?'page':'false');});}
function releaseReader(){state.reader?.destroy();state.reader=null;}
function readingPosition(asset){const p=state.readPositions.get(asset.reference);return p?.version===asset.version?p.scroll:0;}
function rememberPosition(asset,scroll){state.readPositions.set(asset.reference,{version:asset.version,scroll});if(state.readPositions.size>50)state.readPositions.delete(state.readPositions.keys().next().value);}
function releaseGraph(remember=false){if(state.graph){state.graphView=remember?state.graph.capture():null;if(remember&&state.graphKey){state.graphMem.set(state.graphKey,state.graphView);if(state.graphMem.size>30)state.graphMem.delete(state.graphMem.keys().next().value);}state.graph.destroy();state.graph=null;}}
async function render(){
  state.renderFailed=false;
  releaseReader();setImmersive(false);
  state.libraryRefresh=null;
  releaseGraph(state.surface==='relations');
  const epoch=++state.epoch;navState();
  main.replaceChildren(el('p',{class:'loading',role:'status'},'正在读取…'));
  try{
    const node=await ({home:homePage,drafts:draftsPage,assets:assetsPage,relations:relationsPage,events:eventsPage,control:controlPage}[state.surface])(epoch);
    if(valid(epoch)){main.replaceChildren(node);showResume();main.focus({preventScroll:true});}
  }catch(error){if(valid(epoch)){
    if(error.status===409&&state.after){state.after='';notice('资料有变化，已返回当前筛选的第一页。');return render();}
    state.renderFailed=error.status===0||error.status===429||error.status>=500;
    const failure=state.renderFailed?empty('正在恢复资料','稍后会自动继续。'):empty('暂时无法读取',errorMessage(error),button('重试',()=>{state.after='';return render();}));
    main.replaceChildren(state.surface==='control'?el('div',{class:'page settings-page'},heading('设置',''),failure,runtimeSettings()):failure);showResume();
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
  releaseReader();setImmersive(true);
  state.libraryRefresh=null;
  releaseGraph();
  const editor=state.editor;if(!editor?.live)return;
  invalidate();
  state.epoch++;state.surface='drafts';state.selection=null;navState();main.replaceChildren(editor.node);
  await editor.resume();if(editor.live&&!editor.quarantined)editor.input.focus();
}
async function loadPage(input){return query({limit:12,...input});}
function draftRows(drafts,epoch){
  const list=el('div',{class:'draft-list'});
  for(const draft of drafts){
    const title=el('span',{class:'draft-name'},'正在读取文稿…');
    const item=button('',()=>openDraft(draft.reference),'draft-row');item.append(el('span',{class:'draft-row-body'},title,el('span',{class:'subtle'},draft.target?'修改保存在草稿中':'草稿已保存')),el('time',{},date(draft.updated_at)),el('span',{class:'draft-continue'},'继续'));list.append(item);
    query({view:'draft_content',handle:draft.handle}).then(p=>{if(valid(epoch))title.textContent=titleOf(p.text.text)||'未命名文稿';}).catch(()=>{if(valid(epoch))title.textContent='继续文稿';});
  }
  return list;
}
async function homePage(epoch){
  const [drafts,assets,recent]=await Promise.all([loadPage({view:'drafts',limit:3}),loadPage({view:'assets',limit:4}),loadPage({view:'recent',limit:5})]);if(!valid(epoch))return;
  const shelf=el('div',{class:'home-shelf'});for(const asset of assets.assets||[])shelf.append(await assetCard(asset,epoch));
  return el('div',{class:'page home-page'},heading('最近','',button('新建文稿',newDraft,'primary')),
    el('section',{class:'home-writing'},el('div',{class:'section-title'},el('h2',{},'进行中的文稿'),button('全部文稿',()=>navigate('drafts'),'text-link')),
      drafts.drafts?.length?draftRows(drafts.drafts,epoch):el('p',{class:'empty-line'},'没有进行中的文稿。',button('新建',newDraft,'text-link'))),
    el('div',{class:'home-lower'},section('近期资料',shelf.childElementCount?shelf:el('p',{class:'empty-line'},'还没有保存的资料。'),button('查看资料',()=>navigate('assets'),'text-link')),
      section('最近动态',activityList(recent.activity||[],epoch),button('全部动态',()=>navigate('events'),'text-link'))));
}
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
  const [drafts,assets]=await Promise.all([loadPage({view:'drafts',after:state.after}),loadPage({view:'continuable',limit:4})]);if(!valid(epoch))return;
  const cards=el('div',{class:'continuable-list'});for(const asset of assets.assets||[])cards.append(await assetCard(asset,epoch));
  return el('div',{class:'page drafts-page'},heading('文稿','',button('新建文稿',newDraft,'primary')),section('进行中',drafts.drafts?.length?draftRows(drafts.drafts,epoch):el('p',{class:'empty-line'},'没有进行中的文稿。'),pager(drafts,async after=>{state.after=after;await render();})),section('可以继续',cards.childElementCount?cards:el('p',{class:'empty-line'},'保存后的文稿可以在这里继续编辑。')));
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
  releaseReader();state.libraryRefresh=null;
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
  if(show){setImmersive(true);state.surface='drafts';state.selection=null;navState();}
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
  const filter=createSelect({label:'资料状态',skin:'toolbar',value:state.filter,options:Object.entries({'':'全部状态',ready:'已整理',pending:'待整理',stopped:'已停止使用'}).map(([value,label])=>({value,label})),onChange:value=>{state.filter=value;search();}});
  const search=async()=>{state.search=input.value;state.after='';state.libraryReference=null;await render();};
  input.addEventListener('keydown',event=>{if(event.key==='Enter')search();});
  const page=await loadPage({view:'assets',query:state.search,state:state.filter,after:state.after});if(!valid(epoch))return;
  state.selection=null;
  let assets=page.assets||[],selection=0,nextPage=page.next,indexLoading=false,windowStart=-1;
  const entries=new Map(),previews=new Map(),versions=new Map(),current=scope(),pagination=el('div');
  const list=el('div',{class:'library-index','aria-label':'资料目录'}),reader=el('section',{class:'library-reader','aria-label':'阅读资料'});
  const layout=el('div',{class:'library-layout'},el('aside',{class:'library-browser'},
    el('header',{class:'library-heading'},el('h1',{},'资料'),button('+',newDraft,'new-document',{'aria-label':'新建文稿',title:'新建文稿'})),
    el('div',{class:'library-search'},input,button('查找',search,'search-button')),
    el('div',{class:'library-filter'},filter.node),list,
    pagination,state.after?button('回到第一页',async()=>{state.after='';await render();},'text-link'):null),reader);
  const alive=()=>valid(epoch)&&current();
  const listed=el('span',{class:'listed-count',role:'status'});layout.querySelector?.('.library-filter')?.append(listed);
  function makeEntry(asset){
    const identity=JSON.stringify([asset.version,asset.state]);
    if(entries.has(asset.reference)&&versions.get(asset.reference)===identity)return entries.get(asset.reference);
    const title=el('span',{class:'index-title'},asset.state==='stopped'?'正在删除的资料':'正在读取…'),excerpt=el('span',{class:'index-excerpt'});
    const entry=button('',()=>select(asset,true),'index-entry');entry.append(title,excerpt,el('span',{class:'index-state '+asset.state,title:statusName(asset.state),'aria-label':statusName(asset.state)},asset.state==='ready'?'●':asset.state==='pending'?'○':'◦'));entries.set(asset.reference,entry);versions.set(asset.reference,identity);previews.delete(asset.reference);
    if(asset.state==='stopped'){entry.disabled=true;return entry;}
    query({view:'source',handle:asset.handle}).then(p=>{if(alive())entry.append(el('span',{class:p.source?.authored?'index-origin authored':'index-origin'},p.source?.authored?'我创建的':p.source?.actor||''));}).catch(()=>{});
    const preview=query({view:'content',handle:asset.handle});previews.set(asset.reference,preview);
    preview.then(p=>{if(!alive())return;title.textContent=titleOf(p.text.text);excerpt.textContent=readingBody(p.text.text).trim().replace(/\s+/g,' ').slice(0,140);}).catch(()=>{if(alive())title.textContent='打开资料';});
    return entry;
  }
  function drawIndex(force=false){
    const rows=[];let height=0;
    for(const value of ['ready','pending','stopped']){const group=assets.filter(a=>a.state===value);if(!group.length)continue;rows.push({top:height,height:36,label:`${statusName(value)} · 已列出 ${group.length} 条`});height+=36;for(const asset of group){rows.push({top:height,height:112,asset});height+=112;}}
    const scroll=Math.min(list.scrollTop||0,Math.max(0,height-112)),start=Math.max(0,rows.findIndex(r=>r.top+r.height>=Math.max(0,scroll-336)));
    if(!force&&start===windowStart)return;windowStart=start;
    const visible=rows.slice(start,start+28),bottomAt=visible.length?visible.at(-1).top+visible.at(-1).height:0;
    const top=el('div',{'aria-hidden':'true',style:`height:${rows[start]?.top||0}px;flex-shrink:0`}),bottom=el('div',{'aria-hidden':'true',style:`height:${Math.max(0,height-bottomAt)}px;flex-shrink:0`});
    list.replaceChildren(top,...visible.map(r=>r.asset?makeEntry(r.asset):el('h2',{class:'index-group'},r.label)),bottom);
    for(const [id,entry]of entries)entry.setAttribute('aria-current',String(id===state.libraryReference));
    if(!assets.length)list.append(empty(state.search?'没有找到资料':'这里还没有资料',''));
    listed.textContent=`已列出 ${assets.length} 条`;
  }
  async function moreIndex(){
    if(indexLoading||!nextPage||!alive())return;indexLoading=true;
    try{const fresh=await loadPage({view:'assets',query:state.search,state:state.filter,after:nextPage});if(!alive())return;
      if(assets.length>=192){state.after=nextPage;await render();return;}
      const ids=new Set(assets.map(a=>a.reference));assets.push(...(fresh.assets||[]).filter(a=>!ids.has(a.reference)));nextPage=fresh.next;drawIndex(true);showPagination();
    }finally{indexLoading=false;}
  }
  function showPagination(){pagination.replaceChildren(nextPage?button(assets.length>=192?'继续浏览后续资料':'继续查看更多',moreIndex,'text-link'):'');}
  function populate(page){
    assets=page.assets||[];nextPage=page.next;const ids=new Set(assets.map(a=>a.reference));
    for(const id of entries.keys())if(!ids.has(id)){entries.delete(id);previews.delete(id);versions.delete(id);}
    drawIndex(true);showPagination();
  }
  list.addEventListener('scroll',()=>{drawIndex();if(list.scrollHeight-list.scrollTop-list.clientHeight<200&&assets.length<192)moreIndex().catch(e=>notice(errorMessage(e),true));},{passive:true});
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
    releaseReader();reader.replaceChildren(el('div',{class:'reader-loading',role:'status'},'正在打开资料…'));
    try{
      const resolved=await resolve(asset.reference,asset.handle);if(!active())return;
      if(resolved.unavailable){reader.replaceChildren(empty('这份资料已不可用','请选择其他资料。'));return;}
      const item=resolved.assets[0];
      const [first,sourcePage]=await Promise.all([item.version===asset.version&&cachedPreview?cachedPreview:query({view:'content',handle:item.handle}),query({view:'source',handle:item.handle})]);if(!active())return;
      releaseReader();
      const view=createReader({asset:item,first,source:sourcePage.source||{},valid:active,
        onEdit:()=>editAsset(item),onRelations:quote=>showRelations(item,'',quote),onForget:body=>forget(item,body),
        onBack:()=>{layout.classList.remove('is-reading');setImmersive(false);entries.get(item.reference)?.focus();},onReload:()=>select(item,true),
        position:readingPosition(item),onPosition:(value,ratio)=>{entries.get(item.reference)?.style?.setProperty('--read-progress',`${Math.round(ratio*100)}%`);rememberPosition(item,value);}});
      state.reader=view;reader.replaceChildren(view.node);state.selection=item;state.selectionStatus=view.status;
    }catch(error){if(active())reader.replaceChildren(empty('暂时无法打开',errorMessage(error),button('重试',()=>select(asset,explicit))));}
  }
  const initial=assets.find(a=>a.reference===state.libraryReference&&a.state!=='stopped')||assets.find(a=>a.state!=='stopped');
  if(initial)select(initial,false);else reader.append(empty(assets.length?'资料正在清理':'还没有可阅读的资料',state.search?'可以换个关键词再试。':'新建一篇文稿，或通过已连接的应用保存资料。',button('新建文稿',newDraft,'primary')));
  return layout;
}
async function openAsset(asset){
  releaseReader();state.libraryRefresh=null;releaseGraph(true);state.editor?.suspend();invalidate();
  const epoch=++state.epoch;main.replaceChildren(el('p',{class:'loading',role:'status'},'正在打开资料…'));
  const page=await resolve(asset.reference,asset.handle);if(!valid(epoch))return;
  if(page.unavailable){state.selection=null;notice('这份资料已不可用。');return navigate('assets');}
  asset=page.assets[0];const [first,source]=await Promise.all([query({view:'content',handle:asset.handle}),query({view:'source',handle:asset.handle})]);if(!valid(epoch))return;
  state.surface='assets';state.selection=asset;navState();setImmersive(true);
  const view=createReader({asset,first,source:source.source||{},valid:()=>valid(epoch),onEdit:()=>editAsset(asset),onRelations:quote=>showRelations(asset,'',quote),onForget:body=>forget(asset,body),onBack:()=>navigate('assets'),onReload:()=>openAsset(asset),position:readingPosition(asset),onPosition:value=>rememberPosition(asset,value)});
  state.reader=view;state.selectionStatus=view.status;main.replaceChildren(view.node);showResume();
}
async function forget(asset,body){
  const operation=operationID();
  await confirm('删除这份资料？',el('div',{},el('p',{},'这份资料、保留的原始内容，以及基于它编辑的草稿都会删除，无法在这里撤销。已导出的备份和其他应用保存的副本不受影响。'),prose(body)), '删除资料',async()=>{
    const result=await act({action:'forget',handle:asset.handle,operation_id:operation});
    releaseReader();state.readPositions.clear();state.graphMem.clear();invalidate();state.selection=null;state.epoch++;document.querySelectorAll('dialog').forEach(d=>d.remove());
    if(state.editor?.meta.target_reference===asset.reference){clearRescue(state.editor.meta.reference);state.editor.destroy();state.editor=null;}
    const rescue=rescuedInput();if(rescue?.target_reference===asset.reference)clearRescue(rescue.reference);
    main.replaceChildren();notice(statusName(result.state));state.surface='control';state.after='';await render();
  },true);
}
function activityList(items,epoch){
  if(!items.length)return empty('暂无动态','');
  const list=el('ol',{class:'activity-list'});
  let previousDay='';
  for(const item of items){
    const day=item.at?new Date(item.at).toLocaleDateString('zh-CN'):'';if(day!==previousDay){const today=new Date(),yesterday=new Date();yesterday.setDate(today.getDate()-1);list.append(el('li',{class:'activity-day'},day===today.toLocaleDateString('zh-CN')?'今天':day===yesterday.toLocaleDateString('zh-CN')?'昨天':day));previousDay=day;}
    const invalid=item.invalidated_relations;
    const reasons=invalid?[invalid.quote_missing?`${invalid.quote_missing} 条关联的引文不再匹配`:'',invalid.quote_ambiguous?`${invalid.quote_ambiguous} 条关联的引文无法唯一定位`:'',invalid.target_unavailable?`${invalid.target_unavailable} 条关联的目标不可用`:''].filter(Boolean).join('；'):'';
    list.append(el('li',{class:item.unavailable?'forgotten-event':''},el('time',{},date(item.at)),el('div',{},el('strong',{},eventName[item.kind]||'资料发生变化'),item.unavailable?el('p',{class:'forgotten-slot'},'已删除的资料'):item.asset?assetLink(item.asset,epoch):null,item.subject?el('p',{},[item.subject,connectionLabel(item)].filter(Boolean).join(' · ')):null,item.state&&item.state!=='completed'?el('p',{class:'subtle'},statusName(item.state)):null,reasons?el('p',{},reasons):null)));
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
  const [first,sourcePage]=await Promise.all([query({view:'content',handle:item.handle}),query({view:'source',handle:item.handle})]);if(!valid(epoch)||!current())return;
  let opened;const view=createReader({asset:item,first,source:sourcePage.source||{},basis,valid:()=>valid(epoch)&&current()&&!!opened?.close.current(),onEdit:async()=>{opened.close();await editAsset(item);},onRelations:quote=>{opened.close();return showRelations(item,'',quote);},onForget:body=>forget(item,body),onBack:()=>opened.close()});
  opened=dialog('阅读资料',view.node);opened.modal.classList.add('reading-modal');opened.modal.addEventListener('close',()=>view.destroy(),{once:true});
}
function graphPage(epoch,assets,page,center=null,relations=null){
  releaseGraph(true);state.graphKey=center?.reference||'overview';
  const graph=createGraph({assets,next:page.next||'',organization:page.organization,center,relations,valid:()=>valid(epoch),snapshot:state.graphMem.get(state.graphKey)||state.graphView,selectionQuote:state.selectionQuote,onOpen:openGraphAsset,onOverview:()=>navigate('relations')});state.graph=graph;
  return el('div',{class:'page graph-page'},heading('关联图','选择资料查看联系，选择连线查看依据。'),graph.node);
}
async function relationsPage(epoch){
  const page=await loadPage({view:'overview',after:state.after});if(!valid(epoch))return;
  return graphPage(epoch,page.assets||[],page);
}
async function showRelations(asset,after='',quote=''){
  releaseReader();setImmersive(false);state.selectionQuote=quote;releaseGraph(true);
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
  state.pendingDirty=true;const current=scope();
  const page=await loadPage({view:'pending',limit:10});if(!state.active||!current())return;
  const trigger=document.getElementById('pending-entry'),panel=document.getElementById('pending-panel');
  document.getElementById('pending-count').textContent=String((page.decisions||[]).length)+(page.next?'+':'');
  trigger.hidden=!page.next&&!page.decisions?.length;trigger.classList.toggle('has-pending',!!page.decisions?.length);
  const signature=JSON.stringify(page.decisions||[])+page.next;
  if(signature!==state.pendingSignature){panel.replaceChildren(...[el('div',{class:'pending-heading'},el('h2',{},'待确认'),button('收起',()=>{panel.hidden=true;trigger.setAttribute('aria-expanded','false');trigger.focus();},'quiet')),el('div',{class:'decision-list'},...(page.decisions||[]).map(d=>decisionCard(d))),pager(page,after=>pendingMore(after),'继续查看')].filter(Boolean));state.pendingSignature=signature;}
  if(trigger.hidden)panel.hidden=true;
  state.pendingDirty=false;state.pendingNotice?.();state.pendingNotice=null;
}
async function pendingMore(after){
  const current=scope(),page=await loadPage({view:'pending',limit:10,after});if(!current())return;
  const panel=document.getElementById('pending-panel');panel.replaceChildren(...[el('h2',{},'待确认'),...(page.decisions||[]).map(d=>decisionCard(d)),pager(page,pendingMore),button('回到最近',()=>{state.pendingSignature=null;return refreshPending();})].filter(Boolean));
}
function decisionCard(d,historical=false){
  const node=el('article',{class:'decision-card'},row(el('h3',{},d.kind==='enrollment'?'连接应用':d.kind==='handoff'?'迁移资料库':d.kind==='forget'?'删除资料':'更改访问权限'),tag(statusName(d.state))),el('p',{},[d.subject,connectionLabel(d)].filter(Boolean).join(' · ')),el('p',{},d.consequence),d.verification?el('p',{class:'verification'},d.verification):null,
    d.permissions?.length?el('p',{class:'subtle'},d.permissions.map(permissionName).join('、')):null,historical?el('time',{},date(d.at)):null,
    ...(d.targets||[]).map(target=>assetLink(target,state.epoch)));
  if(!historical&&(d.state==='awaiting_approval'||['enrollment','handoff'].includes(d.kind)&&d.state==='pending')){
    for(const [accept,label]of [[false,'拒绝'],[true,d.kind==='enrollment'?'允许连接':d.kind==='handoff'?'同意迁移':'确认']])node.append(button(label,async()=>{
      await act({action:'decide',handle:d.handle,accept});node.classList.add('decided');
      if(!matchMedia('(prefers-reduced-motion: reduce)').matches)await new Promise(r=>setTimeout(r,220));
      state.after='';await refreshPending();if(['control','events'].includes(state.surface))await render();
    },accept?'primary':'quiet'));
  }
  return node;
}
function connectionCard(person){
  const current=scope(),confirmation=el('div',{class:'permission-confirm',hidden:true});
  const node=el('article',{class:'connection-card'},el('div',{},el('h3',{},person.name),el('p',{class:'subtle'},connectionLabel(person)),el('p',{},person.permissions?.length?person.permissions.map(permissionName).join(' · '):'未授予资料访问权限')));
  const controls=row(button('设置权限',()=>permissions(person)));
  if(person.permissions?.length)controls.append(button('收回权限',()=>{const operation=operationID();controls.hidden=true;confirmation.hidden=false;confirmation.replaceChildren(el('p',{},'收回后，这个应用将不能查看或修改资料；已有资料保留。'),row(button('保留权限',()=>{confirmation.hidden=true;controls.hidden=false;}),button('确认收回',async()=>{if(!current()||!node.isConnected)return;const result=await act({action:'permissions',handle:person.handle,permissions:[],operation_id:operation});if(!current()||!node.isConnected)return;notice(statusName(result.state));await render();},'danger-quiet')));},'text-link'));
  node.append(controls,confirmation);return node;
}
async function controlPage(epoch){
  const [connections,health,history,grants]=await Promise.all([loadPage({view:'connections'}),query({view:'health'}),loadPage({view:'history',limit:8}),loadPage({view:'draft_grants'})]);
  const people=el('div',{class:'connection-list'});
  for(const person of (connections.connections||[]).filter(p=>!p.owner))people.append(connectionCard(person));
  const archive=el('div',{class:'archive-grid'},el('div',{},el('h3',{},'下载备份'),el('p',{},'包含资料、草稿和访问权限。请妥善保管备份文件。'),button('下载备份',async()=>{const blob=await request('backup',{}, {blob:true,timeout:180000});download(blob,'ownward-backup.zip');notice('备份已交给浏览器保存。');},'primary')),
    el('div',{},el('h3',{},'从备份恢复'),el('p',{},'从备份创建一份资料库，当前资料不变。'),button('选择备份',restoreArchive),button('查看恢复结果',restoredArchives)));
  const older=el('details',{},el('summary',{},'查看处理记录'),...(history.decisions||[]).map(d=>decisionCard(d,true)),!history.decisions?.length?el('p',{class:'subtle'},'暂无处理记录。'):null,pager(history,after=>historyDialog(after),'查看更多处理记录'));
  return el('div',{class:'page settings-page'},heading('设置',''),
    section('显示',appearanceControls()),
    section('处理记录',older),
    section('已连接的应用',people.childElementCount?people:el('p',{class:'subtle'},'暂无应用连接。'),pager(connections,after=>connectionsDialog(after))),
    section('草稿编辑权限',...(grants.grants||[]).map(g=>row(el('span',{},`${g.connection.name} · ${connectionLabel(g.connection)} · 有效至 ${date(g.expires_at)}`),button('取消编辑权限',async()=>{await act({action:'revoke_grant',handle:g.handle});await render();}))),!grants.grants?.length?el('p',{class:'subtle'},'暂无应用获准编辑草稿。'):null,pager(grants,after=>grantsDialog(after))),
    section('备份',health.health!=='normal'?el('p',{class:'callout'},'资料库有未完成的操作，请查看待确认事项。'):null,archive),
    runtimeSettings());
}
function runtimeSettings(){
  return el('section',{class:'runtime-settings','aria-label':'关闭与停止'},
    el('div',{class:'runtime-note'},el('h2',{},'关闭网页'),el('p',{},'直接关闭网页，Ownward 仍会在后台正常运行，已连接的应用不受影响。再次点击电脑中的 Ownward，即可打开网页。')),
    el('div',{class:'runtime-stop'},
      el('div',{},el('h2',{},'停止运行'),el('p',{id:'runtime-description'},'停止后台服务，已连接的应用也会断开，已保存的资料会保留。再次点击电脑中的 Ownward，会自动启动服务并打开网页。')),
      button('停止 Ownward',stopOwnward,'quiet',{id:'stop-ownward','aria-describedby':'runtime-description stop-feedback'})),
    el('p',{id:'stop-feedback',class:'stop-feedback',role:'status',hidden:true}));
}
async function pagedDialog(title,view,after,items,limit){
  const body=el('div',{}),d=dialog(title,body),current=scope();
  async function load(next){
    try{
      const p=await loadPage({view,after:next,...(limit?{limit}:{})});
      if(current()&&d.close.current())body.replaceChildren(...[...items(p),pager(p,load)].filter(Boolean));
    }catch(error){
      if(!current()||!d.close.current())return;
      if(error.status===409&&next)return load('');
      notice(errorMessage(error),true);
    }
  }
  await load(after);
}
async function historyDialog(after){return pagedDialog('处理记录','history',after,p=>(p.decisions||[]).map(d=>decisionCard(d,true)),8);}
async function connectionsDialog(after){return pagedDialog('更多应用','connections',after,p=>(p.connections||[]).filter(c=>!c.owner).map(connectionCard));}
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
    await request('restore',file.files[0],{raw:true,type:'application/octet-stream',timeout:180000});if(!close.current())return;close();
    await restoredArchives();
  }}]);
}
async function restoredArchives(){
  const current=scope(),items=await request('restored',{action:'list'});if(!current())return;
  const body=el('div',{},el('p',{},'恢复出的资料可以单独打开。原资料和日常入口保持不变。'));
  let view;
  for(const item of items||[]){
    const actions=row(),entry=el('div',{},el('p',{},`恢复于 ${date(item.created)}`),actions);
    if(item.ready){const open=button('打开恢复的资料',async()=>{
      const result=await request('restored',{action:'open',id:item.id},{timeout:180000});if(!current()||!view.close.current())return;notice('恢复的资料已在浏览器中打开。');
      if(!result.default_available)return;
      actions.replaceChildren(open,button('以后打开这份资料',()=>dialog('更换日常打开的资料',el('p',{},'以后点击 Ownward 将打开这份恢复的资料。原资料会保留，已连接的应用不会自动切换。'),[
        {label:'取消',run:close=>close()},
        {label:'确认更换',style:'primary',run:async close=>{await request('restored',{action:'default',id:item.id,revision:result.default_revision});if(!current()||!close.current())return;close();if(view.close.current())actions.replaceChildren(open,el('span',{class:'subtle'},'当前日常资料'));notice('日常入口已更新。');}}
      ])));
    });actions.append(open);}else actions.append(el('p',{class:'subtle'},'恢复尚未完成。重新选择同一份备份即可继续。'),button('重新选择备份',()=>{view.close();restoreArchive();}));
    body.append(entry);
  }
  if(!items?.length)body.append(el('p',{class:'subtle'},'暂无恢复结果。'));
  if(items?.length>=64)body.append(el('p',{class:'subtle'},'显示最近 64 次恢复。较早的资料仍保存在原处。'));
  view=dialog('恢复结果',body);
}
async function poll(){
  if(!state.active||state.exiting||state.polling)return;
  const run=state.run;
  clearTimeout(state.pollTimer);
  state.polling=true;
  try{
    if(rescueCleanupPending()){retryRescueCleanup();showStorageCleanup();}
    const page=await query({view:'changes',cursor:state.cursor});
    if(!state.active||run!==state.run)return;
    document.getElementById('connection-state').hidden=true;
    document.getElementById('connection-state').textContent='';document.getElementById('health-state').hidden=true;
    if(state.renderFailed){await render();if(!state.active||run!==state.run)return;}
    if(page.changed){
      if(page.reset){
        releaseReader();state.readPositions.clear();state.graphMem.clear();invalidate();state.epoch++;state.after='';
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
          else if(current.assets[0].version!==selected.version){if(state.reader){state.reader.markUpdated();await state.libraryRefresh?.();}else{state.selection=null;notice('内容有变化，请重新打开核对。');await render();}}
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
  }catch(error){if(state.active&&run===state.run&&error.status!==-1){document.getElementById('connection-state').hidden=false;document.getElementById('connection-state').textContent=error.status===429?'稍后自动更新。':'正在恢复更新。未保存的文字会留在本页。';}}
  finally{if(run===state.run){state.polling=false;if(state.active)state.pollTimer=setTimeout(poll,document.hidden?12000:2200);}}
}
