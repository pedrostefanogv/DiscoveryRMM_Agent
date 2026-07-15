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

function loadDebugConfig() {
  try {
    appApi().GetDebugConfig().then(function (cfg) {
      if (apiSchemeEl) apiSchemeEl.value = cfg.apiScheme || 'https';
      if (apiServerEl) apiServerEl.value = cfg.apiServer || '';
      if (natsServerEl) natsServerEl.value = cfg.natsServer || '';
      if (debugAuthTokenEl) debugAuthTokenEl.value = cfg.authToken || '';
      if (debugAgentIDEl) debugAgentIDEl.value = cfg.agentId || '';
      if (automationP2PWingetInstallEnabledEl) automationP2PWingetInstallEnabledEl.value = String(!!cfg.automationP2pWingetInstallEnabled);
      // Avancado
      if (natsWsServerEl) natsWsServerEl.value = cfg.natsWsServer || '';
      if (natsServerHostEl) natsServerHostEl.value = cfg.natsServerHost || '';
      if (natsUseWssExternalEl) natsUseWssExternalEl.value = String(!!cfg.natsUseWssExternal);
      if (allowInsecureTlsEl) allowInsecureTlsEl.checked = !!cfg.allowInsecureTls;
      if (enforceTlsHashValidationEl) enforceTlsHashValidationEl.checked = !!cfg.enforceTlsHashValidation;
      if (handshakeEnabledEl) handshakeEnabledEl.checked = cfg.handshakeEnabled !== false;
      if (apiTlsCertHashEl) apiTlsCertHashEl.value = cfg.apiTlsCertHash || '';
      if (natsTlsCertHashEl) natsTlsCertHashEl.value = cfg.natsTlsCertHash || '';
      updateDebugResponseLabel();
      if (typeof syncProvisioningOverlayFromConfig === 'function') {
        syncProvisioningOverlayFromConfig(cfg);
      }
    }).catch(function () {});
  } catch (e) {}
  startAgentStatusPoll();
}

function updateDebugResponseLabel() {
  if (!debugResponseLabelEl) return;
  debugResponseLabelEl.innerHTML = translate('debug.testResponse');
}

function updateDebugBindHint(bound) {
  if (debugBindAllInterfacesHintEl) {
    if (bound) {
      debugBindAllInterfacesHintEl.innerHTML = '<span style="color:#f4a259;">⚠ Interface acessivel na rede local (0.0.0.0). Desmarque para restringir ao localhost.</span>';
    } else {
      debugBindAllInterfacesHintEl.innerHTML = 'Permite acessar esta interface de outras maquinas na rede (0.0.0.0)';
    }
  }
  if (debugBindStatusEl) {
    debugBindStatusEl.textContent = bound ? '✓ Rede' : '✓ Local';
  }
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

  var sendTestHeartbeatBtn = document.getElementById('sendTestHeartbeatBtn');
  if (sendTestHeartbeatBtn) {
    sendTestHeartbeatBtn.addEventListener('click', function () {
      sendTestHeartbeatBtn.disabled = true;
      sendTestHeartbeatBtn.textContent = 'Enviando...';
      setDebugStatus('Enviando heartbeat manual...', 'info');
      appApi().SendTestHeartbeat().then(function (result) {
        setDebugStatus('Heartbeat: ' + result, result.indexOf('sucesso') >= 0 ? 'success' : 'error');
        sendTestHeartbeatBtn.disabled = false;
        sendTestHeartbeatBtn.textContent = 'Enviar Heartbeat';
      }).catch(function (err) {
        setDebugStatus('Heartbeat erro: ' + String(err), 'error');
        sendTestHeartbeatBtn.disabled = false;
        sendTestHeartbeatBtn.textContent = 'Enviar Heartbeat';
      });
    });
  }

  if (debugSaveBtn) {
    debugSaveBtn.addEventListener('click', function () {
      setDebugStatus(translate('debug.saving'), '');
      appApi().SetDebugConfig({
        apiScheme: apiSchemeEl ? apiSchemeEl.value : 'https',
        apiServer: apiServerEl ? apiServerEl.value.trim() : '',
        natsServer: natsServerEl ? natsServerEl.value.trim() : '',
        natsWsServer: natsWsServerEl ? natsWsServerEl.value.trim() : '',
        natsServerHost: natsServerHostEl ? natsServerHostEl.value.trim() : '',
        natsUseWssExternal: natsUseWssExternalEl ? natsUseWssExternalEl.value === 'true' : false,
        authToken: debugAuthTokenEl ? debugAuthTokenEl.value : '',
        agentId: debugAgentIDEl ? debugAgentIDEl.value.trim() : '',
        allowInsecureTls: allowInsecureTlsEl ? allowInsecureTlsEl.checked : false,
        enforceTlsHashValidation: enforceTlsHashValidationEl ? enforceTlsHashValidationEl.checked : false,
        handshakeEnabled: handshakeEnabledEl ? handshakeEnabledEl.checked : true,
        apiTlsCertHash: apiTlsCertHashEl ? apiTlsCertHashEl.value.trim() : '',
        natsTlsCertHash: natsTlsCertHashEl ? natsTlsCertHashEl.value.trim() : '',
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
        natsWsServer: natsWsServerEl ? natsWsServerEl.value.trim() : '',
        authToken: debugAuthTokenEl ? debugAuthTokenEl.value : '',
        agentId: debugAgentIDEl ? debugAgentIDEl.value.trim() : '',
        allowInsecureTls: allowInsecureTlsEl ? allowInsecureTlsEl.checked : false,
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

  // ── Network bind toggle ──────────────────────────────────────────────
  if (debugBindAllInterfacesEl) {
    // Poll current bind state on page load
    try {
      appApi().IsDebugHTTPBoundToAllInterfaces().then(function (bound) {
        debugBindAllInterfacesEl.checked = !!bound;
        updateDebugBindHint(!!bound);
      }).catch(function () {
        debugBindAllInterfacesEl.checked = false;
        updateDebugBindHint(false);
      });
    } catch (e) {
      debugBindAllInterfacesEl.checked = false;
      updateDebugBindHint(false);
    }

    debugBindAllInterfacesEl.addEventListener('change', function () {
      var enabled = debugBindAllInterfacesEl.checked;
      debugBindAllInterfacesEl.disabled = true;
      if (debugBindStatusEl) debugBindStatusEl.textContent = '...';
      try {
        appApi().SetDebugHTTPBindAllInterfaces(enabled).then(function () {
          updateDebugBindHint(enabled);
          if (debugBindStatusEl) debugBindStatusEl.textContent = enabled ? '✓ Rede' : '✓ Local';
          debugBindAllInterfacesEl.disabled = false;
        }).catch(function (err) {
          debugBindAllInterfacesEl.checked = !enabled; // revert
          setDebugStatus('Erro ao alterar bind: ' + (err.message || String(err)), 'error');
          updateDebugBindHint(!enabled);
          if (debugBindStatusEl) debugBindStatusEl.textContent = '';
          debugBindAllInterfacesEl.disabled = false;
        });
      } catch (e) {
        debugBindAllInterfacesEl.checked = !enabled; // revert
        setDebugStatus('Erro ao alterar bind: ' + String(e), 'error');
        if (debugBindStatusEl) debugBindStatusEl.textContent = '';
        debugBindAllInterfacesEl.disabled = false;
      }
    });
  }
}
