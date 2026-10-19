"use strict";

// ─────────────────────────────────────────────────────────────────────────────
// Janela independente do chat de suporte (chat vinculado ao chamado).
//
// Regras do recurso refletidas aqui:
//   - a janela é um PAINEL fixo, sem X e sem maximizar: quem fecha é o botão
//     "Encerrar" (com confirmação) — fechar a janela é encerrar o chat;
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
    // capturePending: pedido de print feito e ainda sem resultado (o botão fica
    // desabilitado nesse intervalo; ver requestScreenshot/clearCapturePending).
    capturePending: false,
    closing: false,
    // Comentários do CHAMADO (conversa do ticket), além do chat ao vivo.
    ticketId: "",
    comments: [],
    commentsLoadedFor: "",
    commentsLoading: false,
    commentsOpen: false,
    // Object URL da imagem aberta no visor. `viewerOwnsUrl` distingue a URL
    // criada aqui (revogada ao fechar) da URL reaproveitada do cache de
    // miniaturas (que continua viva para o chip).
    viewerUrl: null,
    viewerOwnsUrl: false,
    // Callback para salvar a imagem ABERTA no visor (recebe askPath). null
    // quando o visor foi aberto sem um anexo associado.
    viewerSave: null
  };

  // ── Cache de miniaturas ───────────────────────────────────────────────────
  //
  // `render()` reconstrói TODAS as mensagens a cada evento de estado (mensagem
  // nova, refresh de até 60s, envio). Sem cache, cada print seria baixado de
  // novo — base64 pela ponte do Go, em tamanho cheio — a cada render, o que
  // pesa numa conversa com várias imagens. O cache é por id de anexo e tem
  // teto: ao sair, a URL é revogada.
  // Watchdog do pedido de print: se nem o resultado nem o aviso de falha
  // chegarem (janela principal fechada, sem UI), o botão não fica travado.
  var captureWatchdog = null;
  var thumbnailUrls = {};
  var thumbnailFailed = {};
  var thumbnailOrder = [];
  var thumbnailLimit = 24;

  // Caminho do arquivo JA salvo, por anexo (chave chat:<id> / comment:<id>).
  // Num segundo clique o anexo nao e baixado de novo: so informamos onde ele
  // esta. pendingAttachmentSaves deduplica cliques enquanto a gravacao corre.
  var savedAttachmentPaths = {};
  var pendingAttachmentSaves = {};

  function byId(id) { return document.getElementById(id); }

  function cacheElements() {
    els.statusDot = byId("scStatusDot");
    els.statusChip = byId("scStatusChip");
    els.subtitle = byId("scSubtitle");
    els.technician = byId("scTechnician");
    els.banner = byId("scBanner");
    els.messages = byId("scMessages");
    els.empty = byId("scEmpty");
    els.pending = byId("scPending");
    els.comments = byId("scComments");
    els.commentsToggle = byId("scCommentsToggle");
    els.commentsCount = byId("scCommentsCount");
    els.commentsList = byId("scCommentsList");
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
    els.viewer = byId("scAttachmentViewer");
    els.viewerImage = byId("scViewerImage");
    els.viewerCaption = byId("scViewerCaption");
    els.viewerClose = byId("scViewerClose");
    els.viewerSave = byId("scViewerSave");
  }

  /**
   * Filtro do seletor de arquivos a partir da política do SERVIDOR
   * (GET .../support-chat/attachment-policy, via SupportChatAttachmentPolicyJSON).
   *
   * Antes o agent devolvia um espelho hardcoded e o `accept` não era aplicado: o
   * usuário escolhia o arquivo para só então descobrir que o tipo era proibido.
   * Paste e drag&drop não passam pelo `accept` — esses continuam validados em
   * uploadFile() com a mesma lista.
   */
  function applyFileInputAccept() {
    if (!els.fileInput) return;

    var allowed = (state.policy && state.policy.allowedContentTypes) || [];
    if (!allowed.length) {
      els.fileInput.removeAttribute("accept");
      return;
    }
    els.fileInput.setAttribute("accept", allowed.join(","));
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

  /**
   * Tipos de imagem que o WebView2 RENDERIZA (miniatura e visor).
   *
   * `image/*` não serve como critério: `image/tiff` e `image/heic` estão na
   * família `image/` mas não renderizam aqui — o anexo cairia no mesmo vazio do
   * PDF (tela sem conteúdo, só o nome). Todo o resto BAIXA/salva em disco, que é
   * o caminho útil. A mesma regra existe no console web
   * (DiscoveryRMM_Site/src/utils/chatAttachment.ts).
   */
  var RENDERABLE_IMAGE_TYPES = [
    "image/png", "image/jpeg", "image/jpg", "image/pjpeg", "image/x-png",
    "image/gif", "image/apng", "image/webp", "image/bmp",
    "image/x-icon", "image/vnd.microsoft.icon", "image/avif", "image/svg+xml"
  ];

  var IMAGE_EXTENSIONS = [
    ".png", ".jpg", ".jpeg", ".jfif", ".gif", ".apng", ".webp",
    ".bmp", ".ico", ".avif", ".svg"
  ];

  // Sem tipo confiável (o picker do Windows devolve "" / octet-stream quando não
  // reconhece a extensão), a extensão do nome decide — igual ao console web.
  function isRenderableImage(contentType, fileName) {
    var type = String(contentType || "").toLowerCase();
    var semi = type.indexOf(";");
    if (semi >= 0) type = type.substring(0, semi);
    type = type.trim();

    if (RENDERABLE_IMAGE_TYPES.indexOf(type) >= 0) return true;
    if (type && type !== "application/octet-stream") return false;

    var name = String(fileName || "").toLowerCase();
    for (var i = 0; i < IMAGE_EXTENSIONS.length; i++) {
      var ext = IMAGE_EXTENSIONS[i];
      if (name.length > ext.length && name.slice(-ext.length) === ext) return true;
    }
    return false;
  }

  /**
   * A conversa ACABOU (e a janela segue aberta só em LEITURA)?
   *
   * `ended` é o estado explícito do serviço, mas o estado local também é
   * limpo depois do fim — e aí a janela volta para "none" carregando as
   * mensagens que o frontend já tinha. Sem esta derivação ela caía no ramo "sem
   * conexão" e mostrava "Reconectando ao servidor… / Sem conexão em tempo real.
   * As mensagens serão reenviadas quando a conexão voltar." — aviso sem sentido
   * numa conversa terminada, porque não há mais nada a reenviar.
   */
  function isConversationOver() {
    return state.status === "ended" ||
      (state.status === "none" && state.messages.length > 0);
  }

  function render() {
    var label = statusLabel(state.status);
    els.statusChip.textContent = label;

    els.statusDot.className = "sc-dot " + (
      state.status === "active" ? "sc-dot--online" :
      state.status === "waiting" ? "sc-dot--waiting" : "sc-dot--offline");

    // Conversa terminada vem ANTES do aviso de conexão: o fim é o que está
    // acontecendo, e reconectar não muda mais nada.
    var over = isConversationOver();

    if (over) {
      // Quem encerrou fica registrado na mensagem de sistema; a janela NÃO é
      // fechada sozinha para o usuário continuar lendo o que já foi conversado.
      var reason = "";
      state.messages.forEach(function (message) {
        if (isSystemMessage(message) && message.body) reason = message.body;
      });
      els.subtitle.textContent = reason || "Conversa encerrada";
      els.banner.hidden = true;
    } else if (!state.connected) {
      els.subtitle.textContent = "Reconectando ao servidor…";
      els.banner.hidden = false;
      els.banner.textContent = "Sem conexão em tempo real. As mensagens serão reenviadas quando a conexão voltar.";
    } else if (state.status === "waiting") {
      els.subtitle.textContent = "Aguardando o suporte iniciar a conversa";
      els.banner.hidden = true;
    } else {
      els.subtitle.textContent = "Conversa em andamento";
      els.banner.hidden = true;
    }

    renderInvite();
    renderMessages();
    renderPending();
    renderTechnician();
    renderComments();

    var canSend = state.status === "active" && !state.sending;
    els.send.disabled = !canSend;
    // Conversa terminada também trava o campo: não há para quem escrever.
    els.input.disabled = over;
    els.attach.disabled = !canSend;
    if (els.shot) els.shot.disabled = !canSend || state.capturePending;

    // Encerrar é o ÚNICO caminho de fechamento (a janela não tem mais o X), por
    // isso o botão fica sempre acionável — inclusive depois de a conversa já ter
    // terminado (pelo suporte, por exemplo), quando ele vira "Fechar".
    els.end.disabled = false;
    els.end.textContent = state.status === "waiting" ? "Recusar" : (over ? "Fechar" : "Encerrar");
    els.end.title = over ? "Fechar a janela do chat" : "Encerrar o chat";
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

  // ── Render da conversa ────────────────────────────────────────────────
  //
  // OTIMIZAÇÃO: `render()` reconstrói tudo a cada evento de estado (mensagem
  // nova, snapshot de até 60s, envio, mudança de status). Sem assinatura, um
  // evento que não muda NADA na conversa (ex.: o snapshot periódico) apagava e
  // recriava todos os nós — perdia a posição de rolagem, a seleção de texto e
  // disparava layout a cada ciclo. Aqui a lista só é reconstruída quando o
  // CONTEÚDO muda, e num único fragmento (um layout em vez de N).
  //
  // A assinatura usa o JSON das mensagens de propósito: o snapshot canônico
  // completa nome/tipo/tamanho do anexo DEPOIS do envelope do NATS, sem a chave
  // da mensagem mudar — comparar só as chaves deixaria o anexo eternamente
  // exibido como "anexo".
  var messagesSignature = "";

  function messagesSignatureOf() {
    return state.status + "|" + JSON.stringify(state.messages);
  }

  /** Usuário está no fim da conversa? (tolerância de 32px para DPI/fontes) */
  function isMessagesAtBottom(container) {
    return container.scrollHeight - container.scrollTop - container.clientHeight <= 32;
  }

  function renderMessages() {
    var container = els.messages;
    if (!container) return;

    var signature = messagesSignatureOf();
    if (signature === messagesSignature) return;
    messagesSignature = signature;

    // Rolagem grudada no fim só quando o usuário JÁ estava no fim: antes cada
    // atualização jogava a conversa para a última mensagem no meio da leitura.
    var stickToBottom = isMessagesAtBottom(container);

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

    var fragment = document.createDocumentFragment();
    state.messages.forEach(function (message) {
      fragment.appendChild(buildRow(message));
    });
    container.appendChild(fragment);

    if (stickToBottom) container.scrollTop = container.scrollHeight;
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

  // BUG CORRIGIDO: o anexo enviado pelo suporte aparecia só como um chip com o
  // nome "anexo" (o envelope do NATS não trazia nome/tipo) e o clique apenas
  // BAIXAVA o arquivo — não havia como VER o print dentro da janela. Agora a
  // imagem ganha miniatura e abre num visor; os demais tipos são salvos em disco
  // pelo backend (sem painel de downloads do Edge).
  function buildAttachments(attachments) {
    var wrap = document.createElement("div");
    wrap.className = "sc-attachments";

    attachments.forEach(function (attachment) {
      var isImage = isRenderableImage(attachment.contentType, attachment.fileName);

      var chip = document.createElement("button");
      chip.type = "button";
      chip.className = "sc-attachment" + (isImage ? " sc-attachment--image" : "");
      chip.title = isImage
        ? "Abrir imagem (Shift+clique para salvar em outro local)"
        : "Salvar anexo (Shift+clique para escolher onde salvar)";

      var label = document.createElement("span");
      label.className = "sc-attachment-name";
      label.textContent = attachment.fileName || "anexo";
      chip.appendChild(label);

      chip.addEventListener("click", function (e) {
        openAttachment(attachment, !!(e && e.shiftKey));
      });
      wrap.appendChild(chip);

      if (isImage) loadAttachmentThumbnail(chip, attachment);
    });

    return wrap;
  }

  /**
   * Miniatura do print dentro do próprio chip.
   *
   * O webview não tem o token do agent: os bytes vêm do Go em base64 (o mesmo
   * caminho do clique). Falha aqui é silenciosa — o chip segue clicável.
   */
  function loadAttachmentThumbnail(chip, attachment) {
    if (!attachment || !attachment.id) return;

    if (thumbnailUrls[attachment.id]) {
      attachThumbnail(chip, thumbnailUrls[attachment.id], attachment);
      return;
    }
    if (thumbnailFailed[attachment.id]) return;

    call("SupportChatDownloadAttachment", attachment.id)
      .then(function (payload) {
        var blob = payloadToBlob(payload, attachment.contentType);
        if (!blob) {
          thumbnailFailed[attachment.id] = true;
          return;
        }

        var url = URL.createObjectURL(blob);
        thumbnailUrls[attachment.id] = url;
        thumbnailOrder.push(attachment.id);
        while (thumbnailOrder.length > thumbnailLimit) {
          var oldest = thumbnailOrder.shift();
          if (thumbnailUrls[oldest]) {
            URL.revokeObjectURL(thumbnailUrls[oldest]);
            delete thumbnailUrls[oldest];
          }
        }

        attachThumbnail(chip, url, attachment);
      })
      .catch(function () {
        // Sem miniatura o chip continua clicável; não repetimos a tentativa a
        // cada render (o clique em "abrir" tenta de novo).
        thumbnailFailed[attachment.id] = true;
      });
  }

  function attachThumbnail(chip, url, attachment) {
    var img = document.createElement("img");
    img.alt = attachment.fileName || "print";
    img.src = url;
    chip.insertBefore(img, chip.firstChild);
    chip.className += " sc-attachment--thumb";
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

  // ── Técnico (atendente) que abriu a conversa ─────────────────────────────

  function renderTechnician() {
    if (!els.technician) return;

    var name = String(state.supportName || "").trim();
    if (!name) {
      els.technician.hidden = true;
      els.technician.textContent = "";
      return;
    }

    els.technician.hidden = false;
    els.technician.textContent = "";

    var label = document.createElement("span");
    label.textContent = "Técnico: ";
    var strong = document.createElement("strong");
    strong.textContent = name;

    els.technician.appendChild(label);
    els.technician.appendChild(strong);
  }

  // ── Comentários do CHAMADO (conversa do ticket) ──────────────────────────
  // São um canal separado do chat ao vivo: aqui o usuário vê o histórico do
  // chamado e quem comentou. Notas internas do suporte NÃO são exibidas.

  function isOwnComment(comment) {
    return String((comment && comment.author) || "").trim().toLowerCase() === "agent";
  }

  function visibleComments(list) {
    return (Array.isArray(list) ? list : []).filter(function (comment) {
      return comment && !comment.isInternal;
    });
  }

  function formatCommentStamp(raw) {
    if (!raw) return "";
    var date = new Date(raw);
    if (isNaN(date.getTime())) return "";
    try {
      var day = date.toLocaleDateString("pt-BR");
      var time = formatTime(raw);
      return time ? day + " " + time : day;
    } catch (err) {
      return formatTime(raw);
    }
  }

  /**
   * Salva em disco um anexo de COMENTÁRIO pelo backend (grava e devolve o
   * caminho). Não usa mais Blob + <a download>: o painel de downloads do Edge
   * cobria a conversa e cada clique baixava uma cópia nova.
   *
   * O caminho fica em cache por anexo: repetir o clique só avisa onde o arquivo
   * está. Shift+clique abre o "Salvar como" e grava uma nova cópia se o usuário
   * quiser.
   */
  function saveCommentAttachment(comment, attachment, askPath) {
    var ticketId = state.ticketId;
    if (!ticketId || !comment || !comment.id || !attachment || !attachment.id) {
      showBanner("Anexo indisponível: o servidor não informou o identificador do arquivo.");
      return Promise.resolve(null);
    }

    var key = "comment:" + attachment.id;
    var fileName = attachment.fileName || "anexo";

    if (!askPath && savedAttachmentPaths[key]) {
      showBanner("Anexo já salvo em: " + savedAttachmentPaths[key]);
      return Promise.resolve(savedAttachmentPaths[key]);
    }
    if (!askPath && pendingAttachmentSaves[key]) {
      return pendingAttachmentSaves[key];
    }
    if (!askPath) showBanner("Salvando anexo…");

    var start;
    if (askPath) {
      start = call("ChooseTicketAttachmentSavePath", fileName, "Salvar anexo")
        .then(function (destPath) { return { destPath: destPath || "", asked: true }; });
    } else {
      start = Promise.resolve({ destPath: "", asked: false });
    }

    var request = start.then(function (choice) {
      if (choice.asked && !choice.destPath) return null;
      if (choice.destPath) {
        return call("SaveTicketCommentAttachmentAs", ticketId, comment.id, attachment.id, fileName, choice.destPath);
      }
      return call("SaveTicketCommentAttachment", ticketId, comment.id, attachment.id, fileName);
    }).then(function (path) {
      if (!path) return null;
      savedAttachmentPaths[key] = path;
      showBanner("Anexo salvo em: " + path);
      return path;
    }).catch(function (err) {
      showBanner("Não foi possível salvar o anexo do comentário: " + describe(err));
      return null;
    }).then(function (result) {
      if (!askPath) delete pendingAttachmentSaves[key];
      return result;
    });

    if (!askPath) pendingAttachmentSaves[key] = request;
    return request;
  }

  /**
   * Anexos do COMENTÁRIO na conversa: mesma linguagem do anexo do chat ao
   * vivo — imagem ganha MINIATURA (abre no visor) e os demais são salvos em
   * disco pelo backend.
   */
  function buildCommentAttachments(comment) {
    var attachments = Array.isArray(comment.attachments) ? comment.attachments : [];
    if (!attachments.length) return null;

    var wrap = document.createElement("div");
    wrap.className = "sc-comment-attachments";

    attachments.forEach(function (attachment) {
      var isImage = isRenderableImage(attachment.contentType, attachment.fileName);

      var chip = document.createElement("button");
      chip.type = "button";
      chip.className = "sc-attachment" + (isImage ? " sc-attachment--image" : "");
      chip.title = isImage
        ? "Abrir imagem (Shift+clique para salvar em outro local)"
        : "Salvar anexo (Shift+clique para escolher onde salvar)";

      var label = document.createElement("span");
      label.className = "sc-attachment-name";
      label.textContent = attachment.fileName || "anexo";
      chip.appendChild(label);

      chip.addEventListener("click", function (e) {
        var askPath = !!(e && e.shiftKey);
        if (!askPath && thumbnailUrls[attachment.id]) {
          showAttachmentViewer(thumbnailUrls[attachment.id], attachment.fileName || "imagem", false,
            function () { saveCommentAttachment(comment, attachment, false); },
            function (askPath) { return saveCommentAttachment(comment, attachment, askPath); });
          return;
        }
        saveCommentAttachment(comment, attachment, askPath);
      });

      wrap.appendChild(chip);

      if (isImage) loadCommentThumbnail(chip, comment, attachment);
    });

    return wrap;
  }

  /** Miniatura de um anexo de comentário (mesmo cache do anexo do chat). */
  function loadCommentThumbnail(chip, comment, attachment) {
    if (!comment || !attachment || !attachment.id || !state.ticketId) return;

    if (thumbnailUrls[attachment.id]) {
      attachThumbnail(chip, thumbnailUrls[attachment.id], attachment);
      return;
    }
    if (thumbnailFailed[attachment.id]) return;

    call("DownloadTicketCommentAttachment", state.ticketId, comment.id, attachment.id)
      .then(function (payload) {
        var blob = payloadToBlob(payload, attachment.contentType);
        if (!blob) {
          thumbnailFailed[attachment.id] = true;
          return;
        }

        var url = URL.createObjectURL(blob);
        thumbnailUrls[attachment.id] = url;
        thumbnailOrder.push(attachment.id);
        while (thumbnailOrder.length > thumbnailLimit) {
          var oldest = thumbnailOrder.shift();
          if (thumbnailUrls[oldest]) {
            URL.revokeObjectURL(thumbnailUrls[oldest]);
            delete thumbnailUrls[oldest];
          }
        }

        attachThumbnail(chip, url, attachment);
      })
      .catch(function () {
        thumbnailFailed[attachment.id] = true;
      });
  }

  function buildCommentRow(comment) {
    var own = isOwnComment(comment);
    var deleted = !!(comment && comment.deletedAt);

    var row = document.createElement("div");
    row.className = "sc-comment " + (own ? "sc-comment--own" : "sc-comment--other") +
      (deleted ? " sc-comment--deleted" : "");

    var bubble = document.createElement("div");
    bubble.className = "sc-bubble";
    bubble.textContent = comment.content || "";
    if (deleted) {
      var tag = document.createElement("span");
      tag.className = "sc-comment-deleted";
      tag.textContent = " (comentário excluído)";
      bubble.appendChild(tag);
    }
    row.appendChild(bubble);

    var attachmentsWrap = buildCommentAttachments(comment);
    if (attachmentsWrap) row.appendChild(attachmentsWrap);

    var meta = document.createElement("div");
    meta.className = "sc-meta";
    var who = own ? "Você" : (comment.author || "Suporte");
    var when = formatCommentStamp(comment.createdAt);
    meta.textContent = when ? who + " · " + when : who;
    row.appendChild(meta);

    return row;
  }

  function commentPlaceholder(text) {
    var el = document.createElement("div");
    el.className = "sc-comments-empty";
    el.textContent = text;
    return el;
  }

  function renderComments() {
    if (!els.comments) return;

    if (!state.ticketId) {
      els.comments.hidden = true;
      return;
    }

    els.comments.hidden = false;

    if (els.commentsCount) {
      els.commentsCount.hidden = !state.comments.length;
      els.commentsCount.textContent = state.comments.length ? String(state.comments.length) : "";
    }
    if (els.commentsToggle) {
      els.commentsToggle.setAttribute("aria-expanded", state.commentsOpen ? "true" : "false");
    }
    if (!els.commentsList) return;

    els.commentsList.hidden = !state.commentsOpen;
    if (!state.commentsOpen) return;

    while (els.commentsList.firstChild) els.commentsList.removeChild(els.commentsList.firstChild);

    if (state.commentsLoading && !state.comments.length) {
      els.commentsList.appendChild(commentPlaceholder("Carregando comentários…"));
      return;
    }
    if (!state.comments.length) {
      els.commentsList.appendChild(commentPlaceholder("Nenhum comentário no chamado ainda."));
      return;
    }

    state.comments.forEach(function (comment) {
      els.commentsList.appendChild(buildCommentRow(comment));
    });
  }

  /**
   * Carrega os comentários do chamado vinculado à sessão do chat.
   *
   * O parâmetro force ignora o cache por ticket: usado ao abrir o painel, para
   * o usuário conseguir atualizar a conversa sem reiniciar o chat.
   */
  function loadTicketComments(force) {
    var ticketId = state.ticketId;
    if (!ticketId) return Promise.resolve();
    if (!force && state.commentsLoadedFor === ticketId) return Promise.resolve();

    state.commentsLoading = true;
    renderComments();

    return call("GetTicketComments", ticketId)
      .then(function (list) {
        if (state.ticketId !== ticketId) return;
        state.commentsLoadedFor = ticketId;
        state.comments = visibleComments(list);
      })
      .catch(function (err) {
        if (state.ticketId !== ticketId) return;
        state.commentsLoadedFor = "";
        showBanner("Não foi possível carregar os comentários do chamado: " + describe(err));
      })
      .then(function () {
        if (state.ticketId !== ticketId) return;
        state.commentsLoading = false;
        renderComments();
      });
  }

  function toggleComments() {
    state.commentsOpen = !state.commentsOpen;
    renderComments();
    if (state.commentsOpen) loadTicketComments(true);
  }

  /**
   * Chave de identidade da mensagem na união de snapshots.
   *
   * `clientMessageId` vem primeiro porque é a chave de idempotência do EMISSOR:
   * é ela que casa a bolha otimista (ainda sem sequência) com a mensagem
   * persistida. Antes a chave era só sequência/id, as duas cópias não casavam e
   * a mensagem própria aparecia DUPLICADA na janela.
   */
  function messageKey(message) {
    if (!message) return "";
    var clientID = String(message.clientMessageId || "");
    if (clientID) return "c:" + clientID;
    if (message.sequenceNumber) return "s:" + message.sequenceNumber;
    return message.id ? "id:" + message.id : "";
  }

  /**
   * Une as mensagens do serviço (incoming) com as já exibidas.
   *
   * Com o split serviço/UI o estado chega de DOIS produtores: o serviço (NATS,
   * via IPC) e o REST chamado por esta janela — por isso a UNIÃO da lista, e
   * não a substituição.
   *
   * BUG CORRIGIDO: o snapshot CANÔNICO (incoming) precisa vencer. A mensagem
   * chega pelo NATS só com os IDs dos anexos e o snapshot REST seguinte completa
   * nome/tipo no serviço; como a versão antiga era mantida por ser a primeira,
   * o anexo enviado pelo suporte nunca aparecia na conversa ao vivo — só
   * fechando e reabrindo a janela.
   */
  function mergeMessages(incoming) {
    var seen = {};
    var merged = [];

    function push(message) {
      if (!message) return;
      var key = messageKey(message);
      if (!key) return;
      if (seen[key]) return;
      seen[key] = true;
      merged.push(message);
    }

    (incoming || []).forEach(push);
    (state.messages || []).forEach(push);

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

    // Comentários do chamado: um ticket novo invalida o cache do anterior.
    var nextTicketId = String(next.ticketId || "");
    if (nextTicketId && nextTicketId !== state.ticketId) {
      state.ticketId = nextTicketId;
      state.comments = [];
      state.commentsLoadedFor = "";
      state.commentsOpen = false;
    } else if (nextTicketId) {
      state.ticketId = nextTicketId;
    }
    state.messages = mergeMessages(Array.isArray(next.messages) ? next.messages : []);

    // Convite pendente ganha título próprio: o tooltip do botão na barra de
    // tarefas mostra o título da janela e é o que sobra quando o usuário está
    // em outro aplicativo em tela cheia.
    if (state.status === "waiting") {
      document.title = "Convite do suporte — responda";
    } else if (next.supportName || next.ticketTitle) {
      var parts = [];
      if (next.supportName) parts.push("Atendente: " + next.supportName);
      if (next.ticketTitle) parts.push(next.ticketTitle);
      document.title = "Chat — " + parts.join(" · ");
    } else if (document.title !== "Chat com o suporte") {
      // VOLTA ao título normal quando o convite sai de cena: sem este else o
      // título "Convite do suporte — responda" ficava para sempre numa conversa
      // cujo snapshot não trouxe atendente/chamado.
      document.title = "Chat com o suporte";
    }
    render();

    // O ticket vem do snapshot do chat; os comentários entram assim que ele é
    // conhecido (e uma única vez por ticket).
    if (state.ticketId) loadTicketComments(false);
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

  /**
   * Anexa arquivos escolhidos/colados/arrastados.
   *
   * BUG DE UX: paste e drag&drop não passam pelo botão de anexo (que fica
   * desabilitado quando a conversa não está ativa). Anexar antes do aceite ou
   * depois do fim tentava o upload, o servidor recusava (presign exige sessão
   * ativa) e o usuário recebia um erro de rede genérico sem entender a causa.
   */
  function attachFiles(files) {
    if (state.status !== "active") {
      showBanner(state.status === "waiting"
        ? "Aceite o convite do suporte antes de enviar arquivos."
        : "A conversa não está ativa: não há como enviar arquivos agora.");
      return;
    }
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
          showBanner("Não foi possível enviar o anexo: " + describe(err));
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
    if (state.status !== "active" || state.sending || state.capturePending) return;

    state.capturePending = true;
    if (els.shot) els.shot.disabled = true;
    showBanner("Abrindo a captura de tela…");

    // Watchdog: sem ele, uma captura que não devolve nem print nem falha
    // deixaria o botão desabilitado para sempre. O caminho normal é o evento
    // (supportchat:screenshot / *_failed); isto cobre "janela principal sem UI".
    if (captureWatchdog) clearTimeout(captureWatchdog);
    captureWatchdog = setTimeout(function () {
      captureWatchdog = null;
      if (state.closing) {
        state.capturePending = false;
        return;
      }
      if (!state.capturePending) return;
      clearCapturePending();
      showBanner("A captura de tela não respondeu. Tente novamente.");
    }, 60000);

    call("SupportChatRequestScreenshot")
      .catch(function (err) {
        clearCapturePending();
        showBanner("Falha ao abrir a captura de tela: " + describe(err));
      });
  }

  // clearCapturePending libera o botão de print. Ele fica desabilitado entre o
  // pedido e o resultado (eventos supportchat:screenshot / *_failed): era o
  // clique repetido durante a captura que gerava dezenas de pedidos
  // concorrentes — e, no backend, o erro "sessão de captura expirada ou
  // inválida" na hora de concluir a seleção.
  function clearCapturePending() {
    state.capturePending = false;
    if (captureWatchdog) {
      clearTimeout(captureWatchdog);
      captureWatchdog = null;
    }
    // Janela já encerrada: o DOM foi desmontado, render() estouraria.
    if (state.closing) return;
    render();
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

  /**
   * Abre um anexo da conversa: imagem no visor da própria janela, qualquer
   * outro tipo é salvo em disco pelo backend.
   *
   * BUG CORRIGIDO: um anexo sem identificador (mensagem antiga/replay) morria
   * em silêncio no clique — o usuário não tinha retorno nenhum.
   */
  function openAttachment(attachment, askPath) {
    if (!attachment || !attachment.id) {
      showBanner("Anexo indisponível: o servidor não informou o identificador do arquivo.");
      return;
    }

    var isImage = isRenderableImage(attachment.contentType, attachment.fileName);

    // Miniatura já baixada e já é imagem: abre direto, sem segundo download.
    if (!askPath && isImage && thumbnailUrls[attachment.id]) {
      showAttachmentViewer(thumbnailUrls[attachment.id], attachment.fileName || "imagem", false,
        function () { saveSupportChatAttachment(attachment, false); },
        function (askPath) { return saveSupportChatAttachment(attachment, askPath); });
      return;
    }

    // PDF, log, zip, csv, executável, anexo sem tipo e formatos de imagem que o
    // WebView2 não renderiza: grava DIRETO em disco pelo Go (sem o painel de
    // downloads do Edge e sem baixar de novo a cada clique).
    if (!isImage || askPath) {
      saveSupportChatAttachment(attachment, askPath);
      return;
    }

    call("SupportChatDownloadAttachment", attachment.id)
      .then(function (payload) {
        var blob = payloadToBlob(payload, attachment.contentType);
        if (!blob) {
          showBanner("O anexo veio vazio do servidor.");
          return;
        }

        showAttachmentViewer(URL.createObjectURL(blob), attachment.fileName || "imagem", true,
          function () { saveSupportChatAttachment(attachment, false); },
          function (askPath) { return saveSupportChatAttachment(attachment, askPath); });
      })
      .catch(function (err) { showBanner("Falha ao abrir o anexo: " + describe(err)); });
  }

  /**
   * Salva em disco um anexo do CHAT AO VIVO pelo backend. O caminho fica em
   * cache: repetir o clique não baixa outra cópia (só mostra onde está).
   * Shift+clique abre o "Salvar como" para gravar em outro local.
   */
  function saveSupportChatAttachment(attachment, askPath) {
    if (!attachment || !attachment.id) {
      showBanner("Anexo indisponível: o servidor não informou o identificador do arquivo.");
      return Promise.resolve(null);
    }

    var key = "chat:" + attachment.id;
    var fileName = attachment.fileName || "anexo";

    if (!askPath && savedAttachmentPaths[key]) {
      showBanner("Anexo já salvo em: " + savedAttachmentPaths[key]);
      return Promise.resolve(savedAttachmentPaths[key]);
    }
    if (!askPath && pendingAttachmentSaves[key]) {
      return pendingAttachmentSaves[key];
    }
    if (!askPath) showBanner("Salvando anexo…");

    var start;
    if (askPath) {
      start = call("ChooseSupportChatAttachmentSavePath", fileName, "Salvar anexo")
        .then(function (destPath) { return { destPath: destPath || "", asked: true }; });
    } else {
      start = Promise.resolve({ destPath: "", asked: false });
    }

    var request = start.then(function (choice) {
      if (choice.asked && !choice.destPath) return null;
      if (choice.destPath) {
        return call("SupportChatSaveAttachmentAs", attachment.id, fileName, choice.destPath);
      }
      return call("SupportChatSaveAttachment", attachment.id, fileName);
    }).then(function (path) {
      if (!path) return null;
      savedAttachmentPaths[key] = path;
      showBanner("Anexo salvo em: " + path);
      return path;
    }).catch(function (err) {
      showBanner("Falha ao salvar o anexo: " + describe(err));
      return null;
    }).then(function (result) {
      if (!askPath) delete pendingAttachmentSaves[key];
      return result;
    });

    if (!askPath) pendingAttachmentSaves[key] = request;
    return request;
  }

  /** Converte o payload base64 devolvido pelo Go em Blob tipado. */
  function payloadToBlob(payload, fallbackContentType) {
    var base64 = payload && payload.base64 ? payload.base64 : "";
    if (!base64) return null;

    var contentType = (payload && payload.contentType) || fallbackContentType || "application/octet-stream";
    var bytes = atob(base64);
    var buffer = new Uint8Array(bytes.length);
    for (var i = 0; i < bytes.length; i++) buffer[i] = bytes.charCodeAt(i);
    return new Blob([buffer], { type: contentType });
  }

  function showAttachmentViewer(url, fileName, owned, onRenderError, onSave) {
    closeAttachmentViewer();
    state.viewerUrl = url;
    state.viewerOwnsUrl = !!owned;
    state.viewerSave = typeof onSave === "function" ? onSave : null;
    if (els.viewerSave) {
      els.viewerSave.hidden = !state.viewerSave;
      els.viewerSave.disabled = !state.viewerSave;
    }
    if (els.viewerCaption) els.viewerCaption.textContent = fileName || "";
    if (els.viewerImage) {
      els.viewerImage.src = url;
      els.viewerImage.alt = fileName || "anexo";
      // Rede de segurança: um arquivo que passa por imagem mas não renderiza
      // (corrompido) deixaria o visor VAZIO, sem caminho para o arquivo. Entrega
      // o download e fecha o visor em vez de travar aqui.
      els.viewerImage.onerror = function () {
        var downloadUrl = state.viewerUrl;
        if (!downloadUrl) return;
        var ownsViewerUrl = state.viewerOwnsUrl;
        // Se a URL veio do cache de miniaturas, ela é lixo (não renderiza):
        // some com a entrada para o chip não repetir o mesmo caminho. A URL
        // criada pelo próprio visor continua marcada como "owned" para o
        // closeAttachmentViewer revogá-la (sem isso o Blob vazaria).
        Object.keys(thumbnailUrls).forEach(function (id) {
          if (thumbnailUrls[id] === downloadUrl) delete thumbnailUrls[id];
        });
        closeAttachmentViewer();
        // URL do cache de miniaturas não é revogada pelo close (o chip poderia
        // reusá-la); como a entrada foi descartada por não renderizar, libera.
        if (!ownsViewerUrl) URL.revokeObjectURL(downloadUrl);
        // Entrega o arquivo pelo backend (grava em disco e informa o caminho)
        // em vez de disparar o painel de downloads do Edge.
        if (typeof onRenderError === "function") onRenderError();
      };
    }
    if (els.viewer) els.viewer.hidden = false;
  }

  function closeAttachmentViewer() {
    // Só revoga a URL criada pelo próprio visor; a do cache de miniaturas
    // continua em uso pelo chip.
    if (state.viewerUrl && state.viewerOwnsUrl) {
      URL.revokeObjectURL(state.viewerUrl);
    }
    state.viewerUrl = null;
    state.viewerOwnsUrl = false;
    state.viewerSave = null;
    if (els.viewerSave) {
      els.viewerSave.hidden = true;
      els.viewerSave.disabled = true;
    }
    if (els.viewerImage) {
      // Limpa o handler ANTES de tirar o src: remover o src dispara "error" em
      // alguns motores e o fallback de download entraria em loop.
      els.viewerImage.onerror = null;
      els.viewerImage.removeAttribute("src");
    }
    if (els.viewer) els.viewer.hidden = true;
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

    // Sem sessão (janela órfã) ou conversa já terminada — inclusive pelo
    // suporte: não há o que confirmar, o botão apenas fecha a janela.
    if (state.status === "none" || isConversationOver()) {
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
        "Encerrar fecha esta janela e avisa o suporte. O histórico da conversa fica disponível no chamado.";
    }
    if (els.confirmOk) els.confirmOk.textContent = "Encerrar chat";
    if (els.confirmCancel) els.confirmCancel.textContent = "Continuar conversando";
  }

  function finishClose() {
    if (state.closing) return;
    state.closing = true;

    call("SupportChatConfirmClose")
      .catch(function (err) {
        // A janela não tem botão de fechar: se o binding falhar, travar o estado
        // em "closing" deixaria o usuário preso numa janela que não fecha por
        // nenhum caminho. Libera nova tentativa.
        state.closing = false;
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

    on(els.commentsToggle, "click", toggleComments);
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

    // Visor de imagem: "Salvar imagem" grava o arquivo aberto pelo backend
    // (Shift+clique abre o "Salvar como"). O clique não pode fechar o visor.
    on(els.viewerSave, "click", function (event) {
      event.stopPropagation();
      if (typeof state.viewerSave !== "function") return;
      var request = state.viewerSave(!!(event && event.shiftKey));
      if (request && typeof request.then === "function") {
        request.then(function (path) {
          if (!path || !els.viewerCaption) return;
          if (els.viewer && els.viewer.hidden) return;
          els.viewerCaption.textContent = "Salvo em: " + path;
        });
      }
    });

    // Visor de imagem: fecha no X, no clique fora da figura e no Esc.
    on(els.viewer, "click", function (event) {
      if (event.target === els.viewer || event.target === els.viewerClose) {
        closeAttachmentViewer();
      }
    });
    document.addEventListener("keydown", function (event) {
      if (event.key === "Escape") closeAttachmentViewer();
    });

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
      clearCapturePending();
      attachScreenshotDataUrl(dataUrl);
    });

    // O overlay não produziu print (sessão inválida, cancelamento, erro de
    // encode): libera o botão em vez de deixá-lo preso até o watchdog.
    window.wails.on("supportchat:screenshot_failed", function (payload) {
      var reason = payload && payload.error ? String(payload.error) : "";
      clearCapturePending();
      showBanner("Não foi possível capturar a tela." + (reason ? " " + reason : ""));
    });
  }

  function bootstrap() {
    cacheElements();
    wireEvents();
    wireRealtime();

    // A janela não tem botão de fechar: o caminho normal de saída é o botão
    // "Encerrar". Um fechamento do SISTEMA (Alt+F4) continua sendo interceptado
    // no Go (WindowClosing) e vira supportchat:confirm_close; este handler só
    // cobre o fechamento que não passa pelo hook.
    window.addEventListener("beforeunload", function () {
      if (!state.closing) {
        // Melhor esforço: encerra a sessão sem bloquear o fechamento.
        call("SupportChatEnd", "user_closed").catch(function () {});
      }
    });

    // Política de anexos: NÃO bloqueia a abertura da janela (é uma consulta ao
    // servidor, sujeita a rede degradada). O default é permissivo e o servidor
    // valida de verdade no presign/complete.
    call("SupportChatAttachmentPolicyJSON")
      .then(function (policy) {
        if (!policy) return;
        state.policy = policy;
        applyFileInputAccept();
        render();
      })
      .catch(function () { /* mantém o default */ });

    applyState({ status: "none", connected: false, messages: [] });
    refresh().catch(function (err) {
      showBanner("Não foi possível carregar a conversa: " + describe(err));
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", bootstrap);
  } else {
    bootstrap();
  }
})();
