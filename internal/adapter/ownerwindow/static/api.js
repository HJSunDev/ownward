export class ApiError extends Error {
  constructor(status, message) { super(message); this.status = status; }
}
const schema = 'ownward.owner-view/v1', sessionKey = 'ownward.owner-session';
export const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
export const operationID = () => crypto.randomUUID();
export const storage = {
  get(key) { try { return sessionStorage.getItem(key); } catch { return null; } },
  set(key, value) { try { sessionStorage.setItem(key, value); return true; } catch { return false; } },
  remove(key) { try { sessionStorage.removeItem(key); return true; } catch { return false; } }
};
let session = storage.get(sessionKey), active = 0, generation = 0;
export function invalidate(){generation++;}
export function scope(){const current=generation;return ()=>current===generation;}
const waiting = [];
async function slot() { if (active >= 3) await new Promise(resolve => waiting.push(resolve)); else active++; }
function release() { const next=waiting.shift(); if(next)next();else active--; }
export async function request(path, data = {}, options = {}) {
  const current=scope();
  await slot();
  const abort = new AbortController(), timer = setTimeout(() => abort.abort(), options.timeout || 45000);
  try {
    if(!current())throw new ApiError(-1,'操作页面已变化。');
    const response = await fetch(`v1/${path}`, {
      method: 'POST', credentials: 'omit', cache: 'no-store', redirect: 'error', signal: abort.signal,
      headers: {'Content-Type': options.type || 'application/json', 'X-Ownward-View': schema,
        ...(session ? {Authorization: `Bearer ${session}`} : {}), ...options.headers},
      body: options.raw ? data : JSON.stringify(data)
    });
    if(!current())throw new ApiError(-1,'操作页面已变化。');
    if (!response.ok) {
      if (response.status === 401) window.dispatchEvent(new Event('owner-auth-lost'));
      throw new ApiError(response.status, response.status === 409 ? '内容已更新，请核对后继续。' :
        response.status === 401 ? '验证已失效，请从本机物主入口重新打开。' :
        response.status === 429 ? '正在处理其他操作，请稍后重试。' : '这次操作未完成，请保留输入后重试。');
    }
    const result=options.blob ? await response.blob() : await response.json();
    if(!current())throw new ApiError(-1,'操作页面已变化。');
    return result;
  } catch (error) {
    if (error instanceof ApiError) throw error;
    if(!current())throw new ApiError(-1,'操作页面已变化。');
    throw new ApiError(0, '连接暂时中断，尚未同步的输入已留在此窗口。');
  } finally { clearTimeout(timer); release(); }
}
export const query = input => request('query', input);
export const act = input => request('action', input);
export async function initialize() {
  invalidate();
  const fragment = location.hash.slice(1), token = /^[a-f0-9]{64}$/.test(fragment)?fragment:''; history.replaceState(null, '', location.pathname);
  if (token) { const result = await request('bootstrap', {token}); session = result.session; storage.set(sessionKey, session); }
  if (!session) throw new ApiError(401, '请从本机物主入口打开这个窗口。');
  return query({view: 'health'});
}
export const resolve = (reference, handle) => query({view: 'resolve', ...(reference ? {reference} : {handle})});
export async function text(view, handle, valid = () => true) {
  const parts = []; let offset = 0;
  for (;;) {
    const page = await query({view, handle, offset});
    if (!valid()) throw new ApiError(-1, '阅读已结束。');
    parts.push(page.text.text);
    if (!page.text.more) return parts.join('');
    if (page.text.next_offset <= offset) throw new ApiError(0, '正文暂时无法继续读取。');
    offset = page.text.next_offset;
  }
}
let saves=Promise.resolve(),lastSave=0;
export function replace(handle,value){
  const current=scope();
  const job=saves.then(async()=>{await pause(Math.max(0,1100-(Date.now()-lastSave)));if(!current())throw new ApiError(-1,'操作页面已变化。');lastSave=Date.now();return replaceText(handle,value);});
  saves=job.catch(()=>{});return job;
}
async function replaceText(handle, value) {
  const body = new Blob([value], {type: 'text/plain;charset=utf-8'});
  if (body.size > 256 * 1024 * 1024) throw new ApiError(413, '本次文字超出可提交大小，输入仍保留在窗口。');
  return request('draft-text', body, {raw: true, type: 'text/plain; charset=utf-8', headers: {'X-Ownward-Handle': handle}, timeout: 180000});
}
export async function logout() {
  const previous=session;
  try { return await request('logout'); }
  finally { if(session===previous){storage.remove(sessionKey);session=null;} }
}
