import {act, query, resolve, text, replace, pause, operationID, storage, scope} from './api.js';
import {el, button, row, prose, dialog, confirm, notice, download} from './ui.js';

const rescueKey = 'ownward.owner-input';
// The running window owns its rescue even when browser storage is unavailable.
// Storage is only the best-effort copy that can survive a page reload.
let rescueValue=storage.get(rescueKey),rescueStored=true,cleanupPending=false;
export function rescuedInput() { try { return JSON.parse(rescueValue); } catch { return null; } }
export const rescueNeedsWindow=()=>cleanupPending||!!rescuedInput()&&!rescueStored;
export const rescueCleanupPending=()=>cleanupPending;
export function retryRescueCleanup(){if(cleanupPending)cleanupPending=storage.remove(rescueKey)===false;return !cleanupPending;}
function eraseStoredRescue(){cleanupPending=storage.remove(rescueKey)===false;}
// Automatic cleanup belongs to one draft. Only an explicit window-wide exit
// clears without a reference; moving to a successor names the former owner.
export function clearRescue(reference) { if(reference===undefined||rescuedInput()?.reference===reference){rescueValue=null;rescueStored=true;eraseStoredRescue();} }
function storeRescue(rescue,previousReference=rescue.reference) {
  const previous=rescuedInput();
  if(previous&&previous.reference!==previousReference)return false;
  const value=JSON.stringify(rescue),stored=storage.set(rescueKey,value);
  // A failed successor transfer must leave the former owner's snapshot intact.
  if(!stored&&previousReference!==rescue.reference)return false;
  rescueValue=value;rescueStored=stored;if(stored)cleanupPending=false;return stored;
}
export function retainReceipt(rescue){
  const retained=storeRescue({reference:rescue.reference,publishID:rescue.publishID});
  // An unavailable body's privacy does not depend on storage accepting a new
  // receipt. If replacement fails, remove only that body's old rescue.
  if(!retained&&rescuedInput()?.reference===rescue.reference)eraseStoredRescue();
  return retained;
}

// One editor, one ordered write stream. A response only acknowledges the exact
// submitted string; typing that happened in flight remains dirty.
export class Editor {
  constructor(meta, content, hooks, rescue) {
    this.meta=meta;this.base=content;this.value=content;this.hooks=hooks;this.live=true;
    this.busy=null;this.queue=Promise.resolve();this.conflict=null;this.timer=null;this.lastWrite=0;this.composing=false;this.paused=false;this.quarantined=false;this.finishing=false;this.refreshPending=!!rescue?.refreshPending||rescue?.pendingSave!==undefined;this.pendingSave=rescue?.pendingSave;
    this.publishID=rescue?.publishID || null;this.rebase=rescue?.rebase||null;this.discardPending=rescue?.discardPending||null;
    this.input=el('textarea',{'aria-label':'文稿正文',class:'draft-input',spellcheck:false,value:content});
    this.status=el('span',{class:'save-state',role:'status'},'输入已保存');
    this.conflictBox=el('section',{class:'conflict-box',hidden:true,'aria-label':'内容核对'});
    this.previewButton=button('预览并存入资料',()=>this.preview(),'primary');
    this.grantButton=button('交给接入者续写',()=>hooks.grant(this));
    this.discardButton=button('弃稿',()=>this.discard(),'danger-quiet');
    this.retryButton=button('重试保存',()=>this.recoveryAction.run());this.retryButton.hidden=true;
    this.node=el('section',{class:'editor'},el('header',{class:'editor-toolbar'},row(button('返回文稿',()=>hooks.leave()),el('span',{class:'tag'},meta.target?'正在编辑资料':'进行中的文稿')),row(this.status,this.retryButton)),
      this.conflictBox,this.input,el('footer',{class:'editor-footer'},el('p',{class:'subtle'},'输入自动保存为文稿；确认后，整篇存入资料。'),row(this.grantButton,this.discardButton,this.previewButton)));
    this.input.addEventListener('compositionstart',()=>{this.composing=true;});
    this.input.addEventListener('compositionend',()=>{this.composing=false;this.changed();});
    this.input.addEventListener('input',()=>this.changed());
    if (rescue && rescue.reference===meta.reference && typeof rescue.text==='string') {
      this.value=rescue.text;this.input.value=this.value;
      if (this.refreshPending) {this.setStatus('保存结果等待核对；输入已保留');this.retryButton.hidden=false;}
      else if (rescue.version!==meta.version && this.value!==content) this.showConflict(meta,content);
      else if(this.dirty) {this.setStatus('已救回尚未同步的输入');this.schedule();}
    }
    if(this.publishID) {this.setStatus('上次存入结果待核对');this.retryButton.hidden=false;}
    if(this.discardPending)this.discardStatus();
    this.updateButtons();
  }
  get dirty(){return this.value!==this.base;}
  // One current operation determines both the visible action and its effect.
  get recoveryAction(){
    if(this.discardPending)return {label:'重试弃稿',run:()=>this.retryDiscard()};
    if(this.publishID)return {label:'核对存入结果',run:()=>this.reconcile()};
    if(this.refreshPending)return {label:'重试核对文稿',run:()=>this.reconcile(true)};
    return {label:'重试保存',run:()=>this.save()};
  }
  setStatus(value){if(this.live)this.status.textContent=value+(cleanupPending?'；浏览器暂存尚待清理，请勿刷新或关闭':rescueNeedsWindow()?'；暂存仅在当前窗口，请勿刷新或关闭':'');}
  updateButtons(){
    this.retryButton.textContent=this.recoveryAction.label;
    this.previewButton.disabled=!this.value.trim()||!!this.conflict||!!this.publishID&&!this.retryPublishAllowed||this.composing||this.quarantined||this.finishing||!!this.discardPending||this.refreshPending;
    this.input.readOnly=!!this.publishID&&!this.conflict||this.quarantined||this.finishing||!!this.discardPending||this.refreshPending;
    this.discardButton.disabled=this.finishing||!!this.discardPending||!!this.unavailableBody||this.refreshPending;
    this.grantButton.disabled=this.discardButton.disabled||!!this.publishID;this.retryButton.disabled=this.finishing;
  }
  snapshot(){return {handle:this.meta.handle,version:this.meta.version,text:this.value,reference:this.meta.reference,publishID:this.publishID};}
  matches(snapshot){return this.live&&this.meta.reference===snapshot.reference&&this.meta.version===snapshot.version&&this.value===snapshot.text&&this.publishID===snapshot.publishID;}
  requireSnapshot(snapshot){if(!this.matches(snapshot)||this.composing||this.quarantined)throw new Error('内容或操作有变化，请重新核对后继续。');}
  // A confirmed operation owns the editor until its outcome is known. Closing
  // its dialog does not release this ownership or authorize deleting new input.
  async finish(label,task,discard=false){
    if(!this.live||this.finishing||this.refreshPending||this.discardPending&&!discard)throw new Error('正在完成这篇文稿的操作，请稍候。');
    const current=scope();
    this.finishing=true;clearTimeout(this.timer);this.updateButtons();this.setStatus(label);
    try{return await this.serial(async()=>{
      if(!current())return;
      try{return await task();}
      catch(error){if(this.live&&current()&&error.status===409)await this.refresh(true);throw error;}
    });}
    catch(error){if(this.live)this.setStatus(this.discardPending?'弃稿结果待核对':this.publishID?'存入结果待核对':this.refreshPending?'文稿已有更新，等待核对':this.conflict?'内容已有更新，需要核对':this.dirty?'操作未完成；输入已保留':'操作未完成；已保存内容仍在');throw error;}
    finally{this.finishing=false;if(this.live){this.updateButtons();if(this.dirty)this.schedule();}}
  }
  persist(){
    if(!this.live)return;
    if(this.unavailableBody){retainReceipt({reference:this.meta.reference,publishID:this.publishID});return;}
    if(!this.dirty&&!this.conflict&&!this.publishID&&!this.rebase&&!this.discardPending&&!this.refreshPending&&this.pendingSave===undefined){clearRescue(this.meta.reference);return;}
    const ok=storeRescue({reference:this.meta.reference,target_reference:this.meta.target_reference,version:this.meta.version,text:this.value,publishID:this.publishID,rebase:this.rebase,discardPending:this.discardPending,refreshPending:this.refreshPending,pendingSave:this.pendingSave});
    if(!ok)this.setStatus('尚未同步');
  }
  changed(){if(this.input.readOnly){this.input.value=this.value;return;}this.value=this.input.value;this.persist();this.updateButtons();if(!this.conflict){this.setStatus('尚未同步');this.schedule();}}
  schedule(){clearTimeout(this.timer);if(this.live&&!this.paused&&!this.quarantined&&!this.composing&&!this.conflict&&!this.publishID&&!this.finishing&&!this.discardPending&&!this.refreshPending)this.timer=setTimeout(()=>this.save().catch(()=>{}),1100);}
  serial(task){
    const job=this.queue.then(async()=>{if(!this.live)return;this.busy=job;try{return await task();}finally{if(this.busy===job)this.busy=null;}});
    this.queue=job.catch(()=>{});return job;
  }
  suspend(){this.paused=true;clearTimeout(this.timer);this.persist();}
  async resume(){this.paused=false;await this.reconcile();if(this.dirty)this.schedule();}
  quarantine(){this.quarantined=true;this.node.hidden=true;clearTimeout(this.timer);this.persist();this.updateButtons();}
  async save(){
    return this.serial(async()=>{
      if(this.publishID||this.discardPending||!this.dirty||this.conflict||this.composing||this.quarantined||this.finishing||this.refreshPending)return;
      await this.write();
    });
  }
  async write(){
    const current=scope();
    await pause(Math.max(0,1100-(Date.now()-this.lastWrite)));
    if(!this.live||!current()||this.conflict||this.composing||this.quarantined)return;
    const value=this.value,handle=this.meta.handle;
    this.setStatus('正在保存…');this.lastWrite=Date.now();
    // Own the uncertainty before sending, including a lost acknowledgement.
    // Typing remains available while this request is in flight.
    this.pendingSave=value;this.persist();
    let accepted=false;
    try{
      const result=await replace(handle,value);
      accepted=true;
      if(!this.live)return;
      // Acceptance does not complete the concurrency check. Keep the exact
      // submitted string apart from later typing until metadata + text agree.
      this.meta={...this.meta,handle:result.handle};this.pendingSave=value;this.refreshPending=true;
      this.persist();this.updateButtons();await this.refresh();
    }catch(error){
      if(!this.live)return;
      if(!accepted&&error.status===429){this.pendingSave=undefined;this.refreshPending=false;}
      else this.refreshPending=true;
      if(!accepted&&error.status===409&&current()){this.pendingSave=undefined;this.setStatus('文稿已有更新，等待核对');this.persist();this.updateButtons();this.retryButton.hidden=false;await this.refresh(true);return;}
      this.setStatus(this.refreshPending?'保存结果等待核对；输入已保留':error.status===429?'正在等待保存；输入已保留':'保存未完成；输入已保留');this.persist();this.retryButton.hidden=false;this.updateButtons();
      if(error.status===429)this.schedule();
      throw error;
    }
  }
  async flush(){
    clearTimeout(this.timer);
    if(this.finishing||this.discardPending||this.refreshPending)throw new Error('正在完成这篇文稿的操作，请稍候。');
    if(this.composing)throw new Error('请先完成正在输入的文字。');
    await this.save();
    if(this.dirty||this.conflict||this.publishID&&!this.retryPublishAllowed||this.refreshPending||this.pendingSave!==undefined||this.discardPending||this.finishing||this.composing)throw new Error('请先完成保存或核对，再继续。');
  }
  async reconcile(force=false){
    const current=scope();return this.serial(()=>current()?this.refresh(force):undefined);
  }
  async refresh(force=false){
    if(!this.live)return;
    force=force||this.refreshPending;
    if(this.unavailableBody){await this.completeDiscard();return;}
    if(this.discardPending){await this.readDiscard();return;}
    if(this.publishID){await this.readPublication();return;}
    const page=await resolve(this.meta.reference);
    if(!this.live)return;
    if(page.unavailable){this.unavailable();return;}
    const meta=page.drafts[0];
    if(!force&&meta.version===this.meta.version){this.quarantined=false;this.node.hidden=false;this.updateButtons();return;}
    const content=await text('draft_content',meta.handle,()=>this.live);
    if(!this.live)return;
    const submitted=this.pendingSave;this.pendingSave=undefined;
    this.refreshPending=false;this.quarantined=false;this.node.hidden=false;
    if(content===this.value||content===submitted){
      this.meta=meta;this.base=content;this.conflict=null;this.conflictBox.hidden=true;this.conflictBox.replaceChildren();this.persist();this.retryButton.hidden=true;
      this.setStatus(this.dirty?'尚未同步':'输入已保存');this.updateButtons();if(this.dirty)this.schedule();return;
    }
    if(this.dirty||this.conflict||force){this.showConflict(meta,content);return;}
    this.meta=meta;this.base=content;this.value=content;this.input.value=content;
    this.setStatus('已接续最新内容');this.updateButtons();
  }
  showConflict(meta,content){
    clearTimeout(this.timer);const conflict=this.conflict={meta,content};this.persist();this.setStatus('内容已有更新，需要核对');
    this.retryButton.hidden=!this.publishID;
    const choose=keep=>{const snapshot=this.snapshot();return this.finish('正在保存核对结果…',async()=>{
      this.requireSnapshot(snapshot);if(this.conflict!==conflict)throw new Error('另一处内容又有变化，请重新核对。');
      this.meta=meta;this.base=content;if(!keep){this.value=content;this.input.value=content;}
      this.finishConflict();if(this.dirty)await this.write();
    });};
    this.conflictBox.hidden=false;
    this.conflictBox.replaceChildren(el('h2',{},'两份文字都在，核对后再继续'),el('p',{},'下方编辑区保留你刚写的文字。可以先合并，再明确保存；不会自动覆盖。'),
      el('details',{},el('summary',{},'查看另一处已保存的全文'),prose(content)),row(
        button('采用另一处的文字',()=>choose(false)),
        button('保留编辑区文字并保存',()=>choose(true),'primary')));
    this.updateButtons();
  }
  finishConflict(){this.conflict=null;this.conflictBox.hidden=true;this.conflictBox.replaceChildren();this.retryPublishAllowed=!!this.publishID;this.persist();this.setStatus(this.dirty?'尚未同步':'输入已保存');this.updateButtons();}
  unavailable(){clearRescue(this.meta.reference);this.destroy();this.hooks.unavailable();}
  async preview(){
    const current=scope();
    await this.flush();if(!this.live||!current())return;
    await this.reconcile(true);if(this.conflict||!this.live)return;
    if(!current()||this.dirty||this.composing)return;
    const snapshot=this.snapshot();
    let before='',source=null;
    if(this.meta.target){
      try{
        before=await text('content',this.meta.target,()=>this.live);
        source=(await query({view:'source',handle:this.meta.target})).source;
      }catch(error){if(error.status===409){await this.targetConflict();return;}throw error;}
    }
    if(!current())return;this.requireSnapshot(snapshot);
    const content=el('div',{},el('p',{class:'preview-note'},source?.preserve_original?'原件保留为证据，修订内容成为当前使用的内容。':this.meta.target?'整篇修改将成为当前内容。':'整篇文字将存入资料，之后可以继续编辑。'),
      el('div',{class:'comparison'},el('section',{},el('h3',{},'现在'),prose(before||'尚未存入资料')),el('section',{},el('h3',{},'将成为'),prose(snapshot.text))));
    const d=dialog('确认整篇内容',content,[{label:'返回编辑',run:close=>close()},{label:'确认存入',style:'primary',run:async close=>{
      if(!this.live||!current()||!close.current())return close();
      await this.finish('正在核对存入结果…',async()=>{
        this.requireSnapshot(snapshot);if(this.dirty||this.conflict)throw new Error('请重新预览已保存的全文。');
        this.publishID=this.publishID||operationID();this.retryPublishAllowed=false;this.persist();this.updateButtons();this.retryButton.hidden=false;
        try{const result=await act({action:'publish_draft',handle:snapshot.handle,operation_id:this.publishID});if(this.live){clearRescue(this.meta.reference);this.destroy();close();await this.hooks.published(result.handle);}}
        catch(error){close();if(this.live&&current())await this.readPublication();}
      });
    }}]);
    this.previewDialog=d;
  }
  async targetConflict(){
    const currentScope=scope();
    const page=await resolve(this.meta.target_reference);
    if(!this.live||!currentScope())return;if(page.unavailable){this.unavailable();return;}
    const target=page.assets[0],current=await text('content',target.handle,()=>this.live);
    if(!this.live||!currentScope())return;
    const snapshot=this.snapshot(),mine=snapshot.text,previousHandle=snapshot.handle;
    dialog('资料已在另一处更新',el('div',{},el('p',{},'原稿仍然保留。请核对双方全文，再决定是否以最新资料为基础继续编辑。'),el('div',{class:'comparison'},el('section',{},el('h3',{},'当前资料'),prose(current)),el('section',{},el('h3',{},'你的文稿'),prose(mine)))),[
      {label:'返回核对',run:close=>close()},
      {label:'以最新资料为基础继续',style:'primary',run:async close=>{
        if(!this.live||!currentScope()||!close.current())return;
        await this.finish('正在接续核对后的文字…',async()=>{
        this.requireSnapshot(snapshot);
        if(!this.rebase){const created=await act({action:'create_draft',target:target.handle});this.rebase={reference:created.reference};this.persist();}
        const next=await resolve(this.rebase.reference);
        if(!this.live||!currentScope())return;
        if(next.unavailable){this.rebase=null;this.persist();throw new Error('用于接续的文稿已不可用，请重新核对。');}
        let meta=next.drafts[0],body=await text('draft_content',meta.handle,()=>this.live&&currentScope());
        if(body!==mine){
          if(body!==current){throw new Error('接续文稿也有新变化。原稿与接续稿都已保留，请从文稿页核对。');}
          await replace(meta.handle,mine);
          const verified=await resolve(meta.reference);
          if(verified.unavailable)throw new Error('接续文稿已不可用，原稿仍保留，请重新核对。');
          meta=verified.drafts[0];
          body=await text('draft_content',meta.handle,()=>this.live&&currentScope());
        }
        if(!this.live||!currentScope())return;this.requireSnapshot(snapshot);
        // A revision and its full text form a pair. If a second writer changed
        // the successor, reopening must enter conflict, never autosave over it.
        const rescue={reference:meta.reference,target_reference:meta.target_reference,version:body===mine?meta.version:null,text:mine};
        if(!storeRescue(rescue,this.meta.reference))throw new Error('接续暂存未完成，原稿仍保留，请重试。');
        this.destroy();close();
        // Transfer the protected workspace before awaiting old-draft cleanup.
        // A detached successor still owns its rescue and blocks unsafe switches.
        await this.hooks.rebased(meta,body,rescue);
        try{await act({action:'discard_draft',handle:previousHandle});}catch(error){if(error.status!==-1)notice('新文稿已完整保存；旧稿有变化，已保留供你核对。');}
        });
      }}]);
  }
  async readPublication(){
    if(!this.publishID)return;
    const current=scope();
    this.setStatus('存入结果待核对');this.retryButton.hidden=false;this.updateButtons();
    const page=await query({view:'publish_receipt',operation_id:this.publishID});
    if(!this.live||!current())return;
    const result=page.publication;
    if(result.state==='completed'||result.state==='changed'){
      clearRescue(this.meta.reference);this.destroy();notice(result.state==='changed'?'已确认上次存入；这份资料后来又有更新。':'已确认上次存入成功。');await this.hooks.published(result.asset);return;
    }
    if(result.state==='unavailable'){this.unavailable();return;}
    this.setStatus('尚未确认存入结果，请核对');
    const draft=await resolve(this.meta.reference);
    if(!this.live||!current())return;
    if(!draft.unavailable){
      const meta=draft.drafts[0],body=await text('draft_content',meta.handle,()=>this.live&&current());
      if(!this.live||!current())return;
      this.refreshPending=false;this.quarantined=false;this.node.hidden=false;
      if(body!==this.value){this.showConflict(meta,body);}
      else{this.meta=meta;this.base=body;this.retryPublishAllowed=true;}
      this.persist();this.retryButton.hidden=false;this.updateButtons();
      notice('尚未确认上次存入。请重新核对全文；确认后会重试原次存入，不自动重复发布。');
    }
    else{
      const retained=retainReceipt({reference:this.meta.reference,publishID:this.publishID});
      this.destroy();await this.hooks.pendingReceipt(retained);
    }
  }
  async discard(){
    if(this.finishing||this.discardPending)return;
    const current=scope();
    await this.queue;if(!this.live||this.finishing||!current())return;
    const snapshot=this.snapshot();
    await confirm('弃掉这篇文稿？',el('p',{},'正在进行的这篇文稿会被删除。已入库的原资料不受影响。'),'确认弃稿',async()=>{
      if(!current())return;
      await this.finish('正在弃稿…',async()=>{
        this.requireSnapshot(snapshot);this.discardPending={handle:snapshot.handle,version:snapshot.version};this.persist();
        await this.submitDiscard();
      });
    },true);
  }
  discardStatus(){this.setStatus('弃稿结果待核对；确认后可重试');this.retryButton.hidden=false;this.updateButtons();}
  async readDiscard(){
    const current=scope(),page=await resolve(this.meta.reference);if(!this.live||!current())return;
    if(page.unavailable){
      // A missing draft can also mean the pending publication won the race.
      // Its receipt, not disappearance alone, determines the visible outcome.
      await this.completeDiscard();return;
    }
    if(page.drafts[0].version!==this.discardPending.version){
      // A newer revision cannot be deleted by the outstanding conditional
      // request. Preserve both texts instead of extending the old approval.
      this.discardPending=null;this.refreshPending=true;
      this.setStatus('文稿已有更新，等待核对');this.updateButtons();this.persist();await this.refresh(true);return;
    }
    this.quarantined=false;this.node.hidden=false;
    this.discardPending.handle=page.drafts[0].handle;this.persist();this.discardStatus();
  }
  async submitDiscard(){
    const current=scope();this.discardStatus();
    try{await act({action:'discard_draft',handle:this.discardPending.handle});}
    catch(error){if(this.live&&current()){await this.readDiscard();if(!this.live)return;}throw error;}
    if(!this.live||!current())return;await this.completeDiscard();
  }
  async completeDiscard(){
    // A missing draft can mean either deletion or publication. Receipt absence
    // is not proof of deletion: bounded receipt retention can expire a result.
    if(this.publishID){
      // Disappearance is already authoritative. Retire the body and deletion
      // condition even if explaining the earlier publication must wait.
      this.unavailableBody=true;this.discardPending=null;this.refreshPending=false;this.pendingSave=undefined;
      this.quarantined=false;this.node.hidden=false;
      this.input.value='';this.value='';this.base='';this.conflict=null;
      this.conflictBox.replaceChildren();this.conflictBox.hidden=true;this.previewDialog?.close();
      this.persist();this.setStatus('文稿已不可用，上次存入结果待核对');this.retryButton.hidden=false;this.updateButtons();
      const current=scope(),page=await query({view:'publish_receipt',operation_id:this.publishID});
      if(!this.live||!current())return;
      const result=page.publication;
      if(result.state==='completed'||result.state==='changed'){
        clearRescue(this.meta.reference);this.destroy();notice('已确认上次存入成功；资料已保留。');await this.hooks.published(result.asset);return;
      }
      if(result.state==='unavailable'){this.unavailable();return;}
      const retained=retainReceipt({reference:this.meta.reference,publishID:this.publishID});
      this.destroy();await this.hooks.pendingReceipt(retained);return;
    }
    clearRescue(this.meta.reference);this.destroy();await this.hooks.discarded();
  }
  async retryDiscard(){
    const current=scope();
    await this.finish('正在核对弃稿结果…',async()=>{
      await this.readDiscard();if(this.live&&current()&&this.discardPending)await this.submitDiscard();
    },true);
  }
  destroy(){this.live=false;clearTimeout(this.timer);this.previewDialog?.close();this.input.value='';this.value='';this.base='';this.pendingSave=undefined;this.conflict=null;this.node.replaceChildren();}
}
