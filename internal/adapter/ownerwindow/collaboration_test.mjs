import {createPageNavigation} from './static/ui.js';
import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';

async function harness({copy=async()=>{},flush=async()=>{},read}={}){
  class Node{
    constructor(tag,attrs={},children=[]){this.tag=tag;Object.assign(this,attrs);this.children=children.filter(Boolean);this.hidden=!!attrs.hidden;}
    replaceChildren(...children){this.children=children;}
    append(...children){this.children.push(...children);}
    focus(){this.focused=true;}select(){this.selected=true;}
  }
  const calls=[],views=[];let current=true,page={collaborations:[]};
  const api={scope:()=>()=>current,query:async q=>{calls.push(q);return read?read(q):page;},act:async q=>{calls.push(q);return {handle:'invite-handle',invitation:{id:'invitation',expires_at:'2099-01-01T00:00:00Z'},instruction:'Public request; no body or authority.'};}};
  const ui={createPageNavigation,el:(tag,attrs,...children)=>new Node(tag,attrs,children),button:(label,action,style)=>Object.assign(new Node('button',{},[label]),{label,action,style}),date:v=>v,errorMessage:e=>e.message,
    dialog:(title,body)=>{let live=true;const close=()=>{live=false;};close.current=()=>live;const view={title,body,close};views.push(view);return view;}};
  const context=vm.createContext({navigator:{clipboard:{writeText:copy}},document:{getElementById:()=>({click(){calls.push('pending-open');}})}});
  const source=await readFile(new URL('./static/collaboration.js',import.meta.url),'utf8');
  const module=new vm.SourceTextModule(source,{context});
  await module.link(spec=>{const values=spec==='./api.js'?api:ui;return new vm.SyntheticModule(Object.keys(values),function(){for(const [k,v]of Object.entries(values))this.setExport(k,v);},{context});});
  await module.evaluate();
  const editor={live:true,meta:{handle:'draft-handle'},flush,collaboration:new Node('aside')};
  function find(node,predicate){if(predicate(node))return node;for(const child of node?.children||[]){const found=typeof child==='object'&&find(child,predicate);if(found)return found;}}
  return {api:module.namespace,editor,calls,views,find,page:value=>{page=value;},leave:()=>{current=false;}};
}

test('an empty connection inventory still has an actionable handoff and cancellation',async()=>{
  let copied='';const h=await harness({copy:async text=>{copied=text;}});
  await h.api.openCollaboration(h.editor);const view=h.views[0];
  await h.find(view.body,n=>n.label==='复制协作请求').action();
  await h.find(view.body,n=>n.label==='复制协作请求').action();
  assert.equal(h.calls.filter(c=>c.action==='invite_draft').length,1);
  assert.match(copied,/Public request/);
  assert.equal(h.calls.some(c=>c.view==='connections'||c.action==='grant_draft'),false);
  const cancel=h.find(view.body,n=>n.label==='取消这次请求');assert.equal(cancel.hidden,false);await cancel.action();
  assert.ok(h.calls.some(c=>c.action==='cancel_invitation'&&c.handle==='invite-handle'));
  assert.equal(cancel.hidden,true);assert.equal(h.editor.live,true);
});

test('collaboration history remains bounded and later pages stay reachable',async()=>{
  const h=await harness();h.page({collaborations:[],next:'page-two'});await h.api.openCollaboration(h.editor);
  assert.equal(h.editor.collaboration.hidden,false);assert.ok(h.find(h.editor.collaboration,n=>n.label==='查看全部协作'));
  h.page({collaborations:[{state:'approved',handle:'later-request',connection:{name:'Later writer'}}]});
  await h.find(h.views[0].body,n=>n.label==='下一页').action();
  assert.ok(h.calls.some(c=>c.after==='page-two'&&c.limit===20));
  await h.find(h.views[0].body,n=>n.label==='结束协助').action();
  assert.ok(h.calls.some(c=>c.action==='end_collaboration'&&c.handle==='later-request'));
});

test('background changes and ending collaboration keep the currently inspected page',async()=>{
  let ended=false;
  const h=await harness({read:async q=>q.state==='active'?{collaborations:[]}:
    q.after?{collaborations:[{state:ended?'cancelled':'approved',handle:'later',connection:{name:'Later writer'}}]}:
    {collaborations:[{state:'declined',connection:{name:'First writer'}}],next:'two'}});
  await h.api.openCollaboration(h.editor);const view=h.views[0];
  await h.find(view.body,n=>n.label==='下一页').action();
  const later=()=>h.find(view.body,n=>n.tag==='span'&&n.children?.some(c=>typeof c==='string'&&c.includes('Later writer')));
  const before=later();assert.ok(before);
  await h.api.refreshCollaboration(h.editor);assert.equal(later(),before,'unchanged data preserves DOM and focus');
  ended=true;await h.find(view.body,n=>n.label==='结束协助').action();
  assert.ok(later());assert.equal(h.find(view.body,n=>n.label==='结束协助'),undefined);
  assert.ok(h.calls.some(c=>c.after==='two'&&c.refresh===true));
  assert.equal(h.editor.collaboration.hidden,true,'history does not manufacture an active summary');
});

test('clipboard failure offers selectable text without losing the draft',async()=>{
  const h=await harness({copy:async()=>{throw new Error('denied');}});await h.api.openCollaboration(h.editor);
  await h.find(h.views[0].body,n=>n.label==='复制协作请求').action();
  const fallback=h.find(h.views[0].body,n=>n.tag==='textarea');assert.equal(fallback.hidden,false);assert.equal(fallback.selected,true);assert.match(fallback.value,/Public request/);
});

test('an ended handoff in an open panel can immediately start a fresh request',async()=>{
  const h=await harness();await h.api.openCollaboration(h.editor);const copy=h.find(h.views[0].body,n=>n.label==='复制协作请求');await copy.action();
  h.page({collaborations:[{invitation:'invitation',state:'cancelled',connection:{name:'Writer'}}]});await h.api.refreshCollaboration(h.editor);
  await copy.action();assert.equal(h.calls.filter(c=>c.action==='invite_draft').length,2);
});

test('invitation validity is independent of the history page and another collaborator ending',async()=>{
  let state='mixed';
  const h=await harness({read:async q=>q.reference?state==='gone'?{unavailable:true}:{collaborations:[{state:'cancelled'},...(state==='mixed'?[{state:'approved'}]:[])]}:
    {collaborations:[],next:q.after?'':'two'}});
  await h.api.openCollaboration(h.editor);const copy=h.find(h.views[0].body,n=>n.label==='复制协作请求');
  await copy.action();await h.find(h.views[0].body,n=>n.label==='下一页').action();
  await copy.action();assert.equal(h.calls.filter(c=>c.action==='invite_draft').length,1,'one ended recipient cannot cancel the other recipient');
  state='gone';await copy.action();assert.equal(h.calls.filter(c=>c.action==='invite_draft').length,2,'deleted invitation cannot be copied again from another history page');
});

test('leaving during save cannot create an invitation in a stale editor',async()=>{
  let release;const h=await harness({flush:()=>new Promise(r=>{release=r;})});const opened=h.api.openCollaboration(h.editor);h.leave();release();await opened;
  assert.equal(h.views.length,0);assert.equal(h.calls.length,0);
});

test('approval status states permission, not invented execution progress; end uses that request',async()=>{
  const h=await harness();h.page({collaborations:[{handle:'request-handle',state:'approved',connection:{name:'Writer'},expires_at:'18:30',verification:'ABC'}]});
  await h.api.refreshCollaboration(h.editor);assert.equal(h.editor.collaboration.hidden,false);
  const row=h.editor.collaboration.children[0];assert.match(row.children[0].children[0],/可编辑.*18:30/);assert.doesNotMatch(row.children[0].children[0],/正在|完成/);
  h.page({collaborations:[]});await h.find(row,n=>n.label==='结束协助').action();
  assert.ok(h.calls.some(c=>c.action==='end_collaboration'&&c.handle==='request-handle'));assert.equal(h.editor.collaboration.hidden,true);
});
