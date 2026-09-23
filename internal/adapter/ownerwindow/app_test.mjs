import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';

// Exercise the actual application lifecycle, with rendering/transport replaced
// at its edges. Browser acceptance separately covers the real DOM and HTTP.
async function harness(){
  const calls=[],saved=new Map(),visible=new Set();
  const nodes=new Map();
  const node=id=>{if(!nodes.has(id))nodes.set(id,{textContent:'',hidden:false,contains:n=>visible.has(n),replaceChildren:()=>calls.push(['clear',id])});return nodes.get(id);};
  const document={getElementById:node,querySelectorAll:()=>[],activeElement:{tagName:'DIV'},hidden:false};
  const api={query:async()=>({changed:true,cursor:'new-cursor'}),resolve:async()=>({assets:[{version:'v1'}]}),invalidate:()=>calls.push(['invalidate']),scope:()=>()=>true};
  for(const n of ['act','text','request','logout','operationID'])api[n]=()=>{};
  const editor={Editor:class{},rescuedInput:()=>null,clearRescue:()=>saved.clear(),retainReceipt:()=>{}};
  const ui={};for(const n of ['el','button','row','heading','empty','prose','tag','date','notice','clearNotice','statusName','permissionName','dialog','confirm','download','errorMessage'])ui[n]=()=>{};
  const context=vm.createContext({document,window:{},navigator:{},setTimeout:()=>1,clearTimeout:()=>{}});
  const source=await readFile(new URL('./static/app.js',import.meta.url),'utf8');
  const module=new vm.SourceTextModule(source+'\nexport {state,lock,poll}; export function observe(renderPage,pending){render=renderPage;refreshPending=pending;}',{context});
  await module.link(spec=>{const value=spec.includes('api.js')?api:spec.includes('editor.js')?editor:ui;return new vm.SyntheticModule(Object.keys(value),function(){for(const [k,v] of Object.entries(value))this.setExport(k,v);},{context});});
  await module.evaluate();const app=module.namespace;
  app.observe(async()=>calls.push(['render']),async()=>calls.push(['pending']));
  return {app,calls,saved,document,visible};
}

test('explicit logout destroys dirty and conflicted editor without re-persisting input',async()=>{
  for(const conflict of [false,true]){
    const h=await harness();h.saved.set('input','private');
    h.app.state.editor={dirty:true,conflict,persist:()=>h.saved.set('input','private'),destroy:()=>h.calls.push(['destroy'])};
    h.app.lock('bye',false);assert.equal(h.saved.size,0);assert.equal(h.app.state.editor,null);assert.equal(h.app.state.active,false);
    assert.ok(h.calls.some(c=>c[0]==='destroy'));
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
