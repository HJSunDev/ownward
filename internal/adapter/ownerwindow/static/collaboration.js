import {act,query,scope} from './api.js';
import {el,button,dialog,date,errorMessage,createPageNavigation} from './ui.js';

const label=state=>({awaiting_approval:'等待你确认',approved:'可编辑',declined:'已拒绝',cancelled:'已结束',expired:'已到期',ended:'已结束'})[state]||'已结束';

// One projection feeds both the quiet editor status and the handoff panel.
// Granting access never claims that the external agent is running.
export async function refreshCollaboration(editor){
  if(!editor?.live||!editor.collaboration)return;
  const ticket=editor.collaborationRead=(editor.collaborationRead||0)+1;
  const current=scope(),page=await query({view:'draft_collaborations',handle:editor.meta.handle,limit:20,state:'active'});
  if(!current()||!editor.live||ticket!==editor.collaborationRead)return;
  const active=(page.collaborations||[]).filter(c=>['approved','awaiting_approval'].includes(c.state));
  const signature=JSON.stringify([!!page.next,active.map(c=>[c.connection.name,c.state,c.expires_at,c.verification])]);
  if(editor.collaborationSignature!==signature){
    editor.collaborationSignature=signature;editor.collaboration.hidden=!active.length&&!page.next;
    editor.collaboration.replaceChildren(...active.slice(0,3).map(c=>el('div',{class:'collaboration-row'},el('span',{},`${c.connection.name} · ${label(c.state)}${c.state==='approved'?` · 至 ${date(c.expires_at)}`:''}`),button('结束协助',async()=>{await act({action:'end_collaboration',handle:c.handle});await refreshCollaboration(editor);},'text-link'))));
    if(active.length>3||page.next)editor.collaboration.append(button('查看全部协作',()=>openCollaboration(editor),'text-link'));
  }
  await editor.collaborationPanel?.();
}

export async function openCollaboration(editor){
  const current=scope();await editor.flush();if(!current()||!editor.live)return;
  const status=el('div',{class:'collaboration-status',role:'status'}),fallback=el('textarea',{class:'collaboration-copy',readOnly:true,hidden:true,'aria-label':'协作请求，可手动复制'});
  let invitation=null;
  const cancel=button('取消这次请求',async()=>{if(!invitation)return;await act({action:'cancel_invitation',handle:invitation.handle});invitation=null;cancel.hidden=true;fallback.hidden=true;fallback.value='';status.textContent='这次请求已取消，原文稿保留。';await refreshCollaboration(editor);},'text-link');cancel.hidden=true;
  const explanation=el('p',{},'把请求发给你常用的智能体，告诉它想怎样修改。已有授权可直接继续；临时协助可交给你信任的主智能体确认，也可在这里手动处理。');
  const checkInvitation=async()=>{
    const expected=invitation;if(!expected)return;
    const page=await query({view:'draft_collaborations',handle:editor.meta.handle,reference:expected.id,limit:16});
    if(!current()||!editor.live||!view.close.current()||invitation!==expected)return;
    if(page.unavailable||page.collaborations?.length&&page.collaborations.every(c=>!['approved','awaiting_approval'].includes(c.state))){
      invitation=null;cancel.hidden=true;fallback.hidden=true;fallback.value='';status.textContent='本次协作已结束。如需继续，可重新复制请求；原稿保留。';
    }
  };
  const copy=button('复制协作请求',async()=>{
    if(!current()||!editor.live||!view.close.current())return;
    await editor.flush();if(!current()||!editor.live||!view.close.current())return;
    await checkInvitation();if(!current()||!editor.live||!view.close.current())return;
    if(!invitation||Date.parse(invitation.expires_at)<=Date.now()){
      const result=await act({action:'invite_draft',handle:editor.meta.handle});
      if(!current()||!editor.live||!view.close.current())return;
      invitation={...result.invitation,handle:result.handle};cancel.hidden=false;fallback.value=result.instruction;
    }
    try{await navigator.clipboard.writeText(fallback.value);if(view.close.current())status.textContent='已复制。到智能体对话中粘贴，并告诉它你的修改要求。';}
    catch{if(!view.close.current()||!current()||!editor.live)return;fallback.hidden=false;fallback.focus();fallback.select();status.textContent='请复制下面的请求，粘贴到智能体对话中。';}
  },'primary');
  const requests=el('div',{class:'collaboration-requests',tabindex:'-1'});
  const view=dialog('在智能体中继续这篇文稿',el('div',{class:'collaboration-panel'},explanation,el('div',{class:'collaboration-row'},copy,cancel),status,fallback,requests,el('p',{class:'subtle'},'临时协助只允许编辑这篇草稿，一小时后结束。是否加入资料由你决定，可告诉主智能体，也可在页面操作。')));
  const listHandle=editor.meta.handle;
  let signature='';
  const update=(page,after='')=>{
    if(!view.close.current()||!current()||!editor.live){if(editor.collaborationPanel===refresh)editor.collaborationPanel=null;return;}
    const nextSignature=JSON.stringify([after,page],(key,value)=>key==='handle'||key==='cursor'||key==='next'?undefined:value)+!!page.next;
    if(signature===nextSignature)return;signature=nextSignature;
    const scroll=requests.scrollTop,focused=requests.contains?.(document.activeElement);
    requests.replaceChildren(...(page.collaborations||[]).map(c=>el('div',{class:'collaboration-row'},el('span',{},`${c.connection.name} · ${label(c.state)}`),c.state==='awaiting_approval'?button('核对申请',()=>{view.close();document.getElementById('pending-entry').click();},'text-link'):c.state==='approved'?button('结束协助',async()=>{await act({action:'end_collaboration',handle:c.handle});await refreshCollaboration(editor);},'text-link'):null)));
    if(after)requests.append(button('返回第一页',()=>navigation.load(''),'text-link'));
    if(page.next)requests.append(button('下一页',()=>navigation.load(page.next),'text-link'));
    requests.scrollTop=scroll;if(focused)requests.focus?.({preventScroll:true});
  };
  const navigation=createPageNavigation(after=>query({view:'draft_collaborations',handle:listHandle,limit:20,after,refresh:true}),update,()=>view.close.current()&&current()&&editor.live);
  const refresh=async()=>{if(!view.close.current()||!current()||!editor.live){if(editor.collaborationPanel===refresh)editor.collaborationPanel=null;return;}await checkInvitation();return navigation.load();};
  editor.collaborationPanel=refresh;
  try{await refreshCollaboration(editor);}catch(error){if(view.close.current())status.textContent=errorMessage(error);}
}
