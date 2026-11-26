// Compose preview environment cards (docs/proposals/compose-preview-environments.md),
// shared by the run detail「预览环境」tab and the requirement drawer's delivery tab.
// The list comes from GET /api/runs/:id/preview-envs or /api/issues/:id/preview-envs.
const PREVIEW_ENV_STATUS={
  deploying:['部署中','Deploying','info'],
  ready:['可访问','Ready','success'],
  failed:['部署失败','Failed','error'],
  destroying:['销毁中','Destroying','warn'],
  destroyed:['已销毁','Destroyed','muted']
};
function previewEnvT(zh,en){return typeof lfT==='function'?lfT(zh,en):zh}
function previewEnvBadgeHTML(status){
  const [zh,en,tone]=PREVIEW_ENV_STATUS[status]||[status||'未知',status||'Unknown','muted'];
  return `<span class="hui-badge" data-size="sm" data-tone="${tone}"><span class="hui-badge-label">${escapeHTML(previewEnvT(zh,en))}</span></span>`;
}
function previewEnvRepoName(env){
  const repo=(state?.data?.repositories||[]).find(r=>r.id===env.repositoryId);
  return repo?.name||env.repositoryId||'—';
}
// Deploys stuck past the server's takeover window (deploy timeout + margin).
const PREVIEW_ENV_DEPLOY_STALE_MS=40*60*1000;
// Destroy is offered for ready/failed environments, as a retry for a failed
// teardown, and for a deploy whose deployer is gone; empty means no button.
function previewEnvDestroyLabel(env,now=Date.now()){
  if(env.status==='ready'||env.status==='failed')return previewEnvT('销毁','Destroy');
  if(env.status==='destroying'&&env.lastError)return previewEnvT('重试销毁','Retry teardown');
  if(env.status==='deploying'&&now-Date.parse(env.updatedAt||'')>PREVIEW_ENV_DEPLOY_STALE_MS)return previewEnvT('销毁','Destroy');
  return '';
}
function previewEnvCardHTML(env,adminUser){
  const id=escapeHTML(env.id);
  const sha=env.sha?`<code title="${escapeHTML(env.sha)}">${escapeHTML(env.sha.slice(0,8))}</code>`:'—';
  const live=env.status!=='destroyed'&&env.status!=='destroying';
  const url=env.url&&live?`<code class="preview-env-url" title="${escapeHTML(env.url)}">${escapeHTML(env.url)}</code>`:'—';
  const password=env.adminPassword
    ?`<code>${escapeHTML(adminUser)}</code> / <code data-preview-env-secret>${escapeHTML(env.adminPassword)}</code><button type="button" class="button ghost small" data-preview-env-copy="${escapeHTML(env.adminPassword)}" aria-label="${escapeHTML(previewEnvT('复制管理员密码','Copy admin password'))}">${escapeHTML(previewEnvT('复制','Copy'))}</button>`
    :`<span class="muted">${escapeHTML(previewEnvT('仅项目成员可见','Visible to project members'))}</span>`;
  const actions=[
    env.status==='ready'&&env.adminPassword?`<button type="button" class="button primary small" data-preview-env-open="${id}">${escapeHTML(previewEnvT('打开预览','Open preview'))}</button>`:'',
    previewEnvDestroyLabel(env)?`<button type="button" class="button danger-outline small" data-preview-env-destroy="${id}">${escapeHTML(previewEnvDestroyLabel(env))}</button>`:''
  ].join('');
  const error=env.lastError?`<p class="preview-env-error">${escapeHTML(env.lastError)}</p>`:'';
  return `<article class="preview-env-card" data-preview-env="${id}"><header><b>${escapeHTML(previewEnvRepoName(env))}</b>${previewEnvBadgeHTML(env.status)}</header><div class="detail-grid"><div class="detail-fact"><span>${escapeHTML(previewEnvT('提交','Commit'))}</span><b>${sha}</b></div><div class="detail-fact"><span>${escapeHTML(previewEnvT('分支','Branch'))}</span><b>${escapeHTML(env.branch||'—')}</b></div><div class="detail-fact"><span>URL</span><b>${url}</b></div><div class="detail-fact"><span>${escapeHTML(previewEnvT('管理员账号','Admin account'))}</span><b>${password}</b></div></div>${error}${actions?`<div class="preview-env-actions">${actions}</div>`:''}</article>`;
}
// list: undefined while loading, {error} on failure, or the API payload.
function previewEnvCardsHTML(list){
  if(!list)return `<div class="workspace-loading"><span class="workspace-spinner"></span><b>${escapeHTML(previewEnvT('正在读取预览环境','Loading preview environments'))}</b></div>`;
  if(list.error)return `<div class="workspace-unavailable"><span>!</span><b>${escapeHTML(previewEnvT('预览环境加载失败','Failed to load preview environments'))}</b><p>${escapeHTML(list.error)}</p><button class="button ghost small" data-preview-env-refresh>${escapeHTML(previewEnvT('重试','Retry'))}</button></div>`;
  const items=list.items||[];
  if(!items.length){
    const hint=list.enabled===false&&list.reason?list.reason:previewEnvT('「部署预览环境」步骤完成后，每个含 compose 文件的仓库会在这里出现一个环境。','After the preview deploy step, each repository with a compose file gets an environment here.');
    return `<div class="workspace-unavailable"><span>◇</span><b>${escapeHTML(previewEnvT('还没有预览环境','No preview environments yet'))}</b><p>${escapeHTML(hint)}</p><button class="button ghost small" data-preview-env-refresh>${escapeHTML(previewEnvT('刷新','Refresh'))}</button></div>`;
  }
  return `<div class="preview-env-list">${items.map(env=>previewEnvCardHTML(env,list.adminUser||'preview')).join('')}</div>`;
}
async function fetchPreviewEnvs(scope,id){
  try{return await api(`/api/${scope}/${encodeURIComponent(id)}/preview-envs`,{silent:true})}
  catch(err){return {error:String(err?.message||err)}}
}
// The tab is opened before the ticket request so popup blockers keep it.
async function openPreviewEnv(envId,button){
  if(button?.disabled)return;
  if(button)button.disabled=true;
  const tab=window.open('','_blank');
  if(tab)tab.opener=null;
  try{
    const res=await api(`/api/preview-envs/${encodeURIComponent(envId)}/ticket`,{method:'POST',body:'{}'});
    if(tab)tab.location.href=res.url;else window.open(res.url,'_blank','noopener');
  }catch(err){
    tab?.close();
    toast(previewEnvT('打开预览失败：','Could not open preview: ')+String(err?.message||err),'err');
  }finally{
    if(button)button.disabled=false;
  }
}
async function destroyPreviewEnv(envId,reload){
  const ok=await afConfirm({title:previewEnvT('销毁预览环境','Destroy preview environment'),message:previewEnvT('将停止并删除该预览环境的容器、卷和路由，URL 随之失效。重新运行 SOP 会以同一地址重建。','Stops and removes the containers, volumes and route of this environment; its URL stops working. Rerunning the SOP rebuilds it at the same address.'),danger:true,confirmText:previewEnvT('确认销毁','Destroy')});
  if(!ok)return;
  try{
    await api(`/api/preview-envs/${encodeURIComponent(envId)}`,{method:'DELETE'});
    toast(previewEnvT('已开始销毁预览环境','Teardown started'),'ok');
  }catch(err){
    toast(String(err?.message||err),'err');
  }
  reload?.();
}
function bindPreviewEnvCards(host,reload){
  if(!host)return;
  host.querySelectorAll('[data-preview-env-open]').forEach(b=>b.onclick=()=>openPreviewEnv(b.dataset.previewEnvOpen,b));
  host.querySelectorAll('[data-preview-env-copy]').forEach(b=>b.onclick=()=>copyText(b.dataset.previewEnvCopy,previewEnvT('管理员密码已复制','Admin password copied')));
  host.querySelectorAll('[data-preview-env-destroy]').forEach(b=>b.onclick=()=>destroyPreviewEnv(b.dataset.previewEnvDestroy,reload));
  host.querySelectorAll('[data-preview-env-refresh]').forEach(b=>b.onclick=()=>reload?.());
}
