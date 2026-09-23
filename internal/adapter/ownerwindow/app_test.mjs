import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';

// Exercise the actual application lifecycle, with rendering/transport replaced
// at its edges. Browser acceptance separately covers the real DOM and HTTP.
async function harness(overrides={},editorOverrides={}){
  const calls=[],saved=new Map(),visible=new Set();
  const nodes=new Map();
  const node=id=>{if(!nodes.has(id))nodes.set(id,{textContent:'',hidden:false,append:()=>{},prepend:()=>{},contains:n=>visible.has(n),replaceChildren:()=>calls.push(['clear',id])});return nodes.get(id);};
  const document={getElementById:node,querySelectorAll:()=>[],addEventListener:()=>{},activeElement:{tagName:'DIV'},hidden:false};
  const api={query:async()=>({changed:true,cursor:'new-cursor'}),resolve:async()=>({assets:[{version:'v1'}]}),invalidate:()=>calls.push(['invalidate']),scope:()=>()=>true};
  for(const n of ['act','text','request','logout','operationID'])api[n]=()=>{};
  Object.assign(api,overrides);
  const editor={Editor:class{},rescuedInput:()=>null,clearRescue:()=>saved.clear(),retainReceipt:()=>{}};
  Object.assign(editor,editorOverrides);
  const ui={};for(const n of ['el','button','row','heading','empty','prose','tag','date','notice','clearNotice','statusName','permissionName','dialog','confirm','download','errorMessage'])ui[n]=()=>{};
  ui.confirm=async(_,__,___,run)=>run();
  const handlers={},window={addEventListener:(name,run)=>handlers[name]=run};
  const context=vm.createContext({document,window,navigator:{},setTimeout:()=>1,clearTimeout:()=>{}});
  const source=await readFile(new URL('./static/app.js',import.meta.url),'utf8');
  const module=new vm.SourceTextModule(source+'\nexport {state,lock,poll,openDraft,newDraft,editAsset}; export function observe(renderPage,pending){render=renderPage;refreshPending=pending;}',{context});
  await module.link(spec=>{const value=spec.includes('api.js')?api:spec.includes('editor.js')?editor:ui;return new vm.SyntheticModule(Object.keys(value),function(){for(const [k,v] of Object.entries(value))this.setExport(k,v);},{context});});
  await module.evaluate();const app=module.namespace;
  app.observe(async()=>calls.push(['render']),async()=>calls.push(['pending']));
  return {app,calls,saved,document,visible,handlers};
}

test('explicit logout destroys dirty and conflicted editor without re-persisting input',async()=>{
  for(const conflict of [false,true]){
    const h=await harness();h.saved.set('input','private');
    h.app.state.editor={dirty:true,conflict,persist:()=>h.saved.set('input','private'),destroy:()=>h.calls.push(['destroy'])};
    h.app.lock('bye',false);assert.equal(h.saved.size,0);assert.equal(h.app.state.editor,null);assert.equal(h.app.state.active,false);
    assert.ok(h.calls.some(c=>c[0]==='destroy'));
  }
});

test('opening a listed draft applies only its matching rescue and keeps late hooks on their own editor',async()=>{
  for(const reference of ['successor','unrelated']){
    const rescue={reference:'successor',version:null,text:'my text'},constructed=[];
    let h;
    class Editor {
      constructor(meta,content,hooks,input){this.meta=meta;this.node={};this.input={focus(){}};this.hooks=hooks;constructed.push({meta,content,input,editor:this});}
    }
    h=await harness({resolve:async()=>({drafts:[{reference,version:'v3',handle:'remote'}]}),text:async()=> 'other writer'},
      {Editor,rescuedInput:()=>rescue});
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

test('confirmed logout clears input before success, expired-session or disconnected replies',async()=>{
  for(const status of [200,401,503,0]){
    let h;
    h=await harness({logout:async()=>{
      assert.equal(h.saved.size,0);assert.equal(h.app.state.editor,null);assert.equal(h.app.state.active,false);
      if(status===401)h.handlers['owner-auth-lost']();
      if(status!==200)throw Object.assign(new Error('transport'),{status});
    }});
    await h.app.start({cursor:'initial'});h.saved.set('input','private');
    h.app.state.editor={dirty:true,persist:()=>h.saved.set('input','private'),destroy:()=>h.calls.push(['destroy'])};
    await h.document.getElementById('logout').onclick();
    assert.equal(h.saved.size,0);assert.equal(h.app.state.editor,null);
    assert.match(h.document.getElementById('status').textContent,status===200||status===401?/已退出/:/尚未确认/);
    h.handlers['owner-auth-lost']();assert.equal(h.saved.size,0);
  }
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
