// Run with: node --experimental-vm-modules --test internal/adapter/ownerwindow/editor_test.mjs
// No DOM package or browser dependency: adversarial transport ordering tests.
import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';

class Node {
  constructor(attrs={}){Object.assign(this,{value:'',children:[],hidden:false,isConnected:true},attrs);}
  append(...items){this.children.push(...items);}
  replaceChildren(...items){this.children=items;}
  addEventListener(){}
  focus(){}
}
function deferred(){let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};}
async function harness(overrides={}){
  const saved=new Map(),calls=[],dialogs=[];
  const remote={reference:'draft-ref',handle:'handle-1',version:'v1',text:'base'};
  const hooks={leave:async()=>{},grant:async()=>{},open:async()=>{},rebased:async()=>{},discarded:async()=>{},pendingReceipt:async()=>calls.push(['pendingReceipt']),published:async h=>calls.push(['published',h]),unavailable:()=>calls.push(['unavailable'])};
  const api={
    storage:{get:k=>saved.get(k)||null,set:(k,v)=>{saved.set(k,v);return true;},remove:k=>saved.delete(k)},
    pause:async()=>{},operationID:()=>{calls.push(['new-operation']);return 'operation-id';},scope:()=>()=>true,
    resolve:async()=>{calls.push(['resolve']);return {drafts:[{...remote}]};},
    text:async()=>remote.text,
    query:async q=>{calls.push(['query',q]);return {publication:{state:'unknown'}};},
    act:async a=>{calls.push(['act',a]);return {handle:'published-asset'};},
    replace:async(handle,value)=>{calls.push(['replace',handle,value]);remote.text=value;remote.handle='handle-2';remote.version='v2';return {handle:remote.handle};},...overrides
  };
  const ui={
    el:(tag,attrs,...children)=>{const n=new Node(attrs);n.tag=tag;n.append(...children);return n;},
    button:(label,action)=>new Node({label,action}),row:(...c)=>new Node({children:c}),prose:value=>new Node({textContent:value}),
    notice:message=>calls.push(['notice',message]),download:()=>{},confirm:()=>{},
    dialog:(title,body,actions)=>{const d={title,body,actions,close:()=>{d.closed=true;}};d.close.current=()=>!d.closed;dialogs.push(d);return d;}
  };
  const context=vm.createContext({console,Date,JSON,Blob,setTimeout:()=>1,clearTimeout:()=>{}});
  const source=await readFile(new URL('./static/editor.js',import.meta.url),'utf8');
  const module=new vm.SourceTextModule(source,{context});
  await module.link(spec=>{const values=spec.includes('api.js')?api:ui;return new vm.SyntheticModule(Object.keys(values),function(){for(const [k,v] of Object.entries(values))this.setExport(k,v);},{context});});
  await module.evaluate();
  return {Editor:module.namespace.Editor,remote,saved,calls,hooks,api,dialogs,meta:()=>({...remote})};
}

test('typing during a slow save is not acknowledged by the older response',async()=>{
  const gate=deferred(),entered=deferred();let h;
  h=await harness({replace:async(handle,value)=>{entered.resolve();await gate.promise;h.remote.text=value;h.remote.version='v2';h.remote.handle='h2';return {handle:'h2'};}});
  const e=new h.Editor(h.meta(),'base',h.hooks);
  e.input.value='first input';e.changed();const saving=e.save();await entered.promise;
  e.input.value='later input';e.changed();gate.resolve();await saving;
  assert.equal(e.value,'later input');assert.equal(e.base,'first input');assert.equal(e.dirty,true);
  assert.equal(JSON.parse(h.saved.get('ownward.owner-input')).text,'later input');
  e.destroy();
});

test('a remote write after our successful save retains both conflict texts across reload',async()=>{
  let h;h=await harness({replace:async()=>{h.remote.text='remote second write';h.remote.version='v3';h.remote.handle='h3';return {handle:'h2'};}});
  const e=new h.Editor(h.meta(),'base',h.hooks);e.input.value='my accepted write';e.changed();await e.save();
  assert.equal(e.conflict.content,'remote second write');
  const rescue=JSON.parse(h.saved.get('ownward.owner-input'));assert.equal(rescue.text,'my accepted write');
  const reopened=new h.Editor(h.meta(),h.remote.text,h.hooks,rescue);
  assert.equal(reopened.value,'my accepted write');assert.equal(reopened.conflict.content,'remote second write');
  e.destroy();reopened.destroy();
});

test('forced refresh cannot race ahead of an unfinished write',async()=>{
  const gate=deferred(),entered=deferred();let h;
  h=await harness({replace:async(_,value)=>{entered.resolve();await gate.promise;h.remote.text=value;h.remote.version='v2';return {handle:'h2'};}});
  const e=new h.Editor(h.meta(),'base',h.hooks);e.input.value='new';e.changed();
  const write=e.save();await entered.promise;const refresh=e.reconcile(true);
  await Promise.resolve();assert.equal(h.calls.filter(c=>c[0]==='resolve').length,0);
  gate.resolve();await Promise.all([write,refresh]);assert.equal(e.value,'new');assert.equal(e.conflict,null);e.destroy();
});

test('publication unknown retains its operation and only retries on explicit preview confirmation',async()=>{
  const h=await harness();h.remote.text='pending publication';
  const rescue={reference:'draft-ref',version:'v1',text:h.remote.text,publishID:'original-operation'};
  const e=new h.Editor(h.meta(),h.remote.text,h.hooks,rescue);await e.recoverPublication();
  assert.equal(e.publishID,'original-operation');assert.equal(h.calls.some(c=>c[0]==='act'),false);
  await e.preview();const preview=h.dialogs.at(-1);assert.equal(preview.title,'确认整篇内容');
  await preview.actions.find(a=>a.label==='确认存入').run(preview.close);
  const call=h.calls.find(c=>c[0]==='act')[1];assert.equal(call.operation_id,'original-operation');assert.equal(h.calls.some(c=>c[0]==='new-operation'),false);
});

test('forget visibility failure keeps editor quarantined and can be retried without new writes',async()=>{
  let fails=true;const h=await harness({resolve:async()=>{if(fails)throw new Error('offline');return {unavailable:true};}});
  const e=new h.Editor(h.meta(),'base',h.hooks);e.input.value='local';e.changed();e.quarantine();
  await assert.rejects(e.reconcile(true));assert.equal(e.node.hidden,true);assert.equal(e.live,true);
  fails=false;await e.reconcile(true);assert.equal(e.live,false);assert.equal(h.saved.size,0);assert.ok(h.calls.some(c=>c[0]==='unavailable'));
});

test('suspending a conflict does not clear local text or force a save',async()=>{
  const h=await harness(),e=new h.Editor(h.meta(),'base',h.hooks);
  e.input.value='local unsynced';e.changed();e.showConflict({...h.meta(),version:'v2'},'other text');e.suspend();
  assert.equal(e.value,'local unsynced');assert.equal(e.paused,true);assert.equal(h.calls.some(c=>c[0]==='replace'),false);
  assert.equal(JSON.parse(h.saved.get('ownward.owner-input')).text,'local unsynced');e.destroy();
});

test('unknown publication with unavailable draft destroys text and retains only receipt across persist',async()=>{
  const h=await harness({resolve:async()=>({unavailable:true})});
  const e=new h.Editor(h.meta(),'private text',h.hooks,{reference:'draft-ref',version:'v1',text:'private text',publishID:'original-operation'});
  await e.recoverPublication();e.persist();
  assert.equal(e.live,false);assert.equal(e.value,'');assert.equal(e.base,'');assert.equal(e.input.value,'');assert.equal(e.node.children.length,0);
  assert.deepEqual(JSON.parse(h.saved.get('ownward.owner-input')),{reference:'draft-ref',publishID:'original-operation'});
  assert.ok(h.calls.some(c=>c[0]==='pendingReceipt'));
});

test('failed receipt lookup leaves an explicit recovery action and honest state',async()=>{
  const h=await harness({query:async()=>{throw new Error('offline');}}),e=new h.Editor(h.meta(),'base',h.hooks);
  e.publishID='original-operation';await assert.rejects(e.recoverPublication());
  assert.equal(e.retryButton.hidden,false);assert.equal(e.status.textContent,'存入结果待核对');e.destroy();
});

test('successor changed after replace reopens as conflict without overwriting the other writer',async()=>{
  let h,writes=0;const successor={reference:'new-draft',handle:'new-1',version:'v1',text:'current asset'};
  h=await harness({
    resolve:async ref=>ref==='target-ref'?{assets:[{handle:'asset-current'}]}:{drafts:[{...successor}]},
    text:async(view)=>view==='content'?'current asset':successor.text,
    act:async a=>a.action==='create_draft'?{reference:successor.reference}:{},
    replace:async()=>{writes++;successor.text='other writer after accepted mine';successor.handle='new-3';successor.version='v3';return {handle:'new-2'};}
  });
  const e=new h.Editor({...h.meta(),target_reference:'target-ref'},'mine',h.hooks);await e.targetConflict();
  const d=h.dialogs.at(-1);await d.actions.find(a=>a.style==='primary').run(d.close);
  const rescue=JSON.parse(h.saved.get('ownward.owner-input'));
  const reopened=new h.Editor({...successor},successor.text,h.hooks,rescue);
  assert.equal(reopened.conflict.content,successor.text);assert.equal(reopened.value,'mine');
  await reopened.save();assert.equal(writes,1);assert.equal(successor.text,'other writer after accepted mine');reopened.destroy();
});

test('pending publication conflict allows explicit manual merge without automatic writes',async()=>{
  const h=await harness(),e=new h.Editor(h.meta(),'base',h.hooks,{reference:'draft-ref',version:'v0',text:'mine',publishID:'op'});
  await e.recoverPublication();assert.equal(e.input.readOnly,false);e.input.value='merged';e.changed();await e.save();
  assert.equal(h.calls.some(c=>c[0]==='replace'),false);e.destroy();
});
