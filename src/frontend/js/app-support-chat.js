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
  // EMBEDDED_IN_MAIN_WINDOW: o chat roda na janela PRINCIPAL (aba), não numa
  // janela dedicada. Muda comportamentos de ciclo de vida: beforeunload não
  // encerra a sessão (close-to-tray da principal não é fim de conversa) e a
  // aba é surface do menu, não janela.
  var EMBEDDED_IN_MAIN_WINDOW = true;
  var api = function () { return window.go && window.go.app && window.go.app.App; };

  var els = {};
  var state = {
    status: "none",
    connected: false,
    // link: estado REAL do canal com o servidor, em quatro valores:
    //   "opening" — janela recém-aberta, ainda sem o primeiro snapshot com
    //               sessão: a conversa está vindo, NÃO é "sem conexão";
    //   "live"    — conversa carregada e canal saudável;
    //   "offline" — o BACKEND confirmou que está fora E o último snapshot
    //               falhou (só então o aviso de reconexão é verdadeiro);
    //   "over"    — conversa encerrada (nada a reenviar).
    // BUG RELATADO ("abro o chat e aparece Sem conexão em tempo real"): o
    // banner dependia de um único boolean `connected` recalculado a cada
    // evento — e o processo de UI nunca tem NATS próprio, então qualquer
    // snapshot que chegasse sem sessão (corrida do aceite × /active, evento
    // atrasado) pintava o aviso numa conversa que estava aberta.
    link: "opening",
    // openDeadlineMs: até quando a janela aceita o estado "opening" sem
    // avisar. Depois disso, sem conversa carregada, ela avisa de verdade.
    openDeadlineMs: 0,
    lastSnapshotError: false,
    // liveSession: sessão em que a conversa já foi vista viva. Monotônico —
    // um snapshot posterior sem sessão da MESMA sessão não derruba o link.
    liveSession: "",
    // sessionState: último status visto por sessão (ver applyState).
    sessionState: {},
    // chatSessionId: sessão cujo transcript está na tela (reuso da janela).
    chatSessionId: "",
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
    viewerSave: null,
    // viewerDoc: anexo NÃO-imagem aberto no cartão de documento (PDF, planilha
    // etc.). Preenchido por showDocumentViewer; null no modo imagem.
    viewerDoc: null
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
    // Composer: na aba embutida é o wrapper #scComposer; na janela dedicada
    // (rollback) é o <footer class="support-chat-composer">, sem id.
    els.composer = byId("scComposer") || document.querySelector(".support-chat-composer");
    els.ended = byId("scEndedNotice");
    els.endedClose = byId("scEndedClose");
    els.endedSupport = byId("scEndedSupport");
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
    els.viewerDoc = byId("scViewerDoc");
    els.viewerDocName = byId("scViewerDocName");
    els.viewerOpen = byId("scViewerOpen");
    els.viewerFolder = byId("scViewerFolder");
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
    // Conversa terminada vem ANTES do aviso de conexão: o fim é o que está
    // acontecendo, e reconectar não muda mais nada.
    var over = isConversationOver();
    // O chip deriva de `over`, não do status bruto: um snapshot vazio depois do
    // fim deixava o status local em "none" e o chip piscava para "sem conversa"
    // por cima do histórico (relato de "friz" na statusbar).
    var label = over ? statusLabel("ended") : statusLabel(state.status);
    if (els.statusChip) els.statusChip.textContent = label;

    // statusDot só existe na JANELA DEDICADA (rollback); na aba embutida o
    // estado vai pelo chip/subtitle da statusbar.
    if (els.statusDot) {
      els.statusDot.className = "sc-dot " + (
        over ? "sc-dot--offline" :
        state.status === "active" ? "sc-dot--online" :
        state.status === "waiting" ? "sc-dot--waiting" : "sc-dot--offline");
    }
    // Rede de segurança do link: se o ramo que colocou a janela em
    // "opening" já venceu (OPENING_WINDOW_MS) e nenhum snapshot trouxe a
    // conversa, o aviso aparece mesmo sem uma chamada de render() nova.
    // Enquanto o link está "live" isto é no-op.
    if (!over && state.link !== "live") {
      recomputeLink();
    }

    // O técnico aparece UMA vez, no elemento próprio da statusbar
    // (#scTechnician). Antes o subtitle repetia "— Técnico: nome" e o nome
    // aparecia duas vezes na mesma linha.
    if (over) {
      // Quem encerrou fica registrado na mensagem de sistema; a aba NÃO é
      // fechada sozinha para o usuário continuar lendo o que já foi conversado.
      var reason = "";
      state.messages.forEach(function (message) {
        if (isSystemMessage(message) && message.body) reason = message.body;
      });
      // FIX 21/10 (2): o banner de sistema repetia "Conversa encerrada" em cima
      // do histórico (flutuando sobre as mensagens). O estado já está no chip e
      // no subtitle da statusbar — o banner só tem utilidade para avisos de
      // conexão/envio.
      if (els.banner) els.banner.hidden = true;
      if (els.subtitle) {
        els.subtitle.textContent = "Conversa encerrada";
        els.subtitle.title = reason || "";
      }
    } else if (state.link === "opening") {
      // (contrato do teste: o literal da abertura permanece exato)
      if (els.subtitle) {
        els.subtitle.textContent = "Abrindo a conversa…";
        els.subtitle.title = "";
      }
      if (els.banner) els.banner.hidden = true;
    } else if (state.link === "idle") {
      if (els.subtitle) {
        els.subtitle.textContent = "Sem conversa";
        els.subtitle.title = "";
      }
      if (els.banner) els.banner.hidden = true;
    } else if (state.link === "offline") {
      if (els.subtitle) {
        els.subtitle.textContent = "Reconectando ao servidor…";
        els.subtitle.title = "";
      }
      if (els.banner) {
        els.banner.hidden = false;
        els.banner.textContent = "Sem conexão em tempo real. As mensagens serão reenviadas quando a conexão voltar.";
      }
    } else if (state.status === "waiting") {
      if (els.subtitle) {
        els.subtitle.textContent = "Aguardando o suporte iniciar a conversa";
        els.subtitle.title = "";
      }
      if (els.banner) els.banner.hidden = true;
    } else {
      if (els.subtitle) {
        els.subtitle.textContent = "Conversa em andamento";
        els.subtitle.title = "";
      }
      if (els.banner) els.banner.hidden = true;
    }

    renderInvite();
    renderMessages();
    renderPending();
    renderTechnician();
    renderComments();

    // Conversa encerrada: o composer sai de cena. Antes ele continuava na tela
    // com o textarea desabilitado, parecendo que "travou" — o usuário clicava e
    // digitava sem nenhum retorno. A faixa de leitura assume o rodapé.
    if (els.ended) els.ended.hidden = !over;
    if (els.composer) els.composer.hidden = over;

    var canSend = state.status === "active" && !state.sending;
    if (els.send) els.send.disabled = !canSend;
    // Conversa terminada também trava o campo: não há para quem escrever.
    if (els.input) els.input.disabled = over;
    if (els.attach) els.attach.disabled = !canSend;
    if (els.shot) els.shot.disabled = !canSend || state.capturePending;
    if (els.end) {
      // Encerrar é o ÚNICO caminho de fechamento, por isso o botão fica sempre
      // acionável — inclusive depois de a conversa já ter terminado (pelo
      // suporte, por exemplo), quando ele vira "Fechar".
      els.end.disabled = false;
      els.end.textContent = state.status === "waiting" ? "Recusar" : (over ? "Fechar" : "Encerrar");
      els.end.title = over ? "Fechar a conversa" : "Encerrar o chat";
    }
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

    // Convite respondido AQUI (fluxo fallback da janela: agente local, sem o
    // cartão nativo): a janela do chat é a superfície do convite e precisa
    // SAIR DE CENA assim que o aceite for reportado. Sem isto ela ficava na
    // tela logo depois do aceite — por cima da nova janela do chat, que o Go
    // abre em seguida.
    var hideAfterAccept = accepted && state.status === "waiting";

    call("SupportChatRespondInvite", accepted, accepted ? "" : "user_denied")
      .then(function () {
        if (els.invite) els.invite.hidden = true;
        if (els.inviteAccept) els.inviteAccept.disabled = false;
        if (els.inviteReject) els.inviteReject.disabled = false;
        // Aceite: recarrega a conversa (com novas tentativas se o servidor ainda
        // não confirmou o status). A janela do chat NÃO é aberta por aqui: no
        // caminho normal ela é criada pelo Go no aceite do cartão nativo.
        if (hideAfterAccept) {
          state.closing = true;
          return call("HideSupportChatInviteWindow")
            .catch(function () { /* o Go também esconde no aceite */ })
            .then(function () { return null; });
        }
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
    state.pending.forEach(function (item, index) {
      // Chip com MINIATURA (quando é imagem e já temos o data URL), spinner
      // durante o upload e botão para descartar — o mesmo padrão do composer
      // do Chat IA. Sem a miniatura o usuário não sabia o que está anexando.
      var chip = document.createElement("span");
      chip.className = "sc-pending-chip";

      if (item.dataUrl && isRenderableImage(item.contentType, item.fileName)) {
        var thumb = document.createElement("img");
        thumb.src = item.dataUrl;
        thumb.alt = item.fileName || "print";
        chip.appendChild(thumb);
        chip.classList.add("sc-pending-chip--image");
      }

      var label = document.createElement("span");
      label.className = "sc-pending-chip-name";
      label.textContent = item.fileName || "anexo";
      chip.appendChild(label);

      if (item.uploading) {
        var spinner = document.createElement("span");
        spinner.className = "sc-pending-chip-status";
        spinner.textContent = "enviando…";
        chip.appendChild(spinner);
        chip.classList.add("sc-pending-chip--uploading");
      }

      var remove = document.createElement("button");
      remove.type = "button";
      remove.className = "sc-pending-chip-remove";
      remove.title = "Remover anexo";
      remove.setAttribute("aria-label", "Remover anexo");
      remove.textContent = "×";
      remove.addEventListener("click", function () {
        state.pending = state.pending.filter(function (p) { return p !== item; });
        renderPending();
      });
      chip.appendChild(remove);

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

  /**
   * No processo de UI o serviço é quem fala com o NATS: `connected` do serviço
   * LOCAL é sempre false (a UI nunca assina o canal — ela recebe o estado por
   * IPC). Por isso o link não pode ser derivado dele. A regra atual:
   *
   *   - "over"     conversa encerrada (nada a reenviar, banner NÃO aparece);
   *   - "opening"  janela recém-aberta e ainda sem conversa carregada;
   *   - "live"     conversa carregada (hasSession) OU evento de estado do bind;
   *   - "offline"  só quando o snapshot REST FALHOU e não há conversa viva —
   *                é o único momento em que o aviso de reconexão é verdadeiro.
   */
  function recomputeLink() {
    if (isConversationOver()) {
      state.link = "over";
      return;
    }
    if (state.hasSession || state.status === "active" || state.status === "waiting") {
      state.link = "live";
      state.openDeadlineMs = 0;
      return;
    }
    // Sem conversa carregada: a janela aberta ainda está esperando o primeiro
    // snapshot. Passado o prazo, e se o último snapshot falhou, aí sim avisa.
    if (state.openDeadlineMs && Date.now() < state.openDeadlineMs) {
      state.link = "opening";
      return;
    }
    if (state.lastSnapshotError) {
      state.link = "offline";
      return;
    }
    // BUG CORRIGIDO: aqui caía em "opening" para sempre quando o snapshot era
    // BEM-SUCEDIDO e simplesmente não havia conversa (janela aberta numa sessão
    // que já terminou, leitura vazia). O painel ficava eternamente em "Abrindo
    // a conversa…" — antes ele pelo menos dizia algo. "idle" = abriu, nada a
    // mostrar, canal OK.
    state.link = "idle";
  }

  /**
   * normalizeStatePayload aceita o estado em duas formas:
   *
   *   - objeto plano — contrato atual (mapa único do Go / campos do IPC);
   *   - array [estado, extras] — o Wails v3 entrega `ev.data` como SLICE quando
   *     o Go emite mais de um argumento (`EventManager.Emit(name, a, b)`).
   *
   * BUG CORRIGIDO: o handler chamava applyState(payload) direto. Quando o evento
   * vinha como array, `payload.status` era undefined -> a UI caía para "none" a
   * cada evento: chip piscando entre "ativo"/"encerrado"/"sem conversa" e campo
   * de digitação DESABILITADO no meio da conversa (o usuário "não conseguia
   * digitar"). A normalização mantém compatibilidade com build antigo do Go.
   */
  function normalizeStatePayload(payload) {
    if (!Array.isArray(payload)) return payload;
    var statePart = payload[0] || {};
    var extras = payload[1] || {};
    if (statePart && typeof statePart === "object" && !Array.isArray(statePart) &&
        extras && typeof extras === "object" && extras.realtime && !statePart.realtime) {
      statePart.realtime = extras.realtime;
    }
    return statePart;
  }

  /**
   * applyState aplica um snapshot/evento de estado de forma MONOTÔNICA por
   * sessão. O evento `supportchat:state` e o resultado de `SupportChatRefresh`
   * chegam por caminhos diferentes (IPC, binding) e podem se cruzar: um
   * `waiting` emitido ANTES do aceite podia ser aplicado DEPOIS do `active` e
   * derrubar a conversa para "aguardando"/"sem conexão".
   *
   * Regras: `ended` é terminal para a sessão; `active` não regride para
   * `waiting`; e uma sessão que já foi vista viva mantém hasSession.
   */
  function applyState(next) {
    next = normalizeStatePayload(next);
    if (!next || typeof next !== "object" || Array.isArray(next)) return;

    // Evento SEM nenhum campo de estado (payload antigo do IPC, array não
    // normalizado, objeto vazio): não pode rebaixar a conversa para "none".
    // Um payload só com "messages" também é inválido — o status/hasSession são
    // obrigatórios no contrato.
    if (!next.status && !next.sessionId && typeof next.hasSession !== "boolean") {
      return;
    }

    // liveSession anterior: decide se um snapshot sem sessão da MESMA
    // conversa pode derrubar o link (ver o bloco hasSession "grudado").
    var previousLiveSession = state.liveSession;
    var nextStatus = next.status || "none";
    var nextSession = String(next.sessionId || "");

    // "ended" é TERMINAL para a conversa exibida: snapshot sem sessão ou evento
    // atrasado não pode devolver a UI para "sem conversa". Sem esta guarda o
    // chip/serviço oscilava ("encerrado" x "sem conversa") e o campo ficava
    // desabilitado enquanto o usuário ainda lia o histórico. Uma sessão NOVA
    // (sessionId diferente) continua liberada.
    if (state.status === "ended" && nextStatus !== "ended" &&
        (!nextSession || nextSession === state.chatSessionId)) {
      if (next.realtime && typeof next.realtime.active === "boolean") {
        state.connected = next.realtime.active;
      }
      recomputeLink();
      render();
      updateSupportChatTabSurface();
      return;
    }

    // GUARDAS PRIMEIRO: um evento atrasado é DESCARTADO por inteiro. Antes eles
    // ficavam depois da troca de sessão abaixo — e um `waiting` atrasado da
    // sessão ANTIGA já tinha zerado o transcript da conversa atual antes de
    // retornar (a janela reusada aparecia vazia).
    if (nextSession) {
      var previousStatus = state.sessionState[nextSession];
      if (previousStatus === "ended" && nextStatus !== "ended") {
        // Evento atrasado de uma conversa já encerrada: não reabre.
        return;
      }
      if (previousStatus === "active" && nextStatus === "waiting") {
        // `waiting` depois de `active` só pode ser atraso de entrega.
        return;
      }
      state.sessionState[nextSession] = nextStatus;
    }

    // Sessão DIFERENTE = outra conversa. O transcript, o técnico e o chamado
    // anteriores pertencem à sessão antiga e não podem vazar para a nova —
    // inclusive quando o próximo evento é um convite "waiting" que chega depois
    // de uma conversa encerrada (antes o histórico antigo continuava na tela
    // até o aceite). Snapshots SEM sessão não limpam nada (leitura transitória).
    if (nextSession && state.chatSessionId && state.chatSessionId !== nextSession) {
      state.chatSessionId = nextSession;
      state.messages = [];
      state.pending = [];
      state.comments = [];
      state.supportName = "";
      state.ticketTitle = "";
      // O chamado anterior também não pode vazar: sem zerar ticketId, o painel
      // de comentários da nova conversa carregava os comentários do ticket
      // antigo (sessão avulsa, sem ticket, herdava o ticket da anterior).
      state.ticketId = "";
      state.commentsOpen = false;
      // Um load de comentários da sessão anterior pode estar em voo; sem este
      // reset o painel da sessão nova podia ficar preso em "Carregando…".
      state.commentsLoading = false;
      // REVISÃO: o contador de mensagens pertence à conversa anterior — sem o
      // reset, o primeiro snapshot da conversa nova com MAIS mensagens que a
      // antiga marcava destaque de "mensagem não vista" indevidamente.
      applyState._lastCount = 0;
      state.commentsLoadedFor = "";
    } else if (nextSession) {
      state.chatSessionId = nextSession;
    }

    state.status = nextStatus;
    var nextHasSession = !!next.hasSession;
    // hasSession "grudado": depois de a conversa ser vista viva nesta sessão,
    // um snapshot sem sessão da MESMA sessão (leitura transitória, evento
    // atrasado) não derruba o link. Outra sessão reseta.
    if (!nextHasSession && nextSession && previousLiveSession === nextSession && nextStatus !== "ended") {
      nextHasSession = true;
    }
    if (nextStatus === "ended" && nextSession && previousLiveSession === nextSession) {
      state.liveSession = "";
    } else if (nextSession && nextHasSession && nextStatus !== "ended") {
      state.liveSession = nextSession;
    }
    state.hasSession = nextHasSession;
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

    // Snapshot/evento com conteúdo = o canal está entregando: o link volta a
    // "live" sozinho (o aviso de reconexão some sem ação do usuário).
    state.lastSnapshotError = false;
    if (next.realtime && next.realtime.active === false) {
      // O BACKEND informa que está fora. Isso NÃO pinta o banner por si só:
      // o link deixa de ser "live" só quando não há conversa carregada.
      state.connected = false;
    } else if (next.realtime && next.realtime.active === true) {
      state.connected = true;
    }
    recomputeLink();

    // Título próprio era da JANELA DEDICADA (tooltip na barra de tarefas).
    // Embutido na principal, o document.title pertence ao app inteiro —
    // sobrescrever aqui trocava o título da interface do usuário. O chamado
    // por convite agora é sinalizado pelo item do menu (badge).
    if (!EMBEDDED_IN_MAIN_WINDOW) {
      if (state.status === "waiting") {
        document.title = "Convite do suporte — responda";
      } else if (next.supportName || next.ticketTitle) {
        var parts = [];
        if (next.supportName) parts.push("Atendente: " + next.supportName);
        if (next.ticketTitle) parts.push(next.ticketTitle);
        document.title = "Chat — " + parts.join(" · ");
      } else if (document.title !== "Chat com o suporte") {
        document.title = "Chat com o suporte";
      }
    }
    // Mensagem NOVA com a aba fora da tela (ou janela oculta no tray) =
    // destaque no item do menu; abrir a aba limpa o destaque.
    var grewMessages = state.messages.length > (applyState._lastCount || 0);
    applyState._lastCount = state.messages.length;
    if (grewMessages && !supportChatTabEmTela()) {
      supportChatUnseen = true;
    }

    render();

    // O ticket vem do snapshot do chat; os comentários entram assim que ele é
    // conhecido (e uma única vez por ticket).
    if (state.ticketId) loadTicketComments(false);

    // Superfície da ABA EMBUTIDA: visibilidade do item do menu + destaque de
    // "mensagem não vista" enquanto a aba não estiver em tela.
    updateSupportChatTabSurface();
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
        // Remove APENAS os anexos que foram enviados nesta mensagem. Antes um
        // "pending = []" descartava também uploads ainda em andamento: o
        // arquivo terminava de subir mas nunca era enviado (sumia da tela).
        var sent = {};
        attachmentIDs.forEach(function (id) { sent[id] = true; });
        state.pending = state.pending.filter(function (item) {
          return !item.attachmentId || !sent[item.attachmentId];
        });
        state.sending = false;
        render();
        // O snapshot é SEPARADO do envio. Antes ele fazia parte da mesma
        // cadeia: se a mensagem fosse enviada mas o refresh falhasse, o catch
        // mostrava "Falha ao enviar" — e o usuário reenviava o que já tinha
        // ido. refresh() já trata os próprios erros.
        refresh().catch(function () { /* refresh() já avisa */ });
      })
      .catch(function (err) {
        state.sending = false;
        // render ANTES do banner: render() esconde o banner nos estados
        // normais e a mensagem de erro era apagada no mesmo tick — o usuário
        // via o envio falhar sem nenhum aviso.
        render();
        showBanner("Falha ao enviar: " + describe(err));
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

  /**
   * Carrega a conversa do servidor.
   *
   * BUG RELATADO: "a janela do chat não abre depois do aceite". Quando a janela
   * nasce no aceite, o primeiro snapshot pode chegar ANTES de o servidor
   * confirmar a mudança Pending -> Active (o POST do aceite e este GET correm em
   * paralelo) ou numa leitura transitória que devolve "sem sessão". O estado
   * caía em "none" e a janela ficava "Sem conversa" — sem mensagens, com o
   * campo travado — até um evento de estado novo.
   *
   * Agora um snapshot vazio (sem sessão) NUNCA é aceito como resposta final:
   * tenta de novo algumas vezes com um pequeno intervalo. O serviço preserva o
   * "waiting" local (SupportChatRefresh usa RefreshKeepingInvite), então a
   * tentativa extra não apaga um convite pendente.
   */
  var REFRESH_EMPTY_RETRIES = 4;
  var REFRESH_EMPTY_DELAY_MS = 900;

  function isBlankSnapshot(next) {
    return !next || (!next.hasSession && (!next.status || next.status === "none"));
  }

  /**
   * A janela está no estado "vazia por enquanto" (sem sessão ainda)?
   *
   * Serve para as re-tentativas: uma janela recém-aberta pelo aceite nasce
   * "none" e PRECISA de novas tentativas; uma janela já carregada (conversa
   * encerrada vinda do snapshot do boot, ou estado ainda desconhecido) NÃO deve
   * rivalizar com a interface principal — ela já está cuidando do aviso.
   */
  function isUnsettled() {
    return state.status === "none" && !state.messages.length && !state.ticketTitle;
  }

  // Janela recém-aberta: enquanto o primeiro snapshot COM sessão não chega, o
  // painel fica em "Abrindo a conversa…" e tenta de novo — nunca anuncia
  // "sem conexão" nesse intervalo (era o defeito relatado: abrir o chat e
  // ver o aviso de reconexão). Teto de tempo para não mascarar um servidor
  // realmente fora.
  var OPENING_WINDOW_MS = 8000;
  var OPENING_RETRY_MS = 1000;
  var OPENING_RETRIES = 8;

  function refresh(attempt) {
    attempt = attempt || 0;
    var wasSettled = !isUnsettled();

    return call("SupportChatRefresh")
      .then(function (next) {
        state.lastSnapshotError = false;
        var blank = isBlankSnapshot(next);
        // Re-tenta quando o snapshot voltou vazio E a janela ainda não tinha
        // nada carregado (corrida do aceite × /active). Janela já preenchida
        // não entra no ciclo (evita URL/estado "vazia" reaparecendo).
        if (blank && !wasSettled && attempt < REFRESH_EMPTY_RETRIES && !state.closing) {
          return new Promise(function (resolve) {
            setTimeout(resolve, REFRESH_EMPTY_DELAY_MS);
          }).then(function () { return refresh(attempt + 1); });
        }
        applyState(next);
        // Ainda sem conversa carregada: agenda a próxima tentativa curta
        // (a janela acabou de abrir e o snapshot pode estar atrás do POST do
        // aceite). Para quando a conversa aparece ou o prazo vence.
        if (isUnsettled() && state.link === "opening" && !state.closing) {
          scheduleOpeningRetry();
        }
      })
      .catch(function (err) {
        // Snapshot falhou (rede). Mantém o estado em memória do NATS e marca
        // a falha: é só com ela (e sem conversa viva) que o aviso de
        // reconexão passa a ser exibido.
        state.lastSnapshotError = true;
        recomputeLink();
        render();
        // O aviso de reconexão ("Sem conexão em tempo real") já é o texto do
        // banner nesse caso; repetir o erro do snapshot por cima só trocava
        // a mensagem útil por um texto técnico.
        if (state.link !== "offline") {
          showBanner("Não foi possível atualizar a conversa: " + describe(err));
        }
      });
  }

  var openingRetryTimer = null;
  var snapshotDebounceTimer = null;

  /**
   * requestSnapshotSoon pede um snapshot em breve (debounce 150 ms): vários
   * eventos de estado juntos (o serviço emite um por transição) viram UMA
   * consulta em vez de N — o mesmo cuidado do refresh(attempt) com a corrida
   * do aceite. force=true também dispara quando a janela já tem conversa
   * (caso do reuso).
   */
  function requestSnapshotSoon(force) {
    if (state.closing) return;
    if (snapshotDebounceTimer) {
      // Com force, um pedido pendente já vira o que precisamos: cancela e
      // reagenda (janela reusada precisa buscar a conversa NOVA já).
      if (!force) return;
      clearTimeout(snapshotDebounceTimer);
      snapshotDebounceTimer = null;
    }
    snapshotDebounceTimer = setTimeout(function () {
      snapshotDebounceTimer = null;
      if (state.closing) return;
      if (openingRetryTimer) { clearTimeout(openingRetryTimer); openingRetryTimer = null; }
      // force: a janela pode ter conversa carregada de OUTRA sessão (reuso) e
      // refresh() não se recusa a rodar por isso.
      refresh().catch(function () { /* tratado em refresh */ });
    }, 150);
  }
  // openingAttempts: quantas re-tentativas de abertura já foram agendadas.
  // Contador próprio (o `attempt` de refresh() zera a cada chamada externa).
  var openingAttempts = 0;
  var openingDeadlineTimer = null;

  function scheduleOpeningRetry() {
    if (openingRetryTimer || state.closing) return;
    if (openingAttempts >= OPENING_RETRIES) return;
    openingAttempts += 1;
    openingRetryTimer = setTimeout(function () {
      openingRetryTimer = null;
      if (state.closing) return;
      if (isConversationOver() || state.hasSession) return;
      refresh().catch(function () { /* tratado em refresh */ });
    }, OPENING_RETRY_MS);
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

    var entry = { fileName: file.name || "print.png", contentType: file.type || "application/octet-stream", uploading: true, attachmentId: null, dataUrl: "" };
    state.pending.push(entry);
    renderPending();

    var reader = new FileReader();
    reader.onload = function () {
      var result = String(reader.result || "");
      var base64 = result.indexOf(",") >= 0 ? result.split(",")[1] : result;
      // Guarda o data URL para a MINIATURA do chip enquanto o upload corre.
      entry.dataUrl = result;
      renderPending();

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
    // WebView2 não renderiza: abre o CARTÃO DE DOCUMENTO (abrir arquivo / abrir
    // pasta / salvar como). Shift+clique continua indo direto para o
    // "Salvar como" (atalho de quem já sabe o que quer).
    if (!isImage) {
      if (askPath) {
        saveSupportChatAttachment(attachment, true);
        return;
      }
      showDocumentViewer(attachment);
      return;
    }
    if (askPath) {
      saveSupportChatAttachment(attachment, true);
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
      els.viewerSave.textContent = "Salvar imagem";
      els.viewerSave.title = "Salvar imagem (Shift+clique para escolher onde salvar)";
    }
    if (els.viewerDoc) els.viewerDoc.hidden = true;
    state.viewerDoc = null;
    if (els.viewerImage) els.viewerImage.hidden = false;
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
    state.viewerDoc = null;
    if (els.viewerDoc) els.viewerDoc.hidden = true;
    if (els.viewerImage) els.viewerImage.hidden = false;
    if (els.viewerSave) {
      els.viewerSave.textContent = "Salvar imagem";
      els.viewerSave.title = "Salvar imagem (Shift+clique para escolher onde salvar)";
    }
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

  /**
   * Cartão de DOCUMENTO (PDF, planilha, zip...): além de salvar, oferece
   * abrir com o app padrão e abrir a pasta selecionando o arquivo. Antes o
   * clique no chip apenas salvava em disco com um banner fácil de perder — o
   * usuário não tinha caminho nenhum para abrir o PDF.
   *
   * O arquivo é salvo (cache de caminho) antes das ações; o caminho vem do
   * próprio backend e os bindings validam que existe antes de abrir.
   */
  function showDocumentViewer(attachment) {
    closeAttachmentViewer();
    if (!attachment) return;

    state.viewerDoc = attachment;
    state.viewerSave = function (askPath) { return saveSupportChatAttachment(attachment, askPath); };

    var fileName = attachment.fileName || "anexo";
    if (els.viewerDocName) els.viewerDocName.textContent = fileName;
    if (els.viewerImage) {
      // Sem src no modo documento (e sem handler de erro: não há imagem).
      els.viewerImage.onerror = null;
      els.viewerImage.removeAttribute("src");
      els.viewerImage.hidden = true;
    }
    if (els.viewerSave) {
      els.viewerSave.hidden = false;
      els.viewerSave.disabled = false;
      els.viewerSave.textContent = "Salvar como…";
      els.viewerSave.title = "Salvar como… (escolher onde salvar)";
    }
    if (els.viewerDoc) els.viewerDoc.hidden = false;
    if (els.viewer) els.viewer.hidden = false;
  }

  // ensureSavedDocument devolve o caminho em disco do anexo (salvando na
  // primeira vez; chamadas seguintes usam o cache e só avisam onde está).
  function ensureSavedDocument(attachment) {
    var key = "chat:" + (attachment && attachment.id ? attachment.id : "");
    if (key !== "chat:" && savedAttachmentPaths[key]) {
      return Promise.resolve(savedAttachmentPaths[key]);
    }
    return saveSupportChatAttachment(attachment, false).then(function (path) {
      return path || null;
    });
  }

  function documentAction(kind) {
    var attachment = state.viewerDoc;
    if (!attachment) return;
    ensureSavedDocument(attachment).then(function (path) {
      if (!path) return; // saveSupportChatAttachment já avisou o erro
      var method = kind === "folder" ? "SupportChatRevealAttachment" : "SupportChatOpenAttachment";
      call(method, path)
        .then(function () {
          if (kind === "folder") showBanner("Pasta aberta no Explorer: " + path);
          // "open": sem banner — o app padrão assume a cena.
        })
        .catch(function (err) {
          showBanner("Não foi possível " + (kind === "folder" ? "abrir a pasta" : "abrir o arquivo") + ": " + describe(err));
        });
    });
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
      .then(function () {
        if (!EMBEDDED_IN_MAIN_WINDOW) return;
        // Embutido: não há janela para fechar. O Go emite supportchat:closed
        // (que aplica ended e esconde o item do menu); aqui a view sai logo —
        // o usuário clicou "Encerrar chat", a intenção é sair da conversa.
        state.closing = false;
        if (typeof setActiveTab === "function") setActiveTab("status");
      })
      .catch(function (err) {
        // Se o binding falhar, travar o estado em "closing" deixaria o usuário
        // preso num chat que não encerra por nenhum caminho. Libera nova
        // tentativa.
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

    // Anexos por colar/arrastar só pertencem ao chat de suporte quando a aba
    // está em tela e o alvo está dentro dela. Antes o listener era global:
    // colar um print no Chat IA (ou em qualquer outra aba) também enviava o
    // arquivo para o chat de suporte, e o dragover/drop global bloqueava o
    // comportamento nativo de outras telas.
    function supportChatOwnsAttachmentTarget(target) {
      var view = byId("supportChatView");
      if (!view) return true; // janela dedicada (rollback): só existe o chat
      if (!supportChatTabEmTela()) return false;
      if (!target || target === document || target === document.body) return true;
      return view.contains(target);
    }

    document.addEventListener("paste", function (event) {
      if (!event.clipboardData || !event.clipboardData.files || !event.clipboardData.files.length) return;
      if (!supportChatOwnsAttachmentTarget(event.target)) return;
      event.preventDefault();
      attachFiles(event.clipboardData.files);
    });

    ["dragover", "drop"].forEach(function (name) {
      document.addEventListener(name, function (event) {
        if (!supportChatOwnsAttachmentTarget(event.target)) return;
        event.preventDefault();
        if (name === "drop" && event.dataTransfer && event.dataTransfer.files) {
          attachFiles(event.dataTransfer.files);
        }
      });
    });

    on(els.inviteAccept, "click", function () { respondInvite(true); });
    on(els.inviteReject, "click", function () { respondInvite(false); });

    on(els.end, "click", askClose);

    // Faixa de conversa encerrada: "Fechar" encerra a leitura e devolve a UI
    // para Status; "Ir para Suporte" leva ao canal assíncrono (chamados), que
    // é onde o usuário pode abrir uma nova conversa sobre o assunto.
    on(els.endedClose, "click", function () { finishClose(); });
    on(els.endedSupport, "click", function () {
      if (typeof setActiveTab === "function") setActiveTab("support");
      if (typeof loadSupportTickets === "function") loadSupportTickets();
    });

    // Cartão de documento: abrir com o app padrão / abrir a pasta.
    on(els.viewerOpen, "click", function () { documentAction("open"); });
    on(els.viewerFolder, "click", function () { documentAction("folder"); });

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

    // Sessão para a qual já pedimos o snapshot inicial de mensagens.
    // O handler de estado NÃO pode pedir um snapshot a cada evento: refresh()
    // emite supportchat:state e o handler pedia outro refresh — loop contínuo
    // de GET /active + re-render (flicker/"friz" e carga no servidor).
    var snapshotRequestedSession = "";
    window.wails.on("supportchat:state", function (payload) {
      applyState(payload);
      // Só falta snapshot quando a conversa está ATIVA e o evento chegou sem
      // transcript (caso do aceite: o POST responde antes de as mensagens
      // serem carregadas). Uma vez por sessão; o próprio refresh emite o
      // estado completo depois.
      if (state.status === "active" && state.messages.length === 0 &&
          state.chatSessionId && snapshotRequestedSession !== state.chatSessionId) {
        snapshotRequestedSession = state.chatSessionId;
        requestSnapshotSoon();
      }
    });
    // O Go pede um recarregamento explícito (janela REUSADA no aceite: a
    // mesma janela passa de "convite" para "conversa" e precisa buscar a
    // sessão nova). Sem este listener ela reabria e ficava sem conversa.
    window.wails.on("supportchat:refresh", function () {
      beginOpening();
      requestSnapshotSoon(true);
    });
    window.wails.on("supportchat:error", function (message) { showBanner(String(message || "Erro no chat.")); });
    window.wails.on("supportchat:confirm_close", function () { askClose(); });

    // ── ABA EMBUTIDA (janela principal) ─────────────────────────────────────
    //
    // O chat de suporte roda DENTRO da janela principal (sem segunda janela
    // WebView2 — causa raiz dos congelamentos). O Go pede a exibição com o
    // evento supportchat:show; o estado waiting/active controla a visibilidade
    // e o destaque do item no menu lateral.
    window.wails.on("supportchat:show", function () {
      markSupportChatSeen();
      showSupportChatTab();
    });

    // Sessão encerrada no Go (botão Encerrar confirmado, suporte encerrou pelo
    // console, servidor expirou): a aba limpa a conversa e o item do menu sai
    // (o próximo supportchat:state ended também traz a superfície para o fim).
    window.wails.on("supportchat:closed", function () {
      applyState({ status: "ended", connected: state.connected, messages: state.messages });
      markSupportChatSeen();
    });

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

  // ── Abertura da janela ───────────────────────────────────────────────────

  /**
   * beginOpening marca a janela como "aberta agora" e desenha o cabeçalho que
   * o Go já conhece (sessão/chamado/atendente vêm na URL).
   *
   * Assim o painel abre COM a conversa (cabeçalho) mesmo antes do primeiro
   * snapshot — e a rolagem do render() não fica com a tela vazia enquanto a
   * rede responde.
   */
  function beginOpening() {
    state.link = "opening";
    state.openDeadlineMs = Date.now() + OPENING_WINDOW_MS;
    openingAttempts = 0;
    // Não há render() periódico: quando o prazo vence sem conversa, o painel
    // precisa sair de "Abrindo a conversa…" por conta própria.
    if (openingDeadlineTimer) clearTimeout(openingDeadlineTimer);
    openingDeadlineTimer = setTimeout(function () {
      openingDeadlineTimer = null;
      if (state.closing || state.hasSession || isConversationOver()) return;
      recomputeLink();
      render();
      scheduleOpeningRetry();
    }, OPENING_WINDOW_MS + 50);

    var params = openingParams();
    if (params.sessionId) {
      state.hasSession = true;
      state.sessionState[params.sessionId] = "active";
    }
    if (params.ticketTitle) state.ticketTitle = params.ticketTitle;
    if (params.supportName) state.supportName = params.supportName;

    // Sinaliza ao Go a PRIMEIRA PINTURA: é o que fecha a medida de tempo
    // "aceite → janela visível" no log do agent (SupportChatWindowPainted).
    if (window.requestAnimationFrame) {
      window.requestAnimationFrame(function () {
        call("SupportChatWindowPainted").catch(function () { /* build antigo */ });
      });
    }
  }

  /**
   * openingParams lê os campos que o Go envia na URL da janela
   * (sessionId/ticketId/supportName/ticketTitle). Valores ausentes devolvem
   * string vazia — a janela funciona igual sem eles (build antigo).
   */
  function openingParams() {
    var result = { sessionId: "", ticketId: "", supportName: "", ticketTitle: "" };
    try {
      var search = String((window.location && window.location.search) || "");
      if (!search) return result;
      var query = new URLSearchParams(search);
      ["sessionId", "ticketId", "supportName", "ticketTitle"].forEach(function (key) {
        var value = query.get(key);
        if (value) result[key] = value;
      });
    } catch (err) {
      // URLSearchParams indisponível: sem cabeçalho antecipado.
    }
    return result;
  }

  // ── Integração com a janela principal (aba "Chat do Suporte") ─────────────
  //
  // A aba só EXISTE para o usuário quando há sessão de suporte (waiting ou
  // active): o item do menu fica oculto fora disso e volta a ficar depois do
  // fim. Enquanto a aba não estiver em tela com mensagem nova, o item fica
  // DESTACADO (classe sidebar-link--attention + badge).
  var supportChatUnseen = false;

  function supportChatTabButton() {
    return document.getElementById("tabSupportChat");
  }

  function supportChatSessionVisible() {
    return state.status === "waiting" || state.status === "active";
  }

  // supportChatTabEmTela diz se a aba do chat está a view ativa agora.
  function supportChatTabEmTela() {
    try {
      return (typeof activeTab !== "undefined" && activeTab === "supportChat");
    } catch (_) {
      return false;
    }
  }

  // updateSupportChatTabSurface sincroniza visibilidade/destaque do item do
  // menu com o estado da sessão. Mensagem nova com a aba fora da tela (ou a
  // janela principal oculta) = item destacado; abrindo a aba, limpa.
  function updateSupportChatTabSurface() {
    var btn = supportChatTabButton();
    if (!btn) return;
    var visible = supportChatSessionVisible();
    btn.hidden = !visible;
    if (!visible) {
      btn.classList.remove("sidebar-link--attention");
      var badge0 = document.getElementById("tabSupportChatBadge");
      if (badge0) badge0.hidden = true;
      // Sessão acabou: se o usuário estava NA aba, mantém a view por alguns
      // segundos (ele vê o "conversa encerrada") e devolve para Status —
      // troca imediata parecia "o chat sumiu do nada".
      if (supportChatTabEmTela() && typeof setActiveTab === "function") {
        if (supportChatReturnTimer) { clearTimeout(supportChatReturnTimer); }
        supportChatReturnTimer = setTimeout(function () {
          supportChatReturnTimer = null;
          if (supportChatTabEmTela() && !supportChatSessionVisible() && typeof setActiveTab === "function") {
            setActiveTab("status");
          }
        }, SUPPORT_CHAT_RETURN_DELAY_MS);
      }
      return;
    }
    var emTela = supportChatTabEmTela();
    // Aba em tela = mensagens vistas: limpa o destaque. Antes a flag antiga
    // sobrevivia e o item voltava destacado numa sessão nova mesmo com o
    // usuário olhando para ela.
    supportChatUnseen = !emTela;
    btn.classList.toggle("sidebar-link--attention", supportChatUnseen);
    var badge = document.getElementById("tabSupportChatBadge");
    if (badge) badge.hidden = !supportChatUnseen;
  }

  // Timer do retorno automático pós-encerramento (ver acima).
  var supportChatReturnTimer = null;
  var SUPPORT_CHAT_RETURN_DELAY_MS = 6000;

  function refreshSupportChatTabSurface() {
    updateSupportChatTabSurface();
  }

  function showSupportChatTab() {
    if (typeof setActiveTab === "function") {
      setActiveTab("supportChat");
    }
    markSupportChatSeen();
    // UX: abrir a conversa já posiciona o cursor no campo de envio (o usuário
    // foi trazido até aqui PARA conversar). Atraso curto: a view precisa ter
    // sido exibida para o focus pegar.
    setTimeout(function () {
      if (els.input && !els.input.disabled && typeof activeTab !== "undefined" && activeTab === "supportChat") {
        try { els.input.focus(); } catch (_) {}
      }
    }, 120);
  }

  function markSupportChatSeen() {
    supportChatUnseen = false;
    var btn = supportChatTabButton();
    if (btn) btn.classList.remove("sidebar-link--attention");
    var badge = document.getElementById("tabSupportChatBadge");
    if (badge) badge.hidden = true;
  }

  // Exposto para o Go (ExecJS) e para o app.js (clique no item limpa o destaque).
  window.__discoveryShowSupportChat = showSupportChatTab;
  window.__discoveryClearSupportChatAttention = markSupportChatSeen;

  function bootstrap() {
    cacheElements();
    wireEvents();
    wireRealtime();

    // A janela não tem botão de fechar: o caminho normal de saída é o botão
    // "Encerrar". Um fechamento do SISTEMA (Alt+F4) continua sendo interceptado
    // no Go (WindowClosing) e vira supportchat:confirm_close; este handler só
    // cobre o fechamento que não passa pelo hook.
    // beforeunload: na JANELA DEDICADA, fechar a janela = fim da conversa.
    // EMBUTIDO na janela principal, o unload acontece a cada close-to-tray/
    // refresh da UI — encerrar a sessão ali era destruir a conversa do
    // usuário a cada minimização. O fim passa a ser EXCLUSIVAMENTE o botão
    // "Encerrar" (com confirmação) e o encerramento pelo suporte/servidor.
    if (!EMBEDDED_IN_MAIN_WINDOW) {
      window.addEventListener("beforeunload", function () {
        if (!state.closing) {
          // Melhor esforço: encerra a sessão sem bloquear o fechamento.
          call("SupportChatEnd", "user_closed").catch(function () {});
        }
      });
    }

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

    // EMBUTIDO: o boot da janela principal NÃO é "janela do chat aberta" —
    // beginOpening + snapshot com retries eram o custo de NASCIMENTO da janela
    // dedicada. Aqui o chat espera o primeiro supportchat:state/show (ou o
    // refresh do aceite); sem sessão pendente, nenhuma chamada de rede extra
    // no boot da UI.
    if (!EMBEDDED_IN_MAIN_WINDOW) {
      beginOpening();
    }
    applyState({ status: "none", connected: false, messages: [] });

    // EMBUTIDO (janela principal): o boot NÃO é "janela do chat aberta" —
    // beginOpening + retries eram o custo de NASCIMENTO da janela dedicada.
    // O snapshot inicial roda APÓS o applyState inicial (contrato do
    // bootstrap: a consulta de política não pode ser encadeada antes de
    // carregar a conversa) e só ELEVA o estado (waiting/active): regressar a
    // none depois de um waiting real recriava o bug do evento atrasado — nos
    // ramos sem sessão apenas o flag de conexão muda, sem novo applyState.
    if (EMBEDDED_IN_MAIN_WINDOW) {
      call("SupportChatRefresh")
        .then(function (next) {
          state.connected = true;
          if (next && (next.status === "waiting" || next.status === "active")) {
            beginOpening();
            applyState(next);
          }
        })
        .catch(function () {
          state.connected = false;
        });
    }

    // Sinal de "janela com conteúdo" para o Go (SupportChatWindowReady).
    //
    // BUG RELATADO (agent relançado no tray): a janela do chat aparecia, mas em
    // branco — o WebView2 criava o ambiente e a navegação não completava. O Go
    // usa este sinal como prova de que a página carregou; sem ele dentro do
    // prazo, a janela é recarregada/renavegada automaticamente
    // (ensureSupportChatWindowContent).
    call("SupportChatWindowReady").catch(function () { /* binding ausente em build antigo */ });

    // EMBUTIDO: o snapshot inicial já foi feito no bloco acima (sem retries,
    // sem beginOpening). O refresh() incondicional aqui era do ciclo da janela
    // dedicada — na principal ele DOBRARIA o custo de boot (5 GET /active) e
    // agendaria retries de "abertura" sem janela nenhuma.
    if (!EMBEDDED_IN_MAIN_WINDOW) {
      refresh().catch(function (err) {
        showBanner("Não foi possível carregar a conversa: " + describe(err));
      });
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", bootstrap);
  } else {
    bootstrap();
  }
})();
