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
  return el('button', {type: 'button', class: `button ${style}`, ...attrs, onclick: async event => {
    const b = event.currentTarget; if (b.disabled||running) return; running=true;b.setAttribute('aria-busy','true');
    try { await action(event); } catch (error) { if (error.status !== -1) notice(errorMessage(error), true); }
    finally {running=false;b.removeAttribute('aria-busy');}
  }}, label);
}
export function notice(message, danger = false) {
  const box = document.querySelector('dialog[open]:last-of-type .modal-notice') || document.getElementById('notice'); box.textContent = message; box.classList.toggle('danger', danger); box.hidden = false;
}
export function errorMessage(error){
  if(error instanceof TypeError||error instanceof ReferenceError||error instanceof SyntaxError){console.error(error);return '窗口暂时无法完成这次操作，请保留输入并重试。';}
  return error.message;
}
export function clearNotice() { document.getElementById('notice').hidden = true; }
export const date = value => value ? new Intl.DateTimeFormat('zh-CN', {month:'long', day:'numeric', hour:'2-digit', minute:'2-digit'}).format(new Date(value)) : '';
export const heading = (title, description, action) => el('header', {class:'page-heading'}, el('div',{},el('p',{class:'eyebrow'},'OWNWARD · 物主窗口'),el('h1',{},title),el('p',{class:'lead'},description)),action);
export const empty = (title, description, action) => el('div',{class:'empty'},el('span',{class:'empty-mark','aria-hidden':'true'},'○'),el('h3',{},title),el('p',{},description),action);
export const prose = (value, cls = '') => el('pre',{class:`prose ${cls}`},value);
export const tag = (value, cls = '') => el('span',{class:`tag ${cls}`},value);
export const row = (...children) => el('div',{class:'row'},...children);
export const statusName = value => ({ready:'已整理',pending:'待整理',stopped:'已停止使用',completed:'已完成',declined:'已拒绝',superseded:'已失效',awaiting_approval:'待决定',cleaning:'正在清理',stopping:'正在停止使用',approved:'已批准',rebuilding:'正在重新整理'})[value] || '正在处理';
export const permissionName = value => ({read:'读取资料',maintain:'存入与维护资料',manage:'管理资料与接入'})[value] || '其他已授能力';
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
