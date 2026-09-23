import nodeTest from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';
const test=(name,run)=>nodeTest(name,{timeout:5000},run);

// Exercise App + Editor together: restoring, switching and cleanup share the
// real rescue slot. Transport substitutes enforce the API's page generation.
class Node {
  constructor(tag='div',attrs={},children=[]){Object.assign(this,{tagName:tag.toUpperCase(),value:'',children,hidden:false,isConnected:true,handlers:{},dataset:{},classList:{toggle(){}}},attrs);}
  append(...items){this.children.push(...items);}
  prepend(...items){this.children.unshift(...items);}
  replaceChildren(...items){this.children=items;}
  addEventListener(name,run){this.handlers[name]=run;}
  setAttribute(){}
  focus(){}
  contains(node){return this===node||this.children.some(c=>c instanceof Node&&c.contains(node));}
  remove(){this.removed=true;}
  all(){return [this,...this.children.filter(c=>c instanceof Node&&!c.removed).flatMap(c=>c.all())];}
  get childElementCount(){return this.children.filter(c=>c instanceof Node).length;}
  get lastChild(){return this.children.at(-1);}
}
const rescueKey='ownward.owner-input';
function deferred(){let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};}
async function harness({failFirst=false,rescue:initialRescue,overrides={}}={}){
  const saved=new Map(),calls=[],notices=[],nodes=new Map(),confirmations=[],handlers={},listeners={},downloads=[],timers=new Map();let generation=0,fail=failFirst,timerID=0;
  const listen=(name,run)=>{(listeners[name]??=[]).push(run);handlers[name]=(...args)=>Promise.all(listeners[name].map(f=>f(...args)));};
  const drafts=new Map([['A',{reference:'A',handle:'hA',version:'v1',text:'A already saved'}]]);
  const rescue=initialRescue||{reference:'A',version:'v1',text:'A unique unsynced input'};
  saved.set(rescueKey,JSON.stringify(rescue));
  const node=id=>{if(!nodes.has(id))nodes.set(id,new Node());return nodes.get(id);};
  const document={getElementById:node,querySelector:()=>document.modalNotice||null,querySelectorAll:s=>s.startsWith('.')?[...node('main').all(),...node('entry').all()].filter(n=>(n.class||'').split(' ').includes(s.slice(1))):[],addEventListener:(name,run)=>listen('document:'+name,run),hidden:false,activeElement:new Node()};
  const api={
    storage:{get:k=>saved.get(k)||null,set:(k,v)=>{saved.set(k,v);return true;},remove:k=>{saved.delete(k);return true;}},
    scope:()=>{const at=generation;return()=>at===generation;},invalidate:()=>generation++,pause:async()=>{},operationID:()=> 'op',
    query:async input=>{calls.push(['query',input]);return {drafts:[],assets:[],activity:[],decisions:[],cursor:'cursor'};},
    resolve:async reference=>{calls.push(['resolve',reference]);if(reference==='A'&&fail){fail=false;throw Object.assign(new Error('temporary failure'),{status:503});}const d=drafts.get(reference);return d?{drafts:[{...d}]}:{unavailable:true};},
    text:async(_,handle)=>[...drafts.values()].find(d=>d.handle===handle)?.text,
    act:async input=>{calls.push(['act',input]);assert.equal(input.action,'create_draft');drafts.set('B',{reference:'B',handle:'hB',version:'v1',text:''});return {reference:'B',handle:'hB'};},
    replace:async(handle,value)=>{const d=[...drafts.values()].find(d=>d.handle===handle);d.text=value;d.version='v2';return {handle:d.handle};},
    request:async()=>{},logout:async()=>{},initialize:async()=>{location.hash='';api.invalidate();return {cursor:'initial'};},...overrides
  };
  for(const name of ['query','resolve','text','act','replace']){const run=api[name];api[name]=async(...args)=>{const current=api.scope();const result=await run(...args);if(!current())throw Object.assign(new Error('old page'),{status:-1});return result;};}
  const ui={
    el:(tag,attrs,...children)=>new Node(tag,attrs,children),button:(label,run)=>new Node('button',{label,run}),
    row:(...children)=>new Node('div',{},children),heading:(...children)=>new Node('header',{},children),
    empty:(...children)=>new Node('div',{},children),prose:value=>new Node('div',{textContent:value}),tag:value=>new Node('span',{textContent:value}),
    date:()=>'',notice:(message)=>notices.push(message),clearNotice(){},statusName:v=>v,permissionName:v=>v,
    dialog(){},confirm:async(title,body,label,run)=>confirmations.push({title,body,label,run}),download:(blob,name)=>downloads.push({blob,name}),errorMessage:e=>e.message
  };
  const location={hash:'',reload(){let blocked=false;handlers.beforeunload?.({preventDefault(){blocked=true;}});calls.push(['reload',blocked]);}};
  const context=vm.createContext({console,Date,JSON,Blob,Event,location,document,window:{addEventListener:listen,dispatchEvent:event=>handlers[event.type]?.(event)},navigator:{},setTimeout:(fn,ms)=>{timers.set(++timerID,{fn,ms});return timerID;},clearTimeout:id=>timers.delete(id)});
  const synthetic=values=>new vm.SyntheticModule(Object.keys(values),function(){for(const[k,v]of Object.entries(values))this.setExport(k,values===api&&typeof v==='function'?(...args)=>values[k](...args):v);},{context});
  const base=new URL('./static/',import.meta.url);
  const actualUI=new vm.SourceTextModule(await readFile(new URL('ui.js',base),'utf8'),{context});await actualUI.link(()=>{});await actualUI.evaluate();
  ui.notice=(message,...args)=>{notices.push(message);return actualUI.namespace.notice(message,...args);};ui.clearNotice=actualUI.namespace.clearNotice;
  const apiModule=synthetic(api),uiModule=synthetic(ui);
  const editor=new vm.SourceTextModule(await readFile(new URL('editor.js',base),'utf8'),{context});
  await editor.link(spec=>spec.includes('api.js')?apiModule:uiModule);await editor.evaluate();
  const app=new vm.SourceTextModule(await readFile(new URL('app.js',base),'utf8')+'\nexport {state,newDraft,openDraft,editAsset,navigate,manageRescue,forget,poll,showRelations,openAsset};',{context});
  await app.link(spec=>spec.includes('api.js')?apiModule:spec.includes('editor.js')?editor:uiModule);await app.evaluate();
  const bootstrap=async()=>{const module=new vm.SourceTextModule(await readFile(new URL('bootstrap.js',base),'utf8'),{context});await module.link(spec=>spec.includes('api.js')?apiModule:app);await module.evaluate();};
  return {app:app.namespace,editor:editor.namespace,saved,drafts,rescue,calls,notices,node,api,ui,document,confirmations,handlers,listeners,timers,downloads,location,bootstrap};
}

test('selected projections follow derived changes, retry failures and never steal navigation',async()=>{
  for(const surface of ['relations','assets']){
    let asset={reference:'asset',handle:'asset-h',version:'body-v1',state:'pending'},organization='rebuilding',fail=false,slow=false;
    const entered=deferred(),gate=deferred(),queries=[];
    const h=await harness({rescue:{reference:'A',version:'v1',text:'A already saved'}});
    const resolve=h.api.resolve,query=h.api.query;
    h.api.resolve=async ref=>ref==='asset'?{assets:[{...asset}]}:resolve(ref);
    h.api.text=async()=> 'Selected body';
    h.api.query=async q=>{queries.push(q);if(q.view==='relations'){if(slow){entered.resolve();await gate.promise;}if(fail)throw new Error('offline');return {organization,relations:[]};}if(q.view==='content')return {text:{text:'Selected body'}};if(q.view==='source')return {source:{authored:true}};if(q.view==='changes')return {changed:true,cursor:'derived-next'};return query(q);};
    await h.app.start({cursor:'initial'});await h.app[surface==='relations'?'showRelations':'openAsset'](asset);
    const currentScope=h.api.scope();asset={...asset,state:'ready'};organization='available';
    if(surface==='relations'){
      fail=true;await h.app.poll();assert.equal(h.app.state.cursor,'initial');assert.equal(h.app.state.selection.state,'pending');fail=false;
    }
    await h.app.poll();assert.equal(h.app.state.selection.state,'ready');assert.equal(h.app.state.cursor,'derived-next');assert.equal(currentScope(),true);
    const values=h.node('main').all().flatMap(n=>[n.textContent,...n.children.filter(c=>typeof c==='string')]);
    assert.ok(values.includes(surface==='relations'?'暂未连上其他资料':'ready'));
    assert.equal(values.some(v=>typeof v==='string'&&v.includes('正在重新整理')),false);
    assert.equal(h.app.state.editor.meta.reference,'A');
    if(surface==='relations'){
      slow=true;const polling=h.app.poll();await entered.promise;await h.app.navigate('events');gate.resolve();await polling;
      assert.equal(h.app.state.surface,'events');assert.equal(h.app.state.selection,null);assert.ok(h.node('main').all().some(n=>n.children.includes('发生过的变化')));
      assert.ok(queries.filter(q=>q.view==='relations').every(q=>!q.after));
    }
  }
});

test('unverified saved input survives authentication recovery including memory-only storage',async()=>{
  for(const memoryOnly of [false,true]){
    const h=await harness({rescue:{reference:'A',version:'v1',text:'A already saved'}});await h.app.start({cursor:'c1'});
    const e=h.app.state.editor,read=h.api.text;let offline=true,writes=0;
    if(memoryOnly)h.api.storage.set=()=>false;
    h.api.replace=async()=>{writes++;Object.assign(h.drafts.get('A'),{text:'other writer',version:'v3'});return {handle:'hA'};};
    h.api.text=async(...args)=>{if(offline)throw new Error('read offline');return read(...args);};
    e.input.value='my words';e.changed();await assert.rejects(e.save(),/read offline/);
    await h.handlers['owner-auth-lost']();assert.equal(h.editor.rescuedInput().text,'my words');assert.equal(h.editor.rescuedInput().refreshPending,true);
    offline=false;await h.app.start({cursor:'c2'});const recovered=h.app.state.editor;
    assert.equal(recovered.value,'my words');assert.equal(recovered.conflict.content,'other writer');assert.equal(writes,1);
    await h.app.navigate('events');await h.app.openDraft('A');assert.equal(recovered.value,'my words');assert.equal(writes,1);
  }
});

test('reentry with pending publication and discard restores the discard action despite receipt failure',async()=>{
  let deletes=0,receipts=0;
  const h=await harness({rescue:{reference:'A',version:'v1',text:'A already saved',publishID:'op',discardPending:{version:'v1',handle:'old'}},overrides:{query:async q=>{if(q.view==='publish_receipt'){receipts++;throw new Error('receipt offline');}return {};},act:async()=>{deletes++;throw new Error('delete offline');}}});
  await h.app.start({cursor:'c1'});const e=h.app.state.editor;
  assert.ok(e);assert.equal(e.retryButton.textContent,'重试弃稿');assert.equal(e.input.readOnly,true);assert.equal(receipts,0);assert.equal(deletes,0);
  await h.app.navigate('assets');await h.app.openDraft('A');assert.equal(e.retryButton.textContent,'重试弃稿');
  await assert.rejects(e.retryButton.run(),/delete offline/);assert.equal(deletes,1);assert.equal(receipts,0);
  await h.handlers['owner-auth-lost']();await h.app.start({cursor:'c2'});assert.equal(h.app.state.editor.retryButton.textContent,'重试弃稿');assert.equal(deletes,1);
});

test('unmounted unavailable rescue loses its body before receipt failure and remains explicitly releasable',async()=>{
  const h=await harness({rescue:{reference:'A',version:'v1',text:'private unavailable text',publishID:'op',discardPending:{version:'v1',handle:'old'}},overrides:{query:async q=>{if(q.view==='publish_receipt')throw new Error('receipt offline');return {};}}});
  h.drafts.delete('A');await h.app.start({cursor:'c1'});
  assert.equal(h.app.state.editor,null);assert.deepEqual(JSON.parse(h.saved.get(rescueKey)),{reference:'A',publishID:'op'});
  assert.equal(h.node('main').all().some(n=>n.textContent==='private unavailable text'),false);
  await h.app.manageRescue();await h.confirmations.at(-1).run();await h.app.newDraft();assert.equal(h.app.state.editor.meta.reference,'B');
});

test('ending receipt recovery withdraws only its own notice before another draft starts',async()=>{
  for(const superseded of [false,true]){
    const h=await harness({rescue:{reference:'A',publishID:'op'},overrides:{query:async q=>q.view==='publish_receipt'?{publication:{state:'unknown'}}:{}}});
    h.drafts.delete('A');await h.app.start({cursor:'c1'});const target=h.node('notice');assert.match(target.textContent,/只保留核对线索/);
    await h.app.openDraft('A');assert.equal(target.hidden,false);
    if(superseded)h.ui.notice('另一项操作尚未完成',true);
    await h.app.manageRescue();h.document.modalNotice=new Node();await h.confirmations.at(-1).run();h.document.modalNotice=null;
    assert.equal(target.hidden,!superseded);if(superseded)assert.equal(target.textContent,'另一项操作尚未完成');
    await h.app.newDraft();assert.equal(h.app.state.editor.meta.reference,'B');assert.equal(h.app.state.receiptNotice,null);
  }
});

test('receipt notices follow reauthentication and authoritative terminal outcomes',async()=>{
  for(const terminal of ['completed','changed','unavailable']){
  let outcome='unknown';
  const h=await harness({rescue:{reference:'A',publishID:'op'},overrides:{query:async q=>q.view==='publish_receipt'?{publication:{state:outcome,asset:'winner'}}:q.view==='source'?{source:{}}:{}}});
  h.drafts.delete('A');await h.app.start({cursor:'c1'});assert.equal(h.node('notice').hidden,false);
  await h.handlers['owner-auth-lost']();assert.equal(h.node('notice').hidden,true);
  await h.app.start({cursor:'c2'});assert.equal(h.node('notice').hidden,false);assert.match(h.node('notice').textContent,/只保留核对线索/);
  outcome=terminal;h.api.resolve=async reference=>reference==='A'?{unavailable:true}:{assets:[{handle:'winner',reference:'asset',version:'v1',state:'pending'}]};
  h.api.text=async()=> 'published body';
  await h.app.openDraft('A');assert.equal(h.editor.rescuedInput(),null);assert.equal(h.app.state.receiptNotice,null);
  assert.doesNotMatch(h.node('notice').hidden?'':h.node('notice').textContent,/只保留核对线索/);
  }
});

test('failed restoration protects input across every other-draft entry and keeps recovery visible on all five surfaces',async()=>{
  const h=await harness({failFirst:true});await h.app.start({cursor:'c1'});
  for(const [entry,arg] of [['newDraft'],['editAsset',{handle:'asset'}],['openDraft','B'],['openDraft','removed']]){
    await assert.rejects(h.app[entry](arg),/恢复或处理/);
    assert.equal(h.app.state.editor,null);assert.equal(JSON.parse(h.saved.get(rescueKey)).text,h.rescue.text);
  }
  assert.equal(h.calls.filter(c=>c[0]==='act').length,0);
  for(const surface of ['drafts','assets','relations','events','control']){
    await h.app.navigate(surface);
    assert.ok(h.node('main').all().some(n=>n.label==='恢复上次文稿'));
    assert.equal(JSON.parse(h.saved.get(rescueKey)).text,h.rescue.text);
  }
  await h.app.openDraft('A');assert.equal(h.app.state.editor.value,h.rescue.text);
  await h.app.newDraft();assert.equal(h.drafts.get('A').text,h.rescue.text);assert.equal(h.app.state.editor.meta.reference,'B');
});

test('restoration already durable can release the workspace; a conflict cannot',async()=>{
  const h=await harness({rescue:{reference:'A',version:'v1',text:'A already saved'}});
  await h.app.start({cursor:'c1'});await h.app.newDraft();assert.equal(h.app.state.editor.meta.reference,'B');
  const conflict=await harness({rescue:{reference:'A',version:'old',text:'my unresolved words'}});
  await conflict.app.start({cursor:'c1'});await assert.rejects(conflict.app.newDraft(),/核对/);
  assert.equal(conflict.app.state.editor.value,'my unresolved words');assert.equal(conflict.app.state.editor.conflict.content,'A already saved');
});

test('flush cannot release pending publication or a partially completed rebase',async()=>{
  for(const field of ['publishID','rebase']){
    const h=await harness();await h.app.start({cursor:'c1'});await h.app.state.editor.save();
    const e=h.app.state.editor;e[field]=field==='publishID'?'pending-operation':{reference:'successor'};e.retryPublishAllowed=true;e.persist();
    await assert.rejects(h.app.newDraft(),/恢复或处理/);assert.equal(h.calls.filter(c=>c[0]==='act').length,0);
    assert.equal(JSON.parse(h.saved.get(rescueKey)).reference,'A');
    await h.app.navigate('drafts');assert.ok(h.node('main').all().some(n=>n.label==='处理这份暂存'));
    await h.app.manageRescue();await h.confirmations.at(-1).run();
    assert.equal(e.live,false);assert.equal(h.drafts.get('A').text,h.rescue.text);
    await h.app.newDraft();assert.equal(h.app.state.editor.meta.reference,'B');
  }
});

test('mounted recovery handling protects active operations and uses memory text when storage fails',async()=>{
  const h=await harness();await h.app.start({cursor:'c1'});const e=h.app.state.editor;
  for(const flag of ['busy','finishing','composing']){e[flag]=true;await assert.rejects(h.app.manageRescue(),/仍在进行/);e[flag]=false;}
  h.api.storage.set=()=>false;e.input.value='newest memory-only input';e.changed();
  await h.app.manageRescue();const confirmation=h.confirmations.at(-1);
  await confirmation.body.all().find(n=>n.label==='下载暂存文字').run();assert.equal(await h.downloads[0].blob.text(),'newest memory-only input');
  e.rebase={reference:'new-successor'};await assert.rejects(confirmation.run(),/已有变化/);assert.equal(e.live,true);
});

test('memory-only pending operations still own the workspace, recovery entry and exit confirmation',async()=>{
  for(const field of ['publishID','rebase']){
    const h=await harness();await h.app.start({cursor:'c1'});await h.app.state.editor.save();
    const e=h.app.state.editor;h.api.storage.set=()=>false;e[field]=field==='publishID'?'memory-only':{reference:'successor'};e.retryPublishAllowed=true;e.persist();
    assert.equal(h.saved.has(rescueKey),false);
    for(const [entry,arg] of [['newDraft'],['editAsset',{handle:'asset'}],['openDraft','B']])await assert.rejects(h.app[entry](arg),/恢复或处理/);
    assert.equal(e.live,true);assert.equal(h.calls.filter(c=>c[0]==='act').length,0);
    await h.app.navigate('drafts');assert.ok(h.node('main').all().some(n=>n.label==='处理这份暂存'));
    let prompted=false;h.handlers.beforeunload({preventDefault(){prompted=true;}});assert.equal(prompted,true);
    await h.node('logout').onclick();assert.equal(h.confirmations.at(-1).label,'清除暂存并退出');
    await h.app.manageRescue();await h.confirmations.at(-1).run();assert.equal(e.live,false);
  }
});

test('unmounted rescue participates in unload and explicit logout, including an expired session',async()=>{
  let h;h=await harness({failFirst:true,overrides:{logout:async()=>{h.handlers['owner-auth-lost']();throw Object.assign(new Error('expired'),{status:401});}}});
  await h.app.start({cursor:'c1'});let prompted=false;h.handlers.beforeunload({preventDefault(){prompted=true;}});assert.equal(prompted,true);
  await h.node('logout').onclick();assert.equal(h.saved.has(rescueKey),true);
  const confirmation=h.confirmations.at(-1);assert.equal(confirmation.label,'清除暂存并退出');
  await confirmation.body.all().find(n=>n.label==='下载当前文字').run();assert.equal(await h.downloads[0].blob.text(),h.rescue.text);
  await confirmation.run();assert.equal(h.saved.has(rescueKey),false);assert.equal(h.app.state.active,false);
});

test('locked reentry revalidates in place without duplicate handlers, navigation or polling',async()=>{
  const h=await harness();await h.bootstrap();
  const beforeUnload=()=>{let blocked=false;h.handlers.beforeunload({preventDefault(){blocked=true;}});return blocked;};
  for(let i=0;i<3;i++){
    await h.handlers['owner-auth-lost']();assert.equal(beforeUnload(),true);
    h.location.hash='#main';await h.handlers.hashchange();assert.equal(h.app.state.active,false);
    h.location.hash='#'+'b'.repeat(64);await h.handlers.hashchange();
    assert.equal(h.app.state.active,true);assert.equal(h.app.state.editor.value,h.rescue.text);
    assert.equal(h.calls.filter(c=>c[0]==='reload').length,0);assert.equal(beforeUnload(),true);
    assert.equal(h.node('navigation').children.length,10);
    for(const list of Object.values(h.listeners))assert.equal(list.length,1);
    assert.equal([...h.timers.values()].filter(t=>t.fn.name==='poll').length,1);
  }
});

test('explicitly handling rescue preserves the durable draft and permits new work',async()=>{
  const h=await harness({failFirst:true});await h.app.start({cursor:'c1'});await h.app.manageRescue();
  const confirmation=h.confirmations.at(-1);assert.equal(h.saved.has(rescueKey),true);
  await confirmation.body.all().find(n=>n.label==='下载暂存文字').run();assert.equal(await h.downloads[0].blob.text(),h.rescue.text);
  await confirmation.run();assert.equal(h.saved.has(rescueKey),false);assert.equal(h.drafts.get('A').text,'A already saved');
  await h.app.newDraft();assert.equal(h.app.state.editor.meta.reference,'B');
});

test('a clear confirmation cannot clear changed text or a newly mounted editor',async()=>{
  for(const change of ['text','editor']){
    const h=await harness({failFirst:change==='editor'});await h.app.start({cursor:'c1'});await h.app.manageRescue();const old=h.confirmations.at(-1);
    if(change==='text'){h.app.state.editor.input.value='later rescue';h.app.state.editor.changed();}else await h.app.openDraft('A');
    await assert.rejects(old.run(),/已有变化/);assert.equal(h.saved.has(rescueKey),true);
  }
});

test('clearing while recovery is in flight invalidates its late result before a new draft opens',async()=>{
  const gate=deferred(),entered=deferred();let delay=false,h;
  h=await harness({failFirst:true});await h.app.start({cursor:'c1'});
  const original=h.api.resolve;h.api.resolve=async reference=>{if(delay&&reference==='A'){entered.resolve();await gate.promise;}return original(reference);};
  delay=true;const opening=h.app.openDraft('A');const outcome=opening.catch(e=>e);await entered.promise;
  await h.app.manageRescue();await h.confirmations.at(-1).run();await h.app.newDraft();
  gate.resolve();await outcome;assert.equal(h.app.state.editor.meta.reference,'B');assert.equal(h.saved.has(rescueKey),false);
});

test('a pending-queue read failure does not prevent restoring input',async()=>{
  const h=await harness({overrides:{query:async input=>{if(input.view==='pending')throw new Error('offline');return {};}}});
  await h.app.start({cursor:'c1'});assert.equal(h.app.state.editor.value,h.rescue.text);
});

test('forget cleans an unmounted rescue for that asset only',async()=>{
  for(const target of ['same','other']){
    const h=await harness({failFirst:true,rescue:{reference:'A',version:'v1',text:'private words',target_reference:'same'},overrides:{act:async()=>({state:'completed'})}});
    await h.app.start({cursor:'c1'});await h.app.forget({reference:target,handle:'asset'},'asset body');await h.confirmations.at(-1).run();
    assert.equal(h.saved.has(rescueKey),target!=='same');
  }
});

test('automatic editor cleanup and receipt retention cannot mutate another drafts rescue',async()=>{
  const h=await harness();
  const other=new h.editor.Editor({reference:'B',handle:'B',version:'v1'},'saved',{});
  other.persist();assert.equal(JSON.parse(h.saved.get(rescueKey)).reference,'A');
  other.input.value='B local';other.changed();assert.equal(JSON.parse(h.saved.get(rescueKey)).text,h.rescue.text);
  h.editor.clearRescue('B');h.editor.retainReceipt({reference:'B',publishID:'unknown'});
  assert.equal(JSON.parse(h.saved.get(rescueKey)).text,h.rescue.text);other.destroy();
});

test('unknown publication remains honest and can be explicitly released without publishing or deleting',async()=>{
  const h=await harness({rescue:{reference:'A',publishID:'unknown'},overrides:{query:async()=>({publication:{state:'unknown'}}),resolve:async()=>({unavailable:true})}});
  await h.app.start({cursor:'c1'});await assert.rejects(h.app.newDraft(),/恢复或处理/);
  await h.app.manageRescue();const confirmation=h.confirmations.at(-1);assert.equal(confirmation.label,'结束本窗口核对');
  await confirmation.run();assert.equal(h.saved.has(rescueKey),false);assert.equal(h.calls.filter(c=>c[0]==='act').length,0);
});

test('unavailable text is removed when storing a receipt fails, including reset and editor recovery',async()=>{
  for(const path of ['open','reset','editor']){
    const h=await harness({failFirst:true,rescue:{reference:'A',version:'v1',text:'private unavailable text',publishID:'unknown'},overrides:{query:async input=>input.view==='publish_receipt'?{publication:{state:'unknown'}}:{}}});
    if(path==='editor'){h.editor.clearRescue();await h.app.openDraft('A').catch(()=>{});await h.app.openDraft('A');h.app.state.editor.publishID='unknown';h.app.state.editor.persist();}
    else await h.app.start({cursor:'c1'});
    h.api.storage.set=()=>false;h.drafts.delete('A');
    if(path==='reset'){h.api.query=async()=>({changed:true,reset:true,cursor:'reset'});await h.app.poll();}
    else if(path==='editor')await h.app.state.editor.reconcile();else await h.app.openDraft('A');
    assert.equal(h.saved.has(rescueKey),false);assert.equal(h.app.state.editor,null);
    assert.deepEqual(JSON.parse(JSON.stringify(h.editor.rescuedInput())),{reference:'A',publishID:'unknown'});
    assert.ok(h.notices.some(n=>n.includes('核对线索仅留在此窗口')));
  }
});

test('reset verifies rescue before redisplaying it and cannot return a detached editor to a new page',async()=>{
  const h=await harness({failFirst:true});await h.app.start({cursor:'c1'});h.drafts.delete('A');
  h.api.query=async()=>({changed:true,reset:true,cursor:'new'});await h.app.poll();
  assert.equal(h.saved.has(rescueKey),false);assert.equal(h.node('main').all().some(n=>n.label==='恢复上次文稿'),false);
  const other=await harness();await other.app.start({cursor:'c1'});
  const entered=deferred(),gate=deferred();other.api.query=async()=>({changed:true,reset:true,cursor:'new'});
  other.app.state.editor.reconcile=async()=>{entered.resolve();await gate.promise;};
  const polling=other.app.poll();await entered.promise;await other.app.navigate('events');gate.resolve();await polling;
  assert.equal(other.app.state.surface,'events');assert.equal(other.node('main').contains(other.app.state.editor.node),false);
});

test('memory-only input survives repeated authentication loss and failed then successful in-place reentry',async()=>{
  const h=await harness();await h.bootstrap();const old=h.app.state.editor;await old.save();
  const store=h.api.storage.set;h.api.storage.set=()=>false;old.input.value='latest memory-only text';old.changed();
  assert.match(old.status.textContent,/请勿刷新或关闭/);assert.equal(h.saved.has(rescueKey),false);
  await h.handlers['owner-auth-lost']();await h.handlers['owner-auth-lost']();
  assert.equal(old.value,'');assert.equal(h.app.state.editor,null);assert.equal(h.node('shell').hidden,true);
  assert.equal(h.editor.rescuedInput().text,'latest memory-only text');
  const initialize=h.api.initialize;h.api.initialize=async()=>{h.location.hash='';throw new Error('try again');};
  h.location.hash='#'+'a'.repeat(64);await h.handlers.hashchange();assert.equal(h.app.state.active,false);
  assert.equal(h.editor.rescuedInput().text,'latest memory-only text');
  h.api.initialize=initialize;h.location.hash='#'+'b'.repeat(64);await h.handlers.hashchange();
  assert.equal(h.app.state.editor.value,'latest memory-only text');assert.equal(h.calls.filter(c=>c[0]==='reload').length,0);
  let warned=false;h.handlers.beforeunload({preventDefault(){warned=true;}});assert.equal(warned,true);
  h.api.storage.set=store;await h.app.state.editor.save();assert.equal(h.drafts.get('A').text,'latest memory-only text');
  assert.equal(h.editor.rescuedInput(),null);assert.equal(h.editor.rescueNeedsWindow(),false);
});

test('unmounted rescue stays available when browser storage later becomes unreadable',async()=>{
  const h=await harness({failFirst:true});await h.app.start({cursor:'c1'});
  h.api.storage.get=()=>null;h.api.storage.set=()=>false;await h.handlers['owner-auth-lost']();
  await h.app.start({cursor:'c1'});assert.equal(h.app.state.editor.value,h.rescue.text);
});

test('storage warning survives conflict and other status changes until protection succeeds',async()=>{
  const h=await harness();await h.app.start({cursor:'c1'});const e=h.app.state.editor;
  const store=h.api.storage.set;h.api.storage.set=()=>false;e.input.value='unprotected';e.changed();
  e.showConflict({...e.meta,version:'other'},'remote text');assert.match(e.status.textContent,/请勿刷新或关闭/);
  e.finishConflict();assert.match(e.status.textContent,/请勿刷新或关闭/);
  e.setStatus('操作未完成');assert.match(e.status.textContent,/请勿刷新或关闭/);
  h.api.storage.set=store;e.persist();e.setStatus('等待保存');assert.doesNotMatch(e.status.textContent,/请勿刷新或关闭/);
});

test('pending publication, discard and successor ownership survive memory-only authentication recovery',async()=>{
  for(const field of ['publishID','discardPending','rebase']){
    const h=await harness({overrides:{query:async input=>input.view==='publish_receipt'?{publication:{state:'unknown'}}:{}}});
    await h.app.start({cursor:'c1'});const e=h.app.state.editor;await e.save();h.api.storage.set=()=>false;
    e[field]=field==='publishID'?'operation':field==='rebase'?{reference:'successor'}:{handle:e.meta.handle,version:e.meta.version};e.persist();
    const expected=JSON.stringify(e[field]);await h.handlers['owner-auth-lost']();await h.app.start({cursor:'c1'});
    assert.equal(JSON.stringify(h.app.state.editor[field]),expected);
    await assert.rejects(h.app.newDraft());
  }
});

test('explicit clear, logout and forget do not resurrect memory-only rescue',async()=>{
  for(const path of ['clear','logout','forget']){
    const h=await harness({rescue:{reference:'A',target_reference:'asset',version:'v1',text:'local'},overrides:{act:async()=>({state:'completed'})}});
    h.drafts.get('A').target_reference='asset';
    await h.app.start({cursor:'c1'});h.api.storage.set=()=>false;h.app.state.editor.input.value='private memory';h.app.state.editor.changed();
    if(path==='clear')await h.app.manageRescue();
    else if(path==='logout')await h.node('logout').onclick();
    else await h.app.forget({reference:'asset',handle:'asset'},'body');
    await h.confirmations.at(-1).run();assert.equal(h.editor.rescuedInput(),null);assert.equal(h.saved.has(rescueKey),false);
    await h.app.start({cursor:'c1'});assert.equal(h.app.state.editor,null);
  }
});

test('failed browser deletion is not mistaken for complete cleanup and retries without reviving old text',async()=>{
  const h=await harness({failFirst:true});await h.app.start({cursor:'c1'});
  const remove=h.api.storage.remove;h.api.storage.remove=()=>false;
  await h.app.manageRescue();await h.confirmations.at(-1).run();
  assert.equal(h.editor.rescuedInput(),null);assert.equal(h.editor.rescueCleanupPending(),true);
  assert.ok(h.node('main').all().some(n=>n.label==='重试清理暂存'));assert.match(h.notices.at(-1),/仍待清理/);
  let warned=false;h.handlers.beforeunload({preventDefault(){warned=true;}});assert.equal(warned,true);
  h.api.storage.set=()=>false;await h.app.newDraft();const e=h.app.state.editor;e.input.value='new B memory';e.changed();
  h.editor.clearRescue('A');assert.equal(h.editor.rescuedInput().text,'new B memory');
  h.api.storage.remove=remove;h.api.query=async()=>({changed:false});await h.app.poll();
  assert.equal(h.saved.has(rescueKey),false);assert.equal(h.editor.rescuedInput().text,'new B memory');assert.equal(h.editor.rescueCleanupPending(),false);
});

test('initial pending read retries independently of changes and stops retrying after success',async()=>{
  let attempts=0,healthy=false;
  const h=await harness({overrides:{query:async input=>{
    if(input.view==='pending'){attempts++;if(!healthy)throw Object.assign(new Error('pending unavailable'),{status:503});return {decisions:[{state:'pending'}]};}
    return input.view==='changes'?{changed:false,cursor:'c1'}:{};
  }}});
  await h.app.start({cursor:'c1'});assert.equal(attempts,1);await h.app.poll();assert.equal(attempts,2);
  assert.equal(h.node('notice').hidden,false);assert.match(h.node('notice').textContent,/稍后会重试/);
  healthy=true;await h.app.poll();assert.equal(attempts,3);assert.equal(h.node('pending-count').textContent,'1');
  assert.equal(h.node('notice').hidden,true);
  await h.app.poll();await h.app.poll();assert.equal(attempts,3);assert.equal(h.app.state.pendingDirty,false);
});

test('pending recovery withdraws only its own notice, including modal targets',async()=>{
  for(const modal of [false,true])for(const superseded of [false,true]){
    let healthy=false;
    const h=await harness({overrides:{query:async input=>{
      if(input.view==='pending'){if(!healthy)throw Object.assign(new Error('pending unavailable'),{status:503});return {decisions:[]};}
      return {changed:false};
    }}});
    const target=modal?new Node():h.node('notice');if(modal)h.document.modalNotice=target;
    await h.app.start({cursor:'c1'});assert.match(target.textContent,/稍后会重试/);
    if(superseded)h.ui.notice('另一个操作尚未完成',true);
    healthy=true;await h.app.poll();assert.equal(target.hidden,!superseded);
    if(superseded)assert.equal(target.textContent,'另一个操作尚未完成');
  }
  const h=await harness();const old=h.ui.notice('同样的文案');h.ui.notice('同样的文案');old();assert.equal(h.node('notice').hidden,false);
});

test('old poll completion cannot cancel or duplicate the new authenticated runs poll',async()=>{
  const h=await harness();await h.app.start({cursor:'c1'});const entered=deferred(),gate=deferred(),query=h.api.query;
  h.api.query=async input=>{if(input.view==='changes'){entered.resolve();await gate.promise;return {changed:false};}return query(input);};
  const old=h.app.poll();await entered.promise;await h.handlers['owner-auth-lost']();await h.app.start({cursor:'new'});
  const timer=h.app.state.pollTimer;gate.resolve();await old;
  assert.equal(h.app.state.pollTimer,timer);assert.equal([...h.timers.values()].filter(t=>t.fn.name==='poll').length,1);
});

test('a poll queued in the destroyed editor cannot commit its cursor into a reauthenticated run',async()=>{
  const h=await harness();await h.app.start({cursor:'c1'});const e=h.app.state.editor,gate=deferred(),entered=deferred();
  const held=e.serial(()=>gate.promise),reconcile=e.reconcile.bind(e);
  e.reconcile=(...args)=>{entered.resolve();return reconcile(...args);};
  const query=h.api.query;h.api.query=async input=>input.view==='changes'?{changed:true,cursor:'old-poll-cursor'}:query(input);
  const polling=h.app.poll();await entered.promise;await h.handlers['owner-auth-lost']();await h.app.start({cursor:'new-authenticated-cursor'});
  gate.resolve();await held;await polling;
  assert.equal(h.app.state.cursor,'new-authenticated-cursor');assert.equal([...h.timers.values()].filter(t=>t.fn.name==='poll').length,1);
});

test('overlapping entry links serialize and preserve rescue through a failed first verification',async()=>{
  const h=await harness();await h.bootstrap();await h.handlers['owner-auth-lost']();
  const entered=deferred(),gate=deferred();let active=0,maximum=0,attempts=0;
  h.api.initialize=async()=>{h.location.hash='';maximum=Math.max(maximum,++active);const n=++attempts;try{if(n===1){entered.resolve();await gate.promise;throw new Error('expired entry');}return {cursor:'new'};}finally{active--;}};
  h.location.hash='#'+'a'.repeat(64);const first=h.handlers.hashchange();await entered.promise;
  h.location.hash='#'+'b'.repeat(64);await h.handlers.hashchange();gate.resolve();await first;
  assert.equal(attempts,2);assert.equal(maximum,1);assert.equal(h.app.state.active,true);assert.equal(h.app.state.editor.value,h.rescue.text);
});
