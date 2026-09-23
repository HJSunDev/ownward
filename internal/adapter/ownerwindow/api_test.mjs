import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';

async function harness(fetch){
  const events=[],store=new Map([['ownward.owner-session','synthetic-session']]);
  const context=vm.createContext({fetch,AbortController,Blob,Event,setTimeout,clearTimeout,crypto:{randomUUID:()=>''},window:{dispatchEvent:e=>events.push(e.type)},location:{hash:'#main',pathname:'/owner/'},history:{replaceState:()=>{}},sessionStorage:{getItem:k=>store.get(k),setItem:(k,v)=>store.set(k,v),removeItem:k=>store.delete(k)}});
  const module=new vm.SourceTextModule(await readFile(new URL('./static/api.js',import.meta.url),'utf8'),{context});
  await module.link(()=>{});await module.evaluate();return {api:module.namespace,events};
}

test('late unauthorized response from an invalidated view cannot lock a new view',async()=>{
  let reply,entered;const started=new Promise(resolve=>entered=resolve);
  const h=await harness(async()=>{entered();return new Promise(resolve=>reply=resolve);});
  const request=h.api.query({view:'health'});await started;h.api.invalidate();reply({ok:false,status:401});
  await assert.rejects(request,e=>e.status===-1);assert.deepEqual(h.events,[]);
});

test('in-page content anchor never becomes an owner bootstrap token',async()=>{
  const paths=[];const h=await harness(async path=>{paths.push(path);return {ok:true,json:async()=>({health:'normal'})};});
  await h.api.initialize();assert.deepEqual(paths,['v1/query']);
});
