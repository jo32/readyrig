'use strict';
(() => {
 if(PUBLIC_VIEW)return;
 const tr=message=>typeof t==='function'?t(message):message;
 let cloud=null,loaded=false,busy=false,renaming=false,editing=false;
 const open=async url=>{if(new URLSearchParams(location.search).get('shell'))await api('/api/window/open-url',{url});else window.open(url,'_blank','noopener,noreferrer')};
 function render(){
  if(!cloud)return;
  const signed=!!cloud.device_id,pairing=cloud.state==='signing_in';
  if(!loaded){$('cloud-url').value=cloud.url||'';$('cloud-name').value=cloud.name||'';loaded=true}
  // Once linked the name is shown as text with an edit button; it follows renames
  // made in the web console while it is not being edited.
  if(!signed)editing=false;
  const viewing=signed&&!editing;
  $('cloud-name-text').textContent=cloud.name||'';$('cloud-name-text').title=cloud.name||'';
  $('cloud-name-view').classList.toggle('hidden',!viewing);
  $('cloud-name').parentElement.classList.toggle('hidden',viewing);
  $('cloud-name-save').classList.toggle('hidden',!editing);$('cloud-name-cancel').classList.toggle('hidden',!editing);
  $('cloud-name-edit').disabled=busy;$('cloud-name-save').disabled=$('cloud-name-cancel').disabled=renaming;
  $('cloud-status').textContent=tr({signed_out:'未登录',signing_in:'等待登录',connecting:'连接中',online:'已连接',offline:'等待重连',revoked:'已解绑',error:'登录失败'}[cloud.state]||cloud.state);
  $('cloud-message').textContent=tr(cloud.message||'');
  $('cloud-email').textContent=cloud.email||'';
  $('cloud-url').disabled=signed||pairing||busy;
  $('cloud-name').disabled=pairing||busy||renaming;
  $('cloud-login').classList.toggle('hidden',signed||pairing);$('cloud-login').disabled=busy;
  $('cloud-disconnect').classList.toggle('hidden',!signed&&!pairing&&cloud.state!=='error');$('cloud-disconnect').disabled=busy;
  $('cloud-open').classList.toggle('hidden',!cloud.url||!signed&&!pairing);$('cloud-open').textContent=tr(pairing?'继续浏览器登录':'打开网页控制台');
  $('cloud-code').textContent=cloud.code?tr('登录验证码')+' · '+cloud.code:'';
  $('cloud-heartbeat').textContent=signed&&cloud.last_heartbeat&&!cloud.last_heartbeat.startsWith('0001')?tr('最近心跳')+' · '+new Date(cloud.last_heartbeat).toLocaleString(readyRigI18n.locale):'';
 }
 async function refreshCloud(){try{cloud=await api('/api/cloud');render()}catch(e){$('cloud-message').textContent=e.message}}
 $('cloud-login').onclick=async()=>{
  busy=true;render();$('cloud-error').textContent='';
  try{cloud=await api('/api/cloud/login',{url:$('cloud-url').value,name:$('cloud-name').value});render();await open(cloud.login_url)}catch(e){$('cloud-error').textContent=e.message}finally{busy=false;render()}
 };
 function startEdit(){editing=true;$('cloud-error').textContent='';$('cloud-name').value=cloud.name||'';render();$('cloud-name').focus();$('cloud-name').select()}
 function cancelEdit(){editing=false;$('cloud-name').value=cloud.name||'';render()}
 async function rename(){
  const name=$('cloud-name').value.trim();
  if(!cloud?.device_id||renaming)return;
  if(name===cloud.name)return cancelEdit();
  renaming=true;render();$('cloud-error').textContent='';
  try{cloud=await api('/api/cloud/rename',{name});editing=false;toast(tr(cloud.name_pending?'电脑名称已保存，联网后同步到云端':'电脑名称已更新'))}catch(e){$('cloud-error').textContent=e.message}finally{renaming=false;render()}
 }
 $('cloud-name-edit').onclick=startEdit;
 $('cloud-name-save').onclick=()=>void rename();
 $('cloud-name-cancel').onclick=cancelEdit;
 $('cloud-name').addEventListener('keydown',e=>{if(!editing)return;if(e.key==='Enter'){e.preventDefault();void rename()}else if(e.key==='Escape'){e.preventDefault();cancelEdit()}});
 $('cloud-disconnect').onclick=async()=>{busy=true;render();$('cloud-error').textContent='';try{cloud=await api('/api/cloud/disconnect',{});loaded=false;render()}catch(e){$('cloud-error').textContent=e.message}finally{busy=false;render()}};
 $('cloud-open').onclick=()=>{if(cloud?.url)void open(cloud.login_url||cloud.url+'/console').catch(e=>{$('cloud-error').textContent=e.message})};
 void refreshCloud();setInterval(()=>{if(state.page==='settings')void refreshCloud()},3000);
 window.addEventListener('readyrig-language-change',render);
})();
