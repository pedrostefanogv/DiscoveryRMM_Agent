function setExportStatus(message, isError) {
  if (!exportStatusEl) return;
  exportStatusEl.textContent = message;
  exportStatusEl.classList.remove('success', 'error');
  exportStatusEl.classList.add(isError ? 'error' : 'success');
}

// ---------------------------------------------------------------------------
// Generic card-list renderer - replaces 13 nearly-identical render functions.
// targetEl:     DOM element to render into
// items:        array of data objects
// emptyMessage: string shown when items is empty/null
// cardRenderer: function(item) => HTML string for one card
// opts:         optional { maxItems: number } to cap rendering
// ---------------------------------------------------------------------------
function renderCardList(targetEl, items, emptyMessage, cardRenderer, opts) {
  if (!items || !items.length) {
    targetEl.innerHTML = '<div class="meta">' + escapeHtml(emptyMessage) + '</div>';
    return;
  }
  var list = items;
  if (opts && opts.maxItems && list.length > opts.maxItems) {
    list = list.slice(0, opts.maxItems);
  }
  targetEl.innerHTML = list.map(cardRenderer).join('');
}

function setActiveTab(tab) {
  if (!isRuntimeTabAllowed(tab)) {
    tab = 'store';
  }

  activeTab = tab;
  var views = {
    status: statusViewEl,
    store: storeViewEl,
    updates: updatesViewEl,
    inventory: inventoryViewEl,
    logs: logsViewEl,
    chat: chatViewEl,
    support: supportViewEl,
    knowledge: knowledgeViewEl,
    automation: automationViewEl,
    debug: debugViewEl,
    psadt: psadtViewEl,
    p2p: p2pViewEl,
  };
  var tabs = {
    status: tabStatusBtn,
    store: tabStoreBtn,
    updates: tabUpdatesBtn,
    inventory: tabInventoryBtn,
    logs: tabLogsBtn,
    chat: tabChatBtn,
    support: tabSupportBtn,
    knowledge: tabKnowledgeBtn,
    automation: tabAutomationBtn,
    debug: tabDebugBtn,
    psadt: tabPSADTBtn,
    p2p: tabP2PBtn,
  };

  var titles = {
    status: translate('tab.status'),
    store: translate('tab.store'),
    updates: translate('tab.updates'),
    inventory: translate('tab.inventory'),
    logs: translate('tab.logs'),
    chat: translate('tab.chat'),
    support: translate('tab.support'),
    knowledge: translate('tab.knowledge'),
    automation: translate('tab.automation'),
    p2p: translate('tab.p2p'),
    debug: translate('tab.debug'),
  };

  Object.keys(views).forEach(function (key) {
    if (views[key]) views[key].classList.toggle('hidden', key !== tab);
    if (tabs[key]) {
      tabs[key].classList.toggle('active', key === tab);
      tabs[key].setAttribute('aria-selected', String(key === tab));
    }
  });

  if (pageTitleEl) pageTitleEl.textContent = titles[tab] || translate('app.name');

  if (tab === 'chat') {
    scheduleChatScrollToBottom();
  }

  // Stop logs auto-refresh when leaving logs tab
  if (tab !== 'logs' && logsAutoRefreshId) {
    clearInterval(logsAutoRefreshId);
    logsAutoRefreshId = null;
  }

  if (tab !== 'status' && typeof stopStatusPoll === 'function') {
    stopStatusPoll();
  }

  // Parar polling P2P ao sair da aba P2P
  if (tab !== 'p2p' && typeof stopP2PPoller === 'function') {
    stopP2PPoller();
  }

  // Stop agent status poll when leaving debug tab
  if (tab !== 'debug') {
    stopAgentStatusPoll();
    stopWatchdogPoll();
  }

  if (tab === 'status' && typeof startStatusPoll === 'function') {
    if (isAppWindowVisible()) startStatusPoll();
  }

  // Start logs auto-refresh when entering logs tab
  if (tab === 'logs') {
    loadLogs();
    if (isAppWindowVisible() && !logsAutoRefreshId) {
      logsAutoRefreshId = setInterval(loadLogs, 3000);
    }
  }
}

var uiRuntimeHeartbeatId = null;
var uiRuntimeRecoveryReloadAt = 0;
const UI_RUNTIME_HEARTBEAT_MS = 15000;
const UI_RUNTIME_RECOVERY_RELOAD_DEDUPE_MS = 5 * 60 * 1000;

function canReportUIRuntime() {
  try {
    var api = appApi();
    return !!(api && typeof api.ReportUIRuntimeState === 'function' && typeof api.SetUIRuntimeSuspended === 'function');
  } catch (_) {
    return false;
  }
}

function reportUIRuntimeState(source) {
  if (!canReportUIRuntime()) return;

  var visible = isAppWindowVisible() && !window.__discoveryUISuspended;
  var focused = typeof document.hasFocus === 'function' ? document.hasFocus() : visible;
  var api = appApi();

  if (!visible) {
    api.SetUIRuntimeSuspended(true, 'janela oculta: ' + String(source || 'frontend')).catch(function () {});
    return;
  }

  api.ReportUIRuntimeState(true, focused, String(source || 'frontend')).catch(function () {});
}

function startUIRuntimeMonitor(source) {
  stopUIRuntimeMonitor(false, source);
  reportUIRuntimeState(source || 'bootstrap');

  if (!isAppWindowVisible()) {
    return;
  }

  uiRuntimeHeartbeatId = setInterval(function () {
    if (document.hidden || window.__discoveryUISuspended) return;
    reportUIRuntimeState('interval');
  }, UI_RUNTIME_HEARTBEAT_MS);
}

function stopUIRuntimeMonitor(announceSuspend, source) {
  if (uiRuntimeHeartbeatId) {
    clearInterval(uiRuntimeHeartbeatId);
    uiRuntimeHeartbeatId = null;
  }
  if (announceSuspend === false) {
    return;
  }
  reportUIRuntimeState(source || 'suspend');
}

function handleUIRuntimeRecoverEvent(data) {
  if (document.hidden || window.__discoveryUISuspended) {
    return;
  }

  var reloadRequested = !!(data && data.reloadRequested);
  if (reloadRequested) {
    var now = Date.now();
    if (now - uiRuntimeRecoveryReloadAt >= UI_RUNTIME_RECOVERY_RELOAD_DEDUPE_MS) {
      uiRuntimeRecoveryReloadAt = now;
      window.location.reload();
      return;
    }
  }

  reportUIRuntimeState('recovery-event');
}

function handleWindowVisibilityChange() {
  if (!isAppWindowVisible()) {
    setUISuspended(true);
    stopUIRuntimeMonitor(true, 'visibilitychange:hidden');
    if (logsAutoRefreshId) {
      clearInterval(logsAutoRefreshId);
      logsAutoRefreshId = null;
    }
    if (typeof stopStatusPoll === 'function') stopStatusPoll();
    if (typeof stopAgentStatusPoll === 'function') stopAgentStatusPoll();
    if (typeof stopWatchdogPoll === 'function') stopWatchdogPoll();
    return;
  }

  setUISuspended(false);
  startUIRuntimeMonitor('visibilitychange:visible');

  if (activeTab === 'status' && typeof startStatusPoll === 'function') {
    startStatusPoll();
  }
  if (activeTab === 'debug') {
    if (typeof startAgentStatusPoll === 'function') startAgentStatusPoll();
    if (typeof startWatchdogPoll === 'function') startWatchdogPoll();
  }
  if (activeTab === 'logs' && !logsAutoRefreshId) {
    logsAutoRefreshId = setInterval(loadLogs, 3000);
  }
}

document.addEventListener('visibilitychange', handleWindowVisibilityChange);
document.addEventListener('ui:suspend', function () {
  stopUIRuntimeMonitor(true, 'ui:suspend');
});
document.addEventListener('ui:resume', function () {
  startUIRuntimeMonitor('ui:resume');
});
window.addEventListener('focus', function () {
  reportUIRuntimeState('window:focus');
});
window.addEventListener('blur', function () {
  reportUIRuntimeState('window:blur');
});
window.addEventListener('beforeunload', function () {
  stopUIRuntimeMonitor(true, 'window:unload');
});
setUISuspended(!isAppWindowVisible());

// ---------------------------------------------------------------------------
// Logs tab
// ---------------------------------------------------------------------------

async function loadLogs() {
  if (window.__discoveryUISuspended || document.hidden) return;
  try {
    var lines = await appApi().GetLogs();
    logsLastLines = lines || [];
    renderLogsOutput();
  } catch (_) {
    // silent - auto-refresh shouldn't spam errors
  }
}

function normalizeLogSource(rawSource) {
  var source = String(rawSource || '').toLowerCase().trim();
  if (!source) return 'other';

  if (source === 'agent-sync') return 'sync';
  if (source.indexOf('sync') === 0) return 'sync';
  if (source.indexOf('winget') === 0 || source.indexOf('install') === 0 || source.indexOf('upgrade') === 0 || source.indexOf('list') === 0) return 'updates';
  if (source.indexOf('inventory') === 0 || source.indexOf('efficiency') === 0) return 'inventory';
  if (source.indexOf('printer') === 0) return 'printer';
  if (source.indexOf('debug') === 0 || source.indexOf('config') === 0 || source.indexOf('installer-bootstrap') === 0) return 'debug';
  if (source.indexOf('startup') === 0 || source.indexOf('shutdown') === 0 || source.indexOf('tray') === 0) return 'startup';
  if (source.indexOf('watchdog') === 0 || source.indexOf('stream-monitor') === 0 || source.indexOf('operation-monitor') === 0) return 'watchdog';
  if (source.indexOf('agent') === 0) return 'agent';
  if (source.indexOf('automation') === 0) return 'automation';
  if (source.indexOf('support') === 0) return 'support';
  if (source.indexOf('chat') === 0) return 'chat';

  return 'other';
}

function detectLogOrigin(line) {
  var text = String(line || '');
  var match = text.match(/^\[([^\]]+)\]/);
  if (!match || !match[1]) {
    return 'other';
  }

  var token = match[1].trim().split(/\s+/)[0] || '';
  return normalizeLogSource(token);
}

function renderLogsOutput() {
  var selectedOrigin = logsOriginFilterEl ? String(logsOriginFilterEl.value || 'all') : 'all';
  var searchTerm = logsSearchInputEl ? String(logsSearchInputEl.value || '').toLowerCase().trim() : '';
  var lines = logsLastLines || [];

  if (selectedOrigin !== 'all') {
    lines = lines.filter(function (line) {
      return detectLogOrigin(line) === selectedOrigin;
    });
  }

  if (searchTerm) {
    lines = lines.filter(function (line) {
      return line.toLowerCase().indexOf(searchTerm) !== -1;
    });
  }

  var visibleLines = lines;
  var total = (logsLastLines || []).length;

  // Atualizar contador
  if (logsLineCountEl) {
    if (selectedOrigin === 'all' && !searchTerm) {
      logsLineCountEl.textContent = '(' + total + ' linhas, max. 5000)';
    } else {
      logsLineCountEl.textContent = '(filtro: ' + lines.length + ' de ' + total + ' linhas)';
    }
  }

  // Renderizar com <span> por linha para scroll não quebrar seleção
  if (lines.length > 2000) {
    // Modo otimizado: textContent para volumes grandes
    logsOutputEl.textContent = lines.join('\n') || '(sem logs)';
  } else {
    // Modo DOM: cada linha em span com textContent (seguro contra XSS)
    logsOutputEl.innerHTML = '';
    for (var i = 0; i < lines.length; i++) {
      var span = document.createElement('span');
      span.textContent = lines[i];
      logsOutputEl.appendChild(span);
      if (i < lines.length - 1) logsOutputEl.appendChild(document.createTextNode('\n'));
    }
    if (lines.length === 0) {
      logsOutputEl.textContent = '(sem logs)';
    }
  }

  if (!logsOutputEl.dataset.pinned || logsOutputEl.dataset.pinned === 'true') {
    logsOutputEl.scrollTop = logsOutputEl.scrollHeight;
  }
}

async function copyLogs() {
  var lines = getVisibleLogLines();
  if (!lines || lines.length === 0) {
    showToast('Nenhum log para copiar', 'warning');
    return;
  }
  try {
    await navigator.clipboard.writeText(lines.join('\n'));
    showToast(lines.length + ' linhas copiadas', 'success');
  } catch (e) {
    showToast('Falha ao copiar: ' + String(e), 'error');
  }
}

function exportLogs() {
  var lines = getVisibleLogLines();
  if (!lines || lines.length === 0) {
    showToast('Nenhum log para exportar', 'warning');
    return;
  }
  var blob = new Blob([lines.join('\n')], { type: 'text/plain' });
  var url = URL.createObjectURL(blob);
  var a = document.createElement('a');
  a.href = url;
  a.download = 'discovery-logs-' + new Date().toISOString().slice(0, 19).replace(/[T:]/g, '-') + '.txt';
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
  showToast(lines.length + ' linhas exportadas', 'success');
}

function getVisibleLogLines() {
  if (!logsOutputEl) return [];
  // Se renderizado com DOM spans, pegar texto de cada span
  if (logsOutputEl.children && logsOutputEl.children.length > 0) {
    var lines = [];
    for (var i = 0; i < logsOutputEl.children.length; i++) {
      var child = logsOutputEl.children[i];
      if (child.tagName === 'SPAN') {
        var txt = child.textContent || '';
        lines.push(txt);
      }
    }
    return lines;
  }
  // Fallback: textContent
  var text = logsOutputEl.textContent || '';
  return text.split('\n').filter(function (l) { return l !== '(sem logs)'; });
}

async function clearLogs() {
  try {
    await appApi().ClearLogs();
    logsLastLines = [];
    renderLogsOutput();
    showToast(translate('logs.cleared'), 'info');
  } catch (error) {
    showToast(translate('logs.clearError', { error: String(error) }), 'error');
  }
}

// ---------------------------------------------------------------------------
// Theme toggle
// ---------------------------------------------------------------------------

function initTheme() {
  var saved = localStorage.getItem('discovery-theme');
  if (saved === 'dark') {
    document.documentElement.setAttribute('data-theme', 'dark');
    updateThemeIcon(true);
  }
}

function toggleTheme() {
  var isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  if (isDark) {
    document.documentElement.removeAttribute('data-theme');
    localStorage.setItem('discovery-theme', 'light');
  } else {
    document.documentElement.setAttribute('data-theme', 'dark');
    localStorage.setItem('discovery-theme', 'dark');
  }
  updateThemeIcon(!isDark);
}

function updateThemeIcon(isDark) {
  var themeIcon = document.getElementById('themeIcon');
  var label = themeToggleBtn ? themeToggleBtn.querySelector('span') : null;
  if (themeIcon) {
    if (isDark) {
      themeIcon.innerHTML = '<circle cx="12" cy="12" r="5"/><line x1="12" y1="1" x2="12" y2="3"/><line x1="12" y1="21" x2="12" y2="23"/><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"/><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"/><line x1="1" y1="12" x2="3" y2="12"/><line x1="21" y1="12" x2="23" y2="12"/><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"/><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"/>';
    } else {
      themeIcon.innerHTML = '<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/>';
    }
  }
  if (label) label.textContent = isDark ? translate('theme.light') : translate('theme.dark');
}

function syncWindowChromeSidebarWidth() {
  if (!sidebarEl) return;
  var mobile = window.matchMedia && window.matchMedia('(max-width: 960px)').matches;
  var widthPx = 220;

  if (mobile && sidebarEl.classList.contains('collapsed')) {
    widthPx = 0;
  } else if (sidebarEl.classList.contains('collapsed')) {
    widthPx = 68;
  }

  document.documentElement.style.setProperty('--sidebar-current-width', widthPx + 'px');
}

window.addEventListener('resize', syncWindowChromeSidebarWidth);
