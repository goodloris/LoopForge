// Personal settings → 飞书: connect the member's Feishu identity so agents in
// conversation workspaces can call lark-cli as whoever sent the round. The
// platform keeps the grant; this card only shows its state and starts the
// OAuth flow (same tab), which returns to /?feishu=connected#/settings.
// 「同时用于登录」 also makes the proven Feishu identity a sign-in of this
// account (or binds it alone later); unbinding needs another way in.

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

function feishuConnectURL(preset,login){
  return '/api/users/me/feishu/connect?preset='+(preset==='readwrite'?'readwrite':'readonly')+(login?'&login=1':'');
}

function feishuLoginHTML(status){
  if(status.loginBound){
    const note=status.canUnbindLogin?'':'<small class="feishu-connect-note">飞书是这个账号唯一的登录方式，不能解绑。</small>';
    return `<div class="feishu-login"><div class="feishu-connect-state"><span class="hui-badge" data-tone="info" data-size="sm"><span class="hui-badge-label">飞书登录</span></span><span>已用于登录${status.loginName?` · ${escapeHTML(status.loginName)}`:''}</span><button type="button" class="credential-btn secondary credential-ink" data-feishu-unbind${status.canUnbindLogin?'':' disabled'}>解绑登录</button></div>${note}</div>`;
  }
  if(status.connected)return '<div class="feishu-login"><div class="feishu-connect-state"><span>还没用于登录。绑定后可以在登录页用飞书登录这个账号。</span><a class="credential-btn secondary credential-ink" data-feishu-bind href="/api/users/me/feishu/bind">同时用于登录</a></div></div>';
  return '';
}

function feishuConnectBodyHTML(status){
  const head=`<div class="feishu-connect-state">${feishuStatusBadgeHTML(status)}`
    +(status.name?`<b>${escapeHTML(status.name)}</b>`:'')
    +(status.preset?`<span class="feishu-connect-preset">${escapeHTML(FEISHU_PRESET_LABELS[status.preset]||status.preset)}</span>`:'')+'</div>';
  const scopes=status.connected&&status.scopes?.length?`<p class="feishu-connect-scopes">已授权：<code>${status.scopes.map(escapeHTML).join(' ')}</code></p>`:'';
  const note=status.status==='expired'?'<p class="feishu-connect-note">授权已失效，请重新授权。</p>':'';
  const loginOption=status.loginBound||status.connected?'':'<label class="feishu-login-opt"><input type="checkbox" data-feishu-login> 同时用于登录（之后可以在登录页用飞书登录这个账号）。不勾选时，之后用飞书登录会新建另一个账号</label>';
  const actions=`<div class="feishu-connect-actions"><a class="credential-btn primary credential-ink" data-feishu-connect href="${feishuConnectURL(status.preset)}">${status.connected||status.status==='expired'?'重新授权':'连接飞书'}</a>`
    +(status.connected?'<button type="button" class="credential-btn secondary credential-ink" data-feishu-disconnect>断开</button>':'')+'</div>';
  return head+scopes+note+feishuPresetPickerHTML(status.preset)+loginOption+actions+feishuLoginHTML(status);
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
  const connect=body.querySelector('[data-feishu-connect]'),login=body.querySelector('[data-feishu-login]');
  const syncConnect=()=>{connect.href=feishuConnectURL(body.querySelector('input[name="feishuPreset"]:checked')?.value,login?.checked)};
  body.querySelectorAll('input[name="feishuPreset"],[data-feishu-login]').forEach(input=>input.addEventListener('change',syncConnect));
  const unbind=body.querySelector('[data-feishu-unbind]');
  if(unbind)unbind.onclick=async()=>{
    if(!await afConfirm({title:'解绑飞书登录',message:'解绑后不能再用飞书登录这个账号；Agent 使用的飞书授权不受影响。',danger:true,confirmText:'解绑'}))return;
    unbind.disabled=true;
    try{await api('/api/users/me/feishu/bind',{method:'DELETE'});toast('已解绑飞书登录');hydrateFeishuConnect(panel)}
    catch(error){unbind.disabled=false;toast(error.message)}
  };
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
  const done={connected:'飞书已连接',bound:'已绑定飞书登录','connected-bound':'飞书已连接，并已用于登录'}[new URLSearchParams(location.search).get('feishu')];
  if(done){
    history.replaceState(null,'',location.pathname+location.hash);
    toast(done,'ok');
  }
  hydrateFeishuConnect(panel);
}
