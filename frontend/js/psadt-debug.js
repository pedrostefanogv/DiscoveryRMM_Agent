"use strict";

(function () {
  var defaultTheme = {
    surface: "#fff8ef",
    text: "#2a1f16",
    accent: "#0b6e4f",
    warning: "#8a4e12",
    danger: "#9a031e"
  };

  function appApi() {
    if (!window.go || !window.go.app || !window.go.app.App) {
      throw new Error("API do Wails indisponivel");
    }
    return window.go.app.App;
  }

  var refreshStateBtn = document.getElementById("refreshStateBtn");
  var closeBtn = document.getElementById("closeBtn");
  var stateKvs = document.getElementById("stateKvs");
  var stateStatus = document.getElementById("stateStatus");

  var moduleVersion = document.getElementById("moduleVersion");
  var checkModuleBtn = document.getElementById("checkModuleBtn");
  var installModuleBtn = document.getElementById("installModuleBtn");
  var moduleStatus = document.getElementById("moduleStatus");

  var colorSurface = document.getElementById("colorSurface");
  var colorText = document.getElementById("colorText");
  var colorAccent = document.getElementById("colorAccent");
  var colorWarning = document.getElementById("colorWarning");
  var colorDanger = document.getElementById("colorDanger");
  var applyThemeBtn = document.getElementById("applyThemeBtn");
  var resetThemeBtn = document.getElementById("resetThemeBtn");

  var notifTitle = document.getElementById("notifTitle");
  var notifMessage = document.getElementById("notifMessage");
  var notifSeverity = document.getElementById("notifSeverity");
  var notifMode = document.getElementById("notifMode");
  var notifLayout = document.getElementById("notifLayout");
  var emitNotifBtn = document.getElementById("emitNotifBtn");
  var notifStatus = document.getElementById("notifStatus");

  function setStatus(el, message, kind) {
    if (!el) return;
    el.textContent = message || "";
    el.className = "status" + (kind ? " " + kind : "");
  }

  function escapeHtml(text) {
    return String(text || "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/\"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function appendPreviewToList(item) {
    var list = document.getElementById("previewList");
    if (!list) return;
    var severityClass = item.severity === "critical" || item.severity === "high" ? "error" : (item.severity === "medium" ? "warning" : "success");
    var html = "";
    html += '<div class="preview-item ' + severityClass + '">';
    html += '<div class="preview-title">' + escapeHtml(item.title) + '</div>';
    html += '<div>' + escapeHtml(item.message) + '</div>';
    html += '<div class="preview-badge">mode=' + escapeHtml(item.mode) + ' | severity=' + escapeHtml(item.severity) + ' | layout=' + escapeHtml(item.layout) + '</div>';
    html += "</div>";
    list.insertAdjacentHTML("afterbegin", html);
  }

  function currentTheme() {
    return {
      surface: colorSurface ? colorSurface.value : defaultTheme.surface,
      text: colorText ? colorText.value : defaultTheme.text,
      accent: colorAccent ? colorAccent.value : defaultTheme.accent,
      warning: colorWarning ? colorWarning.value : defaultTheme.warning,
      danger: colorDanger ? colorDanger.value : defaultTheme.danger
    };
  }

  function applyTheme(theme) {
    var root = document.documentElement;
    root.style.setProperty("--surface", theme.surface);
    root.style.setProperty("--text", theme.text);
    root.style.setProperty("--accent", theme.accent);
    root.style.setProperty("--warning", theme.warning);
    root.style.setProperty("--danger", theme.danger);
  }

  function renderState(state) {
    if (!stateKvs) return;
    var cfg = (state && state.configuration) || {};
    var psadt = cfg.psadt || {};
    var module = (state && state.moduleStatus) || {};

    var rows = [
      ["Debug Mode", String(!!(state && state.runtimeDebugMode))],
      ["PSADT enabled", String(!!psadt.enabled)],
      ["PSADT version", psadt.requiredVersion || "-"],
      ["Auto install", String(!!psadt.autoInstallModule)],
      ["Install source", psadt.installSource || "-"],
      ["Module installed", String(!!module.installed)],
      ["Module version", module.version || "-"],
      ["Policies", String((state && state.notificationPolicies ? state.notificationPolicies.length : 0))],
      ["Brand company", (state && state.notificationBranding && state.notificationBranding.companyName) || "-"]
    ];

    stateKvs.innerHTML = rows.map(function (row) {
      return '<div class="kv"><span class="k">' + escapeHtml(row[0]) + '</span><span class="v mono">' + escapeHtml(row[1]) + '</span></div>';
    }).join("");
  }

  function loadState() {
    setStatus(stateStatus, "Carregando estado...", "");
    appApi().GetPSADTDebugState().then(function (state) {
      renderState(state || {});
      setStatus(stateStatus, "Estado atualizado.", "ok");
      if (state && state.notificationBranding && state.notificationBranding.theme) {
        var t = state.notificationBranding.theme;
        if (t.surface && colorSurface) colorSurface.value = t.surface;
        if (t.text && colorText) colorText.value = t.text;
        if (t.accent && colorAccent) colorAccent.value = t.accent;
        if (t.warning && colorWarning) colorWarning.value = t.warning;
        if (t.danger && colorDanger) colorDanger.value = t.danger;
        applyTheme(currentTheme());
      }
    }).catch(function (err) {
      setStatus(stateStatus, "Falha ao carregar estado: " + (err && err.message ? err.message : String(err)), "error");
    });
  }

  function checkModule() {
    setStatus(moduleStatus, "Verificando modulo...", "");
    appApi().CheckPSADTModuleStatus().then(function (result) {
      var msg = (result && result.message) || "Sem resposta";
      if (result && result.installed) {
        msg = msg + " (versao " + (result.version || "-") + ")";
      }
      setStatus(moduleStatus, msg, result && result.installed ? "ok" : "error");
      loadState();
    }).catch(function (err) {
      setStatus(moduleStatus, "Falha na verificacao: " + (err && err.message ? err.message : String(err)), "error");
    });
  }

  function installModule() {
    setStatus(moduleStatus, "Instalando modulo PSADT...", "");
    var version = moduleVersion ? moduleVersion.value : "4.1.8";
    appApi().InstallPSADTModule(version).then(function (result) {
      var ok = !!(result && result.installed);
      var msg = (result && result.message) || (ok ? "Instalacao concluida" : "Instalacao falhou");
      if (ok && result.version) {
        msg += " (" + result.version + ")";
      }
      setStatus(moduleStatus, msg, ok ? "ok" : "error");
      loadState();
    }).catch(function (err) {
      setStatus(moduleStatus, "Falha na instalacao: " + (err && err.message ? err.message : String(err)), "error");
    });
  }

  function buildNotifFromForm() {
    return {
      title: notifTitle ? notifTitle.value : "Teste",
      message: notifMessage ? notifMessage.value : "Mensagem de teste",
      mode: notifMode ? notifMode.value : "notify_only",
      severity: notifSeverity ? notifSeverity.value : "medium",
      layout: notifLayout ? notifLayout.value : "toast",
      accent: colorAccent ? colorAccent.value : "",
      requireAck: notifMode && notifMode.value === "require_confirmation"
    };
  }

  function emitRuntimeNotification() {
    var payload = buildNotifFromForm();
    appApi().EmitPSADTDebugNotification(payload).then(function () {
      appendPreviewToList(payload);
      setStatus(notifStatus, "Evento emitido no runtime via pipeline real de notificacao.", "ok");
    }).catch(function (err) {
      setStatus(notifStatus, "Falha ao emitir evento: " + (err && err.message ? err.message : String(err)), "error");
    });
  }

  function resetTheme() {
    if (colorSurface) colorSurface.value = defaultTheme.surface;
    if (colorText) colorText.value = defaultTheme.text;
    if (colorAccent) colorAccent.value = defaultTheme.accent;
    if (colorWarning) colorWarning.value = defaultTheme.warning;
    if (colorDanger) colorDanger.value = defaultTheme.danger;
    applyTheme(defaultTheme);
  }

  function executeTestScript() {
    var executeAppName = document.getElementById("executeAppName");
    var executeAppVersion = document.getElementById("executeAppVersion");
    var appName = executeAppName ? executeAppName.value : "TestApp";
    var appVersion = executeAppVersion ? executeAppVersion.value : "1.0.0";
    
    var executeStatus = document.getElementById("executeStatus");
    setStatus(executeStatus, "Executando script PSADT...", "");
    
    appApi().ExecutePSADTTestScript(appName, appVersion).then(function (result) {
      var msg = result && result.success ? "✓ Script executado com sucesso" : "✗ Falha na execução";
      if (result && result.error) msg += ": " + result.error;
      
      var outputEl = document.getElementById("executeOutput");
      if (outputEl) {
        outputEl.textContent = (result && result.output) ? result.output : "(sem saída)";
      }
      
      var duration = result ? result.durationMs : 0;
      var exitCode = result ? result.exitCode : -1;
      setStatus(executeStatus, msg + " (ExitCode: " + exitCode + ", " + duration + "ms)", result && result.success ? "ok" : "error");
      
    }).catch(function (err) {
      setStatus(executeStatus, "Falha: " + (err && err.message ? err.message : String(err)), "error");
    });
  }

  function getScriptTemplate() {
    appApi().GetPSADTScriptTemplate().then(function (template) {
      var scriptEl = document.getElementById("customScript");
      if (scriptEl) {
        scriptEl.value = template;
      }
    }).catch(function (err) {
      setStatus(document.getElementById("customStatus"), "Erro ao carregar template: " + (err && err.message ? err.message : String(err)), "error");
    });
  }

  function executeVisualNotification() {
    var typeEl    = document.getElementById("visualNotifType");
    var titleEl   = document.getElementById("visualNotifTitle");
    var messageEl = document.getElementById("visualNotifMessage");
    var appNameEl = document.getElementById("visualNotifAppName");
    var durEl     = document.getElementById("visualNotifDuration");
    var statusEl  = document.getElementById("visualNotifStatus");
    var outputEl  = document.getElementById("visualNotifOutput");

    var req = {
      notifType:       typeEl    ? typeEl.value                   : "balloon_info",
      title:           titleEl   ? titleEl.value                  : "Discovery Agent",
      message:         messageEl ? messageEl.value                : "Teste de notificacao PSADT",
      appName:         appNameEl ? appNameEl.value                : "TestApp",
      durationSeconds: durEl     ? (parseInt(durEl.value) || 5)  : 5
    };

    setStatus(statusEl, "Executando notificacao PSADT nativa...", "");
    if (outputEl) outputEl.textContent = "";

    appApi().ExecutePSADTVisualNotification(req).then(function (result) {
      if (outputEl) {
        outputEl.textContent = (result && result.output) ? result.output : "(sem saida)";
      }
      var ok  = !!(result && result.success);
      var msg = ok ? "\u2713 Notificacao PSADT executada" : "\u2717 Falha";
      if (result && result.error) msg += ": " + result.error;
      var exitCode = result ? result.exitCode : -1;
      var duration = result ? result.durationMs : 0;
      setStatus(statusEl, msg + " (ExitCode: " + exitCode + ", " + duration + "ms)", ok ? "ok" : "error");
    }).catch(function (err) {
      setStatus(statusEl, "Erro: " + (err && err.message ? err.message : String(err)), "error");
    });
  }

  function executeCustomScript() {
    var scriptEl = document.getElementById("customScript");
    var scriptContent = scriptEl ? scriptEl.value : "";
    var customStatus = document.getElementById("customStatus");
    
    if (!scriptContent || !scriptContent.trim()) {
      setStatus(customStatus, "Script vazio!", "error");
      return;
    }
    
    setStatus(customStatus, "Executando script customizado...", "");
    
    appApi().ExecuteCustomPSADTScript(scriptContent).then(function (result) {
      var msg = result && result.success ? "✓ Sucesso" : "✗ Falha";
      var outputEl = document.getElementById("customOutput");
      if (outputEl) {
        outputEl.textContent = (result && result.output) ? result.output : "(sem saída)";
      }
      var exitCode = result ? result.exitCode : -1;
      setStatus(customStatus, msg + " (ExitCode: " + exitCode + ")", result && result.success ? "ok" : "error");
    }).catch(function (err) {
      setStatus(customStatus, "Erro: " + (err && err.message ? err.message : String(err)), "error");
    });
  }


  if (refreshStateBtn) {
    refreshStateBtn.addEventListener("click", loadState);
  }
  if (closeBtn) {
    closeBtn.addEventListener("click", function () {
      window.close();
    });
  }
  if (checkModuleBtn) {
    checkModuleBtn.addEventListener("click", checkModule);
  }
  if (installModuleBtn) {
    installModuleBtn.addEventListener("click", installModule);
  }
  if (applyThemeBtn) {
    applyThemeBtn.addEventListener("click", function () {
      applyTheme(currentTheme());
      setStatus(stateStatus, "Tema aplicado no preview.", "ok");
    });
  }
  if (resetThemeBtn) {
    resetThemeBtn.addEventListener("click", function () {
      resetTheme();
      setStatus(stateStatus, "Tema resetado.", "ok");
    });
  }
  if (emitNotifBtn) {
    emitNotifBtn.addEventListener("click", emitRuntimeNotification);
  }

  var executeTestBtn = document.getElementById("executeTestBtn");
  var getTemplateBtn = document.getElementById("getTemplateBtn");
  var executeCustomBtn = document.getElementById("executeCustomBtn");

  if (executeTestBtn) {
    executeTestBtn.addEventListener("click", executeTestScript);
  }
  if (getTemplateBtn) {
    getTemplateBtn.addEventListener("click", getScriptTemplate);
  }
  if (executeCustomBtn) {
    executeCustomBtn.addEventListener("click", executeCustomScript);
  }

  var visualNotifBtn = document.getElementById("visualNotifBtn");
  if (visualNotifBtn) {
    visualNotifBtn.addEventListener("click", executeVisualNotification);
  }

  resetTheme();
  loadState();
})();
