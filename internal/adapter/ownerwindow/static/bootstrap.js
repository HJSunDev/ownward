import {initialize} from './api.js';
import {start} from './app.js';
// Reverify inside this window: reloading would destroy memory-only rescue.
let entering=false,retryTimer,failures=0,queuedEntry=false;
const entry=document.getElementById('entry'),title=document.getElementById('entry-title'),status=document.getElementById('status'),retry=document.getElementById('entry-retry'),help=document.getElementById('entry-help');
function reset(){clearTimeout(retryTimer);failures=0;return enter();}
async function enter(){
  if(entering||entry.hidden||entry.dataset.phase==='closed')return;entering=true;
  retry.hidden=true;help.hidden=true;entry.dataset.phase='opening';
  title.textContent=failures?'正在恢复你的资料':'正在打开你的资料';
  status.textContent='稍等片刻，即可继续。';
  try { const health=await initialize();await start(health);failures=0; }
  catch(error){
    if(error.status===-1)return;
    const temporary=error.status===0||error.status===429||error.status>=500;
    failures++;
    if(temporary&&failures<5){
      title.textContent='正在恢复你的资料';status.textContent='暂时未能打开，正在自动重试。';
      retryTimer=setTimeout(enter,Math.min(4000,500*2**(failures-1)));
    }else{
      entry.dataset.phase='waiting';help.hidden=false;retry.hidden=!temporary;
      title.textContent=temporary?'暂时无法打开资料':'请重新打开 Ownward';
      status.textContent=temporary?'请从电脑中的 Ownward 入口打开。它会自动准备好你的资料。':'从电脑中的 Ownward 入口打开，即可继续。';
      if(document.querySelector('.resume-bar'))status.textContent+=' 未保存的文字仍留在此页面，请先保留。';
    }
  } finally {entering=false;if(queuedEntry){queuedEntry=false;return reset();}}
}
retry.addEventListener('click',reset);
window.addEventListener('online',()=>{if(!entry.hidden)reset();});
window.addEventListener('hashchange',()=>{if(!entry.hidden&&/^#[a-f0-9]{64}$/.test(location.hash)){if(entering)queuedEntry=true;else return reset();}});
await enter();
