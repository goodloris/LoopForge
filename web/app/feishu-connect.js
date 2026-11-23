// Personal settings → 飞书: connect the member's Feishu identity so agents in
// conversation workspaces can call lark-cli as whoever sent the round. The
// platform keeps the grant; this card only shows its state and starts the
// OAuth flow (same tab), which returns to /?feishu=connected#/settings.

const FEISHU_PRESET_LABELS = {readonly: '只读', readwrite: '读写'};

function feishuConnectHTML(){
  return '<section class="credential-panel feishu-connect credential-enter" id="feishuConnectPanel" style="--i:2" hidden aria-labelledby="feishuConnectTitle"><header class="credential-section-head"><div><h2 id="feishuConnectTitle">飞书</h2><p>连接后，对话工作区里的 Agent 以你的飞书身份读取文档、日历和消息；不连接时只有应用（bot）身份。</p></div></header><div class="feishu-connect-body" data-feishu-body role="status" aria-live="polite">正在读取飞书授权…</div></section>';
}

function feishuStatusBadgeHTML(status){
  const [tone,label]=status.connected?['success','已连接']:status.status==='expired'?['warn','已过期']:['neutral','未连接'];
  return `<span class="hui-badge" data-tone="${tone}" data-size="sm"><span class="hui-lamp"></span><span class="hui-badge-label">${label}</span></span>`;
}

function feishuPresetPickerHTML(current){
  const pick=current==='readwrite'?'readwrite':'readonly';
  return `<fieldset class="feishu-presets"><legend>权限档位</legend>`
    +`<label><input type="radio" name="feishuPreset" value="readonly"${pick==='readonly'?' checked':''}><span><b>只读（推荐）</b><small>读文档、知识库、云空间、表格、日历和消息记录。</small></span></label>`
    +`<label><input type="radio" name="feishuPreset" value="readwrite"${pick==='readwrite'?' checked':''}><span><b>读写</b><small>另可创建和编辑文档、建日程、以你的名义发消息。对话工作区里的命令会自动执行，不会逐条请你确认。</small></span></label></fieldset>`;
}

function feishuConnectURL(preset){
  return '/api/users/me/feishu/connect?preset='+(preset==='readwrite'?'readwrite':'readonly');
}

function feishuConnectBodyHTML(status){
  const head=`<div class="feishu-connect-state">${feishuStatusBadgeHTML(status)}`
    +(status.name?`<b>${escapeHTML(status.name)}</b>`:'')
    +(status.preset?`<span class="feishu-connect-preset">${escapeHTML(FEISHU_PRESET_LABELS[status.preset]||status.preset)}</span>`:'')+'</div>';
  const scopes=status.connected&&status.scopes?.length?`<p class="feishu-connect-scopes">已授权：<code>${status.scopes.map(escapeHTML).join(' ')}</code></p>`:'';
  const note=status.status==='expired'?'<p class="feishu-connect-note">授权已失效，请重新授权。</p>':'';
  const actions=`<div class="feishu-connect-actions"><a class="credential-btn primary credential-ink" data-feishu-connect href="${feishuConnectURL(status.preset)}">${status.connected||status.status==='expired'?'重新授权':'连接飞书'}</a>`
    +(status.connected?'<button type="button" class="credential-btn secondary credential-ink" data-feishu-disconnect>断开</button>':'')+'</div>';
  return head+scopes+note+feishuPresetPickerHTML(status.preset)+actions;
}

async function hydrateFeishuConnect(panel){
  if(!panel)return;
  const body=panel.querySelector('[data-feishu-body]'),actor=Number(state.currentUser?.id);
  let status;
  try{status=await api('/api/users/me/feishu',{silent:true})}catch{return}
  if(!panel.isConnected||Number(state.currentUser?.id)!==actor)return;
  panel.hidden=!status?.enabled;
  if(!status?.enabled)return;
  body.innerHTML=feishuConnectBodyHTML(status);
  // The connect link follows the chosen preset; the OAuth flow runs in this tab.
  const connect=body.querySelector('[data-feishu-connect]');
  body.querySelectorAll('input[name="feishuPreset"]').forEach(input=>input.addEventListener('change',()=>{if(input.checked)connect.href=feishuConnectURL(input.value)}));
  const disconnect=body.querySelector('[data-feishu-disconnect]');
  if(disconnect)disconnect.onclick=async()=>{
    if(!await afConfirm({title:'断开飞书',message:'断开后，下一轮对话起 Agent 不再使用你的飞书身份。平台会删除保存的授权并通知飞书撤销。',danger:true,confirmText:'断开'}))return;
    disconnect.disabled=true;
    try{await api('/api/users/me/feishu',{method:'DELETE'});toast('已断开飞书');hydrateFeishuConnect(panel)}
    catch(error){disconnect.disabled=false;toast(error.message)}
  };
}

function mountFeishuConnect(host){
  if(!host)return;
  host.insertAdjacentHTML('beforeend',feishuConnectHTML());
  const panel=host.querySelector('#feishuConnectPanel');
  if(new URLSearchParams(location.search).get('feishu')==='connected'){
    history.replaceState(null,'',location.pathname+location.hash);
    toast('飞书已连接','ok');
  }
  hydrateFeishuConnect(panel);
}
