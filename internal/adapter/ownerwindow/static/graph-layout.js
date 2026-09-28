// Layout only: connected components come from supplied edges, never semantics.
export function layoutGraph(nodes, edges) {
  nodes=[...nodes].sort((a,b)=>a.id.localeCompare(b.id));edges=[...edges].sort((a,b)=>(a.source+a.target).localeCompare(b.source+b.target));
  const ids=new Set(nodes.map(n=>n.id)), parent=new Map(nodes.map(n=>[n.id,n.id]));
  const root=id=>{while(parent.get(id)!==id)id=parent.get(id);return id;};
  for(const e of edges)if(ids.has(e.source)&&ids.has(e.target))parent.set(root(e.target),root(e.source));
  const groups=new Map();for(const n of nodes){const key=root(n.id);if(!groups.has(key))groups.set(key,[]);groups.get(key).push(n);}
  const ordered=[...groups.values()].sort((a,b)=>b.length-a.length),points=new Map();
  const columns=Math.max(1,Math.round(Math.sqrt(ordered.length*1.4))),cell=420;
  ordered.forEach((group,index)=>{
    const cx=(index%columns)*cell,cy=Math.floor(index/columns)*340;
    group.forEach((n,i)=>{const a=i*Math.PI*2/group.length-.8,r=group.length===1?0:75+group.length*6;points.set(n.id,{x:cx+Math.cos(a)*r,y:cy+Math.sin(a)*r,cx,cy,group:index,vx:0,vy:0});});
  });
  for(let step=0;step<160;step++){
    const forces=new Map(nodes.map(n=>[n.id,{x:0,y:0}]));
    for(let i=0;i<nodes.length;i++)for(let j=i+1;j<nodes.length;j++){
      const a=points.get(nodes[i].id),b=points.get(nodes[j].id);let dx=a.x-b.x,dy=a.y-b.y;
      if(Math.abs(dx)+Math.abs(dy)<.1)dx=.1;
      const d=Math.max(28,Math.hypot(dx,dy)),f=5200/(d*d);
      forces.get(nodes[i].id).x+=dx/d*f;forces.get(nodes[i].id).y+=dy/d*f;
      forces.get(nodes[j].id).x-=dx/d*f;forces.get(nodes[j].id).y-=dy/d*f;
    }
    for(const e of edges){const a=points.get(e.source),b=points.get(e.target);if(!a||!b)continue;const dx=b.x-a.x,dy=b.y-a.y,d=Math.max(1,Math.hypot(dx,dy)),f=(d-185)*.022;
      forces.get(e.source).x+=dx/d*f;forces.get(e.source).y+=dy/d*f;forces.get(e.target).x-=dx/d*f;forces.get(e.target).y-=dy/d*f;
    }
    for(const [id,p] of points){const f=forces.get(id);p.vx=(p.vx+f.x+(p.cx-p.x)*.009)*.7;p.vy=(p.vy+f.y+(p.cy-p.y)*.012)*.7;p.x+=p.vx;p.y+=p.vy;}
  }
  return points;
}
export function fitGraph(points,width=1000,height=660){
  if(!points.size)return {x:width/2,y:height/2,k:1};
  const all=[...points.values()],minX=Math.min(...all.map(p=>p.x))-85,maxX=Math.max(...all.map(p=>p.x))+85,minY=Math.min(...all.map(p=>p.y))-65,maxY=Math.max(...all.map(p=>p.y))+65;
  const k=Math.min(1.5,(width-100)/(maxX-minX),(height-110)/(maxY-minY));
  return {k,x:width/2-(minX+maxX)/2*k,y:height/2-(minY+maxY)/2*k};
}
