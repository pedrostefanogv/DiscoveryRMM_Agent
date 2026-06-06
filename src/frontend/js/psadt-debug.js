"use strict";

(function () {
  var defaultThemeFallback = {
    surface: "#fffefa",
    text: "#1a1712",
    accent: "#0a7a56",
    warning: "#f0a04b",
    danger: "#c1121f"
  };

  function appApi() {
    if (!window.go || !window.go.app || !window.go.app.App) {
      throw new Error("API do Wails indisponivel");
    }
    return window.go.app.App;
  }

  function getDefaultTheme() {
    var styles = getComputedStyle(document.documentElement);
    return {
      surface: normalizeThemeColor(styles.getPropertyValue("--surface"), defaultThemeFallback.surface),
      text: normalizeThemeColor(styles.getPropertyValue("--text"), defaultThemeFallback.text),
      accent: normalizeThemeColor(styles.getPropertyValue("--accent"), defaultThemeFallback.accent),
      warning: normalizeThemeColor(styles.getPropertyValue("--accent-2"), defaultThemeFallback.warning),
      danger: normalizeThemeColor(styles.getPropertyValue("--danger"), defaultThemeFallback.danger)
    };
  }

  function getPsadtThemeScope() {
    return document.getElementById("psadtView");
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

  // Reage à troca de tema global (claro ↔ escuro) limpando inline styles
  // para que a view PSADT herde os tokens do tema automaticamente.
  var themeObserver = new MutationObserver(function () {
    clearInlinePsadtTheme();
  });
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });

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

  function normalizeThemeColor(value, fallback) {
    var normalized = String(value || "").trim();
    if (!normalized) return fallback;
    if (/^#[0-9a-fA-F]{3,8}$/.test(normalized)) return normalized;
    if (/^(rgb|rgba|hsl|hsla)\(/i.test(normalized)) return normalized;
    return fallback;
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
    var fallbackTheme = getDefaultTheme();
    return {
      surface: colorSurface ? colorSurface.value : fallbackTheme.surface,
      text: colorText ? colorText.value : fallbackTheme.text,
      accent: colorAccent ? colorAccent.value : fallbackTheme.accent,
      warning: colorWarning ? colorWarning.value : fallbackTheme.warning,
      danger: colorDanger ? colorDanger.value : fallbackTheme.danger
    };
  }

  function applyTheme(theme) {
    var scope = getPsadtThemeScope();
    if (!scope) return;
    scope.style.setProperty("--psadt-surface", theme.surface);
    scope.style.setProperty("--psadt-text", theme.text);
    scope.style.setProperty("--psadt-accent", theme.accent);
    scope.style.setProperty("--psadt-warning", theme.warning);
    scope.style.setProperty("--psadt-danger", theme.danger);
  }

  function clearInlinePsadtTheme() {
    var scope = getPsadtThemeScope();
    if (!scope) return;
    scope.style.removeProperty("--psadt-surface");
    scope.style.removeProperty("--psadt-text");
    scope.style.removeProperty("--psadt-accent");
    scope.style.removeProperty("--psadt-warning");
    scope.style.removeProperty("--psadt-danger");
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
    var fallbackTheme = getDefaultTheme();
    if (colorSurface) colorSurface.value = fallbackTheme.surface;
    if (colorText) colorText.value = fallbackTheme.text;
    if (colorAccent) colorAccent.value = fallbackTheme.accent;
    if (colorWarning) colorWarning.value = fallbackTheme.warning;
    if (colorDanger) colorDanger.value = fallbackTheme.danger;
    clearInlinePsadtTheme();
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
    var dialogButtonsEl = document.getElementById("visualDialogButtons");
    var dialogDefaultEl = document.getElementById("visualDialogDefault");
    var dialogIconEl = document.getElementById("visualDialogIcon");
    var dialogTimeoutEl = document.getElementById("visualDialogTimeout");
    var dialogNoWaitEl = document.getElementById("visualDialogNoWait");
    var dialogExitOnTimeoutEl = document.getElementById("visualDialogExitOnTimeout");
    var dialogNotTopMostEl = document.getElementById("visualDialogNotTopMost");
    var dialogForceEl = document.getElementById("visualDialogForce");
    var statusEl  = document.getElementById("visualNotifStatus");
    var outputEl  = document.getElementById("visualNotifOutput");

    var req = {
      notifType:       typeEl    ? typeEl.value                   : "balloon_info",
      title:           titleEl   ? titleEl.value                  : "Discovery Agent",
      message:         messageEl ? messageEl.value                : "Teste de notificacao PSADT",
      appName:         appNameEl ? appNameEl.value                : "TestApp",
      durationSeconds: durEl     ? (parseInt(durEl.value, 10) || 5)  : 5,
      dialogButtons: dialogButtonsEl ? dialogButtonsEl.value : "OkCancel",
      dialogDefault: dialogDefaultEl ? dialogDefaultEl.value : "First",
      dialogIcon: dialogIconEl ? dialogIconEl.value : "Information",
      dialogTimeout: dialogTimeoutEl ? (parseInt(dialogTimeoutEl.value, 10) || 0) : 0,
      dialogNoWait: dialogNoWaitEl ? !!dialogNoWaitEl.checked : false,
      dialogExitOnTimeout: dialogExitOnTimeoutEl ? !!dialogExitOnTimeoutEl.checked : false,
      dialogNotTopMost: dialogNotTopMostEl ? !!dialogNotTopMostEl.checked : false,
      dialogForce: dialogForceEl ? !!dialogForceEl.checked : false
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

  // =====================================================================
  // NOVOS HANDLERS: Preflight, Welcome, Restart, Session Properties
  // =====================================================================

  // --- Preflight Checks ---
  var runPreflightBtn = document.getElementById("runPreflightBtn");
  function runPreflight() {
    var statusEl = document.getElementById("preflightStatus");
    var outputEl = document.getElementById("preflightOutput");
    setStatus(statusEl, "Executando preflight checks...", "");
    if (outputEl) outputEl.textContent = "";

    appApi().RunPSADTPreflightChecks().then(function (r) {
      if (!r || !r.success) {
        setStatus(statusEl, "Erro: " + (r && r.error ? r.error : "desconhecido"), "error");
        return;
      }
      var lines = [];
      lines.push("OS: " + (r.osName || "?") + " " + (r.osVersion || "?"));
      lines.push("Arch: " + (r.architecture || "?") + "  PS: " + (r.psVersion || "?"));
      lines.push("Admin: " + !!r.isAdmin + "  RebootPending: " + !!r.rebootPending);
      lines.push("Network: " + !!r.networkAvailable + "  FocusMode: " + !!r.userInFocusMode);
      lines.push("Module: " + (r.moduleVersion || "?") + "  UserSessions: " + (r.activeUserSessions || 0));
      lines.push("CheckedAt: " + (r.checkedAtUtc || ""));
      if (outputEl) outputEl.textContent = lines.join("\\n");
      setStatus(statusEl, "Preflight checks concluídos.", "ok");
    }).catch(function (err) {
      setStatus(statusEl, "Falha: " + (err && err.message ? err.message : String(err)), "error");
    });
  }
  if (runPreflightBtn) runPreflightBtn.addEventListener("click", runPreflight);

  // --- Welcome Dialog ---
  var runWelcomeBtn = document.getElementById("runWelcomeBtn");
  function runWelcome() {
    var statusEl = document.getElementById("welcomeStatus");
    var processesEl = document.getElementById("welcomeProcesses");
    var countdownEl = document.getElementById("welcomeCountdown");
    var processes = processesEl ? processesEl.value : "msiexec,setup";
    var countdown = countdownEl ? parseInt(countdownEl.value, 10) || 120 : 120;
    setStatus(statusEl, "Exibindo Welcome Dialog...", "");

    appApi().RunPSADTWelcome(processes, countdown).then(function (r) {
      var msg = (r && r.message) || "";
      if (r && r.success) {
        setStatus(statusEl, "✓ " + msg + " (" + (r.durationMs || 0) + "ms)", "ok");
      } else {
        setStatus(statusEl, "✗ " + msg, "error");
      }
    }).catch(function (err) {
      setStatus(statusEl, "Falha: " + (err && err.message ? err.message : String(err)), "error");
    });
  }
  if (runWelcomeBtn) runWelcomeBtn.addEventListener("click", runWelcome);

  // --- Restart Prompt ---
  var runRestartBtn = document.getElementById("runRestartBtn");
  function runRestart() {
    var statusEl = document.getElementById("restartStatus");
    var countdownEl = document.getElementById("restartCountdown");
    var silentEl = document.getElementById("restartSilent");
    var countdown = countdownEl ? parseInt(countdownEl.value, 10) || 300 : 300;
    var silent = silentEl ? !!silentEl.checked : false;
    setStatus(statusEl, "Exibindo Restart Prompt...", "");

    appApi().RunPSADTRestartPrompt(countdown, silent).then(function (r) {
      var msg = (r && r.message) || "";
      if (r && r.success) {
        setStatus(statusEl, "✓ " + msg + " (" + (r.durationMs || 0) + "ms)", "ok");
      } else {
        setStatus(statusEl, "✗ " + msg, "error");
      }
    }).catch(function (err) {
      setStatus(statusEl, "Falha: " + (err && err.message ? err.message : String(err)), "error");
    });
  }
  if (runRestartBtn) runRestartBtn.addEventListener("click", runRestart);

  // --- Session Properties ---
  var loadSessionPropsBtn = document.getElementById("loadSessionPropsBtn");
  function loadSessionProps() {
    var statusEl = document.getElementById("sessionPropsStatus");
    var kvsEl = document.getElementById("sessionPropsKvs");
    setStatus(statusEl, "Carregando propriedades da sessão...", "");

    appApi().GetPSADTSessionProperties().then(function (r) {
      if (!r || !r.success) {
        setStatus(statusEl, "Erro: " + (r && r.error ? r.error : "desconhecido"), "error");
        return;
      }
      var rows = [
        ["App Name", r.appName || "-"],
        ["App Vendor", r.appVendor || "-"],
        ["App Version", r.appVersion || "-"],
        ["Deployment", r.deploymentType || "-"],
        ["Deploy Mode", r.deployMode || "-"],
        ["Log Path", r.logPath || "-"],
        ["Log Name", r.logName || "-"],
        ["Phase", r.installPhase || "-"]
      ];
      if (kvsEl) {
        kvsEl.innerHTML = rows.map(function (row) {
          return '<div class="kv"><span class="k">' + escapeHtml(row[0]) + '</span><span class="v mono">' + escapeHtml(row[1]) + '</span></div>';
        }).join("");
      }
      setStatus(statusEl, "Propriedades carregadas.", "ok");
    }).catch(function (err) {
      setStatus(statusEl, "Falha: " + (err && err.message ? err.message : String(err)), "error");
    });
  }
  if (loadSessionPropsBtn) loadSessionPropsBtn.addEventListener("click", loadSessionProps);

  setTimeout(function () {
    resetTheme();
    loadState();
  }, 0);
})();
