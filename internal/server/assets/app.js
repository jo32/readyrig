'use strict';
const t=(message,values)=>readyRigI18n.t(message,values);
const PUBLIC_VIEW=/^\/[A-Za-z0-9]{8}\/app\//.test(location.pathname);
const APP_BASE=PUBLIC_VIEW?location.pathname.match(/^\/[A-Za-z0-9]{8}\/app/)[0]:'';
const route=path=>APP_BASE+path;
document.body.classList.toggle('public-view',PUBLIC_VIEW);
const $=id=>document.getElementById(id), esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const state={shareMode:null,route:null,relayBusy:false,relayError:'',fixedLoaded:false,connectionMode:null,page:'activity',data:null,calls:[],total:0,selected:null,session:'',category:'',query:'',status:'',offset:0,frames:[],frame:0,tool:null,toolRunning:false,connected:false,detail:null,detailVersion:0,replayDetail:null,replaySide:'after',replayVersion:0,frameSignature:'',framesVersion:0};
const scrollView=document.querySelector('.view'),topBar=document.querySelector('.top');
function updateTopFade(){topBar.style.setProperty('--top-fade',String(Math.min(1,Math.max(0,scrollView.scrollTop)/24)))}
scrollView.addEventListener('scroll',updateTopFade,{passive:true});
updateTopFade();
const labels={success:'成功',error:'失败',denied:'已拦截',running:'运行中',interrupted:'已中断',cancelled:'已取消'}, icons={files:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"><path d="M3 6h7l2 2h9v12H3Z"/><path d="M3 8V4h7l2 2h8v2"/></svg>',terminal:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><rect x="2.5" y="4" width="19" height="16" rx="3"/><path d="m7 9 3 3-3 3m6 0h4"/></svg>',computer:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><rect x="3" y="3" width="18" height="13" rx="2"/><path d="M8 21h8M12 16v5"/></svg>'};
icons.safari='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6"><circle cx="12" cy="12" r="9"/><path d="M15.5 8.5l-2 5-5 2 2-5z"/></svg>';
icons.browser='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6"><rect x="3" y="3" width="18" height="18" rx="4"/><path d="M3 8h18M7 5.5h.01M10 5.5h.01"/></svg>';
icons.system='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>';
const toolIcons={
 help:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5.5C9 3.8 6 3.5 3 4v15c3-.5 6-.2 9 1.5 3-1.7 6-2 9-1.5V4c-3-.5-6-.2-9 1.5Zm0 0v15M6 8h3m-3 4h3m6-4h3m-3 4h3"/></svg>',
 list_projects:'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 7V4h6l2 2h10v14H3V7Zm0 1h18M7 12h10M7 16h7"/></svg>'
};
const descriptions={list_projects:'列出已授权项目、默认目录与完全访问状态。',help:'查看当前开放的工具、完整参数定义和启用状态。',read_file:'读取已授权目录中的文本或二进制文件，支持按行查看。',write_file:'在已授权目录中写入文件，自动创建上级目录。',list_directory:'列出目录中的文件、大小和最后修改时间。',search_files:'在已授权目录中搜索文本，返回文件路径和行号。',exec_command:'运行终端命令，可获取执行状态和后续输出。',write_stdin:'向运行中的命令输入内容、获取结果或停止进程。',computer_screenshot:'拍摄主屏幕快照，自动缩放并记录坐标映射。',computer_action:'点击、输入、滚动或拖拽，并获取操作后的截图。'};
const examples={help:{},read_file:{path:'README.md',start_line:1,end_line:20},write_file:{path:'notes/hello.txt',content:'Hello from ReadyRig'},list_directory:{path:'.'},search_files:{path:'.',query:'TODO'},exec_command:{command:'pwd',cwd:'.',timeout:30,yield_time_ms:1000},write_stdin:{session_id:'填写进程 session_id',chars:'',yield_time_ms:1000},computer_screenshot:{},computer_action:{action:'left_click',frame_id:'填写刚获取的 frame_id',coordinate:[100,100],capture_after:true}};
const pendingCapabilities=new Set();
const pendingPermissions=new Set();
let refreshing=false,again=false,toastTimer,searchTimer,replayTimer,eventStream;
const uiSession='console-'+(sessionStorage.getItem('readyrig-session')||sessionStorage.getItem('relay-session')||crypto.randomUUID());sessionStorage.setItem('readyrig-session',uiSession.replace(/^console-/,''));
async function api(path,body){if(PUBLIC_VIEW&&body!==undefined)throw new Error(t("公网控制台仅供查看，请在本机操作"));const res=await fetch(route(path),{method:body===undefined?'GET':'POST',headers:{'Content-Type':'application/json','X-Session-ID':uiSession,'X-Client-Name':PUBLIC_VIEW?'Public console':'Local console'},body:body===undefined?undefined:JSON.stringify(body)});const data=await res.json();if(!res.ok){const e=new Error(t(data.error||'请求失败'));e.data=data;throw e}return data}
function toast(message){$('toast').textContent=message;$('toast').classList.remove('hidden');clearTimeout(toastTimer);toastTimer=setTimeout(()=>$('toast').classList.add('hidden'),3500)}
function error(message){$('error-banner').textContent=message;$('error-banner').classList.toggle('hidden',!message)}
function badge(status){return `<span class="badge ${esc(status)}">${status==='success'?'✓ ':status==='running'?'◌ ':''}${esc(t(labels[status])||status)}</span>`}
function time(date){return new Date(date).toLocaleTimeString(readyRigI18n.locale,{hour12:false})}
function duration(ms){return ms<1000?`${ms} ms`:`${(ms/1000).toFixed(2)} s`}
function empty(glyph,title,copy,button=''){return `<div class="empty"><div class="empty-glyph">${glyph}</div><h3>${esc(title)}</h3><p>${esc(copy)}</p>${button}</div>`}
function params(){return new URLSearchParams({q:state.query,session:state.session,category:state.category,status:state.status,limit:'40',offset:String(state.offset),view:'summary'})}
async function refresh(){if(refreshing){again=true;return}refreshing=true;try{const [data,list]=await Promise.all([api('/api/state'),api('/api/calls?'+params())]);state.data=data;state.calls=list.calls;state.total=list.total;state.connected=true;renderGlobal();renderCalls();await loadDetail();if(state.page==='projects')renderProjects();if(state.page==='tools')renderTools();if(state.page==='settings')renderSettings();if(state.page==='replay')await loadFrames();error('')}catch(e){state.connected=false;readyRigWindow.setActivity({connected:false});$('live').classList.add('off');$('live').textContent=t("连接已断开");error(e.message)}finally{refreshing=false;if(again){again=false;void refresh()}}}
function renderGlobal(){const d=state.data;readyRigWindow.setActivity({connected:state.connected,paused:d.paused,running:d.summary.running});renderUpdate(d.update);$('app-version').textContent=d.version;const s=d.summary;const active=d.project_access?.projects.find(p=>p.id===d.project_access.active);$('active-project-name').textContent=active?.name||t("本地工作空间");$('platform').textContent=active?`${active.path} · ${d.project_access.full_access?t("完全访问"):t("仅限项目目录")}`:t("{0} · 设备在线", {0: d.permissions.platform});$('stat-total').textContent=s.total;$('stat-running').textContent=s.running;$('stat-latency').innerHTML=s.total?`${s.avg_ms>=1000?(s.avg_ms/1000).toFixed(1):s.avg_ms}<em>${s.avg_ms>=1000?'s':'ms'}</em>`:'—';$('stat-screens').textContent=s.screenshots;$('pause').textContent=d.paused?t("恢复控制"):t("暂停控制");$('pause').classList.toggle('danger',d.paused);$('pause-banner').classList.toggle('hidden',!d.paused);$('live').classList.toggle('off',d.paused);$('live').innerHTML=`<i></i>${d.paused?t("控制已暂停"):PUBLIC_VIEW?t("公网查看模式"):t("服务已连接")}`;$('footer-status').textContent=t("{0} 个工具 · {1} · {2} 次异常", {0: d.tools.length, 1: PUBLIC_VIEW?t("公网查看 · 每 5 秒刷新"):t("本地日志"), 2: s.failed});
 const sessionOptions=`<option value="">${t("全部会话")}</option>`+d.sessions.map(s=>`<option value="${esc(s.id)}">${esc(s.client||s.id)} · ${esc(s.id.slice(0,12))}</option>`).join('');if($('session-filter').innerHTML!==sessionOptions){$('session-filter').innerHTML=sessionOptions;$('session-filter').value=state.session}
}

function callError(c){return c.status==='cancelled'?t("调用已取消，已产生的输出仍然保留。"):c.error}
function summary(call){const a=call.arguments||{};if(call.error&&call.status!=='cancelled')return call.error;return a.command||a.path||a.name||a.url||a.action||a.query||(call.tool==='computer_screenshot'?t("捕获主屏幕 · JPEG"):t("查看调用参数与结果"))}
function renderCalls(){
 const focused=state.detail?.call;const calls=focused?.id===state.selected&&!state.calls.some(c=>c.id===state.selected)?[focused,...state.calls]:state.calls;
 $('result-count').textContent=state.total;

 const callsHTML=calls.length?calls.map(c=>`<div class="call-item ${c.id===state.selected?'open':''} ${c.status==='error'||c.status==='denied'?'bad':''}"><button class="call" data-call="${esc(c.id)}" aria-expanded="${c.id===state.selected}" aria-label="${esc(c.tool+' '+t(labels[c.status])+' '+time(c.started))}"><span class="when"><i class="call-chev">›</i>${time(c.started)}</span><span class="m">${esc(c.tool)}</span><span class="p">${esc(summary(c))}</span><span class="a">${esc(c.client)}</span><span class="st">${badge(c.status)}</span><span class="duration">${c.status==='running'?t("执行中"):duration(c.duration_ms)}</span></button>${c.id===state.selected?'<div id="detail"></div>':''}</div>`).join(''):empty('',t("还没有调用记录"),t("连接 Agent 后，工具调用和执行结果会出现在这里。"),PUBLIC_VIEW?`<button class="button" data-page="tools">${t("查看工具")}</button>`:`<button class="button" data-test-files>${t("测试读取目录")}</button>`);
 const html=callsHTML+(calls.some(c=>c.id===state.selected)?'':'<div id="detail" class="hidden"></div>');
 if($('calls')._html!==html){
  const old=$('detail');
  $('calls').innerHTML=html;$('calls')._html=html;
  // Retain a selected expansion and its reading position when other rows change.
  if(old?.dataset.call===state.selected&&!$('detail').classList.contains('hidden'))$('detail').replaceWith(old);
 }
 $('pagination-info').textContent=state.total?t("{0}–{1} / {2} 条调用", {0: state.offset+1, 1: Math.min(state.offset+40,state.total), 2: state.total}):t("日志仅保存在本机");
 $('previous').disabled=state.offset===0;$('next').disabled=state.offset+40>=state.total;
 if(!state.selected)renderDetail(null);
}
async function loadDetail(){
 const id=state.selected,version=++state.detailVersion;
 if(!id){state.detail=null;renderDetail(null);return}
 if(state.detail?.call.id===id&&state.detail.call.status!=='running'){renderDetail(state.detail.call);return}
 try {const data=await api('/api/calls/'+encodeURIComponent(id));if(version!==state.detailVersion||id!==state.selected)return;state.detail=data;renderCalls();renderDetail(data.call)}
 catch(e){if(version===state.detailVersion){state.detail=null;renderDetail(null);toast(e.message)}}
}
// Bound previews before serialization: screenshot payloads can be many megabytes.
// The stored result remains intact for Copy; it must never become a wrapped text node.
function previewValue(value){
 let remaining=32768,nodes=1000;
 const omitted=()=>t("[预览已省略，复制可获取完整结果]");
 function visit(v,depth){
  if(--nodes<0||remaining<=0||depth>12)return omitted();
  if(typeof v==='string'){const limit=Math.min(4096,remaining);remaining-=Math.min(v.length,limit);return v.length>limit?v.slice(0,limit)+omitted():v}
  if(!v||typeof v!=='object')return v;
  const out=Array.isArray(v)?[]:Object.create(null);
  for(const key of Object.keys(v)){
   if(nodes<=0||remaining<=0){out[Array.isArray(v)?out.length:'…']=omitted();break}
   const name=key.slice(0,256);remaining-=name.length;
   out[name]=key==='data'&&(v.type==='image'||v.type==='audio')?t("[图像或音频数据已省略]"):visit(v[key],depth+1);
  }
  return out;
 }
 return visit(value,0);
}
const pretty=v=>esc(JSON.stringify(previewValue(v),null,2)??'null');
const detailImageURLs=new Set();
function clearDetailImages(){for(const url of detailImageURLs)URL.revokeObjectURL(url);detailImageURLs.clear()}
function showBrowserImage(button){
 const part=state.detail?.call.result?.content?.[Number(button.dataset.browserImage)];
 if(part?.type!=='image'||!/^image\/(png|jpeg|webp)$/.test(part.mimeType)||typeof part.data!=='string')return;
 const bytes=Uint8Array.from(atob(part.data),c=>c.charCodeAt(0));
 const url=URL.createObjectURL(new Blob([bytes],{type:part.mimeType}));
 detailImageURLs.add(url);
 const img=document.createElement('img');img.decoding='async';img.src=url;img.alt=t("Chrome 返回的页面截图");
 button.replaceWith(img);
}
function outputView(c){
 const r=c.result||{};
 if((c.category==='browser'||c.category==='safari')&&Array.isArray(r.content))return `<div class="browser-output">${r.content.slice(0,100).map((part,i)=>part.type==='text'?`<pre data-scroll="browser-${i}">${esc(previewValue(part.text))}</pre>`:part.type==='image'&&/^image\/(png|jpeg|webp)$/.test(part.mimeType)&&typeof part.data==='string'?`<button class="button" data-browser-image="${i}">${t("加载截图")}</button>`:'').join('')}</div>`;

 if(c.category==='terminal')return `<div class="terminal-output"><div class="terminal-heading"><span>›_ ${r.running||c.status==='running'?t("正在执行"):t("执行输出")}</span><span>${r.exit_code!=null?'exit '+esc(r.exit_code):t("实时")}</span></div>${c.arguments?.command?`<pre class="terminal-command">$ ${esc(c.arguments.command)}</pre>`:''}<pre data-scroll="stdout" class="stdout">${esc(r.stdout|| (c.status==='running'?t("等待输出…"):t("（无标准输出）")))}</pre>${r.stderr?`<div class="stream-label">${t("标准错误")}</div><pre data-scroll="stderr" class="stderr">${esc(r.stderr)}</pre>`:''}</div>${r.truncated?`<p class="result-note">${t("输出已达到 1 MiB 上限，后续内容未保存。")}</p>`:''}`;
 if(c.tool==='read_file'&&typeof r.content==='string')return `<div class="detail-section"><h3>${esc(c.arguments?.path)} <span>${esc(r.encoding||'utf-8')} · ${esc(r.size)} B</span></h3><pre class="file-content" data-scroll="file">${esc(r.content)}</pre>${r.truncated?`<p class="result-note">${t("文件内容已截断。")}</p>`:''}</div>`;
 if(Array.isArray(r.entries))return `<div class="detail-section"><h3>${t("目录内容 ")}<span>${t("{0} 项", {0: r.entries.length})}</span></h3>${r.entries.length?`<div class="result-table" data-scroll="entries"><table><thead><tr><th>${t("名称")}</th><th>${t("大小")}</th></tr></thead><tbody>${r.entries.map(e=>`<tr><td>${e.directory?'▱':'▤'} ${esc(e.name)}${e.symlink?' ↗':''}</td><td>${e.directory?'—':esc(e.size)+' B'}</td></tr>`).join('')}</tbody></table></div>`:`<p class="detail-empty">${t("此目录为空")}</p>`}</div>`;
 if(Array.isArray(r.matches))return `<div class="detail-section"><h3>${t("搜索结果 ")}<span>${t("{0} 处", {0: r.matches.length})}</span></h3><div class="search-results" data-scroll="matches">${r.matches.map(m=>`<div><strong>${esc(m.path)}:${esc(m.line)}</strong><pre>${esc(m.text)}</pre></div>`).join('')||`<p class="muted">${t("没有匹配内容")}</p>`}</div></div>`;
 if(c.tool==='write_file'&&r.bytes_written!=null)return `<div class="saved-result">${t("✓ 已写入 {0}", {0: esc(r.path)})}<small>${t("{0} 字节 · 原子写入", {0: esc(r.bytes_written)})}</small></div>`;
 return '';
}
function renderDetail(c){
 const rich=c?outputView(c):'';
 const html=c?`<div class="call-details"><section class="call-body"><div class="call-body-head"><span class="call-body-label">${t("输入参数")}</span><span class="grow"></span><span class="note">JSON</span></div><div class="body-content"><pre data-scroll="args">${pretty(c.arguments)}</pre></div></section><section class="call-body"><div class="call-body-head"><span class="call-body-label">${t("执行结果")}</span><span class="grow"></span><button class="button subtle" data-copy-result>${t("复制")}</button></div><div class="body-content">${c.error?`<div class="detail-error">${esc(callError(c))}</div>`:''}${rich}${c.screenshot?`<div class="detail-section"><h3>${t("屏幕快照 ")}<button data-view-replay="${esc(c.id)}">${t("查看回放 ↗")}</button></h3><a href="${route('/api/screenshots/'+encodeURIComponent(c.screenshot))}" target="_blank" rel="noopener"><img src="${route('/api/screenshots/'+encodeURIComponent(c.screenshot))}" alt="${t("{0} 执行后的屏幕快照", {0: esc(c.tool)})}"></a></div>`:''}${rich||c.screenshot?`<details class="disclosure raw-data" data-preserve="result"><summary><span class="disclosure-title">${t("原始响应")}</span><span class="disclosure-meta">JSON</span></summary><pre data-scroll="raw">${pretty(c.result)}</pre></details>`:`<pre data-scroll="raw">${pretty(c.result)}</pre>`}</div></section></div><div class="call-meta-bar"><span>${t("会话 ")}<code>${esc(c.session)}</code></span><span>${t("调用 ")}<code>${esc(c.id)}</code></span><span>${new Date(c.started).toLocaleString(readyRigI18n.locale,{hour12:false})}</span>${c.status==='running'&&!PUBLIC_VIEW?`<button class="button danger" data-cancel="${esc(c.id)}">${t("停止这次调用")}</button>`:''}</div>`:'';
 for(const target of [$('detail')]){
  if(target.dataset.call===c?.id&&target._html===html)continue;
  const same=target.dataset.call===c?.id;
  const opened=same?[...target.querySelectorAll('details[open]')].map(d=>d.dataset.preserve):[];
  const scrolls=same?[...target.querySelectorAll('[data-scroll]')].map(e=>({key:e.dataset.scroll,top:e.scrollTop,bottom:e.scrollHeight-e.scrollTop-e.clientHeight<20})):[];
  clearDetailImages();
  target.innerHTML=html;target.dataset.call=c?.id||'';target._html=html;
  target.querySelectorAll('details').forEach(d=>d.open=opened.includes(d.dataset.preserve));
  target.querySelectorAll('[data-scroll]').forEach(e=>{const old=scrolls.find(s=>s.key===e.dataset.scroll);e.scrollTop=old?(old.bottom?e.scrollHeight:old.top):0});
 }
}
function showPage(page){
 state.page=page;closeProjectMenu();stopReplay();scrollView.scrollTop=0;updateTopFade();
 document.querySelectorAll('.page').forEach(p=>p.classList.toggle('hidden',p.id!==page+'-page'));
 document.querySelectorAll('.nav').forEach(b=>{b.classList.toggle('active',b.dataset.page===page);b.setAttribute('aria-pressed',String(b.dataset.page===page))});
 if(!state.data)return;
 if(page==='settings')renderSettings();
 if(page==='projects')renderProjects();if(page==='tools')renderTools();
 if(page==='replay')loadFrames().catch(e=>error(e.message));
}
function renderTools(){renderChrome();renderSafari();$('tools').innerHTML=state.data.tools.map(tool=>`<button class="row tool-row" data-tool="${esc(tool.name)}"><span class="tool-icon" aria-hidden="true">${toolIcons[tool.name]||icons[tool.category]||icons.system}</span><span class="who"><span class="name">${esc(tool.name)}</span><span class="sub">${esc(t(descriptions[tool.name]||tool.description))}</span></span><span class="tags"><span class="tag">${tool.mutating?t("读写"):t("只读")}</span><span class="tag">${tool.parallel?t("并发"):t("串行")}</span><span class="tag">${!state.data.enabled[tool.category]?t("未启用"):tool.category==='browser'&&state.data.chrome?.state!=='ready'?t("待连接"):t("已启用")}</span></span><span class="chev">›</span></button>`).join('')}
function renderCLI(){
 const c=state.data?.cli,local=state.data?.local_cli;
 $('cli-panel').classList.toggle('hidden',PUBLIC_VIEW||(!c&&!local));
 $('cli-installation').classList.toggle('hidden',!c);$('cli-status').classList.toggle('hidden',!c);
 const prompt=localConfigurationPrompt(local);
 $('local-config-prompt').classList.toggle('hidden',!prompt);
 if($('local-config-prompt-text').value!==prompt)$('local-config-prompt-text').value=prompt;
 $('copy-local-config-prompt').disabled=!prompt||$('copy-local-config-prompt').getAttribute('aria-busy')==='true';
 if(!c)return;
 const labels={installed:t('已安装 CLI'),existing:t('已保留现有 CLI'),relocate:t('需要移动 App'),error:t('CLI 安装未完成')};
 $('cli-status').textContent=labels[c.state]||c.state;$('cli-path').textContent=c.path||'';
 $('cli-message').textContent=c.state==='relocate'?t('请先将 ReadyRig 移到应用程序文件夹并重新打开。'):c.state==='existing'?t('已有独立安装的 CLI，App 会保留它。新开终端后运行 readyrig。'):c.state==='error'?t('修复下方问题后重新打开 App，即可重试安装 CLI。'):t('新开一个终端窗口，运行 readyrig 即可打开终端界面。');
 $('cli-error').textContent=t(c.error||'');$('cli-error').classList.toggle('hidden',!c.error);
}
function shellArgument(value){return "'"+String(value).replaceAll("'","'\\''")+"'"}
function localConfigurationPrompt(context){
 if(PUBLIC_VIEW||!context?.command||!context?.data_dir)return '';
 const prefix=shellArgument(context.command)+' --data-dir '+shellArgument(context.data_dir);
 const commands=['version','help','status','config show','projects list','tools'].map(command=>'    '+prefix+' '+command).join('\n');
 const lifecycle=context.mode==='desktop'?t("当前实例由 ReadyRig App 运行。直接用 CLI 管理这个实例，不要另起 serve，也不要对 App 使用 stop/restart。config set、init、setup 需要实例停止；如果必须修改启动设置，先说明要修改的项目和重启影响，让我退出 App，再用相同的数据目录完成配置，之后重新打开 App。App 会读取保存的启动设置。"):context.mode==='daemon'?t("当前实例是后台 daemon。运行中的项目、能力、分享和账号通过 CLI 管理；config set、init、setup 需要实例停止。只有需要修改启动设置时，才用相同命令前缀执行 stop、修改配置、再执行 serve，并检查启动结果。"):t("当前实例由前台进程或进程管理器运行。运行中的项目、能力、分享和账号通过 CLI 管理；config set、init、setup 需要实例停止。修改启动设置前说明重启影响，按原来的进程管理方式停止并恢复服务，避免另起第二个实例。");
 return [
  t("请帮我配置这台电脑上的 ReadyRig，使用本机终端中的 ReadyRig CLI。先检查现状，再根据我的需求完成配置。若你不能在这台电脑上执行命令，请明确说明；不要声称已完成配置。"),
  t("以下命令使用当前实例的 CLI 绝对路径和数据目录。路径已经按 POSIX shell 规则加引号，后续每个 CLI 命令都必须保留相同的 --data-dir。先执行这些只读检查，阅读实际版本、帮助、启动设置、项目和可用工具：\n{0}",{0:commands}),
  t("如果我已说明目标，就按目标继续；否则先询问要授权哪些目录、使用哪些能力，以及是否需要公网分享或云端账号。保留现有配置，仅修改完成目标所需的项目；通过 CLI 配置，不要直接编辑配置文件。"),
  t("根据 help 中实际支持的命令操作：projects add/use/list 管理项目；capability <files|terminal|browser|safari|computer> on|off 调整并保存能力开关；share configure/start/status 管理分享；cloud login/status 管理账号绑定。仅在我的需求包含这些功能时操作。需要登录或系统权限时，告诉我具体要完成的步骤，并确认结果。"),
  lifecycle,
  t("项目、能力开关、固定链接配置和账号绑定会保存；pause/resume 的变化只作用于当前运行。其他需要每次启动生效的选项用 config set <key> <value> 保存，具体选项先查 help。Full Access 不能保存，不要默认开启全部能力或公网分享。固定隧道令牌用 share configure --token-stdin 输入，不要把令牌放进命令行或回复中。"),
  t("完成后重新执行 status、projects list 和 tools，按目标检查 connection、share status 或 cloud status，并做必要的最小功能验证。报告实际改了什么、哪些设置会保留、哪些只对本次运行有效，以及仍需我完成的步骤。不能仅凭修改成功就声称整套流程已验证。"),
  t("我想要：...")
 ].join('\n\n');
}
async function copyLocalConfiguration(){
 if(PUBLIC_VIEW)return;
 const button=$('copy-local-config-prompt');button.disabled=true;button.setAttribute('aria-busy','true');
 try{
  const data=await api('/api/state'),prompt=localConfigurationPrompt(data.local_cli);
  if(!prompt)throw new Error(t('本机配置信息暂不可用，请刷新后重试。'));
  state.data=data;renderCLI();
  if(!await copy(prompt)){$('local-config-details').open=true;$('local-config-prompt-text').focus();$('local-config-prompt-text').select()}
 }catch(e){toast(e.message)}
 finally{button.removeAttribute('aria-busy');renderCLI()}
}
function renderSettings(){renderChrome();renderSafari();renderCLI();const d=state.data;const names={files:[t("文件系统"),t("读取、写入和搜索已添加项目中的文件；目录范围在「项目」中管理。")],terminal:[t("终端执行"),t("允许执行宿主机命令。工作目录限制不是系统沙箱；命令拥有当前用户的权限。")],computer:[t("桌面操作"),t("允许截图、鼠标与键盘操作。此驱动使用真实鼠标，移动到屏幕角落可停止输入。")],browser:[t("Chrome 浏览器"),t("检测已开启的 Chrome 远程调试，通过官方 MCP 开放浏览器工具。所有调用均记录日志。")],safari:[t("Safari 浏览器"),t("通过 Safari 内置的 MCP 开放浏览器工具，需要 macOS 27 和 Safari 27。所有调用均记录日志。")]};if(!$('capabilities').children.length||$('capabilities').dataset.locale!==readyRigI18n.locale){$('capabilities').dataset.locale=readyRigI18n.locale;$('capabilities').innerHTML=Object.entries(names).map(([k,[title,description]])=>`<div class="capability"><span class="tool-icon ${k}" aria-hidden="true">${icons[k]}</span><div class="capability-copy"><strong id="capability-${k}-label">${title}</strong><p id="capability-${k}-description">${description}</p></div><div class="capability-control"><span class="switch-state" data-capability-state="${k}" aria-hidden="true"></span><button class="toggle" role="switch" aria-checked="false" aria-labelledby="capability-${k}-label" aria-describedby="capability-${k}-description" data-capability="${k}"><span class="switch-track" aria-hidden="true"><span class="switch-thumb"></span></span></button></div></div>`).join('')}
 for(const b of $('capabilities').querySelectorAll('[data-capability]')){const k=b.dataset.capability,enabled=!!d.enabled[k],pending=pendingCapabilities.has(k);b.setAttribute('aria-checked',String(enabled));b.setAttribute('aria-disabled',String(pending||PUBLIC_VIEW));b.disabled=PUBLIC_VIEW;b.setAttribute('aria-busy',String(pending));b.previousElementSibling.textContent=pending?t("切换中"):enabled?t("已开启"):t("已关闭")}
 renderPermissions();$('workspace-path').textContent=d.workspace;renderConnection()}
function renderPermissions(){
 const p=state.data.permissions,box=$('permissions');
 const signature=JSON.stringify([readyRigI18n.locale,p.supported,p.screen,p.accessibility,...pendingPermissions]);
 if(box.dataset.signature===signature)return;
 box.dataset.signature=signature;
 box.innerHTML=p.supported?[['screen',t("屏幕录制")],['accessibility',t("辅助功能")]].map(([permission,label])=>{
  const granted=p[permission],pending=pendingPermissions.has(permission);
  const status=granted?`<span class="badge success">${t("已授权")}</span>`:PUBLIC_VIEW?`<span class="badge denied">${t("未授权")}</span>`:`<button type="button" class="badge denied permission-action" data-system-permission="${permission}" aria-label="${esc(t("{0}：未授权，去授权",{0:label}))}" aria-busy="${pending}" ${pending?'disabled':''}>${pending?t("正在打开…"):t("未授权 · 去授权 ↗")}</button>`;
  return `<div class="permission-row"><span>${esc(label)}</span>${status}</div>`;
 }).join(''):`<p>${t("此构建不支持原生桌面操作。需 macOS + CGO 构建。")}</p>`;
}
async function openPermissionSettings(permission){
 if(PUBLIC_VIEW||pendingPermissions.has(permission))return;
 pendingPermissions.add(permission);renderPermissions();
 try{await api('/api/access/system-settings',{permission});toast(t("已打开权限设置，请授权后重新启动 ReadyRig。"))}
 catch(e){toast(e.message)}
 finally{pendingPermissions.delete(permission);renderPermissions()}
}
async function toggleCapability(category){
 if(pendingCapabilities.has(category))return;
 const enabled=!state.data.enabled[category];
 pendingCapabilities.add(category);renderSettings();
 try{await api('/api/capability',{category,enabled});state.data.enabled[category]=enabled;await refresh()}
 finally{pendingCapabilities.delete(category);renderSettings()}
}
// A browser tool waits for its own browser: Chrome tools for Chrome, safari_* tools for Safari.
// Returns the message to show while it cannot run, or '' when it can.
function browserPending(tool,data){
 if(tool.category!=='browser'&&tool.category!=='safari')return '';
 const safari=tool.category==='safari',b=safari?data.safari:data.chrome;
 // Safari still takes a call while it waits for the remote automation setting: the answer says how to turn it on.
 if(b?.state==='ready'||safari&&b?.state==='permission_required')return '';
 return b?.message||(safari?'Safari MCP 尚未启动':'等待 Chrome 连接');
}
function renderSafari(){
 const c=state.data?.safari||{state:'waiting',message:t("Safari MCP 尚未启动")};
 const names={waiting:t("等待检测"),permission_required:t("需要授权"),connecting:t("正在连接"),ready:t("已接入"),disabled:t("已关闭"),unavailable:t("需要配置"),error:t("连接失败")};
 const status=state.data?.paused?t("已暂停"):!state.data?.enabled.safari?t("已关闭"):names[c.state]||c.state;
 const summaries={waiting:'Safari MCP 尚未启动。',permission_required:'请在 Safari 的开发者设置中开启「允许远程自动化和外部代理」，然后重试。',connecting:'正在读取 Safari 的工具清单…',disabled:'Safari MCP 已关闭。',unavailable:'Safari 内置 MCP 需要 macOS 27 和 Safari 27 或更新版本，请展开诊断查看原因。',error:'读取 Safari 工具失败，请重新检测或展开诊断。'};
 const message=state.data?.paused?t('控制已暂停，恢复后才能使用浏览器。'):!state.data?.enabled.safari?t('Safari MCP 已关闭。'):c.state==='ready'?t('已接入 {0} 个 Safari 工具。首次使用前，请在 Safari 的开发者设置中开启远程自动化。',{0:c.tools||0}):t(summaries[c.state]||'请展开诊断查看连接状态。');
 for(const el of document.querySelectorAll('[data-safari-status]'))el.textContent=status;
 for(const el of document.querySelectorAll('[data-safari-message]'))el.textContent=message;
 for(const el of document.querySelectorAll('[data-safari-diagnostic]'))el.textContent=t(c.message);
 for(const el of document.querySelectorAll('[data-safari-indicator]'))el.classList.toggle('ready',c.state==='ready'&&!state.data?.paused&&state.data?.enabled.safari);
}
function renderChrome(){
 const c=state.data?.chrome||{state:'waiting',message:t("等待检测 Chrome")};
 const names={waiting:t("等待 Chrome"),permission_required:t("需要授权"),connecting:t("正在连接"),ready:t("已接入"),disabled:t("已关闭"),unavailable:t("需要配置"),error:t("连接失败")};
 const status=state.data?.paused?t("已暂停"):!state.data?.enabled.browser?t("已关闭"):names[c.state]||c.state;
 const summaries={waiting:'在 Chrome 中开启远程调试后，点击重新检测。',permission_required:'请授权读取 Chrome 调试文件。',connecting:'正在连接 Chrome…',disabled:'浏览器工具已关闭。',unavailable:'连接暂不可用，请展开诊断查看原因。',error:'连接失败，请重新检测或展开诊断。'};
 const message=state.data?.paused?t('控制已暂停，恢复后才能使用浏览器。'):!state.data?.enabled.browser?t('浏览器工具已关闭。'):c.state==='ready'?t('已接入 {0} 个浏览器工具。首次使用时，请在 Chrome 中允许连接。',{0:c.tools||0}):t(summaries[c.state]||'请展开诊断查看连接状态。');
 for(const el of document.querySelectorAll('[data-chrome-authorize]'))el.classList.toggle('hidden',PUBLIC_VIEW||new URLSearchParams(location.search).get('shell')!=='darwin'||c.state!=='permission_required');
 for(const el of document.querySelectorAll('[data-chrome-status]'))el.textContent=status;
 for(const el of document.querySelectorAll('[data-chrome-message]'))el.textContent=message;
 for(const el of document.querySelectorAll('[data-chrome-diagnostic]'))el.textContent=t(c.message);
 for(const el of document.querySelectorAll('[data-chrome-indicator]'))el.classList.toggle('ready',c.state==='ready'&&!state.data?.paused&&state.data?.enabled.browser);
}
async function loadFrames(force=false){
 const signature=[state.session,state.data?.summary.total,state.data?.summary.running].join(':');
 if(!force&&signature===state.frameSignature)return;
 const version=++state.framesVersion;let frames=[],offset=0;
 while(true){const list=await api('/api/calls?'+new URLSearchParams({category:'computer',session:state.session,limit:'500',offset:String(offset),view:'summary'}));frames.push(...list.calls.filter(c=>c.screenshot||c.tool==='computer_action'));if(list.calls.length<500||offset>=4500)break;offset+=500}
 if(version!==state.framesVersion)return;
 const old=state.replayTarget||state.frames[state.frame]?.id;state.replayTarget=null;state.frameSignature=signature;state.frames=frames.reverse();const found=state.frames.findIndex(f=>f.id===old);state.frame=found>=0?found:Math.max(0,state.frames.length-1);await renderFrame();
}
async function renderFrame(){
 const frames=state.frames,c=frames[state.frame],version=++state.replayVersion;
 $('frame-caption').textContent=c?`${state.frame+1} / ${frames.length} · ${time(c.started)} · ${c.tool}`:t("暂无快照");
 $('frame-prev').disabled=state.frame<=0;$('frame-next').disabled=state.frame>=frames.length-1;$('play').disabled=!frames.length;
 $('replay-position').max=Math.max(0,frames.length-1);$('replay-position').value=state.frame;$('replay-position').disabled=!frames.length;
 $('filmstrip').innerHTML=frames.map((c,i)=>`<button class="frame ${i===state.frame?'selected':''}" data-frame="${i}" aria-label="${t("查看 {0} 的记录", {0: time(c.started)})}">${c.screenshot?`<img src="${route('/api/screenshots/'+encodeURIComponent(c.screenshot))}" alt="" loading="lazy">`:'<div class="frame-placeholder">⌖</div>'}<span>${time(c.started)} · ${esc(t(labels[c.status]))}</span></button>`).join('');
 if(!c){state.replayDetail=null;$('replay-screen').innerHTML=empty('▣',t("还没有屏幕快照"),t("在连接与权限中启用桌面操作并授予系统权限，随后点击「拍摄快照」。"));$('replay-action').innerHTML='';return}
 $('replay-screen').innerHTML=`<div class="empty">${t("正在加载此步骤…")}</div>`;
 try{const detail=await api('/api/calls/'+encodeURIComponent(c.id));if(version!==state.replayVersion)return;state.replayDetail=detail;if(!detail.call.screenshot&&detail.before)state.replaySide='before';else if(!detail.before&&detail.call.screenshot)state.replaySide='after';paintReplay()}
 catch(e){if(version===state.replayVersion){$('replay-screen').textContent=e.message}}
}
function paintReplay(){
 const detail=state.replayDetail;if(!detail)return;
 const c=detail.call,before=detail.before,a=c.arguments||{},side=state.replaySide;
 const frame=side==='before'?before:c;
 const size=frame?.result?.image_size;
 const point=a.coordinate;
 let overlay='';
 if(side==='before'&&size?.length===2&&point?.length===2&&[...size,...point].every(Number.isFinite)){
  const to=a.to,drag=to?.length===2&&to.every(Number.isFinite);
  overlay=`<svg class="action-overlay" viewBox="0 0 ${size[0]} ${size[1]}" aria-label="${t("动作目标坐标")}"><circle cx="${point[0]}" cy="${point[1]}" r="18"/><circle class="target-dot" cx="${point[0]}" cy="${point[1]}" r="4"/>${drag?`<line x1="${point[0]}" y1="${point[1]}" x2="${to[0]}" y2="${to[1]}"/><circle cx="${to[0]}" cy="${to[1]}" r="12"/>`:''}</svg>`;
 }
 $('replay-screen').innerHTML=frame?.screenshot?`<div class="replay-image"><img src="${route('/api/screenshots/'+encodeURIComponent(frame.screenshot))}" alt="${side==='before'?t("动作使用的参考截图"):t("执行后截图")}">${overlay}</div>`:empty('▣',t("这一步没有保存")+(side==='before'?t("参考"):t("执行后"))+t("截图"),c.error||t("该动作可能关闭了执行后截图，或没有引用 frame_id。"));
 $('replay-action').innerHTML=`<div class="replay-operation"><div><span class="eyebrow">${c.tool==='computer_action'?t("桌面操作"):t("屏幕观察")}</span><h3>${esc(a.action||t("拍摄屏幕"))} ${badge(c.status)}</h3><p>${point?t("目标 ({0}) · ", {0: esc(point.join(', '))}):''}${esc(c.client)} · ${duration(c.duration_ms)}</p>${a.keys?`<p>${t("组合键 {0}", {0: esc(a.keys.join(' + '))})}</p>`:''}${a.text?`<p class="typed-text">${t("输入 {0}", {0: esc(a.text)})}</p>`:''}${c.error?`<p class="error-text">${esc(c.error)}</p>`:''}</div><div class="segmented" role="group" aria-label="${t("回放画面")}"><button aria-pressed="${side==='before'}" data-replay-side="before" class="${side==='before'?'active':''}" ${before?'':'disabled'}>${t("参考画面")}</button><button aria-pressed="${side==='after'}" data-replay-side="after" class="${side==='after'?'active':''}" ${c.screenshot?'':'disabled'}>${t("执行之后")}</button></div></div><p class="replay-note">${side==='before'?t("标记位置来自调用参数，显示在 Agent 使用的参考截图上。"):t("展示本次调用保存的画面。")} <button data-open-call="${esc(c.id)}">${t("查看调用详情 ↗")}</button></p>`;
}
function renderToolText(){
 const tool=state.tool;if(!tool)return;
 $('tool-dialog-label').textContent=t(PUBLIC_VIEW?'工具详情':'工具测试');
 $('tool-description').textContent=t(descriptions[tool.name]||tool.description);
 const chromePending=browserPending(tool,state.data);
 $('tool-warning').textContent=PUBLIC_VIEW?t('公网控制台仅供查看。Agent 可通过 REST 或 MCP 调用已授权工具。'):chromePending?t(chromePending):state.data.enabled[tool.category]?t(tool.mutating?'运行后会实际修改文件或操作电脑。':'调用和结果将保存到执行日志。'):t('此能力尚未启用，请先在连接与权限中开启。');
 $('run-tool').textContent=t(state.toolRunning?'执行中…':'运行工具 →');
 $('run-tool').disabled=state.toolRunning||PUBLIC_VIEW||chromePending||!state.data.enabled[tool.category]||(state.data.paused&&!['help','list_projects'].includes(tool.name));
}
function openTool(name){
 const tool=state.data?.tools.find(item=>item.name===name);if(!tool)return;
 state.tool=tool;$('tool-arguments').readOnly=PUBLIC_VIEW;
 $('tool-title').textContent=name;$('tool-schema').textContent=JSON.stringify(tool.inputSchema,null,2);
 $('tool-arguments').value=JSON.stringify(readyRigI18n.translateData(examples[name]||{}),null,2);
 renderToolText();$('tool-result').classList.add('hidden');$('tool-dialog').showModal();
}
async function copy(text){try{await navigator.clipboard.writeText(text);toast(t("已复制"));return true}catch{const el=document.createElement('textarea');el.value=text;document.body.appendChild(el);el.select();let ok=false;try{ok=document.execCommand('copy')}catch{}finally{el.remove()}toast(ok?t("已复制"):t("无法访问剪贴板，请手动复制"));return ok}}
function theme(){const dark=document.documentElement.dataset.theme!=='dark';document.documentElement.dataset.theme=dark?'dark':'light';localStorage.setItem('readyrig-theme',dark?'dark':'light')}
document.documentElement.dataset.theme=localStorage.getItem('readyrig-theme')||localStorage.getItem('relay-theme')||(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light');
document.addEventListener('click',async e=>{const b=e.target.closest('button');if(!b)return;try{if('browserImage'in b.dataset)showBrowserImage(b);if(b.dataset.page)showPage(b.dataset.page);if('chromeAuthorize'in b.dataset){b.disabled=true;try{const result=await api('/api/chrome/authorize',{});if(result.ok){await api('/api/chrome/refresh',{});toast(t("已选择调试入口，正在重新检测"));await refresh()}}finally{b.disabled=false}}if('chromeRefresh'in b.dataset){await api('/api/chrome/refresh',{});toast(t("正在重新检测 Chrome"));await refresh()}if('safariRefresh'in b.dataset){await api('/api/chrome/refresh',{});toast(t("正在重新检测 Safari"));await refresh()}if('category'in b.dataset){state.category=b.dataset.category;state.offset=0;state.selected=null;document.querySelectorAll('[data-category]').forEach(t=>{t.classList.toggle('active',t===b);t.setAttribute('aria-pressed',String(t===b))});await refresh()}if(b.dataset.call){state.selected=state.selected===b.dataset.call?null:b.dataset.call;renderCalls();await loadDetail()}if(b.dataset.session){state.session=b.dataset.session;state.offset=0;state.selected=null;showPage('activity');await refresh()}if(b.dataset.tool)openTool(b.dataset.tool);if('testFiles'in b.dataset)openTool('list_directory');if('viewReplay'in b.dataset){state.replayTarget=b.dataset.viewReplay;state.frameSignature='';showPage('replay')}if(b.dataset.replaySide){state.replaySide=b.dataset.replaySide;paintReplay()}if(b.dataset.openCall){state.selected=b.dataset.openCall;showPage('activity');await loadDetail()}if(b.dataset.cancel){b.disabled=true;await api('/api/calls/'+encodeURIComponent(b.dataset.cancel)+'/cancel',{});toast(t("已请求停止，等待进程退出"));await refresh()}if('copyResult'in b.dataset&&state.detail)await copy(JSON.stringify(state.detail.call.result,null,2));if(b.dataset.capability)await toggleCapability(b.dataset.capability);if('frame'in b.dataset){stopReplay();state.frame=Number(b.dataset.frame);await renderFrame()}}catch(e){toast(e.message)}});
$('refresh').onclick=()=>refresh();$('session-filter').onchange=()=>{state.session=$('session-filter').value;state.offset=0;state.selected=null;state.frameSignature='';refresh()};
$('connect').onclick=()=>showPage('settings');$('theme').onclick=theme;$('pause').onclick=async()=>{try{await api('/api/pause',{paused:!state.data.paused});await refresh()}catch(e){error(e.message)}};$('resume').onclick=()=>{$('pause').click()};$('search').oninput=()=>{clearTimeout(searchTimer);searchTimer=setTimeout(()=>{state.query=$('search').value;state.offset=0;state.selected=null;refresh()},250)};$('status-filter').onchange=()=>{state.status=$('status-filter').value;state.offset=0;state.selected=null;refresh()};$('previous').onclick=()=>{state.offset=Math.max(0,state.offset-40);state.selected=null;refresh()};$('next').onclick=()=>{state.offset+=40;state.selected=null;refresh()};$('close-tool').onclick=()=>$('tool-dialog').close();$('tool-dialog').addEventListener('click',e=>{if(e.target===$('tool-dialog')){const rect=e.target.getBoundingClientRect();if(e.clientX<rect.left||e.clientX>rect.right||e.clientY<rect.top||e.clientY>rect.bottom)e.target.close()}});
$('run-tool').onclick=async()=>{const b=$('run-tool');state.toolRunning=true;b.disabled=true;b.textContent=t("执行中…");try{const args=JSON.parse($('tool-arguments').value);const result=await api('/api/tools/'+state.tool.name,args);if(result.result?.screenshot)result.result.screenshot=t("[快照已保存，可在桌面回放中查看]");$('tool-result').textContent=JSON.stringify(result,null,2);$('tool-result').classList.remove('hidden');state.selected=result.call_id;await refresh()}catch(e){$('tool-result').textContent=JSON.stringify(e.data||{error:e.message},null,2);$('tool-result').classList.remove('hidden');await refresh()}finally{state.toolRunning=false;b.disabled=PUBLIC_VIEW||(state.data.paused&&!['help','list_projects'].includes(state.tool.name))||!state.data.enabled[state.tool.category];b.textContent=t("运行工具 →")}};
$('capture').onclick=async()=>{const b=$('capture');b.disabled=true;try{await api('/api/tools/computer_screenshot',{});await refresh();toast(t("快照已保存"))}catch(e){toast(e.message)}finally{b.disabled=false}};function stopReplay(){clearTimeout(replayTimer);replayTimer=null;$('play').textContent=t("▶ 播放")}
$('frame-prev').onclick=()=>{stopReplay();state.frame--;void renderFrame()};
$('frame-next').onclick=()=>{stopReplay();state.frame++;void renderFrame()};
$('replay-position').oninput=()=>{stopReplay();state.frame=Number($('replay-position').value);void renderFrame()};
$('play').onclick=async()=>{
 if(replayTimer){stopReplay();return}
 if(state.frame>=state.frames.length-1)state.frame=0;
 $('play').textContent=t("Ⅱ 暂停");
 const tick=async()=>{await renderFrame();if(!replayTimer)return;if(state.frame>=state.frames.length-1){stopReplay();return}replayTimer=setTimeout(()=>{state.frame++;void tick()},1400/Number($('replay-speed').value))};
 replayTimer=-1;await tick();
};
$('copy-prompt').onclick=()=>copyConnection('prompt');$('copy-address').onclick=()=>copyConnection('address');$('copy-config').onclick=()=>copyConnection('config');
$('copy-local-config-prompt').onclick=copyLocalConfiguration;
const nativeExport=!PUBLIC_VIEW&&Boolean(new URLSearchParams(location.search).get('shell'));
let exportState='idle', exportTimer=null, browserExport=null;
function renderExport(p){
 exportState=p.state;
 const active=['running','paused','cancelling'].includes(p.state);
 const names={choosing:t("请选择保存位置"),running:t("正在导出日志…"),paused:t("导出已暂停"),cancelling:t("正在取消…"),cancelled:t("导出已取消"),done:t("日志已保存"),error:t("导出失败"),download:t("已交给浏览器下载")};
 $('export-status').textContent=names[p.state]||'';
 $('export-progress').max=Math.max(1,p.total||0);
 if(p.total!=null)$('export-progress').value=p.state==='done'?Math.max(1,p.total):p.completed||0;
 else $('export-progress').removeAttribute('value');
 $('export-detail').textContent=p.error||[p.total!=null?t("{0} / {1} 条记录 · {2} MB",{0:p.completed||0,1:p.total,2:((p.bytes||0)/1048576).toFixed(2)}):'',p.path||''].filter(Boolean).join(' · ');
 $('export-toggle').hidden=!['running','paused'].includes(p.state);
 $('export-toggle').textContent=p.state==='paused'?t("继续导出"):t("暂停导出");
 $('export-cancel').hidden=!active;$('export-cancel').disabled=p.state==='cancelling';
 $('export-close').hidden=active||p.state==='choosing';
 $('export').disabled=active||p.state==='choosing';
}
async function exportRequest(action){
 const query=action==='start'?params():new URLSearchParams();
 if(action)query.set('action',action);
 const res=await fetch('/api/window/export?'+query,{method:action?'POST':'GET'});
 if(!res.ok)throw new Error(t("导出失败"));
 return res.json();
}
async function pollExport(){
 try{const p=await exportRequest();renderExport(p);if(['running','paused','cancelling'].includes(p.state))exportTimer=setTimeout(pollExport,250)}
 catch(e){$('export-detail').textContent=t("无法获取导出进度，正在重试…");exportTimer=setTimeout(pollExport,1000)}
}
async function exportInBrowser(){
 if(!window.showSaveFilePicker){
  const a=document.createElement('a');a.href=route('/api/export?'+params());a.download='readyrig-calls.ndjson';document.body.append(a);a.click();a.remove();
  renderExport({state:'download',error:t("请在浏览器下载列表中查看进度和保存位置。")});return;
 }
 let writable,reader;
 const job={paused:false,cancelled:false,wake:null,controller:new AbortController()};browserExport=job;
 try{
  const handle=await window.showSaveFilePicker({suggestedName:'readyrig-calls.ndjson'});
  if(job.cancelled)return;
  writable=await handle.createWritable();
  job.progress=()=>({state:job.paused?'paused':'running',path:handle.name});
  renderExport(job.progress());
  const res=await fetch(route('/api/export?'+params()),{signal:job.controller.signal});
  if(!res.ok)throw new Error(t("导出失败"));
  const total=Number(res.headers.get('X-Export-Total'));let completed=0,bytes=0;
  reader=res.body.getReader();
  job.progress=()=>({state:job.paused?'paused':'running',total,completed,bytes,path:handle.name});
  while(true){
   if(job.paused)await new Promise(resolve=>job.wake=resolve);
   if(job.cancelled)throw new DOMException('Cancelled','AbortError');
   const {done,value}=await reader.read();if(done)break;
   if(job.paused)await new Promise(resolve=>job.wake=resolve);
   if(job.cancelled)throw new DOMException('Cancelled','AbortError');
   await writable.write(value);
   if(job.cancelled)throw new DOMException('Cancelled','AbortError');
   bytes+=value.length;
   for(const byte of value)if(byte===10)completed++;
   renderExport(job.progress());
  }
  if(job.cancelled)throw new DOMException('Cancelled','AbortError');
  if(completed!==total)throw new Error(t("导出不完整，请重试"));
  await writable.close();writable=null;renderExport({state:'done',total,completed,bytes,path:handle.name});
 }catch(e){if(writable)await writable.abort().catch(()=>{});renderExport({state:e.name==='AbortError'?'cancelled':'error',error:e.name==='AbortError'?'':e.message})}
 finally{if(reader)await reader.cancel().catch(()=>{});browserExport=null}
}
$('export').onclick=async()=>{
 clearTimeout(exportTimer);$('export-dialog').showModal();renderExport({state:'choosing'});
 try{if(nativeExport){renderExport(await exportRequest('start'));if(['running','paused'].includes(exportState))void pollExport()}else await exportInBrowser()}
 catch(e){renderExport({state:'error',error:e.message})}
};
$('export-toggle').onclick=async()=>{
 if(nativeExport){try{renderExport(await exportRequest(exportState==='paused'?'resume':'pause'))}catch(e){toast(e.message)}}
 else if(browserExport){browserExport.paused=!browserExport.paused;if(!browserExport.paused)browserExport.wake?.();renderExport(browserExport.progress())}
};
$('export-cancel').onclick=async()=>{
 if(nativeExport){try{renderExport(await exportRequest('cancel'))}catch(e){toast(e.message)}}
 else if(browserExport){browserExport.cancelled=true;browserExport.paused=false;browserExport.controller.abort();browserExport.wake?.();renderExport({state:'cancelling'})}
};
$('export-close').onclick=()=>$('export-dialog').close();
$('export-dialog').addEventListener('cancel',event=>{if(['choosing','running','paused','cancelling'].includes(exportState))event.preventDefault()});
if(nativeExport)exportRequest().then(p=>{if(['running','paused','cancelling'].includes(p.state)){$('export-dialog').showModal();renderExport(p);void pollExport()}}).catch(()=>{});
document.addEventListener('keydown',e=>{if((e.metaKey||e.ctrlKey)&&e.key.toLowerCase()==='k'){e.preventDefault();showPage('activity');$('search').focus()}if((e.metaKey||e.ctrlKey)&&e.key.toLowerCase()==='j'){e.preventDefault();theme()}});
async function connect(){try{const key=new URLSearchParams(location.hash.slice(1)).get('key');if(key&&!PUBLIC_VIEW){await api('/api/login',{key});history.replaceState(null,'',location.pathname+location.search)}await refresh();if(state.connected&&!eventStream&&!PUBLIC_VIEW){eventStream=new EventSource(route('/api/events'));eventStream.onmessage=()=>refresh();eventStream.onerror=()=>{$('live').classList.add('off')}}}catch(e){error(e.message)}}
window.addEventListener('hashchange',connect);
setInterval(()=>{if(!document.hidden)refresh()},5000);
document.querySelectorAll('.seg button,.segs button').forEach(b=>b.setAttribute('aria-pressed',String(b.classList.contains('active'))));
void connect();

function renderUpdate(u){
 if(!u)return;
 const names={idle:t("等待检查"),checking:t("正在检查更新…"),latest:t("已是最新版本"),downloading:t("正在下载更新…"),ready:t("更新已准备好"),available:t("有新版本可用"),source:t("开发构建"),disabled:t("自动更新未启用"),error:t("更新失败"),restarting:t("正在重启…")};
 $('update-version').textContent=u.current;
 $('update-status').textContent=names[u.state]||u.state;
 $('update-message').textContent=t(u.reason)||(u.latest?t("新版本 {0} · 当前 {1}", {0: u.latest, 1: u.current}):t("启动后检查，此后每 6 小时检查一次。"));
 $('update-check').disabled=!u.can_check||['checking','downloading','restarting'].includes(u.state);
 $('update-restart').classList.toggle('hidden',!u.can_restart);
 $('update-banner').classList.toggle('hidden',!u.can_restart&&u.state!=='restarting');
 $('update-banner-text').textContent=u.state==='restarting'?t("正在重启 ReadyRig…"):t("ReadyRig {0} 已下载，下次退出时安装。", {0: u.latest});
 $('update-banner-restart').disabled=!u.can_restart;
 $('update-progress').classList.toggle('hidden',u.state!=='downloading');
 if(u.total>0){$('update-progress').max=u.total;$('update-progress').value=u.done}else{$('update-progress').removeAttribute('value')}
 $('update-error').classList.toggle('hidden',!u.error);$('update-error').textContent=t(u.error)||'';
 $('update-notes-box').classList.toggle('hidden',!u.notes);$('update-notes').textContent=u.notes||'';
 let releaseURL='';try{const url=new URL(u.url);if(['https:','http:'].includes(url.protocol))releaseURL=url.href}catch{}
 $('update-release').classList.toggle('hidden',!releaseURL);if(releaseURL)$('update-release').href=releaseURL;else $('update-release').removeAttribute('href');
}
$('update-check').onclick=async()=>{try{renderUpdate(await api('/api/update/check',{}))}catch(e){toast(e.message)}};
async function restartToUpdate(){
 try{await api('/api/update/restart',{});toast(t("正在重启；浏览器控制台请使用终端输出的新链接。"));renderUpdate({...state.data.update,state:'restarting',can_restart:false})}catch(e){toast(e.message)}
}
$('update-restart').onclick=restartToUpdate;$('update-banner-restart').onclick=restartToUpdate;

function connectionGateway(d,mode){
 if(PUBLIC_VIEW||mode==='local')return d.gateway;
 return d.tunnel?.state==='ready'?d.tunnel.gateway:'';
}
function connectionPrompt(gateway,mode,shareMode="quick"){
 const local=!PUBLIC_VIEW&&mode==='local';
 return t("<tools-usage>\n请连接我电脑上的 ReadyRig，并通过它完成我的任务。\n\n接入地址：{0}\n{1}\n\n请先使用你的终端或 HTTP 请求工具完成连接检查：\n1. POST {2}/api/v1/tools/help，请求体 {}。读取返回 result 中的工具名称、参数定义和可用状态。\n2. POST {3}/api/v1/tools/list_projects，请求体 {}，确认已授权目录和默认项目。\n3. 告诉我连接是否成功、有哪些可用能力；如果我已提供具体任务，继续完成，否则等待我的具体任务。不要仅凭这段文字声称已经连接。\n\n后续通过 POST {4}/api/v1/tools/{工具名} 调用工具，直接发送符合该工具参数定义的 JSON；不需要额外套 arguments。完整地址中的随机路径必须保留。可用 curl 发起请求，例如：\ncurl -sS '{5}/api/v1/tools/help' -d '{}'\n\n只在我授权的任务与目录范围内操作。工具被暂停或未授权时，告诉我在 ReadyRig 本机界面开启，不要绕过权限。对需要继续获取输出的命令，按工具返回的 session_id 调用 write_stdin。\n如果你只能使用已配置的 MCP 工具，请让我添加这个 MCP 地址：{6}/mcp；如果既没有 HTTP/终端工具也没有该 MCP 连接，请明确说明当前无法接入。\n</tools-usage>\n\n我想要：...", {0: gateway, 1: local?t("这是本机地址，只能从运行 ReadyRig 的同一台电脑访问；如果你在云端或另一台电脑运行，请让我在 ReadyRig 中切换到「公网」并重新复制 Prompt。"):shareMode==='fixed'?t("这是固定公网地址；仅在 ReadyRig 开启固定链接分享时可用。如果连接失败，请让我确认应用和公网分享正在运行。"):t("这是临时公网地址；如果连接失败或地址失效，请让我确认公网分享已开启并重新复制 Prompt。"), 2: gateway, 3: gateway, 4: gateway, 5: gateway, 6: gateway});
}
function renderConnection(){
 const d=state.data;if(!d)return;
 const t=d.tunnel||{state:'stopped'},active=['installing','starting','ready'].includes(t.state),busy=['installing','starting','stopping'].includes(t.state);
 if(state.connectionMode===null)state.connectionMode=PUBLIC_VIEW||active||d.cloud?.relay?.enabled?'public':'local';
 const remote=PUBLIC_VIEW||state.connectionMode==='public',gateway=connectionGateway(d,state.connectionMode);
 if(state.shareMode===null||active||busy)state.shareMode=t.mode||'quick';
 const fixed=state.shareMode==='fixed';
 $('share-options').classList.toggle('hidden',!remote||PUBLIC_VIEW);
 for(const kind of ['quick','fixed']){const b=$('share-'+kind);b.classList.toggle('active',state.shareMode===kind);b.setAttribute('aria-pressed',String(state.shareMode===kind));b.disabled=active||busy}
 $('share-kind-note').textContent=fixed?readyRigI18n.t("使用自己的域名，关闭后重新开启仍使用同一链接。"):readyRigI18n.t("无需账号或域名；每次开启生成新的临时地址，关闭后失效。");
 $('fixed-settings').classList.toggle('hidden',!fixed);
 if(!state.fixedLoaded&&t.fixed){$('fixed-url').value=t.fixed.url||'';state.fixedLoaded=true}
 for(const id of ['fixed-url','fixed-token','fixed-save'])$(id).disabled=active||busy;
 $('fixed-token').placeholder=t.fixed?.has_token?readyRigI18n.t("已保存；留空保留原令牌"):readyRigI18n.t("粘贴 Cloudflare 隧道令牌");
 $('fixed-saved').textContent=readyRigI18n.t(t.fixed?.error)||(t.fixed?.has_token?readyRigI18n.t("域名和令牌已保存在本机"):readyRigI18n.t("配置仅保存在本机"));
 $('fixed-target').textContent=(d.gateway_origin||'http://127.0.0.1:7332').replace(/^http:\/\//,'');

 for(const mode of ['local','public']){const b=$('connection-'+mode);b.classList.toggle('active',state.connectionMode===mode);b.setAttribute('aria-pressed',String(state.connectionMode===mode))}
 const names={stopped:readyRigI18n.t("未开启"),installing:readyRigI18n.t("正在准备"),starting:readyRigI18n.t("正在连接"),ready:readyRigI18n.t("公网已开启"),stopping:readyRigI18n.t("正在关闭"),error:readyRigI18n.t("连接失败")};
 $('share-state').classList.toggle('hidden',!remote&&!active&&!busy);
 $('share-status').textContent=names[t.state]||t.state;
 $('share-indicator').classList.toggle('ready',t.state==='ready');$('share-indicator').classList.toggle('busy',busy);
 $('share-actions').classList.toggle('hidden',!remote);
 $('share-start').classList.toggle('hidden',active||t.state==='stopping');$('share-start').disabled=busy;$('share-start').textContent=t.state==='error'?readyRigI18n.t("重新连接"):fixed?readyRigI18n.t("开启固定链接"):readyRigI18n.t("开启一次性链接");
 $('share-stop').classList.toggle('hidden',!active&&t.state!=='stopping');$('share-stop').disabled=t.state==='stopping';$('share-stop').textContent=t.state==='installing'?readyRigI18n.t("取消下载"):t.state==='starting'?readyRigI18n.t("取消连接"):readyRigI18n.t("关闭分享");
 $('share-message').classList.toggle('hidden',!remote||!!gateway);
 $('share-message').textContent=t.state==='stopped'?(fixed?readyRigI18n.t("配置好域名路由后，即可开启固定链接。"):readyRigI18n.t("通过 Cloudflare 免登录分享，无需账户或域名。")):t.state==='error'?readyRigI18n.t("暂时无法连接，请重试或展开诊断。"):readyRigI18n.t(t.message)||readyRigI18n.t("正在连接…");
 $('connection-address').classList.toggle('hidden',!gateway);
 $('gateway-address').textContent=gateway||'';
 $('connection-config').textContent=gateway?JSON.stringify({mcpServers:{readyrig:{url:gateway+'/mcp'}}},null,2):'';
 $('copy-address').disabled=!gateway;$('copy-config').disabled=!gateway;
 const prompt=gateway?connectionPrompt(gateway,state.connectionMode,t.mode):'';if($('connection-prompt').value!==prompt)$('connection-prompt').value=prompt;$('copy-prompt').disabled=!gateway;
 $('share-links').classList.toggle('hidden',!remote||t.state!=='ready'||!t.console);
 if(t.console){$('public-console').href=t.console;$('public-console').title=t.console}else{$('public-console').removeAttribute('href');$('public-console').removeAttribute('title')}
 $('connection-note').textContent=remote?(fixed?readyRigI18n.t("仅分享给可信的人，权限仍在本机管理。关闭或退出后断开；重新开启固定分享时，完整地址保持不变。"):readyRigI18n.t("仅分享给可信的人，权限仍在本机管理。关闭分享或退出后断开，重新开启会更换地址。")):readyRigI18n.t("仅供本机连接，重启后地址会更换。远程使用请选择「公网」。");
 $('share-error').textContent=readyRigI18n.t(t.error)||'';$('share-error').classList.toggle('hidden',!remote||!t.error);
 $('share-diagnostics').classList.toggle('hidden',!remote||t.state==='stopped'&&!t.logs?.length);
 $('share-executable').textContent=t.executable||readyRigI18n.t("连接时自动准备");$('share-executable').title=t.executable||'';
 const logs=$('share-logs'),text=t.logs?.join('\n')||readyRigI18n.t("暂无连接记录");
 $('share-log-count').textContent=t.logs?.length?readyRigI18n.t("最近 {0} 条记录", {0: t.logs.length}):readyRigI18n.t("连接记录");
 $('copy-share-logs').disabled=!t.logs?.length;
 if(logs.textContent!==text){const follow=logs.scrollHeight-logs.scrollTop-logs.clientHeight<24;logs.textContent=text;if(follow)logs.scrollTop=logs.scrollHeight}
 renderRoutes(d,t,active,busy,remote);
}
// Two ways for an agent to reach this computer: a direct Cloudflare link, or forwarding through the
// ReadyRig cloud. The cloud route is the opt-in relay: it is the whole path when no link is running,
// and a backup (on standby, holding no connection) while the direct link works.
function renderRoutes(d,t,active,busy,remote){
 if(PUBLIC_VIEW)return;
 const L=readyRigI18n.t,c=d.cloud||{},r=c.relay||{},signed=!!c.device_id,on=!!r.enabled,rs=on?(r.state||'connecting'):'off',ready=t.state==='ready';
 if(state.route===null)state.route=!active&&on?'cloud':'direct';
 const cloudRoute=state.route==='cloud';
 for(const k of ['direct','cloud']){const b=$('route-'+k);b.classList.toggle('selected',state.route===k);b.setAttribute('aria-checked',String(state.route===k))}
 $('route-direct-panel').classList.toggle('hidden',cloudRoute);$('route-cloud-panel').classList.toggle('hidden',!cloudRoute);
 const names={off:'未开启',standby:'待命',connecting:'正在连接',connected:'已连接',error:'连接失败'},login=L('需要先在上方「云端账号」登录。');
 $('backup-toggle').classList.toggle('on',on);$('backup-toggle').setAttribute('aria-checked',String(on));$('backup-toggle').disabled=state.relayBusy||!signed;
 $('backup-note').textContent=state.relayError||(!signed?login:!on?L('开启后，直连链接失效时，文件内容、命令输出和截图会经 ReadyRig 服务器转发；链接正常时不传数据。'):rs==='standby'?L('已开启 · 待命：链接正常，没有数据经过 ReadyRig 服务器。'):rs==='connected'?L('已开启 · 直连链接不可用，已改用 ReadyRig 云端。'):L(r.message||'')||L(names[rs]));
 $('cloud-route-status').textContent=L(names[rs]);$('cloud-route-status').classList.toggle('good',rs==='connected');
 $('cloud-route-toggle').textContent=L(on?'关闭云端转发':'我了解数据会经 ReadyRig 服务器，开启');$('cloud-route-toggle').disabled=state.relayBusy||!signed;
 $('cloud-route-note').textContent=state.relayError||(!signed?login:on&&ready?L('直连链接正常时，这里待命、不传数据。'):on?L(r.message||''):'');
 $('cloud-route-note').classList.toggle('error-text',!!state.relayError);
 const mcp=on&&c.url?c.url.replace(/[/]+$/,'')+'/mcp':'';
 $('cloud-mcp').classList.toggle('hidden',!mcp);$('cloud-mcp-url').textContent=mcp||'—';$('copy-cloud-mcp').disabled=!mcp;
 // One status line for both routes.
 let summary='';
 if(ready&&on&&rs==='standby')summary='直连链接已开启 · 云端备用待命';
 else if(!ready&&on&&rs==='connected')summary=t.state==='stopped'?'经 ReadyRig 云端已连接':'直连链接未就绪 · 云端备用已连接';
 if(summary)$('share-status').textContent=L(summary);
 $('share-state').classList.toggle('hidden',!remote&&!active&&!busy&&!on);
 $('share-indicator').classList.toggle('ready',ready||rs==='connected');
 if(cloudRoute){
  for(const id of ['share-actions','share-message','connection-address','share-links','share-diagnostics','share-error'])$(id).classList.add('hidden');
  $('connection-note').textContent=L('经 ReadyRig 云端转发时，数据会经过 ReadyRig 服务器。权限仍在本机管理，随时可以关闭。');
 }
}
async function setRelayEnabled(enabled){
 state.relayBusy=true;state.relayError='';renderConnection();
 try{await api('/api/cloud/relay',{enabled,acknowledged:enabled});await refresh()}catch(e){state.relayError=e.message}finally{state.relayBusy=false;renderConnection()}
}
for(const k of ['direct','cloud'])$('route-'+k).onclick=()=>{state.route=k;renderConnection()};
$('backup-toggle').onclick=()=>void setRelayEnabled(!state.data?.cloud?.relay?.enabled);
$('cloud-route-toggle').onclick=()=>void setRelayEnabled(!state.data?.cloud?.relay?.enabled);
$('copy-cloud-mcp').onclick=()=>copy($('cloud-mcp-url').textContent);
for(const mode of ['local','public'])$('connection-'+mode).onclick=()=>{state.connectionMode=mode;renderConnection()};
async function changeSharing(action){
 const b=$(action==='start'?'share-start':'share-stop');b.disabled=true;
 try{if(action==='start'&&state.shareMode==='fixed')await saveFixedSettings(false);const t=await api('/api/tunnel/'+action,action==='start'?{mode:state.shareMode||'quick'}:{});state.data.tunnel=t;renderConnection();await refresh()}
 catch(e){toast(e.message);b.disabled=false}
}
for(const kind of ['quick','fixed'])$('share-'+kind).onclick=()=>{state.shareMode=kind;renderConnection()};
async function saveFixedSettings(notify=true){
 const t=await api('/api/tunnel/fixed',{url:$('fixed-url').value,token:$('fixed-token').value});
 state.data.tunnel=t;$('fixed-token').value='';$('fixed-url').value=t.fixed?.url||'';renderConnection();if(notify)toast(t("固定链接配置已保存"));
}
$('fixed-save').onclick=async()=>{const b=$('fixed-save');b.disabled=true;try{await saveFixedSettings()}catch(e){toast(e.message)}finally{b.disabled=false}};
$('share-diagnostics').addEventListener('toggle',()=>{if($('share-diagnostics').open)$('share-logs').scrollTop=$('share-logs').scrollHeight});
$('copy-share-logs').onclick=()=>copy(state.data?.tunnel?.logs?.join('\n')||t("暂无连接记录"));
$('share-start').onclick=()=>changeSharing('start');$('share-stop').onclick=()=>changeSharing('stop');
async function copyConnection(kind){
 const mode=state.connectionMode;
 try{const d=await api('/api/connection'),gateway=connectionGateway(d,mode);if(!gateway)throw new Error(t("分享尚未就绪，请稍后重试"));await copy(kind==='prompt'?connectionPrompt(gateway,mode,d.tunnel?.mode):kind==='config'?JSON.stringify({mcpServers:{readyrig:{url:gateway+'/mcp'}}},null,2):gateway)}catch(e){toast(e.message)}
}
$('copy-public-console').onclick=async()=>{try{const d=await api('/api/connection');if(d.tunnel?.state!=='ready'||!d.tunnel.console)throw new Error(t("分享尚未就绪，请稍后重试"));await copy(d.tunnel.console)}catch(e){toast(e.message)}};

function renderProjects(){
 const access=state.data.project_access;if(!access)return;
 $('project-count').textContent=access.projects.length;
 const html=access.projects.map(p=>`<div class="project-row ${p.id===access.active?'current':''}"><span class="project-folder" aria-hidden="true">${icons.files}</span><div class="project-info"><div class="project-name"><strong>${esc(p.name)}</strong>${p.id===access.active?`<span class="project-default">${t("默认")}</span>`:''}${access.pinned?.[p.id]?`<span class="project-default">${t("{0} 个会话使用中",{0:access.pinned[p.id]})}</span>`:''}</div><div class="project-location" title="${esc(p.path)}">${esc(p.path)}</div></div><div class="project-actions">${p.id!==access.active?`<button class="field" data-project-activate="${esc(p.id)}" aria-label="${t("将 {0} 设为默认", {0: esc(p.name)})}">${t("设为默认")}</button>`:''}<button class="project-more" data-project-more="${esc(p.id)}" aria-label="${t("{0} 的更多操作", {0: esc(p.name)})}" aria-haspopup="menu" aria-expanded="false"><svg viewBox="0 0 20 20" width="18" height="18" fill="currentColor" aria-hidden="true"><circle cx="4" cy="10" r="1.5"/><circle cx="10" cy="10" r="1.5"/><circle cx="16" cy="10" r="1.5"/></svg></button></div></div>`).join('');
 if($('project-list')._html!==html){closeProjectMenu();$('project-list').innerHTML=html;$('project-list')._html=html}
 $('full-access').classList.toggle('on',access.full_access);$('full-access').setAttribute('aria-checked',access.full_access);
 $('access-badge').textContent=access.full_access?t("已开启"):t("仅限项目目录");
 $('access-badge').classList.toggle('enabled',access.full_access);
 $('access-description').textContent=PUBLIC_VIEW?(access.full_access?t("目前可以访问项目之外的目录，权限在本机管理。"):t("目前仅能使用已添加的项目目录，权限在本机管理。")):access.full_access?t("可以访问项目之外的目录，重启后恢复限制。"):t("开启后，可访问当前账户可读写的其他目录。");
 $('disk-access').classList.toggle('hidden',!['darwin','macOS'].includes(state.data.permissions.platform));
}
let projectMenuID='',projectMenuAnchor=null,projectAppVersion=0,projectAppID='',projectAppFocus=false;
function closeProjectAppMenu(){
 projectAppVersion++;projectAppID='';projectAppFocus=false;$('project-app-menu').classList.add('hidden');$('project-menu-open-with').setAttribute('aria-expanded','false');
}
function closeProjectMenu(){
 closeProjectAppMenu();
 $('project-menu').classList.add('hidden');projectMenuAnchor?.setAttribute('aria-expanded','false');projectMenuID='';projectMenuAnchor=null;
}
function openProjectMenu(button){
 if(PUBLIC_VIEW)return;
 const id=button.dataset.projectMore;if(projectMenuID===id){closeProjectMenu();return}
 closeProjectMenu();projectMenuID=id;projectMenuAnchor=button;
 const menu=$('project-menu');menu.classList.remove('hidden');button.setAttribute('aria-expanded','true');
 $('project-menu-remove').disabled=state.data.project_access.projects.length===1;
 $('project-menu-open').disabled=openingLocalPath;
 $('project-menu-open-with').disabled=openingLocalPath;
 $('project-menu-open-with').classList.toggle('hidden',!state.data.local_open?.applications);
 const rect=button.getBoundingClientRect();menu.style.left=Math.max(8,Math.min(rect.right-menu.offsetWidth,innerWidth-menu.offsetWidth-8))+'px';
 menu.style.top=(rect.bottom+menu.offsetHeight+8>innerHeight?Math.max(8,rect.top-menu.offsetHeight-4):rect.bottom+4)+'px';
 (openingLocalPath?$('project-menu-rename'):$('project-menu-open')).focus();
}
let openingLocalPath=false;
async function openLocalPath(project,path,application=''){
 if(PUBLIC_VIEW||openingLocalPath)return;
 openingLocalPath=true;
 try{const result=await api('/api/files/open',{project,path,application});if(result.opened)toast(t("已交给系统打开"))}catch(e){toast(e.message)}finally{openingLocalPath=false}
}
function openProjectFolder(application=''){
 const id=projectMenuID;closeProjectMenu();
 if(id)void openLocalPath(id,'.',application);
}
$('project-menu-open').onclick=()=>openProjectFolder();
function positionProjectAppMenu(){
 const menu=$('project-app-menu'),parent=$('project-menu').getBoundingClientRect(),anchor=$('project-menu-open-with').getBoundingClientRect();
 const left=parent.right+menu.offsetWidth+4<=innerWidth?parent.right+4:parent.left-menu.offsetWidth-4;
 menu.style.left=Math.max(8,Math.min(left,innerWidth-menu.offsetWidth-8))+'px';
 menu.style.top=Math.max(8,Math.min(anchor.top,innerHeight-menu.offsetHeight-8))+'px';
}
function updateProjectAppFade(){
 const list=$('project-app-list'),remaining=list.scrollHeight-list.clientHeight-list.scrollTop;
 list.style.setProperty('--fade-top',Math.min(16,Math.max(0,list.scrollTop))+'px');
 list.style.setProperty('--fade-bottom',Math.min(16,Math.max(0,remaining))+'px');
}
$('project-app-list').addEventListener('scroll',updateProjectAppFade,{passive:true});
async function openProjectApplications(focus=true){
 if(!projectMenuID||openingLocalPath||PUBLIC_VIEW||!state.data.local_open?.applications)return;
 const menu=$('project-app-menu'),list=$('project-app-list');
 if(projectAppID===projectMenuID){projectAppFocus ||= focus;if(focus)menu.querySelector('button')?.focus();return}
 const id=projectMenuID,version=++projectAppVersion;projectAppID=id;projectAppFocus=focus;
 list.innerHTML=`<div class="project-app-message" role="status">${t("正在读取打开方式…")}</div>`;list.scrollTop=0;menu.classList.remove('hidden');
 $('project-menu-open-with').setAttribute('aria-expanded','true');positionProjectAppMenu();updateProjectAppFade();
 try{
  const result=await api('/api/files/applications?'+new URLSearchParams({project:id,path:'.'}));
  if(version!==projectAppVersion||id!==projectMenuID)return;
  list.innerHTML=result.applications.map(app=>`<button role="menuitem" data-open-application="${esc(app.id)}" title="${esc(app.name)}">${app.icon?`<img class="project-app-icon" src="${esc(app.icon)}" alt="">`:'<span class="project-app-icon" aria-hidden="true"></span>'}<span class="project-app-name">${esc(app.name)}</span>${app.default?`<span class="project-app-default">${t("默认")}</span>`:''}</button>`).join('')||`<div class="project-app-message" role="status">${t("没有可用的打开方式")}</div>`;
 }catch(e){if(version!==projectAppVersion||id!==projectMenuID)return;list.innerHTML=`<div class="project-app-message" role="status">${esc(e.message)}</div>`}
 positionProjectAppMenu();if(projectAppFocus)menu.querySelector('button')?.focus();updateProjectAppFade();
}
$('project-menu-open-with').onclick=()=>void openProjectApplications();
$('project-menu-open-with').onmouseenter=()=>void openProjectApplications(false);
for(const button of $('project-menu').querySelectorAll('button:not(#project-menu-open-with)'))button.onmouseenter=closeProjectAppMenu;
$('project-app-menu').onclick=e=>{const button=e.target.closest('[data-open-application]');if(button)openProjectFolder(button.dataset.openApplication)};
$('project-menu-rename').onclick=()=>{const p=state.data.project_access.projects.find(p=>p.id===projectMenuID);closeProjectMenu();if(p)openProject(p)};
$('project-menu-remove').onclick=async()=>{
 const id=projectMenuID;closeProjectMenu();
 try{await api('/api/projects',{action:'remove',id});await refresh();toast(t("已移除目录授权，文件保留"))}catch(e){toast(e.message)}
};
document.addEventListener('click',e=>{if(e.target.closest('[data-project-more]'))openProjectMenu(e.target.closest('[data-project-more]'));else if(!e.target.closest('#project-menu,#project-app-menu'))closeProjectMenu()});
document.addEventListener('keydown',e=>{
 if(!projectMenuID)return;
 const inApps=!!document.activeElement?.closest('#project-app-menu');
 if(e.key==='Escape'){e.preventDefault();if(!$('project-app-menu').classList.contains('hidden')){closeProjectAppMenu();$('project-menu-open-with').focus()}else{const anchor=projectMenuAnchor;closeProjectMenu();anchor?.focus()}}
 if(e.key==='ArrowRight'&&document.activeElement===$('project-menu-open-with')){e.preventDefault();void openProjectApplications()}
 if(e.key==='ArrowLeft'&&inApps){e.preventDefault();closeProjectAppMenu();$('project-menu-open-with').focus()}
 if(['ArrowUp','ArrowDown','Home','End'].includes(e.key)){e.preventDefault();if(!inApps)closeProjectAppMenu();const buttons=[...(inApps?$('project-app-menu'):$('project-menu')).querySelectorAll('button:not(:disabled):not(.hidden)')];const i=buttons.indexOf(document.activeElement);const next=e.key==='Home'?0:e.key==='End'?buttons.length-1:(i+(e.key==='ArrowDown'?1:-1)+buttons.length)%buttons.length;buttons[next]?.focus()}
});
document.querySelector('.view').addEventListener('scroll',closeProjectMenu,{passive:true});window.addEventListener('resize',closeProjectMenu);
let directoryParent='',directoryVersion=0,editingProject='';
async function browseDirectory(path){
 const version=++directoryVersion;$('project-error').textContent='';
 try{
  const d=await api('/api/directories?'+new URLSearchParams({path}));if(version!==directoryVersion)return;
  $('project-path').value=d.path;directoryParent=d.parent;$('directory-up').disabled=d.path===d.parent;
  $('directory-list').innerHTML=d.directories.map(p=>`<button type="button" class="directory-entry" data-directory="${esc(p.path)}"><span class="tool-icon">${icons.files}</span><span>${esc(p.name)}</span><span class="chev">›</span></button>`).join('')||`<p class="directory-empty">${t("此目录没有子目录，可以直接添加。")}</p>`;
  $('directory-browser').classList.remove('hidden');$('directory-truncated').classList.toggle('hidden',!d.truncated);
 }catch(e){if(version===directoryVersion){$('directory-browser').classList.add('hidden');$('project-error').textContent=e.message}}
}
function openProject(project){
 if(!project&&new URLSearchParams(location.search).get('shell')==='darwin'){void addNativeProject();return}
 editingProject=project?.id||'';directoryVersion++;
 $('project-name').value=project?.name||'';$('project-path').value=project?.path||'';$('project-path').disabled=!!project;
 $('browse-directory').classList.toggle('hidden',!!project);$('directory-browser').classList.add('hidden');$('project-error').textContent='';
 $('project-dialog-title').textContent=project?t("重命名项目"):t("添加项目目录");$('save-project').textContent=project?t("保存名称"):t("添加目录");
 $('project-dialog').showModal();if(!project)void browseDirectory('');
}
async function addNativeProject(){
 const button=$('add-project');if(button.disabled)return;button.disabled=true;
 try{
  const {path}=await api('/api/window/select-directory',{});if(!path)return;
  await api('/api/projects',{action:'add',path});toast(t("目录已添加"));await refresh();
 }catch(e){toast(e.message)}finally{button.disabled=false}
}
$('add-project').onclick=()=>openProject();$('close-project').onclick=()=>$('project-dialog').close();
$('browse-directory').onclick=()=>browseDirectory($('project-path').value);
$('directory-up').onclick=()=>browseDirectory(directoryParent);$('directory-home').onclick=()=>browseDirectory('');
$('project-path').addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();void browseDirectory(e.target.value)}});
$('project-form').onsubmit=async e=>{
 e.preventDefault();const b=$('save-project');b.disabled=true;$('project-error').textContent='';
 try{await api('/api/projects',{action:editingProject?'rename':'add',id:editingProject,name:$('project-name').value,path:$('project-path').value});$('project-dialog').close();toast(editingProject?t("项目已重命名"):t("目录已添加"));await refresh()}catch(e){$('project-error').textContent=e.message}finally{b.disabled=false}
};
$('full-access').onclick=async()=>{
 const b=$('full-access');b.disabled=true;
 try{const access=await api('/api/access',{full_access:!state.data.project_access.full_access});state.data.project_access=access;renderProjects();toast(access.full_access?t("完全访问已开启，本次运行有效"):t("已恢复项目目录限制"));await refresh()}catch(e){toast(e.message)}finally{b.disabled=false}
};
$('open-disk-settings').onclick=async()=>{try{await api('/api/access/system-settings',{});toast(t("已打开 macOS 完全磁盘访问权限设置"))}catch(e){toast(e.message)}};
document.addEventListener('click',async e=>{
 const b=e.target.closest('button');if(!b)return;
 try{
  if(b.dataset.systemPermission)await openPermissionSettings(b.dataset.systemPermission);
  if(b.dataset.directory)await browseDirectory(b.dataset.directory);
  if(b.dataset.projectRename)openProject(state.data.project_access.projects.find(p=>p.id===b.dataset.projectRename));
  if(b.dataset.projectActivate||b.dataset.projectRemove){b.disabled=true;await api('/api/projects',{action:b.dataset.projectActivate?'activate':'remove',id:b.dataset.projectActivate||b.dataset.projectRemove});await refresh();toast(b.dataset.projectActivate?t("默认项目已切换"):t("已移除目录授权，文件保留"))}
 }catch(e){b.disabled=false;toast(e.message)}
});

window.addEventListener('readyrig-language-error',event=>toast(event.detail));
window.addEventListener('readyrig-language-change',()=>{
 if(!state.data)return;
 renderGlobal();renderCalls();renderDetail(state.detail?.call);renderTools();renderProjects();renderSettings();
 if(state.replayDetail)paintReplay();else if(state.page==='replay')void renderFrame();
 if($('tool-dialog').open&&state.tool)renderToolText();
 if($('project-dialog').open){$('project-dialog-title').textContent=t(editingProject?'重命名项目':'添加项目目录');$('save-project').textContent=t(editingProject?'保存名称':'添加目录');}
});
