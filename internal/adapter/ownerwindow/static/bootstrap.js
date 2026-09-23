import {initialize} from './api.js';
import {start} from './app.js';
// Following a fresh entry in an already locked tab is same-document navigation.
// Reload only that verified entry shape; normal in-page links remain navigation.
window.addEventListener('hashchange',()=>{if(!document.getElementById('entry').hidden&&/^#[a-f0-9]{64}$/.test(location.hash))location.reload();});
try { const health = await initialize(); await start(health); }
catch (error) { document.getElementById('status').textContent = `${error.message} 可在本机运行 ownward owner-window 重新验证。`; }
