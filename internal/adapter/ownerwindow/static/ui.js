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
  return node;
}
// Accessible listbox combobox replacing native <select>. Self-contained; the
// pure index helpers are exported so the contract can be asserted without a DOM.
let selectUid = 0;
export function stepIndex(count, from, delta, wrap = true) {
  if (count <= 0) return -1;
  const next = wrap ? (((from + delta) % count) + count) % count : from + delta;
  return Math.max(0, Math.min(count - 1, next));
}
export function typeaheadIndex(labels, query, from = 0) {
  const q = String(query || '').toLowerCase();
  if (!q) return -1;
  for (let i = 0; i < labels.length; i++) {
    const at = stepIndex(labels.length, from + i, 1);
    if (String(labels[at]).toLowerCase().startsWith(q)) return at;
  }
  return -1;
}
export function createSelect({ label = '', options = [], value, onChange = () => {}, skin = 'field', className = '' } = {}) {
  const opts = options.map(o => (o && typeof o === 'object' ? { value: o.value, label: o.label ?? String(o.value) } : { value: o, label: String(o) }));
  let current = opts.some(o => o.value === value) ? value : (opts[0]?.value ?? '');
  let active = Math.max(0, opts.findIndex(o => o.value === current)), typed = '', typedAt = 0;
  const labelOf = v => opts.find(o => o.value === v)?.label ?? '';
  const id = `select-${++selectUid}`;
  const wrap = el('div', { class: `select ${skin} ${className}`.trim() });
  const valueText = el('span', { class: 'select-value' }, labelOf(current));
  const trig = el('button', { type: 'button', class: 'select-trigger', role: 'combobox', 'aria-haspopup': 'listbox', 'aria-expanded': 'false', 'aria-controls': id, 'aria-label': label || undefined }, valueText, el('span', { class: 'select-caret', 'aria-hidden': 'true' }, '▾'));
  const list = el('ul', { id, class: 'select-list', role: 'listbox', tabindex: '-1', hidden: true, 'aria-label': label || undefined });
  const items = opts.map((o, i) => el('li', { class: 'select-option', role: 'option', id: `${id}-${i}`, 'aria-selected': String(o.value === current) }, o.label));
  list.append(...items); wrap.append(trig, list);
  function setActive(i, reveal = false) {
    active = i;
    items.forEach((it, n) => it.classList.toggle('active', n === i));
    if (i >= 0) { trig.setAttribute('aria-activedescendant', items[i].id); if (reveal) items[i].scrollIntoView({ block: 'nearest' }); }
    else trig.removeAttribute('aria-activedescendant');
  }
  function open() { if (!list.hidden) return; list.hidden = false; trig.setAttribute('aria-expanded', 'true'); setActive(Math.max(0, opts.findIndex(o => o.value === current)), true); document.addEventListener('pointerdown', outside, true); }
  function close(focus = true) { if (list.hidden) return; list.hidden = true; trig.setAttribute('aria-expanded', 'false'); trig.removeAttribute('aria-activedescendant'); document.removeEventListener('pointerdown', outside, true); if (focus) trig.focus(); }
  function outside(event) { if (!wrap.contains(event.target)) close(false); }
  function commit(i) { const o = opts[i]; if (!o) return; current = o.value; valueText.textContent = o.label; items.forEach((it, n) => it.setAttribute('aria-selected', String(n === i))); close(); onChange(current, o); }
  trig.addEventListener('click', () => (list.hidden ? open() : close()));
  trig.addEventListener('keydown', event => {
    const k = event.key;
    if (k === 'Escape') { if (!list.hidden) { event.preventDefault(); close(); } return; }
    if (k === 'ArrowDown' || k === 'ArrowUp') { event.preventDefault(); if (list.hidden) open(); else setActive(stepIndex(opts.length, active, k === 'ArrowDown' ? 1 : -1), true); return; }
    if (k === 'Home' || k === 'End') { if (list.hidden) return; event.preventDefault(); setActive(k === 'Home' ? 0 : opts.length - 1, true); return; }
    if (k === 'Enter' || k === ' ') { if (list.hidden) return; event.preventDefault(); commit(active); return; }
    if (k.length === 1) { const now = Date.now(); typed = now - typedAt < 700 ? typed + k : k; typedAt = now; const at = typeaheadIndex(opts.map(o => o.label), typed, active); if (at >= 0) { if (list.hidden) open(); setActive(at, true); } }
  });
  items.forEach((it, i) => { it.addEventListener('mousedown', event => event.preventDefault()); it.addEventListener('click', () => commit(i)); it.addEventListener('mousemove', () => setActive(i)); });
  wrap.addEventListener('focusout', event => { if (!wrap.contains(event.relatedTarget)) close(false); });
  return { node: wrap, value: () => current, set: v => { const i = opts.findIndex(o => o.value === v); if (i >= 0) commit(i); }, focus: () => trig.focus() };
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
export const empty = (title, description, action) => { if(action?.classList)action.classList.add('align-start'); return el('div',{class:'empty'},el('h3',{},title),description?el('p',{},description):null,action); };
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
  modal.addEventListener('keydown',event=>{
    if(event.key!=='Tab')return;
    const targets=[...modal.querySelectorAll('button,input,select,textarea,a[href],summary,[tabindex]')].filter(n=>!n.disabled&&n.tabIndex>=0&&n.getClientRects().length);
    const first=targets[0],last=targets.at(-1);if(!first){event.preventDefault();return;}
    if(event.shiftKey&&document.activeElement===first){event.preventDefault();last.focus();}
    else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first.focus();}
  });
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

// Reading and confirmation use the same literal-text renderer. Editing keeps
// a native textarea, sharing its text and typography without an HTML roundtrip.
export function documentView(value, cls = '', editable = false) {
  if(editable)return el('textarea', {class:`document-text draft-input ${cls}`,value,spellcheck:false,'aria-label':'文稿正文'});
  const node=prose('',`document-text ${cls}`);renderDocument(node,value);return node;
}
export function renderDocument(node,value,marks=[]){
  // A separated first line is a typographic heading, never inferred content.
  const title=/^([^\r\n]{1,110})\r?\n\s*\r?\n/.exec(value),end=title?title[1].length:0;
  function parts(start,stop){const items=[];let at=start;
    for(const m of marks){const empty=m.start===m.end;if(empty?!(m.start>=start&&(m.start<stop||stop===value.length&&m.start===stop)):m.end<=start||m.start>=stop)continue;const a=Math.max(start,m.start),b=Math.min(stop,m.end);if(a<at||b<a)continue;items.push(value.slice(at,a),el('mark',{class:m.class},value.slice(a,b)||'〔此处无文字〕'));at=b;}
    items.push(value.slice(at,stop));return items;
  }
  node.replaceChildren(...(end?[el('span',{class:'document-title'},...parts(0,end)),...parts(end,value.length)]:parts(0,value.length)));
}
export function matchRanges(value, needle) {
  if(!needle)return [];
  const ranges=[];let at=0;
  while(at<value.length){const start=value.indexOf(needle,at);if(start<0)break;ranges.push([start,start+needle.length]);at=start+needle.length;}
  return ranges;
}
export function renderMatches(node, value, needle, selected=0) {
  const ranges=matchRanges(value,needle);renderDocument(node,value,ranges.map(([start,end],i)=>({start,end,class:i===selected?'match current-match':'match'})));return ranges.length;
}
export function differences(before,after){
  const lines=value=>{let at=0;return (value.match(/[^\n]*\n|[^\n]+$/g)||[]).map(text=>{const line={text,start:at};at+=text.length;return line;});};
  const a=lines(before),b=lines(after),index=new Map(),counts=new Map();
  for(const line of a)counts.set(line.text,(counts.get(line.text)||0)+1);
  b.forEach((line,i)=>index.set(line.text,index.has(line.text)?-1:i));
  // Unique unchanged lines anchor a monotone alignment in O(n log n).
  // Repeated/ambiguous passages stay one range; no semantic diff is claimed.
  const candidates=[];a.forEach((line,i)=>{const j=index.get(line.text);if(counts.get(line.text)===1&&j>=0)candidates.push({i,j});});
  const tails=[],previous=[];
  candidates.forEach((pair,i)=>{let lo=0,hi=tails.length;while(lo<hi){const mid=(lo+hi)>>1;if(candidates[tails[mid]].j<pair.j)lo=mid+1;else hi=mid;}previous[i]=lo?tails[lo-1]:-1;tails[lo]=i;});
  const anchors=[];for(let at=tails.at(-1);at!==undefined&&at>=0;at=previous[at])anchors.push(candidates[at]);anchors.reverse();anchors.push({i:a.length,j:b.length});
  const changes=[];let fromA=0,fromB=0;
  for(const pair of anchors){let endA=a[pair.i]?.start??before.length,endB=b[pair.j]?.start??after.length,startA=fromA,startB=fromB;
    while(startA<endA&&startB<endB&&before[startA]===after[startB]){startA++;startB++;}
    while(endA>startA&&endB>startB&&before[endA-1]===after[endB-1]){endA--;endB--;}
    if(startA!==endA||startB!==endB){if(startA>0&&/^[\uDC00-\uDFFF]$/.test(before[startA]||after[startB]||'')){startA--;startB--;}
      if(/^[\uDC00-\uDFFF]$/.test(before[endA]||''))endA++;if(/^[\uDC00-\uDFFF]$/.test(after[endB]||''))endB++;
      changes.push({before:{start:startA,end:endA},after:{start:startB,end:endB}});
    }
    fromA=(a[pair.i]?.start??before.length)+(a[pair.i]?.text.length||0);fromB=(b[pair.j]?.start??after.length)+(b[pair.j]?.text.length||0);
  }
  return changes;
}
export function comparison(before, after) {
  const left=documentView(before),right=documentView(after),panels=[left,right];
  const changes=differences(before,after);
  for(const [node,value,side]of [[left,before,'before'],[right,after,'after']])renderDocument(node,value,changes.map((change,i)=>({...change[side],class:`difference change-${i}`})));
  const tabs=row(),columns=el('div',{class:'comparison-columns'},el('section',{},el('h3',{},'现在'),left),el('section',{},el('h3',{},'将成为'),right));
  const root=el('div',{class:'comparison', 'data-side':'after'},tabs,columns);
  for(const [side,label]of [['before','现在'],['after','将成为']])tabs.append(button(label,()=>{root.setAttribute('data-side',side);for(const b of tabs.children)b.setAttribute('aria-pressed',String(b.textContent===label));},'compare-tab',{'aria-pressed':String(side==='after')}));
  if(changes.length)root.prepend(el('div',{class:'change-navigation','aria-label':'修改位置'},...changes.map((_,i)=>button(`改动 ${i+1}`,()=>{root.querySelectorAll(`.change-${i}`).forEach(mark=>mark.scrollIntoView({block:'center',behavior:'auto'}));},'text-link'))));
  let sync=false;
  for(const [i,node]of panels.entries())node.addEventListener('scroll',()=>{if(sync)return;const other=panels[1-i],range=node.scrollHeight-node.clientHeight;sync=true;other.scrollTop=range?node.scrollTop/range*(other.scrollHeight-other.clientHeight):0;queueMicrotask(()=>sync=false);});
  return root;
}
let appearance={theme:'system',size:'19'},immersive=false;
export function applyAppearance(){
  try{const saved=JSON.parse(localStorage.getItem('ownward.appearance')||'{}');if(['system','light','dark'].includes(saved.theme))appearance.theme=saved.theme;if(['17','19','21'].includes(saved.size))appearance.size=saved.size;}catch{}
  document.documentElement.dataset.theme=appearance.theme;
  document.documentElement.style.setProperty('--reading-size',appearance.size+'px');
}
export function appearanceControls(){
  const save=patch=>{appearance={...appearance,...patch};try{localStorage.setItem('ownward.appearance',JSON.stringify(appearance));}catch{}applyAppearance();};
  const theme=createSelect({label:'显示主题',skin:'field',value:appearance.theme,options:Object.entries({system:'跟随系统',light:'浅色',dark:'深色'}).map(([value,label])=>({value,label})),onChange:value=>save({theme:value})});
  const size=createSelect({label:'正文字号',skin:'field',value:appearance.size,options:['17','19','21'].map(value=>({value,label:`${value} px`})),onChange:value=>save({size:value})});
  return el('div',{class:'appearance-controls'},el('label',{},'主题',theme.node),el('label',{},'正文',size.node));
}
export function setImmersive(value){immersive=value;document.getElementById('shell').classList.toggle('immersive',value);for(const b of document.querySelectorAll('.immersion-toggle')){b.textContent=value?'显示导航':'专注模式';b.setAttribute('aria-pressed',String(value));}}
export function immersionButton(){return button(immersive?'显示导航':'专注模式',()=>setImmersive(!immersive),'quiet immersion-toggle',{'aria-pressed':String(immersive)});}
