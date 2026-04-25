"use strict";

// ---------------------------------------------------------------------------
// Debug page
// ---------------------------------------------------------------------------

function setDebugStatus(message, type) {
  if (!debugStatusEl) return;
  debugStatusEl.textContent = message;
  debugStatusEl.className = 'debug-status' + (type ? ' ' + type : '');
}

function renderAgentStatus(s) {
  if (!agentStatusDotEl || !agentStatusLabelEl) return;
  agentStatusDotEl.className = 'agent-status-indicator ' + (s && s.connected ? 'online' : 'offline');
  agentStatusLabelEl.textContent = s && s.connected ? translate('common.online') : translate('debug.offlineDisconnected');
  if (agentStatusDetailEl) {
    var parts = [];
    if (s && s.agentId) parts.push(translate('field.id') + ': ' + s.agentId);
    if (s && s.server) parts.push(translate('debug.serverLabel', { server: s.server }));
    if (s && s.lastEvent) parts.push(s.lastEvent);
    agentStatusDetailEl.textContent = parts.join('  |  ');
  }
}

function refreshAgentStatus() {
  if (document.hidden) return;
  try {
    appApi().GetAgentStatus().then(function (s) {
      renderAgentStatus(s);
    }).catch(function () {
      renderAgentStatus(null);
    });
  } catch (e) {
    renderAgentStatus(null);
  }
}

function startAgentStatusPoll() {
  refreshAgentStatus();
  if (!agentStatusPollId) {
    agentStatusPollId = setInterval(refreshAgentStatus, 5000);
  }
}

function stopAgentStatusPoll() {
  if (agentStatusPollId) {
    clearInterval(agentStatusPollId);
    agentStatusPollId = null;
  }
}

// ========== Watchdog Health Monitor ==========

function refreshWatchdogHealth() {
  if (document.hidden) return;
  if (!watchdogHealthContainer) return;

  try {
    appApi().GetWatchdogHealth().then(function (checks) {
      renderWatchdogHealth(checks);
    }).catch(function (err) {
      watchdogHealthContainer.innerHTML = '<div class="watchdog-loading">' + escapeHtml(translate('debug.statusLoadError', { error: String(err) })) + '</div>';
    });
  } catch (e) {
    watchdogHealthContainer.innerHTML = '<div class="watchdog-loading">' + escapeHtml(translate('debug.watchdogUnavailable')) + '</div>';
  }
}

function renderWatchdogHealth(checks) {
  if (!watchdogHealthContainer) return;

  if (!checks || checks.length === 0) {
    watchdogHealthContainer.innerHTML = '<div class="watchdog-loading">' + escapeHtml(translate('debug.noMonitoredComponents')) + '</div>';
    return;
  }

  var html = '';
  for (var i = 0; i < checks.length; i++) {
    var check = checks[i];
    var statusClass = (check.status || 'unknown').toLowerCase();
    var componentName = formatComponentName(check.component);
    var badgeClass = check.recoverable ? 'recoverable' : 'not-recoverable';
    var badgeText = check.recoverable ? translate('debug.autoRecoverable') : translate('debug.manualRecovery');
    
    html += '<div class="watchdog-component-card ' + statusClass + '">';
    html += '  <div class="watchdog-status-dot ' + statusClass + '"></div>';
    html += '  <div class="watchdog-component-info">';
    html += '    <div class="watchdog-component-name">' + componentName + '</div>';
    html += '    <div class="watchdog-component-message">' + escapeHtml(check.message || translate('debug.noInformation')) + '</div>';
    html += '  </div>';
    html += '  <div class="watchdog-component-badge ' + badgeClass + '">' + badgeText + '</div>';
    html += '</div>';
  }

  watchdogHealthContainer.innerHTML = html;
}

function formatComponentName(component) {
  var names = {
    'tray': 'System Tray',
    'ai_service': 'Servico de IA',
    'agent_connection': 'Conexao do Agente',
    'automation_service': 'Automacao',
    'inventory': 'Inventario',
    'ui_runtime': 'Runtime UI'
  };
  return names[component] || component;
}

function startWatchdogPoll() {
  stopWatchdogPoll();
  refreshWatchdogHealth();
  watchdogPollId = setInterval(refreshWatchdogHealth, 15000); // Update every 15s
}

function stopWatchdogPoll() {
  if (watchdogPollId) {
    clearInterval(watchdogPollId);
    watchdogPollId = null;
  }
}

function loadDebugConfig() {
  try {
    appApi().GetDebugConfig().then(function (cfg) {
      if (apiSchemeEl) apiSchemeEl.value = cfg.apiScheme || 'https';
      if (apiServerEl) apiServerEl.value = cfg.apiServer || '';
      if (natsServerEl) natsServerEl.value = cfg.natsServer || '';
      if (debugAuthTokenEl) debugAuthTokenEl.value = cfg.authToken || '';
      if (debugAgentIDEl) debugAgentIDEl.value = cfg.agentId || '';
      if (automationP2PWingetInstallEnabledEl) automationP2PWingetInstallEnabledEl.value = String(!!cfg.automationP2pWingetInstallEnabled);
      updateDebugResponseLabel();
      if (typeof syncProvisioningOverlayFromConfig === 'function') {
        syncProvisioningOverlayFromConfig(cfg);
      }
    }).catch(function () {});
  } catch (e) {}
  startAgentStatusPoll();
  startWatchdogPoll();
}

function updateDebugResponseLabel() {
  if (!debugResponseLabelEl) return;
  debugResponseLabelEl.innerHTML = translate('debug.testResponse');
}

function initDebug() {
  var openP2PDebugWindowBtn = document.getElementById('openP2PDebugWindowBtn');
  var openPSADTDebugWindowBtn = document.getElementById('openPSADTDebugWindowBtn');
  if (openP2PDebugWindowBtn) {
    openP2PDebugWindowBtn.addEventListener('click', function () {
      setActiveTab('p2p');
      if (typeof loadP2PView === 'function') {
        loadP2PView();
      }
    });
  }

  if (openPSADTDebugWindowBtn) {
    openPSADTDebugWindowBtn.addEventListener('click', function () {
      setActiveTab('psadt');
    });
  }

  if (agentStatusRefreshBtn) {
    agentStatusRefreshBtn.addEventListener('click', refreshAgentStatus);
  }

  if (watchdogRefreshBtn) {
    watchdogRefreshBtn.addEventListener('click', refreshWatchdogHealth);
  }

  if (debugSaveBtn) {
    debugSaveBtn.addEventListener('click', function () {
      setDebugStatus(translate('debug.saving'), '');
      appApi().SetDebugConfig({
        apiScheme: apiSchemeEl ? apiSchemeEl.value : 'https',
        apiServer: apiServerEl ? apiServerEl.value.trim() : '',
        natsServer: natsServerEl ? natsServerEl.value.trim() : '',
        authToken: debugAuthTokenEl ? debugAuthTokenEl.value : '',
        agentId: debugAgentIDEl ? debugAgentIDEl.value.trim() : '',
        automationP2pWingetInstallEnabled: automationP2PWingetInstallEnabledEl ? automationP2PWingetInstallEnabledEl.value === 'true' : false,
      }).then(function () {
        workflowStatesCache = null;
        workflowStatesCacheKey = '';
        setDebugStatus(translate('debug.savedSuccess'), 'success');
        if (typeof syncProvisioningOverlayFromRuntime === 'function') {
          syncProvisioningOverlayFromRuntime();
        }
        setTimeout(refreshAgentStatus, 1500);
      }).catch(function (err) {
        setDebugStatus(translate('debug.saveError', { error: (err.message || String(err)) }), 'error');
      });
    });
  }

  if (debugTestBtn) {
    debugTestBtn.addEventListener('click', function () {
      setDebugStatus(translate('debug.testingConnection'), '');
      updateDebugResponseLabel();
      if (debugResponseWrapEl) debugResponseWrapEl.classList.add('hidden');
      if (debugResponseEl) debugResponseEl.textContent = '';
      appApi().TestDebugConnection({
        apiScheme: apiSchemeEl ? apiSchemeEl.value : 'https',
        apiServer: apiServerEl ? apiServerEl.value.trim() : '',
        natsServer: natsServerEl ? natsServerEl.value.trim() : '',
        authToken: debugAuthTokenEl ? debugAuthTokenEl.value : '',
        agentId: debugAgentIDEl ? debugAgentIDEl.value.trim() : '',
      }).then(function (body) {
        setDebugStatus(translate('debug.connectedSuccess'), 'success');
        if (debugResponseEl) debugResponseEl.textContent = body;
        if (debugResponseWrapEl) debugResponseWrapEl.classList.remove('hidden');
      }).catch(function (err) {
        setDebugStatus(translate('debug.connectionFailure', { error: (err.message || String(err)) }), 'error');
        if (debugResponseWrapEl) debugResponseWrapEl.classList.add('hidden');
      });
    });
  }
}
