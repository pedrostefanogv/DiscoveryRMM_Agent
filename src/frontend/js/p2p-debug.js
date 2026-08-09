"use strict";

(function () {
  function appApi() {
    if (!window.go || !window.go.app || !window.go.app.App) {
      throw new Error("API do Wails indisponivel");
    }
    return window.go.app.App;
  }

  var refreshBtn = document.getElementById("refreshBtn");
  var bootstrapSyncBtn = document.getElementById("bootstrapSyncBtn");
  var cleanupBtn = document.getElementById("cleanupBtn");
  var clearAllArtifactsBtn = document.getElementById("clearAllArtifactsBtn");
  var saveConfigBtn = document.getElementById("saveConfigBtn");
  var statusGrid = document.getElementById("statusGrid");
  var serviceHealthGrid = document.getElementById("serviceHealthGrid");
  var peersBody = document.getElementById("peersBody");
  var artifactsBody = document.getElementById("artifactsBody");
  var auditList = document.getElementById("auditList");
  var statusLine = document.getElementById("statusLine");
  var transferProgressPanel = document.getElementById("transferProgressPanel");
  var transferProgressList = document.getElementById("transferProgressList");
  var artifactSelectEl = document.getElementById("artifactSelect");
  var peerSelectEl = document.getElementById("peerSelect");
  var artifactNameEl = document.getElementById("artifactName");
  var artifactContentEl = document.getElementById("artifactContent");
  var publishArtifactBtn = document.getElementById("publishArtifactBtn");
  var publishRealArtifactBtn = document.getElementById("publishRealArtifactBtn");
  var replicateBtn = document.getElementById("replicateBtn");
  var auditActionFilterEl = document.getElementById("auditActionFilter");
  var auditPeerFilterEl = document.getElementById("auditPeerFilter");
  var auditStatusFilterEl = document.getElementById("auditStatusFilter");

  var enabledEl = document.getElementById("enabled");
  var modeEl = document.getElementById("mode");
  var ttlEl = document.getElementById("ttl");
  var seedPercentEl = document.getElementById("seedPercent");
  var minSeedsEl = document.getElementById("minSeeds");
  var tokenMinutesEl = document.getElementById("tokenMinutes");
  var sharedSecretEl = document.getElementById("sharedSecret");

  function setStatus(message, type) {
    if (!statusLine) return;
    statusLine.textContent = message || "";
    statusLine.className = "status-line" + (type ? " " + type : "");
  }

  function formatDate(raw) {
    if (!raw) return "-";
    var d = new Date(raw);
    if (isNaN(d.getTime())) return raw;
    return d.toLocaleString("pt-BR");
  }

  function renderStatus(status) {
    if (!statusGrid) return;
    var rows = [
      ["Ativo", String(!!status.active)],
      ["Discovery", status.discoveryMode || "-"],
      ["Peers", String(status.knownPeers || 0)],
      ["Escuta", status.listenAddress || "-"],
      ["TempDir", status.tempDir || "-"],
      ["TTL (h)", String(status.tempTtlHours || "-")],
      ["Ultima descoberta", formatDate(status.lastDiscoveryTickUtc)],
      ["Ultima limpeza", formatDate(status.lastCleanupUtc)],
      ["Erro", status.lastError || "-"]
    ];
    var plan = status.currentSeedPlan || {};
    var metrics = status.metrics || {};
    rows.push(["Plano seeds", (plan.selectedSeeds || 0) + " / " + (plan.totalAgents || 0)]);
    rows.push(["Publicados", String(metrics.publishedArtifacts || 0)]);
    rows.push(["Replicacoes", String(metrics.replicationsSucceeded || 0) + " ok / " + String(metrics.replicationsFailed || 0) + " falhas"]);
    rows.push(["Fila", String(metrics.queuedReplications || 0) + " aguardando / " + String(metrics.activeReplications || 0) + " ativas"]);
    rows.push(["Auto sync", String(metrics.autoDistributionRuns || 0)]);
    rows.push(["Bytes P2P", String(metrics.bytesServed || 0) + " up / " + String(metrics.bytesDownloaded || 0) + " down"]);

    statusGrid.innerHTML = rows.map(function (entry) {
      return '<div class="kv"><span class="k">' + escapeHtml(entry[0]) + '</span><span class="v mono">' + escapeHtml(entry[1]) + '</span></div>';
    }).join("");

	if (bootstrapSyncBtn) {
	  bootstrapSyncBtn.disabled = !status.active;
	  bootstrapSyncBtn.title = status.active ? "" : "P2P local inativo nesta execução";
	}
  }

  function renderServiceHealth(health) {
    if (!serviceHealthGrid) return;

    if (!health || health.error) {
      serviceHealthGrid.innerHTML = '<div class="kv"><span class="k">Status</span><span class="v" style="color: var(--danger);">Indisponível (' + escapeHtml(health && health.error ? health.error : "desconectado") + ')</span></div>';
      return;
    }

    var rows = [
      ["Rodando", String(!!health.running)],
      ["Verificado em", formatDate(health.checked_at)],
      ["Componentes", String(health.component_count || 0)],
      ["Recuperáveis", String(health.recoverable_count || 0)],
      ["Degradados", String(health.degraded_count || 0)],
      ["Não saudáveis", String(health.unhealthy_count || 0)]
    ];

    if (health.components && Array.isArray(health.components) && health.components.length > 0) {
      rows.push(["Detalhes de componentes:"]);
      health.components.forEach(function (comp) {
        var compStatus = (comp.status || "").toUpperCase();
        var icon = comp.recoverable ? "⚠" : (compStatus === "HEALTHY" ? "✓" : "✗");
        rows.push([
          "  " + escapeHtml(String(comp.component || "-")),
          icon + " " + escapeHtml(compStatus)
        ]);
      });
    }

    serviceHealthGrid.innerHTML = rows.map(function (entry) {
      return '<div class="kv"><span class="k">' + escapeHtml(entry[0]) + '</span><span class="v mono">' + escapeHtml(entry[1]) + '</span></div>';
    }).join("");
  }

  function renderPeers(peers) {
    if (!peersBody) return;
    if (!peers || !peers.length) {
      peersBody.innerHTML = '<tr><td colspan="4">Nenhum peer descoberto.</td></tr>';
      return;
    }

    peersBody.innerHTML = peers.map(function (peer) {
      var addr = (peer.address || "-") + (peer.port ? (":" + peer.port) : "");
      var displayName = (peer.host || peer.agentId || "-");
      return "<tr>" +
        "<td class=\"mono\" title=\"" + escapeHtml(peer.agentId || "") + "\">" + escapeHtml(displayName) + "</td>" +
        "<td class=\"mono\">" + escapeHtml(addr) + "</td>" +
        "<td>" + escapeHtml((peer.source || "-") + " / " + (peer.connectedVia || "-")) + "</td>" +
        "<td>" + escapeHtml(formatDate(peer.lastSeenUtc)) + "</td>" +
        "</tr>";
    }).join("");

    if (peerSelectEl) {
      peerSelectEl.innerHTML = peers.map(function (peer) {
        return '<option value="' + escapeHtml(peer.agentId || '') + '">' + escapeHtml((peer.agentId || '-') + ' - ' + ((peer.address || '-') + (peer.port ? ':' + peer.port : ''))) + '</option>';
      }).join("");
    }

    if (auditPeerFilterEl) {
      var current = auditPeerFilterEl.value || "all";
      var options = ['<option value="all">todos</option>'];
      options = options.concat(peers.map(function (peer) {
        var id = peer.agentId || '';
        return '<option value="' + escapeHtml(id) + '">' + escapeHtml(id || '-') + '</option>';
      }));
      auditPeerFilterEl.innerHTML = options.join("");
      if (Array.prototype.some.call(auditPeerFilterEl.options, function (opt) { return opt.value === current; })) {
        auditPeerFilterEl.value = current;
      }
    }
  }

  function renderArtifacts(artifacts) {
    if (!artifactsBody) return;
    if (!artifacts || !artifacts.length) {
      artifactsBody.innerHTML = '<tr><td colspan="4">Nenhum artifact local.</td></tr>';
      if (artifactSelectEl) artifactSelectEl.innerHTML = '<option value="">Nenhum</option>';
      return;
    }

    artifactsBody.innerHTML = artifacts.map(function (artifact) {
      var name = artifact.artifactName || '-';
      return '<tr>' +
        '<td class="mono">' + escapeHtml(name) + '</td>' +
        '<td>' + escapeHtml(String(artifact.sizeBytes || 0)) + '</td>' +
        '<td class="mono">' + escapeHtml((artifact.checksumSha256 || '-').slice(0, 18)) + '...</td>' +
        '<td style="white-space:nowrap;"><button class="btn btn-sm danger" data-p2p-delete-debug-artifact="' + escapeHtml(name) + '" title="Apagar este artifact">🗑️</button></td>' +
        '</tr>';
    }).join('');

    artifactsBody.querySelectorAll('[data-p2p-delete-debug-artifact]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var artifactName = this.getAttribute('data-p2p-delete-debug-artifact');
        if (!window.confirm('Apagar o artifact "' + artifactName + '"?')) return;
        appApi().DeleteP2PArtifact(artifactName).then(function (msg) {
          setStatus(msg || 'Artifact apagado.', 'ok');
          refreshAll();
        }).catch(function (err) {
          setStatus('Erro ao apagar: ' + (err && err.message ? err.message : String(err)), 'error');
        });
      });
    });

    if (artifactSelectEl) {
      artifactSelectEl.innerHTML = artifacts.map(function (artifact) {
        return '<option value="' + escapeHtml(artifact.artifactName || '') + '">' + escapeHtml(artifact.artifactName || '-') + '</option>';
      }).join('');
    }
  }

  function renderAudit(events) {
    if (!auditList) return;
    if (!events || !events.length) {
      auditList.innerHTML = '<div class="audit-item"><div class="audit-head"><span>Sem eventos</span><span>-</span></div><div>Nenhuma atividade registrada.</div></div>';
      return;
    }

    auditList.innerHTML = events.map(function (event) {
      var itemClass = event.success ? "audit-item ok" : "audit-item error";
      var summary = [event.action || "evento", event.source || "-", event.peerAgentId || "-"].join(" / ");
      var artifact = event.artifactName ? ("Artifact: " + event.artifactName) : "Artifact: -";
      return '<div class="' + itemClass + '">' +
        '<div class="audit-head"><span class="mono">' + escapeHtml(summary) + '</span><span>' + escapeHtml(formatDate(event.timestampUtc)) + '</span></div>' +
        '<div>' + escapeHtml(artifact) + '</div>' +
        '<div>' + escapeHtml(event.message || '-') + '</div>' +
        '</div>';
    }).join('');
  }

  function fillConfig(cfg) {
    if (!cfg) return;
    if (enabledEl) enabledEl.value = String(!!cfg.enabled);
    if (modeEl) modeEl.value = cfg.discoveryMode || "mdns";
    if (ttlEl) ttlEl.value = cfg.tempTtlHours || 168;
    if (seedPercentEl) seedPercentEl.value = cfg.seedPercent || 10;
    if (minSeedsEl) minSeedsEl.value = cfg.minSeeds || 2;
    if (tokenMinutesEl) tokenMinutesEl.value = cfg.authTokenRotationMinutes || 15;
    if (sharedSecretEl) sharedSecretEl.value = cfg.sharedSecret || "";
  }

  function readConfig() {
    return {
      enabled: enabledEl ? enabledEl.value === "true" : true,
      discoveryMode: modeEl ? modeEl.value : "mdns",
      tempTtlHours: ttlEl ? Number(ttlEl.value || 168) : 168,
      seedPercent: seedPercentEl ? Number(seedPercentEl.value || 10) : 10,
      minSeeds: minSeedsEl ? Number(minSeedsEl.value || 2) : 2,
      authTokenRotationMinutes: tokenMinutesEl ? Number(tokenMinutesEl.value || 15) : 15,
      sharedSecret: sharedSecretEl ? sharedSecretEl.value : ""
    };
  }

  function refreshAll() {
    var auditAction = auditActionFilterEl ? auditActionFilterEl.value : "all";
    var auditPeer = auditPeerFilterEl ? auditPeerFilterEl.value : "all";
    var auditStatus = auditStatusFilterEl ? auditStatusFilterEl.value : "all";

    Promise.all([
      appApi().GetP2PDebugStatus(),
      appApi().GetP2PPeers(),
      appApi().GetP2PConfig(),
      appApi().ListP2PArtifacts(),
      appApi().ListP2PAuditEventsFiltered(auditAction, auditPeer, auditStatus).catch(function () {
        return appApi().ListP2PAuditEvents();
      }),
      appApi().GetServiceHealth().catch(function () {
        return { error: "Service desconectado" };
      })
    ]).then(function (results) {
      renderStatus(results[0] || {});
      renderPeers(results[1] || []);
      fillConfig(results[2] || {});
      renderArtifacts(results[3] || []);
      renderAudit(results[4] || []);
      renderServiceHealth(results[5] || { error: "resposta inválida" });
    }).catch(function (err) {
      setStatus("Falha ao atualizar: " + (err && err.message ? err.message : String(err)), "error");
    });
  }

  function escapeHtml(text) {
    return String(text || "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/\"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  if (refreshBtn) {
    refreshBtn.addEventListener("click", function () {
      refreshAll();
      setStatus("Status atualizado.", "ok");
    });
  }

  if (bootstrapSyncBtn) {
	bootstrapSyncBtn.addEventListener("click", function () {
	  bootstrapSyncBtn.disabled = true;
	  setStatus("Sincronizando bootstrap P2P...", "");
	  appApi().SyncP2PBootstrapNow().then(function (msg) {
		setStatus(msg || "Sincronizacao de bootstrap P2P concluida.", "ok");
		refreshAll();
	  }).catch(function (err) {
		setStatus("Falha ao sincronizar bootstrap P2P: " + (err && err.message ? err.message : String(err)), "error");
	  }).finally(function () {
		bootstrapSyncBtn.disabled = false;
	  });
	});
  }

  if (cleanupBtn) {
    cleanupBtn.addEventListener("click", function () {
      appApi().CleanupP2PTempNow().then(function (msg) {
        setStatus(msg || "Limpeza concluida.", "ok");
        refreshAll();
      }).catch(function (err) {
        setStatus("Falha ao limpar cache: " + (err && err.message ? err.message : String(err)), "error");
      });
    });
  }

  if (clearAllArtifactsBtn) {
    clearAllArtifactsBtn.addEventListener("click", function () {
      if (!window.confirm("Tem certeza que deseja apagar todos os artifacts locais? Esta acao nao pode ser desfeita.")) {
        return;
      }
      appApi().ClearAllP2PArtifacts().then(function (msg) {
        setStatus(msg || "Todos os artifacts locais foram apagados.", "ok");
        refreshAll();
      }).catch(function (err) {
        setStatus("Falha ao apagar artifacts: " + (err && err.message ? err.message : String(err)), "error");
      });
    });
  }

  if (saveConfigBtn) {
    saveConfigBtn.addEventListener("click", function () {
      var cfg = readConfig();
      appApi().SetP2PConfig(cfg).then(function () {
        setStatus("Configuracao salva.", "ok");
        refreshAll();
      }).catch(function (err) {
        setStatus("Falha ao salvar: " + (err && err.message ? err.message : String(err)), "error");
      });
    });
  }

  if (publishArtifactBtn) {
    publishArtifactBtn.addEventListener("click", function () {
      var name = artifactNameEl ? artifactNameEl.value.trim() : "";
      var content = artifactContentEl ? artifactContentEl.value : "";
      appApi().PublishP2PTestArtifact(name, content).then(function (artifact) {
        setStatus("Artifact publicado: " + (artifact && artifact.artifactName ? artifact.artifactName : name), "ok");
        if (artifactNameEl) artifactNameEl.value = "";
        if (artifactContentEl) artifactContentEl.value = "";
        refreshAll();
      }).catch(function (err) {
        setStatus("Falha ao publicar artifact: " + (err && err.message ? err.message : String(err)), "error");
      });
    });
  }

  if (publishRealArtifactBtn) {
    publishRealArtifactBtn.addEventListener("click", function () {
      appApi().SelectAndPublishP2PArtifact().then(function (artifact) {
        setStatus("Arquivo publicado: " + (artifact && artifact.artifactName ? artifact.artifactName : "selecionado"), "ok");
        refreshAll();
      }).catch(function (err) {
        setStatus("Falha ao publicar arquivo: " + (err && err.message ? err.message : String(err)), "error");
      });
    });
  }

  if (replicateBtn) {
    replicateBtn.addEventListener("click", function () {
      var artifactName = artifactSelectEl ? artifactSelectEl.value : "";
      var peerID = peerSelectEl ? peerSelectEl.value : "";
      appApi().ReplicateP2PArtifactToPeer(artifactName, peerID).then(function (msg) {
        setStatus(msg || "Replicacao concluida.", "ok");
        refreshAll();
      }).catch(function (err) {
        setStatus("Falha na replicacao: " + (err && err.message ? err.message : String(err)), "error");
      });
    });
  }

  if (auditActionFilterEl) {
    auditActionFilterEl.addEventListener("change", refreshAll);
  }
  if (auditPeerFilterEl) {
    auditPeerFilterEl.addEventListener("change", refreshAll);
  }
  if (auditStatusFilterEl) {
    auditStatusFilterEl.addEventListener("change", refreshAll);
  }

  // Botão para abrir pasta de downloads P2P
  var openP2PFolderBtn = document.getElementById('openP2PFolderBtn');
  if (openP2PFolderBtn) {
    openP2PFolderBtn.addEventListener('click', function () {
      appApi().GetP2PTempDir().then(function (dir) {
        if (window.wails && typeof window.wails.openURL === 'function') {
          window.wails.openURL('file:///' + dir.replace(/\\/g, '/'));
        }
      }).catch(function () {
        setStatus('Falha ao obter diretorio P2P.', 'error');
      });
    });
  }

  refreshAll();
  setInterval(function () {
    if (!document.hidden) {
      refreshAll();
    }
  }, 5000);

  // ── Transferência P2P em tempo real ────────────────────────────────────

  var transferProgressMap = {};
  var transferProgressTimers = {}; // timeout IDs por key, para evitar flicker

  function formatBytes(bytes) {
    if (!bytes || bytes <= 0) return "0 B";
    var units = ["B", "KB", "MB", "GB"];
    var i = Math.floor(Math.log(bytes) / Math.log(1024));
    if (i >= units.length) i = units.length - 1;
    return (bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1) + " " + units[i];
  }

  function renderTransferProgressList() {
    if (!transferProgressPanel || !transferProgressList) return;
    var keys = Object.keys(transferProgressMap);
    if (keys.length === 0) {
      transferProgressPanel.classList.add("hidden");
      return;
    }
    transferProgressPanel.classList.remove("hidden");
    var html = "";
    keys.forEach(function (key) {
      var p = transferProgressMap[key];
      var pct = p.totalBytes > 0 ? Math.round((p.bytesRead / p.totalBytes) * 100) : 0;
      var barColor = p.error ? "var(--danger)" : (p.done && !p.error ? "var(--accent)" : "#4a90d9");
      var directionArrow = String(p.direction || "").toLowerCase() === "upload" ? "→" : "←";
      var label = escapeHtml(p.artifactName) + " " + directionArrow + " " + escapeHtml(p.peerID);
      var subLabel = "";
      if (p.phase) {
        var phaseMap = { "assembling": "remontando", "verifying": "verificando" };
        subLabel = " (" + (phaseMap[p.phase] || p.phase) + ")";
      } else if (p.totalChunks > 0) {
        subLabel = " [chunk " + (p.completedChunks || 0) + "/" + p.totalChunks + "]";
      }
      var statusText = formatBytes(p.bytesRead) + " / " + formatBytes(p.totalBytes) + " (" + pct + "%)";
      html += '<div class="transfer-item" style="margin-bottom:8px;">' +
        '<div style="display:flex;justify-content:space-between;font-size:0.78rem;margin-bottom:3px;">' +
          '<span class="mono">' + label + subLabel + '</span>' +
          '<span style="color:var(--muted)">' + statusText + '</span>' +
        '</div>' +
        (!p.phase ? (
          '<div style="background:var(--bg);border-radius:6px;height:8px;overflow:hidden;">' +
            '<div style="background:' + barColor + ';height:100%;width:' + pct + '%;transition:width 0.2s ease;"></div>' +
          '</div>'
        ) : (
          '<div style="color:var(--muted);font-size:0.74rem;margin-top:2px;">⏳ Processando...</div>'
        )) +
        (p.error ? '<div style="color:var(--danger);font-size:0.76rem;margin-top:2px;">' + escapeHtml(p.error) + '</div>' : '') +
        (p.done && !p.error ? '<div style="color:var(--accent);font-size:0.76rem;margin-top:2px;">✓ Concluido</div>' : '') +
      '</div>';

      // Agenda remoção apenas para entradas concluídas/erro, evitando
      // múltiplos timers acumulados (cancela timer anterior primeiro).
      if (p.done) {
        clearTimeout(transferProgressTimers[key]);
        transferProgressTimers[key] = setTimeout(function () {
          // Só remove se a entrada ainda estiver como done (não foi reaberta
          // por um novo chunk da mesma transferência).
          var entry = transferProgressMap[key];
          if (entry && entry.done) {
            delete transferProgressMap[key];
            delete transferProgressTimers[key];
            renderTransferProgressList();
          }
        }, 10000);
      } else {
        // Transferência ainda em andamento: cancela qualquer timer pendente
        // para que a entrada não desapareça enquanto há chunks sendo enviados.
        clearTimeout(transferProgressTimers[key]);
        delete transferProgressTimers[key];
      }
    });
    transferProgressList.innerHTML = html;
  }

  function onTransferProgress(p) {
    if (!p) return;
    var key = (p.artifactName || "?") + "|" + (p.peerID || "?") + "|" + (p.operation || "?");
    transferProgressMap[key] = p;
    renderTransferProgressList();
  }

  if (window.wails && typeof window.wails.on === 'function') {
    window.wails.on("p2p:transfer-progress", onTransferProgress);
  }
})();
