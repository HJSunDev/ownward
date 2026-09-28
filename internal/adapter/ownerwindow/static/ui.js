export function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (key === 'class') node.className = value;
    else if (key.startsWith('on')) node.addEventListener(key.slice(2).toLowerCase(), value);
    else if (key === 'text') node.textContent = value;
    else if (key in node && !key.startsWith('aria')) node[key] = value;
    else node.setAttribute(key, value);
  }
  node.append(...children.filter(x => x != null)); return node;
}
export function button(label, action, style = 'quiet', attrs = {}) {
  let running=false;
  const node=el('button', {type: 'button', class: `button ${style}`, ...attrs, onclick: async event => {
    const b = event.currentTarget; if (b.disabled||running) return; running=true;b.setAttribute('aria-busy','true');
    try { await action(event); } catch (error) { if (error.status !== -1) notice(errorMessage(error), true); }
    finally {running=false;b.removeAttribute('aria-busy');}
  }}, label);
  const name=attrs['data-surface']||({'新建文稿':'plus','编辑':'edit','邀请应用协助':'spark','返回资料':'back','返回文稿':'back','查看关联':'relations'}[label]);
  if(name){const paths={assets:'M4 4h6l2 2h8v14H4z M4 10h16',drafts:'M5 3h10l4 4v14H5z M9 12h6 M9 16h5 M14 3v5h5',relations:'M9 7l7 3 M8 9l2 8 M15 12l-3 5',events:'M4 12a8 8 0 1 0 3-6 M4 4v5h5 M12 7v5l3 2',control:'M5 7h14 M5 17h14 M9 4v6 M15 14v6',plus:'M12 5v14 M5 12h14',edit:'M4 20l4-1 12-12-4-4L4 15z M13 6l4 4',spark:'M12 3l2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5z',back:'M14 5l-7 7 7 7 M7 12h14'};
    const mark=document.createElementNS('http://www.w3.org/2000/svg','svg');for(const [k,v]of Object.entries({viewBox:'0 0 24 24',width:18,height:18,fill:'none',stroke:'currentColor','stroke-width':1.5,'stroke-linecap':'round','stroke-linejoin':'round','aria-hidden':'true',class:'button-icon'}))mark.setAttribute(k,v);
    const path=document.createElementNS('http://www.w3.org/2000/svg','path');path.setAttribute('d',paths[name]||paths.assets);mark.append(path);
    if(name==='relations')for(const [cx,cy]of [[7,6],[18,11],[11,19]]){const c=document.createElementNS('http://www.w3.org/2000/svg','circle');c.setAttribute('cx',cx);c.setAttribute('cy',cy);c.setAttribute('r','2.5');mark.append(c);}node.prepend(mark);
  }
  return node;
}
const noticeOwners = new WeakMap();
export function notice(message, danger = false) {
  const box = document.querySelector('dialog[open]:last-of-type .modal-notice') || document.getElementById('notice'), owner = {};
  noticeOwners.set(box, owner);box.textContent = message; box.classList.toggle('danger', danger); box.hidden = false;
  return () => { if(noticeOwners.get(box)===owner){box.hidden=true;noticeOwners.delete(box);} };
}
export function errorMessage(error){
  if(error instanceof TypeError||error instanceof ReferenceError||error instanceof SyntaxError){console.error(error);return '窗口暂时无法完成这次操作，请保留输入并重试。';}
  return error.message;
}
export function clearNotice() { const box=document.getElementById('notice');box.hidden=true;noticeOwners.delete(box); }
export const date = value => value ? new Intl.DateTimeFormat('zh-CN', {month:'long', day:'numeric', hour:'2-digit', minute:'2-digit'}).format(new Date(value)) : '';
export const heading = (title, description, action) => el('header', {class:'page-heading'}, el('div',{},el('h1',{},title),description?el('p',{class:'lead'},description):null),action);
export const empty = (title, description, action) => el('div',{class:'empty'},el('h3',{},title),description?el('p',{},description):null,action);
export const prose = (value, cls = '') => el('pre',{class:`prose ${cls}`},value);
export const tag = (value, cls = '') => el('span',{class:`tag ${cls}`},value);
export const row = (...children) => el('div',{class:'row'},...children);
export const statusName = value => ({ready:'已整理',pending:'待整理',stopped:'已停止使用',completed:'已完成',declined:'已拒绝',superseded:'内容已变化',awaiting_approval:'待确认',cleaning:'正在删除',stopping:'正在停止使用',approved:'已允许',rebuilding:'正在重新整理'})[value] || '处理中';
export const permissionName = value => ({read:'查看资料',maintain:'添加与修改资料',manage:'管理资料与访问权限'})[value] || '其他权限';
export function dialog(title, contents, actions = []) {
  const previous = document.activeElement, modal = el('dialog',{class:'modal','aria-label':title});
  const close = () => { modal.close(); modal.replaceChildren();modal.remove(); if (previous?.isConnected) previous.focus(); };
  close.current=()=>modal.isConnected&&modal.open;
  modal.append(el('header',{},el('h2',{},title),button('关闭',close,'icon',{'aria-label':'关闭对话框'})),el('div',{class:'modal-body'},el('p',{class:'modal-notice',role:'status',hidden:true}),contents),el('footer',{},...actions.map(item=>button(item.label,()=>item.run(close),item.style || 'quiet'))));
  modal.addEventListener('cancel',event=>{event.preventDefault();close();}); document.body.append(modal);modal.showModal();return {modal,close};
}
export function confirm(title, content, label, run, danger = false) {
  return new Promise(resolve => {
    const d = dialog(title, content, [{label:'返回',run:close=>{close();resolve(false);}},{label,style:danger?'danger':'primary',run:async close=>{await run();close();resolve(true);}}]);
    d.modal.addEventListener('close',()=>resolve(false),{once:true});
  });
}
export function download(blob, name) {
  const url = URL.createObjectURL(blob), a = el('a',{href:url,download:name}); document.body.append(a);a.click();a.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);
}
