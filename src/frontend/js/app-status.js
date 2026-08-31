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

// Escreve textContent apenas quando o valor muda. Reescrever o mesmo texto a
// cada ciclo de polling (4s) força reflow e causa a "piscada" percebida nos
// cards da página de status.
function statusSetText(el, value) {
  if (!el) return;
  var text = value === null || value === undefined ? '' : String(value);
  if (el.textContent !== text) {
    el.textContent = text;
  }
}

// Mostra/esconde elemento apenas quando o estado muda (evita reflow redundante).
function statusSetHidden(el, hidden) {
  if (!el || el.hidden === hidden) return;
  el.hidden = hidden;
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
// Best-effort: nunca lança e nunca bloqueia o render principal — cada chamada
// tem timeout próprio porque no agent nativo essas APIs podem demorar
// (locks do subsistema P2P) e não podem travar a página de status.
function withTimeout(promise, ms) {
  var timerId;
  var timeoutPromise = new Promise(function (_resolve, reject) {
    timerId = setTimeout(function () { reject(new Error('timeout')); }, ms);
  });
  // Limpa o timer quando a promise original resolve/rejeita primeiro,
  // evitando acumulo de timers pendentes com o polling de 4s.
  promise.then(
    function () { clearTimeout(timerId); },
    function () { clearTimeout(timerId); }
  );
  return Promise.race([promise, timeoutPromise]);
}

var p2pFactsInFlight = false;

async function fetchP2PIntegrationFacts() {
  if (p2pFactsInFlight) return null; // evita chamadas concorrentes
  p2pFactsInFlight = true;
  try {
    return await doFetchP2PIntegrationFacts();
  } finally {
    p2pFactsInFlight = false;
  }
}

async function doFetchP2PIntegrationFacts() {
  var api = appApi();
  if (!api) return null;
  var facts = {};
  try {
    if (typeof api.GetAutoProvisioningStats === 'function') {
      var ztc = await withTimeout(api.GetAutoProvisioningStats(), 3000);
      facts.ztcProvisioned = ztc && ztc.totalProvisioned != null ? Number(ztc.totalProvisioned) : 0;
    }
  } catch (_error) {
    facts.ztcProvisioned = null;
  }
  try {
    if (typeof api.GetP2PDebugStatus === 'function') {
      var p2p = await withTimeout(api.GetP2PDebugStatus(), 3000);
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
    var dotClass = 'agent-status-indicator ' + (connected ? 'online' : 'offline');
    // Não reatribuir a mesma classe: reiniciaria a animação pulse a cada poll.
    if (statusConnectionDotEl.className !== dotClass) {
      statusConnectionDotEl.className = dotClass;
    }
  }
  if (statusConnectionLabelEl) {
    statusSetText(statusConnectionLabelEl, connected ? translate('common.online') : translate('common.offline'));
  }

  var line1 = translate('window.meta.server') + ': ' + statusSafe(data && data.server, '-');
  var line2 = translate('status.transportState') + ': ' + formatConnectionTypeLabel(data && data.connectionType);

  if (statusConnectionDetailEl) {
    statusSetText(statusConnectionDetailEl, line1 + '\n' + line2);
  }

  if (statusAppVersionEl) statusSetText(statusAppVersionEl, statusSafe(data && data.appVersion, 'dev'));
  var sidebarVersionEl = document.querySelector('.sidebar-version');
  if (sidebarVersionEl) statusSetText(sidebarVersionEl, 'v' + statusSafe(data && data.appVersion, 'dev'));
  if (statusAppCommitEl) statusSetText(statusAppCommitEl, statusSafe(data && data.appCommit, '-'));
  if (statusBuildDateEl) {
    var buildDateUtc = data && data.buildDateUtc;
    statusSetText(statusBuildDateEl, buildDateUtc ? formatDate(buildDateUtc, '-') : translate('common.unavailable'));
  }
  if (statusOSNameEl) statusSetText(statusOSNameEl, statusSafe(data && (data.osDisplayVersion || data.osVersion), '-'));
  if (statusOSVersionEl) statusSetText(statusOSVersionEl, statusSafe(data && data.osVersion, '-'));
  if (statusOSEditionEl) {
    var edition = data && data.osEdition ? String(data.osEdition) : '';
    if (!edition && data && data.osName) {
      // Fallback: usa o nome do SO quando a edição não veio do backend.
      edition = String(data.osName);
    }
    statusSetText(statusOSEditionEl, statusSafe(edition, '-'));
  }

  if (statusRealtimeEl) {
    if (data && data.realtimeAvailable) {
      statusSetText(statusRealtimeEl, data.realtimeNatsConnected ? translate('common.online') : translate('common.degraded'));
    } else {
      statusSetText(statusRealtimeEl, translate('common.unavailable'));
    }
  }

  if (statusRealtimeAgentsEl) {
    statusSetText(statusRealtimeAgentsEl, resolveConnectedP2PAgents(data));
  }

  if (statusServerPongAtEl) {
    statusSetText(statusServerPongAtEl, formatStatusRelativeDate(data && data.lastGlobalPongAtUtc));
  }

  if (statusNonCriticalTrafficEl) {
    statusSetText(statusNonCriticalTrafficEl, formatNonCriticalTrafficStatus(data));
  }

  // Indicador de update pendente/adiado no card Status do Agente.
  if (statusUpdatePendingBannerEl) {
    var hasPending = !!(data && data.updatePendingTargetVersion);
    var isDeferred = !!(data && data.updateDeferred);
    if (hasPending) {
      var versionLabel = statusSafe(data.updatePendingTargetVersion, '?');
      if (isDeferred) {
        statusSetText(statusUpdatePendingBannerEl, translate('status.updatePendingDeferred', { version: versionLabel }));
      } else {
        statusSetText(statusUpdatePendingBannerEl, translate('status.updatePendingReady', { version: versionLabel }));
      }
      statusSetHidden(statusUpdatePendingBannerEl, false);
    } else {
      statusSetHidden(statusUpdatePendingBannerEl, true);
      statusSetText(statusUpdatePendingBannerEl, '');
    }
  }
  if (agentUpdateInstallBtnEl) {
    // Botão "Atualizar agora" só aparece com update pendente de instalação
    // (janela aberta → aguardando ir ao tray). Permite forçar a instalação.
    statusSetHidden(agentUpdateInstallBtnEl, !(data && data.updatePendingTargetVersion));
  }

  if (statusMessageEl) {
    var message = statusSafe(data && data.realtimeMessage, translate('common.noAdditionalInfo'));
    if (data && data.nonCriticalDeferred && data.nonCriticalDeferredReason) {
      message += ' | ' + translate('status.nonCriticalReason') + ': ' + data.nonCriticalDeferredReason;
    }
    statusSetText(statusMessageEl, message);
  }

  // Indicadores de integracao (Zero Touch + P2P).
  if (statusZtcProvisionedEl) {
    statusSetText(statusZtcProvisionedEl, data && data.ztcProvisioned != null && data.ztcProvisioned >= 0
      ? String(data.ztcProvisioned)
      : '-');
  }
  if (statusP2PBytesEl) {
    if (data && data.p2pBytesUp != null && data.p2pBytesDown != null) {
      statusSetText(statusP2PBytesEl, formatP2PBytesStatus(data.p2pBytesUp) + ' / ' + formatP2PBytesStatus(data.p2pBytesDown));
    } else {
      statusSetText(statusP2PBytesEl, '-');
    }
  }
  if (statusP2PActiveEl) {
    if (data && data.p2pActive === true) {
      statusSetText(statusP2PActiveEl, translate('common.online'));
    } else if (data && data.p2pActive === false) {
      statusSetText(statusP2PActiveEl, translate('common.offline'));
    } else {
      statusSetText(statusP2PActiveEl, '-');
    }
  }
}

function renderStatusError(message) {
  if (statusConnectionDotEl) {
    var dotClass = 'agent-status-indicator offline';
    if (statusConnectionDotEl.className !== dotClass) {
      statusConnectionDotEl.className = dotClass;
    }
  }
  if (statusConnectionLabelEl) {
    statusSetText(statusConnectionLabelEl, translate('status.failedRead'));
  }
  if (statusConnectionDetailEl) {
    statusSetText(statusConnectionDetailEl, statusSafe(message, translate('status.couldNotLoadAgentStatus')));
  }
}

async function loadStatusOverview() {
  if (document.hidden) {
    return;
  }
  try {
    var api = appApi();
    // Render principal não depende dos fatos P2P/ZTC: no agent nativo essas
    // chamadas podem ser lentas e não podem atrasar o status básico.
    var result = await Promise.all([
      api.GetStatusOverview(),
      fetchConnectedP2PAgents(),
    ]);
    var data = result[0] || {};
    if (result[1] !== null) {
      data.p2pConnectedAgents = result[1];
    }
    renderStatusOverview(data);
    window.__lastStatusData = data;
  } catch (error) {
    renderStatusError(error && error.message ? error.message : String(error));
    return;
  }
  // Fatos de integração carregam em segundo plano e atualizam SOMENTE os
  // campos deles, sem re-renderizar o restante (evita sobrescrever dados
  // mais novos que o polling de 4s possa ter trazido enquanto isso).
  fetchP2PIntegrationFacts().then(function (facts) {
    if (!facts) return;
    if (window.__lastStatusData) {
      Object.assign(window.__lastStatusData, facts);
    }
    if (statusZtcProvisionedEl && facts.ztcProvisioned != null) {
      statusSetText(statusZtcProvisionedEl, String(facts.ztcProvisioned));
    }
    if (statusP2PBytesEl && facts.p2pBytesUp != null && facts.p2pBytesDown != null) {
      statusSetText(statusP2PBytesEl, formatP2PBytesStatus(facts.p2pBytesUp) + ' / ' + formatP2PBytesStatus(facts.p2pBytesDown));
    }
    if (statusP2PActiveEl && facts.p2pActive != null) {
      statusSetText(statusP2PActiveEl, facts.p2pActive ? translate('common.online') : translate('common.offline'));
    }
  }).catch(function () { /* best-effort */ });
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
}

initStatusPage();

// Hook chamado pelo app-window.js quando um evento de conectividade dedicado
// (agent:connectivity) chega do backend. Atualiza imediatamente os indicadores
// da página de Status, sem depender do polling.
window.__connectivityEventPing = function (connected, transport, source) {
  if (statusConnectionDotEl) {
    var dotClass = 'agent-status-indicator ' + (connected ? 'online' : 'offline');
    if (statusConnectionDotEl.className !== dotClass) {
      statusConnectionDotEl.className = dotClass;
    }
  }
  if (statusConnectionLabelEl) {
    statusSetText(statusConnectionLabelEl, connected ? translate('common.online') : translate('common.offline'));
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
