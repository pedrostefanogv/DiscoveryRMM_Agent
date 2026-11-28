"use strict";

(function () {
  var allFiles = [];
  var loadedOnce = false;
  var downloadingIds = {};
  var progressStates = {};

  var scopeMeta = {
    0: { label: "Global", cls: "file-scope-global" },
    1: { label: "Cliente", cls: "file-scope-client" },
    2: { label: "Site", cls: "file-scope-site" },
    "0": { label: "Global", cls: "file-scope-global" },
    "1": { label: "Cliente", cls: "file-scope-client" },
    "2": { label: "Site", cls: "file-scope-site" },
    "Global": { label: "Global", cls: "file-scope-global" },
    "Client": { label: "Cliente", cls: "file-scope-client" },
    "Site": { label: "Site", cls: "file-scope-site" }
  };

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

  function fileTypeIconSvg(fileName) {
    var ext = "";
    if (fileName && fileName.lastIndexOf(".") !== -1) {
      ext = fileName.slice(fileName.lastIndexOf(".") + 1).toLowerCase();
    }
    if (ext === "exe" || ext === "msi" || ext === "msix") {
      return '<svg viewBox="0 -960 960 960" width="20" height="20" fill="currentColor" aria-hidden="true" focusable="false"><path d="M440-183v-274L200-596v274l240 139Zm80 0 240-139v-274L520-457v274Zm-40-343 237-137-237-137-237 137 237 137ZM160-252q-19-11-29.5-29T120-321v-318q0-22 10.5-40t29.5-29l280-161q19-11 40-11t40 11l280 161q19 11 29.5 29t10.5 40v318q0 22-10.5 40T800-252L520-91q-19 11-40 11t-40-11L160-252Z"/></svg>';
    }
    if (ext === "zip" || ext === "7z" || ext === "rar" || ext === "tar" || ext === "gz") {
      return '<svg viewBox="0 -960 960 960" width="20" height="20" fill="currentColor" aria-hidden="true" focusable="false"><path d="M160-160q-33 0-56.5-23.5T80-240v-480q0-33 23.5-56.5T160-800h240l80 80h320q33 0 56.5 23.5T880-640v400q0 33-23.5 56.5T800-160H160Zm0-80h640v-400H447l-80-80H160v480Zm280-120h80v-80h-80v80Zm0-120h80v-80h-80v80Zm0-120h80v-80h-80v80Z"/></svg>';
    }
    if (ext === "ps1" || ext === "bat" || ext === "cmd" || ext === "sh") {
      return '<svg viewBox="0 -960 960 960" width="20" height="20" fill="currentColor" aria-hidden="true" focusable="false"><path d="M160-160q-33 0-56.5-23.5T80-240v-480q0-33 23.5-56.5T160-800h640q33 0 56.5 23.5T880-720v480q0 33-23.5 56.5T800-160H160Zm0-80h640v-400H160v400Zm140-40-56-56 103-104-104-104 57-56 160 160-160 160Zm180 0v-80h240v80H480Z"/></svg>';
    }
    return '<svg viewBox="0 -960 960 960" width="20" height="20" fill="currentColor" aria-hidden="true" focusable="false"><path d="M240-80q-33 0-56.5-23.5T160-160v-640q0-33 23.5-56.5T240-880h320l240 240v480q0 33-23.5 56.5T720-80H240Zm280-520v-200H240v640h480v-440H520Z"/></svg>';
  }

  function populateFolderFilter() {
    var folderEl = document.getElementById("filesFolderFilter");
    if (!folderEl) return;
    var prev = folderEl.value;
    var seen = {};
    var folders = [];
    for (var i = 0; i < allFiles.length; i++) {
      var fp = (allFiles[i].folderPath || "").trim();
      if (fp && fp !== "/" && !seen[fp]) {
        seen[fp] = true;
        folders.push(fp);
      }
    }
    folders.sort(function (a, b) { return a.localeCompare(b); });
    folderEl.innerHTML = '<option value="">Todas as pastas</option>' +
      folders.map(function (fp) {
        return '<option value="' + escapeHtmlAttr(fp) + '">' + escapeHtml(fp) + "</option>";
      }).join("");
    if (folders.length === 0) {
      folderEl.classList.add("hidden");
      folderEl.value = "";
    } else {
      folderEl.classList.remove("hidden");
      folderEl.value = seen[prev] ? prev : "";
    }
  }

  function buildProgressMarkup(f) {
    var st = progressStates[f.id];
    if (!st) return "";
    var labelName = escapeHtmlAttr(f.name || f.fileName || "arquivo");
    if (st.kind === "loading") {
      var barAttrs = st.determinate
        ? ' value="' + Number(st.percentage || 0) + '" max="100"'
        : "";
      return (
        '<progress class="file-progress-bar"' + barAttrs +
        ' aria-label="Progresso do download de ' + labelName + '"></progress>' +
        '<div class="file-progress-text">' + escapeHtml(st.text || "Preparando download...") + "</div>"
      );
    }
    if (st.kind === "done") {
      return (
        '<div class="file-progress-text">' +
          '<span class="file-progress-success">' +
            (st.verified ? "✓ Concluído e verificado (SHA-256)" : "✓ Download concluído") +
          "</span>" +
          '<span class="mono">' + escapeHtml(st.savedPath || "") + "</span>" +
          '<button type="button" class="btn subtle btn-sm" data-open-folder="' + escapeHtmlAttr(st.savedPath || "") + '">Abrir pasta</button>' +
        "</div>"
      );
    }
    if (st.kind === "error") {
      return '<div class="file-progress-text file-progress-error">' + escapeHtml(st.text || "Falha no download") + "</div>";
    }
    return "";
  }

  function updateCountPill(filteredCount, totalCount) {
    var pill = document.getElementById("filesCountPill");
    if (!pill) return;
    if (!loadedOnce) {
      pill.classList.add("hidden");
      return;
    }
    pill.textContent = filteredCount === totalCount
      ? String(totalCount)
      : filteredCount + " / " + totalCount;
    pill.classList.remove("hidden");
  }

  function renderLoadingSkeleton() {
    var container = document.getElementById("filesListContainer");
    if (!container) return;
    container.innerHTML =
      '<li class="files-skeleton-card" aria-hidden="true"></li>' +
      '<li class="files-skeleton-card" aria-hidden="true"></li>' +
      '<li class="files-skeleton-card" aria-hidden="true"></li>';
  }

  function showErrorBanner(msg) {
    var errEl = document.getElementById("filesErrorBanner");
    if (!errEl) return;
    if (!msg) {
      errEl.classList.add("hidden");
      errEl.innerHTML = "";
      return;
    }
    errEl.innerHTML =
      "<span>" + escapeHtml(msg) + "</span>" +
      '<button type="button" class="btn subtle btn-sm" id="filesRetryBtn">Tentar novamente</button>';
    errEl.classList.remove("hidden");
    var retryBtn = document.getElementById("filesRetryBtn");
    if (retryBtn) {
      retryBtn.addEventListener("click", function () { loadAgentFiles(true); });
    }
  }

  function renderFiles() {
    var container = document.getElementById("filesListContainer");
    if (!container) return;

    var searchEl = document.getElementById("filesSearchInput");
    var scopeEl = document.getElementById("filesScopeFilter");
    var folderEl = document.getElementById("filesFolderFilter");
    var sortEl = document.getElementById("filesSortSelect");

    var term = (searchEl ? searchEl.value : "").trim().toLowerCase();
    var scopeVal = scopeEl ? scopeEl.value : "";
    var folderVal = folderEl ? folderEl.value : "";
    var sortVal = sortEl ? sortEl.value : "name_asc";

    var filtered = allFiles.filter(function (f) {
      if (scopeVal !== "" && String(f.scope) !== scopeVal) return false;
      if (folderVal !== "" && (f.folderPath || "") !== folderVal) return false;
      if (term) {
        var hay = (
          (f.name || "") + " " +
          (f.fileName || "") + " " +
          (f.description || "") + " " +
          (f.category || "") + " " +
          (f.folderPath || "")
        ).toLowerCase();
        if (hay.indexOf(term) === -1) return false;
      }
      return true;
    });

    filtered.sort(function (a, b) {
      if (sortVal === "size_desc") return (b.sizeBytes || 0) - (a.sizeBytes || 0);
      if (sortVal === "size_asc") return (a.sizeBytes || 0) - (b.sizeBytes || 0);
      return String(a.name || a.fileName || "").localeCompare(String(b.name || b.fileName || ""));
    });

    updateCountPill(filtered.length, allFiles.length);

    if (!filtered.length) {
      var hasActiveFilter = !!(term || scopeVal !== "" || folderVal !== "");
      container.innerHTML =
        '<li class="files-empty-state">' +
          '<svg viewBox="0 -960 960 960" width="28" height="28" fill="currentColor" aria-hidden="true" focusable="false"><path d="M160-160q-33 0-56.5-23.5T80-240v-480q0-33 23.5-56.5T160-800h240l80 80h320q33 0 56.5 23.5T880-640v400q0 33-23.5 56.5T800-160H160Zm0-80h640v-400H447l-80-80H160v480Z"/></svg>' +
          "<div>" + (hasActiveFilter
            ? "Nenhum arquivo corresponde aos filtros selecionados."
            : "Nenhum arquivo disponível para este equipamento.") + "</div>" +
          (hasActiveFilter
            ? '<button type="button" class="btn subtle btn-sm" id="filesClearFiltersBtn">Limpar filtros</button>'
            : "") +
        "</li>";
      var clearBtn = document.getElementById("filesClearFiltersBtn");
      if (clearBtn) {
        clearBtn.addEventListener("click", function () {
          if (searchEl) searchEl.value = "";
          if (scopeEl) scopeEl.value = "";
          if (folderEl) folderEl.value = "";
          renderFiles();
        });
      }
      return;
    }

    container.innerHTML = filtered
      .map(function (f) {
        var meta = scopeMeta[f.scope] || scopeMeta[0];
        var displayName = f.name || f.fileName || "Arquivo";
        var isBusy = !!downloadingIds[f.id];
        var disAttr = isBusy ? " disabled" : "";
        return (
          '<li class="file-card" data-file-id="' + escapeHtmlAttr(f.id) + '">' +
            '<div class="file-card-main">' +
              '<div class="file-card-icon">' + fileTypeIconSvg(f.fileName) + "</div>" +
              '<div class="file-card-body">' +
                '<div class="file-card-title-row">' +
                  '<strong class="file-card-title">' + escapeHtml(displayName) + "</strong>" +
                  '<span class="file-scope-badge ' + meta.cls + '">' + escapeHtml(meta.label) + "</span>" +
                  (f.folderPath && f.folderPath !== "/" ? '<span class="badge badge-muted">' + escapeHtml(f.folderPath) + "</span>" : "") +
                  (f.category ? '<span class="badge badge-muted">' + escapeHtml(f.category) + "</span>" : "") +
                "</div>" +
                '<div class="file-card-meta">' +
                  '<span class="file-card-filename">' + escapeHtml(f.fileName) + "</span>" +
                  '<span aria-hidden="true">•</span>' +
                  "<span>" + formatFileSize(f.sizeBytes) + "</span>" +
                  (f.sha256
                    ? '<button type="button" class="file-hash-btn" data-copy-sha="' + escapeHtmlAttr(f.sha256) + '" aria-label="Copiar SHA-256 de ' + escapeHtmlAttr(displayName) + '">SHA-256: ' + escapeHtml(f.sha256.slice(0, 10)) + "…</button>"
                    : "") +
                "</div>" +
                (f.description ? '<div class="file-card-desc">' + escapeHtml(f.description) + "</div>" : "") +
                '<div class="file-card-progress" id="fileProgress-' + escapeHtmlAttr(f.id) + '">' + buildProgressMarkup(f) + "</div>" +
              "</div>" +
            "</div>" +
            '<div class="file-card-actions">' +
              '<button type="button" class="btn primary btn-sm" data-file-dl="' + escapeHtmlAttr(f.id) + '" data-file-name="' + escapeHtmlAttr(f.fileName) + '" aria-label="Baixar ' + escapeHtmlAttr(displayName) + '"' + disAttr + ">Baixar</button>" +
              '<button type="button" class="btn subtle btn-sm" data-file-saveas="' + escapeHtmlAttr(f.id) + '" data-file-name="' + escapeHtmlAttr(f.fileName) + '" aria-label="Salvar ' + escapeHtmlAttr(displayName) + ' como"' + disAttr + ">Salvar como</button>" +
            "</div>" +
          "</li>"
        );
      })
      .join("");
  }

  function updateProgressDom(fileID) {
    var prog = document.getElementById("fileProgress-" + fileID);
    if (!prog) return;
    var fileObj = null;
    for (var i = 0; i < allFiles.length; i++) {
      if (allFiles[i].id === fileID) {
        fileObj = allFiles[i];
        break;
      }
    }
    prog.innerHTML = buildProgressMarkup(fileObj || { id: fileID, name: fileID });
  }

  async function loadAgentFiles(force) {
    if (loadedOnce && !force) return;
    var banner = document.getElementById("filesStatusBanner");
    showErrorBanner("");
    if (banner) banner.textContent = "Carregando arquivos...";
    if (!loadedOnce) renderLoadingSkeleton();

    try {
      var api = typeof appApi === "function" ? appApi() : null;
      if (!api || typeof api.ListAgentFiles !== "function") {
        throw new Error("Binding nativo de arquivos indisponível. Atualize os bindings ou reinicie o aplicativo.");
      }
      var list = await api.ListAgentFiles();
      allFiles = Array.isArray(list) ? list : [];
      loadedOnce = true;
      populateFolderFilter();
      if (banner) {
        banner.textContent = allFiles.length === 1
          ? "1 arquivo disponível."
          : allFiles.length + " arquivos disponíveis.";
      }
      renderFiles();
    } catch (err) {
      var msg = err && err.message ? err.message : String(err);
      if (banner) banner.textContent = "";
      showErrorBanner("Erro ao carregar arquivos: " + msg);
      if (!loadedOnce) {
        var container = document.getElementById("filesListContainer");
        if (container) container.innerHTML = "";
      }
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
    progressStates[fileID] = { kind: "loading", determinate: false, text: "Preparando download..." };
    updateProgressDom(fileID);

    var statusBanner = document.getElementById("filesStatusBanner");
    try {
      var api = typeof appApi === "function" ? appApi() : null;
      if (!api || typeof api.DownloadAgentFile !== "function") {
        throw new Error("Binding de download indisponível.");
      }
      var res = await api.DownloadAgentFile(fileID, fileName, !!chooseLocation);
      progressStates[fileID] = {
        kind: "done",
        savedPath: res && res.savedPath ? res.savedPath : "",
        verified: !!(res && res.verified)
      };
      updateProgressDom(fileID);
      if (statusBanner) {
        statusBanner.textContent = "Download concluído: " + fileName;
      }
      if (typeof showToast === "function") {
        showToast("Download concluído: " + fileName, "success");
      }
    } catch (err) {
      var errMsg = err && err.message ? err.message : String(err);
      if (errMsg.toLowerCase().indexOf("cancelado") !== -1) {
        delete progressStates[fileID];
      } else {
        progressStates[fileID] = { kind: "error", text: "Falha: " + errMsg };
        if (statusBanner) {
          statusBanner.textContent = "Erro no download de " + fileName + ": " + errMsg;
        }
      }
      updateProgressDom(fileID);
    } finally {
      delete downloadingIds[fileID];
      setFileButtonsDisabled(fileID, false);
    }
  }

  function handleProgressEvent(d) {
    if (!d || !d.fileId || d.done) return;
    var pct = typeof d.percentage === "number" ? d.percentage : 0;
    var totalStr = d.total > 0 ? " / " + formatFileSize(d.total) : "";
    progressStates[d.fileId] = {
      kind: "loading",
      determinate: d.total > 0,
      percentage: pct,
      text: "Baixando: " + pct + "% (" + formatFileSize(d.written) + totalStr + ")"
    };
    updateProgressDom(d.fileId);
  }

  window.loadAgentFiles = loadAgentFiles;

  document.addEventListener("DOMContentLoaded", function () {
    var refreshBtn = document.getElementById("filesRefreshBtn");
    var searchEl = document.getElementById("filesSearchInput");
    var scopeEl = document.getElementById("filesScopeFilter");
    var folderEl = document.getElementById("filesFolderFilter");
    var sortEl = document.getElementById("filesSortSelect");
    var container = document.getElementById("filesListContainer");

    if (refreshBtn) refreshBtn.addEventListener("click", function () { loadAgentFiles(true); });
    if (searchEl) searchEl.addEventListener("input", renderFiles);
    if (scopeEl) scopeEl.addEventListener("change", renderFiles);
    if (folderEl) folderEl.addEventListener("change", renderFiles);
    if (sortEl) sortEl.addEventListener("change", renderFiles);

    if (container) {
      container.addEventListener("click", function (ev) {
        var btn = ev.target && ev.target.closest ? ev.target.closest("button") : null;
        if (!btn) return;
        if (btn.dataset.fileDl) {
          triggerDownload(btn.dataset.fileDl, btn.dataset.fileName || "arquivo", false);
        } else if (btn.dataset.fileSaveas) {
          triggerDownload(btn.dataset.fileSaveas, btn.dataset.fileName || "arquivo", true);
        } else if (btn.dataset.openFolder) {
          var api = typeof appApi === "function" ? appApi() : null;
          if (api && typeof api.OpenDownloadedFileFolder === "function") {
            api.OpenDownloadedFileFolder(btn.dataset.openFolder);
          }
        } else if (btn.dataset.copySha) {
          var sha = btn.dataset.copySha;
          if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
            navigator.clipboard.writeText(sha).then(function () {
              if (typeof showToast === "function") showToast("SHA-256 copiado.", "info");
            }).catch(function () {});
          }
        }
      });
    }

    if (window.wails && typeof window.wails.on === "function") {
      window.wails.on("files:download-progress", handleProgressEvent);
    } else if (window.wails && window.wails.Events && typeof window.wails.Events.On === "function") {
      window.wails.Events.On("files:download-progress", function (ev) {
        handleProgressEvent(ev && ev.data ? ev.data : ev);
      });
    }
  });
})();
