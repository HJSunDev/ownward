import {initialize} from './api.js';
import {start} from './app.js';
// Reverify inside this window: reloading would destroy memory-only rescue.
let entering=false;
async function enter(){
  if(entering)return;entering=true;
  try { const health=await initialize();await start(health); }
  catch(error){document.getElementById('status').textContent=`${error.message} 如果有未保存的文字，请保留此页面。`;}
  finally {entering=false;if(!document.getElementById('entry').hidden&&/^#[a-f0-9]{64}$/.test(location.hash))return enter();}
}
window.addEventListener('hashchange',()=>{if(!document.getElementById('entry').hidden&&/^#[a-f0-9]{64}$/.test(location.hash))return enter();});
await enter();
