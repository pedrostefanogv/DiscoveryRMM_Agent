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
      if (automationP2PWingetInstallEnabledEl) automationP2PWingetInstallEnabledEl.value = String(cfg.automationP2pWingetInstallEnabled !== false);
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
  loadHardwareIdentity();
}

function updateDebugResponseLabel() {
  if (!debugResponseLabelEl) return;
  debugResponseLabelEl.innerHTML = translate('debug.testResponse');
}

// ── Identidade de Hardware (TPM EK + UUID SMBIOS) ────────────────────────
function renderHardwareIdentity(h) {
  if (!h) {
    if (hardwareTpmEkEl) hardwareTpmEkEl.textContent = '—';
    if (hardwareSmbiosUuidEl) hardwareSmbiosUuidEl.textContent = '—';
    return;
  }
  if (hardwareTpmEkEl) {
    hardwareTpmEkEl.textContent = h.tpmEkAvailable && h.tpmEk ? h.tpmEk : '—';
  }
  if (hardwareTpmEkStatusEl) {
    if (h.tpmEkAvailable) {
      hardwareTpmEkStatusEl.textContent = '✓ TPM ' + (h.tpmEkAlg || '');
      hardwareTpmEkStatusEl.className = 'debug-hardware-status ok';
    } else {
      hardwareTpmEkStatusEl.textContent = h.tpmEkError ? '✗ ' + h.tpmEkError : '✗ indisponível';
      hardwareTpmEkStatusEl.className = 'debug-hardware-status err';
    }
  }
  if (hardwareSmbiosUuidEl) {
    hardwareSmbiosUuidEl.textContent = h.smbiosUuidAvailable && h.smbiosUuid ? h.smbiosUuid : '—';
  }
  if (hardwareSmbiosUuidStatusEl) {
    if (h.smbiosUuidAvailable) {
      hardwareSmbiosUuidStatusEl.textContent = '✓ SMBIOS';
      hardwareSmbiosUuidStatusEl.className = 'debug-hardware-status ok';
    } else {
      hardwareSmbiosUuidStatusEl.textContent = h.smbiosUuidError ? '✗ ' + h.smbiosUuidError : '✗ indisponível';
      hardwareSmbiosUuidStatusEl.className = 'debug-hardware-status err';
    }
  }
}

function loadHardwareIdentity() {
  try {
    appApi().GetHardwareIdentity().then(function (h) {
      renderHardwareIdentity(h);
    }).catch(function () {
      renderHardwareIdentity(null);
    });
  } catch (e) {
    renderHardwareIdentity(null);
  }
}

function refreshHardwareIdentity() {
  try {
    appApi().RefreshHardwareIdentity().then(function (h) {
      renderHardwareIdentity(h);
    }).catch(function () {
      renderHardwareIdentity(null);
    });
  } catch (e) {
    renderHardwareIdentity(null);
  }
}

function updateDebugBindHint(bound) {
  if (debugBindAllInterfacesHintEl) {
    if (bound) {
      debugBindAllInterfacesHintEl.innerHTML = '<span style="color:#f4a259;">⚠ Interface acessivel na rede local (0.0.0.0). Desmarque para restringir ao localhost.</span>';
    } else {
      debugBindAllInterfacesHintEl.innerHTML = 'Permite que outras maquinas da rede acessem esta interface (0.0.0.0)';
    }
  }
  if (debugBindStatusEl) {
    debugBindStatusEl.textContent = bound ? '✓ Rede' : '✓ Local';
  }
}

function initDebug() {
  var openP2PDebugWindowBtn = document.getElementById('openP2PDebugWindowBtn');
  var openPSADTDebugWindowBtn = document.getElementById('openPSADTDebugWindowBtn');

  // Config de debug atualizada em outro processo (serviço via IPC — SetConfig,
  // segurança remota, bootstrap): recarrega o formulário para refletir a
  // config VIVA em vez da cópia stale do boot (evita "só resolve fechando e
  // abrindo o agent/ui").
  if (window.wails && typeof window.wails.on === 'function') {
    window.wails.on('debug:config_updated', function () {
      loadDebugConfig();
    });
  }

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

  if (hardwareRefreshBtn) {
    hardwareRefreshBtn.addEventListener('click', function () {
      hardwareRefreshBtn.disabled = true;
      refreshHardwareIdentity();
      setTimeout(function () { hardwareRefreshBtn.disabled = false; }, 800);
    });
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
        // A loja pode voltar a funcionar com a nova config: marca o catálogo
        // como dirty (o evento debug:config_updated também recarrega quando
        // chegar — este é o cinto de segurança caso o evento se perca).
        storeCatalogDirty = true;
        if (typeof setActiveTab === 'function' && activeTab === 'store' && !window.__discoveryUISuspended && !document.hidden) {
          loadCatalog();
        }
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

  initSensorDebug();
}

// ---------------------------------------------------------------------------
// Sensores (monitoramento) — Debug
// ---------------------------------------------------------------------------
// Seção "Sensores (monitoramento)" dentro da view de Debug. Mostra o estado do
// motor de sensores (fingerprint da policy, sync/backoff, erros) e a tabela de
// sensores com a próxima/última coleta, o cache do script e os canais.
// Contrato do backend:
//   GetSensorDebugSnapshot()      -> { agentId, generatedAt, debugMode, engine, sensors[], historyWindow }
//   RunSensorsNowDebug(idsJSON)   -> { scheduled, message }   ("" ou "[]" = todos)
//   RefreshSensorPolicyDebug()    -> { scheduled, message }

var sensorDebugPollId = null;

// Poll de 5 s: só roda com a aba de debug ativa E o checkbox marcado. O
// intervalo é sempre limpo por stopSensorDebugPoll() (mesma convenção do
// agentStatusPollId), então trocar de aba não deixa timer órfão.
var SENSOR_DEBUG_POLL_MS = 5000;

function sensorDebugEl(id) {
  return document.getElementById(id);
}

function sensorDebugSetStatus(message, type) {
  var el = sensorDebugEl('sensorDebugStatus');
  if (!el) return;
  var stamp = new Date().toLocaleTimeString();
  el.textContent = message + '  (' + stamp + ')';
  el.className = 'debug-status' + (type ? ' ' + type : '');
}

function sensorDebugEscape(value) {
  if (value === null || value === undefined) return '';
  return String(value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// "—" para nulos: o operador precisa distinguir "nunca coletou" de "coletou 0".
function sensorDebugDash(value) {
  if (value === null || value === undefined || value === '') return '—';
  return String(value);
}

function sensorDebugTime(value) {
  if (!value) return '—';
  var d = new Date(value);
  if (isNaN(d.getTime())) return String(value);
  return d.toLocaleString();
}

function sensorDebugSeconds(value) {
  if (value === null || value === undefined || value === '') return '—';
  return String(value) + 's';
}

// sensorType e SensorType (underlying string) e MARSHALA como string normal.
// Pode vir AUSENTE (omitempty) ou VAZIO quando o servidor mandou um numero
// desconhecido — os dois casos caem no rotulo de desconhecido.
function sensorDebugSensorType(sensor) {
  var t = sensor && sensor.sensorType ? String(sensor.sensorType) : '';
  return t ? t : '(tipo desconhecido)';
}

// Canais da AMOSTRA sao map[string]float64: as chaves dependem do coletor e
// nao existe rotulo fixo. Renderiza chave=valor (mesma decisao do console, que
// mapeia unidades e nao chaves). Chave nula/ausente vira "?".
function sensorDebugRunChannelsText(channels) {
  if (!channels || typeof channels !== 'object') return '—';
  var keys = Object.keys(channels);
  if (!keys.length) return '—';
  keys.sort();
  return keys.map(function (k) {
    var v = channels[k];
    return k + '=' + (v === null || v === undefined ? '—' : String(v));
  }).join(', ');
}

function sensorDebugChannelsText(sensor) {
  if (!sensor || !sensor.channels || !sensor.channels.length) {
    return '—';
  }
  return sensor.channels.map(function (ch) {
    var key = ch && ch.key ? ch.key : '?';
    return ch && ch.isPrimary ? key + '*' : key;
  }).join(', ');
}

function sensorDebugRenderEngine(snap) {
  var wrap = sensorDebugEl('sensorDebugEngine');
  if (!wrap) return;
  var engine = (snap && snap.engine) || {};
  var rows = [];

  function row(label, value, alert) {
    rows.push(
      '<div class="kv' + (alert ? ' alert' : '') + '">' +
      '<span class="k">' + sensorDebugEscape(label) + '</span>' +
      '<span class="v">' + sensorDebugEscape(value) + '</span>' +
      '</div>'
    );
  }

  row(translate('debug.sensorsEngineFingerprint'), sensorDebugDash(engine.fingerprintShort || engine.fingerprint));
  row(translate('debug.sensorsEngineLastSync'), sensorDebugTime(engine.lastSyncAt));
  row(translate('debug.sensorsEngineBackoff'), engine.backoffActive
    ? translate('debug.sensorsEngineBackoffOn') + ' ' + sensorDebugTime(engine.backoffUntil)
    : translate('debug.sensorsEngineBackoffOff'), !!engine.backoffActive);
  row(translate('debug.sensorsEngineDisabled'), engine.disabled ? translate('common.yes') : translate('common.no'), !!engine.disabled);
  row(translate('debug.sensorsEnginePolicyLoaded'), engine.policyLoaded ? translate('common.yes') : translate('common.no'), !engine.policyLoaded);
  row(translate('debug.sensorsEngineInFlight'), engine.inFlight ? translate('common.yes') : translate('common.no'));
  row(translate('debug.sensorsEngineConcurrency'), sensorDebugDash(engine.maxConcurrency) + ' / pendentes: ' + sensorDebugDash(engine.pendingSamples));
  row(translate('debug.sensorsEngineTick'), sensorDebugDash(engine.scheduleTickSeconds) + 's  |  flush: ' + sensorDebugDash(engine.flushIntervalSeconds) + 's  |  padrão: ' + sensorDebugDash(engine.defaultIntervalSeconds) + 's');
  row(translate('debug.sensorsEngineLastError'), engine.lastError ? String(engine.lastError) : '—', !!engine.lastError);

  wrap.innerHTML = rows.join('');
}

function sensorDebugRenderRows(snap) {
  var tbody = sensorDebugEl('sensorDebugTableBody');
  var wrap = sensorDebugEl('sensorDebugTableWrap');
  var empty = sensorDebugEl('sensorDebugEmpty');
  if (!tbody || !wrap || !empty) return;

  var sensors = (snap && snap.sensors) || [];
  if (!sensors.length) {
    tbody.innerHTML = '';
    wrap.classList.add('hidden');
    empty.classList.remove('hidden');
    empty.innerHTML = '<p data-i18n="debug.sensorsEmpty">' + sensorDebugEscape(translate('debug.sensorsEmpty')) + '</p>';
    return;
  }

  empty.classList.add('hidden');
  wrap.classList.remove('hidden');

  tbody.innerHTML = sensors.map(function (sensor) {
    var alerts = [];
    if (sensor.disabled) alerts.push('disabled');
    if (sensor.overdue) alerts.push('overdue');
    if (!sensor.scriptCached) alerts.push('nocache');
    var rowClass = alerts.length ? ' class="' + alerts.join(' ') + '"' : '';

    var interval = sensorDebugSeconds(sensor.intervalSeconds);
    if (sensor.jitterSeconds) interval += ' ±' + sensor.jitterSeconds + 's';
    if (sensor.timeoutSeconds) interval += ' / timeout ' + sensor.timeoutSeconds + 's';

    var script = sensor.scriptCached
      ? sensorDebugDash(sensor.scriptName) + (sensor.scriptVersion ? ' v' + sensor.scriptVersion : '')
      : translate('debug.sensorsScriptNotCached');

    var detailsId = 'sensorDebugDetail-' + sensorDebugEscape(sensor.sensorId || sensor.key || Math.random().toString(36).slice(2));
    detailsId = detailsId.replace(/[^A-Za-z0-9_-]/g, '_');

    return '' +
      '<tr' + rowClass + '>' +
      '<td class="name">' + sensorDebugEscape(sensorDebugDash(sensor.name)) + '</td>' +
      '<td class="mono">' + sensorDebugEscape(sensorDebugDash(sensor.key)) + '</td>' +
      '<td>' + sensorDebugEscape(sensorDebugSensorType(sensor)) + '</td>' +
      '<td class="mono">' + sensorDebugEscape(sensorDebugDash(sensor.target)) + '</td>' +
      '<td>' + sensorDebugEscape(interval) + '</td>' +
      '<td>' + sensorDebugEscape(sensorDebugTime(sensor.nextRunAt)) +
      (sensor.overdue ? ' <span class="sensor-flag">' + sensorDebugEscape(translate('debug.sensorsOverdue')) + '</span>' : '') + '</td>' +
      '<td>' + sensorDebugEscape(sensorDebugTime(sensor.lastRunAt)) + '</td>' +
      '<td>' + sensorDebugEscape(script) +
      (!sensor.scriptCached ? ' <span class="sensor-flag">' + sensorDebugEscape(translate('debug.sensorsResyncHint')) + '</span>' : '') + '</td>' +
      '<td>' + sensorDebugEscape(sensorDebugChannelsText(sensor)) + '</td>' +
      '<td class="actions">' +
      '<button type="button" class="btn subtle" data-sensor-run="' + sensorDebugEscape(sensor.sensorId) + '" data-i18n="debug.sensorsRunNow">' + sensorDebugEscape(translate('debug.sensorsRunNow')) + '</button>' +
      '<button type="button" class="btn subtle" data-sensor-details="' + detailsId + '" data-i18n="debug.sensorsDetails">' + sensorDebugEscape(translate('debug.sensorsDetails')) + '</button>' +
      '</td>' +
      '</tr>' +
      '<tr class="sensor-detail-row"><td colspan="10">' +
      '<div id="' + detailsId + '" class="sensor-detail hidden">' + sensorDebugRenderSensorDetail(sensor) + '</div>' +
      '</td></tr>';
  }).join('');
}

function sensorDebugRenderSensorDetail(sensor) {
  var parts = [];

  parts.push('<div class="sensor-detail-block"><h4>' + sensorDebugEscape(translate('debug.sensorsDetailChannels')) + '</h4>');
  if (sensor.channels && sensor.channels.length) {
    parts.push('<ul class="sensor-detail-channels">' + sensor.channels.map(function (ch) {
      var label = ch && ch.label ? ch.label : '';
      return '<li><span class="mono">' + sensorDebugEscape(ch && ch.key ? ch.key : '?') + '</span>' +
        (ch && ch.isPrimary ? ' <span class="sensor-flag">' + sensorDebugEscape(translate('debug.sensorsChannelPrimary')) + '</span>' : '') +
        (label ? ' — ' + sensorDebugEscape(label) : '') + '</li>';
    }).join('') + '</ul>');
  } else {
    parts.push('<p class="muted">—</p>');
  }
  parts.push('</div>');

  var results = (sensor.lastResults || []);
  parts.push('<div class="sensor-detail-block"><h4>' + sensorDebugEscape(translate('debug.sensorsDetailLastResults')) + '</h4>');
  if (!results.length) {
    parts.push('<p class="muted">' + sensorDebugEscape(translate('debug.sensorsNoResults')) + '</p>');
  } else {
    parts.push(results.map(function (res) {
      // measure e um objeto de forma VARIAVEL por tipo de sensor, cheio de
      // campos ponteiro. Renderizamos o objeto inteiro como JSON indentado em
      // vez de assumir chaves fixas (a forma muda a cada coletor).
      var measure = null;
      if (res && res.measure !== null && res.measure !== undefined && typeof res.measure === 'object') {
        try {
          measure = JSON.stringify(res.measure, null, 2);
        } catch (e) {
          measure = null;
        }
      }
      return '<div class="sensor-result">' +
        '<div class="sensor-result-head">' +
        '<span class="mono">' + sensorDebugEscape(sensorDebugDash(res && res.targetKey)) + '</span> · ' +
        sensorDebugEscape(sensorDebugTime(res && res.startedAt)) +
        (res && res.durationMs !== undefined && res.durationMs !== null ? ' · ' + sensorDebugEscape(res.durationMs) + ' ms' : '') +
        (res && res.error ? ' · <span class="err">' + sensorDebugEscape(res.error) + '</span>' : '') +
        '</div>' +
        '<div class="sensor-result-status">' +
        sensorDebugEscape(translate('debug.sensorsDetailDeclared')) + ': ' +
        sensorDebugEscape(sensorDebugDash(res && res.declaredStatus)) +
        (res && res.declaredMessage ? ' — ' + sensorDebugEscape(res.declaredMessage) : '') +
        '</div>' +
        (res && res.channels && typeof res.channels === 'object'
          ? '<div class="sensor-result-channels">' + sensorDebugEscape(sensorDebugRunChannelsText(res.channels)) + '</div>'
          : '') +
        (measure && measure !== '{}'
          ? '<pre class="sensor-result-measure">' + sensorDebugEscape(measure) + '</pre>'
          : '<p class="muted">' + sensorDebugEscape(translate('debug.sensorsNoMeasure')) + '</p>') +
        '</div>';
    }).join(''));
  }
  parts.push('</div>');

  return parts.join('');
}

function sensorDebugRenderHistory(snap) {
  var wrap = sensorDebugEl('sensorDebugHistoryWrap');
  var body = sensorDebugEl('sensorDebugHistoryBody');
  if (!wrap || !body) return;

  var items = [];
  var sensors = (snap && snap.sensors) || [];
  sensors.forEach(function (sensor) {
    (sensor.lastResults || []).forEach(function (res) {
      items.push({ sensor: sensor, result: res });
    });
  });

  if (!items.length) {
    wrap.classList.add('hidden');
    body.innerHTML = '';
    return;
  }

  // Mais recente primeiro: a janela vem do backend (historyWindow) e aqui só
  // ordenamos por startedAt para o operador ver a última coleta no topo.
  items.sort(function (a, b) {
    var ta = a.result && a.result.startedAt ? Date.parse(a.result.startedAt) : 0;
    var tb = b.result && b.result.startedAt ? Date.parse(b.result.startedAt) : 0;
    return (isNaN(tb) ? 0 : tb) - (isNaN(ta) ? 0 : ta);
  });

  wrap.classList.remove('hidden');
  body.innerHTML = items.map(function (item) {
    var sensor = item.sensor;
    var res = item.result || {};
    var collected = 0;
    var total = 0;
    if (res.channels && typeof res.channels === 'object' && !Array.isArray(res.channels)) {
      Object.keys(res.channels).forEach(function (k) {
        total++;
        if (res.channels[k] !== null && res.channels[k] !== undefined) collected++;
      });
    }
    return '<div class="sensor-history-item' + (res.error ? ' err' : '') + '">' +
      '<span class="sensor-history-sensor">' + sensorDebugEscape(sensorDebugDash(sensor.name || sensor.key)) + '</span>' +
      '<span class="mono">' + sensorDebugEscape(sensorDebugDash(res.targetKey)) + '</span>' +
      '<span>' + sensorDebugEscape(sensorDebugTime(res.startedAt)) + '</span>' +
      '<span>' + (res.durationMs !== undefined && res.durationMs !== null ? sensorDebugEscape(res.durationMs) + ' ms' : '—') + '</span>' +
      '<span>' + (total ? sensorDebugEscape(collected + '/' + total + ' ' + translate('debug.sensorsHistoryChannels')) : '—') + '</span>' +
      '<span class="err">' + (res.error ? sensorDebugEscape(res.error) : '') + '</span>' +
      '</div>';
  }).join('');
}

function renderSensorDebug(snap) {
  if (!snap) return;
  sensorDebugRenderEngine(snap);
  sensorDebugRenderRows(snap);
  sensorDebugRenderHistory(snap);
}

// HTTP fallback: quando a página roda no navegador (debug server) não existe o
// binding Wails; o bridge de debug-http-bridge.js já expõe window.wails. Aqui
// só tratamos a ausência total da API para não deixar a tabela em branco.
function sensorDebugApi(method, args) {
  var api = appApi();
  if (!api || typeof api[method] !== 'function') {
    throw new Error(translate('debug.sensorsApiUnavailable'));
  }
  return api[method].apply(api, args || []);
}

function loadSensorDebugSnapshot() {
  try {
    sensorDebugApi('GetSensorDebugSnapshot').then(function (snap) {
      renderSensorDebug(snap);
      var count = (snap && snap.sensors) ? snap.sensors.length : 0;
      if (!count) {
        sensorDebugSetStatus(translate('debug.sensorsStatusEmpty'), '');
      } else if (snap && snap.engine && snap.engine.lastError) {
        sensorDebugSetStatus(translate('debug.sensorsStatusEngineError', { error: String(snap.engine.lastError) }), 'error');
      } else if (snap && snap.engine && snap.engine.disabled) {
        sensorDebugSetStatus(translate('debug.sensorsStatusDisabled'), 'error');
      } else if (snap && snap.engine && snap.engine.backoffActive) {
        sensorDebugSetStatus(translate('debug.sensorsStatusBackoff'), 'error');
      } else {
        sensorDebugSetStatus(translate('debug.sensorsStatusLoaded', { count: String(count) }), 'success');
      }
    }).catch(function (err) {
      renderSensorDebug(null);
      sensorDebugSetStatus(translate('debug.sensorsStatusError', { error: (err && err.message) || String(err) }), 'error');
    });
  } catch (e) {
    sensorDebugSetStatus(translate('debug.sensorsStatusError', { error: (e && e.message) || String(e) }), 'error');
  }
}

function runSensorDebugNow(sensorId) {
  var ids = sensorId ? [sensorId] : [];
  sensorDebugSetStatus(translate('debug.sensorsRunning'), '');
  try {
    sensorDebugApi('RunSensorsNowDebug', [JSON.stringify(ids)]).then(function (res) {
      var message = (res && res.message) ? res.message : '';
      sensorDebugSetStatus(translate('debug.sensorsRunScheduled', { message: message }), res && res.scheduled ? 'success' : 'error');
      loadSensorDebugSnapshot();
    }).catch(function (err) {
      sensorDebugSetStatus(translate('debug.sensorsRunError', { error: (err && err.message) || String(err) }), 'error');
    });
  } catch (e) {
    sensorDebugSetStatus(translate('debug.sensorsRunError', { error: (e && e.message) || String(e) }), 'error');
  }
}

function runSensorDebugAll() {
  runSensorDebugNow('');
}

function resyncSensorDebugPolicy() {
  sensorDebugSetStatus(translate('debug.sensorsResyncing'), '');
  try {
    sensorDebugApi('RefreshSensorPolicyDebug').then(function (res) {
      var message = (res && res.message) ? res.message : '';
      sensorDebugSetStatus(translate('debug.sensorsResyncScheduled', { message: message }), res && res.scheduled ? 'success' : 'error');
      loadSensorDebugSnapshot();
    }).catch(function (err) {
      sensorDebugSetStatus(translate('debug.sensorsResyncError', { error: (err && err.message) || String(err) }), 'error');
    });
  } catch (e) {
    sensorDebugSetStatus(translate('debug.sensorsResyncError', { error: (e && e.message) || String(e) }), 'error');
  }
}

function sensorDebugAutoEnabled() {
  var auto = sensorDebugEl('sensorDebugAutoEl');
  return !!(auto && auto.checked);
}

function startSensorDebugPoll() {
  stopSensorDebugPoll();
  if (!sensorDebugAutoEnabled()) return;
  if (typeof activeTab !== 'undefined' && activeTab !== 'debug') return;
  sensorDebugPollId = setInterval(function () {
    if (document.hidden) return;
    if (typeof activeTab !== 'undefined' && activeTab !== 'debug') {
      stopSensorDebugPoll();
      return;
    }
    if (!sensorDebugAutoEnabled()) {
      stopSensorDebugPoll();
      return;
    }
    loadSensorDebugSnapshot();
  }, SENSOR_DEBUG_POLL_MS);
}

function stopSensorDebugPoll() {
  if (sensorDebugPollId) {
    clearInterval(sensorDebugPollId);
    sensorDebugPollId = null;
  }
}

function initSensorDebug() {
  var refreshBtn = sensorDebugEl('sensorDebugRefreshBtn');
  var runAllBtn = sensorDebugEl('sensorDebugRunAllBtn');
  var resyncBtn = sensorDebugEl('sensorDebugResyncBtn');
  var autoEl = sensorDebugEl('sensorDebugAutoEl');
  var tableBody = sensorDebugEl('sensorDebugTableBody');

  if (refreshBtn) {
    refreshBtn.addEventListener('click', function () {
      loadSensorDebugSnapshot();
    });
  }

  if (runAllBtn) {
    runAllBtn.addEventListener('click', function () {
      runAllBtn.disabled = true;
      runSensorDebugAll();
      setTimeout(function () { runAllBtn.disabled = false; }, 800);
    });
  }

  if (resyncBtn) {
    resyncBtn.addEventListener('click', function () {
      resyncBtn.disabled = true;
      resyncSensorDebugPolicy();
      setTimeout(function () { resyncBtn.disabled = false; }, 800);
    });
  }

  if (autoEl) {
    autoEl.addEventListener('change', function () {
      if (sensorDebugAutoEnabled()) {
        startSensorDebugPoll();
        loadSensorDebugSnapshot();
      } else {
        stopSensorDebugPoll();
      }
    });
  }

  // Delegação: a tabela é redesenhada a cada snapshot, então listener por
  // linha seria perdido. Um único listener no tbody cobre "Coletar agora" e
  // o expandir/recolher dos detalhes.
  if (tableBody) {
    tableBody.addEventListener('click', function (ev) {
      var target = ev.target;
      if (!target || target.tagName !== 'BUTTON') return;

      var runId = target.getAttribute('data-sensor-run');
      if (runId) {
        target.disabled = true;
        runSensorDebugNow(runId);
        setTimeout(function () { target.disabled = false; }, 800);
        return;
      }

      var detailsId = target.getAttribute('data-sensor-details');
      if (detailsId) {
        var box = document.getElementById(detailsId);
        if (box) {
          box.classList.toggle('hidden');
          target.textContent = box.classList.contains('hidden')
            ? translate('debug.sensorsDetails')
            : translate('debug.sensorsDetailsHide');
        }
      }
    });
  }
}

window.loadSensorDebugSnapshot = loadSensorDebugSnapshot;
window.stopSensorDebugPoll = stopSensorDebugPoll;

