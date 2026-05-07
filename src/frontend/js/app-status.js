"use strict";

var statusPollId = null;
var serviceHealthPollId = null;

const statusRefreshBtnEl = document.getElementById('statusRefreshBtn');
const statusConnectionDotEl = document.getElementById('statusConnectionDot');
const statusConnectionLabelEl = document.getElementById('statusConnectionLabel');
const statusConnectionDetailEl = document.getElementById('statusConnectionDetail');
const statusAppVersionEl = document.getElementById('statusAppVersion');
const statusCheckedAtEl = document.getElementById('statusCheckedAt');
const statusOSNameEl = document.getElementById('statusOSName');
const statusOSVersionEl = document.getElementById('statusOSVersion');
const statusRealtimeEl = document.getElementById('statusRealtime');
const statusRealtimeAgentsEl = document.getElementById('statusRealtimeAgents');
const statusServerPongAtEl = document.getElementById('statusServerPongAt');
const statusNonCriticalTrafficEl = document.getElementById('statusNonCriticalTraffic');
const statusMessageEl = document.getElementById('statusMessage');
const serviceHealthDotEl = document.getElementById('serviceHealthDot');
const serviceHealthLabelEl = document.getElementById('serviceHealthLabel');
const serviceHealthIndicatorEl = document.getElementById('serviceHealthIndicator');

function statusSafe(value, fallback) {
  if (value === null || value === undefined || String(value).trim() === '') {
    return fallback || '-';
  }
  return String(value);
}

// formatStatusDate mantida como alias para compatibilidade; use formatDate diretamente.
function formatStatusDate(value) { return formatDate(value, '-'); }

function formatConnectionTypeLabel(value) {
  var normalized = String(value || '').trim().toLowerCase();
  if (!normalized || normalized === '-') {
    return '-';
  }
  if (normalized === 'nats') {
    return 'NATS';
  }
  if (normalized === 'wss' || normalized === 'ws' || normalized === 'nats-ws' || normalized === 'nats-wss') {
    return 'NATS WS';
  }
  if (normalized.includes('nats') && normalized.includes('ws')) {
    return 'NATS WS';
  }
  return normalized.toUpperCase();
}

function formatStatusRelativeDate(value) {
  if (!value && value !== 0) return '-';
  var d = value instanceof Date ? value : new Date(value);
  if (isNaN(d.getTime())) return statusSafe(value, '-');

  var diffSeconds = Math.round((d.getTime() - Date.now()) / 1000);
  var absSeconds = Math.abs(diffSeconds);
  if (absSeconds >= 24 * 60 * 60) {
    return formatStatusDate(d);
  }

  var localeTag = 'pt-BR';
  try {
    localeTag = getAppLocaleTag(getAppLocale());
  } catch (_e) {
    // Fallback para locale padrao quando i18n ainda nao estiver pronto.
  }

  if (typeof Intl !== 'undefined' && typeof Intl.RelativeTimeFormat === 'function') {
    var rtf = new Intl.RelativeTimeFormat(localeTag, { numeric: 'auto' });
    if (absSeconds < 60) return rtf.format(diffSeconds, 'second');
    if (absSeconds < 60 * 60) return rtf.format(Math.round(diffSeconds / 60), 'minute');
    return rtf.format(Math.round(diffSeconds / 3600), 'hour');
  }

  if (absSeconds < 60) return diffSeconds < 0 ? ('ha ' + absSeconds + 's') : ('em ' + absSeconds + 's');
  var absMinutes = Math.round(absSeconds / 60);
  if (absSeconds < 60 * 60) return diffSeconds < 0 ? ('ha ' + absMinutes + ' min') : ('em ' + absMinutes + ' min');
  var absHours = Math.round(absSeconds / 3600);
  return diffSeconds < 0 ? ('ha ' + absHours + ' h') : ('em ' + absHours + ' h');
}

function resolveConnectedP2PAgents(data) {
  var p2pConnectedAgents = Number(data && data.p2pConnectedAgents);
  if (Number.isFinite(p2pConnectedAgents) && p2pConnectedAgents >= 0) {
    return String(p2pConnectedAgents);
  }
  return statusSafe(data && data.realtimeConnectedAgents, '0');
}

function formatNonCriticalTrafficStatus(data) {
  if (!(data && data.nonCriticalDeferred)) {
    return translate('status.nonCriticalNormal');
  }

  var untilLabel = formatStatusRelativeDate(data.nonCriticalDeferredUntilUtc);
  if (untilLabel === '-') {
    return translate('status.nonCriticalDeferred');
  }
  return translate('status.nonCriticalDeferredUntil', { until: untilLabel });
}

async function fetchConnectedP2PAgents() {
  try {
    var api = appApi();
    if (!api || typeof api.GetP2PPeers !== 'function') {
      return null;
    }
    var peers = await api.GetP2PPeers();
    if (!Array.isArray(peers)) {
      return null;
    }
    return peers.length;
  } catch (_error) {
    return null;
  }
}

function renderStatusOverview(data) {
  var connected = !!(data && data.connected);

  if (statusConnectionDotEl) {
    statusConnectionDotEl.className = 'agent-status-indicator ' + (connected ? 'online' : 'offline');
  }
  if (statusConnectionLabelEl) {
    statusConnectionLabelEl.textContent = connected ? translate('common.online') : translate('common.offline');
  }

  var line1 = translate('window.meta.pc') + ': ' + statusSafe(data && data.hostname, translate('status.localComputer'));
  var serverPart = translate('window.meta.server') + ': ' + statusSafe(data && data.server, '-');
  var connPart = translate('window.meta.connection') + ': ' + formatConnectionTypeLabel(data && data.connectionType);
  var transportPart = translate('status.transportState') + ': ' + (data && data.transportConnected ? translate('common.online') : translate('common.offline'));
  var line2 = serverPart + ' / ' + connPart + ' / ' + transportPart;
  var line3 = '';
  if (data && data.onlineReason) {
    line3 = translate('status.onlineSignal') + ': ' + statusSafe(data.onlineReason, '-');
  }

  if (statusConnectionDetailEl) {
    statusConnectionDetailEl.textContent = line3 ? (line1 + '\n' + line2 + '\n' + line3) : (line1 + '\n' + line2);
  }

  if (statusAppVersionEl) statusAppVersionEl.textContent = statusSafe(data && data.appVersion, 'dev');
  if (statusCheckedAtEl) statusCheckedAtEl.textContent = formatStatusDate(data && data.checkedAtUtc);
  if (statusOSNameEl) statusOSNameEl.textContent = statusSafe(data && data.osName, '-');
  if (statusOSVersionEl) statusOSVersionEl.textContent = statusSafe(data && data.osVersion, '-');

  if (statusRealtimeEl) {
    if (data && data.realtimeAvailable) {
      statusRealtimeEl.textContent = data.realtimeNatsConnected ? translate('common.online') : translate('common.degraded');
    } else {
      statusRealtimeEl.textContent = translate('common.unavailable');
    }
  }

  if (statusRealtimeAgentsEl) {
    statusRealtimeAgentsEl.textContent = resolveConnectedP2PAgents(data);
  }

  if (statusServerPongAtEl) {
    statusServerPongAtEl.textContent = formatStatusRelativeDate(data && data.lastGlobalPongAtUtc);
  }

  if (statusNonCriticalTrafficEl) {
    statusNonCriticalTrafficEl.textContent = formatNonCriticalTrafficStatus(data);
  }

  if (statusMessageEl) {
    var message = statusSafe(data && data.realtimeMessage, translate('common.noAdditionalInfo'));
    if (data && data.nonCriticalDeferred && data.nonCriticalDeferredReason) {
      message += ' | ' + translate('status.nonCriticalReason') + ': ' + data.nonCriticalDeferredReason;
    }
    statusMessageEl.textContent = message;
  }
}

function renderStatusError(message) {
  if (statusConnectionDotEl) {
    statusConnectionDotEl.className = 'agent-status-indicator offline';
  }
  if (statusConnectionLabelEl) {
    statusConnectionLabelEl.textContent = translate('status.failedRead');
  }
  if (statusConnectionDetailEl) {
    statusConnectionDetailEl.textContent = statusSafe(message, translate('status.couldNotLoadAgentStatus'));
  }
}

function renderServiceHealth(health) {
  if (!health) {
    if (serviceHealthDotEl) serviceHealthDotEl.className = 'agent-status-indicator offline';
    if (serviceHealthLabelEl) serviceHealthLabelEl.textContent = translate('status.serviceUnavailable');
    updateTopbarServiceIndicator('offline', translate('status.serviceUnavailable'));
    return;
  }

  if (health.error) {
    if (serviceHealthDotEl) serviceHealthDotEl.className = 'agent-status-indicator offline';
    if (serviceHealthLabelEl) serviceHealthLabelEl.textContent = translate('status.serviceUnavailable');
    updateTopbarServiceIndicator('offline', translate('status.serviceUnavailable'));
    return;
  }

  var running = !!health.running;
  var unhealthyCount = health.unhealthy_count || 0;
  var degradedCount = health.degraded_count || 0;
  var hasProblems = unhealthyCount > 0 || degradedCount > 0;

  var statusClass = 'online';
  var statusLabel = translate('status.serviceOk');
  if (!running) {
    statusClass = 'offline';
    statusLabel = translate('status.serviceUnavailable');
  } else if (hasProblems) {
    statusClass = 'warning';
    statusLabel = translate('status.serviceUnavailable');
  }

  if (serviceHealthDotEl) serviceHealthDotEl.className = 'agent-status-indicator ' + statusClass;
  if (serviceHealthLabelEl) serviceHealthLabelEl.textContent = statusLabel;

  updateTopbarServiceIndicator(statusClass, statusLabel);
}

function updateTopbarServiceIndicator(statusClass, label) {
  if (!serviceHealthIndicatorEl) return;
  
  // Atualizar classe
  serviceHealthIndicatorEl.className = 'topbar-indicator ' + statusClass;
  
  // Mostrar indicador se há conteúdo relevante
  if (statusClass !== 'online' || label) {
    serviceHealthIndicatorEl.style.display = 'inline-flex';
    serviceHealthIndicatorEl.title = translate('status.serviceIndicator', { label: label || statusClass });
  }
}

async function loadServiceHealth() {
  if (document.hidden) {
    return;
  }
  try {
    var health = await appApi().GetServiceHealth();
    renderServiceHealth(health || { error: 'Resposta vazia' });
  } catch (error) {
    renderServiceHealth({ error: error && error.message ? error.message : String(error) });
  }
}

async function loadStatusOverview() {
  if (document.hidden) {
    return;
  }
  try {
    var api = appApi();
    var result = await Promise.all([
      api.GetStatusOverview(),
      fetchConnectedP2PAgents(),
    ]);
    var data = result[0] || {};
    if (result[1] !== null) {
      data.p2pConnectedAgents = result[1];
    }
    renderStatusOverview(data);
  } catch (error) {
    renderStatusError(error && error.message ? error.message : String(error));
  }
  // Carregar health do service em paralelo
  loadServiceHealth().catch(function() {
    // Ignorar erro de service health para não interromper status geral
  });
}

function startStatusPoll() {
  stopStatusPoll();
  loadStatusOverview();
  statusPollId = setInterval(loadStatusOverview, 10000);
}

function stopStatusPoll() {
  if (statusPollId) {
    clearInterval(statusPollId);
    statusPollId = null;
  }
  if (serviceHealthPollId) {
    clearInterval(serviceHealthPollId);
    serviceHealthPollId = null;
  }
}

function initStatusPage() {
  if (statusRefreshBtnEl) {
    statusRefreshBtnEl.addEventListener('click', loadStatusOverview);
  }
}

initStatusPage();
