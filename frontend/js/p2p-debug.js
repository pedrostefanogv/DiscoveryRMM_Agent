"use strict";

(function () {
  function appApi() {
    if (!window.go || !window.go.main || !window.go.main.App) {
      throw new Error("API do Wails indisponivel");
    }
    return window.go.main.App;
  }

  var refreshBtn = document.getElementById("refreshBtn");
  var cleanupBtn = document.getElementById("cleanupBtn");
  var saveConfigBtn = document.getElementById("saveConfigBtn");
  var statusGrid = document.getElementById("statusGrid");
  var peersBody = document.getElementById("peersBody");
  var artifactsBody = document.getElementById("artifactsBody");
  var statusLine = document.getElementById("statusLine");
  var artifactSelectEl = document.getElementById("artifactSelect");
  var peerSelectEl = document.getElementById("peerSelect");
  var artifactNameEl = document.getElementById("artifactName");
  var artifactContentEl = document.getElementById("artifactContent");
  var publishArtifactBtn = document.getElementById("publishArtifactBtn");
  var publishRealArtifactBtn = document.getElementById("publishRealArtifactBtn");
  var replicateBtn = document.getElementById("replicateBtn");

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
    rows.push(["Bytes P2P", String(metrics.bytesServed || 0) + " up / " + String(metrics.bytesDownloaded || 0) + " down"]);

    statusGrid.innerHTML = rows.map(function (entry) {
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
      return "<tr>" +
        "<td class=\"mono\">" + escapeHtml(peer.agentId || "-") + "</td>" +
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
  }

  function renderArtifacts(artifacts) {
    if (!artifactsBody) return;
    if (!artifacts || !artifacts.length) {
      artifactsBody.innerHTML = '<tr><td colspan="3">Nenhum artifact local.</td></tr>';
      if (artifactSelectEl) artifactSelectEl.innerHTML = '<option value="">Nenhum</option>';
      return;
    }

    artifactsBody.innerHTML = artifacts.map(function (artifact) {
      return '<tr>' +
        '<td class="mono">' + escapeHtml(artifact.artifactName || '-') + '</td>' +
        '<td>' + escapeHtml(String(artifact.sizeBytes || 0)) + '</td>' +
        '<td class="mono">' + escapeHtml((artifact.checksumSha256 || '-').slice(0, 18)) + '...</td>' +
        '</tr>';
    }).join('');

    if (artifactSelectEl) {
      artifactSelectEl.innerHTML = artifacts.map(function (artifact) {
        return '<option value="' + escapeHtml(artifact.artifactName || '') + '">' + escapeHtml(artifact.artifactName || '-') + '</option>';
      }).join('');
    }
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
    Promise.all([
      appApi().GetP2PDebugStatus(),
      appApi().GetP2PPeers(),
      appApi().GetP2PConfig(),
      appApi().ListP2PArtifacts()
    ]).then(function (results) {
      renderStatus(results[0] || {});
      renderPeers(results[1] || []);
      fillConfig(results[2] || {});
      renderArtifacts(results[3] || []);
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

  refreshAll();
  setInterval(function () {
    if (!document.hidden) {
      refreshAll();
    }
  }, 5000);
})();
