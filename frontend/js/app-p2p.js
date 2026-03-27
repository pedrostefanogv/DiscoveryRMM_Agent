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

function p2pSetStatus(message, type) {
  var statusLine = p2pEl('statusLine');
  if (!statusLine) return;
  statusLine.textContent = message || '';
  statusLine.style.color = type === 'error' ? '#9a031e' : (type === 'ok' ? '#0b6e4f' : '');
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
  rows.push(['Bytes P2P', String(metrics.bytesServed || 0) + ' up / ' + String(metrics.bytesDownloaded || 0) + ' down']);

  statusGrid.innerHTML = rows.map(function (entry) {
    return '<div class="fact"><div class="k">' + p2pEscapeHtml(entry[0]) + '</div><div class="v mono">' + p2pEscapeHtml(entry[1]) + '</div></div>';
  }).join('');
}

function p2pRenderPeers(peers) {
  var peersBody = p2pEl('peersBody');
  var peerSelect = p2pEl('peerSelect');
  var auditPeerFilter = p2pEl('auditPeerFilter');

  if (peersBody) {
    if (!peers || !peers.length) {
      peersBody.innerHTML = '<tr><td colspan="4">Nenhum peer descoberto.</td></tr>';
    } else {
      peersBody.innerHTML = peers.map(function (peer) {
        var addr = (peer.address || '-') + (peer.port ? (':' + peer.port) : '');
        return '<tr>' +
          '<td class="mono">' + p2pEscapeHtml(peer.agentId || '-') + '</td>' +
          '<td class="mono">' + p2pEscapeHtml(addr) + '</td>' +
          '<td>' + p2pEscapeHtml((peer.source || '-') + ' / ' + (peer.connectedVia || '-')) + '</td>' +
          '<td>' + p2pEscapeHtml(p2pFormatDate(peer.lastSeenUtc)) + '</td>' +
          '</tr>';
      }).join('');
    }
  }

  if (peerSelect) {
    var previous = peerSelect.value || '';
    peerSelect.innerHTML = (peers || []).map(function (peer) {
      var id = peer.agentId || '';
      var label = (peer.agentId || '-') + ' - ' + ((peer.address || '-') + (peer.port ? ':' + peer.port : ''));
      return '<option value="' + p2pEscapeHtml(id) + '">' + p2pEscapeHtml(label) + '</option>';
    }).join('');
    if (previous && Array.prototype.some.call(peerSelect.options, function (opt) { return opt.value === previous; })) {
      peerSelect.value = previous;
    }
    p2pRenderRemoteArtifactsForSelectedPeer();
  }

  if (auditPeerFilter) {
    var current = auditPeerFilter.value || 'all';
    var options = ['<option value="all">todos</option>'];
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

function p2pRenderRemoteArtifactsForSelectedPeer() {
  var artifactSelect = p2pEl('artifactSelect');
  var peerSelect = p2pEl('peerSelect');
  if (!artifactSelect || !peerSelect) return;

  var selectedPeer = (peerSelect.value || '').trim();
  var peerEntry = null;
  for (var i = 0; i < p2pPeerArtifactIndex.length; i++) {
    if ((p2pPeerArtifactIndex[i].peerAgentId || '').trim() === selectedPeer) {
      peerEntry = p2pPeerArtifactIndex[i];
      break;
    }
  }

  var artifacts = (peerEntry && peerEntry.artifacts) ? peerEntry.artifacts : [];
  artifactSelect.innerHTML = artifacts.map(function (artifact) {
    var name = artifact.artifactName || '';
    return '<option value="' + p2pEscapeHtml(name) + '">' + p2pEscapeHtml(name || '-') + '</option>';
  }).join('');
}

function p2pRenderArtifacts(artifacts) {
  var artifactsBody = p2pEl('artifactsBody');
  var artifactSelect = p2pEl('artifactSelect');

  if (artifactsBody) {
    if (!artifacts || !artifacts.length) {
      artifactsBody.innerHTML = '<tr><td colspan="3">Nenhum artifact local.</td></tr>';
    } else {
      artifactsBody.innerHTML = artifacts.map(function (artifact) {
        return '<tr>' +
          '<td class="mono">' + p2pEscapeHtml(artifact.artifactName || '-') + '</td>' +
          '<td>' + p2pEscapeHtml(String(artifact.sizeBytes || 0)) + '</td>' +
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
    auditList.innerHTML = '<div class="automation-task-card"><div class="meta">Nenhuma atividade registrada.</div></div>';
    return;
  }

  auditList.innerHTML = events.map(function (event) {
    var badgeClass = event.success ? 'success' : 'error';
    var summary = [event.action || 'evento', event.source || '-', event.peerAgentId || '-'].join(' / ');
    var artifact = event.artifactName ? ('Artifact: ' + event.artifactName) : 'Artifact: -';
    return '<article class="automation-task-card">' +
      '<div class="automation-task-top">' +
      '<h4 class="mono">' + p2pEscapeHtml(summary) + '</h4>' +
      '<span class="automation-execution-badge ' + badgeClass + '">' + p2pEscapeHtml(event.success ? 'ok' : 'erro') + '</span>' +
      '</div>' +
      '<div class="automation-task-meta">' + p2pEscapeHtml(p2pFormatDate(event.timestampUtc)) + '</div>' +
      '<div class="automation-task-desc">' + p2pEscapeHtml(artifact) + '</div>' +
      '<div class="meta">' + p2pEscapeHtml(event.message || '-') + '</div>' +
      '</article>';
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
  p2pRenderRemoteArtifactsForSelectedPeer();
  p2pRenderAudit(results[5] || []);
  p2pRenderAutoProvisioning(results[6]);
  // Limpar mensagem de erro anterior em caso de sucesso
  p2pSetStatus('', '');
}

function initP2PPage() {
  var refreshBtn = p2pEl('refreshBtn');
  var cleanupBtn = p2pEl('cleanupBtn');
  var saveConfigBtn = p2pEl('saveConfigBtn');
  var publishArtifactBtn = p2pEl('publishArtifactBtn');
  var publishRealArtifactBtn = p2pEl('publishRealArtifactBtn');
  var replicateBtn = p2pEl('replicateBtn');
  var peerSelect = p2pEl('peerSelect');
  var auditActionFilter = p2pEl('auditActionFilter');
  var auditPeerFilter = p2pEl('auditPeerFilter');
  var auditStatusFilter = p2pEl('auditStatusFilter');

  if (refreshBtn) {
    refreshBtn.addEventListener('click', function () {
      loadP2PView();
      p2pSetStatus('Status atualizado.', 'ok');
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

  if (publishArtifactBtn) {
    publishArtifactBtn.addEventListener('click', function () {
      var artifactName = p2pEl('artifactName');
      var artifactContent = p2pEl('artifactContent');
      var name = artifactName ? artifactName.value.trim() : '';
      var content = artifactContent ? artifactContent.value : '';
      p2pApi().PublishP2PTestArtifact(name, content).then(function (artifact) {
        p2pSetStatus('Artifact publicado: ' + (artifact && artifact.artifactName ? artifact.artifactName : name), 'ok');
        if (artifactName) artifactName.value = '';
        if (artifactContent) artifactContent.value = '';
        loadP2PView();
      }).catch(function (err) {
        p2pSetStatus('Falha ao publicar artifact: ' + (err && err.message ? err.message : String(err)), 'error');
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

  if (replicateBtn) {
    replicateBtn.addEventListener('click', function () {
      var artifactSelect = p2pEl('artifactSelect');
      var peerSelect = p2pEl('peerSelect');
      var artifactName = artifactSelect ? artifactSelect.value : '';
      var peerID = peerSelect ? peerSelect.value : '';
      p2pApi().PullP2PArtifactFromPeer(artifactName, peerID).then(function (artifact) {
        var label = artifact && artifact.artifactName ? artifact.artifactName : artifactName;
        p2pSetStatus('Artifact baixado do peer: ' + label, 'ok');
        loadP2PView();
      }).catch(function (err) {
        p2pSetStatus('Falha no pull do peer: ' + (err && err.message ? err.message : String(err)), 'error');
      });
    });
  }

  if (peerSelect) {
    peerSelect.addEventListener('change', p2pRenderRemoteArtifactsForSelectedPeer);
  }

  [auditActionFilter, auditPeerFilter, auditStatusFilter].forEach(function (el) {
    if (!el) return;
    el.addEventListener('change', loadP2PView);
  });

  if (!p2pRefreshTimerId) {
    startP2PPoller();
  }
}
