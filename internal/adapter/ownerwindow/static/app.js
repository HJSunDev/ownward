import {query, act, resolve, text, request, logout, operationID, invalidate, scope} from './api.js';
import {Editor, rescuedInput, clearRescue, retainReceipt} from './editor.js';
import {el, button, row, heading, empty, prose, tag, date, notice, clearNotice, statusName, permissionName, dialog, confirm, download, errorMessage} from './ui.js';

const main=document.getElementById('main');
const state={surface:'drafts',epoch:0,cursor:'',editor:null,selection:null,active:true,polling:false,after:'',search:'',filter:'',exiting:false};
const surfaces=[['drafts','文稿','01'],['assets','资料','02'],['relations','关系','03'],['events','动态','04'],['control','掌控','05']];
const eventName={create:'存入资料',created:'存入资料',update:'更新资料',updated:'更新资料',correct:'更正资料',correction:'更正资料',draft_published:'文稿存入资料',forget:'遗忘资料',permissions:'调整接入能力',enrollment:'接入决定',handoff:'迁移决定',access:'接入变更'};
const valid=epoch=>state.active&&epoch===state.epoch;
const section=(title,...content)=>el('section',{class:'section'},el('div',{class:'section-title'},el('h2',{},title)),...content);
const sourceLabel=s=>s?.authored?'我创建的':s?.actor||s?.ref||'未注明来源';

export async function start(health){
  state.cursor=health.cursor;
  document.getElementById('entry').hidden=true;document.getElementById('shell').hidden=false;
  const nav=document.getElementById('navigation');
  for(const [id,label,n] of surfaces)nav.append(button(label,()=>navigate(id),'nav-item',{'data-surface':id,'aria-label':label}),el('span',{class:'nav-number','aria-hidden':'true'},n));
  document.getElementById('pending-entry').onclick=()=>navigate('control').catch(error=>notice(error.message,true));
  document.getElementById('logout').onclick=async()=>{
    try{
      const exit=async()=>{
        if(state.exiting)return;state.exiting=true;
        lock('窗口内的暂存已清除，正在结束会话。',false);
        let message='已退出物主窗口。';
        try{await logout();}catch(error){if(error.status!==401)message='本窗口已退出并清除暂存；服务端会话结束尚未确认。';}
        document.getElementById('status').textContent=message+' 请从本机物主入口重新验证。';
      };
      if(state.editor?.dirty||state.editor?.conflict||state.editor?.publishID){await confirm('退出前保留文字',el('div',{},el('p',{},'还有尚未完成核对的文字。可以先保存一份文本到本机；确认退出会清除窗口内的暂存。'),button('下载当前文字',()=>download(new Blob([state.editor.value],{type:'text/plain;charset=utf-8'}),'未完成的文稿.txt'))),'清除暂存并退出',exit,true);}else await exit();
    }catch(error){notice(errorMessage(error),true);}
  };
  window.addEventListener('owner-auth-lost',()=>lock('验证已失效，请从本机物主入口重新打开。未同步输入将在重新验证后核对。'));
  window.addEventListener('beforeunload',event=>{if(state.editor?.dirty||state.editor?.busy||state.editor?.publishID){state.editor.persist();event.preventDefault();event.returnValue='';}});
  window.addEventListener('online',()=>poll());
  document.addEventListener('visibilitychange',()=>{if(!document.hidden)poll();});
  await render();await refreshPending();
  const rescue=rescuedInput();
  if(rescue?.reference){
    try{await openDraft(rescue.reference,rescue);}catch(error){notice('尚未核对上次输入；请保留此窗口并重试。',true);}
  }
  setTimeout(poll,2200);
}
function lock(message,preserveInput=!state.exiting){
  invalidate();
  state.active=false;state.epoch++;
  if(preserveInput)state.editor?.persist();state.editor?.destroy();state.editor=null;state.selection=null;
  if(!preserveInput)clearRescue();
  document.querySelectorAll('dialog').forEach(d=>d.remove());main.replaceChildren();
  document.getElementById('shell').hidden=true;document.getElementById('entry').hidden=false;
  document.getElementById('status').textContent=message+' 可在本机运行 ownward owner-window 重新验证。';
}
async function navigate(surface){
  if(!state.active)return;
  state.editor?.suspend();
  invalidate();
  state.surface=surface;state.selection=null;state.after='';clearNotice();await render();
}
function navState(){document.querySelectorAll('[data-surface]').forEach(b=>{b.setAttribute('aria-current',b.dataset.surface===state.surface?'page':'false');});}
async function render(){
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
  if(state.editor?.live&&!main.contains(state.editor.node))main.prepend(el('div',{class:'resume-bar'},el('span',{},state.editor.dirty||state.editor.conflict?'有一篇文稿的输入留在此窗口，等待继续核对。':'进行中的文稿已保留。'),button('回到这篇文稿',()=>resumeEditor())));
  else if(!state.editor&&rescuedInput()?.publishID)main.prepend(el('div',{class:'resume-bar'},el('span',{},'上次存入结果仍待核对。'),button('核对上次存入',()=>{const rescue=rescuedInput();return openDraft(rescue.reference,rescue);})));
}
async function resumeEditor(){
  const editor=state.editor;if(!editor?.live)return;
  invalidate();
  state.epoch++;state.surface='drafts';state.selection=null;navState();main.replaceChildren(editor.node);
  await editor.resume();if(editor.live&&!editor.quarantined)editor.input.focus();
}
async function loadPage(input){return query({limit:12,...input});}
function pager(page,run,label='继续查看'){
  return page.next?el('div',{class:'pagination'},el('span',{class:'subtle'},'按页呈现，不是全部数量'),button(label,()=>run(page.next))):null;
}
async function assetCard(asset,epoch,open=openAsset){
  if(asset.state==='stopped')return el('article',{class:'asset-card'},el('h3',{},'已停止使用的资料'),tag('正在清理'),el('p',{class:'subtle'},'正文与来源不再提供读取。'));
  const title=el('span',{class:'asset-title'},'读取原文开头…'),source=el('span',{class:'source'},'来源待核对');
  const node=el('article',{class:'asset-card'},button('',()=>open(asset),'asset-open'),row(tag(statusName(asset.state),asset.state),el('time',{},date(asset.updated_at))),source);
  node.querySelector('button').append(title);
  // Only this visible page is enriched; failures never hide the openable item.
  query({view:'content',handle:asset.handle}).then(page=>{if(valid(epoch))title.textContent=page.text.text.trim().split(/\r?\n/)[0].slice(0,110)||'未命名的资料';}).catch(()=>{if(valid(epoch))title.textContent='打开原文';});
  query({view:'source',handle:asset.handle}).then(page=>{if(valid(epoch))source.textContent=sourceLabel(page.source);}).catch(()=>{if(valid(epoch))source.textContent='打开后核对来源';});
  return node;
}
async function draftsPage(epoch){
  const [drafts,assets,recent]=await Promise.all([loadPage({view:'drafts',after:state.after}),loadPage({view:'continuable',limit:4}),loadPage({view:'recent',limit:4})]);
  const list=el('div',{class:'draft-list'});
  for(const draft of drafts.drafts||[]){
    const title=el('span',{},'读取文稿…');
    list.append(button('',()=>openDraft(draft.reference),'draft-row'));
    list.lastChild.append(el('span',{class:'document-mark','aria-hidden':'true'},'↗'),el('span',{class:'draft-row-body'},title,el('span',{class:'subtle'},draft.target?'正在修订资料':'尚未存入资料')),el('time',{},date(draft.updated_at)));
    query({view:'draft_content',handle:draft.handle}).then(p=>{if(valid(epoch))title.textContent=p.text.text.trim().split(/\r?\n/)[0].slice(0,90)||'空白文稿';}).catch(()=>{if(valid(epoch))title.textContent='继续文稿';});
  }
  const cards=el('div',{class:'asset-grid'});for(const asset of assets.assets||[])cards.append(await assetCard(asset,epoch));
  return el('div',{class:'page'},heading('文字在这里成形','从一篇文稿开始，也可以继续已有的积累。',button('＋ 新建文稿',newDraft,'primary')),
    section('进行中的文稿',list.childElementCount?list:empty('留一页，慢慢写','输入会自动保存。写完后，再将整篇存入资料。',button('开始第一篇',newDraft)),pager(drafts,async after=>{state.after=after;await render();})),
    section('文稿与资料',cards.childElementCount?cards:empty('这里还没有资料','文稿存入后，会和其他资料一同在这里。')),
    section('近期变动',activityList(recent.activity||[],epoch),button('查看全部动态',()=>navigate('events'))));
}
async function newDraft(){const current=scope(),editor=state.editor;if(editor)await editor.flush();if(!current()||state.editor!==editor)return;const result=await act({action:'create_draft',text:''});await openDraft(result.reference);}
async function editAsset(asset){const current=scope(),editor=state.editor;if(editor)await editor.flush();if(!current()||state.editor!==editor)return;const result=await act({action:'create_draft',target:asset.handle});await openDraft(result.reference);}
async function openDraft(reference,rescue){
  const saved=rescue||rescuedInput();rescue=saved?.reference===reference?saved:null;
  if(state.editor?.meta.reference===reference){return resumeEditor();}
  const current=scope(),previous=state.editor;
  if(previous){await previous.flush();if(!current()||state.editor!==previous)return;previous.destroy();state.editor=null;}
  invalidate();
  if(rescue?.publishID){
    const recovered=await query({view:'publish_receipt',operation_id:rescue.publishID});
    if(['completed','changed'].includes(recovered.publication.state)){clearRescue();notice('已核对上次存入成功。');return openAsset({handle:recovered.publication.asset});}
    if(recovered.publication.state==='unavailable'){clearRescue();notice('上次存入的资料已不可用，相关暂存已清除。');return navigate('drafts');}
  }
  const epoch=++state.epoch,page=await resolve(reference);
  if(!valid(epoch))return;
  if(page.unavailable){
    if(rescue?.publishID){
      // A missing draft is also how discard/forget becomes visible. Preserve
      // only the operation locator; never revive its unavailable body.
      retainReceipt(rescue);notice('存入结果尚未确认，原稿已不可用。请核对近期动态，避免重复存入。',true);return render();
    }
    clearRescue();notice('这篇文稿已不可用，相关窗口副本已清除。');await navigate('drafts');return;
  }
  const meta=page.drafts[0],content=await text('draft_content',meta.handle,()=>valid(epoch));
  if(!valid(epoch))return;
  const editor=installEditor(meta,content,rescue);
  if(editor.publishID)await editor.recoverPublication();
}
function installEditor(meta,content,rescue,show=true){
  if(show){state.surface='drafts';state.selection=null;navState();}
  const editor=new Editor(meta,content,{
    leave:()=>navigate('drafts'),open:reference=>openDraft(reference),grant:grantDraft,
    rebased:async(meta,body,rescue)=>{const visible=releaseEditor(editor);if(visible!==null)installEditor(meta,body,rescue,visible);},
    discarded:async()=>{const visible=releaseEditor(editor);if(visible)await navigate('drafts');},
    published:async handle=>{const visible=releaseEditor(editor);if(visible)await openAsset({handle});else if(visible===false)notice('已确认文稿存入成功，可从资料页查看。');},
    pendingReceipt:async()=>{const visible=releaseEditor(editor);if(visible===null)return;if(visible)await navigate('drafts');else showResume();notice('存入结果尚未确认，原稿已不可用。只保留核对线索，请核对近期动态，避免重复存入。',true);},
    unavailable:()=>{const visible=releaseEditor(editor);if(visible===null)return;notice('资料或文稿已不可用，相关窗口副本已清除。');if(visible){document.querySelectorAll('dialog').forEach(d=>d.remove());navigate('drafts');}}
  },rescue);
  state.editor=editor;
  if(show){main.replaceChildren(editor.node);editor.input.focus();}else{editor.suspend();showResume();}
  return editor;
}
function releaseEditor(editor){if(state.editor!==editor)return null;const visible=main.contains(editor.node);state.editor=null;document.querySelectorAll('.resume-bar').forEach(n=>n.remove());return visible;}
async function assetsPage(epoch){
  const input=el('input',{type:'search',placeholder:'按原文用词查找','aria-label':'查找资料',value:state.search});
  const filter=el('select',{'aria-label':'资料状态'},...Object.entries({'':'全部状态',ready:'已整理',pending:'待整理',stopped:'已停止使用'}).map(([value,label])=>el('option',{value,selected:state.filter===value},label)));
  const search=async()=>{state.search=input.value;state.filter=filter.value;state.after='';await render();};
  input.addEventListener('keydown',event=>{if(event.key==='Enter')search();});filter.addEventListener('change',search);
  const page=await loadPage({view:'assets',query:state.search,state:state.filter,after:state.after}),list=el('div',{class:'asset-grid'});
  for(const a of page.assets||[])list.append(await assetCard(a,epoch));
  return el('div',{class:'page'},heading('自己的资料','原文、来源与状态，直接核对。'),el('div',{class:'search-bar'},input,filter,button('查找',search)),
    list.childElementCount?list:empty('这一页没有匹配的资料','试试原文中的其他用词，或调整状态。'),pager(page,async after=>{state.after=after;await render();}),state.after?button('回到第一页',async()=>{state.after='';await render();}):null);
}
async function openAsset(asset){
  state.editor?.suspend();
  invalidate();
  const epoch=++state.epoch;main.replaceChildren(el('p',{class:'loading',role:'status'},'正在打开原文…'));
  const page=await resolve(asset.reference,asset.handle);if(!valid(epoch))return;
  if(page.unavailable){state.selection=null;notice('这份资料已不可用。');return navigate('assets');}
  asset=page.assets[0];
  const [body,sourcePage]=await Promise.all([text('content',asset.handle,()=>valid(epoch)),query({view:'source',handle:asset.handle})]);
  if(!valid(epoch))return;
  state.surface='assets';state.selection=asset;navState();
  const content=prose(body),source=sourcePage.source;
  const switchOriginal=button('查看来源原件',async()=>{
    const original=await text('original',asset.handle,()=>valid(epoch));
    const details=JSON.parse(await text('original_details',asset.handle,()=>valid(epoch)));
    if(valid(epoch))dialog('来源原件 · 留作证据',el('div',{},el('p',{class:'source'},sourceLabel({actor:details.source?.actor,ref:details.source?.ref})),details.source?.ref?prose(details.source.ref,'source-ref'):null,prose(original)));
  });
  main.replaceChildren(el('article',{class:'reading'},row(button('← 全部资料',()=>navigate('assets')),tag(statusName(asset.state),asset.state)),
    el('header',{class:'reading-heading'},el('p',{class:'eyebrow'},'原文 · 当前内容'),el('h1',{},body.trim().split(/\r?\n/)[0].slice(0,110)||'资料'),row(el('span',{class:'source'},sourceLabel(source)),el('time',{},date(asset.updated_at))),source.ref?prose(source.ref,'source-ref'):null),
    row(button('编辑这篇内容',()=>editAsset(asset),'primary'),button('查看关联',()=>showRelations(asset)),asset.has_original?switchOriginal:null,button('遗忘这份资料',()=>forget(asset,body),'danger-quiet')),
    content));showResume();
}
async function forget(asset,body){
  const operation=operationID();
  await confirm('遗忘整份资料',el('div',{},el('p',{},'将停止使用并清理这份资料、来源原件，以及绑定它的文稿。此处遗忘整份内容；只改一部分，请返回编辑。'),prose(body)), '确认遗忘',async()=>{
    const result=await act({action:'forget',handle:asset.handle,operation_id:operation});
    invalidate();state.selection=null;state.epoch++;document.querySelectorAll('dialog').forEach(d=>d.remove());
    if(state.editor?.meta.target_reference===asset.reference){state.editor.destroy();state.editor=null;clearRescue();}
    main.replaceChildren();notice(statusName(result.state));state.surface='control';state.after='';await render();
  },true);
}
function activityList(items,epoch){
  if(!items.length)return empty('还没有动态','发生的变化会如实留在这里。');
  const list=el('ol',{class:'activity-list'});
  for(const item of items){
    const invalid=item.invalidated_relations;
    const reasons=invalid?[invalid.quote_missing?`${invalid.quote_missing} 条关联的引文不再匹配`:'',invalid.quote_ambiguous?`${invalid.quote_ambiguous} 条关联的引文无法唯一定位`:'',invalid.target_unavailable?`${invalid.target_unavailable} 条关联的目标不可用`:''].filter(Boolean).join('；'):'';
    list.append(el('li',{},el('time',{},date(item.at)),el('div',{},el('strong',{},eventName[item.kind]||'资料发生变化'),el('p',{class:'subtle'},item.unavailable?'已遗忘的资料':item.subject?[item.subject,item.distinction].filter(Boolean).join(' · '):statusName(item.state||'completed')),reasons?el('p',{},reasons):null,item.asset?button('查看原文',()=>openAsset({handle:item.asset})):null)));
  }return list;
}
async function eventsPage(epoch){
  const page=await loadPage({view:'recent',after:state.after});
  return el('div',{class:'page'},heading('发生过的变化','最近的在前。保留近期操作事实，不复制已遗忘的正文。'),activityList(page.activity||[],epoch),pager(page,async after=>{state.after=after;await render();},'查看更早'),state.after?button('回到最近',async()=>{state.after='';await render();}):null);
}
async function relationsPage(epoch){
  const page=await loadPage({view:'overview',after:state.after}),o=page.overview,list=el('div',{class:'asset-grid'});
  for(const asset of page.assets||[])list.append(await assetCard(asset,epoch,showRelations));
  return el('div',{class:'page'},heading('从一份资料，走向另一份','选择一个起点，查看关联与依据。这里呈现关系，不需要你维护它。'),
    el('div',{class:'relation-overview'},el('h2',{},'这一组资料的关联概况'),el('p',{},page.organization==='rebuilding'?'关联正在重新整理，当前概况可能不完整。':page.organization==='unavailable'?'关联暂不可用。资料原文与掌控仍可使用。':'这是按页呈现的近似概况，不是全部资料的关系图。'),o?row(tag(`${o.connected} 项已有连接`),tag(`${o.unconnected} 项暂未连上`),tag(`${o.pending} 项待整理`)):null),
    list.childElementCount?list:empty('先有一份资料，再有连接','整理后的关系会在这里出现。'),pager(page,async after=>{state.after=after;await render();}));
}
function readableMeaning(value){
  // Only user-language fields. Identity, offsets, generation and protocol data
  // never leak through a generic JSON renderer.
  if(typeof value==='string')return value;
  if(Array.isArray(value))return value.map(readableMeaning).filter(Boolean).join('\n');
  if(value&&typeof value==='object')return ['description','reason','rationale','explanation','meaning','claim','summary','text','context','condition','label','title'].map(k=>readableMeaning(value[k])).filter(Boolean).join('\n');
  return '';
}
async function showRelations(asset,after=''){
  state.editor?.suspend();invalidate();
  const epoch=++state.epoch,page=after?{assets:[asset]}:await resolve(asset.reference,asset.handle);if(!valid(epoch))return;
  if(page.unavailable){notice('这份资料已不可用。');return navigate('relations');}
  asset=page.assets[0];const relations=await loadPage({view:'relations',handle:asset.handle,after});
  const first=await query({view:'content',handle:asset.handle});if(!valid(epoch))return;
  state.surface='relations';state.selection=asset;navState();
  const list=el('div',{class:'relation-list'});
  for(const relation of relations.relations||[]){
    const reason=el('p',{},relation.evidence||'正在核对关联依据…');
    if(relation.meaning)text('relation_text',relation.meaning,()=>valid(epoch)).then(raw=>{if(valid(epoch))reason.textContent=readableMeaning(JSON.parse(raw))||relation.evidence||'请查看关联原文，核对具体依据。';}).catch(()=>{if(valid(epoch))reason.textContent='关联说明暂时无法读取，仍可核对两端原文。';});
    list.append(el('article',{class:'relation-card'},el('span',{class:'relation-line','aria-hidden':'true'},'↔'),el('div',{},el('h2',{},'资料之间的关联'),reason,row(button('查看一端原文',()=>openAsset({handle:relation.source})),button('查看另一端原文',()=>openAsset({handle:relation.target}))),...(relation.basis||[]).map((basis,i)=>button(`查看依据原文 ${i+1}`,()=>openAsset({handle:basis.asset}))))));
  }
  main.replaceChildren(el('div',{class:'page'},button('← 选择其他起点',()=>navigate('relations')),heading(first.text.text.trim().split(/\r?\n/)[0].slice(0,80)||'这份资料','围绕它，逐条核对关系与来源。'),button('打开当前原文',()=>openAsset(asset)),
    relations.organization!=='available'?el('p',{class:'callout'},relations.organization==='rebuilding'?'关系正在重新整理；以下是当前可用的部分。':'关系暂不可用，原文仍可打开。'):null,
    list.childElementCount?list:empty(asset.state==='pending'?'还在等待整理':'暂未连上其他资料','没有连接不代表没有价值；此处不会补造关系。'),pager(relations,next=>showRelations(asset,next),'继续查看关联')));
}
async function refreshPending(){
  const page=await loadPage({view:'pending',limit:10});if(!state.active)return;
  document.getElementById('pending-count').textContent=page.next?'有待处理':String((page.decisions||[]).length);
  document.getElementById('pending-entry').classList.toggle('has-pending',!!page.decisions?.length);
}
function decisionCard(d,historical=false){
  const node=el('article',{class:'decision-card'},row(el('h3',{},d.kind==='enrollment'?'接入申请':d.kind==='handoff'?'迁移确认':'资料与能力决定'),tag(statusName(d.state))),el('p',{},[d.subject,d.distinction].filter(Boolean).join(' · ')),el('p',{},d.consequence),d.verification?el('p',{class:'verification'},d.verification):null,
    d.permissions?.length?el('p',{class:'subtle'},d.permissions.map(permissionName).join('、')):null,
    ...(d.targets||[]).map((target,i)=>button(`核对所涉资料 ${i+1}`,()=>openAsset({handle:target}))));
  if(!historical&&d.state==='awaiting_approval'||!historical&&d.kind==='enrollment'&&d.state==='pending'||!historical&&d.kind==='handoff'&&d.state==='pending'){
    for(const [accept,label] of [[false,'拒绝'],[true,'批准并接续']])node.append(button(label,async()=>{
      await confirm(label,el('div',{},el('h3',{},[d.subject,d.distinction].filter(Boolean).join(' · ')),el('p',{},(d.permissions||[]).map(permissionName).join('、')),d.verification?el('p',{class:'verification'},d.verification):null,el('p',{},d.consequence),el('p',{},'决定会直接约束原来的任务。内容有变化时，需要重新核对。')),label,async()=>{
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
  for(const person of connections.connections||[])people.append(el('article',{class:'connection-card'},el('div',{},el('h3',{},person.name),el('p',{class:'subtle'},person.distinction),el('p',{},person.owner?'物主本人':person.permissions?.length?person.permissions.map(permissionName).join(' · '):'当前没有资料访问能力')),person.owner?tag('本人'):button('调整能力',()=>permissions(person))));
  const archive=el('div',{class:'archive-grid'},el('div',{},el('h3',{},'留一份可恢复的备份'),el('p',{},'包含资料、文稿与掌控状态。保存在你信任的位置。'),button('下载备份',async()=>{const blob=await request('backup',{}, {blob:true,timeout:180000});download(blob,'ownward-backup.zip');notice('备份已交给浏览器保存。');},'primary')),
    el('div',{},el('h3',{},'从备份恢复'),el('p',{},'恢复到独立位置，原资料库保持原样。完成后重新验证物主。'),button('选择备份',restoreArchive)));
  const older=el('details',{},el('summary',{},'查看近期已决定事项'),...(history.decisions||[]).map(d=>decisionCard(d,true)),pager(history,after=>historyDialog(after),'查看更多已决定事项'));
  return el('div',{class:'page'},heading('你始终掌控','谁能接入、什么可以发生，由你决定。'),
    section('待处理事项',decisions.childElementCount?decisions:empty('暂时没有需要你决定的事','有新事项时，窗口上方会持续显示入口。'),pager(pending,async after=>{state.after=after;await render();}),older),
    section('接入者',people,pager(connections,after=>connectionsDialog(after))),
    section('文稿工作授权',...(grants.grants||[]).map(g=>row(el('span',{},`${g.connection.name} · ${g.connection.distinction} · 有效至 ${date(g.expires_at)}`),button('撤销这次工作授权',async()=>{await act({action:'revoke_grant',handle:g.handle});await render();}))),!grants.grants?.length?el('p',{class:'subtle'},'当前没有生效的文稿工作授权。'):null,pager(grants,after=>grantsDialog(after))),
    section('备份与健康',row(tag(health.health==='normal'?'正常':'需要注意',health.health==='normal'?'ready':'pending'),el('p',{class:'subtle'},health.health==='normal'?'资料与掌控可正常使用。':'有操作正在处理，或资料状态需要留意。请核对待处理事项。')),archive));
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
async function historyDialog(after){return pagedDialog('已决定事项','history',after,p=>(p.decisions||[]).map(d=>decisionCard(d,true)),8);}
async function connectionsDialog(after){return pagedDialog('更多接入者','connections',after,p=>(p.connections||[]).map(c=>row(el('span',{},`${c.name} · ${c.distinction}`),c.owner?tag('本人'):button('调整能力',()=>permissions(c)))));}
async function grantsDialog(after){return pagedDialog('更多工作授权','draft_grants',after,p=>(p.grants||[]).map(g=>row(el('span',{},`${g.connection.name} · ${date(g.expires_at)}`),button('撤销',async()=>{await act({action:'revoke_grant',handle:g.handle});await render();}))));}
async function permissions(person){
  const checks=['read','maintain','manage'].map(value=>el('label',{class:'check-row'},el('input',{type:'checkbox',value,checked:(person.permissions||[]).includes(value)}),permissionName(value)));
  const panel=el('div',{},el('p',{},`${person.name} · ${person.distinction}`),el('p',{},'未勾选的能力将撤销。全部取消，即撤销这个接入者的全部能力。'),...checks);
  const op=operationID();dialog('调整接入能力',panel,[{label:'取消',run:close=>close()},{label:'确认调整',style:'primary',run:async close=>{
    const selected=checks.map(c=>c.querySelector('input')).filter(c=>c.checked).map(c=>c.value);
    const result=await act({action:'permissions',handle:person.handle,permissions:selected,operation_id:op});if(!close.current())return;close();state.after='';notice(statusName(result.state));await render();
  }}]);
}
async function grantDraft(editor,after=''){
  const current=scope();await editor.flush();if(!current()||!editor.live)return;const p=await loadPage({view:'connections',after}),list=el('div',{},el('p',{},'选一个已接入者，允许它在接下来一小时内读写这篇文稿。存入或弃稿后立即失效；它不能代你发布。'));
  for(const person of p.connections||[]){if(person.owner)continue;list.append(button(`${person.name} · ${person.distinction}`,async()=>{
    const result=await act({action:'grant_draft',handle:editor.meta.handle,target:person.handle,seconds:3600});
    // Work-item handles are protocol inputs for the external agent, not owner credentials.
    const instruction=`请继续这篇 Ownward 文稿。使用 ownward_draft_work 先读取当前内容，再按当前版本提交。draft=${result.handle}\ngrant=${result.grant}`;
    dialog('工作项已准备好',el('div',{},el('p',{},'将工作项交给刚才选择的接入者，在原来的对话里继续。它的修改会出现在这里。'),button('复制给接入者',async()=>{await navigator.clipboard.writeText(instruction);notice('已复制工作项，可粘贴到原来的对话。');})));
  }));}
  if(!(p.connections||[]).some(c=>!c.owner))list.append(el('p',{},'这一页没有其他接入者。可先让智能体接入，再到这里授权。'));
  list.append(pager(p,next=>grantDraft(editor,next))||'');dialog('交给接入者续写',list);
}
async function restoreArchive(){
  const file=el('input',{type:'file','aria-label':'选择 Ownward 备份'});
  dialog('恢复到独立位置',el('div',{},el('p',{},'不会覆盖当前资料。选择完整备份；恢复完成后还需在本机重新验证物主。'),file),[{label:'开始恢复',style:'primary',run:async close=>{
    if(!file.files[0])throw new Error('请先选择备份文件。');
    const result=await request('restore',file.files[0],{raw:true,type:'application/octet-stream',timeout:180000});if(!close.current())return;close();
    const quoted="'"+result.data_dir.replaceAll("'",navigator.platform.startsWith('Win')?"''":"'\\''")+"'";
    dialog('备份已恢复，等待本机验证',el('div',{},el('p',{},'恢复后的资料尚未接入使用。请在本机验证物主后打开新窗口；当前资料库没有被覆盖。'),button('复制本机打开命令',async()=>{await navigator.clipboard.writeText(`ownward owner-window --data-dir ${quoted}`);notice('已复制本机打开命令。');})));
  }}]);
}
async function poll(){
  if(!state.active||state.polling)return;
  clearTimeout(state.pollTimer);
  state.polling=true;
  try{
    const page=await query({view:'changes',cursor:state.cursor});
    if(!state.active)return;
    document.getElementById('connection-state').textContent='本机 · 物主已验证';
    if(page.changed){
      if(page.reset){
        invalidate();state.epoch++;state.after='';
        document.querySelectorAll('dialog').forEach(d=>d.remove());
        const visibleEditor=state.editor&&main.contains(state.editor.node);
        state.editor?.quarantine();main.replaceChildren(el('p',{class:'loading'},'正在核对资料是否仍可使用…'));
        if(state.editor){await state.editor.reconcile(true);}
        if(visibleEditor&&state.editor?.live){main.replaceChildren(state.editor.node);}
        else{state.selection=null;await render();}
        const rescue=rescuedInput();if(rescue?.reference){const current=await resolve(rescue.reference);if(current.unavailable){if(rescue.publishID)retainReceipt(rescue);else clearRescue();}}
      }else{
        // The retained draft and the visible reading/list surface are separate
        // obligations. Neither may consume the other's change checkpoint.
        if(state.editor)await state.editor.reconcile();
        if(!state.active)return;
        if(state.selection){const current=await resolve(state.selection.reference);if(current.unavailable){state.selection=null;await render();}else if(current.assets[0].version!==state.selection.version){state.selection=null;notice('内容有变化，请重新打开核对。');await render();}}
        else if(!state.editor||!main.contains(state.editor.node)){
          if(main.contains(document.activeElement)&&['INPUT','SELECT'].includes(document.activeElement.tagName)){await refreshPending();return;}
          state.after='';await render();
        }
      }
      await refreshPending();
      state.cursor=page.cursor;
    }
  }catch(error){if(state.active&&error.status!==-1){document.getElementById('connection-state').textContent=error.status===429?'正在同步…':'连接暂时中断';}}
  finally{state.polling=false;if(state.active)state.pollTimer=setTimeout(poll,document.hidden?12000:2200);}
}
