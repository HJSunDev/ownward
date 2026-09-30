import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';

async function harness(fetch){
  const events=[],store=new Map([['ownward.owner-session','synthetic-session']]);
  const context=vm.createContext({fetch,AbortController,Blob,Event,setTimeout,clearTimeout,crypto:{randomUUID:()=>''},window:{dispatchEvent:e=>events.push(e.type)},location:{hash:'#main',pathname:'/owner/'},history:{replaceState:()=>{}},sessionStorage:{getItem:k=>store.get(k),setItem:(k,v)=>store.set(k,v),removeItem:k=>store.delete(k)}});
  const module=new vm.SourceTextModule(await readFile(new URL('./static/api.js',import.meta.url),'utf8'),{context});
  await module.link(()=>{});await module.evaluate();return {api:module.namespace,events,store,context};
}

test('late unauthorized response from an invalidated view cannot lock a new view',async()=>{
  let reply,entered;const started=new Promise(resolve=>entered=resolve);
  const h=await harness(async()=>{entered();return new Promise(resolve=>reply=resolve);});
  const request=h.api.query({view:'health'});await started;h.api.invalidate();reply({ok:false,status:401});
  await assert.rejects(request,e=>e.status===-1);assert.deepEqual(h.events,[]);
});

test('explicit logout removes the local session even if the response is lost or rejected',async()=>{
  for(const status of [401,503,0]){
    const h=await harness(async()=>{if(!status)throw new Error('offline');return {ok:false,status};});
    await assert.rejects(h.api.logout());assert.equal(h.store.has('ownward.owner-session'),false);
    await assert.rejects(h.api.initialize(),e=>e.status===401);
  }
});

test('application quit retains access until shutdown is accepted',async()=>{
  for(const status of [200,401,503]){
    const h=await harness(async path=>{assert.equal(path,'v1/quit');return {ok:status===200,status,json:async()=>({state:'stopping'})};});
    if(status===200)await h.api.quit();else await assert.rejects(h.api.quit());
    assert.equal(h.store.has('ownward.owner-session'),status!==200);assert.deepEqual(h.events,[]);
  }
});

test('shutdown bypasses occupied content slots without consuming their capacity',async()=>{
  const releases=[],paths=[];
  const h=await harness(async path=>{
    paths.push(path);
    if(path==='v1/quit')return {ok:true,json:async()=>({state:'stopping'})};
    return new Promise(resolve=>releases.push(()=>resolve({ok:true,json:async()=>({})})));
  });
  const reads=Array.from({length:3},()=>h.api.query({view:'content'}));
  await Promise.resolve();assert.equal(releases.length,3);
  try{
    const stopping=h.api.quit();await Promise.resolve();
    assert.equal(paths.at(-1),'v1/quit');await stopping;
    const waiting=h.api.query({view:'content'});await Promise.resolve();
    assert.equal(releases.length,3);
    releases[0]();await reads[0];await Promise.resolve();
    assert.equal(releases.length,4);releases[3]();await waiting;
  }finally{for(const release of releases)release();await Promise.all(reads);}
});

test('in-page content anchor never becomes an owner bootstrap token',async()=>{
  const paths=[];const h=await harness(async path=>{paths.push(path);return {ok:true,json:async()=>({health:'normal'})};});
  await h.api.initialize();assert.deepEqual(paths,['v1/query']);
});

test('old logout completion cannot erase a freshly verified session',async()=>{
  let release,entered;const started=new Promise(resolve=>entered=resolve),headers=[];
  const h=await harness(async(path,options)=>{
    headers.push([path,options.headers.Authorization]);
    if(path==='v1/logout'){entered();return new Promise(resolve=>release=resolve);}
    return {ok:true,json:async()=>path==='v1/bootstrap'?{session:'new-session'}:{health:'normal'}};
  });
  const old=h.api.logout().catch(e=>e);await started;
  h.context.location.hash='#'+'a'.repeat(64);await h.api.initialize();
  release({ok:true,json:async()=>({})});await old;await h.api.query({view:'health'});
  assert.equal(h.store.get('ownward.owner-session'),'new-session');assert.equal(headers.at(-1)[1],'Bearer new-session');
});

test('network failure from an invalidated request cannot report a current connection failure',async()=>{
  let reject,entered;const started=new Promise(resolve=>entered=resolve);
  const h=await harness(async()=>{entered();return new Promise((_,fail)=>reject=fail);});
  const request=h.api.query({view:'health'});await started;h.api.invalidate();reject(new Error('old network failure'));
  await assert.rejects(request,e=>e.status===-1);
});

test('lost entry reply retries the same exchange without losing its fragment or session',async()=>{
  const exchanges=[];let fail=true;
  const h=await harness(async(path,options)=>{
    if(path==='v1/bootstrap'){exchanges.push(JSON.parse(options.body));if(fail){fail=false;throw new Error('lost reply');}return {ok:true,json:async()=>({session:'recovered'})};}
    return {ok:true,json:async()=>({health:'normal'})};
  });
  h.context.location.hash='#'+'a'.repeat(64);
  await assert.rejects(h.api.initialize(),e=>e.status===0);
  assert.equal(h.context.location.hash,'#'+'a'.repeat(64));
  await h.api.initialize();assert.deepEqual(exchanges[0],exchanges[1]);
  assert.equal(h.store.get('ownward.owner-session'),'recovered');
});

test('expired session is discarded but rejected entry does not interrupt protected input recovery',async()=>{
  const h=await harness(async()=>({ok:false,status:401}));
  h.context.location.hash='#'+'a'.repeat(64);
  await assert.rejects(h.api.initialize(),e=>e.status===401);assert.deepEqual(h.events,[]);
  await assert.rejects(h.api.query({view:'health'}),e=>e.status===401);
  assert.equal(h.store.has('ownward.owner-session'),false);assert.deepEqual(h.events,['owner-auth-lost']);
});
