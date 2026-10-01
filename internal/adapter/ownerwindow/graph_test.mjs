import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';

// Real graph controller and layout; only DOM geometry and HTTP are substituted.
// Browser acceptance covers pixels, pointer capture, pan, zoom and focus motion.
class Node {
  constructor(tag,attrs={},...children){this.tag=tag;this.attrs={...attrs};this.children=children.filter(v=>v!=null);this.handlers={};this.hidden=false;}
  setAttribute(k,v){this.attrs[k]=String(v);}
  getAttribute(k){return this.attrs[k]??null;}
  removeAttribute(k){delete this.attrs[k];}
  append(...values){this.children.push(...values);}
  replaceChildren(...values){this.children=values;}
  addEventListener(k,run){this.handlers[k]=run;}
  all(){return [this,...this.children.filter(n=>n instanceof Node).flatMap(n=>n.all())];}
  querySelectorAll(s){return this.all().slice(1).filter(n=>s.startsWith('.')?(n.attrs.class||'').split(' ').includes(s.slice(1)):n.tag===s);}
  querySelector(s){return this.querySelectorAll(s)[0];}
  focus(){}
  set textContent(value){this.children=[value];}
  get textContent(){return this.children.map(n=>n instanceof Node?n.textContent:String(n)).join('');}
}
const asset=id=>({reference:id,handle:id,state:'ready'});
const edge=(source,target,explanation)=>({source,target,type:'supports',meaning:explanation});
const tick=()=>new Promise(r=>setImmediate(r));
async function ready(graph){for(let i=0;i<200;i++){await tick();if(!graph.node.getAttribute('aria-busy'))return;}throw Error('graph did not settle');}
async function harness({assets=[asset('a'),asset('b')],relations={},override={},valid=()=>true,center=null,onOverview=()=>{},snapshot=null}={}){
  const calls=[],errors=[],opened=[];
  const api={scope:()=>()=>true,resolve:async(_,handle)=>({assets:[asset(handle.split(':')[0])]}),text:async(_,handle)=>JSON.stringify(handle),query:async q=>{
    calls.push(q);
    if(q.view==='content')return {text:{text:q.handle+'\nExcerpt',more:true,next_offset:4096}};
    if(q.view==='source')return {source:{actor:'Notes'}};
    if(q.view==='relations'){const list=relations[q.handle]||[],at=Number(q.after||0);return {relations:list.slice(at,at+1),next:at+1<list.length?String(at+1):''};}
    return {assets:[]};
  },...override};
  const ui={el:(...args)=>new Node(...args),button:(label,run,cls,attrs={})=>new Node('button',{class:cls,run,...attrs},label),row:(...args)=>new Node('div',{},...args),notice:v=>errors.push(v),statusName:v=>v};
  const context=vm.createContext({document:{createElementNS:(_,tag)=>new Node(tag)},ResizeObserver:class{observe(){}disconnect(){}},matchMedia:()=>({matches:true}),cancelAnimationFrame(){},queueMicrotask,performance});
  const module=async path=>new vm.SourceTextModule(await readFile(new URL(path,import.meta.url),'utf8'),{context});
  const layout=await module('./static/graph-layout.js');await layout.link(()=>{});await layout.evaluate();
  const graph=await module('./static/graph.js');
  await graph.link(spec=>{if(spec.includes('graph-layout'))return layout;const values=spec.includes('api')?api:ui;return new vm.SyntheticModule(Object.keys(values),function(){for(const[k,v]of Object.entries(values))this.setExport(k,v);},{context});});
  await graph.evaluate();const result=graph.namespace.createGraph({assets,valid,center,onOverview,snapshot,onOpen:(...v)=>opened.push(v)});await ready(result);
  return {graph:result,calls,errors,opened,layout:layout.namespace,button:label=>result.node.all().find(n=>n.tag==='button'&&n.textContent===label)};
}

test('opaque endpoint handles collapse to one document without fetching its full text',async()=>{
  const h=await harness({relations:{a:[edge('a:1','b:1','one')],b:[edge('a:2','b:2','one')]}});
  assert.equal(h.graph.node.querySelectorAll('.graph-node').length,2);
  assert.equal(h.graph.node.querySelectorAll('.graph-edge').length,1);
  assert.equal(h.calls.filter(q=>q.view==='content'&&q.offset).length,0);
  assert.equal(h.errors.length,0);h.graph.destroy();
});
test('the same supplied evidence is drawn once and input order cannot move the map',async()=>{
  const relation=edge('a','b','same reason'),h=await harness({relations:{a:[relation],b:[relation]}});
  assert.equal(h.graph.node.querySelectorAll('.edge-ink').length,1);
  const nodes=['a','b','c'].map(id=>({id})),edges=[{source:'a',target:'b'},{source:'b',target:'c'}];
  const first=h.layout.layoutGraph(nodes,edges),second=h.layout.layoutGraph([...nodes].reverse(),[...edges].reverse());
  for(const [id,p]of first){assert.equal(p.x,second.get(id).x);assert.equal(p.y,second.get(id).y);}h.graph.destroy();
});
test('different explanations for the same pair remain available; duplicate text is shown once',async()=>{
  const h=await harness({relations:{a:[edge('a','b','first reason'),edge('a','b','second reason')],b:[edge('a','b','first reason')]}});
  await h.graph.node.querySelector('.graph-edge').handlers.keydown({key:'Enter',preventDefault(){}});await tick();await tick();
  const panel=h.graph.node.querySelector('.graph-inspector').textContent;
  assert.ok(panel.includes('first reason'));assert.ok(panel.includes('second reason'));
  assert.equal(panel.split('first reason').length,2);h.graph.destroy();
});
test('late evidence cannot repopulate a closed graph',async()=>{
  let resume;const waiting=new Promise(r=>resume=r);
  const h=await harness({relations:{a:[edge('a','b','private')]},override:{text:()=>waiting}});
  h.graph.node.querySelector('.graph-edge').handlers.keydown({key:'Enter',preventDefault(){}});
  h.graph.destroy();resume(JSON.stringify('private explanation'));await tick();
  assert.equal(h.graph.node.querySelector('.graph-inspector').textContent,'');
  assert.equal(h.graph.node.querySelectorAll('.graph-node').length,0);
});
test('a neighborhood response does not replace an explanation selected while it was loading',async()=>{
  let resume;const waiting=new Promise(r=>resume=r),links=Array.from({length:5},()=>edge('a','b','selected explanation'));
  const h=await harness({override:{query:async q=>{
    if(q.view==='content')return {text:{text:q.handle+'\nExcerpt'}};
    if(q.view==='source')return {source:{actor:'Notes'}};
    if(q.handle==='b')return {relations:[]};
    if(q.after==='3')await waiting;
    const at=Number(q.after||0);return {relations:links.slice(at,at+1),next:at+1<links.length?String(at+1):''};
  }}});
  h.graph.node.querySelector('.graph-node').handlers.keydown({key:'Enter',preventDefault(){}});
  h.graph.node.querySelector('.graph-edge').handlers.keydown({key:'Enter',preventDefault(){}});
  await tick();resume();await ready(h.graph);
  const panel=h.graph.node.querySelector('.graph-inspector').textContent;
  assert.ok(panel.includes('selected explanation'));assert.ok(!panel.includes('所选资料'));h.graph.destroy();
});
test('overview work stays bounded and an unfinished neighborhood has an explicit continuation',async()=>{
  const links=Array.from({length:100},(_,i)=>edge('a','neighbor'+i,'why'+i));
  const h=await harness({assets:[asset('a')],relations:{a:links}});
  assert.equal(h.calls.filter(q=>q.view==='relations').length,3);
  assert.match(h.graph.node.textContent,/部分关联尚未展开/);
  h.graph.node.querySelector('.graph-node').handlers.keydown({key:'Enter',preventDefault(){}});await ready(h.graph);
  assert.equal(h.calls.filter(q=>q.view==='relations').length,7);
  assert.equal(h.button('展开更多关联').hidden,false);h.graph.destroy();
});
test('zoom stops at its limit without drifting the camera',async()=>{
  const h=await harness();
  for(let i=0;i<30;i++)h.button('+').attrs.run();const before=h.graph.capture().camera;
  h.button('+').attrs.run();assert.deepEqual(h.graph.capture().camera,before);
  assert.equal(before.k,3);h.graph.destroy();
});
test('a graph opened from a document returns to the library overview, not its one-node seed',async()=>{
  let opened=0;const h=await harness({assets:[asset('a')],center:asset('a'),onOverview:()=>opened++});
  h.button('返回概览').attrs.run();h.button('查看资料概览').attrs.run();assert.equal(opened,2);h.graph.destroy();
});
test('layout distinguishes connected components and fits finite coordinates, including empty graphs',async()=>{
  const h=await harness();const p=h.layout.layoutGraph([{id:'a'},{id:'b'},{id:'c'}],[{source:'a',target:'b'}]);
  assert.equal(p.get('a').group,p.get('b').group);assert.notEqual(p.get('a').group,p.get('c').group);
  for(const size of [[1000,660],[350,440]])for(const point of [p,new Map()])assert.ok(Object.values(h.layout.fitGraph(point,...size)).every(Number.isFinite));
  h.graph.destroy();
});

test('new relationships replace stale saved positions; unchanged selection and return preserve the space',async()=>{
  const first=await harness(),old=first.graph.capture();first.graph.destroy();
  const h=await harness({snapshot:old,relations:{a:[edge('a','b','new connection')]}});
  const organized=h.graph.capture(),before=new Map(old.points),after=new Map(organized.points);
  assert.notEqual(organized.topology,old.topology);
  assert.ok([...after].some(([id,p])=>Math.hypot(p.x-before.get(id).x,p.y-before.get(id).y)>10));
  h.graph.node.querySelector('.graph-node').handlers.keydown({key:'Enter',preventDefault(){}});await ready(h.graph);
  assert.deepEqual(h.graph.capture().points,organized.points,'selection must not rerun the layout');
  const snapshot=h.graph.capture();h.graph.destroy();
  const restored=await harness({snapshot,relations:{a:[edge('a','b','new connection')]}});
  assert.deepEqual(JSON.parse(JSON.stringify(restored.graph.capture().points)),JSON.parse(JSON.stringify(snapshot.points)));
  assert.deepEqual(JSON.parse(JSON.stringify(restored.graph.capture().camera)),JSON.parse(JSON.stringify(snapshot.camera)));
  restored.graph.destroy();
});

test('labels keep readable non-overlapping boxes and prioritize the current target',async()=>{
  const h=await harness(),items=[{id:'selected',x:190,y:100,width:150,priority:1000},{id:'b',x:240,y:105,width:150,priority:0},{id:'c',x:80,y:180,width:130,priority:0}];
  const placed=h.layout.placeLabels(items,390,300),boxes=[...placed.values()].map(p=>p.box);
  assert.ok(placed.has('selected'));
  for(let i=0;i<boxes.length;i++){const a=boxes[i];assert.ok(a.left>=8&&a.right<=382&&a.top>=8&&a.bottom<=292);for(let j=i+1;j<boxes.length;j++){const b=boxes[j];assert.ok(!(a.left<b.right&&a.right>b.left&&a.top<b.bottom&&a.bottom>b.top));}}
  h.graph.destroy();
});

test('representative topologies remain finite, separated and independent of repeated evidence',async()=>{
  const h=await harness(),nodes=Array.from({length:18},(_,i)=>({id:String(i).padStart(2,'0')}));
  const chain=nodes.slice(1).map((n,i)=>({source:nodes[i].id,target:n.id})),star=nodes.slice(1).map(n=>({source:'00',target:n.id})),cycle=[...chain,{source:'17',target:'00'}];
  for(const links of [[],chain,star,cycle]){
    const points=h.layout.layoutGraph(nodes,links),values=[...points.values()];
    assert.ok(values.every(p=>Number.isFinite(p.x)&&Number.isFinite(p.y)));
    for(let i=0;i<values.length;i++)for(let j=i+1;j<values.length;j++)assert.ok(Math.hypot(values[i].x-values[j].x,values[i].y-values[j].y)>70,'nodes need space for labels and picking');
    assert.equal(h.layout.graphSignature(nodes,links),h.layout.graphSignature([...nodes].reverse(),[...links,...links].reverse()));
    assert.deepEqual(h.layout.layoutGraph(nodes,links),h.layout.layoutGraph(nodes,[...links,...links]));
  }
  h.graph.destroy();
});

test('opposite directions, relationship types and self links have separate visible paths',async()=>{
  const h=await harness({relations:{a:[edge('a','b','forward'),edge('b','a','back'),{...edge('a','b','different type'),type:'contrasts'}],b:[edge('a','a','self')]}});
  const paths=h.graph.node.querySelectorAll('.edge-ink').map(p=>p.getAttribute('d'));
  assert.equal(new Set(paths).size,paths.length);
  assert.equal(new Set(paths.filter(p=>p.includes(' Q')).map(p=>p.split(' Q')[1].split(' ')[0])).size,3,'opposite directions must not share the same geometric curve');
  assert.ok(paths.some(p=>p.includes(' C')),'a self link needs a visible loop');
  h.graph.destroy();
});

test('returning to the graph restores the selected relationship and its explanation',async()=>{
  const relations={a:[edge('a','b','preserved explanation')]},first=await harness({relations});
  await first.graph.node.querySelector('.graph-edge').handlers.keydown({key:'Enter',preventDefault(){}});await tick();await tick();
  const snapshot=first.graph.capture();first.graph.destroy();
  const restored=await harness({relations,snapshot});
  assert.match(restored.graph.node.querySelector('.graph-inspector').textContent,/preserved explanation/);
  assert.equal(restored.graph.node.querySelector('.graph-edge').getAttribute('data-selected'),'true');restored.graph.destroy();
});

test('selecting a node keeps it above an overlapping details drawer',async()=>{
  const h=await harness(),view=h.graph.node.querySelector('.graph-canvas'),panel=h.graph.node.querySelector('.graph-inspector');
  view.getBoundingClientRect=()=>({left:0,right:1000,top:100,bottom:760,height:660});
  panel.getBoundingClientRect=()=>({left:20,right:980,top:350,bottom:760,height:410});
  const target=h.graph.node.querySelector('.graph-node'),id=target.getAttribute('data-node');
  await target.handlers.keydown({key:'Enter',preventDefault(){}});await ready(h.graph);
  const state=h.graph.capture(),point=new Map(state.points).get(id),screenY=100+point.y*state.camera.k+state.camera.y;
  assert.ok(screenY<=305&&screenY>=145,'the selected node must stay inside the uncovered canvas');
  h.graph.destroy();
});
