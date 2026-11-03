const $ = (s, root = document) => root.querySelector(s);
const $$ = (s, root = document) => [...root.querySelectorAll(s)];
let csrf = sessionStorage.getItem('espkvm-csrf') || '';
let state = null;
let refreshHandle = null;
let streamController = null;
let streamRetryHandle = null;
let streamRetryDelay = 1000;
const notice = (message = '', kind = 'error') => {
  const openDialog = $('dialog[open]');
  const inline = openDialog?.querySelector('.dialog-feedback');
  const el = inline || $('#notice');
  el.textContent = message; el.hidden = !message; el.dataset.kind = kind;
};
function toast(message, kind = 'success') {
  if (!message) return;
  const root = $('#toast-root'), el = document.createElement('div');
  el.className = 'toast'; el.dataset.kind = kind; el.textContent = message;
  root.append(el);
  const dismiss = () => { el.classList.add('toast-leave'); setTimeout(() => el.remove(), 220); };
  setTimeout(dismiss, 4200);
}
async function api(path, body) {
  const options = { headers: { 'X-CSRF-Token': csrf } };
  if (body !== undefined) { options.method = 'POST'; options.headers['Content-Type'] = 'application/json'; options.body = JSON.stringify(body); }
  const response = await fetch('/api/' + path, options);
  if (!response.ok) throw new Error(await response.text() || `HTTP ${response.status}`);
  if (response.status === 204) return null;
  return response.json();
}
fetch('/api/version').then(response => response.ok ? response.json() : Promise.reject()).then(info => {
  $('#version-label').textContent = info.version || '版本未知'; $('#version-label').title = info.version || '';
}).catch(() => { $('#version-label').textContent = '版本未知'; });
async function boot() {
  const fragment = location.hash.slice(1);
  const token = new URLSearchParams(fragment).get('token') || decodeURIComponent(fragment);
  if (token) {
    history.replaceState(null, '', location.pathname + location.search);
    try { const result = await fetch('/api/bootstrap', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({token}) }); if (!result.ok) throw new Error(await result.text()); csrf = (await result.json()).csrf; sessionStorage.setItem('espkvm-csrf', csrf); }
    catch (e) { showLogin('登录链接已失效。请使用登录密码继续。'); return; }
  }
  try { if (!csrf) throw new Error('login required'); await enterApp(); }
  catch (e) { sessionStorage.removeItem('espkvm-csrf'); csrf=''; showLogin(); }
}
function showLogin(message = '') {
  stopStateEvents();
  $('#app-shell').hidden = true; $('#login-screen').hidden = false; $('#change-password').hidden = true;
  $('#input-status').textContent = '等待登录'; $('#input-dot').className = '';
  $('#login-error').textContent = message; $('#login-error').hidden = !message;
}
async function enterApp() {
  await api('session');
  $('#login-screen').hidden = true; $('#app-shell').hidden = false; $('#change-password').hidden = false;
  await refresh();
  connectStateEvents();
}
function showError(e) { toast(String(e.message || e).trim(), 'error'); }
function stopStateEvents() {
  streamController?.abort(); streamController = null;
  if (streamRetryHandle) clearTimeout(streamRetryHandle);
  streamRetryHandle = null;
  if (refreshHandle) clearInterval(refreshHandle);
  refreshHandle = null;
}
function startFallbackRefresh() {
  if (!refreshHandle) refreshHandle = setInterval(() => refresh().catch(() => {}), 3000);
}
function connectStateEvents() {
  if (!csrf || streamController) return;
  if (streamRetryHandle) clearTimeout(streamRetryHandle);
  streamRetryHandle = null;
  const controller = new AbortController();
  streamController = controller;
  (async () => {
    try {
      const response = await fetch('/api/events', {headers:{'X-CSRF-Token':csrf},signal:controller.signal,cache:'no-store'});
      if (response.status === 401 || response.status === 403) {
        stopStateEvents(); sessionStorage.removeItem('espkvm-csrf'); csrf=''; showLogin('登录已过期，请重新登录。'); return;
      }
      if (!response.ok || !response.body) throw new Error(`SSE HTTP ${response.status}`);
      if (refreshHandle) clearInterval(refreshHandle);
      refreshHandle = null; streamRetryDelay = 1000;
      const reader = response.body.getReader(), decoder = new TextDecoder();
      let buffer = '';
      while (!controller.signal.aborted) {
        const {value,done} = await reader.read();
        if (done) throw new Error('SSE stream closed');
        buffer += decoder.decode(value,{stream:true}).replace(/\r\n/g,'\n');
        let boundary;
        while ((boundary = buffer.indexOf('\n\n')) >= 0) {
          const frame = buffer.slice(0,boundary); buffer = buffer.slice(boundary+2);
          let eventName = 'message'; const data = [];
          for (const line of frame.split('\n')) {
            if (line.startsWith('event:')) eventName = line.slice(6).trim();
            else if (line.startsWith('data:')) data.push(line.slice(5).trimStart());
          }
          if (eventName === 'state' && data.length) {
            try { state = JSON.parse(data.join('\n')); render(); }
            catch (e) { console.warn('Invalid EspKVM state event',e); }
          }
        }
      }
    } catch (e) {
      if (controller.signal.aborted) return;
      startFallbackRefresh();
    } finally {
      if (streamController === controller) streamController = null;
      if (!controller.signal.aborted && csrf) {
        const delay = streamRetryDelay;
        streamRetryDelay = Math.min(streamRetryDelay * 2, 10000);
        streamRetryHandle = setTimeout(connectStateEvents,delay);
      }
    }
  })();
}
function serialStatus(message, kind = 'info') { const el=$('#serial-message'); el.textContent=message; el.dataset.kind=kind; }
async function act(fn, success = '', button = null) {
  const originalText = button?.textContent;
  if (button) { button.disabled = true; button.textContent = '处理中…'; }
  notice('');
  try { await fn(); await refresh(); if (success) toast(success, 'success'); }
  catch (e) { showError(e); }
  finally { if (button?.isConnected) { button.disabled = false; button.textContent = originalText; } }
}
document.addEventListener('invalid', e => {
  if (e.target?.closest('dialog') || e.target?.closest('#login-form')) toast('请检查必填项，并确认输入格式正确。', 'error');
}, true);

$('#login-form').onsubmit = async e => {
  e.preventDefault();
  const form=e.currentTarget, button=$('#login-submit'), old=button.textContent;
  button.disabled=true; button.textContent='正在登录…'; $('#login-error').hidden=true;
  try {
    const response=await fetch('/api/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:form.elements.password.value})});
    if(!response.ok)throw new Error((await response.text())||`HTTP ${response.status}`);
    csrf=(await response.json()).csrf; sessionStorage.setItem('espkvm-csrf',csrf); form.reset();
    await enterApp(); toast('登录成功。','success');
  } catch(err) { $('#login-error').textContent=String(err.message||err).includes('incorrect password')?'密码不正确，请重试。':'登录失败：'+String(err.message||err); $('#login-error').hidden=false; toast($('#login-error').textContent,'error'); }
  finally {button.disabled=false;button.textContent=old;}
};
$('#change-password').onclick=()=>{$('#password-form').reset();$('#password-dialog').showModal();};
$('#close-password-dialog').onclick=()=>$('#password-dialog').close();
$('#password-form').onsubmit=async e=>{
  if(e.submitter?.value==='cancel')return;
  e.preventDefault(); const form=e.currentTarget, f=form.elements, button=e.submitter, old=button?.textContent;
  if(f.next.value!==f.confirm.value){toast('两次输入的新密码不一致。','error');f.confirm.focus();return;}
  if(f.next.value.length<8){toast('新密码至少需要 8 个字符。','error');f.next.focus();return;}
  if(button){button.disabled=true;button.textContent='正在保存…';}
  try {
    const response=await fetch('/api/password',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:JSON.stringify({current:f.current.value,next:f.next.value})});
    if(!response.ok)throw new Error((await response.text())||`HTTP ${response.status}`);
    $('#password-dialog').close(); form.reset(); csrf='';sessionStorage.removeItem('espkvm-csrf');showLogin();toast('登录密码已修改，请使用新密码重新登录。','success');
  } catch(err) { const message=String(err.message||err); toast(message.includes('current password')?'当前密码不正确。':message.includes('8 to 128')?'新密码长度必须为 8 到 128 个字符。':message,'error'); }
  finally {if(button){button.disabled=false;button.textContent=old;}}
};
function targetLabel(id) { if (id === 'LOCAL') return '本机'; return state.devices.find(d => d.id === id)?.name || id || '本机'; }
function firmwareLabel(value) {
  const version=Number(value||0)>>>0;
  if(!version)return '—';
  return `v${(version>>>16)&255}.${(version>>>8)&255}.${version&255}`;
}
function keepAwakeLabel(d) {
  if (!d.connected) return '设备未连接';
  return ['未启用','暂停：HOST 心跳超时','暂停：HOST 无近期操作','已停止：设备空闲超过 C','等待空闲间隔','保持中'][d.keep_awake_state] || '未知';
}
function keepAwakeMoveMarkup(d) {
  const timestamp = Date.parse(d.keep_awake_last_move_at || '');
  if (!Number.isFinite(timestamp)) return '<small class="keep-awake-last-move">尚无模拟移动</small>';
  const age = Date.now() - timestamp;
  const time = new Date(timestamp).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit',second:'2-digit'});
  return `<small class="keep-awake-last-move ${age >= 0 && age < 20000 ? 'recent' : ''}">上次模拟移动 ${time}</small>`;
}
function setupKeepAwake(form, container) {
  const fields=document.createElement('fieldset'); fields.className='keep-awake-fields';
  fields.innerHTML=`<legend>防休眠策略</legend><label class="check"><input name="keepAwakeEnabled" type="checkbox">启用此设备的防休眠</label>
    <p class="keep-awake-unsupported muted small" hidden>当前设备固件未提供防休眠配置，请先升级固件。</p>
    <div class="two-col"><label>最短空闲检查间隔 A（秒）<input name="keepAwakeMin" type="number" min="1" max="86400" step="1" value="30" required></label><label>最长空闲检查间隔 B（秒）<input name="keepAwakeMax" type="number" min="1" max="86400" step="1" value="60" required></label></div>
    <p class="muted small">每次从 A～B 秒中随机选择间隔。期间操作该设备会重新计时；空闲到期时轻微随机移动鼠标。</p>
    <label>设备无主动操作后停止 C（秒）<input name="keepAwakeStop" type="number" min="0" max="4294967295" step="1" value="1800" required></label>
    <p class="muted small">默认 1800 秒（30 分钟），0 表示一直保持。重新操作该设备后恢复计时；模拟移动不算主动操作。</p>
    <label>HOST 最近有操作的时间范围 D（秒）<input name="keepAwakeHost" type="number" min="0" max="4294967295" step="1" value="120" required></label>
    <p class="muted small">默认 120 秒。HOST 操作本机或任意设备都算；D 大于 0 时，HOST 断开或心跳超过 3 秒未到会暂停防休眠。D 为 0 时始终满足此前提，HOST 离线也可保持。USB 未就绪或有按键、鼠标按钮按住时不会模拟移动。</p>
    <p class="muted small keep-awake-save-note">随设备配置一起保存至 ESP32，保存后设备重启生效。</p>`;
  if(container)container.append(fields); else form.querySelector('.actions').before(fields);
  form.elements.keepAwakeEnabled.onchange=()=>{ updateKeepAwakeFields(form); toast('防休眠策略将在保存设备配置后生效。','info'); };
  writeKeepAwake(form, {enabled:false,interval_min_s:30,interval_max_s:60,stop_after_s:1800,host_active_within_s:120});
}
function updateKeepAwakeFields(form) {
  const supported=form.dataset.keepAwakeSupported==='true';
  form.elements.keepAwakeEnabled.disabled=!supported;
  for(const name of ['keepAwakeMin','keepAwakeMax','keepAwakeStop','keepAwakeHost'])form.elements[name].disabled=!supported||!form.elements.keepAwakeEnabled.checked;
  form.querySelector('.keep-awake-unsupported').hidden=supported;
}
function writeKeepAwake(form, policy) {
  form.dataset.keepAwakeSupported=policy?'true':'false';
  const f=form.elements, p=policy||{};
  f.keepAwakeEnabled.checked=!!p.enabled; f.keepAwakeMin.value=p.interval_min_s??30; f.keepAwakeMax.value=p.interval_max_s??60;
  f.keepAwakeStop.value=p.stop_after_s??1800; f.keepAwakeHost.value=p.host_active_within_s??120;
  updateKeepAwakeFields(form);
}
function readKeepAwake(form) {
  if(form.dataset.keepAwakeSupported!=='true')return undefined;
  const f=form.elements;
  const p={enabled:f.keepAwakeEnabled.checked,interval_min_s:Number(f.keepAwakeMin.value),interval_max_s:Number(f.keepAwakeMax.value),stop_after_s:Number(f.keepAwakeStop.value),host_active_within_s:Number(f.keepAwakeHost.value)};
  if(!Object.entries(p).every(([k,v])=>k==='enabled'||Number.isInteger(v)&&v>=0&&v<=4294967295)||p.interval_min_s<1||p.interval_max_s>86400||p.interval_min_s>p.interval_max_s)throw new Error('防休眠参数不正确：A 和 B 必须为 1～86400 的整数，且 A 不能大于 B；C 和 D 必须是非负整数。');
  return p;
}
function hotkeyLabel(keys) {
  const modifiers = {224:'Ctrl',225:'Shift',226:'Alt',227:'Win',228:'Ctrl',229:'Shift',230:'Alt',231:'Win'};
  return (keys || []).map(code => modifiers[code] || (code >= 4 && code <= 29 ? String.fromCharCode(65 + code - 4) : `键${code}`)).join('+') || '未设置';
}
function render() {
  if (!state) return;
  const input = $('#input-status'), dot = $('#input-dot'); input.textContent = state.input || '未知'; dot.className = state.input === 'running' ? 'ok' : '';
  const sw = state.switch || {}, current = sw.target || 'LOCAL';
  const rdpFallback = /win/i.test(navigator.platform) ? ' · RDP 内：Ctrl + 侧键 1 向前，侧键 2 向后' : '';
  $('#switch-status').textContent = sw.recording ? '正在录制：按下并释放一组修饰键组合。再次点击按钮结束录制。' : `切换快捷键：${hotkeyLabel(state.hotkey)}${rdpFallback} · 当前目标：${targetLabel(current)}${sw.phase && sw.phase !== 'idle' ? ` · ${sw.phase}` : ''}${sw.warning ? ` · ⚠ ${sw.warning}` : ''}`;
  $('#record-hotkey').textContent = sw.recording ? '结束录制' : '录制切换热键';
  const targets = [{id:'LOCAL', name:'本机', ready:true, status:'本地输入'}, ...state.devices.map(d => ({id:d.id,name:d.name || d.id,ready:d.enabled && d.connected && d.usb,status:!d.enabled?'设备已禁用':!d.connected?'等待 TCP 连接':!d.usb?'TCP 在线 · 等待 USB HID 枚举':d.usb_sleeping?'目标机睡眠中 · 切换后操作键鼠可唤醒':'TCP 与 USB HID 已就绪'}))];
  $('#targets').innerHTML = targets.map(t => `<button class="target ${current===t.id?'active':''}" data-target="${esc(t.id)}"><i class="dot ${t.ready?'ok':''}"></i><span>${esc(t.name)}<small>${esc(t.status)}</small></span></button>`).join('');
  $$('[data-target]').forEach(b => b.onclick = () => act(() => api('target',{id:b.dataset.target}), `${targetLabel(b.dataset.target)}已设为输入目标。` , b));
  const devices = $('#devices'); $('#device-count').textContent = `${state.devices.length} 台`;
  devices.innerHTML = state.devices.length ? state.devices.map(d => `<article class="device"><div class="device-head"><i class="dot ${d.connected?'ok':''}"></i><span class="device-name">${esc(d.name || d.id)}</span><button class="icon-button edit-device" data-id="${esc(d.id)}" title="编辑">⋯</button></div><div class="device-ip">${esc(d.id)} · ${esc(d.ip)}:${d.port}</div><div class="device-meta"><span>TCP 连接<strong>${d.connected?'已连接':'未连接'}</strong></span><span>USB HID<strong>${!d.usb?'未枚举':d.usb_sleeping?'睡眠中':'已就绪'}</strong></span><span>Wi-Fi 信号<strong>${d.rssi || '—'} dBm</strong></span><span class="firmware-meta">设备固件<strong class="firmware-value">${esc(d.firmware_version || firmwareLabel(d.firmware))}</strong></span><span>往返延迟<strong>${d.rtt?.p50_ns ? (d.rtt.p50_ns/1e6).toFixed(1)+' ms':'—'}</strong></span><span class="keep-awake-status">防休眠<strong>${esc(keepAwakeLabel(d))}</strong>${keepAwakeMoveMarkup(d)}</span></div>${d.error?`<p class="small error-text">${esc(d.error)}</p>`:''}<div class="device-actions"><button class="button take-device" data-id="${esc(d.id)}" ${d.connected?'disabled':''} title="当设备被另一台主机占用时，请求让本机替换其会话；不切换键鼠目标">${d.connected?'本机已连接':'从其他主机接管'}</button><button class="button serial-provision" data-id="${esc(d.id)}">串口配网</button><button class="button config-device" data-id="${esc(d.id)}">在线配置</button><button class="button delete-device danger" data-id="${esc(d.id)}">删除</button></div></article>`).join('') : '<div class="empty">还没有设备。添加设备后，可在这里管理连接和输入目标。</div>';
  $$('.edit-device').forEach(b => b.onclick = () => editDevice(b.dataset.id));
  $$('.take-device:not(:disabled)').forEach(b => b.onclick = () => { if (confirm('如果设备正由另一台 EspKVM 主机控制，这会请求断开该会话并让本机连接。它不会切换当前键鼠输入目标。继续？')) act(() => api('takeover',{id:b.dataset.id}), '接管请求已发送；设备连回本机后，还需在上方选择输入目标。', b); });
  $$('.serial-provision').forEach(b => b.onclick = () => openSerialProvision(b.dataset.id, b));
  $$('.config-device').forEach(b => b.onclick = () => openConfig(b.dataset.id, b));
  $$('.delete-device').forEach(b => b.onclick = () => {if(confirm('删除此设备及本机保存的配对凭据？不会修改设备本身。')) act(() => api('device-delete',{id:b.dataset.id}), '设备及本机凭据已删除。', b);});
  const pending = state.pending || []; $('#pending-section').hidden = !pending.length;
  $('#pending-list').innerHTML = pending.map(p => `<div class="device pending-card"><div><b>${esc(p.name || p.id)}</b><span class="pending-state">${p.state==='persisted'?'设备已确认写入':'写入结果待确认'}</span></div><p class="muted small">这是 EspKVM 暂存的新旧连接资料，用于处理串口断线或设备改址。验证新连接成功后，新资料才会替换本机当前配置。</p><div class="device-actions"><button class="button primary verify-pending" data-id="${esc(p.id)}">验证并完成</button><button class="button discard-pending danger" data-id="${esc(p.id)}">删除本机恢复记录</button></div></div>`).join('');
  $$('.verify-pending').forEach(b => b.onclick = () => act(() => api('pending-verify',{id:b.dataset.id}), '验证成功，本机设备配置已更新。', b));
  $$('.discard-pending').forEach(b => b.onclick = () => {if(confirm('这只会删除本机保存的恢复记录，不会更改设备上的 Wi-Fi 或其他配置。确定删除？')) act(() => api('pending-discard',{id:b.dataset.id}), '本机恢复记录已删除；设备配置未更改。', b);});
}
function esc(x) { return String(x ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
async function refresh() { state = await api('state'); render(); }
$('#add-device').onclick = () => { notice(''); const f=$('#device-form'); f.reset(); f.elements.originalId.value=''; f.elements.id.disabled=false; $('#dialog-title').textContent='添加设备'; $('#device-dialog').showModal(); };
$('#close-device-dialog').onclick = () => $('#device-dialog').close();
$('#close-config-dialog').onclick = () => $('#config-dialog').close();
$('#close-serial-dialog').onclick = () => $('#serial-dialog').close();
document.querySelectorAll('dialog').forEach(dialog => dialog.addEventListener('mousedown', event => {
  if (event.button === 0 && event.target === dialog && dialog.open) dialog.close();
}));
$('#generate-psk').onclick = e => { const bytes=crypto.getRandomValues(new Uint8Array(16)); $('#device-form').elements.psk.value=[...bytes].map(b=>b.toString(16).padStart(2,'0')).join(''); toast('已生成新的 16 字节随机 PSK。'); };
function editDevice(id) { const d=state.devices.find(x=>x.id===id); if(!d)return; const f=$('#device-form'); f.reset(); f.elements.originalId.value=d.id; f.elements.id.value=d.id; f.elements.id.disabled=true; f.elements.name.value=d.name; f.elements.ip.value=d.ip; f.elements.port.value=d.port; f.elements.psk.value=''; f.elements.enabled.checked=d.enabled; $('#dialog-title').textContent='编辑设备'; $('#device-dialog').showModal(); }
$('#device-form').onsubmit = e => { if(e.submitter?.value==='cancel')return; e.preventDefault(); const f=e.currentTarget.elements; const device={id:f.id.value,name:f.name.value,ip:f.ip.value,port:Number(f.port.value),psk:f.psk.value,enabled:f.enabled.checked}; act(async()=>{await api('device-save',device); $('#device-dialog').close();}, '设备信息已保存。本机正在尝试连接；首次配网请在设备卡上点击“串口配网”。', e.submitter); };
$('#record-hotkey').onclick = () => { const active=!state?.switch?.recording; act(async()=>{ await api('recording',{active}); }, active?'已开始录制，请按下并释放要用作切换键的组合。':'热键录制已结束。', $('#record-hotkey')); };
function dotted(n) { return [24,16,8,0].map(s=>(n>>>s)&255).join('.'); }
async function openConfig(id, button) { const d=state.devices.find(x=>x.id===id); const f=$('#config-form'); const old=button?.textContent; if(button){button.disabled=true;button.textContent='读取配置…';} notice(''); f.reset(); f.elements.id.value=id; try { const c=await api('device-config-get',{id}); f.elements.deviceName.value=c.name||d?.name||''; f.elements.ssid.value=c.ssid||''; f.elements.ip.value=dotted(c.ip); f.elements.gw.value=dotted(c.gw); f.elements.mask.value=dotted(c.mask); f.dataset.vid=c.usb_vid; f.dataset.pid=c.usb_pid; f.dataset.port=c.tcp_port||24800; f.dataset.sixKro=String(!!c.six_kro); writeKeepAwake(f,c.keep_awake); $('#config-dialog').showModal(); toast('设备配置已读取。','success'); } catch(e) { showError(e); } finally {if(button?.isConnected){button.disabled=false;button.textContent=old;}} }
function ipv4(value) { const parts=value.split('.'); if(parts.length!==4||parts.some(p=>!/^\d{1,3}$/.test(p)||Number(p)>255))throw new Error('请输入有效的 IPv4 地址'); return parts.reduce((n,p)=>((n*256)+Number(p))>>>0,0); }
function serialError(e) {
  const message=String(e.message||e).trim();
  if(/access is denied|permission denied|resource busy/i.test(message))return '无法打开串口。请先关闭其他串口监视器或烧录工具，再重试。';
  const invalid=/invalid configuration field (\d+)/i.exec(message);
  if(invalid){const labels={1:'设备名称',2:'Wi-Fi 名称',3:'Wi-Fi 密码',4:'PSK',5:'USB VID',6:'USB PID',7:'设备 IP',8:'网关',9:'子网掩码',10:'TCP 端口',12:'防休眠策略（A、B 范围）'};return `配置格式不正确：请检查${labels[Number(invalid[1])]||`字段 ${invalid[1]}`}。`;}
  const rejected=/configuration rejected \(code (\d+)(?:, field (\d+))?\)/i.exec(message);
  if(rejected){
    const code=Number(rejected[1]), field=Number(rejected[2]||0);
    if(code===1){const labels={1:'设备名称',2:'Wi-Fi 名称',3:'Wi-Fi 密码',4:'PSK',5:'USB VID',6:'USB PID',7:'设备 IP',8:'网关',9:'子网掩码',10:'TCP 端口',12:'防休眠策略（A、B 范围）'};return `设备拒绝了配置：${labels[field]||`字段 ${field}`}不符合要求。请检查后重试。`;}
    if(code===2)return '设备无法把配置提交到 NVS（闪存存储）。设备没有确认保存，请检查固件日志和 NVS 状态。';
    if(code===3)return '设备当前正忙，配置未写入。稍等片刻后再试。';
  }
  if(/timeout/i.test(message))return '串口读取超时，无法确认设备是否收到配置。不要重复写入；先查看“配网待验证”。若串口显示 waiting for download，请退出 ROM 下载模式并确认应用固件正在运行。';
  return message;
}
function serialReadError(e) {
  const message=String(e.message||e).trim();
  if(/access is denied|permission denied|resource busy/i.test(message))return '无法打开串口。请先关闭其他串口监视器或烧录工具，再重试。';
  if(/timeout/i.test(message))return '设备没有返回 EspKVM 配置信息。若串口日志显示 waiting for download，芯片正停在 ROM 下载模式；释放 BOOT/GPIO0 并重启到 SPI 启动模式，确认应用固件已烧录。';
  return `读取设备信息失败：${message}`;
}
$('#config-form').onsubmit = e => { if(e.submitter?.value==='cancel')return; e.preventDefault(); const form=e.currentTarget, f=form.elements; try { const config={name:f.deviceName.value,ssid:f.ssid.value,ip:ipv4(f.ip.value),gw:ipv4(f.gw.value),mask:ipv4(f.mask.value),tcp_port:Number(form.dataset.port||24800),usb_vid:Number(form.dataset.vid),usb_pid:Number(form.dataset.pid)}; config.keep_awake=readKeepAwake(form); config.six_kro=form.dataset.sixKro==='true'; if(f.password.value)config.password=f.password.value; act(async()=>{await api('device-config-set',{id:f.id.value,config});$('#config-dialog').close();}, '配置已发送。若设备断线或地址改变，请在“配网待验证”中验证新连接。', e.submitter); } catch(err) { showError(err); } };
async function refreshSerialPorts() {
  const select=$('#serial-port');
  const button=$('#refresh-ports'), old=button.textContent; button.disabled=true; button.textContent='查找中…';
  try { const ports=await api('serial-ports',{}); select.innerHTML=ports.length?ports.map(p=>`<option value="${esc(p)}">${esc(p)}</option>`).join(''):'<option value="">没有发现串口</option>'; const message=ports.length?`发现 ${ports.length} 个串口。`:'没有发现串口。请连接设备后重试。'; serialStatus(ports.length?`${message}选择连接设备的端口后读取信息。`:message,ports.length?'info':'error'); toast(message,ports.length?'success':'error'); }
  catch(e) { select.innerHTML='<option value="">串口列表读取失败</option>'; serialStatus(`读取串口列表失败：${e.message}`,'error'); showError(e); }
  finally { button.disabled=false; button.textContent=old; }
}
async function openSerialProvision(id, trigger) {
  const form=$('#serial-form'); form.reset(); form.elements.id.value=id; form.dataset.paired='';
  $('#serial-fields').hidden=true; $('#send-serial-config').disabled=true; serialStatus('选择 USB 串口并读取设备信息后，填写网络配置。'); notice('');
  $('#serial-device-label').textContent=`将通过串口为设备 ${id} 配置网络`;
  if(trigger){trigger.disabled=true;trigger.textContent='准备串口配网…';}
  await refreshSerialPorts(); $('#serial-dialog').showModal();
  if(trigger?.isConnected){trigger.disabled=false;trigger.textContent='串口配网';}
}
$('#refresh-ports').onclick=()=>refreshSerialPorts();
$('#read-serial-info').onclick=async()=>{
  const form=$('#serial-form'), port=form.elements.serialPort.value;
  if(!port){serialStatus('没有可用串口。请连接设备并点击“刷新”。','error');return;}
  const button=$('#read-serial-info'), old=button.textContent; button.disabled=true; button.textContent='读取中…'; serialStatus('正在读取设备信息，请稍候…');
  try {
    const info=await api('serial-info',{port}); const c=info.config||{};
    $('#serial-device-label').textContent=`设备 ${form.elements.id.value} · 设备固件 ${info.fw_version || firmwareLabel(info.fw_ver)}`;
    form.dataset.paired=info.paired?'true':'false';
    form.elements.ssid.value=c.ssid||''; form.elements.password.value=''; form.elements.deviceName.value=c.name||state.devices.find(d=>d.id===form.elements.id.value)?.name||'';
    form.elements.ip.value=dotted(c.ip||0); form.elements.gw.value=dotted(c.gw||0); form.elements.mask.value=dotted(c.mask||0);
    form.elements.usbVid.value=Number(c.usb_vid||0).toString(16).padStart(4,'0'); form.elements.usbPid.value=Number(c.usb_pid||0).toString(16).padStart(4,'0');
    form.elements.tcpPort.value=c.tcp_port||24800;
    form.dataset.sixKro=String(!!c.six_kro); writeKeepAwake(form,c.keep_awake);
    $('#serial-password-help').textContent=info.paired?'已配对设备：密码留空会保留设备当前密码；输入新密码可更改。':'首次配网：密码留空表示使用开放 Wi-Fi。';
    $('#serial-fields').hidden=false; $('#send-serial-config').disabled=false;
    serialStatus(info.paired?'已读取已配对设备信息，请确认网络参数。':'已读取未配对设备信息，请填写 Wi-Fi 名称和网络参数。','success');
    toast('设备信息读取成功。');
  } catch(e) { const message=serialReadError(e); serialStatus(message,'error'); toast(message,'error'); }
  finally { button.disabled=false; button.textContent=old; }
};
$('#serial-form').onsubmit=e=>{
  if(e.submitter?.value==='cancel')return;
  e.preventDefault(); const form=e.currentTarget, f=form.elements;
  try {
    const config={name:f.deviceName.value,ssid:f.ssid.value,ip:ipv4(f.ip.value),gw:ipv4(f.gw.value),mask:ipv4(f.mask.value),tcp_port:Number(f.tcpPort.value),usb_vid:parseInt(f.usbVid.value,16),usb_pid:parseInt(f.usbPid.value,16)};
    config.keep_awake=readKeepAwake(form); config.six_kro=form.dataset.sixKro==='true';
    if(form.dataset.paired!=='true'||f.password.value)config.password=f.password.value;
    const request={port:f.serialPort.value,id:f.id.value,config};
    act(async()=>{await api('serial-set',request);$('#serial-dialog').close();}, '配置已写入设备。设备重新联网后，请在“配网待验证”中点击“验证并完成”，更新本机保存的地址和凭据。', e.submitter);
  } catch(err) { toast(serialError(err),'error'); }
};
setupKeepAwake($('#config-form'));
setupKeepAwake($('#serial-form'),$('#serial-fields'));
boot().catch(showError);
