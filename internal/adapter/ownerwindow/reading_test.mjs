import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';

// Actual reader and shared renderer. Only DOM geometry and transport are fake.
class Node {
  constructor(tag){this.tag=tag;this.children=[];this.attrs={};this.handlers={};this.hidden=false;this.value='';this.scrollTop=0;this.scrollHeight=2000;this.clientHeight=600;this.className='';this.style={setProperty(){}};
    this.classList={contains:k=>this.className.split(' ').includes(k),toggle:(k,on)=>{on??=!this.classList.contains(k);this.className=this.className.split(' ').filter(c=>c!==k).concat(on?[k]:[]).join(' ');return on;},add:k=>this.classList.toggle(k,true),remove:k=>this.classList.toggle(k,false)};
  }
  setAttribute(k,v){this.attrs[k]=String(v);}
  removeAttribute(k){delete this.attrs[k];}
  append(...v){this.children.push(...v);}
  prepend(...v){this.children.unshift(...v);}
  replaceChildren(...v){this.children=v;}
  addEventListener(k,v){this.handlers[k]=v;}
  all(){return [this,...this.children.filter(n=>n instanceof Node).flatMap(n=>n.all())];}
  querySelectorAll(s){return this.all().slice(1).filter(n=>s.startsWith('.')?n.classList.contains(s.slice(1)):n.tag===s);}
  querySelector(s){return this.querySelectorAll(s)[0]||null;}
  contains(n){return this.all().includes(n);}
  focus(){}
  scrollIntoView(){this.scrolled=true;}
  click(){return this.handlers.click?.({currentTarget:this});}
  set textContent(v){this.children=[v];}
  get textContent(){return this.children.map(n=>n instanceof Node?n.textContent:String(n??'')).join('');}
}
const tick=()=>new Promise(r=>setImmediate(r));
const deferred=()=>{let resolve;return {promise:new Promise(r=>resolve=r),resolve:v=>resolve(v)};};
async function harness({query=async()=>({text:{text:' second needle',more:false}}),text=async()=>'{"source":{"actor":"Original source"}}',first={text:{text:'Title\n\nfirst needle',more:true,next_offset:19}},...options}={}){
  const nodes=new Map(),calls=[],document={createElement:tag=>new Node(tag),querySelector:()=>null,querySelectorAll:()=>[],getElementById:id=>{if(!nodes.has(id))nodes.set(id,new Node('div'));return nodes.get(id);}};
  const context=vm.createContext({document,window:{getSelection:()=>null},queueMicrotask,console,Date,Intl});
  const ui=new vm.SourceTextModule(await readFile(new URL('./static/ui.js',import.meta.url),'utf8'),{context});await ui.link(()=>{});await ui.evaluate();
  const api=new vm.SyntheticModule(['query','text'],function(){this.setExport('query',async q=>{calls.push(q);return query(q);});this.setExport('text',text);},{context});
  const reader=new vm.SourceTextModule(await readFile(new URL('./static/reading.js',import.meta.url),'utf8'),{context});await reader.link(s=>s.includes('api')?api:ui);await reader.evaluate();
  const result=reader.namespace.createReader({asset:{handle:'a',has_original:true,state:'ready'},first,source:{actor:'Current source'},valid:()=>true,onBack(){},onEdit(){},onRelations(){},onForget(){},...options});
  return {reader:result,ui:ui.namespace,calls,node:result.node,button:label=>result.node.all().find(n=>n.tag==='button'&&n.textContent===label)};
}
test('find counts only loaded text and an explicit next match reads just one page',async()=>{
  const h=await harness();await tick();assert.equal(h.calls.length,0);
  const find=h.node.querySelector('input');find.value='needle';find.handlers.input();
  assert.match(h.node.querySelector('.find-count').textContent,/已加载内容找到 1 处/);assert.equal(h.calls.length,0);
  await h.button('下一处').click();assert.equal(h.calls.length,1);assert.equal(h.calls[0].offset,19);
  assert.match(h.node.querySelector('.find-count').textContent,/本文 2 处/);assert.equal(h.node.querySelector('.current-match').textContent,'needle');
  assert.equal(h.reader.search().value,'Title\n\nfirst needle second needle');
});
test('failed automatic continuation waits for explicit retry and then resumes',async()=>{
  let fail=true;const h=await harness({query:async()=>{if(fail)throw Error('offline');return {text:{text:' end',more:false}};}});
  h.node.scrollTop=1300;h.node.handlers.scroll();await tick();assert.equal(h.calls.length,1);
  h.node.handlers.scroll();await tick();assert.equal(h.calls.length,1);assert.equal(h.button('读取未完成 · 重试').hidden,false);
  fail=false;await h.button('读取未完成 · 重试').click();assert.equal(h.calls.length,2);assert.equal(h.reader.search().more,false);
});
test('changed or destroyed documents reject an in-flight continuation',async()=>{
  for(const action of ['markUpdated','destroy']){const gate=deferred(),h=await harness({query:()=>gate.promise});const reading=h.reader.next(true);h.reader[action]();gate.resolve({text:{text:'stale arrival',more:false}});await reading;
    assert.equal(h.node.querySelector('.reader-body').textContent.includes('stale arrival'),false);
    if(action==='markUpdated'){assert.equal(h.button('编辑').disabled,true);assert.equal(h.button('这份资料已更新 · 重新打开').hidden,false);}else assert.equal(h.reader.search().value,'');
  }
});
test('original switch isolates current continuation and labels historical evidence',async()=>{
  const gate=deferred(),h=await harness({query:q=>q.view==='original'?Promise.resolve({text:{text:'Historical words',more:false}}):gate.promise});
  const late=h.reader.next(true);await h.button('原件').click();gate.resolve({text:{text:'late current words',more:false}});await late;
  assert.equal(h.reader.search().value,'Historical words');assert.equal(h.node.querySelector('.original-note').hidden,false);assert.ok(h.node.textContent.includes('Original source'));
});
test('a broken continuation cursor cannot duplicate text or trigger a retry loop',async()=>{
  const h=await harness({query:async()=>({text:{text:'duplicate',more:true,next_offset:19}})});await h.reader.next(true);assert.equal(h.reader.search().value.includes('duplicate'),false);await h.reader.next();assert.equal(h.calls.length,1);
});
test('reader and confirmation share literal text; comparison preserves Unicode and mobile labels',async()=>{
  const h=await harness();const before='A😀B\n<unsafe>',after='A😁B\n<unsafe>',view=h.ui.documentView(before);
  assert.equal(view.textContent,before);const compare=h.ui.comparison(before,after),columns=compare.querySelector('.comparison-columns');
  const texts=columns.querySelectorAll('pre');assert.equal(texts[0].textContent,before);assert.equal(texts[1].textContent,after);
  assert.equal(texts[0].querySelector('.difference').textContent,'😀');assert.equal(texts[1].querySelector('.difference').textContent,'😁');
  const tabs=compare.querySelectorAll('.compare-tab');assert.deepEqual(tabs.map(n=>n.textContent),['现在','将成为']);await tabs[0].click();assert.equal(compare.attrs['data-side'],'before');
});
test('a search ending at the title boundary never inserts display text',async()=>{
  const h=await harness({first:{text:{text:'Title\n\nActual words',more:false}}});const input=h.node.querySelector('input');input.value='Title';input.handlers.input();
  assert.equal(h.node.querySelector('.reader-body').textContent,'Title\n\nActual words');assert.equal(h.node.querySelectorAll('.match').length,1);
});
test('deep position recovery is bounded and leaves an explicit continuation',async()=>{
  let offset=19;const h=await harness({position:50000,query:async()=>({text:{text:' More text',more:true,next_offset:++offset}})});await tick();
  assert.equal(h.calls.length,4);assert.equal(h.button('继续到上次阅读的位置').hidden,false);
  await h.button('继续到上次阅读的位置').click();assert.equal(h.calls.length,8);h.reader.destroy();
});
test('separate edits have separate anchors and every indicated replacement reconstructs the result',async()=>{
  const h=await harness(),before='Title\n\nfirst old\nunchanged bridge\nlast old',after='Title\n\nfirst new\nunchanged bridge\nlast new';
  assert.equal(h.ui.differences(before,after).length,2);assert.equal(h.ui.comparison(before,after).querySelector('.change-navigation').querySelectorAll('button').length,2);
  let seed=73;const random=n=>{seed=(seed*1664525+1013904223)>>>0;return seed%n;},alphabet=['a','b','🙂','\n','中文'];
  for(let sample=0;sample<150;sample++){const a=Array.from({length:random(40)},()=>alphabet[random(alphabet.length)]).join(''),b=Array.from({length:random(40)},()=>alphabet[random(alphabet.length)]).join('');let result=a;
    for(const change of [...h.ui.differences(a,b)].reverse())result=result.slice(0,change.before.start)+b.slice(change.after.start,change.after.end)+result.slice(change.before.end);
    assert.equal(result,b);
  }
});
