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
    hasSession: false,
    ticketTitle: "",
    supportName: "",
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
    els.shot = byId("scShotBtn");
    els.fileInput = byId("scFileInput");
    els.end = byId("scEndBtn");
    els.confirm = byId("scConfirm");
    els.confirmTitle = byId("scConfirmTitle");
    els.confirmText = byId("scConfirmText");
    els.confirmOk = byId("scConfirmOk");
    els.confirmCancel = byId("scConfirmCancel");
    els.invite = byId("scInvite");
    els.inviteText = byId("scInviteText");
    els.inviteAccept = byId("scInviteAccept");
    els.inviteReject = byId("scInviteReject");
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
      // Quem encerrou fica registrado na mensagem de sistema; a janela NÃO é
      // fechada sozinha para o usuário continuar lendo o que já foi conversado.
      var reason = "";
      state.messages.forEach(function (message) {
        if (isSystemMessage(message) && message.body) reason = message.body;
      });
      els.subtitle.textContent = reason || "Conversa encerrada";
      els.banner.hidden = true;
    } else {
      els.subtitle.textContent = "Conversa em andamento";
      els.banner.hidden = true;
    }

    renderInvite();
    renderMessages();
    renderPending();

    var canSend = state.status === "active" && !state.sending;
    els.send.disabled = !canSend;
    els.input.disabled = state.status === "ended";
    els.attach.disabled = !canSend;
    if (els.shot) els.shot.disabled = !canSend;
    els.end.disabled = state.status === "none";
    els.end.textContent = state.status === "waiting" ? "Recusar" : "Encerrar";
  }

  function formatTime(raw) {
    if (!raw) return "";
    var date = new Date(raw);
    if (isNaN(date.getTime())) return "";
    return date.toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
  }

  /**
   * A confirmação do convite é renderizada AQUI, na janela do chat — é ela que
   * o usuário atende, no mesmo padrão do modal "Encerrar o chat?".
   */
  function renderInvite() {
    if (!els.invite) return;

    if (state.status !== "waiting") {
      els.invite.hidden = true;
      return;
    }

    var who = state.supportName || "Um atendente do suporte";
    var ticket = state.ticketTitle ? " no chamado '" + state.ticketTitle + "'" : "";
    if (els.inviteText) {
      els.inviteText.textContent = who + " abriu um chat" + ticket + ". Deseja conversar agora?";
    }

    els.invite.hidden = false;
  }

  function respondInvite(accepted) {
    if (els.inviteAccept) els.inviteAccept.disabled = true;
    if (els.inviteReject) els.inviteReject.disabled = true;

    call("SupportChatRespondInvite", accepted, accepted ? "" : "user_denied")
      .then(function () {
        if (els.invite) els.invite.hidden = true;
        if (els.inviteAccept) els.inviteAccept.disabled = false;
        if (els.inviteReject) els.inviteReject.disabled = false;
        if (accepted) return refresh();
        // Recusa: o Go descarta o estado e fecha a janela.
        return null;
      })
      .catch(function (err) {
        if (els.inviteAccept) els.inviteAccept.disabled = false;
        if (els.inviteReject) els.inviteReject.disabled = false;
        showBanner("Não foi possível responder ao convite: " + describe(err));
      });
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

  // Mensagem de SISTEMA (ex.: "Chat de suporte encerrado pelo usuário"): não é
  // fala de ninguém — sai como nota centralizada no meio da conversa.
  function isSystemMessage(message) {
    return String((message && message.authorKind) || "").toLowerCase() === "system";
  }

  function buildSystemRow(message) {
    var note = document.createElement("div");
    note.className = "sc-system";
    note.textContent = message.body || "";
    return note;
  }

  function buildRow(message) {
    if (isSystemMessage(message)) return buildSystemRow(message);

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

  /**
   * Une as mensagens por sequência (e por id, para as otimistas).
   *
   * Com o split serviço/UI o estado chega de DOIS produtores: o serviço (NATS,
   * via IPC) e o REST chamado por esta janela. Substituir a lista faria um
   * snapshot atrasado apagar mensagens já exibidas.
   */
  function mergeMessages(incoming) {
    var seen = {};
    var merged = [];

    function push(message) {
      if (!message) return;
      var key = message.sequenceNumber
        ? "s:" + message.sequenceNumber
        : "id:" + (message.id || "");
      if (key === "id:") return;
      if (seen[key]) return;
      seen[key] = true;
      merged.push(message);
    }

    (state.messages || []).forEach(push);
    (incoming || []).forEach(push);

    // Sequência 0 = bolha otimista (ainda sem número canônico): vai para o FIM.
    // Antes ela ordenava primeiro e a mensagem recém-enviada aparecia no topo da
    // conversa até o snapshot canônico chegar.
    function order(message) {
      return message.sequenceNumber > 0 ? message.sequenceNumber : Number.MAX_SAFE_INTEGER;
    }

    merged.sort(function (a, b) {
      return order(a) - order(b);
    });

    return merged;
  }

  function applyState(next) {
    if (!next) return;
    state.status = next.status || "none";
    // No processo de UI o serviço é quem fala com o NATS: `connected` do serviço
    // local é sempre false e o banner "sem conexão em tempo real" aparecia mesmo
    // com tudo funcionando. A conversa só é "ao vivo" quando existe sessão
    // carregada — o serviço entrega os eventos por IPC.
    // "ended" também é um estado legível (a janela fica aberta em leitura):
    // sem isso o banner "sem conexão em tempo real" cobria a nota de
    // encerramento, porque HasSession já é false no fim da conversa.
    state.connected = !!next.connected || !!next.hasSession || next.status === "ended";
    state.hasSession = !!next.hasSession;
    state.ticketTitle = next.ticketTitle || state.ticketTitle;
    state.supportName = next.supportName || state.supportName;
    state.messages = mergeMessages(Array.isArray(next.messages) ? next.messages : []);
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

  /**
   * Abre a captura ASSISTIDA — o mesmo overlay do chat com a IA: seleção de
   * janela/aplicativo ou região da tela e ferramentas de anotação (setas,
   * marcações, passos).
   *
   * O overlay roda na janela principal (é ela que o Go expande para cobrir o
   * desktop); o resultado volta pelo evento `supportchat:screenshot` e entra no
   * fluxo presigned de anexos.
   */
  function requestScreenshot() {
    if (state.status !== "active" || state.sending) return;

    if (els.shot) els.shot.disabled = true;
    showBanner("Abrindo a captura de tela…");

    call("SupportChatRequestScreenshot")
      .catch(function (err) {
        showBanner("Falha ao abrir a captura de tela: " + describe(err));
      })
      .then(function () {
        if (els.shot) els.shot.disabled = false;
        render();
      });
  }

  /** Anexa um print anotado (data URL) vindo do overlay da captura assistida. */
  function attachScreenshotDataUrl(dataUrl) {
    dataUrl = String(dataUrl || "");
    var comma = dataUrl.indexOf(",");
    if (comma < 0) return;

    var meta = dataUrl.substring(0, comma);
    var base64 = dataUrl.substring(comma + 1);

    var contentType = "image/png";
    var match = /data:([^;]+)/.exec(meta);
    if (match) contentType = match[1];

    var bytes = atob(base64);
    var buffer = new Uint8Array(bytes.length);
    for (var i = 0; i < bytes.length; i++) buffer[i] = bytes.charCodeAt(i);

    var blob = new Blob([buffer], { type: contentType });
    var extension = contentType.indexOf("webp") >= 0 ? "webp" : (contentType.indexOf("jpeg") >= 0 ? "jpg" : "png");
    var name = "print-" + new Date().toISOString().replace(/[:.]/g, "-") + "." + extension;

    uploadFile(new File([blob], name, { type: contentType }));
    showBanner("Print anexado. Escreva a mensagem e envie.");
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

  // "end" encerra a conversa; "invite" recusa o convite ainda não aceito.
  var confirmMode = "end";

  function askClose() {
    if (state.closing) return;

    // Enquanto o convite não foi aceito, fechar a janela significa RECUSAR —
    // não faz sentido perguntar "Encerrar o chat?" para uma conversa que ainda
    // não começou.
    if (state.status === "waiting") {
      confirmMode = "invite";
      applyConfirmCopy();
      if (els.confirm) els.confirm.hidden = false;
      return;
    }

    if (state.status === "ended" || state.status === "none") {
      finishClose();
      return;
    }

    confirmMode = "end";
    applyConfirmCopy();
    if (els.confirm) els.confirm.hidden = false;
  }

  function applyConfirmCopy() {
    if (confirmMode === "invite") {
      if (els.confirmTitle) els.confirmTitle.textContent = "Recusar o convite?";
      if (els.confirmText) {
        els.confirmText.textContent =
          "O chat ainda não começou. Recusar avisa o suporte de que você não pode atender agora.";
      }
      if (els.confirmOk) els.confirmOk.textContent = "Recusar convite";
      if (els.confirmCancel) els.confirmCancel.textContent = "Aceitar e conversar";
      return;
    }

    if (els.confirmTitle) els.confirmTitle.textContent = "Encerrar o chat?";
    if (els.confirmText) {
      els.confirmText.textContent =
        "A janela só fecha quando a conversa termina. Ao encerrar, o suporte é avisado e o histórico fica disponível no chamado.";
    }
    if (els.confirmOk) els.confirmOk.textContent = "Encerrar chat";
    if (els.confirmCancel) els.confirmCancel.textContent = "Continuar conversando";
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

  /**
   * Registra um listener só se o elemento existir.
   *
   * Antes, um único id ausente (ex.: build antigo sem o modal de convite)
   * lançava no meio do wiring e os listeners seguintes nunca eram registrados —
   * o botão "Continuar conversando" ficava inerte.
   */
  function on(el, event, handler) {
    if (el && typeof el.addEventListener === "function") {
      el.addEventListener(event, handler);
    }
  }

  function wireEvents() {
    on(els.send, "click", send);

    on(els.input, "keydown", function (event) {
      if (event.key === "Enter" && !event.shiftKey) {
        event.preventDefault();
        send();
      }
    });

    on(els.shot, "click", requestScreenshot);
    on(els.attach, "click", function () { if (els.fileInput) els.fileInput.click(); });
    on(els.fileInput, "change", function () {
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

    on(els.inviteAccept, "click", function () { respondInvite(true); });
    on(els.inviteReject, "click", function () { respondInvite(false); });

    on(els.end, "click", askClose);

    // O mesmo modal serve para "recusar convite" (confirmMode = invite) e para
    // "encerrar chat" (confirmMode = end).
    on(els.confirmOk, "click", function () {
      els.confirm.hidden = true;
      if (confirmMode === "invite") {
        respondInvite(false);
        return;
      }
      finishClose();
    });

    on(els.confirmCancel, "click", function () {
      els.confirm.hidden = true;
      if (confirmMode === "invite") {
        respondInvite(true);
      }
    });
  }

  function wireRealtime() {
    if (!window.wails || typeof window.wails.on !== "function") return;

    window.wails.on("supportchat:state", function (payload) { applyState(payload); });
    window.wails.on("supportchat:error", function (message) { showBanner(String(message || "Erro no chat.")); });
    window.wails.on("supportchat:confirm_close", function () { askClose(); });

    // Print anotado produzido pelo overlay assistido (janela principal).
    window.wails.on("supportchat:screenshot", function (payload) {
      var dataUrl = payload && payload.dataUrl ? payload.dataUrl : "";
      if (!dataUrl) return;
      attachScreenshotDataUrl(dataUrl);
    });
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
