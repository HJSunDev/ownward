import {createPageNavigation} from './static/ui.js';
import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';

// Exercise the actual application lifecycle, with rendering/transport replaced
// at its edges. Browser acceptance separately covers the real DOM and HTTP.
async function harness(overrides={},editorOverrides={},uiOverrides={}){
  const calls=[],saved=new Map(),visible=new Set();
  const nodes=new Map();
  const node=id=>{if(!nodes.has(id))nodes.set(id,{textContent:'',hidden:false,dataset:{},append:()=>{},prepend:()=>{},contains:n=>visible.has(n),replaceChildren:()=>calls.push(['clear',id])});return nodes.get(id);};
  const documentHandlers={};
  const document={getElementById:node,querySelectorAll:()=>[],addEventListener:(name,run)=>{documentHandlers[name]=run;},activeElement:{tagName:'DIV'},hidden:false};
  const api={query:async()=>({changed:true,cursor:'new-cursor'}),resolve:async()=>({assets:[{reference:'asset',version:'v1'}]}),invalidate:()=>calls.push(['invalidate']),scope:()=>()=>true};
  for(const n of ['act','text','request','quit','operationID'])api[n]=()=>{};
  Object.assign(api,overrides);
  const editor={Editor:class{},rescuedInput:()=>null,rescueNeedsWindow:()=>false,rescueCleanupPending:()=>false,retryRescueCleanup:()=>true,clearRescue:()=>saved.clear(),retainReceipt:()=>{}};
  Object.assign(editor,editorOverrides);
  const ui={createPageNavigation,openCollaboration:async()=>{},refreshCollaboration:async()=>{}};for(const n of ['el','button','row','heading','empty','prose','tag','date','notice','clearNotice','statusName','permissionName','dialog','confirm','download','errorMessage','documentView','appearanceControls','applyAppearance','setImmersive'])ui[n]=()=>{};
  ui.createSelect=()=>({node:{},value:()=>'',set(){},focus(){}});
  ui.confirm=async(_,__,___,run)=>run();
  Object.assign(ui,uiOverrides);
  const handlers={},window={addEventListener:(name,run)=>handlers[name]=run};
  const context=vm.createContext({document,window,navigator:{},setTimeout:()=>1,clearTimeout:()=>{}});
  const source=await readFile(new URL('./static/app.js',import.meta.url),'utf8');
  const module=new vm.SourceTextModule(source+'\nexport {stopOwnward,state,lock,poll,openDraft,newDraft,editAsset,restoredArchives}; export function observe(renderPage,pending){render=renderPage;refreshPending=pending;}',{context});
  await module.link(spec=>{const value=spec.includes('reading.js')?{createReader:()=>({node:{},status:{},destroy(){},markUpdated(){}})}:spec.includes('graph.js')?{createGraph:()=>({node:{},capture:()=>null,destroy(){}})}:spec.includes('api.js')?api:spec.includes('editor.js')?editor:ui;return new vm.SyntheticModule(Object.keys(value),function(){for(const [k,v] of Object.entries(value))this.setExport(k,v);},{context});});
  await module.evaluate();const app=module.namespace;
  app.observe(async()=>calls.push(['render']),async()=>calls.push(['pending']));
  return {app,calls,saved,document,visible,handlers,documentHandlers};
}

test('pending disclosure can close without disturbing the reading surface or stealing outside focus',async()=>{
  const h=await harness();await h.app.start({cursor:'initial'});
  const panel=h.document.getElementById('pending-panel'),trigger=h.document.getElementById('pending-entry'),inside={},outside={};
  let focus='outside';panel.hidden=true;panel.contains=target=>target===inside;trigger.contains=target=>target===trigger;
  trigger.setAttribute=(name,value)=>{trigger[name]=value;};trigger.focus=()=>{focus='trigger';};
  panel.querySelector=()=>({focus(){focus='panel';}});
  trigger.onclick();assert.equal(panel.hidden,false);assert.equal(focus,'panel');assert.equal(trigger['aria-expanded'],'true');
  h.documentHandlers.pointerdown({target:inside});assert.equal(panel.hidden,false);
  const before=h.calls.length;let prevented=false;
  h.documentHandlers.keydown({key:'Escape',preventDefault(){prevented=true;}});
  assert.equal(panel.hidden,true);assert.equal(focus,'trigger');assert.ok(prevented);assert.equal(h.calls.length,before,'closing must not replace the reader');
  trigger.onclick();focus='outside';h.documentHandlers.pointerdown({target:outside});
  assert.equal(panel.hidden,true);assert.equal(focus,'outside');assert.equal(trigger['aria-expanded'],'false');
});

test('explicit logout destroys dirty and conflicted editor without re-persisting input',async()=>{
  for(const conflict of [false,true]){
    const h=await harness();h.saved.set('input','private');
    h.app.state.editor={dirty:true,conflict,persist:()=>h.saved.set('input','private'),destroy:()=>h.calls.push(['destroy'])};
    h.app.lock('bye',false);assert.equal(h.saved.size,0);assert.equal(h.app.state.editor,null);assert.equal(h.app.state.active,false);
    assert.ok(h.calls.some(c=>c[0]==='destroy'));
  }
});

test('restore opening keeps the displayed revision and ignores a closed result dialog',async()=>{
  for(const closeEarly of [false,true]){
    const buttons=[],dialogs=[],notices=[],requests=[];let release;
    const opening=new Promise(resolve=>{release=resolve;});
    const node=(...children)=>({children,append(...items){this.children.push(...items);},replaceChildren(...items){this.children=items;}});
    const ui={el:(_tag,_attrs,...children)=>node(...children),row:(...children)=>node(...children),date:()=> '今天',notice:s=>notices.push(s),
      button:(label,run)=>{const value={label,run};buttons.push(value);return value;},
      dialog:(title,body,actions=[])=>{let live=true;const close=()=>{live=false;};close.current=()=>live;const value={title,body,actions,close};dialogs.push(value);return value;}};
    const h=await harness({request:async(_path,body)=>{requests.push(body);if(body.action==='list')return [{id:'result',created:'today',ready:true}];if(body.action==='open')return opening;return {state:'default_updated'};}},{},ui);
    await h.app.restoredArchives();const pending=buttons.find(b=>b.label==='打开恢复的资料').run();
    if(closeEarly)dialogs[0].close();release({default_available:true,default_revision:17});await pending;
    const change=buttons.find(b=>b.label==='以后打开这份资料');
    if(closeEarly){assert.equal(change,undefined);assert.equal(notices.length,0);continue;}
    const attached=(root,label)=>root.label===label||root.children?.some(child=>typeof child==='object'&&child&&attached(child,label));
    assert.ok(attached(dialogs[0].body,'打开恢复的资料'),'restored result must remain reopenable');
    change.run();const confirm=dialogs.at(-1);await confirm.actions.find(a=>a.label==='确认更换').run(confirm.close);
    assert.equal(requests.at(-1).revision,17);assert.equal(requests.at(-1).id,'result');
    assert.ok(attached(dialogs[0].body,'打开恢复的资料'));
    assert.ok(!attached(dialogs[0].body,'以后打开这份资料'),'completed selection is no longer offered as pending');
    await buttons.find(b=>b.label==='打开恢复的资料').run();assert.equal(requests.at(-1).action,'open');
  }
});

test('opening a listed draft protects unmatched rescue and keeps late hooks on their own editor',async()=>{
  for(const reference of ['successor','unrelated']){
    const rescue={reference:'successor',version:null,text:'my text'},constructed=[];
    let h;
    class Editor {
      constructor(meta,content,hooks,input){this.meta=meta;this.node={};this.input={focus(){}};this.hooks=hooks;constructed.push({meta,content,input,editor:this});}
    }
    h=await harness({resolve:async()=>({drafts:[{reference,version:'v3',handle:'remote'}]}),text:async()=> 'other writer'},
      {Editor,rescuedInput:()=>rescue});
    if(reference==='unrelated'){await assert.rejects(h.app.openDraft(reference),/恢复或处理/);assert.equal(constructed.length,0);continue;}
    await h.app.openDraft(reference);
    assert.equal(constructed[0].input,reference==='successor'?rescue:null);
    const old=constructed[0].editor,next={node:{}};h.app.state.editor=next;
    await old.hooks.published('old-publication');assert.equal(h.app.state.editor,next);
  }
});

test('lost authentication preserves rescue before isolating editor',async()=>{
  const h=await harness();h.app.state.editor={persist:()=>h.saved.set('input','local'),destroy:()=>h.calls.push(['destroy'])};
  h.app.lock('expired');assert.equal(h.saved.get('input'),'local');assert.equal(h.app.state.editor,null);
});

test('detached draft reconciles without replacing the selected reading surface',async()=>{
  const h=await harness();h.app.state.selection={reference:'asset',version:'v1'};
  h.app.state.editor={node:{},reconcile:async()=>h.calls.push(['reconcile'])};
  await h.app.poll();assert.ok(h.calls.some(c=>c[0]==='reconcile'));assert.ok(!h.calls.some(c=>c[0]==='render'));
  assert.equal(h.app.state.selection.reference,'asset');assert.equal(h.app.state.cursor,'new-cursor');
});

test('focused list defers its checkpoint and refreshes after blur without a new write',async()=>{
  const h=await harness();h.app.state.editor={node:{},reconcile:async()=>{}};
  h.document.activeElement={tagName:'INPUT'};h.visible.add(h.document.activeElement);
  await h.app.poll();assert.equal(h.app.state.cursor,'');assert.ok(!h.calls.some(c=>c[0]==='render'));
  h.document.activeElement={tagName:'DIV'};await h.app.poll();
  assert.ok(h.calls.some(c=>c[0]==='render'));assert.equal(h.app.state.cursor,'new-cursor');
});

test('background list refresh retains its continuation instead of resetting to the first page',async()=>{
  const h=await harness();h.app.state.surface='events';h.app.state.after='older-events';
  await h.app.poll();assert.equal(h.app.state.after,'older-events');assert.ok(h.calls.some(c=>c[0]==='render'));
});

test('application quit clears input only after accepted shutdown',async()=>{
  for(const status of [200,401,503,0]){
    let h;
    h=await harness({quit:async()=>{
      assert.equal(h.saved.size,1);assert.ok(h.app.state.editor);assert.equal(h.app.state.active,true);
      if(status!==200)throw Object.assign(new Error('transport'),{status});
    }});
    await h.app.start({cursor:'initial'});h.saved.set('input','private');
    h.app.state.editor={dirty:true,persist:()=>h.saved.set('input','private'),destroy:()=>h.calls.push(['destroy'])};
    await h.app.stopOwnward();
    assert.equal(h.saved.size,status===200?0:1);
    assert.equal(h.app.state.editor===null,status===200);
    if(status===200){assert.equal(h.document.getElementById('entry').dataset.phase,'closed');assert.equal(h.document.getElementById('entry-help').hidden,true);}
    else assert.equal(h.app.state.active,true);
  }
});

test('stop is visibly pending, blocks duplicate requests and polling, and reports its result in place',async()=>{
  for(const failed of [false,true]){
    let finish,stops=0,reads=0;
    const response=new Promise((resolve,reject)=>{finish=()=>failed?reject(Object.assign(new Error('offline'),{status:0})):resolve();});
    const h=await harness({quit:()=>{stops++;return response;},query:async()=>{reads++;return {};}});
    const stopping=h.app.stopOwnward(),control=h.document.getElementById('stop-ownward'),feedback=h.document.getElementById('stop-feedback');
    assert.equal(control.textContent,'正在停止…');assert.equal(control.disabled,true);
    assert.equal(feedback.hidden,false);assert.match(feedback.textContent,/正在停止后台服务/);
    await h.app.stopOwnward();await h.app.poll();assert.equal(stops,1);assert.equal(reads,0);
    finish();await stopping;
    if(failed){
      assert.equal(control.disabled,false);assert.equal(control.textContent,'停止 Ownward');
      assert.equal(feedback.hidden,false);assert.match(feedback.textContent,/尚未确认停止/);
      assert.equal(h.app.state.exiting,false);assert.equal(h.app.state.active,true);
    }else{
      assert.equal(h.document.getElementById('entry-title').textContent,'Ownward 已停止');
      assert.equal(h.app.state.active,false);assert.match(h.document.getElementById('status').textContent,/重新启动/);
    }
  }
});

test('stopping cannot discard text changed after confirmation or interrupt an editor operation',async()=>{
  let action,stops=0;
  const h=await harness({quit:async()=>{stops++;}},{},{confirm:async(_title,_body,_label,run)=>{action=run;}});
  await h.app.start({cursor:'initial'});
  h.app.state.editor={dirty:true,value:'shown text',busy:false};
  await h.app.stopOwnward();
  h.app.state.editor.value='newer text';
  await assert.rejects(action(),/已有变化/);assert.equal(stops,0);
  await h.app.stopOwnward();h.app.state.editor.busy=true;
  await assert.rejects(action(),/仍在进行/);assert.equal(stops,0);
});

test('delayed draft switch cannot destroy a replacement editor or create another draft',async()=>{
  for(const action of ['openDraft','newDraft','editAsset']){
    let release,entered;const ready=new Promise(r=>entered=r),gate=new Promise(r=>release=r);
    const h=await harness({act:async()=>{throw new Error('obsolete action');},resolve:async()=>{throw new Error('obsolete read');}});
    const old={meta:{reference:'old'},flush:async()=>{entered();await gate;},destroy:()=>h.calls.push(['destroy-old'])};
    const next={destroy:()=>h.calls.push(['destroy-next'])};h.app.state.editor=old;
    const changing=h.app[action]('other');await ready;h.app.state.editor=next;release();await changing;
    assert.equal(h.app.state.editor,next);assert.equal(h.calls.some(c=>c[0].startsWith('destroy')),false);
  }
});

test('detached rebase installs a protected successor before old-draft cleanup can yield',async()=>{
  const constructed=[];let creates=0;
  class Editor{
    constructor(meta,body,hooks,rescue){Object.assign(this,{meta,body,hooks,rescue,node:{},input:{focus(){}},live:true});constructed.push(this);}
    suspend(){this.paused=true;}
    async flush(){if(this.rescue?.version===null)throw new Error('unresolved conflict');}
  }
  const h=await harness({resolve:async()=>({drafts:[{reference:'original',version:'v1',handle:'h'}]}),text:async()=> 'mine',act:async()=>{creates++;}}, {Editor});
  await h.app.openDraft('original');const old=h.app.state.editor;
  h.app.state.surface='control';h.app.state.selection={reference:'reading'};
  const rescue={reference:'successor',version:null,text:'mine'};
  await old.hooks.rebased({reference:'successor',version:'v3',handle:'h3'},'other writer',rescue);
  assert.equal(h.app.state.surface,'control');assert.equal(h.app.state.selection.reference,'reading');
  assert.equal(h.app.state.editor,constructed[1]);assert.equal(h.app.state.editor.rescue,rescue);assert.equal(h.app.state.editor.paused,true);
  await assert.rejects(h.app.newDraft(),/unresolved conflict/);assert.equal(creates,0);
});
