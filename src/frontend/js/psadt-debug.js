"use strict";

// Console de debug das NOTIFICAÇÕES NATIVAS do agente (substituto do console
// de debug do PSADT). Testa os componentes de core/notifyui, a pipeline de
// eventos e o runner de PowerShell — nada de PSAppDeployToolkit.

(function () {
  function appApi() {
    if (!window.go || !window.go.app || !window.go.app.App) {
      throw new Error("API do Wails indisponivel");
    }
    return window.go.app.App;
  }

  function setStatus(el, message, kind) {
    if (!el) return;
    el.textContent = message || "";
    el.className = "status" + (kind ? " " + kind : "");
  }

  function escapeHtml(text) {
    return String(text || "")
      .replace(/&/g, "&" + "amp;")
      .replace(/</g, "&" + "lt;")
      .replace(/>/g, "&" + "gt;")
      .replace(/"/g, "&" + "quot;")
      .replace(/'/g, "&" + "#39;");
  }

  var refreshStateBtn = document.getElementById("refreshStateBtn");
  var stateKvs = document.getElementById("stateKvs");
  var stateStatus = document.getElementById("stateStatus");

  function loadState() {
    if (!stateKvs) return;
    setStatus(stateStatus, "carregando estado...");
    appApi().GetNotificationDebugState().then(function (state) {
      var backend = (state && state.backend) || "native";
      var kvs = [
        ["Backend", backend],
        ["Runtime debug", String(!!(state && state.runtimeDebugMode))],
        ["PSADT", "removido — notificações nativas"]
      ];
      stateKvs.innerHTML = kvs.map(function (kv) {
        return '<div class="kv"><span class="k">' + escapeHtml(kv[0]) + '</span><span class="v">' + escapeHtml(kv[1]) + "</span></div>";
      }).join("");
      setStatus(stateStatus, "estado carregado", "ok");
    }).catch(function (err) {
      setStatus(stateStatus, "falha ao carregar estado: " + err, "error");
    });
  }

  if (refreshStateBtn) {
    refreshStateBtn.addEventListener("click", loadState);
  }
  loadState();

  // ── Preflight nativo ──
  var runPreflightBtn = document.getElementById("runPreflightBtn");
  var preflightStatus = document.getElementById("preflightStatus");
  var preflightOutput = document.getElementById("preflightOutput");

  if (runPreflightBtn) {
    runPreflightBtn.addEventListener("click", function () {
      setStatus(preflightStatus, "executando preflight...");
      runPreflightBtn.disabled = true;
      appApi().RunNativePreflightChecks().then(function (r) {
        runPreflightBtn.disabled = false;
        var lines = [
          "OS: " + (r.osName || "?") + " " + (r.osVersion || "?") + " (" + (r.architecture || "?") + ")",
          "Admin (elevado): " + (r.isAdmin ? "sim" : "nao"),
          "Reboot pendente: " + (r.rebootPending ? "sim" : "nao"),
          "Rede: " + (r.networkAvailable ? "online" : "offline"),
          "Timestamp: " + (r.checkedAtUtc || "")
        ];
        if (preflightOutput) preflightOutput.textContent = lines.join("\n");
        setStatus(preflightStatus, r.success ? "preflight concluido" : "preflight falhou: " + (r.error || ""), r.success ? "ok" : "error");
      }).catch(function (err) {
        runPreflightBtn.disabled = false;
        setStatus(preflightStatus, "falha no preflight: " + err, "error");
      });
    });
  }

  // ── Pipeline de eventos (notificação interna) ──
  var emitNotifBtn = document.getElementById("emitNotifBtn");
  var notifStatus = document.getElementById("notifStatus");

  function appendPreviewToList(item) {
    var list = document.getElementById("previewList");
    if (!list) return;
    var severityClass = item.severity === "critical" || item.severity === "high" ? "error" : (item.severity === "medium" ? "warning" : "success");
    var html = "";
    html += '<div class="preview-item ' + severityClass + '">';
    html += '<div class="preview-title">' + escapeHtml(item.title) + "</div>";
    html += "<div>" + escapeHtml(item.message) + "</div>";
    html += '<div class="preview-badge">mode=' + escapeHtml(item.mode) + " | severity=" + escapeHtml(item.severity) + " | layout=" + escapeHtml(item.layout) + "</div>";
    html += "</div>";
    list.insertAdjacentHTML("afterbegin", html);
  }

  if (emitNotifBtn) {
    emitNotifBtn.addEventListener("click", function () {
      var payload = {
        title: (document.getElementById("notifTitle") || {}).value || "",
        message: (document.getElementById("notifMessage") || {}).value || "",
        severity: (document.getElementById("notifSeverity") || {}).value || "medium",
        mode: (document.getElementById("notifMode") || {}).value || "notify_only",
        layout: (document.getElementById("notifLayout") || {}).value || "toast"
      };
      setStatus(notifStatus, "emitindo evento...");
      appApi().EmitNotificationDebugEvent(payload).then(function () {
        setStatus(notifStatus, "evento emitido", "ok");
        appendPreviewToList(payload);
      }).catch(function (err) {
        setStatus(notifStatus, "falha ao emitir: " + err, "error");
      });
    });
  }

  // ── Teste visual nativo ──
  var visualNotifType = document.getElementById("visualNotifType");
  var visualNotifBtn = document.getElementById("visualNotifBtn");
  var visualNotifStatus = document.getElementById("visualNotifStatus");
  var visualNotifOutput = document.getElementById("visualNotifOutput");
  var visualNotifHelp = document.getElementById("visualNotifHelp");

  var visualHelp = {
    balloon_info: "Toast do Windows (Windows Runtime) com o icone global do agente.",
    balloon_warning: "Toast do Windows com o icone global do agente.",
    balloon_error: "Toast do Windows com o icone global do agente.",
    prompt_ok: "Cartão nativo com um botão OK (tema do Windows).",
    prompt_yesno: "Cartão nativo com Sim/Nao; o botao da direita é a ação principal.",
    prompt_continue: "Cartão nativo Continuar/Adiar.",
    progress: "Janela nativa de progresso com barra indeterminada.",
    dialog_box: "MessageBox nativa do Windows (o texto do botao clicado volta em result).",
    restart_prompt: "Cartão com contagem regressiva. O reboot real NAO é executado.",
    welcome: "Cartão Instalar/Adiar com contador — mesmo prompt da automação.",
    countdown: "Cartão com contador visível e botões OK/Fechar."
  };

  function updateVisualGroups() {
    var t = visualNotifType ? visualNotifType.value : "balloon_info";
    var groups = {
      balloon_info: ["title", "message"],
      balloon_warning: ["title", "message"],
      balloon_error: ["title", "message"],
      prompt_ok: ["title", "subtitle", "message", "prompt"],
      prompt_yesno: ["title", "subtitle", "message", "prompt"],
      prompt_continue: ["title", "subtitle", "message", "prompt"],
      progress: ["title", "subtitle", "message", "progress"],
      dialog_box: ["title", "message", "dialog"],
      restart_prompt: ["title", "message", "restart"],
      welcome: ["title", "message", "welcome"],
      countdown: ["title", "message", "countdown"]
    };
    var active = {};
    (groups[t] || []).forEach(function (g) { active[g] = true; });
    var form = document.getElementById("visualNotifForm");
    if (!form) return;
    var blocks = form.querySelectorAll("[data-visual-group]");
    for (var i = 0; i < blocks.length; i++) {
      var g = blocks[i].getAttribute("data-visual-group");
      blocks[i].style.display = active[g] ? "" : "none";
    }
    if (visualNotifHelp) visualNotifHelp.textContent = visualHelp[t] || "";
    try { localStorage.setItem("psadt.visualNotifType", t); } catch (e) { /* storage indisponivel */ }
  }

  if (visualNotifType) {
    visualNotifType.addEventListener("change", updateVisualGroups);
    var savedType = null;
    try { savedType = localStorage.getItem("psadt.visualNotifType"); } catch (e) { /* storage indisponivel */ }
    if (savedType) visualNotifType.value = savedType;
    updateVisualGroups();
  }

  function field(id) {
    var el = document.getElementById(id);
    return el ? el.value : "";
  }

  if (visualNotifBtn) {
    visualNotifBtn.addEventListener("click", function () {
      var t = visualNotifType ? visualNotifType.value : "balloon_info";
      var req = {
        notifType: t,
        title: field("visualNotifTitle"),
        message: field("visualNotifMessage"),
        subtitle: field("visualNotifSubtitle"),
        appName: "Discovery Agent",
        durationSeconds: parseInt(field("visualNotifDuration"), 10) || 5,
        promptLeftText: field("visualPromptLeft"),
        promptRightText: field("visualPromptRight"),
        promptTimeout: parseInt(field("visualPromptTimeout"), 10) || 0,
        buttonText: field("visualButtonText"),
        buttonCloseText: field("visualButtonCloseText"),
        countdownLabel: field("visualCountdownLabel"),
        countdownNoDefer: !!(document.getElementById("visualCountdownNoDefer") || {}).checked,
        restartCountdownSeconds: parseInt(field("visualRestartCountdown"), 10) || 60,
        allowDefer: !!(document.getElementById("visualAllowDefer") || {}).checked,
        closeProcessesCountdown: parseInt(field("visualCloseCountdown"), 10) || 60,
        dialogButtons: field("visualDialogButtons") || "Ok"
      };
      setStatus(visualNotifStatus, "executando notificacao nativa...");
      if (visualNotifBtn) visualNotifBtn.disabled = true;
      appApi().ExecuteNativeVisualNotification(req).then(function (result) {
        if (visualNotifBtn) visualNotifBtn.disabled = false;
        if (visualNotifOutput) visualNotifOutput.textContent = (result.output || "") + (result.result ? "\nresult=" + result.result : "");
        if (result.success) {
          setStatus(visualNotifStatus, "notificação exibida (desfecho: " + (result.result || "ok") + ")", "ok");
        } else {
          setStatus(visualNotifStatus, "falha: " + (result.error || "desconhecida"), "error");
        }
      }).catch(function (err) {
        if (visualNotifBtn) visualNotifBtn.disabled = false;
        setStatus(visualNotifStatus, "falha: " + err, "error");
      });
    });
  }

  // ── Script PowerShell genérico ──
  var getTemplateBtn = document.getElementById("getTemplateBtn");
  var executeCustomBtn = document.getElementById("executeCustomBtn");
  var customStatus = document.getElementById("customStatus");
  var customOutput = document.getElementById("customOutput");

  if (getTemplateBtn) {
    getTemplateBtn.addEventListener("click", function () {
      appApi().GetPowerShellScriptTemplate().then(function (template) {
        var area = document.getElementById("customScript");
        if (area) area.value = template;
        setStatus(customStatus, "template carregado", "ok");
      }).catch(function (err) {
        setStatus(customStatus, "falha ao carregar template: " + err, "error");
      });
    });
  }

  if (executeCustomBtn) {
    executeCustomBtn.addEventListener("click", function () {
      var content = (document.getElementById("customScript") || {}).value || "";
      setStatus(customStatus, "executando script...");
      executeCustomBtn.disabled = true;
      appApi().ExecuteCustomPowerShellScript(content).then(function (result) {
        executeCustomBtn.disabled = false;
        if (customOutput) customOutput.textContent = result.output || "";
        setStatus(customStatus, result.success ? "script executado (exit " + result.exitCode + ")" : "script falhou: " + (result.error || ""), result.success ? "ok" : "error");
      }).catch(function (err) {
        executeCustomBtn.disabled = false;
        setStatus(customStatus, "falha: " + err, "error");
      });
    });
  }
})();
