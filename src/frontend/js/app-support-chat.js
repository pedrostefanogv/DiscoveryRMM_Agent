"use strict";

// ─────────────────────────────────────────────────────────────────────────────
// Janela independente do chat de suporte (chat vinculado ao chamado).
//
// Regras do recurso refletidas aqui:
//   - a janela só fecha quando o chat termina (o X pede confirmação);
//   - NENHUM histórico de conversas antigas é exibido: só a sessão ativa;
//   - anexos/prints seguem a allowlist pré-autorizada do servidor;
//   - tempo real via NATS (eventos supportchat:*), sem polling.
// ─────────────────────────────────────────────────────────────────────────────

(function () {
  var api = function () { return window.go && window.go.app && window.go.app.App; };

  var els = {};
  var state = {
    status: "none",
    connected: false,
    messages: [],
    pending: [],
    policy: {
      enabled: true,
      allowedContentTypes: ["image/jpeg", "image/png", "image/webp", "application/pdf"],
      maxFileSizeBytes: 10 * 1024 * 1024
    },
    sending: false,
    closing: false
  };

  function byId(id) { return document.getElementById(id); }

  function cacheElements() {
    els.statusDot = byId("scStatusDot");
    els.statusChip = byId("scStatusChip");
    els.subtitle = byId("scSubtitle");
    els.banner = byId("scBanner");
    els.messages = byId("scMessages");
    els.empty = byId("scEmpty");
    els.pending = byId("scPending");
    els.input = byId("scInput");
    els.send = byId("scSendBtn");
    els.attach = byId("scAttachBtn");
    els.fileInput = byId("scFileInput");
    els.end = byId("scEndBtn");
    els.confirm = byId("scConfirm");
    els.confirmOk = byId("scConfirmOk");
    els.confirmCancel = byId("scConfirmCancel");
  }

  function call(method) {
    var args = Array.prototype.slice.call(arguments, 1);
    var target = api();
    if (!target || typeof target[method] !== "function") {
      return Promise.reject(new Error("Binding indisponível: " + method + " (regenerar bindings do Wails)."));
    }
    try {
      return Promise.resolve(target[method].apply(target, args));
    } catch (err) {
      return Promise.reject(err);
    }
  }

  function statusLabel(status) {
    if (status === "waiting") return "aguardando confirmação";
    if (status === "active") return "ativo";
    if (status === "ended") return "encerrado";
    return "sem conversa";
  }

  function render() {
    var label = statusLabel(state.status);
    els.statusChip.textContent = label;

    els.statusDot.className = "sc-dot " + (
      state.status === "active" ? "sc-dot--online" :
      state.status === "waiting" ? "sc-dot--waiting" : "sc-dot--offline");

    if (!state.connected) {
      els.subtitle.textContent = "Reconectando ao servidor…";
      els.banner.hidden = false;
      els.banner.textContent = "Sem conexão em tempo real. As mensagens serão reenviadas quando a conexão voltar.";
    } else if (state.status === "waiting") {
      els.subtitle.textContent = "Aguardando o suporte iniciar a conversa";
      els.banner.hidden = true;
    } else if (state.status === "ended") {
      els.subtitle.textContent = "Conversa encerrada";
      els.banner.hidden = true;
    } else {
      els.subtitle.textContent = "Conversa em andamento";
      els.banner.hidden = true;
    }

    renderMessages();
    renderPending();

    var canSend = state.status === "active" && !state.sending;
    els.send.disabled = !canSend;
    els.input.disabled = state.status === "ended";
    els.attach.disabled = !canSend;
    els.end.disabled = state.status === "none";
  }

  function formatTime(raw) {
    if (!raw) return "";
    var date = new Date(raw);
    if (isNaN(date.getTime())) return "";
    return date.toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
  }

  function renderMessages() {
    var container = els.messages;
    if (!container) return;

    while (container.firstChild) container.removeChild(container.firstChild);

    if (!state.messages.length) {
      var empty = document.createElement("div");
      empty.className = "sc-empty";
      empty.textContent = state.status === "waiting"
        ? "Aguardando o suporte iniciar a conversa…"
        : "Nenhuma mensagem ainda.";
      container.appendChild(empty);
      return;
    }

    state.messages.forEach(function (message) {
      container.appendChild(buildRow(message));
    });

    container.scrollTop = container.scrollHeight;
  }

  function buildRow(message) {
    var own = String(message.authorKind || "").toLowerCase() === "enduser";

    var row = document.createElement("div");
    row.className = "sc-row " + (own ? "sc-row--own" : "sc-row--other");

    var bubble = document.createElement("div");
    bubble.className = "sc-bubble";
    bubble.textContent = message.body || "";
    row.appendChild(bubble);

    var attachments = Array.isArray(message.attachments) ? message.attachments : [];
    if (attachments.length) {
      row.appendChild(buildAttachments(attachments));
    }

    var meta = document.createElement("div");
    meta.className = "sc-meta";
    var who = own ? "Você" : (message.authorName || "Suporte");
    var when = formatTime(message.createdAt);
    meta.textContent = when ? who + " · " + when : who;
    row.appendChild(meta);

    return row;
  }

  function buildAttachments(attachments) {
    var wrap = document.createElement("div");
    wrap.className = "sc-attachments";

    attachments.forEach(function (attachment) {
      var chip = document.createElement("button");
      chip.type = "button";
      chip.className = "sc-attachment";
      chip.textContent = attachment.fileName || "anexo";
      chip.title = "Baixar anexo";
      chip.addEventListener("click", function () { downloadAttachment(attachment); });
      wrap.appendChild(chip);
    });

    return wrap;
  }

  function renderPending() {
    var wrap = els.pending;
    if (!wrap) return;

    while (wrap.firstChild) wrap.removeChild(wrap.firstChild);

    if (!state.pending.length) {
      wrap.hidden = true;
      return;
    }

    wrap.hidden = false;
    state.pending.forEach(function (item) {
      var chip = document.createElement("span");
      chip.className = "sc-pending-chip";
      chip.textContent = item.uploading ? "enviando " + item.fileName + "…" : item.fileName;
      wrap.appendChild(chip);
    });
  }

  function applyState(next) {
    if (!next) return;
    state.status = next.status || "none";
    state.connected = !!next.connected;
    state.messages = Array.isArray(next.messages) ? next.messages : [];
    if (next.supportName || next.ticketTitle) {
      var parts = [];
      if (next.supportName) parts.push("Atendente: " + next.supportName);
      if (next.ticketTitle) parts.push(next.ticketTitle);
      document.title = "Chat — " + parts.join(" · ");
    }
    render();
  }

  // ── Envio ────────────────────────────────────────────────────────────────

  function send() {
    var text = (els.input.value || "").trim();
    var attachmentIDs = state.pending.filter(function (p) { return p.attachmentId; }).map(function (p) { return p.attachmentId; });

    if (!text && !attachmentIDs.length) return;
    if (state.status !== "active" || state.sending) return;

    state.sending = true;
    render();

    call("SupportChatSendMessage", text, attachmentIDs)
      .then(function () {
        els.input.value = "";
        state.pending = [];
        state.sending = false;
        return refresh();
      })
      .catch(function (err) {
        state.sending = false;
        showBanner("Falha ao enviar: " + describe(err));
        render();
      });
  }

  function describe(err) {
    if (!err) return "erro desconhecido";
    if (typeof err === "string") return err;
    return err.message || String(err);
  }

  function showBanner(text) {
    if (!els.banner) return;
    els.banner.hidden = false;
    els.banner.textContent = text;
  }

  function refresh() {
    return call("SupportChatRefresh")
      .then(function (next) { applyState(next); })
      .catch(function (err) {
        // Snapshot falhou (rede): mantém o estado em memória do NATS.
        showBanner("Não foi possível atualizar a conversa: " + describe(err));
      });
  }

  // ── Anexos ───────────────────────────────────────────────────────────────

  function attachFiles(files) {
    Array.prototype.slice.call(files || []).forEach(function (file) { uploadFile(file); });
  }

  function uploadFile(file) {
    if (!file) return;

    if (state.policy && state.policy.enabled === false) {
      showBanner("O envio de anexos está desabilitado pela configuração do servidor.");
      return;
    }

    var allowed = (state.policy && state.policy.allowedContentTypes) || [];
    if (allowed.length && allowed.indexOf(file.type) === -1) {
      showBanner("Tipo de arquivo não permitido: " + (file.type || "desconhecido") + ".");
      return;
    }

    var maxBytes = (state.policy && state.policy.maxFileSizeBytes) || 10485760;
    if (file.size > maxBytes) {
      showBanner("Arquivo maior que o limite permitido (" + Math.round(maxBytes / 1048576) + " MB).");
      return;
    }

    var entry = { fileName: file.name || "print.png", uploading: true, attachmentId: null };
    state.pending.push(entry);
    renderPending();

    var reader = new FileReader();
    reader.onload = function () {
      var result = String(reader.result || "");
      var base64 = result.indexOf(",") >= 0 ? result.split(",")[1] : result;

      call("SupportChatUploadAttachment", file.name || "print.png", file.type || "application/octet-stream", base64)
        .then(function (attachmentId) {
          entry.uploading = false;
          entry.attachmentId = attachmentId;
          renderPending();
        })
        .catch(function (err) {
          entry.failed = true;
          state.pending = state.pending.filter(function (p) { return p !== entry; });
          renderPending();
          showBanner("Falha no anexo: " + describe(err));
        });
    };
    reader.onerror = function () {
      state.pending = state.pending.filter(function (p) { return p !== entry; });
      renderPending();
      showBanner("Não foi possível ler o arquivo.");
    };
    reader.readAsDataURL(file);
  }

  function downloadAttachment(attachment) {
    if (!attachment || !attachment.id) return;

    call("SupportChatDownloadAttachment", attachment.id)
      .then(function (payload) {
        var base64 = payload && payload.base64 ? payload.base64 : "";
        var contentType = (payload && payload.contentType) || "application/octet-stream";
        if (!base64) return;

        var bytes = atob(base64);
        var buffer = new Uint8Array(bytes.length);
        for (var i = 0; i < bytes.length; i++) buffer[i] = bytes.charCodeAt(i);

        var blob = new Blob([buffer], { type: contentType });
        var url = URL.createObjectURL(blob);
        var link = document.createElement("a");
        link.href = url;
        link.download = attachment.fileName || "anexo";
        document.body.appendChild(link);
        link.click();
        document.body.removeChild(link);
        setTimeout(function () { URL.revokeObjectURL(url); }, 30000);
      })
      .catch(function (err) { showBanner("Falha ao baixar o anexo: " + describe(err)); });
  }

  // ── Encerramento ─────────────────────────────────────────────────────────

  function askClose() {
    if (state.status === "ended" || state.status === "none") {
      finishClose();
      return;
    }
    els.confirm.hidden = false;
  }

  function finishClose() {
    if (state.closing) return;
    state.closing = true;

    call("SupportChatConfirmClose")
      .catch(function (err) {
        showBanner("Falha ao encerrar: " + describe(err));
      });
  }

  // ── Wiring ───────────────────────────────────────────────────────────────

  function wireEvents() {
    els.send.addEventListener("click", send);

    els.input.addEventListener("keydown", function (event) {
      if (event.key === "Enter" && !event.shiftKey) {
        event.preventDefault();
        send();
      }
    });

    els.attach.addEventListener("click", function () { els.fileInput.click(); });
    els.fileInput.addEventListener("change", function () {
      attachFiles(els.fileInput.files);
      els.fileInput.value = "";
    });

    document.addEventListener("paste", function (event) {
      if (!event.clipboardData || !event.clipboardData.files || !event.clipboardData.files.length) return;
      event.preventDefault();
      attachFiles(event.clipboardData.files);
    });

    ["dragover", "drop"].forEach(function (name) {
      document.addEventListener(name, function (event) {
        event.preventDefault();
        if (name === "drop" && event.dataTransfer && event.dataTransfer.files) {
          attachFiles(event.dataTransfer.files);
        }
      });
    });

    els.end.addEventListener("click", askClose);
    els.confirmOk.addEventListener("click", function () {
      els.confirm.hidden = true;
      finishClose();
    });
    els.confirmCancel.addEventListener("click", function () { els.confirm.hidden = true; });
  }

  function wireRealtime() {
    if (!window.wails || typeof window.wails.on !== "function") return;

    window.wails.on("supportchat:state", function (payload) { applyState(payload); });
    window.wails.on("supportchat:error", function (message) { showBanner(String(message || "Erro no chat.")); });
    window.wails.on("supportchat:confirm_close", function () { askClose(); });
  }

  function bootstrap() {
    cacheElements();
    wireEvents();
    wireRealtime();

    // O X da janela é interceptado no Go (WindowClosing) e vira supportchat:confirm_close;
    // este handler cobre o fechamento por teclado/atalho do sistema.
    window.addEventListener("beforeunload", function () {
      if (!state.closing) {
        // Melhor esforço: encerra a sessão sem bloquear o fechamento.
        call("SupportChatEnd", "user_closed").catch(function () {});
      }
    });

    call("SupportChatAttachmentPolicyJSON")
      .then(function (policy) { if (policy) state.policy = policy; })
      .catch(function () { /* mantém o default */ })
      .then(function () {
        applyState({ status: "none", connected: false, messages: [] });
        return refresh();
      })
      .catch(function (err) { showBanner("Não foi possível carregar a conversa: " + describe(err)); });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", bootstrap);
  } else {
    bootstrap();
  }
})();
