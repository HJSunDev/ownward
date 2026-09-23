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
