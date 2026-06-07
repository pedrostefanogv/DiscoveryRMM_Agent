"use strict";

var p2pRefreshTimerId = null;
// Intervalo base do polling e backoff em caso de erro consecutivo.
var p2pPollBaseMs = 5000;
var p2pPollCurrentMs = 5000;
var p2pPollMaxMs = 60000;
var p2pPollErrorCount = 0;
var p2pPeerArtifactIndex = [];

// startP2PPoller inicia o polling adaptativo com backoff em caso de falha.
function startP2PPoller() {
  if (p2pRefreshTimerId) return;
  p2pScheduleNextPoll();
}

function stopP2PPoller() {
  if (p2pRefreshTimerId) {
    clearTimeout(p2pRefreshTimerId);
    p2pRefreshTimerId = null;
  }
}

function p2pScheduleNextPoll() {
  p2pRefreshTimerId = setTimeout(function () {
    p2pRefreshTimerId = null;
    var p2pView = document.getElementById('p2pView');
    if (!document.hidden && p2pView && !p2pView.classList.contains('hidden')) {
      loadP2PView().then(function () {
        // Sucesso: resetar backoff
        p2pPollErrorCount = 0;
        p2pPollCurrentMs = p2pPollBaseMs;
        p2pScheduleNextPoll();
      }).catch(function () {
        // Falha: backoff exponencial com cap em p2pPollMaxMs
        p2pPollErrorCount++;
        p2pPollCurrentMs = Math.min(p2pPollCurrentMs * 2, p2pPollMaxMs);
        p2pScheduleNextPoll();
      });
    } else {
      // Aba oculta — reagendar sem fazer requisição
      p2pScheduleNextPoll();
    }
  }, p2pPollCurrentMs);
}

function p2pApi() {
  return appApi();
}

function p2pEl(id) {
  return document.getElementById(id);
}

function p2pSwitchSubtab(targetPanelId) {
  var tabButtons = document.querySelectorAll('#p2pView [data-p2p-tab-target]');
  var panels = document.querySelectorAll('#p2pView .p2p-subtab-panel');
  if (!tabButtons.length || !panels.length) return;

  tabButtons.forEach(function (button) {
    var isActive = button.getAttribute('data-p2p-tab-target') === targetPanelId;
    button.classList.toggle('is-active', isActive);
    button.setAttribute('aria-selected', isActive ? 'true' : 'false');
    button.setAttribute('tabindex', isActive ? '0' : '-1');
  });

  panels.forEach(function (panel) {
    var isActive = panel.id === targetPanelId;
    panel.classList.toggle('hidden', !isActive);
    if (isActive) {
      panel.removeAttribute('hidden');
    } else {
      panel.setAttribute('hidden', 'hidden');
    }
  });
}

function p2pInitSubtabs() {
  var tabButtons = document.querySelectorAll('#p2pView [data-p2p-tab-target]');
  if (!tabButtons.length) return;

  tabButtons.forEach(function (button) {
    if (button.getAttribute('data-p2p-tab-ready') === '1') return;
    button.setAttribute('data-p2p-tab-ready', '1');

    button.addEventListener('click', function () {
      p2pSwitchSubtab(button.getAttribute('data-p2p-tab-target'));
    });

    button.addEventListener('keydown', function (event) {
      if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return;
      event.preventDefault();

      var buttons = Array.prototype.slice.call(tabButtons);
      var currentIndex = buttons.indexOf(button);
      var nextIndex = event.key === 'ArrowRight'
        ? (currentIndex + 1) % buttons.length
        : (currentIndex - 1 + buttons.length) % buttons.length;
      var nextButton = buttons[nextIndex];
      nextButton.focus();
      p2pSwitchSubtab(nextButton.getAttribute('data-p2p-tab-target'));
    });
  });

  var activeTab = document.querySelector('#p2pView [data-p2p-tab-target].is-active');
  var initialTarget = activeTab
    ? activeTab.getAttribute('data-p2p-tab-target')
    : tabButtons[0].getAttribute('data-p2p-tab-target');

  p2pSwitchSubtab(initialTarget);
}

function p2pSetStatus(message, type) {
  var statusLine = p2pEl('statusLine');
  if (!statusLine) return;
  statusLine.textContent = message || '';
  if (type) {
    var rootStyles = getComputedStyle(document.documentElement);
    statusLine.style.color = type === 'error' ? rootStyles.getPropertyValue('--danger').trim() : (type === 'ok' ? rootStyles.getPropertyValue('--success').trim() : '');
  } else {
    statusLine.style.color = '';
  }
}

// p2pFormatDate mantida como alias para compatibilidade; use formatDate diretamente.
function p2pFormatDate(raw) { return formatDate(raw, '-'); }

function p2pEscapeHtml(text) {
  return String(text || '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/\"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function p2pRenderStatus(status) {
  var statusGrid = p2pEl('statusGrid');
  if (!statusGrid) return;
  var rows = [
    ['Ativo', String(!!status.active)],
    ['Discovery', status.discoveryMode || '-'],
    ['Peers', String(status.knownPeers || 0)],
    ['Escuta', status.listenAddress || '-'],
    ['TempDir', status.tempDir || '-'],
    ['TTL (h)', String(status.tempTtlHours || '-')],
    ['Ultima descoberta', p2pFormatDate(status.lastDiscoveryTickUtc)],
    ['Ultima limpeza', p2pFormatDate(status.lastCleanupUtc)],
    ['Erro', status.lastError || '-']
  ];
  var plan = status.currentSeedPlan || {};
  var metrics = status.metrics || {};
  rows.push(['Plano seeds', (plan.selectedSeeds || 0) + ' / ' + (plan.totalAgents || 0)]);
  rows.push(['Publicados', String(metrics.publishedArtifacts || 0)]);
  rows.push(['Replicacoes', String(metrics.replicationsSucceeded || 0) + ' ok / ' + String(metrics.replicationsFailed || 0) + ' falhas']);
  rows.push(['Fila', String(metrics.queuedReplications || 0) + ' aguardando / ' + String(metrics.activeReplications || 0) + ' ativas']);
  rows.push(['Auto sync', String(metrics.autoDistributionRuns || 0)]);
  rows.push(['Bytes P2P', formatP2PBytes(metrics.bytesServed || 0) + ' up / ' + formatP2PBytes(metrics.bytesDownloaded || 0) + ' down']);

  statusGrid.innerHTML = rows.map(function (entry) {
    return '<div class="fact"><div class="k">' + p2pEscapeHtml(entry[0]) + '</div><div class="v mono">' + p2pEscapeHtml(entry[1]) + '</div></div>';
  }).join('');
}

function p2pRenderPeers(peers) {
  var peersBody = p2pEl('peersBody');
  var auditPeerFilter = p2pEl('auditPeerFilter');

  if (peersBody) {
    if (!peers || !peers.length) {
      peersBody.innerHTML = '<tr><td colspan="4">Nenhum peer descoberto.</td></tr>';
    } else {
      peersBody.innerHTML = peers.map(function (peer) {
        var addr = (peer.address || '-') + (peer.port ? (':' + peer.port) : '');
        var agentId = (peer.agentId || '-');
        return '<tr>' +
          '<td class="mono">' + p2pEscapeHtml(agentId) + '</td>' +
          '<td class="mono">' + p2pEscapeHtml(addr) + '</td>' +
          '<td>' + p2pEscapeHtml((peer.source || '-') + ' / ' + (peer.connectedVia || '-')) + '</td>' +
          '<td style="white-space:nowrap;">' +
            '<button class="btn btn-sm" data-p2p-peer-id="' + p2pEscapeHtml(agentId) + '" title="Ver artifacts deste peer">📦</button>' +
          '</td>' +
          '</tr>';
      }).join('');

      peersBody.querySelectorAll('[data-p2p-peer-id]').forEach(function (btn) {
        btn.addEventListener('click', function () {
          var peerId = this.getAttribute('data-p2p-peer-id');
          p2pShowPeerArtifactPanel(peerId);
        });
      });
    }
  }

  if (auditPeerFilter) {
    var current = auditPeerFilter.value || 'all';
    var options = ['<option value="all">Todos os peers</option>'];
    options = options.concat((peers || []).map(function (peer) {
      var id = peer.agentId || '';
      return '<option value="' + p2pEscapeHtml(id) + '">' + p2pEscapeHtml(id || '-') + '</option>';
    }));
    auditPeerFilter.innerHTML = options.join('');
    if (Array.prototype.some.call(auditPeerFilter.options, function (opt) { return opt.value === current; })) {
      auditPeerFilter.value = current;
    }
  }
}

function p2pShowPeerArtifactPanel(peerId) {
  var panel = p2pEl('peerArtifactPanel');
  var select = p2pEl('peerArtifactSelect');
  if (!panel || !select) return;

  var peerEntry = null;
  for (var i = 0; i < p2pPeerArtifactIndex.length; i++) {
    if ((p2pPeerArtifactIndex[i].peerAgentId || '').trim() === peerId) {
      peerEntry = p2pPeerArtifactIndex[i];
      break;
    }
  }

  var artifacts = (peerEntry && peerEntry.artifacts) ? peerEntry.artifacts : [];
  if (!artifacts.length) {
    panel.classList.add('hidden');
    p2pSetStatus('Nenhum artifact disponivel neste peer.', '');
    return;
  }

  select.innerHTML = artifacts.map(function (a) {
    var name = a.artifactName || '';
    return '<option value="' + p2pEscapeHtml(name) + '">' + p2pEscapeHtml(name) + '</option>';
  }).join('');

  select.setAttribute('data-p2p-peer-id', peerId);
  panel.classList.remove('hidden');
}

function p2pRenderArtifacts(artifacts) {
  var artifactsBody = p2pEl('artifactsBody');
  var artifactSelect = p2pEl('artifactSelect');

  if (artifactsBody) {
    if (!artifacts || !artifacts.length) {
      artifactsBody.innerHTML = '<tr><td colspan="3">Nenhum artifact local.</td></tr>';
    } else {
      artifactsBody.innerHTML = artifacts.map(function (artifact) {
        var sizeBytes = Number(artifact.sizeBytes || 0);
        var sizeLabel = formatP2PBytes(sizeBytes);
        return '<tr>' +
          '<td class="mono">' + p2pEscapeHtml(artifact.artifactName || '-') + '</td>' +
          '<td title="' + p2pEscapeHtml(String(sizeBytes) + ' bytes') + '">' + p2pEscapeHtml(sizeLabel) + '</td>' +
          '<td class="mono">' + p2pEscapeHtml((artifact.checksumSha256 || '-').slice(0, 18)) + '...</td>' +
          '</tr>';
      }).join('');
    }
  }

  if (artifactSelect && !artifactSelect.options.length) {
    artifactSelect.innerHTML = '';
  }
}

function p2pRenderAudit(events) {
  var auditList = p2pEl('auditList');
  if (!auditList) return;
  if (!events || !events.length) {
    auditList.innerHTML = '<li class="p2p-audit-item p2p-audit-item-empty">Nenhuma atividade registrada.</li>';
    return;
  }

  auditList.innerHTML = events.map(function (event) {
    var badgeClass = event.success ? 'success' : 'error';
    var summary = [event.action || 'evento', event.source || '-', event.peerAgentId || '-'].join(' / ');
    var artifact = event.artifactName ? ('Artifact: ' + event.artifactName) : 'Artifact: -';
    return '<li class="p2p-audit-item">' +
      '<div class="p2p-audit-item-top">' +
      '<span class="p2p-audit-summary mono">' + p2pEscapeHtml(summary) + '</span>' +
      '<span class="p2p-audit-badge ' + badgeClass + '">' + p2pEscapeHtml(event.success ? 'ok' : 'erro') + '</span>' +
      '</div>' +
      '<div class="p2p-audit-meta">' + p2pEscapeHtml(p2pFormatDate(event.timestampUtc)) + '</div>' +
      '<div class="p2p-audit-meta">' + p2pEscapeHtml(artifact) + '</div>' +
      '<div class="p2p-audit-message">' + p2pEscapeHtml(event.message || '-') + '</div>' +
      '</li>';
  }).join('');
}

function p2pRenderAutoProvisioning(stats) {
  var statusEl = p2pEl('autoProvisioningStatus');
  var eventsEl = p2pEl('autoProvisioningEvents');
  if (!statusEl && !eventsEl) return;

  var s = stats || { enabled: false, totalProvisioned: 0, recentEvents: [] };

  if (statusEl) {
    var rows = [
      ['Ativo', s.enabled ? 'sim' : 'nao'],
      ['Agentes provisionados', String(s.totalProvisioned || 0)],
      ['Endpoint', '/p2p/config/onboard (GET)']
    ];
    statusEl.innerHTML = rows.map(function (r) {
      return '<div class="fact"><div class="k">' + p2pEscapeHtml(r[0]) + '</div><div class="v mono">' + p2pEscapeHtml(r[1]) + '</div></div>';
    }).join('');
  }

  if (eventsEl) {
    var events = s.recentEvents || [];
    if (!events.length) {
      eventsEl.innerHTML = '<div class="automation-task-card"><div class="meta">Nenhum evento de auto-provisioning registrado.</div></div>';
    } else {
      eventsEl.innerHTML = events.map(function (ev) {
        var badge = ev.success ? 'success' : 'error';
        return '<article class="automation-task-card">' +
          '<div class="automation-task-top">' +
          '<h4 class="mono">' + p2pEscapeHtml(ev.sourceAgentId || '-') + '</h4>' +
          '<span class="automation-execution-badge ' + badge + '">' + (ev.success ? 'ok' : 'erro') + '</span>' +
          '</div>' +
          '<div class="automation-task-meta">' + p2pEscapeHtml(p2pFormatDate(ev.timestampUtc)) + '</div>' +
          (ev.serverUrl ? '<div class="automation-task-desc">Servidor: ' + p2pEscapeHtml(ev.serverUrl) + '</div>' : '') +
          '<div class="meta">' + p2pEscapeHtml(ev.message || '-') + '</div>' +
          '</article>';
      }).join('');
    }
  }
}

function p2pFillConfig(cfg) {
  var enabled = p2pEl('enabled');
  var mode = p2pEl('mode');
  var ttl = p2pEl('ttl');
  var seedPercent = p2pEl('seedPercent');
  var minSeeds = p2pEl('minSeeds');
  var tokenMinutes = p2pEl('tokenMinutes');
  var sharedSecret = p2pEl('sharedSecret');

  if (enabled) enabled.value = String(!!cfg.enabled);
  if (mode) mode.value = cfg.discoveryMode || 'mdns';
  if (ttl) ttl.value = cfg.tempTtlHours || 168;
  if (seedPercent) seedPercent.value = cfg.seedPercent || 10;
  if (minSeeds) minSeeds.value = cfg.minSeeds || 2;
  if (tokenMinutes) tokenMinutes.value = cfg.authTokenRotationMinutes || 15;
  if (sharedSecret) sharedSecret.value = cfg.sharedSecret || '';
}

function p2pReadConfig() {
  var enabled = p2pEl('enabled');
  var mode = p2pEl('mode');
  var ttl = p2pEl('ttl');
  var seedPercent = p2pEl('seedPercent');
  var minSeeds = p2pEl('minSeeds');
  var tokenMinutes = p2pEl('tokenMinutes');
  var sharedSecret = p2pEl('sharedSecret');

  return {
    enabled: enabled ? enabled.value === 'true' : true,
    discoveryMode: mode ? mode.value : 'mdns',
    tempTtlHours: ttl ? Number(ttl.value || 168) : 168,
    seedPercent: seedPercent ? Number(seedPercent.value || 10) : 10,
    minSeeds: minSeeds ? Number(minSeeds.value || 2) : 2,
    authTokenRotationMinutes: tokenMinutes ? Number(tokenMinutes.value || 15) : 15,
    sharedSecret: sharedSecret ? sharedSecret.value : ''
  };
}

async function loadP2PView() {
  var p2pView = document.getElementById('p2pView');
  if (!p2pView || p2pView.classList.contains('hidden')) {
    return;
  }

  var auditAction = p2pEl('auditActionFilter');
  var auditPeer = p2pEl('auditPeerFilter');
  var auditStatus = p2pEl('auditStatusFilter');

  var results = await Promise.all([
    p2pApi().GetP2PDebugStatus(),
    p2pApi().GetP2PPeers(),
    p2pApi().GetP2PConfig(),
    p2pApi().ListP2PArtifacts(),
    p2pApi().GetP2PPeerArtifactIndex().catch(function () { return []; }),
    p2pApi().ListP2PAuditEventsFiltered(
      auditAction ? auditAction.value : 'all',
      auditPeer ? auditPeer.value : 'all',
      auditStatus ? auditStatus.value : 'all'
    ).catch(function () { return p2pApi().ListP2PAuditEvents(); }),
    p2pApi().GetAutoProvisioningStats().catch(function () { return null; })
  ]);

  p2pRenderStatus(results[0] || {});
  p2pRenderPeers(results[1] || []);
  p2pFillConfig(results[2] || {});
  p2pRenderArtifacts(results[3] || []);
  p2pPeerArtifactIndex = results[4] || [];
  p2pRenderAudit(results[5] || []);
  p2pRenderAutoProvisioning(results[6]);
  // Limpar mensagem de erro anterior em caso de sucesso
  p2pSetStatus('', '');
}

function initP2PPage() {
  p2pInitSubtabs();

  var refreshBtn = p2pEl('refreshBtn');
  var cleanupBtn = p2pEl('cleanupBtn');
  var clearAllArtifactsBtn = p2pEl('clearAllArtifactsBtn');
  var saveConfigBtn = p2pEl('saveConfigBtn');
  var publishRealArtifactBtn = p2pEl('publishRealArtifactBtn');
  var p2pRefreshPeersBtn = p2pEl('p2pRefreshPeersBtn');
  var auditActionFilter = p2pEl('auditActionFilter');
  var auditPeerFilter = p2pEl('auditPeerFilter');
  var auditStatusFilter = p2pEl('auditStatusFilter');

  if (refreshBtn) {
    refreshBtn.addEventListener('click', function () {
      loadP2PView();
      p2pSetStatus('Status atualizado.', 'ok');
    });
  }

  if (p2pRefreshPeersBtn) {
    p2pRefreshPeersBtn.addEventListener('click', function () {
      var btn = p2pRefreshPeersBtn;
      btn.disabled = true;
      p2pApi().SyncP2PBootstrapNow().then(function (msg) {
        p2pSetStatus(msg || 'Pesquisa de peers concluida.', 'ok');
        return loadP2PView();
      }).catch(function (err) {
        p2pSetStatus('Falha ao pesquisar peers: ' + (err && err.message ? err.message : String(err)), 'error');
      }).finally(function () {
        btn.disabled = false;
      });
    });
  }

  if (cleanupBtn) {
    cleanupBtn.addEventListener('click', function () {
      p2pApi().CleanupP2PTempNow().then(function (msg) {
        p2pSetStatus(msg || 'Limpeza concluida.', 'ok');
        loadP2PView();
      }).catch(function (err) {
        p2pSetStatus('Falha ao limpar cache: ' + (err && err.message ? err.message : String(err)), 'error');
      });
    });
  }

  var cleanupAllBtn = p2pEl('cleanupAllBtn');
  if (cleanupAllBtn) {
    cleanupAllBtn.addEventListener('click', function () {
      if (!window.confirm('Tem certeza que deseja limpar TODO o cache P2P (incluindo artifacts nao expirados)? Esta acao nao pode ser desfeita.')) {
        return;
      }
      p2pApi().ClearAllP2PArtifacts().then(function () {
        return p2pApi().CleanupP2PTempNow();
      }).then(function (msg) {
        p2pSetStatus('Cache P2P completamente limpo.', 'ok');
        loadP2PView();
      }).catch(function (err) {
        p2pSetStatus('Falha na limpeza total: ' + (err && err.message ? err.message : String(err)), 'error');
      });
    });
  }

  if (clearAllArtifactsBtn) {
    clearAllArtifactsBtn.addEventListener('click', function () {
      if (!window.confirm('Tem certeza que deseja apagar todos os artifacts locais? Esta acao nao pode ser desfeita.')) {
        return;
      }
      p2pApi().ClearAllP2PArtifacts().then(function (msg) {
        p2pSetStatus(msg || 'Todos os artifacts locais foram apagados.', 'ok');
        loadP2PView();
      }).catch(function (err) {
        p2pSetStatus('Falha ao apagar artifacts: ' + (err && err.message ? err.message : String(err)), 'error');
      });
    });
  }

  if (saveConfigBtn) {
    saveConfigBtn.addEventListener('click', function () {
      p2pApi().SetP2PConfig(p2pReadConfig()).then(function () {
        p2pSetStatus('Configuracao salva.', 'ok');
        loadP2PView();
      }).catch(function (err) {
        p2pSetStatus('Falha ao salvar: ' + (err && err.message ? err.message : String(err)), 'error');
      });
    });
  }

  if (publishRealArtifactBtn) {
    publishRealArtifactBtn.addEventListener('click', function () {
      p2pApi().SelectAndPublishP2PArtifact().then(function (artifact) {
        p2pSetStatus('Arquivo publicado: ' + (artifact && artifact.artifactName ? artifact.artifactName : 'selecionado'), 'ok');
        loadP2PView();
      }).catch(function (err) {
        p2pSetStatus('Falha ao publicar arquivo: ' + (err && err.message ? err.message : String(err)), 'error');
      });
    });
  }

  var peerArtifactPullBtn = p2pEl('peerArtifactPullBtn');
  if (peerArtifactPullBtn) {
    peerArtifactPullBtn.addEventListener('click', function () {
      var select = p2pEl('peerArtifactSelect');
      var artifactName = select ? select.value : '';
      var peerId = select ? select.getAttribute('data-p2p-peer-id') : '';
      if (!artifactName || !peerId) {
        p2pSetStatus('Selecione um artifact e um peer.', 'error');
        return;
      }
      p2pApi().PullP2PArtifactFromPeer(artifactName, peerId).then(function (artifact) {
        var label = artifact && artifact.artifactName ? artifact.artifactName : artifactName;
        p2pSetStatus('Artifact puxado do peer: ' + label, 'ok');
        var panel = p2pEl('peerArtifactPanel');
        if (panel) panel.classList.add('hidden');
        loadP2PView();
      }).catch(function (err) {
        p2pSetStatus('Falha no pull do peer: ' + (err && err.message ? err.message : String(err)), 'error');
      });
    });
  }

  [auditActionFilter, auditPeerFilter, auditStatusFilter].forEach(function (el) {
    if (!el) return;
    el.addEventListener('change', loadP2PView);
  });

  // Botão para abrir pasta de downloads P2P (tela principal)
  var openP2PFolderMainBtn = p2pEl('openP2PFolderMainBtn');
  if (openP2PFolderMainBtn) {
    openP2PFolderMainBtn.addEventListener('click', function () {
      p2pApi().GetP2PTempDir().then(function (dir) {
        window.runtime.BrowserOpenURL('file:///' + dir.replace(/\\/g, '/'));
      }).catch(function () {
        p2pSetStatus('Falha ao obter diretorio P2P.', 'error');
      });
    });
  }

  if (!p2pRefreshTimerId) {
    startP2PPoller();
  }
}

// ── Progresso de transferência P2P em tempo real ─────────────────────

var p2pTransferMap = {};

function formatP2PBytes(bytes) {
  if (!bytes || bytes <= 0) return "0 B";
  var units = ["B", "KB", "MB", "GB"];
  var i = Math.floor(Math.log(bytes) / Math.log(1024));
  if (i >= units.length) i = units.length - 1;
  return (bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1) + " " + units[i];
}

function renderP2PTransferList() {
  var panel = document.getElementById('p2pTransferPanel');
  var list = document.getElementById('p2pTransferList');
  if (!panel || !list) return;
  var keys = Object.keys(p2pTransferMap);
  if (keys.length === 0) {
    panel.classList.add('hidden');
    return;
  }
  panel.classList.remove('hidden');
  var html = '';
  keys.forEach(function (key) {
    var p = p2pTransferMap[key];
    var totalBytes = Number(p.totalBytes || 0);
    var bytesRead = Number(p.bytesRead || 0);
    var pct = totalBytes > 0 ? Math.round((bytesRead / totalBytes) * 100) : (p.done ? 100 : 0);
    if (pct < 0) pct = 0;
    if (pct > 100) pct = 100;
    var barColor = p.error ? 'var(--danger)' : (p.done && !p.error ? 'var(--accent)' : '#4a90d9');
    var isUpload = String(p.direction || '').toLowerCase() === 'upload' || String(p.operation || '').toLowerCase() === 'serve';
    var directionArrow = isUpload ? '→' : '←';
    var phaseLabel = isUpload ? 'enviando' : 'recebendo';
    var label = p2pEscapeHtml(p.artifactName || '?') + ' ' + directionArrow + ' ' + p2pEscapeHtml(p.peerID || '?');
    if (p.totalChunks > 0) {
      label += ' [chunk ' + (p.chunkIndex + 1) + '/' + p.totalChunks + ']';
    }
    html += '<div class="p2p-transfer-item" style="margin-bottom:8px;">' +
      '<div style="display:flex;justify-content:space-between;font-size:0.78rem;margin-bottom:3px;">' +
        '<span class="mono">' + label + ' (' + phaseLabel + ')</span>' +
        '<span style="color:var(--muted)">' + formatP2PBytes(bytesRead) + ' / ' + formatP2PBytes(totalBytes) + ' (' + pct + '%)</span>' +
      '</div>' +
      '<div style="background:var(--bg);border-radius:6px;height:8px;overflow:hidden;">' +
        '<div style="background:' + barColor + ';height:100%;width:' + pct + '%;transition:width 0.2s ease;"></div>' +
      '</div>' +
      (p.error ? '<div style="color:var(--danger);font-size:0.76rem;margin-top:2px;">' + p2pEscapeHtml(p.error) + '</div>' : '') +
      (p.done && !p.error ? '<div style="color:var(--accent);font-size:0.76rem;margin-top:2px;">✓ Concluido</div>' : '') +
    '</div>';
  });
  list.innerHTML = html;

  // Limpar concluídos após 5s
  keys.forEach(function (key) {
    var p = p2pTransferMap[key];
    if (p.done) {
      setTimeout(function () {
        delete p2pTransferMap[key];
        renderP2PTransferList();
      }, 5000);
    }
  });
}

function onP2PTransferProgress(p) {
  if (!p) return;
  var key = (p.artifactName || '?') + '|' + (p.peerID || '?') + '|' + (p.operation || '?');
  var prev = p2pTransferMap[key] || {};
  p2pTransferMap[key] = {
    artifactName: p.artifactName || prev.artifactName || '?',
    peerID: p.peerID || prev.peerID || '?',
    bytesRead: p.bytesRead != null ? p.bytesRead : (prev.bytesRead || 0),
    totalBytes: p.totalBytes != null ? p.totalBytes : (prev.totalBytes || 0),
    operation: p.operation || prev.operation || '?',
    direction: p.direction || prev.direction || (String(p.operation || prev.operation || '').toLowerCase() === 'serve' ? 'upload' : 'download'),
    chunkIndex: p.chunkIndex != null ? p.chunkIndex : (prev.chunkIndex || 0),
    totalChunks: p.totalChunks != null ? p.totalChunks : (prev.totalChunks || 0),
    done: !!p.done,
    error: p.error || ''
  };
  renderP2PTransferList();
}

// Registrar listener de evento Wails
(function () {
  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn('p2p:transfer-progress', onP2PTransferProgress);
  }
})();
