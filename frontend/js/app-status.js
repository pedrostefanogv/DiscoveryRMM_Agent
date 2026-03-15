"use strict";

var statusPollId = null;

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
const statusInventoryAtEl = document.getElementById('statusInventoryAt');
const statusMessageEl = document.getElementById('statusMessage');

function statusSafe(value, fallback) {
  if (value === null || value === undefined || String(value).trim() === '') {
    return fallback || '-';
  }
  return String(value);
}

function formatStatusDate(value) {
  if (!value) return '-';
  var d = new Date(value);
  if (isNaN(d.getTime())) return String(value);
  return d.toLocaleString('pt-BR');
}

function renderStatusOverview(data) {
  var connected = !!(data && data.connected);

  if (statusConnectionDotEl) {
    statusConnectionDotEl.className = 'agent-status-indicator ' + (connected ? 'online' : 'offline');
  }
  if (statusConnectionLabelEl) {
    statusConnectionLabelEl.textContent = connected ? 'Online' : 'Offline';
  }

  var detailParts = [];
  if (data && data.agentId) detailParts.push('ID: ' + data.agentId);
  if (data && data.server) detailParts.push('Servidor: ' + data.server);
  if (statusConnectionDetailEl) {
    statusConnectionDetailEl.textContent = detailParts.length ? detailParts.join(' | ') : 'Sem identificacao de agente.';
  }

  if (statusAppVersionEl) statusAppVersionEl.textContent = statusSafe(data && data.appVersion, 'dev');
  if (statusCheckedAtEl) statusCheckedAtEl.textContent = formatStatusDate(data && data.checkedAtUtc);
  if (statusOSNameEl) statusOSNameEl.textContent = statusSafe(data && data.osName, '-');
  if (statusOSVersionEl) statusOSVersionEl.textContent = statusSafe(data && data.osVersion, '-');

  if (statusRealtimeEl) {
    if (data && data.realtimeAvailable) {
      statusRealtimeEl.textContent = data.realtimeNatsConnected ? 'Online' : 'Degradado';
    } else {
      statusRealtimeEl.textContent = 'Indisponivel';
    }
  }

  if (statusRealtimeAgentsEl) {
    statusRealtimeAgentsEl.textContent = statusSafe(data && data.realtimeConnectedAgents, '0');
  }

  if (statusInventoryAtEl) {
    statusInventoryAtEl.textContent = formatStatusDate(data && data.lastInventoryCollected);
  }

  if (statusMessageEl) {
    statusMessageEl.textContent = statusSafe(data && data.realtimeMessage, 'Sem informacoes adicionais.');
  }
}

function renderStatusError(message) {
  if (statusConnectionDotEl) {
    statusConnectionDotEl.className = 'agent-status-indicator offline';
  }
  if (statusConnectionLabelEl) {
    statusConnectionLabelEl.textContent = 'Falha na leitura de status';
  }
  if (statusConnectionDetailEl) {
    statusConnectionDetailEl.textContent = statusSafe(message, 'Nao foi possivel carregar o status do agente.');
  }
}

async function loadStatusOverview() {
  try {
    var data = await appApi().GetStatusOverview();
    renderStatusOverview(data || {});
  } catch (error) {
    renderStatusError(error && error.message ? error.message : String(error));
  }
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
}

function initStatusPage() {
  if (statusRefreshBtnEl) {
    statusRefreshBtnEl.addEventListener('click', loadStatusOverview);
  }
}

initStatusPage();
