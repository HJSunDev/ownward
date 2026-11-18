import test from 'node:test';
import assert from 'node:assert/strict';
import net from 'node:net';
import {randomUUID} from 'node:crypto';
import {deliverCodex} from './codex-delivery.mjs';

async function host(t, steer) {
  const pipePath = `\\\\.\\pipe\\ownward-delivery-test-${randomUUID()}`;
  const seen = [], sockets = new Set();
  const server = net.createServer(socket => {
    sockets.add(socket); socket.on('close', () => sockets.delete(socket)); socket.on('error', () => {});
    let buffer = Buffer.alloc(0);
    const respond = value => {
      const body=Buffer.from(JSON.stringify(value)), frame=Buffer.alloc(body.length+4);
      frame.writeUInt32LE(body.length); body.copy(frame,4);
      socket.write(frame.subarray(0,3)); setImmediate(()=>{if(!socket.destroyed)socket.write(frame.subarray(3));});
    };
    socket.on('data', chunk => {
      buffer=Buffer.concat([buffer,chunk]);
      while(buffer.length>=4){const size=buffer.readUInt32LE();if(buffer.length<size+4)return;
        const msg=JSON.parse(buffer.subarray(4,size+4));buffer=buffer.subarray(size+4);seen.push(msg);
        const result=value=>respond({type:'response',requestId:msg.requestId,resultType:'success',method:msg.method,handledByClientId:'owner',result:value});
        if(msg.method==='initialize')result({clientId:'caller'});
        else if(msg.method==='thread-owner-discovery')result({supportsUntrustedAppInput:true});
        else if(msg.method==='thread-follower-steer-turn')steer(msg,result,respond,socket);
        else assert.fail('Unexpected request');
      }
    });
  });
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(pipePath,resolve);});
  t.after(async()=>{for(const socket of sockets)socket.destroy();await new Promise(resolve=>server.close(resolve));});
  return {pipePath,seen};
}
const inputs={threadId:'known-thread',message:'代理消息：完整中文，不新增授权',cwd:'E:/work',queueExe:'known-codex'};

test('active owner accepts input in its existing turn; stable identity and full text survive fragmented frames',async t=>{
  const h=await host(t,(_msg,result)=>result({result:{turnId:'active-turn'}}));
  let queued=0;
  const args={...inputs,pipePath:h.pipePath,queueMessage:async()=>{queued++;}};
  const a=await deliverCodex(args),b=await deliverCodex(args);
  assert.equal(a.phase,'submitted');assert.equal(a.mode,'steer');assert.equal(a.turnId,'active-turn');assert.equal(queued,0);
  assert.equal(a.clientUserMessageId,b.clientUserMessageId);
  const requests=h.seen.filter(x=>x.method==='thread-follower-steer-turn');
  assert.equal(requests[0].targetClientId,'owner');assert.equal(requests[0].version,1);
  assert.equal(requests[0].params.conversationId,inputs.threadId);
  assert.deepEqual(requests[0].params.input,[{type:'text',text:inputs.message,text_elements:[]}]);
  assert.ok(!h.seen.some(x=>/start|resume|interrupt/.test(x.method)));
});
test('only explicit inactive-turn rejection permits ordinary idle delivery',async t=>{
  const h=await host(t,(msg,_result,respond)=>respond({type:'response',requestId:msg.requestId,resultType:'error',error:'Conversation known-thread is not being streamed.'}));
  let args;
  const result=await deliverCodex({...inputs,pipePath:h.pipePath,queueMessage:async(...value)=>{args=value;return{phase:'submitted',mode:'queue-idle'};}});
  assert.deepEqual(args,[inputs.queueExe,inputs.threadId,inputs.message]);assert.equal(result.mode,'queue-idle');
});
test('non-idle rejection never silently falls back to queue',async t=>{
  const h=await host(t,(msg,_result,respond)=>respond({type:'response',requestId:msg.requestId,resultType:'error',error:'request-version-mismatch'}));
  let queued=false;
  const result=await deliverCodex({...inputs,pipePath:h.pipePath,queueMessage:async()=>{queued=true;}});
  assert.equal(result.phase,'failed');assert.equal(result.code,'request-version-mismatch');assert.equal(queued,false);
});
test('lost acknowledgement after sending is unknown, not a fresh queued resend',async t=>{
  const h=await host(t,(_msg,_result,_respond,socket)=>socket.destroy());
  let queued=false;
  const result=await deliverCodex({...inputs,pipePath:h.pipePath,queueMessage:async()=>{queued=true;}});
  assert.equal(result.phase,'unknown');assert.equal(queued,false);
});
test('accepted reply without a turn identity does not claim verified steering',async t=>{
  const h=await host(t,(_msg,result)=>result({result:{}}));
  const result=await deliverCodex({...inputs,pipePath:h.pipePath});
  assert.equal(result.phase,'unknown');assert.equal(result.code,'steer-reply-missing-turn-id');
});
