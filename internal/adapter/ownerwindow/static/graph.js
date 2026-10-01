import {query,resolve,text,scope} from './api.js';
import {el,button,row,notice,statusName} from './ui.js';
import {layoutGraph,fitGraph,graphSignature,placeLabels} from './graph-layout.js';

const NS='http://www.w3.org/2000/svg',MAX_NODES=48,MAX_EDGES=72;
const titleOf=value=>value.trim().split(/\r?\n/)[0].slice(0,100)||'未命名资料';
const meaning=value=>typeof value==='string'?value:Array.isArray(value)?value.map(meaning).filter(Boolean).join('\n'):value&&typeof value==='object'?['description','reason','rationale','explanation','meaning','claim','summary','text','context','condition','label','title'].map(k=>meaning(value[k])).filter(Boolean).join('\n'):'';
const svg=(name,attrs={},...children)=>{const n=document.createElementNS(NS,name);for(const [k,v] of Object.entries(attrs))n.setAttribute(k,v);n.append(...children);return n;};

export function createGraph({assets=[],next='',organization='available',center=null,relations=null,valid,onOpen,onOverview,snapshot=null,selectionQuote=''}){
  let alive=true,busy=false,serial=0,raf=0,selected=null,focused=false,points=new Map(),camera={x:500,y:330,k:1},drag=null,width=1000,height=660,loaded=false,suppressClick=false,pendingSelect=null,selectedEdge=null,hovered=null,hoveredEdge=null,topology='',layoutPoints=new Map(),autoFit=!snapshot;
  const retainedPoints=new Map(snapshot?.points||[]),labelSlots=new Map(),labelWidths=new Map();
  let layoutMode=snapshot?.layoutMode||'landscape';
  const currentMode=()=>width<560?'portrait':'landscape';
  const measure=document.createElement?.('canvas')?.getContext?.('2d');
  if(snapshot?.topology){topology=snapshot.topology;layoutPoints=new Map(snapshot.points||[]);}
  const current=scope(),ok=()=>alive&&current()&&valid(),nodes=new Map(),edges=new Map(),handles=new Map(),reads=new Map(),pages=new Map();
  const reduced=matchMedia('(prefers-reduced-motion: reduce)'),details=el('aside',{class:'graph-inspector','aria-label':'所选资料与关联'});
  const status=el('span',{class:'graph-status',role:'status'},'正在读取关联…');
  const viewport=svg('svg',{viewBox:'0 0 1000 660',class:'graph-canvas',tabindex:'0','aria-label':'资料关联图，可用方向键移动，加减键缩放，Home 适应画面'}),world=svg('g'),edgeLayer=svg('g'),nodeLayer=svg('g');world.append(edgeLayer,nodeLayer);viewport.append(world);
  const zoomLabel=el('span',{class:'zoom-label'},'100%');
  const fit=()=>{autoFit=true;move(fitGraph(points,width,height));};
  const zoom=factor=>{autoFit=false;const k=Math.min(3,Math.max(.2,camera.k*factor)),ratio=k/camera.k;move({k,x:width/2+(camera.x-width/2)*ratio,y:height/2+(camera.y-height/2)*ratio});};
  const clearSelection=()=>{if(center)return onOverview();serial++;const wasFocused=focused;focused=false;selected=null;selectedEdge=null;draw();inspect();if(wasFocused)fit();};
  const back=button('返回概览',clearSelection,'graph-back');back.hidden=true;
  const more=button('显示更多资料',()=>loadMore(),'quiet');more.hidden=!next;
  const scene=el('div',{class:'graph-scene'},viewport,el('div',{class:'graph-corner'},back),el('div',{class:'graph-controls'},button('−',()=>zoom(1/1.2),'icon',{'aria-label':'缩小'}),zoomLabel,button('+',()=>zoom(1.2),'icon',{'aria-label':'放大'}),button('适应画面',fit,'quiet')),el('div',{class:'graph-legend'},el('span',{class:'legend-dot'}),'已整理',el('span',{class:'legend-dot pending'}),'待整理',el('span',{class:'gesture-hint'},'拖动平移 · 滚轮缩放')));
  const node=el('div',{class:'graph-explorer'},el('div',{class:'graph-workspace'},scene,details),el('div',{class:'graph-foot'},status,more));
  const touch=new Map();let pinch=null,velocity={x:0,y:0},motionPoint=null,motionAt=0;
  function coordinates(event){const p=new DOMPoint(event.clientX,event.clientY).matrixTransform(viewport.getScreenCTM().inverse());return {x:p.x,y:p.y};}
  function attention() {
    const edge=edges.get(hoveredEdge||selectedEdge),target=hovered||selected;
    const ids=new Set(edge?[edge.source,edge.target]:target?[target]:[]);
    if(!edge&&target)for(const e of edges.values())if(e.source===target||e.target===target){ids.add(e.source);ids.add(e.target);}
    return {ids,edge,target};
  }
  function updateAttention() {
    const {ids,edge,target}=attention();
    for(const g of nodeLayer.children){const id=g.getAttribute('data-node');g.setAttribute('data-emphasis',!ids.size?'normal':ids.has(id)?'near':'far');g.setAttribute('data-hovered',String(id===hovered));}
    for(const g of edgeLayer.children){const e=edges.get(g.getAttribute('data-edge')),near=edge?e.id===edge.id:e.source===target||e.target===target;g.setAttribute('data-emphasis',!ids.size?'normal':near?'near':'far');g.setAttribute('data-selected',String(e.id===selectedEdge));}
  }
  function applyCamera(){
    world.setAttribute('transform',`translate(${camera.x} ${camera.y}) scale(${camera.k})`);zoomLabel.textContent=Math.round(camera.k*100)+'%';
    const {ids}=attention(),items=[];
    for(const group of nodeLayer.children){
      const id=group.getAttribute('data-node'),p=points.get(id),label=group.querySelector('text');if(!p)continue;
      label.setAttribute('font-size',13/camera.k);label.setAttribute('stroke-width',4/camera.k);
      for(const circle of group.querySelectorAll('circle'))circle.setAttribute('r',Number(circle.getAttribute('data-radius'))/camera.k);
      items.push({id,x:p.x*camera.k+camera.x,y:p.y*camera.k+camera.y,width:labelWidths.get(id)||130,priority:id===selected?1000:id===hovered?900:ids.has(id)?100:0});
    }
    const placements=placeLabels(items,width,height,labelSlots);
    for(const group of nodeLayer.children){
      const id=group.getAttribute('data-node'),label=group.querySelector('text'),slot=placements.get(id);
      label.setAttribute('visibility',slot?'visible':'hidden');if(!slot)continue;
      labelSlots.set(id,slot.slot);label.setAttribute('x',slot.dx/camera.k);label.setAttribute('y',slot.dy/camera.k);label.setAttribute('text-anchor',slot.anchor);
    }
  }
  function reveal(id){
    const p=points.get(id);if(!p)return;const x=p.x*camera.k+camera.x,y=p.y*camera.k+camera.y;
    const view=viewport.getBoundingClientRect?.(),panel=details.getBoundingClientRect?.();let bottom=height;
    if(view?.height&&panel?.height&&panel.left<view.right&&panel.right>view.left&&panel.top>view.top&&panel.top<view.bottom)bottom=(panel.top-view.top)*height/view.height;
    const inset=Math.min(105,width/3),dx=x<inset?inset-x:x>width-inset?width-inset-x:0,dy=y<45?45-y:y>bottom-45?Math.max(45,bottom-45)-y:0;
    if(dx||dy){autoFit=false;move({...camera,x:camera.x+dx,y:camera.y+dy});}
  }
  const resize=new ResizeObserver(entries=>{
    const rect=entries[0].contentRect;if(!rect.width||!rect.height||!ok())return;
    const dx=(rect.width-width)/2,dy=(rect.height-height)/2;width=rect.width;height=rect.height;viewport.setAttribute('viewBox',`0 0 ${width} ${height}`);
    cancelAnimationFrame(raf);if(loaded&&autoFit&&layoutMode!==currentMode())draw();
    camera=loaded&&autoFit?fitGraph(points,width,height):{...camera,x:camera.x+dx,y:camera.y+dy};applyCamera();
  });resize.observe(viewport);
  function move(target){
    cancelAnimationFrame(raf);if(reduced.matches){camera={...target};applyCamera();return;}
    const start={...camera},began=performance.now();
    const frame=now=>{if(!ok())return;const t=Math.min(1,(now-began)/420),e=(1-(1+8*t)*Math.exp(-8*t))/(1-9*Math.exp(-8));camera={x:start.x+(target.x-start.x)*e,y:start.y+(target.y-start.y)*e,k:start.k+(target.k-start.k)*e};applyCamera();if(t<1)raf=requestAnimationFrame(frame);};raf=requestAnimationFrame(frame);
  }
  viewport.addEventListener('wheel',event=>{event.preventDefault();autoFit=false;cancelAnimationFrame(raf);const p=coordinates(event),k=Math.min(3,Math.max(.2,camera.k*Math.exp(-event.deltaY*.0015))),ratio=k/camera.k;camera={k,x:p.x-(p.x-camera.x)*ratio,y:p.y-(p.y-camera.y)*ratio};applyCamera();},{passive:false});
  viewport.addEventListener('pointerdown',event=>{if(event.button!==0)return;const p=coordinates(event);velocity={x:0,y:0};motionPoint=p;motionAt=performance.now();touch.set(event.pointerId,p);viewport.setPointerCapture(event.pointerId);cancelAnimationFrame(raf);suppressClick=false;const target=event.target.closest('[data-node]'),edge=event.target.closest('[data-edge]');drag={p,origin:{...camera},id:target?.getAttribute('data-node'),edge:edge?.getAttribute('data-edge'),moved:false};if(touch.size===2){const [a,b]=[...touch.values()];pinch={distance:Math.max(1,Math.hypot(a.x-b.x,a.y-b.y)),camera:{...camera},middle:{x:(a.x+b.x)/2,y:(a.y+b.y)/2}};}});
  viewport.addEventListener('pointermove',event=>{if(!touch.has(event.pointerId))return;const p=coordinates(event);const now=performance.now(),dt=Math.max(8,now-motionAt);velocity={x:(p.x-motionPoint.x)/dt,y:(p.y-motionPoint.y)/dt};motionPoint=p;motionAt=now;touch.set(event.pointerId,p);if(pinch&&touch.size===2){const [a,b]=[...touch.values()],k=Math.min(3,Math.max(.2,pinch.camera.k*Math.hypot(a.x-b.x,a.y-b.y)/pinch.distance)),r=k/pinch.camera.k;camera={k,x:(a.x+b.x)/2-(pinch.middle.x-pinch.camera.x)*r,y:(a.y+b.y)/2-(pinch.middle.y-pinch.camera.y)*r};drag.moved=true;autoFit=false;applyCamera();return;}if(!drag)return;const dx=p.x-drag.p.x,dy=p.y-drag.p.y;if(Math.hypot(dx,dy)>4)drag.moved=true;if(drag.moved){autoFit=false;camera={...drag.origin,x:drag.origin.x+dx,y:drag.origin.y+dy};applyCamera();}});
  const endPointer=event=>{touch.delete(event.pointerId);suppressClick=!!drag?.moved;if(drag&&!drag.moved&&event.type==='pointerup'){if(drag.id)select(drag.id);else if(drag.edge)inspectEdge(edges.get(drag.edge));suppressClick=!!(drag.id||drag.edge);}const glide=drag?.moved&&!pinch&&event.type==='pointerup'&&!reduced.matches&&performance.now()-motionAt<70;drag=null;pinch=null;if(glide){const start=performance.now();let last=start;const frame=now=>{if(!ok())return;const dt=Math.min(32,now-last);last=now;velocity.x*=Math.exp(-dt/70);velocity.y*=Math.exp(-dt/70);camera.x+=velocity.x*dt;camera.y+=velocity.y*dt;applyCamera();if(now-start<420&&Math.hypot(velocity.x,velocity.y)>.015)raf=requestAnimationFrame(frame);};raf=requestAnimationFrame(frame);}};viewport.addEventListener('pointerup',endPointer);viewport.addEventListener('pointercancel',endPointer);
  viewport.addEventListener('click',event=>{if(suppressClick){suppressClick=false;return;}const target=event.target.closest('[data-node]');if(target)select(target.getAttribute('data-node'));});
  viewport.addEventListener('keydown',event=>{
    if(event.key==='Escape'){event.preventDefault();clearSelection();return;}
    if(event.target!==viewport)return;
    if(event.key==='Home'){event.preventDefault();fit();return;}
    if(event.key==='+'||event.key==='='){event.preventDefault();zoom(1.2);return;}
    if(event.key==='-'){event.preventDefault();zoom(1/1.2);return;}
    const delta={ArrowLeft:[65,0],ArrowRight:[-65,0],ArrowUp:[0,65],ArrowDown:[0,-65]}[event.key];
    if(delta){event.preventDefault();autoFit=false;move({...camera,x:camera.x+delta[0],y:camera.y+delta[1]});}
  });
  function hover(event){
    if(drag)return;const n=event.target.closest?.('[data-node]'),e=event.target.closest?.('[data-edge]');
    hovered=n?.getAttribute('data-node')||null;hoveredEdge=e?.getAttribute('data-edge')||null;updateAttention();applyCamera();
  }
  viewport.addEventListener('pointerover',hover);
  viewport.addEventListener('pointerleave',()=>{hovered=null;hoveredEdge=null;updateAttention();applyCamera();});
  viewport.addEventListener('focusin',event=>{hover(event);if(hovered)reveal(hovered);});
  viewport.addEventListener('focusout',()=>{hovered=null;hoveredEdge=null;updateAttention();applyCamera();});
  function addAsset(asset){
    if(!asset||asset.state==='stopped')return null;
    if(nodes.has(asset.reference)){handles.set(asset.handle,asset.reference);return nodes.get(asset.reference);}
    if(nodes.size>=MAX_NODES)return null;
    const n={...asset,id:asset.reference,title:'正在读取…',excerpt:'',source:''};nodes.set(n.id,n);handles.set(n.handle,n.id);return n;
  }
  async function hydrate(n){if(!n||n.loaded)return;if(n.loading)return n.loading;n.loading=(async()=>{const [body,source]=await Promise.all([query({view:'content',handle:n.handle}),query({view:'source',handle:n.handle})]);if(!ok())return;n.title=titleOf(body.text.text);n.excerpt=body.text.text.trim().split(/\r?\n/).slice(1).join('\n').trim().slice(0,280);n.authored=!!source.source?.authored;n.source=source.source?.authored?'我创建的':source.source?.actor||source.source?.ref||'未注明来源';n.loaded=true;})();try{await n.loading;}finally{n.loading=null;}}
  async function endpoint(handle){if(handles.has(handle))return nodes.get(handles.get(handle));if(reads.has(handle))return reads.get(handle);const p=(async()=>{const result=await resolve(undefined,handle);if(!ok()||result.unavailable)return null;const n=addAsset(result.assets[0]);if(n){handles.set(handle,n.id);await hydrate(n);}return n;})();reads.set(handle,p);return p;}
  async function absorb(page){
    for(const relation of page.relations||[]){if(!ok()||edges.size>=MAX_EDGES)return;const [a,b]=await Promise.all([endpoint(relation.source),endpoint(relation.target)]);if(!ok())return;if(!a||!b)continue;
      const key=[a.id,b.id,relation.type].join('\u0000');
      if(!edges.has(key)){if(edges.size>=MAX_EDGES)return;edges.set(key,{source:a.id,target:b.id,id:key,variants:[]});}
      const variants=edges.get(key).variants;
      if(!variants.some(v=>v.meaning===relation.meaning&&v.evidence===relation.evidence&&JSON.stringify(v.basis)===JSON.stringify(relation.basis)))variants.push(relation);
    }
  }
  async function expand(id,count=4,initial=null){
    const n=nodes.get(id);if(!n)return;let after=pages.get(id);if(after===null)return;
    for(let i=0;i<count;i++){const page=i===0&&initial?initial:await query({view:'relations',handle:n.handle,after:after||'',limit:12});if(!ok())return;await absorb(page);if(!ok())return;after=page.next||null;pages.set(id,after);if(!after||nodes.size>=MAX_NODES||edges.size>=MAX_EDGES)break;}
  }
  function visibleGraph(){const all=[...nodes.values()],allEdges=[...edges.values()];if(!focused||!selected)return {nodes:all,edges:allEdges};const ids=new Set([selected]);for(const e of allEdges)if(e.source===selected||e.target===selected){ids.add(e.source);ids.add(e.target);}return {nodes:all.filter(n=>ids.has(n.id)),edges:allEdges.filter(e=>ids.has(e.source)&&ids.has(e.target))};}
  function draw(){
    if(!ok())return;const active=document.activeElement?.getAttribute('data-node'),shown=visibleGraph();
    const signature=graphSignature([...nodes.values()],[...edges.values()]);
    if(signature!==topology||layoutMode!==currentMode()){
      const anchor=layoutPoints.get(selected),nextPoints=layoutGraph([...nodes.values()],[...edges.values()],retainedPoints,{aspect:width<560?.55:1.35,repack:layoutMode!==currentMode()});
      if(anchor&&nextPoints.has(selected)){const next=nextPoints.get(selected);camera.x+=(anchor.x-next.x)*camera.k;camera.y+=(anchor.y-next.y)*camera.k;}
      layoutMode=currentMode();layoutPoints=nextPoints;topology=signature;retainedPoints.clear();for(const [id,p]of layoutPoints)retainedPoints.set(id,{...p});
    }
    points=new Map(shown.nodes.map(n=>[n.id,layoutPoints.get(n.id)]));edgeLayer.replaceChildren();nodeLayer.replaceChildren();
    let spareLines=MAX_EDGES-shown.edges.length;const routes=new Map(),pairs=new Map();
    for(const e of [...shown.edges].sort((a,b)=>a.id.localeCompare(b.id))){
      const key=JSON.stringify([e.source,e.target].sort()),paths=Math.max(1,Math.min(1+spareLines,e.variants.length));spareLines-=paths-1;
      const pair=pairs.get(key)||{count:0};pairs.set(key,pair);routes.set(e.id,{offset:pair.count,paths,pair});pair.count+=paths;
    }
    for(const e of shown.edges){const a=points.get(e.source),b=points.get(e.target),g=svg('g',{'data-edge':e.id,class:'graph-edge',tabindex:'0',role:'button','aria-label':`${nodes.get(e.source).title} 与 ${nodes.get(e.target).title}的关联依据`});
      const dx=b.x-a.x,dy=b.y-a.y,d=Math.max(1,Math.hypot(dx,dy)),route=routes.get(e.id);
      for(let i=0;i<route.paths;i++){
        const lane=route.offset+i,bend=(lane-(route.pair.count-1)/2)*20*(e.source<e.target?1:-1),loop=45+lane*14;
        const path=e.source===e.target?`M${a.x},${a.y} C${a.x-loop},${a.y-loop*1.6} ${a.x+loop},${a.y-loop*1.6} ${a.x},${a.y}`:`M${a.x},${a.y} Q${(a.x+b.x)/2-dy/d*bend},${(a.y+b.y)/2+dx/d*bend} ${b.x},${b.y}`;
        g.append(svg('path',{d:path,class:'edge-ink','stroke-width':1.2,fill:'none'}),svg('path',{d:path,class:'edge-hit',fill:'none'}));
      }
      g.addEventListener('click',()=>{if(!suppressClick)inspectEdge(e);});g.addEventListener('keydown',event=>{if(event.key==='Enter'||event.key===' '){event.preventDefault();inspectEdge(e);}});edgeLayer.append(g);
    }
    if(measure)measure.font='13px '+getComputedStyle(viewport).fontFamily;
    for(const n of shown.nodes){const p=points.get(n.id),degree=shown.edges.filter(e=>e.source===n.id||e.target===n.id).length;
      const g=svg('g',{class:'graph-node'+(n.id===selected?' selected':'')+(n.state==='pending'?' pending':''),'data-node':n.id,transform:`translate(${p.x} ${p.y})`,tabindex:'0',role:'button','aria-label':`${n.title}，${statusName(n.state)}`,'aria-pressed':String(n.id===selected)});
      g.append(svg('circle',{'data-radius':22,class:'node-hit'}),svg('circle',{'data-radius':11+Math.min(2,degree*.25),class:'node-halo'}),svg('circle',{'data-radius':4+Math.min(3,Math.sqrt(degree)*.8),class:'node-dot'}));const label=svg('text',{'text-anchor':'middle'});let title=n.title;
      if(measure){while(measure.measureText(title).width>150&&title.length>1)title=title.slice(0,-1);if(title!==n.title)title+='…';labelWidths.set(n.id,measure.measureText(title).width);}
      else{title=title.length>12?title.slice(0,12)+'…':title;labelWidths.set(n.id,title.length*13);}
      label.textContent=title;const full=svg('title');full.textContent=n.title;g.append(label,full);g.addEventListener('keydown',event=>{if(event.key==='Enter'||event.key===' '){event.preventDefault();select(n.id);}});nodeLayer.append(g);if(active===n.id)g.focus();
    }
    back.hidden=!focused;status.textContent=`已显示 ${shown.nodes.length} 份资料 · ${shown.edges.length} 条连线${[...pages.values()].some(v=>v)?' · 部分关联尚未展开':''}${shown.edges.reduce((n,e)=>n+e.variants.length,0)>MAX_EDGES?' · 部分依据连线未展开':''}${organization!=='available'?' · '+(organization==='rebuilding'?'正在重新整理':'关联暂不可用'):''}`;more.hidden=!next||nodes.size>=MAX_NODES;updateAttention();applyCamera();
  }
  function inspect(){details.setAttribute('data-open',String(!!selected));
    const n=nodes.get(selected);if(!n){
      const index=el('div',{class:'graph-index','aria-label':'画面中的资料'},...[...nodes.values()].map(item=>button(item.title,()=>select(item.id),'text-link')));
      details.replaceChildren(el('div',{class:'inspector-intro'},el('span',{class:'small-label'},'浏览关联'),el('h2',{},loaded?(nodes.size?'从一份资料开始':'还没有资料'):'正在展开资料'),el('p',{},nodes.size?'选择一份资料，沿着联系继续阅读。':loaded?'保存资料后，可以在这里浏览它们的联系。':'资料和已有的联系会一起出现在这里。')),index);return;
    }
    const links=[...edges.values()].filter(e=>e.source===n.id||e.target===n.id);
    const focus=button(focused?(center?'查看资料概览':'查看全部已显示资料'):'聚焦这份资料',()=>{if(center&&focused)return onOverview();focused=!focused;draw();inspect();fit();},'quiet');
    const expandButton=button('展开更多关联',()=>run(async()=>{const at=serial;await expand(n.id,6);if(!ok())return;draw();if(at===serial)inspect();}),'text-link');expandButton.hidden=pages.get(n.id)===null||nodes.size>=MAX_NODES||edges.size>=MAX_EDGES;
    details.replaceChildren(...[el('div',{class:'inspector-heading'},el('span',{class:'small-label'},'所选资料'),button('×',clearSelection,'icon',{'aria-label':'取消选择'})),el('h2',{},n.title),el('p',{class:'source'},n.source),selectionQuote&&n.id===center?.reference?el('blockquote',{class:'selection-quote'},el('small',{},'选中的内容 · 关联范围为整份资料'),el('mark',{},selectionQuote)):null,n.excerpt?el('p',{class:'inspector-excerpt'},n.excerpt):null,row(button('阅读全文',()=>onOpen(n),'primary'),focus,button('告诉智能体',async()=>{await navigator.clipboard.writeText(`请核对这份 Ownward 资料的关联：${n.title}\n${n.excerpt}`);notice('已复制，可粘贴到智能体对话中。');},'text-link')),el('div',{class:'inspector-section'},el('h3',{},'相关资料'),links.length?el('div',{class:'neighbor-list'},...links.map(e=>{const other=nodes.get(e.source===n.id?e.target:e.source);return el('div',{class:'neighbor-row'},button(other.title,()=>select(other.id),'text-link'),button('查看依据',()=>inspectEdge(e),'icon'));})):el('p',{class:'subtle'},n.state==='pending'?'正在等待整理。':nodes.size>=MAX_NODES?'这份资料的关联尚未显示。':pages.get(n.id)===null?'暂无关联资料。':'尚未展开这份资料的关联。'),expandButton,nodes.size>=MAX_NODES||edges.size>=MAX_EDGES?button('单独查看这份资料的关联',()=>onOpen(n,true),'text-link'):null)].filter(Boolean));
  }
  async function inspectEdge(edge){
    if(!edge||!ok())return;selectedEdge=edge.id;updateAttention();applyCamera();
    for(const line of edgeLayer.children)line.setAttribute('data-selected',String(line.getAttribute('data-edge')===edge.id));
    const at=++serial,current=()=>ok()&&at===serial,reasons=el('div',{class:'relation-details'}),seen=new Set(),basisSeen=new Set();let offset=0;
    const nextReasons=button('继续查看关联说明',loadReasons,'text-link');
    details.setAttribute('data-open','true');details.replaceChildren(el('div',{class:'inspector-heading'},el('span',{class:'small-label'},'关联依据'),button('×',clearSelection,'icon',{'aria-label':'收起关联依据'})),el('h2',{},nodes.get(edge.source).title),el('span',{class:'relation-between'},'与'),el('h2',{},nodes.get(edge.target).title),reasons,nextReasons,el('div',{class:'inspector-section'},el('h3',{},'查看资料'),button(nodes.get(edge.source).title,()=>onOpen(nodes.get(edge.source)),'text-link'),button(nodes.get(edge.target).title,()=>onOpen(nodes.get(edge.target)),'text-link')));
    async function loadReasons(){
      const end=Math.min(offset+4,edge.variants.length);nextReasons.hidden=true;
      try{while(offset<end&&current()){
        const v=edge.variants[offset],raw=v.meaning?await text('relation_text',v.meaning,current):'',description=(raw?meaning(JSON.parse(raw)):'')||v.evidence||'暂无文字说明。';
        if(!current())return;
        if(!seen.has(description)){seen.add(description);reasons.append(el('p',{class:'relation-reason'},description));}
        for(const b of v.basis||[]){const resolved=await resolve(undefined,b.asset);if(!current())return;const asset=resolved.assets?.[0];if(!asset)continue;const key=[asset.reference,b.start_rune,b.end_rune,b.role].join(':');if(basisSeen.has(key))continue;basisSeen.add(key);const label=({source:'来源引文',target:'相关资料引文',condition:'成立条件',context:'上下文'})[b.role]||'引用内容';reasons.append(button(label,()=>onOpen(asset,false,b),'text-link'));}
        offset++;
      }}catch(error){if(current())notice('关联说明暂未读完，可继续读取。',true);}
      if(current())nextReasons.hidden=offset>=edge.variants.length;
    }
    await loadReasons();
  }
  async function select(id){if(!ok())return;const at=++serial;selected=id;selectedEdge=null;draw();inspect();reveal(id);if(busy){pendingSelect={id,at};return;}await run(async()=>{await expand(id,4);if(!ok())return;draw();if(at===serial)inspect();});}
  async function run(work){if(busy||!ok())return;busy=true;node.setAttribute('aria-busy','true');try{await work();}catch(error){if(ok()){status.textContent='关联暂时无法更新';notice(error.message,true);details.append(button('重新读取',()=>run(async()=>{if(selected)await expand(selected,4);else await initialLoad();if(!ok())return;draw();inspect();fit();}),'quiet'));}}finally{busy=false;node.removeAttribute('aria-busy');const pending=pendingSelect;pendingSelect=null;if(pending&&pending.at===serial&&ok())await select(pending.id);}}
  async function loadMore(){await run(async()=>{const page=await query({view:'overview',after:next,limit:12});if(!ok())return;next=page.next||'';const added=(page.assets||[]).map(addAsset).filter(Boolean);await Promise.all(added.map(hydrate));await Promise.all(added.map(n=>expand(n.id,1)));if(ok()){draw();inspect();if(autoFit)fit();}});}
  async function initialLoad(){
    const added=assets.map(addAsset).filter(Boolean);await Promise.all(added.map(hydrate));if(!ok())return;
    if(center){selected=center.reference;focused=true;await expand(selected,6,relations);}else await Promise.all(added.map(n=>expand(n.id,3)));
    if(!ok())return;if(snapshot?.selected&&nodes.has(snapshot.selected)){selected=snapshot.selected;focused=snapshot.focused;}
    const restore=snapshot?.topology===graphSignature([...nodes.values()],[...edges.values()])&&snapshot?.layoutMode===currentMode();
    selectedEdge=snapshot?.selectedEdge&&edges.has(snapshot.selectedEdge)?snapshot.selectedEdge:null;
    loaded=true;draw();if(selectedEdge)await inspectEdge(edges.get(selectedEdge));else inspect();
    autoFit=restore?!!snapshot.autoFit:true;move(restore?snapshot.camera:fitGraph(points,width,height));
  }
  inspect();applyCamera();queueMicrotask(()=>run(initialLoad));
  return {node,capture:()=>({selected,selectedEdge,focused,autoFit,camera:{...camera},topology,layoutMode,points:[...retainedPoints]}),destroy:()=>{alive=false;serial++;resize.disconnect();cancelAnimationFrame(raf);nodes.clear();edges.clear();reads.clear();handles.clear();touch.clear();details.replaceChildren();world.replaceChildren();labelSlots.clear();labelWidths.clear();}};
}
