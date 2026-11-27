"use strict";

(function () {
  var allFiles = [];
  var loadedOnce = false;
  var downloadingIds = {};
  var scopeLabels = { 0: "Global", 1: "Cliente", 2: "Site" };

  function formatFileSize(bytes) {
    if (!bytes || bytes <= 0) return "0 B";
    var units = ["B", "KB", "MB", "GB"];
    var v = bytes;
    var i = 0;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return v.toFixed(i === 0 ? 0 : 1) + " " + units[i];
  }

  function renderFiles() {
    var container = document.getElementById("filesListContainer");
    if (!container) return;

    var searchEl = document.getElementById("filesSearchInput");
    var scopeEl = document.getElementById("filesScopeFilter");
    var term = (searchEl ? searchEl.value : "").trim().toLowerCase();
    var scopeVal = scopeEl ? scopeEl.value : "";

    var filtered = allFiles.filter(function (f) {
      if (scopeVal !== "" && String(f.scope) !== scopeVal) return false;
      if (term) {
        var hay = (f.name + " " + f.fileName + " " + (f.description || "")).toLowerCase();
        if (hay.indexOf(term) === -1) return false;
      }
      return true;
    });

    if (!filtered.length) {
      container.innerHTML = '<div class="empty-state meta">Nenhum arquivo disponível para este equipamento.</div>';
      return;
    }

    container.innerHTML = filtered
      .map(function (f) {
        var scopeText = scopeLabels[f.scope] || "Global";
        return (
          '<div class="card" style="padding:12px;margin-bottom:10px;display:flex;justify-content:space-between;align-items:center;gap:12px;">' +
            '<div style="min-width:0;flex:1;">' +
              '<div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">' +
                '<strong style="font-size:14px;">' + escapeHtml(f.name || f.fileName) + '</strong>' +
                '<span class="badge">' + escapeHtml(scopeText) + '</span>' +
                (f.folderPath && f.folderPath !== '/' ? '<span class="badge">' + escapeHtml(f.folderPath) + '</span>' : '') +
                (f.category ? '<span class="badge">' + escapeHtml(f.category) + '</span>' : '') +
              '</div>' +
              '<div class="meta" style="font-size:12px;margin-top:2px;">' +
                escapeHtml(f.fileName) + ' • ' + formatFileSize(f.sizeBytes) +
                (f.sha256 ? ' • SHA-256: ' + escapeHtml(f.sha256.slice(0, 12)) + '...' : '') +
              '</div>' +
              (f.description ? '<div class="meta" style="font-size:12px;margin-top:4px;">' + escapeHtml(f.description) + '</div>' : '') +
              '<div class="meta" id="fileProgress-' + escapeHtmlAttr(f.id) + '" style="font-size:12px;margin-top:4px;"></div>' +
            '</div>' +
            '<div style="display:flex;gap:6px;flex-shrink:0;">' +
              '<button type="button" class="btn btn-primary" data-file-dl="' + escapeHtmlAttr(f.id) + '" data-file-name="' + escapeHtmlAttr(f.fileName) + '">Baixar</button>' +
              '<button type="button" class="btn btn-secondary" data-file-saveas="' + escapeHtmlAttr(f.id) + '" data-file-name="' + escapeHtmlAttr(f.fileName) + '">Salvar como</button>' +
            '</div>' +
          '</div>'
        );
      })
      .join("");
  }

  async function loadAgentFiles(force) {
    if (loadedOnce && !force) return;
    var banner = document.getElementById("filesStatusBanner");
    if (banner) banner.textContent = "Carregando arquivos...";
    try {
      var list = await appApi().ListAgentFiles();
      allFiles = Array.isArray(list) ? list : [];
      loadedOnce = true;
      if (banner) banner.textContent = allFiles.length + " arquivo(s) disponíveis.";
      renderFiles();
    } catch (err) {
      if (banner) banner.textContent = "Erro ao carregar arquivos: " + (err && err.message ? err.message : err);
    }
  }

  function setFileButtonsDisabled(fileID, disabled) {
    var container = document.getElementById("filesListContainer");
    if (!container) return;
    var btns = container.querySelectorAll("button");
    for (var i = 0; i < btns.length; i++) {
      var b = btns[i];
      if (b.dataset.fileDl === fileID || b.dataset.fileSaveas === fileID) {
        b.disabled = disabled;
      }
    }
  }

  async function triggerDownload(fileID, fileName, chooseLocation) {
    if (downloadingIds[fileID]) return;
    downloadingIds[fileID] = true;
    setFileButtonsDisabled(fileID, true);
    var prog = document.getElementById("fileProgress-" + fileID);
    if (prog) prog.textContent = "Iniciando download...";
    try {
      var res = await appApi().DownloadAgentFile(fileID, fileName, !!chooseLocation);
      if (prog && res && res.savedPath) {
        prog.innerHTML =
          'Salvo em: <span class="mono">' + escapeHtml(res.savedPath) + '</span> ' +
          (res.verified ? "✓ SHA-256" : "") +
          ' <button type="button" class="btn btn-link" data-open-folder="' + escapeHtmlAttr(res.savedPath) + '">Abrir pasta</button>';
      }
      if (typeof showToast === "function") {
        showToast("Download concluído: " + fileName, "success");
      }
    } catch (err) {
      if (prog) prog.textContent = "Falha: " + (err && err.message ? err.message : err);
    } finally {
      delete downloadingIds[fileID];
      setFileButtonsDisabled(fileID, false);
    }
  }

  window.loadAgentFiles = loadAgentFiles;

  document.addEventListener("DOMContentLoaded", function () {
    var refreshBtn = document.getElementById("filesRefreshBtn");
    var searchEl = document.getElementById("filesSearchInput");
    var scopeEl = document.getElementById("filesScopeFilter");
    var container = document.getElementById("filesListContainer");

    if (refreshBtn) refreshBtn.addEventListener("click", function () { loadAgentFiles(true); });
    if (searchEl) searchEl.addEventListener("input", renderFiles);
    if (scopeEl) scopeEl.addEventListener("change", renderFiles);

    if (container) {
      container.addEventListener("click", function (ev) {
        var btn = ev.target && ev.target.closest ? ev.target.closest("button") : null;
        if (!btn) return;
        if (btn.dataset.fileDl) {
          triggerDownload(btn.dataset.fileDl, btn.dataset.fileName || "arquivo", false);
        } else if (btn.dataset.fileSaveas) {
          triggerDownload(btn.dataset.fileSaveas, btn.dataset.fileName || "arquivo", true);
        } else if (btn.dataset.openFolder) {
          appApi().OpenDownloadedFileFolder(btn.dataset.openFolder);
        }
      });
    }

    if (window.wails && window.wails.Events && typeof window.wails.Events.On === "function") {
      window.wails.Events.On("files:download-progress", function (ev) {
        var d = ev && ev.data ? ev.data : ev;
        if (!d || !d.fileId) return;
        var prog = document.getElementById("fileProgress-" + d.fileId);
        if (prog && !d.done) {
          prog.textContent = "Baixando: " + (d.percentage || 0) + "% (" + formatFileSize(d.written) + ")";
        }
      });
    }
  });
})();
