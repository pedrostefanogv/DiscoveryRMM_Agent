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
const statusInventoryAtEl = document.getElementById('statusInventoryAt');
const statusMessageEl = document.getElementById('statusMessage');
const openP2PDebugStatusBtnEl = document.getElementById('openP2PDebugStatusBtn');
const serviceHealthDotEl = document.getElementById('serviceHealthDot');
const serviceHealthLabelEl = document.getElementById('serviceHealthLabel');
const serviceHealthCountEl = document.getElementById('serviceHealthCount');
const serviceHealthProblemsEl = document.getElementById('serviceHealthProblems');
const serviceHealthDetailEl = document.getElementById('serviceHealthDetail');
const serviceHealthIndicatorEl = document.getElementById('serviceHealthIndicator');

function statusSafe(value, fallback) {
  if (value === null || value === undefined || String(value).trim() === '') {
    return fallback || '-';
  }
  return String(value);
}

// formatStatusDate mantida como alias para compatibilidade; use formatDate diretamente.
function formatStatusDate(value) { return formatDate(value, '-'); }

function renderStatusOverview(data) {
  var connected = !!(data && data.connected);

  if (statusConnectionDotEl) {
    statusConnectionDotEl.className = 'agent-status-indicator ' + (connected ? 'online' : 'offline');
  }
  if (statusConnectionLabelEl) {
    statusConnectionLabelEl.textContent = connected ? 'Online' : 'Offline';
  }

  var line1 = 'PC: ' + statusSafe(data && data.hostname, 'Computador local');
  var serverPart = 'Servidor: ' + statusSafe(data && data.server, '-');
  var connPart = 'Conexao: ' + statusSafe(data && data.connectionType, '-');
  var line2 = serverPart + ' / ' + connPart;

  if (statusConnectionDetailEl) {
    statusConnectionDetailEl.textContent = line1 + '\n' + line2;
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

function renderServiceHealth(health) {
  if (!health) {
    if (serviceHealthDotEl) serviceHealthDotEl.className = 'agent-status-indicator offline';
    if (serviceHealthLabelEl) serviceHealthLabelEl.textContent = 'Service indisponivel';
    if (serviceHealthDetailEl) {
      serviceHealthDetailEl.textContent = 'Nao foi possivel comunicar com o servico Discovery. Reinicie o computador e tente novamente. Se o problema persistir, contate o suporte.';
    }
    updateTopbarServiceIndicator('offline', '');
    return;
  }

  if (health.error) {
    if (serviceHealthDotEl) serviceHealthDotEl.className = 'agent-status-indicator offline';
    if (serviceHealthLabelEl) serviceHealthLabelEl.textContent = 'Service indisponivel';
    var userMessage = statusSafe(
      health.user_message,
      'Nao foi possivel comunicar com o servico Discovery. Reinicie o computador e tente novamente. Se o problema persistir, contate o suporte.'
    );
    if (serviceHealthDetailEl) serviceHealthDetailEl.textContent = userMessage;
    updateTopbarServiceIndicator('offline', userMessage);
    return;
  }

  var running = !!health.running;
  var unhealthyCount = health.unhealthy_count || 0;
  var componentCount = health.component_count || 0;
  var degradedCount = health.degraded_count || 0;

  // Determinar status visual
  var statusClass = 'offline';
  var statusLabel = 'Offline';
  if (!running) {
    statusClass = 'offline';
    statusLabel = 'Não está rodando';
  } else if (unhealthyCount > 0) {
    statusClass = 'error';
    statusLabel = 'Problema detectado';
  } else if (degradedCount > 0) {
    statusClass = 'warning';
    statusLabel = 'Degradado';
  } else {
    statusClass = 'online';
    statusLabel = 'Saudável';
  }

  if (serviceHealthDotEl) serviceHealthDotEl.className = 'agent-status-indicator ' + statusClass;
  if (serviceHealthLabelEl) serviceHealthLabelEl.textContent = statusLabel;
  if (serviceHealthCountEl) serviceHealthCountEl.textContent = String(componentCount);
  if (serviceHealthProblemsEl) serviceHealthProblemsEl.textContent = String(unhealthyCount + degradedCount);
  
  var detail = '';
  if (health.components && health.components.length > 0) {
    var problemComps = health.components.filter(function(c) { 
      return String(c.status || '').toLowerCase() !== 'healthy'; 
    });
    if (problemComps.length > 0) {
      detail = 'Problemas em: ' + problemComps.map(function(c) { 
        return String(c.component || c.Component || 'desconhecido'); 
      }).join(', ');
    } else {
      detail = 'Todos os componentes estão saudáveis';
    }
  }
  if (serviceHealthDetailEl) serviceHealthDetailEl.textContent = detail || 'Aguardando...';
  
  updateTopbarServiceIndicator(statusClass, statusLabel);
}

function updateTopbarServiceIndicator(statusClass, label) {
  if (!serviceHealthIndicatorEl) return;
  
  // Atualizar classe
  serviceHealthIndicatorEl.className = 'topbar-indicator ' + statusClass;
  
  // Mostrar indicador se há conteúdo relevante
  if (statusClass !== 'online' || label) {
    serviceHealthIndicatorEl.style.display = 'inline-flex';
    serviceHealthIndicatorEl.title = 'Service: ' + (label || statusClass);
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
    var data = await appApi().GetStatusOverview();
    renderStatusOverview(data || {});
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
  if (openP2PDebugStatusBtnEl) {
    openP2PDebugStatusBtnEl.addEventListener('click', function () {
      setActiveTab('p2p');
      if (typeof loadP2PView === 'function') {
        loadP2PView();
      }
    });
  }
}

initStatusPage();
