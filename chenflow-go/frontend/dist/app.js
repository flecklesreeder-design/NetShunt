/* ==================== ChenFlow 前端应用逻辑 ==================== */
/* 页面切换 · HTTP API 桥接 · 轮询实时数据 · 事件处理 */


// ===== Wails Go API 桥接 =====
function api(method, params = {}) {
  return new Promise((resolve, reject) => {
    let retries = 0;
    function tryCall() {
      if (window.go && window.go.main && window.go.main.App) {
        window.go.main.App.ApiCall(method, params)
          .then(r => resolve(r))
          .catch(e => {
            console.error(`[API] ${method} 失败:`, e);
            reject(e);
          });
      } else if (retries++ < 50) {
        setTimeout(tryCall, 100);
      } else {
        reject(new Error('Wails 绑定未就绪'));
      }
    }
    tryCall();
  });
}

// ===== 工具函数 =====
function $(sel) { return document.querySelector(sel); }
function $$(sel) { return document.querySelectorAll(sel); }

function confirmDialog(message) {
  return new Promise(resolve => {
    const dialog = $('#confirmDialog');
    $('#confirmMessage').textContent = message;
    dialog.style.display = 'flex';
    $('#confirmOk').onclick = () => { dialog.style.display = 'none'; resolve(true); };
    $('#confirmCancel').onclick = () => { dialog.style.display = 'none'; resolve(false); };
  });
}

async function handleCloseWindow() {
  try {
    const res = await api('get_close_action');
    const action = res.action || 'prompt';
    if (action === 'minimize') {
      api('hide_window');
    } else if (action === 'tray') {
      api('hide_to_tray');
    } else if (action === 'quit') {
      api('close_window');
    } else {
      const dialog = $('#closeDialog');
      dialog.style.display = 'flex';
      $('#closeDialogMinimize').onclick = () => {
        dialog.style.display = 'none';
        api('set_close_action', {action: 'minimize'});
        api('hide_window');
      };
      $('#closeDialogTray').onclick = () => {
        dialog.style.display = 'none';
        api('set_close_action', {action: 'tray'});
        api('hide_to_tray');
      };
      $('#closeDialogQuit').onclick = () => {
        dialog.style.display = 'none';
        api('set_close_action', {action: 'quit'});
        api('close_window');
      };
    }
  } catch (e) {
    api('close_window');
  }
}

function showToast(message) {
  const overlay = $('#toastOverlay');
  const box = $('#toastBox');
  $('#toastContent').textContent = message;
  overlay.classList.add('show');
  box.style.animation = 'none';
  void box.offsetWidth;
  box.style.animation = '';
  $('#toastBtn').onclick = () => overlay.classList.remove('show');
}

function appendLog(area, text) {
  const el = $(area);
  el.appendChild(document.createTextNode(text + '\n'));
  el.scrollTop = el.scrollHeight;
}

// ===== 页面导航 =====
function switchPage(name) {
  $$('.page').forEach(p => p.classList.remove('active'));
  $(`#page-${name}`).classList.add('active');
  $$('.nav-btn[data-page]').forEach(btn => {
    btn.classList.toggle('active', btn.dataset.page === name);
  });
  if (name === 'traffic') refreshAdapterSelect();
  if (name === 'settings') loadSettingsData();
}

function switchSubPage(name) {
  $$('.sub-page').forEach(p => p.classList.remove('active'));
  $(`#sub-${name}`).classList.add('active');
  $$('.sub-nav-btn').forEach(btn => {
    btn.classList.toggle('active', btn.dataset.subpage === name);
  });
  if (name === 'adapters') refreshAdapterManager();
  if (name === 'strategies') loadStrategyList();
  if (name === 'hotkey_setting') {
    api('get_hotkey').then(res => {
      if (res && res.hotkey) {
        $('#hotkeyInput').value = res.hotkey.replace(/\+/g, '+').replace(/\b\w/g, c => c.toUpperCase());
        $('#hotkeyInput').style.color = '';
        updateHotkeyHint(res.hotkey);
      }
    });
  }
  if (name === 'close_behavior') initCloseBehavior();
}

// ===== 主题切换 =====
function setTheme(theme) {
  document.documentElement.setAttribute('data-theme', theme);
  api('set_theme', {theme});
}

// ===== 语言切换 =====
function setLanguage(lang) {
  if (typeof setLang === 'function') setLang(lang);
  api('set_language', {language: lang});
  $$('.lang-card').forEach(card => {
    const radio = card.querySelector('input[type="radio"]');
    const active = card.dataset.lang === lang;
    card.classList.toggle('active', active);
    if (radio) radio.checked = active;
  });
  if (typeof loadStrategyList === 'function') loadStrategyList();
  if (typeof refreshAdapterManager === 'function') refreshAdapterManager();
  if (typeof loadAdapterMonitor === 'function') loadAdapterMonitor();
}

// ===== 主页 =====
function showApplyProgress() {
  const overlay = $('#applyProgressOverlay');
  if (overlay) overlay.classList.add('show');
  const bar = $('#applyProgressBar');
  if (bar) bar.style.width = '0%';
  const msg = $('#applyProgressMsg');
  if (msg) msg.textContent = t('common.preparing');
  const count = $('#applyProgressCount');
  if (count) count.textContent = '0 / 0';
}
function hideApplyProgress() {
  const overlay = $('#applyProgressOverlay');
  if (overlay) overlay.classList.remove('show');
}
async function applyRoles() {
  const btn = $('#btnApply');
  if (btn) btn.classList.add('tool-card-loading');
  showApplyProgress();
  try {
    await api('apply_strategies');
  } catch (e) {
    hideApplyProgress();
    showToast(t('common.apply_fail', e.message || e));
    if (btn) btn.classList.remove('tool-card-loading');
  }
}

async function toggleGuard() {
  const enabled = $('#guardSwitch').checked;
  await api('toggle_guard', {enabled});
}

function clearLog() {
  $('#logArea').textContent = '';
}

// ===== 流量监控 =====
async function refreshAdapterSelect() {
  try {
    const res = await api('get_adapter_names');
    const select = $('#adapterSelect');
    const current = select.value;
    select.innerHTML = '<option value="">' + t('traffic.select_placeholder') + '</option>';
    (res.names || []).forEach(name => {
      const opt = document.createElement('option');
      opt.value = name; opt.textContent = name;
      select.appendChild(opt);
    });
    if (current) select.value = current;
  } catch (e) { console.error(e); }
}

// ===== 分析仪 =====
async function toggleLogging() {
  const enabled = $('#logSwitch').checked;
  if (enabled) {
    $('#logPathLabel').textContent = t('analyzer.log_on');
  } else {
    $('#logPathLabel').textContent = t('analyzer.log_off');
  }
  await api('toggle_logging', {enabled});
}

// ===== 诊断工具 =====
async function runPing() {
  const target = $('#targetInput').value.trim();
  if (!target) return;
  appendLog('#toolLogArea', t('diag.ping_running', target));
  const off = window.runtime.EventsOn('diag:line', (line) => appendLog('#toolLogArea', line));
  window.runtime.EventsOnce('diag:done', () => {
    off();
    appendLog('#toolLogArea', t('diag.ping_done'));
  });
  await api('ping', {target});
}

async function runTracert() {
  const target = $('#targetInput').value.trim();
  if (!target) return;
  appendLog('#toolLogArea', t('diag.tracert_running', target));
  const off = window.runtime.EventsOn('diag:line', (line) => appendLog('#toolLogArea', line));
  window.runtime.EventsOnce('diag:done', () => {
    off();
    appendLog('#toolLogArea', t('diag.tracert_done'));
  });
  await api('tracert', {target});
}

async function flushDns() {
  appendLog('#toolLogArea', t('diag.flushdns_running'));
  const res = await api('flush_dns');
  appendLog('#toolLogArea', res.result || '');
  appendLog('#toolLogArea', t('diag.done'));
}

function runDiagStream(label, method, extra) {
  appendLog('#toolLogArea', `--- ${label} ---`);
  const off = window.runtime.EventsOn('diag:line', (line) => appendLog('#toolLogArea', line));
  window.runtime.EventsOnce('diag:done', () => {
    off();
    appendLog('#toolLogArea', t('diag.tool_done', label));
  });
  api(method, extra || {});
}

async function runNslookup() {
  const target = $('#targetInput').value.trim();
  if (!target) return;
  runDiagStream(`Nslookup ${target}`, 'nslookup', {target});
}

async function runNetstat() {
  runDiagStream('Netstat -ano', 'netstat');
}

async function runRoutePrint() {
  runDiagStream('Route Print', 'route_print');
}

async function runArpTable() {
  runDiagStream('ARP Table', 'arp_table');
}

async function runIpconfigAll() {
  runDiagStream('IPConfig /all', 'ipconfig_all');
}

async function runPortTest() {
  const target = $('#targetInput').value.trim();
  const port = $('#portInput').value.trim();
  if (!target || !port) return;
  runDiagStream(t('diag.port_test_title', target, port), 'port_test', {target, port});
}

async function runSpeedTest() {
  runDiagStream(t('diag.speed_test_title'), 'speed_test');
}

// ===== 设置: 网卡管理 =====
async function refreshAdapterManager() {
  const container = $('#adapterMgrList');
  if (container) container.innerHTML = '<div style="padding:16px;color:var(--text-muted)">' + t('adapter.loading') + '</div>';
  try {
    const [profileRes, stratRes] = await Promise.all([
      api('get_adapter_profiles'),
      api('get_strategies')
    ]);
    strategyCache = stratRes.strategies || [];
    renderAdapterList(profileRes.profiles || []);
  } catch (e) {
    if (container) container.innerHTML = `<div style="padding:16px;color:var(--danger)">加载失败: ${e.message || e}</div>`;
    console.error('refreshAdapterManager:', e);
  }
}

function renderAdapterList(profiles) {
  const container = $('#adapterMgrList');
  container.innerHTML = '';
  const stratTypeLabels = { fallback: t('strategy.type_label.fallback'), online: t('strategy.type_label.online'), local: t('strategy.type_label.local') };
  const stratTypeColors = {
    fallback: ['#fff', 'var(--accent)'],
    online: ['#fff', 'var(--success)'],
    local: ['#fff', 'var(--purple)'],
  };
  profiles.forEach(p => {
    const row = document.createElement('div');
    row.className = 'adapter-row';
    const statusColor = p.is_up ? 'online' : 'offline';
    const statusTip = p.is_up ? t('strategy.status.online') : t('strategy.status.offline');
    const gwMode = p.gateway_mode === 'manual' ? t('adapter.gateway_manual') : t('adapter.gateway_auto');
    const gwValue = p.gateway_manual || p.gateway_auto || '';
    const gwEffective = p.gateway || t('adapter.empty');

    const sid = p.strategy_id;
    const boundStrat = strategyCache.find(s => s.id === sid);
    const stratType = boundStrat ? boundStrat.type : '';
    const tc = stratTypeColors[stratType] || ['var(--text-tertiary)', 'var(--bg-tertiary)'];
    const tl = boundStrat ? (stratTypeLabels[stratType] || stratType) : t('strategy.no_bind');
    const isDefaultExit = p.is_default_exit;

    row.innerHTML = `
      <div class="adapter-status-dot ${statusColor}"></div>
      <span class="text-${p.is_up ? 'success' : 'danger'}" style="font-size:11px;width:32px">${statusTip}</span>
      <div class="adapter-info">
        <div class="adapter-name">${p.name}</div>
        <div class="adapter-detail">IP ${p.ip || t('adapter.empty')}  ·  ${t('adapter.gateway')} ${p.if_index || t('adapter.empty')}</div>
        <div class="adapter-gw-row">
          <span class="text-tertiary" style="font-size:11px;width:28px">${t('adapter.gateway')}</span>
          <select class="select select-sm gw-mode-sel" data-name="${p.name}" style="width:80px;height:24px">
            <option ${gwMode===t('adapter.gateway_auto')?'selected':''}>${t('adapter.gateway_auto')}</option>
            <option ${gwMode===t('adapter.gateway_manual')?'selected':''}>${t('adapter.gateway_manual')}</option>
          </select>
          <input type="text" class="input input-sm gw-entry" data-name="${p.name}"
                 value="${gwValue}" style="width:140px;height:24px"
                 ${p.gateway_mode !== 'manual' ? 'disabled' : ''}>
          <span class="text-secondary gw-effective" style="font-size:11px">${t('adapter.gateway_effective', gwEffective)}</span>
        </div>
      </div>
      <span class="role-badge" style="color:${tc[0]};background:${tc[1]}">${tl}</span>
      <label style="display:flex;align-items:center;gap:4px;cursor:pointer;font-size:11px;color:var(--text-secondary);white-space:nowrap;flex-shrink:0">
        <input type="checkbox" class="default-exit-cb" data-name="${p.name}" ${isDefaultExit ? 'checked' : ''}>
        ${t('adapter.default_exit')}
      </label>
      <select class="select select-sm strategy-sel" data-name="${p.name}" style="width:132px;height:32px">
        <option value="" ${sid == null || sid < 0 ? 'selected' : ''}>${t('adapter.no_bind')}</option>
        ${strategyCache.filter(s => s.type !== 'fallback').map(s => `<option value="${s.id}" ${sid === s.id ? 'selected' : ''}>${s.name}</option>`).join('')}
      </select>
    `;
    container.appendChild(row);
  });

  $$('.strategy-sel').forEach(sel => {
    sel.onchange = () => {
      const val = sel.value;
      const strategyId = val === '' ? -1 : parseInt(val);
      api('bind_strategy_to_adapter', { adapter_name: sel.dataset.name, strategy_id: strategyId }).then(() => refreshAdapterManager());
    };
  });
  $$('.default-exit-cb').forEach(cb => {
    cb.onchange = () => {
      api('set_default_exit', { adapter_name: cb.dataset.name, enabled: cb.checked }).then(() => refreshAdapterManager());
    };
  });
  $$('.gw-mode-sel').forEach(sel => {
    sel.onchange = () => {
      const entry = container.querySelector(`.gw-entry[data-name="${sel.dataset.name}"]`);
      entry.disabled = sel.value !== t('adapter.gateway_manual');
      api('set_gateway_mode', {name: sel.dataset.name, mode: sel.value});
    };
  });
  $$('.gw-entry').forEach(entry => {
    entry.onchange = () => api('set_gateway_value', {name: entry.dataset.name, value: entry.value});
  });
}

async function recommendRoles() {
  await api('recommend_roles');
  refreshAdapterManager();
  loadStrategyList();
}

// ===== 设置: 地址管理 =====
async function importAddresses() {
  const input = document.createElement('input');
  input.type = 'file';
  input.accept = '.txt';
  input.onchange = async () => {
    const file = input.files[0];
    if (!file) return;
    const text = await file.text();
    const lines = text.split('\n').filter(l => l.trim() && !l.startsWith('#'));
    const profileRes = await api('get_adapter_profiles');
    const profiles = profileRes.profiles || [];
    const defaultAdapter = profiles.length > 0 ? profiles[0].name : '';
    let count = 0;
    for (let line of lines) {
      line = line.trim();
      if (!line) continue;
      let parts = line.includes('|') ? line.split('|').map(p => p.trim()) : [line];
      let rType, target;
      if (parts.length >= 2) { rType = parts[0]; target = parts[1]; }
      else {
        target = parts[0];
        rType = /^\d+\.\d+\.\d+\.\d+$/.test(target) ? 'IPv4' : 'URL';
      }
      try { await api('add_rule', {type: rType, target, adapter: defaultAdapter}); count++; }
      catch (e) { console.error(t('rule.import_fail'), line, e); }
    }
    const console_el = $('#addrConsole');
    if (console_el) console_el.textContent = t('addr_mgr.console_hint', count, text);
    showToast(t('addr_mgr.import_ok', count));
  };
  input.click();
}

function clearAddresses() {
  const el = $('#addrConsole');
  if (el) el.textContent = t('addr_mgr.template');
}

// ===== 设置: 网卡监控 =====
async function loadAdapterMonitor() {
  const container = $('#adapterMonitorList');
  if (container) container.innerHTML = '<div style="padding:16px;color:var(--text-muted)">' + t('monitor.loading') + '</div>';
  try {
    const data = await api('get_adapter_monitor_config');
    $('#monitorSwitch').checked = data.enabled;
    container.innerHTML = '';
    (data.adapters || []).forEach(name => {
      const item = document.createElement('div');
      item.className = 'adapter-monitor-item';
      const checked = (data.monitored || []).includes(name) ? 'checked' : '';
      item.innerHTML = `<input type="checkbox" data-name="${name}" ${checked}> <span>${name}</span>`;
      container.appendChild(item);
    });
    const saveBtn = document.createElement('button');
    saveBtn.className = 'btn btn-accent';
    saveBtn.textContent = t('monitor.save');
    saveBtn.style.cssText = 'margin:16px 0';
    saveBtn.onclick = saveAdapterMonitor;
    container.appendChild(saveBtn);
  } catch (e) {
    if (container) container.innerHTML = `<div style="padding:16px;color:var(--danger)">` + t('monitor.load_fail', e.message||e) + `</div>`;
  }
}

async function saveAdapterMonitor() {
  const monitored = $$('#adapterMonitorList input[type="checkbox"]:checked')
    .map(cb => cb.dataset.name);
  await api('save_adapter_monitor', {enabled: $('#monitorSwitch').checked, monitored});
}

// ===== 规则管理器 =====
async function openRuleManager() {
  $('#ruleModal').style.display = 'flex';
  await refreshRuleList();
  await refreshRuleAdapterOptions();
}

function closeRuleManager() {
  $('#ruleModal').style.display = 'none';
}

async function refreshRuleList() {
  const body = $('#ruleListBody');
  if (body) body.innerHTML = '<div style="padding:16px;color:var(--text-muted)">' + t('rule.loading') + '</div>';
  try {
    const res = await api('get_rules');
    body.innerHTML = '';
    const rules = res.rules || [];
    if (rules.length === 0) {
      body.innerHTML = '<div style="padding:16px;color:var(--text-muted)">' + t('rule.empty') + '</div>';
      return;
    }
    rules.forEach(r => {
      const row = document.createElement('div');
      row.className = 'rule-row';
      const ipStr = r.type === 'URL' ? t('rule.ip_count', (r.ips||[]).length) : ((r.ips||[])[0] || '-');
      const statusColor = r.status === 'Pending' ? 'var(--warning)' : 'var(--success)';
      row.innerHTML = `
        <span style="width:80px;color:var(--accent)">[${r.type}]</span>
        <span style="width:200px">${r.target}</span>
        <span style="width:200px;color:var(--text-secondary);font-size:12px">${ipStr}</span>
        <span style="width:100px">${r.adapter}</span>
        <span style="width:80px;color:${statusColor};font-size:12px">${r.status}</span>
        <button class="btn btn-danger btn-sm" style="width:50px" data-id="${r.id}">${t('rule.delete')}</button>
      `;
      body.appendChild(row);
    });
    $$('#ruleListBody button[data-id]').forEach(btn => {
      btn.onclick = async () => {
        try { await api('delete_rule', {id: parseInt(btn.dataset.id)}); refreshRuleList(); }
        catch (e) { showToast(t('rule.delete_fail', e.message||e)); }
      };
    });
  } catch (e) {
    if (body) body.innerHTML = `<div style="padding:16px;color:var(--danger)">` + t('rule.load_fail', e.message||e) + `</div>`;
  }
}

async function refreshRuleAdapterOptions() {
  try {
    const res = await api('get_adapter_names');
    const sel = $('#newRuleAdapter');
    sel.innerHTML = '';
    (res.names || []).forEach(n => { const o = document.createElement('option'); o.textContent = n; sel.appendChild(o); });
  } catch (e) { console.error('refreshRuleAdapterOptions:', e); }
}

async function addRule() {
  const type = $('#newRuleType').value;
  const target = $('#newRuleTarget').value.trim();
  const adapter = $('#newRuleAdapter').value;
  if (!target) { showToast(t('rule.input_target')); return; }
  try {
    await api('add_rule', {type, target, adapter});
    $('#newRuleTarget').value = '';
    refreshRuleList();
  } catch (e) { showToast(t('rule.add_fail', e.message||e)); }
}

async function applyAllRules() {
  const btn = $('#btnApplyAllRules');
  if (btn) { btn.disabled = true; btn.textContent = t('rule.applying'); }
  showApplyProgress();
  const title = $('#applyProgressTitle');
  if (title) title.textContent = t('rule.applying_title');
  try {
    await api('apply_all_rules');
  } catch (e) {
    hideApplyProgress();
    showToast(t('rule.apply_fail', e.message||e));
    if (btn) { btn.disabled = false; btn.textContent = t('rule.apply_strategy'); }
    if (title) title.textContent = t('rule.applying_strategy');
  }
}

function downloadTemplate() {
  const text = t('rule.template');
  const blob = new Blob(['\ufeff' + text], {type: 'text/plain;charset=utf-8'});
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'chenflow_rules_template.txt';
  a.click();
}

async function exportRules() {
  try {
    const res = await api('get_rules');
    const rules = res.rules || [];
    let text = t('rule.export_header');
    rules.forEach(r => { text += `${r.type}|${r.target}|${r.adapter}\n`; });
    const blob = new Blob(['\ufeff' + text], {type: 'text/plain;charset=utf-8'});
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'chenflow_rules_export.txt';
    a.click();
  } catch (e) { showToast(t('rule.export_fail', e.message||e)); }
}

async function importRules() {
  const input = document.createElement('input');
  input.type = 'file';
  input.accept = '.txt,.csv';
  input.onchange = async () => {
    const file = input.files[0];
    if (!file) return;
    const text = await file.text();
    const lines = text.split('\n');
    let imported = 0, skipped = 0;
    const profileRes = await api('get_adapter_profiles');
    const profiles = profileRes.profiles || [];
    const defaultAdapter = profiles.length > 0 ? profiles[0].name : '';
    for (let i = 0; i < lines.length; i++) {
      let line = lines[i].trim();
      if (!line || line.startsWith('#')) continue;
      let parts;
      if (line.includes('|')) {
        parts = line.split('|').map(p => p.trim());
      } else if (line.includes(',')) {
        parts = line.split(',').map(p => p.trim());
        if (i === 0 && (parts[0] === t('rule.skip_header') || parts[0] === 'Type')) continue;
      } else {
        parts = [line];
      }
      let rType, target, adapter;
      if (parts.length >= 3) {
        rType = parts[0]; target = parts[1]; adapter = parts[2];
      } else if (parts.length === 2) {
        rType = parts[0]; target = parts[1]; adapter = defaultAdapter;
      } else {
        target = parts[0];
        if (/^\d+\.\d+\.\d+\.\d+$/.test(target)) rType = 'IPv4';
        else if (target.includes(':')) rType = 'IPv6';
        else rType = 'URL';
        adapter = defaultAdapter;
      }
      if (!target) { skipped++; continue; }
      try {
        await api('add_rule', {type: rType, target, adapter});
        imported++;
      } catch (e) {
        console.error(t('rule.import_fail'), parts, e);
        skipped++;
      }
    }
    refreshRuleList();
    showToast(t('rule.import_ok', imported, skipped));
  };
  input.click();
}

async function exportRules() {
  try {
    const res = await api('get_rules');
    const rules = res.rules || [];
    let csv = t('rule.csv_header');
    rules.forEach(r => { csv += `${r.type},${r.target},${r.adapter}\n`; });
    const blob = new Blob(['\ufeff' + csv], {type: 'text/csv;charset=utf-8'});
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'chenflow_rules_export.csv';
    a.click();
  } catch (e) { showToast(t('rule.export_fail', e.message||e)); }
}


// ===== 安全重置 =====
async function emergencyReset() {
  const ok = await confirmDialog(t('reset.confirm'));
  if (!ok) return;
  showApplyProgress();
  const title = $('#applyProgressTitle');
  if (title) title.textContent = t('reset.running_title');
  try {
    await api('emergency_reset');
  } catch (e) {
    hideApplyProgress();
    showToast(t('reset.fail', e.message || e));
    if (title) title.textContent = t('reset.applying_title');
  }
}

// ===== 设置数据加载 =====
async function loadSettingsData() {
  refreshAdapterManager();
}

// ===== 快捷键设置 =====
let hotkeyCapturing = false;

async function initHotkeySetting() {
  const input = $('#hotkeyInput');
  const btnCapture = $('#btnCaptureHotkey');
  const btnReset = $('#btnResetHotkey');
  if (!input || !btnCapture || !btnReset) return;

  try {
    const res = await api('get_hotkey');
    if (res && res.hotkey) {
      input.value = res.hotkey.replace(/\+/g, '+').replace(/\b\w/g, c => c.toUpperCase());
      updateHotkeyHint(res.hotkey);
    }
  } catch (e) {}

  btnCapture.onclick = () => {
    if (hotkeyCapturing) {
      hotkeyCapturing = false;
      btnCapture.textContent = t('hotkey.reset');
      input.style.color = '';
      api('get_hotkey').then(res => {
        if (res && res.hotkey) input.value = res.hotkey.replace(/\+/g, '+').replace(/\b\w/g, c => c.toUpperCase());
      });
      return;
    }
    hotkeyCapturing = true;
    input.value = t('hotkey.pressing');
    input.style.color = 'var(--accent)';
    btnCapture.textContent = t('hotkey.cancel');
  };

  btnReset.onclick = async () => {
    try {
      const res = await api('set_hotkey', { hotkey: 'ctrl+alt+h' });
      if (res && res.ok) {
        input.value = 'Ctrl+Alt+H';
        input.style.color = '';
        updateHotkeyHint('ctrl+alt+h');
        showToast(t('hotkey.reset_ok'));
      } else {
        showToast(res && res.error ? res.error : t('hotkey.set_fail'));
      }
    } catch (e) {
      showToast(t('hotkey.capture_fail', e.message || e));
    }
  };

  document.addEventListener('keydown', async (e) => {
    if (!hotkeyCapturing) return;
    e.preventDefault();
    e.stopPropagation();

    const mods = [];
    if (e.ctrlKey) mods.push('ctrl');
    if (e.altKey) mods.push('alt');
    if (e.shiftKey) mods.push('shift');
    if (e.metaKey) mods.push('win');

    let key = '';
    if (e.key.length === 1 && /[a-z0-9]/i.test(e.key)) {
      key = e.key.toLowerCase();
    } else if (/^F\d$/i.test(e.key)) {
      key = e.key.toLowerCase();
    }

    if (mods.length === 0 || !key) return;

    const hotkey = mods.join('+') + '+' + key;
    try {
      const res = await api('set_hotkey', { hotkey });
      if (res && res.ok) {
        input.value = res.hotkey || hotkey;
        input.style.color = '';
        hotkeyCapturing = false;
        btnCapture.textContent = t('hotkey.reset');
        updateHotkeyHint(hotkey);
        showToast(t('hotkey.set_ok', input.value));
      } else {
        showToast(res && res.error ? res.error : t('hotkey.set_fail'));
      }
    } catch (err) {
      showToast(t('hotkey.capture_fail', err.message || err));
    }
  });
}

function updateHotkeyHint(hotkey) {
  const hint = $('#hotkeyHint');
  if (!hint) return;
  const display = hotkey.replace(/\+/g, '+').replace(/\b\w/g, c => c.toUpperCase());
  hint.innerHTML = t('hotkey.sidebar_hint', display);
}

// ===== 关闭行为设置 =====
async function initCloseBehavior() {
  const labels = {prompt: t('close_dialog.label.prompt'), minimize: t('close_dialog.label.minimize'), tray: t('close_dialog.label.tray'), quit: t('close_dialog.label.quit')};
  const res = await api('get_close_action');
  const action = res.action || 'prompt';
  const label = $('#closeActionLabel');
  if (label) label.textContent = labels[action] || t('close_dialog.label.prompt');
  highlightCloseOpt(action);
  $('#closeOptPrompt').onclick = () => { api('set_close_action', {action: 'prompt'}); if (label) label.textContent = t('close_dialog.label.prompt'); highlightCloseOpt('prompt'); };
  $('#closeOptMinimize').onclick = () => { api('set_close_action', {action: 'minimize'}); if (label) label.textContent = t('close_dialog.label.minimize'); highlightCloseOpt('minimize'); };
  $('#closeOptTray').onclick = () => { api('set_close_action', {action: 'tray'}); if (label) label.textContent = t('close_dialog.label.tray'); highlightCloseOpt('tray'); };
  $('#closeOptQuit').onclick = () => { api('set_close_action', {action: 'quit'}); if (label) label.textContent = '彻底关闭'; highlightCloseOpt('quit'); };
}

function highlightCloseOpt(action) {
  const map = {prompt: '#closeOptPrompt', minimize: '#closeOptMinimize', tray: '#closeOptTray', quit: '#closeOptQuit'};
  Object.values(map).forEach(sel => { const el = $(sel); if (el) el.classList.remove('tool-card-primary'); });
  const active = $(map[action] || map.prompt);
  if (active) active.classList.add('tool-card-primary');
}

// ===== 轮询实时数据 =====
let _lastLogLen = 0;
async function pollRealtimeData() {
  try {
    const [logRes, statusRes, trafficRes, auditRes] = await Promise.all([
      api('get_log'),
      api('get_status'),
      api('get_traffic'),
      api('get_audit'),
    ]);

    if (logRes.log && logRes.log.length > _lastLogLen) {
      const newLog = logRes.log.substring(_lastLogLen);
      _lastLogLen = logRes.log.length;
      const el = $('#logArea');
      el.appendChild(document.createTextNode(newLog));
      el.scrollTop = el.scrollHeight;
    }

    if (statusRes.text) {
      $('#statusLabel').textContent = statusRes.text;
      $('#statusLabel').style.color = statusRes.color || 'var(--success)';
    }

    if (trafficRes.dl_s) {
      $('#dlSpeed').textContent = trafficRes.dl_s;
      $('#upSpeed').textContent = trafficRes.up_s;
      $('#dlTotal').textContent = t('traffic.total', trafficRes.dl_t);
      $('#upTotal').textContent = t('traffic.total', trafficRes.up_t);
    }

    if (auditRes.data && auditRes.data.length > 0) {
      const body = $('#auditTableBody');
      body.innerHTML = '';
      auditRes.data.forEach(row => {
        const tr = document.createElement('tr');
        tr.innerHTML = row.map(c => `<td>${c}</td>`).join('');
        body.appendChild(tr);
      });
    }
  } catch (e) {}
}

// ===== 事件绑定 =====
document.addEventListener('DOMContentLoaded', () => {

  // 标题栏窗口控制
  $('#btnMinimize').onclick = () => api('minimise_window');
  $('#btnCloseWin').onclick = handleCloseWindow;

  // 导航
  $$('.nav-btn[data-page]').forEach(btn => {
    btn.onclick = () => switchPage(btn.dataset.page);
  });
  $$('.sub-nav-btn').forEach(btn => {
    btn.onclick = () => switchSubPage(btn.dataset.subpage);
  });

  // 主页
  $('#btnApply').onclick = applyRoles;
  $('#guardSwitch').onchange = toggleGuard;
  $('#btnClearLog').onclick = clearLog;
  $('#btnOpenManager').onclick = openRuleManager;

  // 流量
  $('#adapterSelect').onchange = () => api('select_traffic_adapter', {name: $('#adapterSelect').value});

  // 分析仪
  $('#logSwitch').onchange = toggleLogging;

  // 诊断
  $('#btnPing').onclick = runPing;
  $('#btnTracert').onclick = runTracert;
  $('#btnFlushDns').onclick = flushDns;
  $('#btnNslookup').onclick = runNslookup;
  $('#btnNetstat').onclick = runNetstat;
  $('#btnRoutePrint').onclick = runRoutePrint;
  $('#btnArpTable').onclick = runArpTable;
  $('#btnIpconfig').onclick = runIpconfigAll;
  $('#btnPortTest').onclick = runPortTest;
  $('#btnSpeedTest').onclick = runSpeedTest;

  // 设置: 网卡管理
  $('#btnRefreshAdapters').onclick = () => refreshAdapterManager();
  $('#btnRecommendRoles').onclick = recommendRoles;
  $('#btnApplyFromSettings').onclick = applyRoles;

  // 设置: 主题
  $$('.theme-card').forEach(card => {
    card.onclick = () => {
      const theme = card.dataset.theme;
      $$('.theme-card input[type="radio"]').forEach(r => r.checked = false);
      card.querySelector('input[type="radio"]').checked = true;
      setTheme(theme);
    };
  });

  // 设置: 语言
  $$('.lang-card').forEach(card => {
    card.onclick = () => {
      const lang = card.dataset.lang;
      setLanguage(lang);
    };
  });

  // 设置: 快捷键
  initHotkeySetting();

  // 捐赠二维码点击放大
  $$('.donate-qr').forEach(img => {
    img.style.cursor = 'zoom-in';
    img.onclick = () => {
      const overlay = $('#imgPreviewOverlay');
      const large = $('#imgPreviewLarge');
      const label = $('#imgPreviewLabel');
      large.src = img.src;
      label.textContent = img.nextElementSibling ? img.nextElementSibling.textContent : '';
      overlay.classList.add('active');
    };
  });
  $('#imgPreviewOverlay').onclick = () => {
    $('#imgPreviewOverlay').classList.remove('active');
  };

  // 检查更新
  $('#btnCheckUpdate').onclick = () => {
    $('#updateStatus').textContent = t('update.checking');
    $('#updateProgress').style.display = 'none';
    api('check_update');
  };
  window.runtime.EventsOn('update:result', (data) => {
    if (data.available) {
      $('#updateStatus').innerHTML = t('update.found', data.latest) + ' <button class="btn btn-sm btn-accent" id="btnDoUpdate" style="margin-left:8px">' + t('update.download_btn') + '</button>';
      $('#btnDoUpdate').onclick = () => {
        $('#updateProgress').style.display = 'block';
        $('#updateStatus').textContent = t('update.downloading');
        api('perform_update', {url: data.downloadUrl});
      };
    } else if (data.error) {
      $('#updateStatus').textContent = t('update.error') + ': ' + data.error;
    } else {
      $('#updateStatus').textContent = t('update.latest');
    }
  });
  window.runtime.EventsOn('update:progress', (data) => {
    if (data.stage === 'downloading') {
      $('#updateStatus').textContent = t('update.downloading') + ' ' + data.percent + '%';
      $('#updateProgressBar').style.width = data.percent + '%';
    } else if (data.stage === 'installing') {
      $('#updateStatus').textContent = t('update.installing');
      $('#updateProgressBar').style.width = '100%';
    }
  });
  window.runtime.EventsOn('update:error', (err) => {
    $('#updateStatus').textContent = t('update.error') + ': ' + err;
    $('#updateProgress').style.display = 'none';
  });
  window.runtime.EventsOn('update:done', () => {
    $('#updateStatus').textContent = t('update.done');
  });


  // 设置: 网卡监控
  $('#monitorSwitch').onchange = () => api('toggle_adapter_monitor', {enabled: $('#monitorSwitch').checked});

  // 规则管理器
  $('#btnCloseModal').onclick = closeRuleManager;
  $('#btnAddRule').onclick = addRule;
  $('#btnApplyAllRules').onclick = applyAllRules;
  $('#btnDownloadTemplate').onclick = downloadTemplate;
  $('#btnExportRules').onclick = exportRules;
  $('#btnImportRules').onclick = importRules;


  // 安全重置
  $('#btnEmergencyReset').onclick = emergencyReset;

  // 新建/编辑策略弹窗
  $('#btnNewStrategy').onclick = () => openStrategyModal(null);
  $('#btnRefreshStrategies').onclick = loadStrategyList;
  $('#btnApplyStrategies').onclick = applyStrategies;
  $$('.strategy-tab').forEach(tab => {
    tab.onclick = () => switchStrategyType(tab.dataset.stype);
  });
  $('#btnCloseStrategyModal').onclick = closeStrategyModal;
  $('#btnCancelStrategy').onclick = closeStrategyModal;
  $('#btnCreateStrategy').onclick = createStrategy;
  $('#btnSyncNow').onclick = syncNow;
  $('#btnImportTxt').onclick = importTxt;
  $('#strategyAddresses').oninput = updateAddrCount;
  $('#strategyModal').onclick = (e) => {
    if (e.target === $('#strategyModal')) closeStrategyModal();
  };

  // 初始加载

  loadAdapterMonitor();
  loadStrategyList();
  api('get_theme').then(res => {
    if (res.theme && res.theme !== 'dawn') setTheme(res.theme);
  });
  api('get_language').then(res => {
    if (res && res.language && res.language !== 'zh') {
      setLanguage(res.language);
    } else if (res && res.language === 'zh') {
      $$('.lang-card').forEach(card => {
        const radio = card.querySelector('input[type="radio"]');
        const active = card.dataset.lang === 'zh';
        card.classList.toggle('active', active);
        if (radio) radio.checked = active;
      });
    }
  });

  // 网卡断开事件监听
  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn('adapter_disconnected', (adapterName) => {
      showToast(t('common.adapter_disconnected', adapterName));
    });
    window.runtime.EventsOn('adapter_connected', (adapterName) => {
      showToast(t('common.adapter_connected', adapterName));
    });
    // 策略应用进度事件
    window.runtime.EventsOn('apply:progress', (data) => {
      const bar = $('#applyProgressBar');
      const msg = $('#applyProgressMsg');
      const count = $('#applyProgressCount');
      if (bar && data.total > 0) {
        bar.style.width = Math.round(data.current / data.total * 100) + '%';
      } else if (bar) {
        bar.style.width = '0%';
      }
      if (msg) msg.textContent = data.message || '';
      if (count) count.textContent = `${data.current || 0} / ${data.total || 0}`;
    });
    window.runtime.EventsOn('apply:done', (data) => {
      hideApplyProgress();
      if (data && data.ok) {
        const msgs = (data.messages || []).join('\n');
        showToast(t('common.strategy_applied', data.applied || 0, data.failed || 0, msgs));
      } else {
        showToast(data && data.error ? data.error : t('common.apply_error'));
      }
      const btn1 = $('#btnApply');
      if (btn1) btn1.classList.remove('tool-card-loading');
      const btn2 = $('#btnApplyStrategies');
      if (btn2) { btn2.disabled = false; btn2.textContent = t('common.apply_strategy'); }
      if (typeof loadStrategyList === 'function') loadStrategyList();
    });
    // 安全重置进度事件
    window.runtime.EventsOn('reset:progress', (data) => {
      const bar = $('#applyProgressBar');
      const msg = $('#applyProgressMsg');
      const count = $('#applyProgressCount');
      if (bar && data.total > 0) {
        bar.style.width = Math.round(data.current / data.total * 100) + '%';
      } else if (bar) {
        bar.style.width = '0%';
      }
      if (msg) msg.textContent = data.message || '';
      if (count) count.textContent = `${data.current || 0} / ${data.total || 0}`;
    });
    window.runtime.EventsOn('reset:done', () => {
      hideApplyProgress();
      const title = $('#applyProgressTitle');
      if (title) title.textContent = t('reset.applying_title');
      showToast(t('reset.done'));
    });
    // 应用规则进度事件
    window.runtime.EventsOn('apply_rules:progress', (data) => {
      const bar = $('#applyProgressBar');
      const msg = $('#applyProgressMsg');
      const count = $('#applyProgressCount');
      if (bar && data.total > 0) {
        bar.style.width = Math.round(data.current / data.total * 100) + '%';
      } else if (bar) {
        bar.style.width = '0%';
      }
      if (msg) msg.textContent = data.message || '';
      if (count) count.textContent = `${data.current || 0} / ${data.total || 0}`;
    });
    window.runtime.EventsOn('apply_rules:done', (data) => {
      hideApplyProgress();
      const title = $('#applyProgressTitle');
      if (title) title.textContent = t('rule.applying_strategy');
      const btn = $('#btnApplyAllRules');
      if (btn) { btn.disabled = false; btn.textContent = t('rule.apply_strategy'); }
      if (data && data.ok) {
        showToast(t('rule.apply_done', data.applied || 0, data.failed || 0));
      } else {
        showToast(data && data.error ? data.error : t('rule.apply_error'));
      }
      refreshRuleList();
    });
  }

  // 启动轮询（每 1 秒）
  setInterval(pollRealtimeData, 1000);
});
// ==================== 策略路由：增删改查 ====================

let currentStrategyType = 'online';
let editingStrategyId = null;
let strategyCache = [];

const STRATEGY_TYPE_LABELS = {
  online: 'online',
  local: 'local',
  fallback: 'fallback',
};

function getStrategyTypeLabel(type) {
  return t('strategy.type_label.' + type);
}

async function loadStrategyList() {
  const container = $('#strategyList');
  if (!container) return;
  container.innerHTML = '<div style="padding:24px;text-align:center;color:var(--text-muted)">' + t('strategy.loading') + '</div>';
  try {
    const res = await api('get_strategies');
    strategyCache = res.strategies || [];
    renderStrategyList(strategyCache);
  } catch (e) {
    container.innerHTML = `<div style="padding:24px;text-align:center;color:var(--danger)">` + t('strategy.load_fail', e.message || e) + `</div>`;
  }
}

function renderStrategyList(strategies) {
  const container = $('#strategyList');
  if (!strategies.length) {
    container.innerHTML = '<div style="padding:24px;text-align:center;color:var(--text-muted)">' + t('strategy.empty') + '</div>';
    return;
  }
  container.innerHTML = strategies.map(s => {
    const isFallback = s.type === 'fallback';
    const typeLabel = getStrategyTypeLabel(s.type);
    const typeColor = isFallback ? 'var(--purple)' : (s.type === 'online' ? 'var(--accent)' : 'var(--success)');
    let sourceInfo = '';
    if (s.source) {
      if (s.source.mode === 'online') {
        sourceInfo = `<div class="strat-src-line">${s.source.url ? s.source.url.substring(0, 60) : t('strategy.no_url')}</div>`;
        sourceInfo += `<div class="strat-src-meta">` + t('strategy.update_every', s.source.update_interval || 24, s.source.update_unit === 'days' ? t('strategy.unit_days') : t('strategy.unit_hours')) + `</div>`;
      } else {
        const count = s.source.address_count || (s.source.addresses ? s.source.addresses.length : 0);
        sourceInfo = `<div class="strat-src-meta">` + t('strategy.addr_count_line', count) + `</div>`;
      }
    }
    const adapterInfo = s.adapter || t('strategy.no_bind');
    return `
      <div class="strat-card ${isFallback ? 'strat-fallback' : ''}" data-id="${s.id}">
        <div class="strat-card-header">
          <div class="strat-card-title-row">
            <span class="strat-type-badge" style="background:${typeColor}20;color:${typeColor}">${typeLabel}</span>
            <span class="strat-card-name">${s.name}</span>
            ${s.enabled ? '<span class="strat-enabled-dot"></span>' : ''}
          </div>
          <div class="strat-card-actions">
            <button class="btn btn-ghost btn-sm strat-edit-btn" data-id="${s.id}">${t('strategy.edit')}</button>
            ${isFallback ? '' : `<button class="btn btn-ghost btn-sm strat-del-btn" data-id="${s.id}" style="color:var(--danger)">${t('strategy.delete')}</button>`}
          </div>
        </div>
        <div class="strat-card-body">
          ${sourceInfo}
          <div class="strat-adapter-row">
            <span class="text-muted" style="font-size:11px">${t('strategy.bind_adapter')}</span>
            <span class="strat-adapter-name">${adapterInfo}</span>
          </div>
        </div>
      </div>
    `;
  }).join('');

  container.querySelectorAll('.strat-edit-btn').forEach(btn => {
    btn.onclick = () => editStrategy(parseInt(btn.dataset.id));
  });
  container.querySelectorAll('.strat-del-btn').forEach(btn => {
    btn.onclick = () => deleteStrategy(parseInt(btn.dataset.id));
  });
}

function editStrategy(id) {
  const s = strategyCache.find(x => x.id === id);
  if (!s) return;
  openStrategyModal(s);
}

async function deleteStrategy(id) {
  const s = strategyCache.find(x => x.id === id);
  if (!s) return;
  if (!confirm(t('strategy.confirm_delete', s.name))) return;
  try {
    const res = await api('delete_strategy', { id });
    if (res && res.ok) {
      loadStrategyList();
    } else {
      showToast(res && res.error ? res.error : t('rule.delete_fail', ''));
    }
  } catch (e) {
    showToast(t('rule.delete_fail', e.message || e));
  }
}

function showStrategyDesc(type) {
  const box = $('#strategyDescBox');
  const text = $('#strategyDescText');
  if (type) {
    text.textContent = t('strategy.desc.' + type);
    box.style.display = 'flex';
  } else {
    box.style.display = 'none';
  }
}

function openStrategyModal(strategy) {
  editingStrategyId = strategy ? strategy.id : null;
  $('#strategyModal').style.display = 'flex';
  const titleEl = $('#strategyModalTitle');
  if (titleEl) titleEl.textContent = strategy ? t('strategy.modal_title_edit') : t('strategy.modal_title_new');
  const createBtn = $('#btnCreateStrategy');
  if (createBtn) createBtn.textContent = strategy ? t('strategy.save') : t('strategy.create');

  const defaultURL = 'https://raw.githubusercontent.com/misakaio/chnroutes2/master/chnroutes.txt';

  if (strategy && strategy.type !== 'fallback') {
    $('#strategyName').value = strategy.name || '';
    $('#strategyTabs').style.display = '';
    const type = strategy.type || 'online';
    switchStrategyType(type);
    showStrategyDesc(type);
    if (strategy.source) {
      if (type === 'online') {
        $('#strategyURL').value = strategy.source.url || defaultURL;
        $('#strategyInterval').value = strategy.source.update_interval || 24;
        $('#strategyUnit').value = strategy.source.update_unit || 'hours';
        setSyncStatus(strategy.source.sync_status || 'idle', strategy.source.sync_status || t('strategy.sync_not'));
      } else {
        $('#strategyAddresses').value = (strategy.source.addresses || []).join('\n');
        updateAddrCount();
      }
    }
  } else if (strategy && strategy.type === 'fallback') {
    $('#strategyName').value = strategy.name || '';
    $('#strategyTabs').style.display = 'none';
    $('#formOnline').style.display = 'none';
    $('#formLocal').style.display = 'none';
    showStrategyDesc('fallback');
  } else {
    $('#strategyTabs').style.display = '';
    $('#strategyName').value = '';
    $('#strategyURL').value = defaultURL;
    $('#strategyInterval').value = '24';
    $('#strategyUnit').value = 'hours';
    $('#strategyAddresses').value = '';
    updateAddrCount();
    setSyncStatus('idle', t('strategy.sync_not'));
    switchStrategyType('online');
    showStrategyDesc(null);
  }
}

function closeStrategyModal() {
  $('#strategyModal').style.display = 'none';
  editingStrategyId = null;
}

function switchStrategyType(type) {
  currentStrategyType = type;
  $$('.strategy-tab').forEach(t => t.classList.toggle('active', t.dataset.stype === type));
  $('#formOnline').style.display = type === 'online' ? 'flex' : 'none';
  $('#formLocal').style.display = type === 'local' ? 'flex' : 'none';
  if (editingStrategyId === null) showStrategyDesc(type);
}

function setSyncStatus(status, text) {
  const dot = $('#syncDot');
  dot.className = 'strategy-sync-dot ' + status;
  $('#syncText').textContent = text;
}

async function syncNow() {
  const url = $('#strategyURL').value.trim();
  if (!url) { setSyncStatus('failed', '请先填写 URL'); return; }
  setSyncStatus('syncing', '正在同步...');
  try {
    const res = await api('sync_strategy_url', { url });
    if (res && res.ok) {
      const count = res.count || 0;
      if (res.cache_file) window._strategyCacheFile = res.cache_file;
      setSyncStatus('success', `同步成功 · ${count} 条地址（已缓存到本地）`);
      if (editingStrategyId !== null) {
        const strategy = {
          name: $('#strategyName').value.trim(),
          type: currentStrategyType,
          enabled: true,
          adapter: '',
          source: {
            mode: 'online',
            url,
            update_interval: parseInt($('#strategyInterval').value) || 24,
            update_unit: $('#strategyUnit').value,
            sync_status: 'success',
            cache_file: window._strategyCacheFile || '',
          },
        };
        await api('update_strategy', { strategy, id: editingStrategyId });
      }
    } else {
      setSyncStatus('failed', res && res.error ? res.error : '同步失败');
    }
  } catch (e) {
    setSyncStatus('failed', '同步失败: ' + (e.message || e));
  }
}

function importTxt() {
  const input = document.createElement('input');
  input.type = 'file';
  input.accept = '.txt';
  input.onchange = () => {
    const file = input.files[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      const existing = $('#strategyAddresses').value.trim();
      const imported = reader.result.trim();
      $('#strategyAddresses').value = existing ? existing + '\n' + imported : imported;
      updateAddrCount();
    };
    reader.readAsText(file);
  };
  input.click();
}

function updateAddrCount() {
  const text = $('#strategyAddresses').value.trim();
  const count = text ? text.split('\n').filter(l => l.trim()).length : 0;
  $('#addrCountHint').textContent = t('strategy.addr_count', count);
}

async function createStrategy() {
  const name = $('#strategyName').value.trim();
  if (!name) { showToast(t('strategy.name') + '?'); return; }

  const isFallbackEdit = editingStrategyId === 0;
  const strategy = {
    name,
    type: isFallbackEdit ? 'fallback' : currentStrategyType,
    enabled: true,
    adapter: '',
  };

  if (!isFallbackEdit) {
    if (currentStrategyType === 'online') {
    const url = $('#strategyURL').value.trim();
    if (!url) { showToast(t('strategy.url_label') + '?'); return; }
    strategy.source = {
      mode: 'online',
      url,
      update_interval: parseInt($('#strategyInterval').value) || 24,
      update_unit: $('#strategyUnit').value,
      sync_status: 'idle',
      cache_file: window._strategyCacheFile || '',
    };
  } else {
    const text = $('#strategyAddresses').value.trim();
    if (!text) { showToast(t('strategy.addr_list')); return; }
    const addresses = text.split('\n').map(l => l.trim()).filter(l => l);
    strategy.source = {
      mode: 'local',
      addresses,
      address_count: addresses.length,
    };
    }
  }

  try {
    const isEdit = editingStrategyId !== null;
    const apiMethod = isEdit ? 'update_strategy' : 'create_strategy';
    const params = { strategy };
    if (isEdit) params.id = editingStrategyId;
    const res = await api(apiMethod, params);
    if (res && res.ok) {
      closeStrategyModal();
      loadStrategyList();
    } else {
      showToast(res && res.error ? res.error : (isEdit ? t('strategy.save') : t('strategy.create')));
    }
  } catch (e) {
    showToast(t('common.apply_fail', e.message || e));
  }
}
async function applyStrategies() {
  const btn = $('#btnApplyStrategies');
  if (btn) { btn.disabled = true; btn.textContent = t('common.applying'); }
  showApplyProgress();
  try {
    await api('apply_strategies');
  } catch (e) {
    hideApplyProgress();
    showToast(t('common.apply_fail', e.message || e));
    if (btn) { btn.disabled = false; btn.textContent = t('common.apply_strategy'); }
  }
}
