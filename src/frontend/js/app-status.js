"use strict";

var statusPollId = null;

const statusRefreshBtnEl = document.getElementById('statusRefreshBtn');
const statusConnectionDotEl = document.getElementById('statusConnectionDot');
const statusConnectionLabelEl = document.getElementById('statusConnectionLabel');
const statusConnectionDetailEl = document.getElementById('statusConnectionDetail');
const statusAppVersionEl = document.getElementById('statusAppVersion');
const statusAppCommitEl = document.getElementById('statusAppCommit');
const statusBuildDateEl = document.getElementById('statusBuildDate');
const statusOSNameEl = document.getElementById('statusOSName');
const statusOSVersionEl = document.getElementById('statusOSVersion');
const statusOSEditionEl = document.getElementById('statusOSEdition');
const statusRealtimeEl = document.getElementById('statusRealtime');
const statusRealtimeAgentsEl = document.getElementById('statusRealtimeAgents');
const statusServerPongAtEl = document.getElementById('statusServerPongAt');
const statusNonCriticalTrafficEl = document.getElementById('statusNonCriticalTraffic');
const statusMessageEl = document.getElementById('statusMessage');
const statusZtcProvisionedEl = document.getElementById('statusZtcProvisioned');
const statusP2PBytesEl = document.getElementById('statusP2PBytes');
const statusP2PActiveEl = document.getElementById('statusP2PActive');
const statusUpdatePendingBannerEl = document.getElementById('statusUpdatePendingBanner');
const agentUpdateInstallBtnEl = document.getElementById('agentUpdateInstallBtn');

function statusSafe(value, fallback) {
  if (value === null || value === undefined || String(value).trim() === '') {
    return fallback || '-';
  }
  return String(value);
}


function formatConnectionTypeLabel(value) {
  var normalized = String(value || '').trim().toLowerCase();
  if (!normalized || normalized === '-') {
    return '-';
  }
  if (normalized === 'nats') {
    return 'NATS';
  }
  if (normalized === 'wss' || normalized === 'ws' || normalized === 'nats-ws' || normalized === 'nats-wss') {
    return 'WSS';
  }
  if (normalized.includes('ws')) {
    return 'WSS';
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
    return formatDate(d, '-');
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

// Busca indicadores extras (Zero Touch provisionados + bytes P2P).
// Best-effort: retorna null em caso de falha para nao impactar o status.
async function fetchP2PIntegrationFacts() {
  var api = appApi();
  if (!api) return null;
  var facts = {};
  try {
    if (typeof api.GetAutoProvisioningStats === 'function') {
      var ztc = await api.GetAutoProvisioningStats();
      facts.ztcProvisioned = ztc && ztc.totalProvisioned != null ? Number(ztc.totalProvisioned) : 0;
    }
  } catch (_error) {
    facts.ztcProvisioned = null;
  }
  try {
    if (typeof api.GetP2PDebugStatus === 'function') {
      var p2p = await api.GetP2PDebugStatus();
      var metrics = (p2p && p2p.metrics) || {};
      facts.p2pActive = !!(p2p && p2p.active);
      facts.p2pBytesUp = Number(metrics.bytesServed || 0);
      facts.p2pBytesDown = Number(metrics.bytesDownloaded || 0);
    }
  } catch (_error) {
    facts.p2pActive = null;
  }
  return facts;
}

function formatP2PBytesStatus(bytes) {
  var n = Number(bytes || 0);
  var units = ['B', 'KB', 'MB', 'GB', 'TB'];
  var i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  var label = (i === 0 ? String(n) : n.toFixed(1)) + ' ' + units[i];
  return label;
}

function renderStatusOverview(data) {
  // "Online" reflete o transporte conectado (fonte confiável do agentconn),
  // com fallback para o campo connected consolidado. Consistente com a bolinha
  // da barra (app-window.js) — o pong global pode ficar stale sem derrubar o
  // transporte, e não deve deixar o agente aparecendo offline.
  var connected = !!(data && (data.transportConnected || data.connected));

  if (statusConnectionDotEl) {
    statusConnectionDotEl.className = 'agent-status-indicator ' + (connected ? 'online' : 'offline');
  }
  if (statusConnectionLabelEl) {
    statusConnectionLabelEl.textContent = connected ? translate('common.online') : translate('common.offline');
  }

  var line1 = translate('window.meta.server') + ': ' + statusSafe(data && data.server, '-');
  var line2 = translate('status.transportState') + ': ' + formatConnectionTypeLabel(data && data.connectionType);

  if (statusConnectionDetailEl) {
    statusConnectionDetailEl.textContent = line1 + '\n' + line2;
  }

  if (statusAppVersionEl) statusAppVersionEl.textContent = statusSafe(data && data.appVersion, 'dev');
  var sidebarVersionEl = document.querySelector('.sidebar-version');
  if (sidebarVersionEl) sidebarVersionEl.textContent = 'v' + statusSafe(data && data.appVersion, 'dev');
  if (statusAppCommitEl) statusAppCommitEl.textContent = statusSafe(data && data.appCommit, '-');
  if (statusBuildDateEl) {
    var buildDateUtc = data && data.buildDateUtc;
    statusBuildDateEl.textContent = buildDateUtc ? formatDate(buildDateUtc, '-') : translate('common.unavailable');
  }
  if (statusOSNameEl) statusOSNameEl.textContent = statusSafe(data && (data.osDisplayVersion || data.osVersion), '-');
  if (statusOSVersionEl) statusOSVersionEl.textContent = statusSafe(data && data.osVersion, '-');
  if (statusOSEditionEl) {
    var edition = data && data.osEdition ? String(data.osEdition) : '';
    if (!edition && data && data.osName) {
      // Fallback: usa o nome do SO quando a edição não veio do backend.
      edition = String(data.osName);
    }
    statusOSEditionEl.textContent = statusSafe(edition, '-');
  }

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

  // Indicador de update pendente/adiado no card Status do Agente.
  if (statusUpdatePendingBannerEl) {
    var hasPending = !!(data && data.updatePendingTargetVersion);
    var isDeferred = !!(data && data.updateDeferred);
    if (hasPending) {
      var versionLabel = statusSafe(data.updatePendingTargetVersion, '?');
      if (isDeferred) {
        statusUpdatePendingBannerEl.textContent = translate('status.updatePendingDeferred', { version: versionLabel });
      } else {
        statusUpdatePendingBannerEl.textContent = translate('status.updatePendingReady', { version: versionLabel });
      }
      statusUpdatePendingBannerEl.hidden = false;
    } else {
      statusUpdatePendingBannerEl.hidden = true;
      statusUpdatePendingBannerEl.textContent = '';
    }
  }
  if (agentUpdateInstallBtnEl) {
    agentUpdateInstallBtnEl.hidden = !(data && data.updatePendingTargetVersion);
  }

  if (statusMessageEl) {
    var message = statusSafe(data && data.realtimeMessage, translate('common.noAdditionalInfo'));
    if (data && data.nonCriticalDeferred && data.nonCriticalDeferredReason) {
      message += ' | ' + translate('status.nonCriticalReason') + ': ' + data.nonCriticalDeferredReason;
    }
    statusMessageEl.textContent = message;
  }

  // Indicadores de integracao (Zero Touch + P2P).
  if (statusZtcProvisionedEl) {
    statusZtcProvisionedEl.textContent = data && data.ztcProvisioned != null && data.ztcProvisioned >= 0
      ? String(data.ztcProvisioned)
      : '-';
  }
  if (statusP2PBytesEl) {
    if (data && data.p2pBytesUp != null && data.p2pBytesDown != null) {
      statusP2PBytesEl.textContent = formatP2PBytesStatus(data.p2pBytesUp) + ' / ' + formatP2PBytesStatus(data.p2pBytesDown);
    } else {
      statusP2PBytesEl.textContent = '-';
    }
  }
  if (statusP2PActiveEl) {
    if (data && data.p2pActive === true) {
      statusP2PActiveEl.textContent = translate('common.online');
    } else if (data && data.p2pActive === false) {
      statusP2PActiveEl.textContent = translate('common.offline');
    } else {
      statusP2PActiveEl.textContent = '-';
    }
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

async function loadStatusOverview() {
  if (document.hidden) {
    return;
  }
  try {
    var api = appApi();
    var result = await Promise.all([
      api.GetStatusOverview(),
      fetchConnectedP2PAgents(),
      fetchP2PIntegrationFacts(),
    ]);
    var data = result[0] || {};
    if (result[1] !== null) {
      data.p2pConnectedAgents = result[1];
    }
    if (result[2]) {
      Object.assign(data, result[2]);
    }
    renderStatusOverview(data);
  } catch (error) {
    renderStatusError(error && error.message ? error.message : String(error));
  }
}

function startStatusPoll() {
  stopStatusPoll();
  loadStatusOverview();
  statusPollId = setInterval(loadStatusOverview, 4000);
}

function stopStatusPoll() {
  if (statusPollId) {
    clearInterval(statusPollId);
    statusPollId = null;
  }
}

function initStatusPage() {
  if (statusRefreshBtnEl) {
    statusRefreshBtnEl.addEventListener('click', loadStatusOverview);
  }
  if (agentUpdateInstallBtnEl) {
    agentUpdateInstallBtnEl.addEventListener('click', async function () {
      agentUpdateInstallBtnEl.disabled = true;
      var originalText = agentUpdateInstallBtnEl.textContent;
      agentUpdateInstallBtnEl.textContent = translate('status.installingUpdate');
      try {
        var api = appApi();
        if (api && typeof api.CheckAgentUpdate === 'function') {
          await api.CheckAgentUpdate();
        }
      } catch (e) {
        console.error('CheckAgentUpdate (install now) error:', e);
      } finally {
        agentUpdateInstallBtnEl.disabled = false;
        agentUpdateInstallBtnEl.textContent = originalText;
        loadStatusOverview();
      }
    });
  }
  if (agentUpdateInstallBtnEl) {
}

initStatusPage();

// Hook chamado pelo app-window.js quando um evento de conectividade dedicado
// (agent:connectivity) chega do backend. Atualiza imediatamente os indicadores
// da página de Status, sem depender do polling.
window.__connectivityEventPing = function (connected, transport, source) {
  if (statusConnectionDotEl) {
    statusConnectionDotEl.className = 'agent-status-indicator ' + (connected ? 'online' : 'offline');
  }
  if (statusConnectionLabelEl) {
    statusConnectionLabelEl.textContent = connected ? translate('common.online') : translate('common.offline');
  }
};

// Este arquivo carrega DEPOIS do app-window.js; se um evento de conectividade
// já tiver chegado, re-aplica o último estado aos indicadores.
if (typeof window.__lastConnectivityState === 'function') {
  try {
    var __lastConn = window.__lastConnectivityState();
    if (__lastConn && typeof window.__connectivityEventPing === 'function') {
      window.__connectivityEventPing(__lastConn.connected, __lastConn.transport, 'replay');
    }
  } catch (e) { /* não crítico */ }
}
