"use strict";

// ── Captura de tela assistida (chat IA) ──────────────────────────────────
//
// Dois fluxos:
//   1. MANUAL — o usuário clica no ícone de câmera do composer; o backend
//      congela o desktop (janela principal cobre o desktop virtual) e este
//      arquivo desenha o overlay de seleção. O print fica anexado à próxima
//      mensagem e é enviado ao LLM como imagem.
//   2. IA — a tool MCP capture_screenshot(mode=interactive) pede a seleção;
//      o backend publica "screenshot:request" e o mesmo overlay é aberto.
//
// O consentimento da IA (permissão para capturar) é tratado no backend com o
// dock de pergunta do chat (chat:question) — este arquivo apenas conduz a UI
// da captura.

var screenshotAttachments = [];
var screenshotOverlayState = null;
// Token da captura em andamento: guarda contra a espera "eterna" do overlay
// (janela preta cobrindo o desktop) quando o backend não responde.
var screenshotCaptureToken = null;
// Cancelamento pedido ANTES de o backend responder (Esc/clique no X durante o
// "preparando"): a sessão só existe no payload, então o cancelamento é aplicado
// quando ele chega.
var screenshotCaptureAborted = false;

// Host do overlay: true na janela DEDICADA (screenshot-overlay.html), false na
// janela principal. Cada host usa só a metade que lhe cabe — a janela principal
// pede a captura e aplica o resultado; a janela do overlay desenha a seleção.
var screenshotOverlayHost = typeof window !== "undefined" && !!window.__SCREENSHOT_OVERLAY_HOST__;
// Destino do print anotado: "aiChat" (padrão), "supportChat", "ticketComment" ou
// "llm". Vem no payload da sessão; é ele que decide para onde o resultado volta.
var screenshotOverlayDestination = "";
// screenshotOverlayResultEvent é o evento global (qualquer janela recebe) que
// leva o print anotado da janela do overlay de volta para a janela de origem.
var SCREENSHOT_OVERLAY_RESULT_EVENT = "screenshot:overlay_result";

function screenshotApi() {
  return typeof appApi === "function" ? appApi() : null;
}

function screenshotParse(raw) {
  if (typeof raw === "string") {
    try {
      return JSON.parse(raw);
    } catch (_) {
      return null;
    }
  }
  return raw || null;
}

function screenshotFeedback(message, isError) {
  if (typeof showFeedback === "function") {
    showFeedback(message, isError);
  }
}

// Eventos de captura carregam timestamp: o broker do chat entrega pendências
// no próximo poll, então um evento antigo poderia reabrir o overlay (ou mostrar
// um print duplicado) muito depois de a sessão ter sido encerrada.
var SCREENSHOT_EVENT_MAX_AGE_MS = 120000;

function screenshotIsFresh(value) {
  if (!value) return true; // sem timestamp (compat) — aceita
  var parsed = Date.parse(value);
  if (isNaN(parsed)) return true;
  return Date.now() - parsed <= SCREENSHOT_EVENT_MAX_AGE_MS;
}

function screenshotT(key, vars) {
  if (typeof translate === "function") return translate(key, vars);
  return key;
}

// ── Anexos do composer ───────────────────────────────────────────────────

function screenshotRenderAttachments() {
  // Re-render troca os elementos: uma prévia presa perderia o mouseout.
  screenshotHideHoverPreview();
  var bar = document.getElementById("chatAttachments");
  if (!bar) return;
  if (!screenshotAttachments.length) {
    bar.classList.add("hidden");
    bar.innerHTML = "";
    return;
  }
  bar.classList.remove("hidden");
  bar.innerHTML = "";
  screenshotAttachments.forEach(function (att, idx) {
    var chip = document.createElement("div");
    chip.className = "chat-attachment";
    var img = document.createElement("img");
    img.src = att.dataUrl;
    img.alt = att.label || "print";
    img.title = screenshotT("screenshot.viewLarger");
    var meta = document.createElement("span");
    meta.className = "chat-attachment-meta";
    meta.textContent = att.label || "print";
    if (att.savedPath) meta.title = att.savedPath;

    // Ações do anexo: salvar em arquivo, copiar o caminho salvo e OCR.
    var tools = document.createElement("div");
    tools.className = "chat-attachment-tools";

    var saveBtn = document.createElement("button");
    saveBtn.type = "button";
    saveBtn.className = "chat-attachment-tool";
    saveBtn.textContent = "💾";
    saveBtn.title = screenshotT("screenshot.saveFile");
    saveBtn.setAttribute("aria-label", screenshotT("screenshot.saveFile"));
    saveBtn.addEventListener("click", function () {
      screenshotSaveAttachment(att);
    });
    tools.appendChild(saveBtn);

    var pathBtn = document.createElement("button");
    pathBtn.type = "button";
    pathBtn.className = "chat-attachment-tool";
    pathBtn.textContent = "🔗";
    pathBtn.title = att.savedPath ? screenshotT("screenshot.copyPath") : screenshotT("screenshot.copyPathEmpty");
    pathBtn.setAttribute("aria-label", screenshotT("screenshot.copyPath"));
    pathBtn.disabled = !att.savedPath;
    pathBtn.addEventListener("click", function () {
      if (att.savedPath) screenshotCopyPathToClipboard(att.savedPath);
    });
    tools.appendChild(pathBtn);

    var ocrBtn = document.createElement("button");
    ocrBtn.type = "button";
    ocrBtn.className = "chat-attachment-tool";
    ocrBtn.textContent = "T";
    ocrBtn.title = screenshotT("screenshot.ocrAction");
    ocrBtn.setAttribute("aria-label", screenshotT("screenshot.ocrAction"));
    ocrBtn.addEventListener("click", function () {
      screenshotOpenOcr(att.dataUrl, att.label || "");
    });
    tools.appendChild(ocrBtn);

    var remove = document.createElement("button");
    remove.type = "button";
    remove.className = "chat-attachment-remove";
    remove.title = screenshotT("chat.screenshotRemove");
    remove.textContent = "\u00d7";
    remove.addEventListener("click", function () {
      screenshotAttachments.splice(idx, 1);
      screenshotRenderAttachments();
    });
    chip.appendChild(img);
    chip.appendChild(meta);
    chip.appendChild(tools);
    chip.appendChild(remove);
    bar.appendChild(chip);
  });
}

// screenshotSaveAttachment salva o print anexado em arquivo e guarda o caminho
// no próprio anexo (o botão de copiar caminho passa a valer para ele).
function screenshotSaveAttachment(att) {
  if (!att || !att.dataUrl) return;
  screenshotSaveImageDataUrl(att.dataUrl, {}).then(function (saved) {
    if (!saved) return;
    att.savedPath = saved.path;
    att.savedFileName = saved.fileName || "";
    screenshotRenderAttachments();
  });
}

// screenshotTakePendingImages devolve (e limpa) as imagens anexadas — chamada
// pelo dispatch da mensagem do chat.
function screenshotTakePendingImages() {
  if (!screenshotAttachments.length) return [];
  var images = screenshotAttachments.map(function (att) { return att.dataUrl; });
  screenshotAttachments = [];
  screenshotRenderAttachments();
  return images;
}

// ── Política UNIFICADA de anexos do chat IA ──────────────────────────────
//
// A configuração de Anexos do servidor vale também para o chat IA/LLM:
// enabled, allowlist MIME e limite (padrão 10 MB) vêm de
// GET /me/ai-chat/attachment-settings via binding AIChatAttachmentPolicy.
// A UI usa a política apenas para evitar erro previsível — o Go e o servidor
// revalidam (enabled/MIME/tamanho decodificado) no envio.
var screenshotAttachmentPolicy = null;
var screenshotAttachmentPolicyLoaded = false;
var screenshotAttachmentPolicyPending = null;

function screenshotDefaultAttachmentPolicy() {
  return {
    enabled: true,
    allowedContentTypes: ["image/png", "image/jpeg", "image/webp", "image/gif"],
    maxFileSizeBytes: 10 * 1024 * 1024,
  };
}

function screenshotParseAttachmentPolicy(raw) {
  var parsed = raw;
  if (typeof raw === "string") {
    try { parsed = JSON.parse(raw); } catch (_) { parsed = null; }
  }
  if (!parsed || typeof parsed !== "object") return null;
  var defaults = screenshotDefaultAttachmentPolicy();
  var types = Array.isArray(parsed.allowedContentTypes)
    ? parsed.allowedContentTypes
        .map(function (item) { return String(item || "").toLowerCase(); })
        .filter(function (item) { return !!item; })
    : [];
  var max = Number(parsed.maxFileSizeBytes);
  return {
    enabled: typeof parsed.enabled === "boolean" ? parsed.enabled : defaults.enabled,
    allowedContentTypes: types.length ? types : defaults.allowedContentTypes,
    maxFileSizeBytes: max > 0 ? max : defaults.maxFileSizeBytes,
  };
}

// screenshotEnsureAttachmentPolicy busca a política uma vez (falha/rota ausente
// cai no default local; o servidor continua sendo a autoridade).
function screenshotEnsureAttachmentPolicy() {
  if (screenshotAttachmentPolicyLoaded) return Promise.resolve(screenshotAttachmentPolicy);
  if (screenshotAttachmentPolicyPending) return screenshotAttachmentPolicyPending;
  screenshotAttachmentPolicyPending = Promise.resolve()
    .then(function () {
      var api = screenshotApi();
      if (!api || typeof api.AIChatAttachmentPolicy !== "function") return null;
      return api.AIChatAttachmentPolicy();
    })
    .then(function (raw) {
      screenshotAttachmentPolicy = screenshotParseAttachmentPolicy(raw);
      screenshotAttachmentPolicyLoaded = true;
      screenshotAttachmentPolicyPending = null;
      return screenshotAttachmentPolicy;
    })
    .catch(function () {
      screenshotAttachmentPolicy = screenshotDefaultAttachmentPolicy();
      screenshotAttachmentPolicyLoaded = true;
      screenshotAttachmentPolicyPending = null;
      return screenshotAttachmentPolicy;
    });
  return screenshotAttachmentPolicyPending;
}

function screenshotEffectiveAttachmentPolicy() {
  return screenshotAttachmentPolicy || screenshotDefaultAttachmentPolicy();
}

function screenshotAttachmentMimeFromDataUrl(dataUrl) {
  var match = /^data:([^;,]+)/i.exec(String(dataUrl || ""));
  return match ? String(match[1]).toLowerCase() : "";
}

function screenshotAttachmentDataUrlBytes(dataUrl) {
  var raw = String(dataUrl || "");
  var comma = raw.indexOf(",");
  var base64 = comma >= 0 ? raw.substring(comma + 1) : "";
  return Math.floor(base64.length * 3 / 4);
}

function screenshotAttachmentTypeAllowed(contentType, policy) {
  var list = (policy && policy.allowedContentTypes) || [];
  if (!list.length) return true;
  var wanted = String(contentType || "").toLowerCase();
  for (var i = 0; i < list.length; i += 1) {
    if (String(list[i]).toLowerCase() === wanted) return true;
  }
  return false;
}

// Devolve "" quando o anexo pode entrar na fila ou a chave do motivo.
function screenshotAttachmentCheck(contentType, sizeBytes) {
  var policy = screenshotEffectiveAttachmentPolicy();
  if (!policy.enabled) return "disabled";
  if (!screenshotAttachmentTypeAllowed(contentType, policy)) return "type";
  if (Number(sizeBytes) > Number(policy.maxFileSizeBytes)) return "size";
  return "";
}

function screenshotAttachmentLimitLabel(bytes) {
  if (typeof commentAttachmentSizeLabel === "function") return commentAttachmentSizeLabel(bytes);
  var value = Number(bytes) || 0;
  if (value >= 1048576) return (value / 1048576).toFixed(1) + " MB";
  if (value >= 1024) return (value / 1024).toFixed(1) + " KB";
  return value + " B";
}

function screenshotAttachmentBlockedMessage(reason, sizeBytes) {
  var policy = screenshotEffectiveAttachmentPolicy();
  if (reason === "disabled") return screenshotT("chat.screenshotAttachmentsDisabled");
  if (reason === "size") {
    return screenshotT("chat.screenshotAttachmentTooLarge", {
      limit: screenshotAttachmentLimitLabel(policy.maxFileSizeBytes),
      size: screenshotAttachmentLimitLabel(sizeBytes),
    });
  }
  return screenshotT("chat.screenshotAttachmentTypeBlocked");
}

// screenshotPushAttachment valida (enabled/MIME/tamanho) e enfileira o data URL.
function screenshotPushAttachment(dataUrl, label, sizeBytes) {
  var contentType = screenshotAttachmentMimeFromDataUrl(dataUrl);
  var bytes = Number(sizeBytes) > 0 ? Number(sizeBytes) : screenshotAttachmentDataUrlBytes(dataUrl);
  var reason = screenshotAttachmentCheck(contentType, bytes);
  if (reason) {
    screenshotFeedback(screenshotAttachmentBlockedMessage(reason, bytes), true);
    return false;
  }
  // Sem limite de quantidade: todas as imagens válidas vão para o LLM.
  screenshotAttachments.push({ dataUrl: dataUrl, label: label || "print" });
  screenshotRenderAttachments();
  screenshotFeedback(screenshotT("chat.screenshotAttached"), false);
  return true;
}

function screenshotAttach(result) {
  if (!result || !result.dataUrl) return;
  // Política ainda não carregada: usa o default para validar agora e busca em
  // segundo plano para os próximos anexos (o backend revalida no envio).
  if (!screenshotAttachmentPolicyLoaded) void screenshotEnsureAttachmentPolicy();
  screenshotPushAttachment(
    result.dataUrl,
    result.label || ((result.width || 0) + "x" + (result.height || 0)),
    0
  );
}

// screenshotRestoreAttachments devolve anexos que não puderam ser enviados
// (binding ausente ou falha no dispatch) — o usuário não perde o print.
function screenshotRestoreAttachments(images) {
  if (!images || !images.length) return;
  images.forEach(function (src) {
    if (!src) return;
    screenshotAttachments.push({ dataUrl: src, label: "print" });
  });
  screenshotRenderAttachments();
}

// screenshotClearAttachments limpa os anexos pendentes (ex.: limpar o chat).
function screenshotClearAttachments() {
  if (!screenshotAttachments.length) return;
  screenshotAttachments = [];
  screenshotRenderAttachments();
}

// screenshotRenderSentImages mostra as miniaturas na bolha da mensagem já
// enviada (o servidor recebe as imagens junto do texto).
function screenshotRenderSentImages(images) {
  if (!images || !images.length) return;
  var bubbles = document.querySelectorAll(".chat-msg.user");
  var last = bubbles[bubbles.length - 1];
  if (!last) return;
  images.forEach(function (src) {
    var img = document.createElement("img");
    img.className = "chat-msg-image";
    img.src = src;
    last.appendChild(img);
  });
  if (typeof scheduleChatScrollToBottom === "function") scheduleChatScrollToBottom();
}

// ── Colar imagem da área de transferência ─────────────────────────────────
//
// Só IMAGENS: outros tipos de arquivo são recusados com aviso (o backend também
// descarta qualquer data URL que não seja data:image/). O conteúdo colado passa
// por normalização antes de virar anexo porque o agente ignora em silêncio
// data URLs acima de 6 MiB — um print 4K colado em PNG estouraria esse teto.
var screenshotPastedImageMaxDim = 2560;
var screenshotPastedImageBudget = 4 << 20; // tamanho da data URL (base64)

// screenshotEncodeCanvasWithinBudget tenta PNG e, se estourar o orçamento, JPEG
// em qualidades decrescentes — o print colado precisa caber no limite do chat.
function screenshotEncodeCanvasWithinBudget(canvas) {
  var attempts = [
    { mime: "image/png" },
    { mime: "image/jpeg", quality: 0.92 },
    { mime: "image/jpeg", quality: 0.8 },
  ];
  var dataUrl = "";
  for (var i = 0; i < attempts.length; i += 1) {
    try {
      dataUrl = attempts[i].quality === undefined
        ? canvas.toDataURL(attempts[i].mime)
        : canvas.toDataURL(attempts[i].mime, attempts[i].quality);
    } catch (_) {
      dataUrl = "";
    }
    if (dataUrl && dataUrl.length <= screenshotPastedImageBudget) return dataUrl;
  }
  return dataUrl; // último recurso: o backend decide se aceita
}

// screenshotPrepareClipboardImage converte o arquivo colado em data URL
// redimensionada (máx. 2560 px) e dentro do orçamento.
function screenshotPrepareClipboardImage(file, done) {
  var url = "";
  try {
    url = URL.createObjectURL(file);
  } catch (_) {
    url = "";
  }
  if (!url) {
    done("", "");
    return;
  }
  var img = new Image();
  img.onload = function () {
    try { URL.revokeObjectURL(url); } catch (_) {}
    var w = img.naturalWidth || 0;
    var h = img.naturalHeight || 0;
    if (!w || !h) {
      done("", "");
      return;
    }
    var scale = Math.min(1, screenshotPastedImageMaxDim / Math.max(w, h));
    var outW = Math.max(1, Math.round(w * scale));
    var outH = Math.max(1, Math.round(h * scale));
    var canvas = document.createElement("canvas");
    canvas.width = outW;
    canvas.height = outH;
    canvas.getContext("2d").drawImage(img, 0, 0, outW, outH);
    done(screenshotEncodeCanvasWithinBudget(canvas), outW + "x" + outH);
  };
  img.onerror = function () {
    try { URL.revokeObjectURL(url); } catch (_) {}
    done("", "");
  };
  img.src = url;
}

// screenshotPasteImages anexa as imagens coladas no composer. Sem teto de
// quantidade: TODAS as imagens válidas são mantidas e enviadas ao LLM. A
// política unificada (enabled/MIME/tamanho) é consultada ANTES de codificar:
// não adianta reduzir uma imagem que o servidor recusaria pelo tipo.
async function screenshotPasteImages(files) {
  if (!files || !files.length) return;
  await screenshotEnsureAttachmentPolicy();
  for (var i = 0; i < files.length; i += 1) {
    (function (file) {
      var declaredType = String(file.type || "").toLowerCase();
      var earlyReason = declaredType
        ? screenshotAttachmentCheck(declaredType, Number(file.size) || 0)
        : "";
      if (earlyReason) {
        screenshotFeedback(screenshotAttachmentBlockedMessage(earlyReason, Number(file.size) || 0), true);
        return;
      }
      screenshotPrepareClipboardImage(file, function (dataUrl, label) {
        if (!dataUrl) {
          screenshotFeedback(screenshotT("chat.pasteImageFailed"), true);
          return;
        }
        // Revalida o data URL final: o redimensionamento pode ter mudado o MIME
        // (PNG -> JPEG) e o tamanho em bytes.
        screenshotPushAttachment(dataUrl, label || "print", 0);
      });
    })(files[i]);
  }
}

// initScreenshotPaste liga o Ctrl+V de imagens na caixa de digitação do chat.
// Texto puro continua colando normalmente; arquivos que não são imagem são
// bloqueados com aviso (por enquanto só imagens são aceitas).
function initScreenshotPaste() {
  var input = document.getElementById("chatInput");
  if (!input || input.dataset.screenshotPasteBound === "1") return;
  input.dataset.screenshotPasteBound = "1";
  input.addEventListener("paste", function (event) {
    var clipboard = event.clipboardData;
    if (!clipboard) return;
    var items = clipboard.items || [];
    var images = [];
    var others = 0;
    for (var i = 0; i < items.length; i += 1) {
      var item = items[i];
      if (item.kind !== "file") continue;
      if ((item.type || "").toLowerCase().indexOf("image/") === 0) {
        var file = item.getAsFile ? item.getAsFile() : null;
        if (file) images.push(file);
      } else {
        others += 1;
      }
    }
    if (!images.length) {
      if (others > 0) {
        event.preventDefault();
        screenshotFeedback(screenshotT("chat.onlyImages"), true);
      }
      return;
    }
    event.preventDefault();
    screenshotPasteImages(images);
  });
}

function screenshotAppendInfoBubble(result, label, evidence) {
  if (!result || !result.dataUrl) return;
  // O overlay roda em janela própria: sem o chat da IA aqui, a bolha é exibida
  // pela janela principal (que recebe o evento com byLlm=true).
  if (screenshotOverlayHost) return;
  var container = document.getElementById("chatMessages");
  if (!container) return;
  var div = document.createElement("div");
  div.className = "chat-msg assistant screenshot-info";
  var caption = document.createElement("div");
  caption.className = "chat-msg-caption";
  caption.textContent = label;
  var img = document.createElement("img");
  img.className = "chat-msg-image";
  img.src = result.dataUrl;
  // Na timeline a visualização é por CLIQUE (sem prévia no hover).
  img.title = screenshotT("screenshot.viewLarger");
  if (result.auditId) img.setAttribute("data-screenshot-id", String(result.auditId));
  div.appendChild(caption);
  div.appendChild(img);
  // Bloco de evidência do print (data/hora UTC, hostname e SHA-256 curto).
  if (evidence && evidence.enabled) {
    var block = document.createElement("div");
    block.className = "screenshot-evidence-block";
    block.textContent = screenshotEvidenceSummary(evidence);
    div.appendChild(block);
  }
  // Insere ANTES da bolha de streaming: o print foi produzido no MEIO do turno
  // e a resposta final ainda vai ser preenchida na bolha criada no início.
  if (typeof insertBeforeStreamingBubble === "function") {
    insertBeforeStreamingBubble(div);
  } else {
    container.appendChild(div);
    if (typeof scheduleChatScrollToBottom === "function") scheduleChatScrollToBottom();
  }
}

// ── Overlay de seleção ───────────────────────────────────────────────────

function screenshotOverlayRoot() {
  return document.getElementById("screenshotOverlay");
}

function screenshotOverlayImg() {
  return document.getElementById("screenshotOverlayImage");
}

function screenshotOverlayCanvas() {
  return document.getElementById("screenshotAnnotCanvas");
}

function screenshotEnsureOverlayEls() {
  if (screenshotOverlayRoot()) return;
  var root = document.createElement("div");
  root.id = "screenshotOverlay";
  root.className = "screenshot-overlay hidden";
  root.innerHTML = [
    '<div id="screenshotOverlayToolbar" class="screenshot-overlay-toolbar">',
    // Alça de arraste: a barra nasce centralizada no topo, mas pode ser movida
    // quando ela cobre justamente a área que o usuário quer selecionar.
    '  <span id="screenshotOverlayDrag" class="screenshot-overlay-drag" data-i18n-title="screenshot.annotDrag">⣿</span>',
    '  <span id="screenshotOverlayHint" class="screenshot-overlay-hint"></span>',
    '  <span id="screenshotOverlaySize" class="screenshot-overlay-size"></span>',
    '  <button id="screenshotModeFull" type="button" class="btn subtle"></button>',
    '  <button id="screenshotModeWindow" type="button" class="btn subtle"></button>',
    '  <button id="screenshotModeCancel" type="button" class="btn danger"></button>',
    '</div>',
    // A viewport recorta; o conteúdo (imagem + anotações + caixas) é maior que ela
    // quando o zoom passa de 1 e anda por translate. Toda a matemática de
    // coordenadas parte de `img.getBoundingClientRect()`, que já reflete zoom e
    // deslocamento — por isso o zoom não exige mudar os conversores existentes.
    '<div id="screenshotOverlayViewport" class="screenshot-overlay-viewport">',
    '  <div id="screenshotOverlayContent" class="screenshot-overlay-content">',
    '    <img id="screenshotOverlayImage" class="screenshot-overlay-image" alt="tela congelada" draggable="false" />',
    '    <canvas id="screenshotAnnotCanvas" class="screenshot-annot-canvas hidden"></canvas>',
    '    <div id="screenshotOverlayWindow" class="screenshot-overlay-window hidden"></div>',
    '    <div id="screenshotOverlaySelection" class="screenshot-overlay-selection hidden"></div>',
    '    <input id="screenshotAnnotTextInput" class="screenshot-annot-input hidden" type="text" maxlength="120" />',
    '  </div>',
    '</div>',
    // Controle de zoom/pan do print. Fica FORA da barra do topo (que some quando a
    // área é escolhida), então acompanha as duas fases da captura.
    '<div id="screenshotZoomBar" class="screenshot-zoom-bar hidden">',
    '  <button type="button" class="screenshot-zoom-btn" id="screenshotZoomOut" data-i18n-title="screenshot.zoomOut">−</button>',
    '  <span id="screenshotZoomLabel" class="screenshot-zoom-label" data-i18n-title="screenshot.zoomHint">100%</span>',
    '  <button type="button" class="screenshot-zoom-btn" id="screenshotZoomIn" data-i18n-title="screenshot.zoomIn">+</button>',
    // Ampliar a área selecionada: ação explícita (aparece só com a seleção
    // travada). Sem ela, travar uma região pequena a exibia ampliada na tela toda.
    '  <button type="button" class="screenshot-zoom-btn hidden" id="screenshotZoomArea" data-i18n-title="screenshot.zoomArea"><svg viewBox="0 0 16 16" class="screenshot-annot-icon" aria-hidden="true"><rect x="2" y="3" width="12" height="10" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.5"/><circle cx="8" cy="8" r="2.6" fill="none" stroke="currentColor" stroke-width="1.5"/></svg></button>',
    '  <button type="button" class="screenshot-zoom-btn" id="screenshotZoomFit" data-i18n-title="screenshot.zoomFit">⤢</button>',
    '</div>',
    // Mensagem exibida enquanto o backend congela a tela: a janela já cobre o
    // desktop com fundo preto nesse intervalo, e sem isto o usuário via uma
    // "tela toda preta" sem nenhuma pista do que estava acontecendo.
    '<div id="screenshotOverlayLoading" class="screenshot-overlay-loading hidden"></div>',
    '<div id="screenshotAnnotToolbar" class="screenshot-annot-toolbar hidden">',
    '  <span id="screenshotAnnotDrag" class="screenshot-annot-drag" data-i18n-title="screenshot.annotDrag">⣿</span>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="rect" data-i18n-title="screenshot.annotRect">▭</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="circle" data-i18n-title="screenshot.annotCircle">◯</button>',
    // Realce/spotlight: escurece o entorno e deixa a região arrastada em destaque.
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="spotlight" data-i18n-title="screenshot.annotSpotlight"><svg viewBox="0 0 16 16" class="screenshot-annot-icon" aria-hidden="true"><rect x="1.5" y="1.5" width="13" height="13" rx="2" fill="currentColor" opacity="0.25"/><rect x="5" y="5" width="6" height="6" rx="1" fill="none" stroke="currentColor" stroke-width="1.5"/></svg></button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="arrow" data-i18n-title="screenshot.annotArrow">↗</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="line" data-i18n-title="screenshot.annotLine">╱</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="pen" data-i18n-title="screenshot.annotPen">✎</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="marker" data-i18n-title="screenshot.annotMarker">🖍</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="blur" data-i18n-title="screenshot.annotBlur">▦</button>',
    // Lupa: arraste sobre a área pequena e o recorte aparece ampliado ao lado.
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="magnify" data-i18n-title="screenshot.annotMagnify"><svg viewBox="0 0 16 16" class="screenshot-annot-icon" aria-hidden="true"><circle cx="6.8" cy="6.8" r="4.3" fill="none" stroke="currentColor" stroke-width="1.6"/><line x1="10.1" y1="10.1" x2="13.6" y2="13.6" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg></button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="step" data-i18n-title="screenshot.annotStep">①</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="text" data-i18n-title="screenshot.annotText">T</button>',
    // Ícone em SVG (não emoji): o 🧽 renderizava como um borrão laranja em alguns
    // WebViews e destoava dos demais glifos.
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="eraser" data-i18n-title="screenshot.annotEraser"><svg viewBox="0 0 16 16" class="screenshot-annot-icon" aria-hidden="true"><rect x="3" y="5.8" width="10" height="4.8" rx="1.4" transform="rotate(-38 8 8.2)" fill="none" stroke="currentColor" stroke-width="1.5"/><line x1="6.1" y1="11" x2="9.9" y2="7.2" stroke="currentColor" stroke-width="1.3"/></svg></button>',
    '  <span class="screenshot-annot-sep"></span>',
    '  <button type="button" class="screenshot-annot-color" data-annot-color="#ff3b30" style="background:#ff3b30"></button>',
    '  <button type="button" class="screenshot-annot-color" data-annot-color="#ffcc00" style="background:#ffcc00"></button>',
    '  <button type="button" class="screenshot-annot-color" data-annot-color="#34c759" style="background:#34c759"></button>',
    // Quarta cor: os atalhos 1–4 escolhem as quatro amostras da barra.
    '  <button type="button" class="screenshot-annot-color" data-annot-color="#0a84ff" style="background:#0a84ff"></button>',
    // Cor personalizada: abre o seletor nativo (paleta do sistema). O próprio
    // botão é o "quadradinho" da cor escolhida; sem escolha, mostra um arco-íris.
    '  <button type="button" id="screenshotAnnotColorCustom" class="screenshot-annot-color screenshot-annot-color-custom" data-i18n-title="screenshot.annotColorCustom"></button>',
    '  <input type="color" id="screenshotAnnotColorInput" class="screenshot-annot-color-input" value="#ff3b30" tabindex="-1" aria-hidden="true" />',
    '  <span class="screenshot-annot-sep"></span>',
    // Espessuras como BARRAS de alturas diferentes (antes eram pontos ●●●, que não
    // comunicavam "traço grosso" e ficavam estranhos ao lado dos outros ícones).
    '  <button type="button" class="screenshot-annot-btn" data-annot-width="3" data-i18n-title="screenshot.widthThin"><span class="screenshot-width-mark" style="height:2px"></span></button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-width="6" data-i18n-title="screenshot.widthMedium"><span class="screenshot-width-mark" style="height:4px"></span></button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-width="10" data-i18n-title="screenshot.widthThick"><span class="screenshot-width-mark" style="height:7px"></span></button>',
    '  <span class="screenshot-annot-sep"></span>',
    // Salvar em arquivo e copiar o caminho do último arquivo salvo.
    '  <button type="button" class="screenshot-annot-btn" id="screenshotAnnotSave" data-i18n-title="screenshot.saveFile">💾</button>',
    '  <button type="button" class="screenshot-annot-btn" id="screenshotAnnotCopyPath" data-i18n-title="screenshot.copyPath">🔗</button>',
    // Modo evidência: marca d'água (data/hora UTC, hostname, SHA-256) no print.
    '  <button type="button" class="screenshot-annot-btn" id="screenshotAnnotEvidence" aria-pressed="false" data-i18n-title="screenshot.evidenceMode">🛡</button>',
    '  <button type="button" class="screenshot-annot-btn" id="screenshotAnnotUndo" data-i18n-title="screenshot.annotUndo">↺</button>',
    '  <button type="button" class="screenshot-annot-btn" id="screenshotAnnotClear" data-i18n-title="screenshot.annotClear">🧹</button>',
    '  <button type="button" class="screenshot-annot-btn" id="screenshotAnnotReselect" data-i18n-title="screenshot.annotReselect">⟲</button>',
    '  <button type="button" class="screenshot-annot-btn danger" id="screenshotAnnotCancel" data-i18n-title="screenshot.annotCancel">✕</button>',
    '  <button type="button" class="screenshot-annot-confirm" id="screenshotAnnotConfirm" data-i18n-title="screenshot.annotConfirm">✓</button>',
    '</div>',
    // Barra de dicas de atalho (aparece só com a seleção travada).
    '<div id="screenshotShortcutBar" class="screenshot-shortcut-bar hidden"></div>',
    // Seletor de janelas (tecla W): lista as janelas do payload com busca.
    '<div id="screenshotWindowPicker" class="screenshot-window-picker hidden" role="dialog" aria-modal="false">',
    '  <div class="screenshot-window-picker-head">',
    '    <input id="screenshotWindowSearch" type="search" class="screenshot-window-search" autocomplete="off" spellcheck="false" />',
    '    <button id="screenshotWindowPickerClose" type="button" class="screenshot-annot-btn danger" data-i18n-title="action.close">✕</button>',
    '  </div>',
    '  <div id="screenshotWindowList" class="screenshot-window-list" role="listbox"></div>',
    '</div>',
  ].join("");
  document.body.appendChild(root);

  var hint = document.getElementById("screenshotOverlayHint");
  if (hint) hint.textContent = screenshotT("screenshot.overlayHint");
  var full = document.getElementById("screenshotModeFull");
  if (full) full.textContent = screenshotT("screenshot.full");
  var winBtn = document.getElementById("screenshotModeWindow");
  if (winBtn) winBtn.textContent = screenshotT("screenshot.window");
  var cancel = document.getElementById("screenshotModeCancel");
  if (cancel) cancel.textContent = screenshotT("screenshot.cancel");
  // O campo de anotação é criado por JS: o placeholder precisa ser aplicado aqui
  // (o applyI18n já rodou quando o overlay é montado sob demanda).
  var annotTextInput = document.getElementById("screenshotAnnotTextInput");
  if (annotTextInput) annotTextInput.placeholder = screenshotT("screenshot.annotText");
  // A barra do topo também é arrastável (alça ⣿ ou fundo), como a de anotações.
  var overlayBar = document.getElementById("screenshotOverlayToolbar");
  if (overlayBar) overlayBar.addEventListener("mousedown", screenshotStartOverlayToolbarDrag);

  if (full) full.addEventListener("click", function () { screenshotSelectFull(); });
  if (winBtn) winBtn.addEventListener("click", function () { screenshotSelectFrontWindow(); });
  if (cancel) cancel.addEventListener("click", function () { screenshotCancelOverlay(); });

  var img = document.getElementById("screenshotOverlayImage");
  img.addEventListener("mousedown", screenshotOnMouseDown);
  img.addEventListener("mousemove", screenshotOnMouseMove);
  img.addEventListener("mouseup", screenshotOnMouseUp);
  img.addEventListener("mouseleave", function () {
    var highlight = document.getElementById("screenshotOverlayWindow");
    if (highlight) highlight.classList.add("hidden");
  });

  // Anotações (canvas sobre a tela congelada).
  var canvas = document.getElementById("screenshotAnnotCanvas");
  canvas.addEventListener("mousedown", screenshotAnnotDown);
  canvas.addEventListener("mousemove", screenshotAnnotMove);
  canvas.addEventListener("mousemove", screenshotAnnotHoverCursor);
  canvas.addEventListener("mouseup", screenshotAnnotUp);
  canvas.addEventListener("mouseleave", function () {
    canvas.classList.remove("outside");
    screenshotAnnotUp();
  });
  Array.prototype.forEach.call(root.querySelectorAll("[data-annot-tool]"), function (btn) {
    btn.addEventListener("click", function () { screenshotSetAnnotTool(btn.getAttribute("data-annot-tool")); });
  });
  Array.prototype.forEach.call(root.querySelectorAll("[data-annot-color]"), function (btn) {
    btn.addEventListener("click", function () { screenshotSetAnnotColor(btn.getAttribute("data-annot-color")); });
  });
  // Seletor de cor personalizada: o botão abre a paleta nativa e a escolha vale
  // na hora. `input` cobre a paleta do Chrome (dispara a cada ajuste) e `change`
  // o fechamento; sem tratamento o estado ficaria com a cor antiga.
  var colorCustom = document.getElementById("screenshotAnnotColorCustom");
  var colorInput = document.getElementById("screenshotAnnotColorInput");
  if (colorCustom && colorInput) {
    colorCustom.addEventListener("click", function () {
      // Clique no quadradinho = "usar a última cor livre" e reabrir a paleta
      // nela (sem isso, reabrir na cor anterior parecia não fazer nada).
      if (screenshotLastCustomColor) {
        colorInput.value = screenshotLastCustomColor;
        screenshotSetAnnotColor(screenshotLastCustomColor);
      }
      // Mantém o seletor ancorado no botão (o input é 1×1 px dentro da barra).
      if (typeof colorInput.showPicker === "function") {
        try { colorInput.showPicker(); return; } catch (_) { /* fallback abaixo */ }
      }
      colorInput.click();
    });
    function applyCustomColor() {
      screenshotLastCustomColor = colorInput.value;
      screenshotSetAnnotColor(colorInput.value);
    }
    colorInput.addEventListener("input", applyCustomColor);
    colorInput.addEventListener("change", applyCustomColor);
  }
  Array.prototype.forEach.call(root.querySelectorAll("[data-annot-width]"), function (btn) {
    btn.addEventListener("click", function () { screenshotSetAnnotWidth(Number(btn.getAttribute("data-annot-width")) || 6); });
  });
  // Barra flutuante: arrastável (fundo ou alça) para não cobrir o print.
  var annotToolbar = document.getElementById("screenshotAnnotToolbar");
  if (annotToolbar) {
    annotToolbar.addEventListener("mousedown", screenshotStartToolbarDrag);
  }
  // Zoom/pan: roda do mouse (Ctrl+roda = zoom ancorado no cursor), botão do meio
  // para arrastar e os botões da barra de zoom.
  var viewport = screenshotOverlayViewport();
  if (viewport) {
    viewport.addEventListener("wheel", screenshotOverlayWheel, { passive: false });
    viewport.addEventListener("mousedown", screenshotOverlayMiddleDrag);
  }
  var zoomOut = document.getElementById("screenshotZoomOut");
  if (zoomOut) zoomOut.addEventListener("click", function () { screenshotSetZoom(screenshotOverlayView.zoom / screenshotZoomStep); });
  var zoomIn = document.getElementById("screenshotZoomIn");
  if (zoomIn) zoomIn.addEventListener("click", function () { screenshotSetZoom(screenshotOverlayView.zoom * screenshotZoomStep); });
  var zoomFit = document.getElementById("screenshotZoomFit");
  if (zoomFit) zoomFit.addEventListener("click", screenshotResetOverlayView);
  var zoomArea = document.getElementById("screenshotZoomArea");
  if (zoomArea) zoomArea.addEventListener("click", screenshotZoomToSelection);

  var undoBtn = document.getElementById("screenshotAnnotUndo");
  if (undoBtn) undoBtn.addEventListener("click", screenshotAnnotUndo);
  var clearBtn = document.getElementById("screenshotAnnotClear");
  if (clearBtn) clearBtn.addEventListener("click", screenshotAnnotClear);
  var reselectBtn = document.getElementById("screenshotAnnotReselect");
  if (reselectBtn) reselectBtn.addEventListener("click", screenshotAnnotReselect);
  var confirmBtn = document.getElementById("screenshotAnnotConfirm");
  if (confirmBtn) confirmBtn.addEventListener("click", screenshotConfirmSelection);
  // Cancelar também na barra de anotações: depois de travar a área a barra do
  // topo some, e sem isso o usuário perderia o atalho de desistir.
  var annotCancelBtn = document.getElementById("screenshotAnnotCancel");
  if (annotCancelBtn) annotCancelBtn.addEventListener("click", screenshotCancelOverlay);
  var textInput = document.getElementById("screenshotAnnotTextInput");
  if (textInput) {
    textInput.addEventListener("keydown", function (event) {
      // stopPropagation: sem isso o handler global de teclado também veria o
      // Enter (confirmando a captura) e o Escape (cancelando o overlay inteiro)
      // enquanto o usuário digita a anotação.
      if (event.key === "Enter") {
        event.preventDefault();
        event.stopPropagation();
        screenshotCommitText();
      }
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        textInput.classList.add("hidden");
      }
    });
    textInput.addEventListener("blur", function () { screenshotCommitText(); });
  }

  // Extensões (salvar/copiar caminho/evidência/atalhos/seletor de janelas): os
  // elementos existem a partir daqui, então o bind é feito uma única vez.
  screenshotInitOverlayExtras();

  document.addEventListener("keydown", screenshotOverlayKeydown);
}

/**
 * Mostra o overlay em modo "preparando" enquanto o backend congela a tela.
 *
 * A janela principal já foi/está sendo redimensionada para cobrir o desktop
 * (SWP_SHOWWINDOW no Go) e, sem o chrome do app, o usuário ficava olhando um
 * retângulo PRETO sem nenhuma mensagem. Aqui o overlay aparece com o fundo e a
 * dica "capturando"; a tela congelada substitui isso em screenshotShowOverlay.
 */
function screenshotShowPreparing() {
  screenshotEnsureOverlayEls();
  var root = screenshotOverlayRoot();
  if (!root) return;

  root.classList.remove("hidden");
  document.body.classList.add("screenshot-active");

  var loading = document.getElementById("screenshotOverlayLoading");
  if (loading) {
    loading.textContent = screenshotT("screenshot.capturing");
    loading.classList.remove("hidden");
  }
  var toolbar = document.getElementById("screenshotOverlayToolbar");
  if (toolbar) {
    var hint = document.getElementById("screenshotOverlayHint");
    if (hint) hint.textContent = screenshotT("screenshot.capturing");
    var size = document.getElementById("screenshotOverlaySize");
    if (size) size.textContent = "";
    toolbar.classList.remove("hidden");
  }
}

function screenshotShowOverlay(payload) {
  if (!payload || !payload.session) return;
  screenshotEnsureOverlayEls();
  var root = screenshotOverlayRoot();
  var img = document.getElementById("screenshotOverlayImage");
  if (!root || !img) {
    // Sem os elementos do overlay não há como desenhar a captura: o chrome do
    // app (escondido ANTES do round-trip para não piscar a janela principal)
    // precisa voltar, e a sessão do backend ser cancelada — senão a janela
    // ficaria cobrindo o desktop com a UI invisível.
    document.body.classList.remove("screenshot-active");
    screenshotOverlayDestination = "";
    var missingApi = screenshotApi();
    if (missingApi && typeof missingApi.CancelScreenshotOverlay === "function") {
      try {
        var pendingCancel = missingApi.CancelScreenshotOverlay(payload.session);
        if (pendingCancel && typeof pendingCancel.catch === "function") pendingCancel.catch(function () {});
      } catch (_) {}
    }
    return;
  }
  root.classList.remove("hidden");
  // Esconde o chrome do app durante a captura (a janela está em modo overlay).
  document.body.classList.add("screenshot-active");
  var loadingEl = document.getElementById("screenshotOverlayLoading");
  if (loadingEl) loadingEl.classList.add("hidden");
  // O backend respondeu: a espera "eterna" pelo prepare não vale mais.
  screenshotCaptureToken = null;
  // ready=false até a imagem da tela congelada carregar: sem isso, um movimento
  // de mouse antes do onload mapearia coordenadas com naturalWidth=0 (escala 1)
  // e a seleção sairia deslocada.
  screenshotOverlayState = {
    payload: payload,
    dragging: false,
    startX: 0,
    startY: 0,
    hover: null,
    ready: false,
    selection: null,
    annotations: [],
    draft: null,
    tool: "rect",
    color: "#ff3b30",
    lineWidth: 6,
  };
  screenshotSetAnnotToolbarVisible(false);
  // Nova sessão: zoom/pan zerados (sem herdar o enquadramento da captura
  // anterior) e barra de zoom visível nas duas fases da captura. O botão de
  // "ampliar a área" só aparece depois de travar a seleção.
  screenshotResetOverlayView();
  screenshotSetZoomBarVisible(true);
  screenshotSetZoomAreaVisible(false);
  // Nova sessão: barra do topo visível e de volta ao centro (posição arrastada
  // na captura anterior não é herdada).
  screenshotSetOverlayToolbarVisible(true);
  screenshotResetOverlayToolbarPos();
  // Nova sessão de captura: a barra volta a ser posicionada perto da seleção
  // (descarta a posição arrastada da captura anterior).
  screenshotAnnotToolbarPos.dragged = false;
  // Estado inicial das ferramentas (destaque visual nos botões).
  screenshotSetAnnotTool("rect");
  screenshotSetAnnotColor("#ff3b30");
  screenshotSetAnnotWidth(6);
  // Nova captura: nenhum caminho salvo herdado e seletor de janelas fechado.
  screenshotLastSavedPath = "";
  screenshotUpdateCopyPathButton();
  screenshotSetEvidenceButton(false);
  screenshotToggleWindowPicker(false);
  screenshotSetShortcutBarVisible(false);
  img.onload = function () {
    var state = screenshotOverlayState;
    if (!state || state.payload.session !== payload.session) return;
    state.ready = true;
    screenshotPrepareCanvas();
  };
  img.onerror = function () {
    screenshotFeedback(screenshotT("screenshot.failed"), true);
    screenshotCancelOverlay();
  };
  img.src = payload.imageDataUrl || "";
  if (img.complete && img.naturalWidth > 0 && screenshotOverlayState) {
    screenshotOverlayState.ready = true;
    screenshotPrepareCanvas();
  }
  // Rede de segurança: se a tela congelada não carregar (payload vazio, data URL
  // recusada), o overlay ficaria preto para sempre por cima do desktop. Sem
  // imagem não há captura: cancela e avisa.
  setTimeout(function () {
    var state = screenshotOverlayState;
    if (!state || state.payload.session !== payload.session) return;
    if (state.ready) return;
    screenshotFeedback(screenshotT("screenshot.overlayImageFailed"), true);
    screenshotCancelOverlay();
  }, 8000);
  var hint = document.getElementById("screenshotOverlayHint");
  if (hint) {
    hint.textContent = (payload.requestedByLlm
      ? screenshotT("screenshot.llmHint", { reason: payload.reason || "" })
      : screenshotT("screenshot.overlayHint")) + " · " + screenshotT("screenshot.overlayWindowsHint");
  }
  // Política: sem tela inteira permitida (ou exigindo janela), o atalho nem aparece.
  var fullBtn = document.getElementById("screenshotModeFull");
  if (fullBtn) {
    fullBtn.classList.toggle(
      "hidden",
      payload.policyFullScreenAllowed === false || payload.policyWindowRequired === true,
    );
  }
}

// screenshotPrepareCanvas dimensiona o canvas de anotação em pixels da IMAGEM
// (não do CSS): os traços ficam na mesma escala do print final.
function screenshotPrepareCanvas() {
  var img = screenshotOverlayImg();
  var canvas = screenshotOverlayCanvas();
  if (!img || !canvas || !img.naturalWidth) return;
  canvas.width = img.naturalWidth;
  canvas.height = img.naturalHeight;
  canvas.classList.add("hidden");
  // A imagem acabou de carregar: reaplica o enquadramento com as dimensões reais
  // (antes do load o ApplyView usa o tamanho da viewport como fallback).
  screenshotApplyView();
  screenshotAnnotRedraw();
}

function screenshotHideOverlay() {
  screenshotCaptureToken = null;
  var loading = document.getElementById("screenshotOverlayLoading");
  if (loading) loading.classList.add("hidden");
  var root = screenshotOverlayRoot();
  if (root) root.classList.add("hidden");
  document.body.classList.remove("screenshot-active");
  var selection = document.getElementById("screenshotOverlaySelection");
  if (selection) selection.classList.add("hidden");
  var highlight = document.getElementById("screenshotOverlayWindow");
  if (highlight) highlight.classList.add("hidden");
  var textInput = document.getElementById("screenshotAnnotTextInput");
  if (textInput) textInput.classList.add("hidden");
  var canvas = screenshotOverlayCanvas();
  if (canvas) canvas.classList.add("hidden");
  screenshotSetAnnotToolbarVisible(false);
  screenshotSetShortcutBarVisible(false);
  screenshotToggleWindowPicker(false);
  screenshotLastSavedPath = "";
  if (screenshotToolbarDragCleanup) {
    screenshotToolbarDragCleanup();
  }
  if (screenshotOverlayToolbarDragCleanup) {
    screenshotOverlayToolbarDragCleanup();
  }
  screenshotOverlayState = null;
}

function screenshotMetrics() {
  var img = document.getElementById("screenshotOverlayImage");
  var rect = img.getBoundingClientRect();
  var sx = img.naturalWidth > 0 && rect.width > 0 ? img.naturalWidth / rect.width : 1;
  var sy = img.naturalHeight > 0 && rect.height > 0 ? img.naturalHeight / rect.height : 1;
  return { rect: rect, sx: sx, sy: sy };
}

// screenshotPhysicalScale converte pixels do desktop FÍSICO ↔ CSS do overlay
// (virtualWidth / largura CSS da viewport). NÃO confundir com metrics.sx, que é
// "pixels da IMAGEM congelada por CSS": a imagem entregue ao overlay pode estar
// reduzida (≤3840) em relação ao desktop físico, e misturar os dois espaços
// desalinhava o destaque das janelas e o recorte da seleção.
function screenshotPhysicalScale(metrics) {
  var state = screenshotOverlayState;
  var p = state && state.payload ? state.payload : null;
  var sx = 1;
  var sy = 1;
  if (p && metrics && metrics.rect.width > 0 && metrics.rect.height > 0) {
    if (p.virtualWidth > 0) sx = p.virtualWidth / metrics.rect.width;
    if (p.virtualHeight > 0) sy = p.virtualHeight / metrics.rect.height;
  }
  return { x: sx, y: sy };
}

// ── Zoom/pan do overlay (na tela congelada) ──────────────────────────────
//
// Existe uma CAIXA DE VISUALIZAÇÃO (`screenshotViewBox`, em pixels da imagem):
// é SEMPRE a tela congelada inteira, nas duas fases da captura (na fase de escolha
// e depois de travar a seleção). A região selecionada continua, portanto, exibida
// na mesma posição e escala em que está na tela; o recorte só delimita as
// anotações e o entorno escurecido. O conteúdo (imagem + canvas + caixas) é
// dimensionado por `natural × escala` e deslocado por translate, com o pan
// limitado à caixa — ou seja, o zoom nunca sai da tela congelada.
//
// Os conversores de coordenadas continuam partindo de
// img.getBoundingClientRect() (posição/tamanho reais com zoom e deslocamento).
var screenshotOverlayView = { zoom: 1, panX: 0, panY: 0 };
var screenshotZoomMin = 1;
var screenshotZoomMax = 8;
var screenshotZoomStep = 1.25;
// A partir deste zoom o navegador para de suavizar a imagem (pixel nítido para
// conferir texto miúdo).
var screenshotZoomPixelatedFrom = 3;

function screenshotOverlayViewport() {
  return document.getElementById("screenshotOverlayViewport");
}

function screenshotOverlayContent() {
  return document.getElementById("screenshotOverlayContent");
}

function screenshotSetZoomBarVisible(visible) {
  var bar = document.getElementById("screenshotZoomBar");
  if (bar) bar.classList.toggle("hidden", !visible);
}

function screenshotViewportBox() {
  var vp = screenshotOverlayViewport();
  if (!vp) return { w: window.innerWidth, h: window.innerHeight };
  var rect = vp.getBoundingClientRect();
  return {
    w: rect.width > 0 ? rect.width : window.innerWidth,
    h: rect.height > 0 ? rect.height : window.innerHeight,
  };
}

// screenshotViewBox devolve a caixa de visualização em pixels da IMAGEM: SEMPRE a
// tela congelada inteira. A seleção NÃO muda o enquadramento — ela trava o recorte,
// o entorno escurecido e a barra de anotações. Assim a região continua na mesma
// posição e escala em que o usuário a desenhou na tela; ampliar é uma ação
// explícita (roda, +/− ou o botão "ampliar a área").
function screenshotViewBox() {
  var img = screenshotOverlayImg();
  var natW = img && img.naturalWidth > 0 ? img.naturalWidth : 0;
  var natH = img && img.naturalHeight > 0 ? img.naturalHeight : 0;
  if (natW > 0 && natH > 0) {
    return { x: 0, y: 0, w: natW, h: natH };
  }
  var box = screenshotViewportBox();
  return { x: 0, y: 0, w: box.w, h: box.h };
}

// screenshotSelectionClampedRect devolve a seleção em pixels da IMAGEM limitada aos
// limites da imagem. Usada pela lupa (screenshotAnnotBounds): a seleção de JANELA
// pode ter partes fora do desktop (janela arrastada para fora da tela) e a lente
// não pode ser jogada para fora da área que será exportada.
function screenshotSelectionClampedRect() {
  var img = screenshotOverlayImg();
  var natW = img && img.naturalWidth > 0 ? img.naturalWidth : 0;
  var natH = img && img.naturalHeight > 0 ? img.naturalHeight : 0;
  var sel = screenshotSelectionImageRect();
  if (!sel || sel.w <= 0 || sel.h <= 0 || natW <= 0 || natH <= 0) return null;
  var x = Math.max(0, Math.min(sel.x, natW - 1));
  var y = Math.max(0, Math.min(sel.y, natH - 1));
  return {
    x: x,
    y: y,
    w: Math.max(1, Math.min(sel.w, natW - x)),
    h: Math.max(1, Math.min(sel.h, natH - y)),
  };
}

// screenshotViewFit calcula o encaixe da caixa de visualização na viewport:
// preenche a LARGURA (a janela do overlay cobre exatamente o desktop virtual,
// então a imagem não distorce e a região fica no mesmo lugar da tela). O MESMO
// fator vale nas duas fases — não há salto ao travar a seleção. O antigo ramo
// `contain` sobre o recorte ampliava qualquer região pequena para a tela inteira.
function screenshotViewFit(box, vb) {
  return box.w / Math.max(1, vb.w);
}

// screenshotImageScale é a escala atual "px da imagem → px de CSS", já com o
// zoom aplicado.
function screenshotImageScale() {
  var box = screenshotViewportBox();
  var vb = screenshotViewBox();
  return screenshotViewFit(box, vb) * screenshotOverlayView.zoom;
}

// Limita o deslocamento à caixa de visualização. Quando a caixa é menor que a
// viewport num eixo (aspecto diferente), centraliza em vez de deixar buraco.
function screenshotClampPan(value, min, max) {
  if (max <= min) return (min + max) / 2;
  if (value < min) return min;
  if (value > max) return max;
  return value;
}

// screenshotApplyView recalcula tamanho do conteúdo, deslocamento e o quadro da
// seleção. O pan é limitado à caixa de visualização (a tela congelada inteira):
// nunca sobra área preta na janela.
function screenshotApplyView() {
  var content = screenshotOverlayContent();
  if (!content) return;
  var img = screenshotOverlayImg();
  var view = screenshotOverlayView;
  view.zoom = Math.max(screenshotZoomMin, Math.min(screenshotZoomMax, view.zoom || 1));
  var box = screenshotViewportBox();
  var vb = screenshotViewBox();
  var natW = img && img.naturalWidth > 0 ? img.naturalWidth : box.w;
  var natH = img && img.naturalHeight > 0 ? img.naturalHeight : box.h;
  var scale = screenshotViewFit(box, vb) * view.zoom;
  var cw = Math.max(1, Math.round(natW * scale));
  var ch = Math.max(1, Math.round(natH * scale));
  content.style.width = cw + "px";
  content.style.height = ch + "px";
  view.panX = screenshotClampPan(view.panX, vb.x * scale, (vb.x + vb.w) * scale - box.w);
  view.panY = screenshotClampPan(view.panY, vb.y * scale, (vb.y + vb.h) * scale - box.h);
  content.style.transform = "translate(" + -view.panX + "px," + -view.panY + "px)";
  if (img) {
    img.style.imageRendering = view.zoom >= screenshotZoomPixelatedFrom ? "pixelated" : "";
  }
  screenshotPositionSelectionBox();
  // O destaque de janela é recalculado no próximo mousemove; se ficasse parado
  // enquanto o zoom muda, apareceria fora de lugar.
  var highlight = document.getElementById("screenshotOverlayWindow");
  if (highlight) highlight.classList.add("hidden");
  screenshotUpdateZoomLabel();
}

// screenshotPositionSelectionBox desenha o quadro da seleção em px de CSS a
// partir do retângulo em px da IMAGEM. Sem isso o quadro ficava parado enquanto
// o zoom mudava a escala — a moldura saía de cima da área selecionada.
function screenshotPositionSelectionBox() {
  var state = screenshotOverlayState;
  var box = document.getElementById("screenshotOverlaySelection");
  if (!box) return;
  var rect = screenshotSelectionImageRect();
  if (!state || !state.selection || !rect || rect.w <= 0 || rect.h <= 0) {
    // Durante o arraste o quadro é posicionado por screenshotOnMouseMove: um zoom
    // no meio do arraste não pode fazer a moldura sumir.
    if (!state || !state.dragging) box.classList.add("hidden");
    return;
  }
  var scale = screenshotImageScale();
  box.classList.remove("hidden");
  box.classList.add("locked");
  box.style.left = rect.x * scale + "px";
  box.style.top = rect.y * scale + "px";
  box.style.width = rect.w * scale + "px";
  box.style.height = rect.h * scale + "px";
}

function screenshotUpdateZoomLabel() {
  var label = document.getElementById("screenshotZoomLabel");
  if (label) label.textContent = Math.round(screenshotOverlayView.zoom * 100) + "%";
}

// screenshotSetZoom aplica o zoom mantendo parado o ponto sob o cursor (zoom
// ancorado). Sem coordenada, ancora no centro da viewport.
function screenshotSetZoom(next, clientX, clientY) {
  var view = screenshotOverlayView;
  var vp = screenshotOverlayViewport();
  if (!vp) return;
  var zoom = Math.max(screenshotZoomMin, Math.min(screenshotZoomMax, next));
  if (Math.abs(zoom - view.zoom) < 0.001) return;
  var rect = vp.getBoundingClientRect();
  var anchorX = typeof clientX === "number" ? clientX - rect.left : rect.width / 2;
  var anchorY = typeof clientY === "number" ? clientY - rect.top : rect.height / 2;
  // Ponto sob o cursor em px da IMAGEM (independe do zoom atual) — a âncora é
  // preservada mesmo com a escala derivando da caixa de visualização.
  var scaleBefore = screenshotImageScale();
  var imageX = (anchorX + view.panX) / scaleBefore;
  var imageY = (anchorY + view.panY) / scaleBefore;
  view.zoom = zoom;
  var scaleAfter = screenshotImageScale();
  view.panX = imageX * scaleAfter - anchorX;
  view.panY = imageY * scaleAfter - anchorY;
  screenshotApplyView();
}

function screenshotPanBy(dx, dy) {
  screenshotOverlayView.panX += dx;
  screenshotOverlayView.panY += dy;
  screenshotApplyView();
}

function screenshotResetOverlayView() {
  var view = screenshotOverlayView;
  view.zoom = 1;
  view.panX = 0;
  view.panY = 0;
  screenshotApplyView();
  // Volta a 100%: a região retorna ao lugar original e a barra a acompanha
  // (sem efeito quando não há seleção travada nem barra arrastada).
  screenshotRepositionAnnotToolbar();
}

// screenshotSetZoomAreaVisible mostra/esconde o botão "ampliar a área": a ação só
// faz sentido depois que existe uma seleção travada.
function screenshotSetZoomAreaVisible(visible) {
  var btn = document.getElementById("screenshotZoomArea");
  if (btn) btn.classList.toggle("hidden", !visible);
}

// screenshotZoomToSelection amplia a ÁREA selecionada até ela caber na tela e a
// centraliza. É a ação EXPLÍCITA que substitui o antigo enquadramento automático
// (que ampliava qualquer recorte pequeno assim que a seleção era travada).
function screenshotZoomToSelection() {
  var state = screenshotOverlayState;
  if (!state || !state.selection) return;
  var rect = screenshotSelectionImageRect();
  if (!rect || rect.w <= 0 || rect.h <= 0) return;
  var box = screenshotViewportBox();
  var base = screenshotViewFit(box, screenshotViewBox());
  if (!(base > 0)) return;
  var view = screenshotOverlayView;
  // Escala (imagem px → CSS) necessária para a seleção caber inteira na viewport.
  var fit = Math.min(box.w / rect.w, box.h / rect.h);
  view.zoom = Math.max(screenshotZoomMin, Math.min(screenshotZoomMax, fit / base));
  var scale = base * view.zoom;
  // Centraliza a seleção; screenshotApplyView limita o pan aos limites da imagem.
  view.panX = (rect.x + rect.w / 2) * scale - box.w / 2;
  view.panY = (rect.y + rect.h / 2) * scale - box.h / 2;
  screenshotApplyView();
  screenshotRepositionAnnotToolbar();
}

// screenshotRepositionAnnotToolbar devolve a barra de anotações para junto da
// seleção (abaixo quando couber, senão acima) quando o usuário não a arrastou. A
// barra é `fixed` (coordenadas da VIEWPORT) e a seleção está em px da IMAGEM:
// converte pela escala vigente e subtrai o deslocamento do pan.
function screenshotRepositionAnnotToolbar() {
  var state = screenshotOverlayState;
  if (!state || !state.selection) return;
  var rect = screenshotSelectionImageRect();
  if (!rect) return;
  var vp = screenshotOverlayViewport();
  var vpRect = vp ? vp.getBoundingClientRect() : { left: 0, top: 0 };
  var scale = screenshotImageScale();
  screenshotPlaceAnnotToolbar({
    left: vpRect.left + rect.x * scale - screenshotOverlayView.panX,
    top: vpRect.top + rect.y * scale - screenshotOverlayView.panY,
    width: rect.w * scale,
    height: rect.h * scale,
  });
}

// screenshotOverlayWheel: roda = zoom ancorado no cursor (como em visualizadores
// de imagem) e Shift+roda = deslocamento horizontal. Pan contínuo fica no botão
// do meio, nas setas e nos próprios botões de zoom.
function screenshotOverlayWheel(event) {
  if (!screenshotOverlayState) return;
  event.preventDefault();
  // deltaMode: 0 = pixels, 1 = linhas, 2 = páginas (alguns mouses/WebView).
  var unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? screenshotViewportBox().h : 1;
  if (event.shiftKey && !event.ctrlKey && !event.metaKey) {
    screenshotPanBy(event.deltaY * unit, 0);
    return;
  }
  var factor = event.deltaY < 0 ? screenshotZoomStep : 1 / screenshotZoomStep;
  screenshotSetZoom(screenshotOverlayView.zoom * factor, event.clientX, event.clientY);
}

// screenshotOverlayMiddleDrag: botão do meio arrasta o print (o esquerdo é
// reservado para selecionar/desenhar).
function screenshotOverlayMiddleDrag(event) {
  if (event.button !== 1 || !screenshotOverlayState) return;
  event.preventDefault();
  var startX = event.clientX;
  var startY = event.clientY;
  var panX0 = screenshotOverlayView.panX;
  var panY0 = screenshotOverlayView.panY;
  function onMove(ev) {
    screenshotOverlayView.panX = panX0 - (ev.clientX - startX);
    screenshotOverlayView.panY = panY0 - (ev.clientY - startY);
    screenshotApplyView();
  }
  function onUp() {
    document.removeEventListener("mousemove", onMove);
    document.removeEventListener("mouseup", onUp);
  }
  document.addEventListener("mousemove", onMove);
  document.addEventListener("mouseup", onUp);
}

function screenshotWindowAt(cssX, cssY, metrics, physical) {
  var state = screenshotOverlayState;
  if (!state || !state.payload.windows) return null;
  var p = state.payload;
  var scale = physical || screenshotPhysicalScale(metrics);
  for (var i = 0; i < p.windows.length; i += 1) {
    var w = p.windows[i];
    if (w.isSelf || w.blocked || w.minimized || !w.width || !w.height) continue;
    var x = (w.x - p.virtualX) / scale.x;
    var y = (w.y - p.virtualY) / scale.y;
    var ww = w.width / scale.x;
    var hh = w.height / scale.y;
    if (cssX >= x && cssX <= x + ww && cssY >= y && cssY <= y + hh) {
      return { win: w, css: { x: x, y: y, w: ww, h: hh } };
    }
  }
  return null;
}

function screenshotOnMouseDown(event) {
  var state = screenshotOverlayState;
  // Com uma seleção travada, quem recebe o mouse é o canvas de anotação.
  if (!state || !state.ready || state.selection) return;
  // Só o botão esquerdo seleciona; o do meio é pan (screenshotOverlayMiddleDrag).
  if (event.button !== 0) return;
  state.dragging = true;
  state.startX = event.clientX;
  state.startY = event.clientY;
}

function screenshotOnMouseMove(event) {
  var state = screenshotOverlayState;
  if (!state || !state.ready || state.selection) return;
  var metrics = screenshotMetrics();
  var physical = screenshotPhysicalScale(metrics);
  var selection = document.getElementById("screenshotOverlaySelection");
  var highlight = document.getElementById("screenshotOverlayWindow");
  if (state.dragging) {
    var left = Math.min(state.startX, event.clientX) - metrics.rect.left;
    var top = Math.min(state.startY, event.clientY) - metrics.rect.top;
    var width = Math.abs(event.clientX - state.startX);
    var height = Math.abs(event.clientY - state.startY);
    if (selection) {
      selection.classList.remove("hidden");
      selection.style.left = left + "px";
      selection.style.top = top + "px";
      selection.style.width = width + "px";
      selection.style.height = height + "px";
    }
    var size = document.getElementById("screenshotOverlaySize");
    if (size) size.textContent = Math.round(width * physical.x) + " \u00d7 " + Math.round(height * physical.y);
    if (highlight) highlight.classList.add("hidden");
    return;
  }
  var hover = screenshotWindowAt(event.clientX - metrics.rect.left, event.clientY - metrics.rect.top, metrics, physical);
  state.hover = hover;
  if (highlight && hover) {
    highlight.classList.remove("hidden");
    highlight.style.left = hover.css.x + "px";
    highlight.style.top = hover.css.y + "px";
    highlight.style.width = hover.css.w + "px";
    highlight.style.height = hover.css.h + "px";
    highlight.textContent = hover.win.title || hover.win.processName || "";
  } else if (highlight) {
    highlight.classList.add("hidden");
  }
}

function screenshotOnMouseUp(event) {
  var state = screenshotOverlayState;
  if (!state || !state.ready) return;
  var metrics = screenshotMetrics();
  var physical = screenshotPhysicalScale(metrics);
  var moved = Math.abs(event.clientX - state.startX) + Math.abs(event.clientY - state.startY);
  state.dragging = false;
  if (moved < 6) {
    var hover = screenshotWindowAt(event.clientX - metrics.rect.left, event.clientY - metrics.rect.top, metrics, physical);
    if (hover) {
      screenshotLockSelection({
        kind: "window",
        windowHandle: hover.win.handle,
        x: hover.win.x,
        y: hover.win.y,
        width: hover.win.width,
        height: hover.win.height,
        monitorIndex: 0,
      });
    }
    return;
  }
  var left = Math.min(state.startX, event.clientX) - metrics.rect.left;
  var top = Math.min(state.startY, event.clientY) - metrics.rect.top;
  var width = Math.abs(event.clientX - state.startX);
  var height = Math.abs(event.clientY - state.startY);
  if (width < 6 || height < 6) return;
  var p = state.payload;
  if (p.policyWindowRequired === true) {
    // A política exige janela específica: recusa a região AQUI (com o overlay
    // ainda aberto) em vez de falhar só no Finish, depois de fechar tudo.
    var rejectedBox = document.getElementById("screenshotOverlaySelection");
    if (rejectedBox) rejectedBox.classList.add("hidden");
    screenshotFeedback(screenshotT("screenshot.windowRequired"), true);
    return;
  }
  screenshotLockSelection({
    kind: "region",
    x: p.virtualX + Math.round(left * physical.x),
    y: p.virtualY + Math.round(top * physical.y),
    width: Math.round(width * physical.x),
    height: Math.round(height * physical.y),
    windowHandle: 0,
    monitorIndex: 0,
  });
}

function screenshotSelectFull() {
  var state = screenshotOverlayState;
  if (!state) return;
  if (state.payload.policyFullScreenAllowed === false) {
    screenshotFeedback(screenshotT("screenshot.fullBlocked"), true);
    return;
  }
  var p = state.payload;
  screenshotLockSelection({
    kind: "region",
    x: p.virtualX,
    y: p.virtualY,
    width: p.virtualWidth,
    height: p.virtualHeight,
    windowHandle: 0,
    monitorIndex: 0,
  });
}

function screenshotSelectFrontWindow() {
  var state = screenshotOverlayState;
  if (!state || !state.payload.windows) return;
  for (var i = 0; i < state.payload.windows.length; i += 1) {
    var w = state.payload.windows[i];
    if (w.isSelf || w.blocked || w.minimized || !w.width || !w.height) continue;
    screenshotLockSelection({
      kind: "window",
      windowHandle: w.handle,
      x: w.x,
      y: w.y,
      width: w.width,
      height: w.height,
      monitorIndex: 0,
    });
    return;
  }
  screenshotFeedback(screenshotT("screenshot.noWindow"), true);
}

// screenshotLockSelection trava a área/janela escolhida e libera as anotações.
function screenshotLockSelection(selection) {
  var state = screenshotOverlayState;
  if (!state) return;
  state.selection = selection;
  state.annotations = [];
  state.draft = null;
  // A seleção está travada, mas a caixa de visualização CONTINUA sendo a tela
  // congelada inteira: a região permanece na mesma posição e escala em que foi
  // desenhada (o quadro é reposicionado por screenshotApplyView). Ampliar agora é
  // uma ação explícita do usuário, nunca um efeito de travar a seleção.
  screenshotResetOverlayView();
  var canvas = screenshotOverlayCanvas();
  if (canvas) {
    canvas.classList.remove("hidden");
    // Seleção nova: volta o cursor de desenho até o próximo mousemove decidir.
    canvas.classList.remove("outside");
  }
  screenshotSetAnnotToolbarVisible(true);
  // Barra de dicas de atalho: só faz sentido com a seleção travada (é quando
  // as ferramentas de anotação estão ativas).
  screenshotSetShortcutBarVisible(true);
  // O botão "ampliar a área" só passa a existir com a seleção travada.
  screenshotSetZoomAreaVisible(true);
  // A barra do topo (dica + tela inteira/janela/cancelar) some assim que a área
  // é escolhida: a partir daqui quem manda é a barra de anotações.
  screenshotSetOverlayToolbarVisible(false);
  // A barra já está visível (offsetHeight real), então o reposicionamento junto da
  // seleção é feito agora — e refeito a cada ajuste de zoom.
  screenshotRepositionAnnotToolbar();
  screenshotAnnotRedraw();
}

function screenshotAnnotReselect() {
  var state = screenshotOverlayState;
  if (!state) return;
  state.selection = null;
  state.annotations = [];
  state.draft = null;
  var box = document.getElementById("screenshotOverlaySelection");
  if (box) {
    box.classList.add("hidden");
    // Sem a marca `locked` o próximo arraste volta a usar o destaque suave de
    // seleção (o forte é só enquanto o print está travado).
    box.classList.remove("locked");
  }
  var canvas = screenshotOverlayCanvas();
  if (canvas) canvas.classList.add("hidden");
  var textInput = document.getElementById("screenshotAnnotTextInput");
  if (textInput) textInput.classList.add("hidden");
  // Sem seleção o quadro é escondido por screenshotPositionSelectionBox dentro do
  // ApplyView e o botão "ampliar a área" volta a ficar oculto.
  screenshotResetOverlayView();
  screenshotSetAnnotToolbarVisible(false);
  screenshotSetShortcutBarVisible(false);
  screenshotSetZoomAreaVisible(false);
  // Volta a mostrar dica e atalhos para escolher outra área/janela.
  screenshotSetOverlayToolbarVisible(true);
}

function screenshotSetAnnotToolbarVisible(visible) {
  var toolbar = document.getElementById("screenshotAnnotToolbar");
  if (toolbar) toolbar.classList.toggle("hidden", !visible);
}

// ── Barra do topo (dica + tela inteira/janela/cancelar) ───────────────────
//
// Nasce centralizada no topo (via CSS) e pode ser arrastada pela alça ⣿ ou pelo
// fundo quando estiver cobrindo justamente a área que o usuário quer selecionar.
// Ela desaparece quando a seleção é travada (a partir daí vale a barra de
// anotações) e volta ao acionar "refazer seleção".
var screenshotOverlayToolbarDragCleanup = null;

function screenshotSetOverlayToolbarVisible(visible) {
  var bar = document.getElementById("screenshotOverlayToolbar");
  if (!bar) return;
  bar.classList.toggle("hidden", !visible);
}

// screenshotResetOverlayToolbarPos volta a barra do topo ao centro (nova captura):
// sem isso a posição arrastada na captura anterior seria herdada.
function screenshotResetOverlayToolbarPos() {
  var bar = document.getElementById("screenshotOverlayToolbar");
  if (!bar) return;
  bar.style.left = "";
  bar.style.top = "";
  bar.style.transform = "";
}

function screenshotStartOverlayToolbarDrag(event) {
  var bar = document.getElementById("screenshotOverlayToolbar");
  if (!bar || !event) return;
  // Clique em botão/select é ação, não arraste.
  if (event.target && event.target.closest && event.target.closest("button")) return;
  event.preventDefault();
  var rect = bar.getBoundingClientRect();
  var offsetX = event.clientX - rect.left;
  var offsetY = event.clientY - rect.top;
  function onMove(ev) {
    var w = bar.offsetWidth || 0;
    var h = bar.offsetHeight || 0;
    var x = Math.min(Math.max(8, ev.clientX - offsetX), Math.max(8, window.innerWidth - w - 8));
    var y = Math.min(Math.max(8, ev.clientY - offsetY), Math.max(8, window.innerHeight - h - 8));
    // O transform (centralização) sai de cena: a posição passa a ser left/top.
    bar.style.transform = "none";
    bar.style.left = x + "px";
    bar.style.top = y + "px";
  }
  function onUp() {
    document.removeEventListener("mousemove", onMove);
    document.removeEventListener("mouseup", onUp);
    screenshotOverlayToolbarDragCleanup = null;
  }
  if (screenshotOverlayToolbarDragCleanup) {
    screenshotOverlayToolbarDragCleanup();
  }
  screenshotOverlayToolbarDragCleanup = onUp;
  document.addEventListener("mousemove", onMove);
  document.addEventListener("mouseup", onUp);
}

// ── Barra de anotação: posição e arraste ─────────────────────────────────
//
// A barra é `position: fixed` e reposicionada por JS. Regras:
//   * ao travar a seleção, ela aparece PRÓXIMA da área (abaixo; acima quando não
//     couber), alinhada à esquerda da seleção;
//   * se o usuário ARRASTAR (alça ⣿ ou fundo da barra), a posição manual passa a
//     valer para o resto da sessão de captura — não é mais reposicionada sozinha.
var screenshotAnnotToolbarPos = { dragged: false, x: 12, y: 12 };
// Cleanup do arraste em andamento (removido ao esconder o overlay): sem isso,
// fechar a captura no meio do arraste deixaria listeners de mousemove no
// documento mexendo numa barra que não existe mais.
var screenshotToolbarDragCleanup = null;
// Última cor livre escolhida na paleta: mantida entre capturas da sessão para
// reabrir o seletor no mesmo tom (o estado da anotação volta ao preset padrão).
var screenshotLastCustomColor = null;

function screenshotAnnotToolbarEl() {
  return document.getElementById("screenshotAnnotToolbar");
}

function screenshotClampToolbarPos(x, y) {
  var toolbar = screenshotAnnotToolbarEl();
  var w = toolbar && toolbar.offsetWidth ? toolbar.offsetWidth : 520;
  var h = toolbar && toolbar.offsetHeight ? toolbar.offsetHeight : 40;
  var maxX = Math.max(8, window.innerWidth - w - 8);
  var maxY = Math.max(8, window.innerHeight - h - 8);
  return {
    x: Math.min(Math.max(8, x), maxX),
    y: Math.min(Math.max(8, y), maxY),
  };
}

function screenshotApplyToolbarPos(x, y) {
  var toolbar = screenshotAnnotToolbarEl();
  if (!toolbar) return;
  var pos = screenshotClampToolbarPos(x, y);
  screenshotAnnotToolbarPos.x = pos.x;
  screenshotAnnotToolbarPos.y = pos.y;
  toolbar.style.left = pos.x + "px";
  toolbar.style.top = pos.y + "px";
  toolbar.style.bottom = "auto";
}

// screenshotPlaceAnnotToolbar posiciona a barra perto da seleção em CSS px.
function screenshotPlaceAnnotToolbar(selCss) {
  var toolbar = screenshotAnnotToolbarEl();
  if (!toolbar) return;
  if (screenshotAnnotToolbarPos.dragged) {
    screenshotApplyToolbarPos(screenshotAnnotToolbarPos.x, screenshotAnnotToolbarPos.y);
    return;
  }
  if (!selCss) return;
  var h = toolbar.offsetHeight || 40;
  // Preferência: logo abaixo da seleção; sem espaço, logo acima.
  var top = selCss.top + selCss.height + 10;
  if (top + h > window.innerHeight - 8) {
    top = selCss.top - h - 10;
  }
  screenshotApplyToolbarPos(selCss.left, top);
}

function screenshotStartToolbarDrag(event) {
  var toolbar = screenshotAnnotToolbarEl();
  if (!toolbar || !event) return;
  // Clique em botão/select da barra é ação, não arraste.
  if (event.target && event.target.closest && event.target.closest("button")) return;
  event.preventDefault();
  var rect = toolbar.getBoundingClientRect();
  var offsetX = event.clientX - rect.left;
  var offsetY = event.clientY - rect.top;
  function onMove(ev) {
    // A partir do primeiro movimento a posição é do usuário.
    screenshotAnnotToolbarPos.dragged = true;
    screenshotApplyToolbarPos(ev.clientX - offsetX, ev.clientY - offsetY);
  }
  function onUp() {
    document.removeEventListener("mousemove", onMove);
    document.removeEventListener("mouseup", onUp);
    screenshotToolbarDragCleanup = null;
  }
  if (screenshotToolbarDragCleanup) {
    screenshotToolbarDragCleanup();
  }
  screenshotToolbarDragCleanup = onUp;
  document.addEventListener("mousemove", onMove);
  document.addEventListener("mouseup", onUp);
}

function screenshotSetAnnotTool(tool) {
  var state = screenshotOverlayState;
  if (!state || !tool) return;
  state.tool = tool;
  var root = screenshotOverlayRoot();
  if (!root) return;
  Array.prototype.forEach.call(root.querySelectorAll("[data-annot-tool]"), function (btn) {
    btn.classList.toggle("active", btn.getAttribute("data-annot-tool") === tool);
  });
}

// screenshotNormalizeColor padroniza a cor em minúsculas para comparar presets
// (o seletor nativo devolve hex minúsculo; os presets estão em minúsculas).
function screenshotNormalizeColor(color) {
  return String(color || "").trim().toLowerCase();
}

function screenshotSetAnnotColor(color) {
  var state = screenshotOverlayState;
  if (!state || !color) return;
  state.color = color;
  var root = screenshotOverlayRoot();
  if (!root) return;
  var wanted = screenshotNormalizeColor(color);
  var isPreset = false;
  Array.prototype.forEach.call(root.querySelectorAll("[data-annot-color]"), function (btn) {
    var match = screenshotNormalizeColor(btn.getAttribute("data-annot-color")) === wanted;
    if (match) isPreset = true;
    btn.classList.toggle("active", match);
  });
  // O botão de cor personalizada vira o quadradinho da cor escolhida e fica
  // destacado quando a cor ativa NÃO é um dos presets.
  var custom = document.getElementById("screenshotAnnotColorCustom");
  if (custom) {
    custom.classList.toggle("active", !isPreset);
    // `has-color` desliga o miolo da roda de cores: com cor escolhida o botão
    // passa a ser a própria amostra do tom.
    custom.classList.toggle("has-color", !isPreset);
    if (isPreset) {
      custom.style.background = "";
    } else {
      custom.style.background = color;
    }
  }
  var input = document.getElementById("screenshotAnnotColorInput");
  if (input && screenshotNormalizeColor(input.value) !== wanted) {
    input.value = color;
  }
}

function screenshotSetAnnotWidth(width) {
  var state = screenshotOverlayState;
  if (!state || !width) return;
  state.lineWidth = width;
  var root = screenshotOverlayRoot();
  if (!root) return;
  Array.prototype.forEach.call(root.querySelectorAll("[data-annot-width]"), function (btn) {
    btn.classList.toggle("active", Number(btn.getAttribute("data-annot-width")) === width);
  });
}

function screenshotAnnotUndo() {
  var state = screenshotOverlayState;
  if (!state || !state.annotations.length) return;
  state.annotations.pop();
  screenshotAnnotRedraw();
}

function screenshotAnnotClear() {
  var state = screenshotOverlayState;
  if (!state) return;
  state.annotations = [];
  state.draft = null;
  screenshotAnnotRedraw();
}

function screenshotAnnotPoint(event) {
  var metrics = screenshotMetrics();
  return {
    x: (event.clientX - metrics.rect.left) * metrics.sx,
    y: (event.clientY - metrics.rect.top) * metrics.sy,
  };
}

// Ferramentas que desenham por arraste (retângulo/linha/seta/borrão).
function screenshotIsDragTool(tool) {
  return tool === "rect" || tool === "circle" || tool === "spotlight" || tool === "arrow" || tool === "line" || tool === "blur" || tool === "magnify";
}

// screenshotAnnotHoverCursor dá o retorno visual de que só a área selecionada é
// editável: fora dela o cursor deixa de ser a cruz de desenho. Sem isso, clicar
// fora da seleção (o traço é clipado) parecia "ferramenta quebrada".
function screenshotAnnotHoverCursor(event) {
  var state = screenshotOverlayState;
  var canvas = screenshotOverlayCanvas();
  if (!state || !state.selection || !canvas) return;
  // Durante um arraste o traço continua válido mesmo saindo da área (é clipado):
  // não trocar o cursor no meio do gesto.
  if (state.draft) return;
  var inside = screenshotPointInRect(screenshotAnnotPoint(event), screenshotSelectionImageRect());
  canvas.classList.toggle("outside", !inside);
}

function screenshotAnnotDown(event) {
  var state = screenshotOverlayState;
  if (!state || !state.selection) return;
  // Botão do meio é pan, não desenho.
  if (event.button !== 0) return;
  event.preventDefault();
  var point = screenshotAnnotPoint(event);
  // Com a tela congelada inteira visível, é fácil começar um traço FORA da
  // seleção. Ele sairia clipado (invisível) e pareceria que a ferramenta falhou:
  // melhor ignorar o clique e deixar claro que só a área selecionada é editável.
  if (!screenshotPointInRect(point, screenshotSelectionImageRect())) return;
  if (state.tool === "text") {
    screenshotOpenTextInput(event.clientX, event.clientY, point);
    return;
  }
  if (state.tool === "step") {
    // Numeração: um clique por passo (o número é recalculado no redraw).
    state.annotations.push({ type: "step", color: state.color, width: state.lineWidth, x: point.x, y: point.y });
    screenshotAnnotRedraw();
    return;
  }
  if (state.tool === "eraser") {
    screenshotEraseAt(point);
    return;
  }
  var shape = { type: state.tool, color: state.color, width: state.lineWidth, points: [point] };
  if (screenshotIsDragTool(state.tool)) {
    shape.x1 = point.x;
    shape.y1 = point.y;
    shape.x2 = point.x;
    shape.y2 = point.y;
  }
  state.draft = shape;
  screenshotAnnotRedraw();
}

function screenshotAnnotMove(event) {
  var state = screenshotOverlayState;
  if (!state || !state.draft) return;
  event.preventDefault();
  var point = screenshotAnnotPoint(event);
  if (screenshotIsDragTool(state.draft.type)) {
    state.draft.x2 = point.x;
    state.draft.y2 = point.y;
  } else {
    state.draft.points.push(point);
  }
  screenshotAnnotRedraw();
}

function screenshotAnnotUp() {
  var state = screenshotOverlayState;
  if (!state || !state.draft) return;
  var draft = state.draft;
  state.draft = null;
  var keep = false;
  if (screenshotIsDragTool(draft.type)) {
    keep = Math.abs(draft.x2 - draft.x1) > 2 || Math.abs(draft.y2 - draft.y1) > 2;
  } else {
    keep = draft.points.length > 1;
  }
  if (keep) state.annotations.push(draft);
  screenshotAnnotRedraw();
}

// Distância de um ponto ao segmento (px da imagem).
function screenshotDistanceToSegment(px, py, x1, y1, x2, y2) {
  var dx = x2 - x1;
  var dy = y2 - y1;
  var lenSq = dx * dx + dy * dy;
  var t = lenSq > 0 ? ((px - x1) * dx + (py - y1) * dy) / lenSq : 0;
  if (t < 0) t = 0;
  if (t > 1) t = 1;
  var cx = x1 + t * dx;
  var cy = y1 + t * dy;
  return Math.sqrt((px - cx) * (px - cx) + (py - cy) * (py - cy));
}

// Bounding box medida do texto (via canvas de medição).
function screenshotTextBounds(shape) {
  var size = Math.max(14, (shape.width || 6) * 5);
  var text = shape.text || "";
  var width = 0;
  try {
    var canvas = document.createElement("canvas");
    var ctx = canvas.getContext("2d");
    ctx.font = "bold " + size + "px sans-serif";
    width = ctx.measureText(text).width;
  } catch (_) {
    width = text.length * size * 0.6;
  }
  return { x: shape.x || 0, y: shape.y || 0, w: width, h: size * 1.2 };
}

// screenshotShapeDistance devolve a distância do ponto à marca (Infinity quando
// não atinge). A borracha usa isso para apagar a marca MAIS PRÓXIMA do clique,
// não apenas a última desenhada — essencial em marcas sobrepostas.
// O terceiro parâmetro (a tela congelada) só é usado pela lupa, que precisa
// recalcular onde caiu a lente para aceitar o apagamento.
function screenshotShapeDistance(shape, point, source) {
  if (!shape || !point) return Infinity;
  var stroke = Math.max(6, shape.width || 6);
  var half = stroke / 2;
  if (shape.type === "step") {
    var radius = Math.max(13, (shape.width || 6) * 3);
    var dc = Math.sqrt((point.x - shape.x) * (point.x - shape.x) + (point.y - shape.y) * (point.y - shape.y));
    return Math.max(0, dc - radius);
  }
  if (shape.type === "text") {
    var b = screenshotTextBounds(shape);
    if (point.x >= b.x - half && point.x <= b.x + b.w + half && point.y >= b.y - half && point.y <= b.y + b.h + half) {
      return 0;
    }
    return Infinity;
  }
  if (shape.type === "spotlight") {
    // O realce escurece o entorno; a marca é apagada clicando dentro da região.
    var sx1 = Math.min(shape.x1, shape.x2);
    var sy1 = Math.min(shape.y1, shape.y2);
    var sx2 = Math.max(shape.x1, shape.x2);
    var sy2 = Math.max(shape.y1, shape.y2);
    var sInside = point.x >= sx1 && point.x <= sx2 && point.y >= sy1 && point.y <= sy2;
    return sInside ? 0 : Infinity;
  }
  if (shape.type === "blur") {
    // O borrão cobre a área inteira: qualquer ponto dentro apaga.
    var bx = Math.min(shape.x1, shape.x2);
    var by = Math.min(shape.y1, shape.y2);
    var bw = Math.abs(shape.x2 - shape.x1);
    var bh = Math.abs(shape.y2 - shape.y1);
    var inside = point.x >= bx && point.x <= bx + bw && point.y >= by && point.y <= by + bh;
    return inside ? 0 : Infinity;
  }
  if (shape.type === "magnify") {
    // A lupa ocupa dois lugares: a área de origem e a lente. Qualquer um apaga.
    var lens = screenshotMagnifierRects(shape);
    if (!lens) return Infinity;
    if (screenshotPointInRect(point, lens.src) || screenshotPointInRect(point, lens.dst)) return 0;
    return Infinity;
  }
  if (shape.type === "circle") {
    // Elipse: |(px-cx)/rx, (py-cy)/ry| normalizado; a distância aproximada até a
    // borda é |d-1| × menor raio (suficiente para o alvo da borracha).
    var cx = Math.min(shape.x1, shape.x2) + Math.abs(shape.x2 - shape.x1) / 2;
    var cy = Math.min(shape.y1, shape.y2) + Math.abs(shape.y2 - shape.y1) / 2;
    var rx = Math.max(1, Math.abs(shape.x2 - shape.x1) / 2);
    var ry = Math.max(1, Math.abs(shape.y2 - shape.y1) / 2);
    var nx = (point.x - cx) / rx;
    var ny = (point.y - cy) / ry;
    var norm = Math.sqrt(nx * nx + ny * ny);
    return Math.max(0, Math.abs(norm - 1) * Math.min(rx, ry) - half);
  }
  // Só caneta/marca-texto usam a lista de pontos. As ferramentas de ARRASTE
  // (retângulo/seta/linha/círculo) também carregam `points: [origem]` desde o
  // mousedown; se esta checagem viesse antes, a borracha só acertaria a marca
  // clicando exatamente no canto onde o arraste começou.
  if ((shape.type === "pen" || shape.type === "marker") && shape.points && shape.points.length) {
    var best = Infinity;
    if (shape.points.length === 1) {
      var p0 = shape.points[0];
      best = Math.sqrt((point.x - p0.x) * (point.x - p0.x) + (point.y - p0.y) * (point.y - p0.y));
    }
    for (var i = 1; i < shape.points.length; i += 1) {
      best = Math.min(best, screenshotDistanceToSegment(
        point.x, point.y,
        shape.points[i - 1].x, shape.points[i - 1].y,
        shape.points[i].x, shape.points[i].y));
    }
    return Math.max(0, best - half);
  }
  if (shape.x1 !== undefined) {
    if (shape.type === "line" || shape.type === "arrow") {
      return Math.max(0, screenshotDistanceToSegment(point.x, point.y, shape.x1, shape.y1, shape.x2, shape.y2) - half);
    }
    // Retângulo: distância às 4 arestas (não ao miolo).
    var rx1 = Math.min(shape.x1, shape.x2);
    var ry1 = Math.min(shape.y1, shape.y2);
    var rx2 = Math.max(shape.x1, shape.x2);
    var ry2 = Math.max(shape.y1, shape.y2);
    var edges = Math.min(
      screenshotDistanceToSegment(point.x, point.y, rx1, ry1, rx2, ry1),
      screenshotDistanceToSegment(point.x, point.y, rx2, ry1, rx2, ry2),
      screenshotDistanceToSegment(point.x, point.y, rx2, ry2, rx1, ry2),
      screenshotDistanceToSegment(point.x, point.y, rx1, ry2, rx1, ry1));
    return Math.max(0, edges - half);
  }
  return Infinity;
}

// screenshotEraseAt remove a marca mais próxima do clique dentro da tolerância.
// Critério determinístico: menor distância; empates (dentro de um epsilon para
// não depender de ruído de ponto flutuante) resolvem pela marca mais RECENTE,
// que é a que está por cima e o usuário acabou de ver desenhada.
function screenshotEraseAt(point) {
  var state = screenshotOverlayState;
  if (!state) return;
  var tolerance = 16;
  var tieEpsilon = 0.5;
  var bestIndex = -1;
  var bestDistance = Infinity;
  for (var i = 0; i < state.annotations.length; i += 1) {
    var distance = screenshotShapeDistance(state.annotations[i], point, screenshotOverlayImg());
    if (distance > tolerance) continue;
    if (bestIndex < 0 || distance <= bestDistance + tieEpsilon) {
      bestDistance = distance;
      bestIndex = i;
    }
  }
  if (bestIndex >= 0) {
    state.annotations.splice(bestIndex, 1);
    screenshotAnnotRedraw();
  }
}

function screenshotOpenTextInput(clientX, clientY, imagePoint) {
  var input = document.getElementById("screenshotAnnotTextInput");
  var metrics = screenshotMetrics();
  if (!input) return;
  input.dataset.imageX = String(imagePoint.x);
  input.dataset.imageY = String(imagePoint.y);
  input.value = "";
  input.classList.remove("hidden");
  input.style.left = (clientX - metrics.rect.left) + "px";
  input.style.top = (clientY - metrics.rect.top) + "px";
  // O campo flutua SOBRE o print, ancorado no ponto clicado. Como ele tem largura
  // própria, um clique perto da borda direita/inferior jogaria parte do editor
  // para fora da janela (a viewport do overlay recorta). Aqui ele é empurrado de
  // volta para dentro — o texto continua sendo desenhado no ponto original.
  var rect = input.getBoundingClientRect();
  var margin = 8;
  var dx = 0;
  var dy = 0;
  if (rect.right > window.innerWidth - margin) dx = rect.right - (window.innerWidth - margin);
  if (rect.left - dx < margin) dx = rect.left - margin;
  if (rect.bottom > window.innerHeight - margin) dy = rect.bottom - (window.innerHeight - margin);
  if (rect.top - dy < margin) dy = rect.top - margin;
  if (dx) input.style.left = (clientX - metrics.rect.left - dx) + "px";
  if (dy) input.style.top = (clientY - metrics.rect.top - dy) + "px";
  input.focus();
}

function screenshotCommitText() {
  var state = screenshotOverlayState;
  var input = document.getElementById("screenshotAnnotTextInput");
  if (!state || !input || input.classList.contains("hidden")) return;
  var text = (input.value || "").trim();
  input.classList.add("hidden");
  input.value = "";
  if (!text) return;
  state.annotations.push({
    type: "text",
    color: state.color,
    width: state.lineWidth,
    text: text,
    x: Number(input.dataset.imageX) || 0,
    y: Number(input.dataset.imageY) || 0,
  });
  screenshotAnnotRedraw();
}

// screenshotSelectionImageRect converte a seleção (pixels físicos do desktop)
// para pixels da imagem congelada.
function screenshotSelectionImageRect() {
  var state = screenshotOverlayState;
  if (!state || !state.selection || !state.payload.virtualWidth) return null;
  var sx = state.payload.imageWidth / state.payload.virtualWidth;
  var sy = state.payload.imageHeight / state.payload.virtualHeight;
  return {
    x: (state.selection.x - state.payload.virtualX) * sx,
    y: (state.selection.y - state.payload.virtualY) * sy,
    w: state.selection.width * sx,
    h: state.selection.height * sy,
  };
}

function screenshotAnnotRedraw() {
  var state = screenshotOverlayState;
  var canvas = screenshotOverlayCanvas();
  if (!state || !canvas || !canvas.width) return;
  var ctx = canvas.getContext("2d");
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  var clip = screenshotSelectionImageRect();
  if (!clip) return;
  ctx.save();
  ctx.beginPath();
  ctx.rect(clip.x, clip.y, clip.w, clip.h);
  ctx.clip();
  var step = 0;
  state.annotations.forEach(function (shape) {
    if (shape.type === "step") step += 1;
    screenshotDrawShape(ctx, shape, screenshotOverlayImg(), shape.type === "step" ? step : 0, 1);
  });
  if (state.draft) {
    screenshotDrawShape(ctx, state.draft, screenshotOverlayImg(), 0, 1);
  }
  ctx.restore();
  // Prévia do modo evidência: a faixa aparece dentro da área selecionada (é
  // exatamente onde ela cairá na imagem final).
  if (state.evidence && state.evidence.enabled) {
    ctx.save();
    ctx.translate(clip.x, clip.y);
    screenshotDrawEvidenceWatermark(ctx, Math.max(1, clip.w), Math.max(1, clip.h), state.evidence);
    ctx.restore();
  }
}

// screenshotDrawShape desenha uma marca em pixels da IMAGEM. `source` é a tela
// congelada (necessária para o borrão); stepIndex numera os passos.
function screenshotDrawShape(ctx, shape, source, stepIndex, drawScale) {
  if (!shape) return;
  ctx.save();
  ctx.strokeStyle = shape.color || "#ff3b30";
  ctx.fillStyle = shape.color || "#ff3b30";
  ctx.lineWidth = shape.width || 6;
  ctx.lineCap = "round";
  ctx.lineJoin = "round";
  if (shape.type === "rect") {
    screenshotStrokeRoundedRect(ctx, shape);
  } else if (shape.type === "circle") {
    screenshotStrokeEllipse(ctx, shape);
  } else if (shape.type === "spotlight") {
    screenshotDrawSpotlight(ctx, shape);
  } else if (shape.type === "line") {
    ctx.beginPath();
    ctx.moveTo(shape.x1, shape.y1);
    ctx.lineTo(shape.x2, shape.y2);
    ctx.stroke();
  } else if (shape.type === "arrow") {
    screenshotDrawArrow(ctx, shape.x1, shape.y1, shape.x2, shape.y2);
  } else if (shape.type === "blur") {
    screenshotBlurRegion(ctx, shape, source, drawScale);
  } else if (shape.type === "magnify") {
    screenshotDrawMagnifier(ctx, shape, source);
  } else if (shape.type === "pen" || shape.type === "marker") {
    if (shape.points && shape.points.length > 1) {
      if (shape.type === "marker") ctx.globalAlpha = 0.35;
      ctx.lineWidth = shape.type === "marker" ? (shape.width || 6) * 4 : shape.width || 6;
      ctx.beginPath();
      ctx.moveTo(shape.points[0].x, shape.points[0].y);
      for (var i = 1; i < shape.points.length; i += 1) {
        ctx.lineTo(shape.points[i].x, shape.points[i].y);
      }
      ctx.stroke();
    }
  } else if (shape.type === "step") {
    screenshotDrawStep(ctx, shape, stepIndex || 1);
  } else if (shape.type === "text") {
    var size = Math.max(14, (shape.width || 6) * 5);
    ctx.font = "bold " + size + "px sans-serif";
    ctx.textBaseline = "top";
    ctx.lineWidth = Math.max(2, size / 8);
    ctx.strokeStyle = "rgba(0,0,0,0.75)";
    ctx.strokeText(shape.text || "", shape.x, shape.y);
    ctx.fillText(shape.text || "", shape.x, shape.y);
  }
  ctx.restore();
}

// Retângulo com cantos arredondados (visual de "destaque").
function screenshotStrokeRoundedRect(ctx, shape) {
  var x = Math.min(shape.x1, shape.x2);
  var y = Math.min(shape.y1, shape.y2);
  var w = Math.abs(shape.x2 - shape.x1);
  var h = Math.abs(shape.y2 - shape.y1);
  var radius = Math.min(Math.max(6, (shape.width || 6) * 2), w / 2, h / 2);
  screenshotRoundedRectPath(ctx, x, y, w, h, radius);
  ctx.stroke();
}

// Caminho de retângulo arredondado — compartilhado por quem só desenha a borda
// (retângulo) e por quem precisa recortar a área (lente da lupa).
function screenshotRoundedRectPath(ctx, x, y, w, h, radius) {
  var r = Math.min(Math.max(0, radius), w / 2, h / 2);
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.lineTo(x + w - r, y);
  ctx.quadraticCurveTo(x + w, y, x + w, y + r);
  ctx.lineTo(x + w, y + h - r);
  ctx.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
  ctx.lineTo(x + r, y + h);
  ctx.quadraticCurveTo(x, y + h, x, y + h - r);
  ctx.lineTo(x, y + r);
  ctx.quadraticCurveTo(x, y, x + r, y);
  ctx.closePath();
}

// Realce/spotlight: escurece tudo FORA da região arrastada e desenha um
// contorno na cor ativa — o miolo continua nítido e em evidência.
function screenshotDrawSpotlight(ctx, shape) {
  var x = Math.min(shape.x1, shape.x2);
  var y = Math.min(shape.y1, shape.y2);
  var w = Math.abs(shape.x2 - shape.x1);
  var h = Math.abs(shape.y2 - shape.y1);
  if (w < 2 || h < 2) return;
  ctx.save();
  ctx.beginPath();
  ctx.rect(-100000, -100000, 200000, 200000);
  ctx.rect(x, y, w, h);
  ctx.fillStyle = "rgba(0, 0, 0, 0.55)";
  ctx.fill("evenodd");
  ctx.strokeStyle = shape.color || "#ffcc00";
  ctx.lineWidth = Math.max(2, (shape.width || 6) * 0.7);
  ctx.strokeRect(x, y, w, h);
  ctx.restore();
}

// Círculo/elipse inscrita na mesma caixa do arraste do retângulo.
function screenshotStrokeEllipse(ctx, shape) {
  var x = Math.min(shape.x1, shape.x2);
  var y = Math.min(shape.y1, shape.y2);
  var rx = Math.abs(shape.x2 - shape.x1) / 2;
  var ry = Math.abs(shape.y2 - shape.y1) / 2;
  if (rx < 1 || ry < 1) return;
  ctx.beginPath();
  ctx.ellipse(x + rx, y + ry, rx, ry, 0, 0, Math.PI * 2);
  ctx.stroke();
}

// Limites onde uma anotação pode ser desenhada: o recorte SELECIONADO (que vira
// a imagem final) quando existe, senão a imagem inteira. A lupa usa isso para
// não jogar a lente para fora da área que será exportada.
function screenshotAnnotBounds() {
  // Usa a SELEÇÃO limitada à imagem — não a caixa de visualização, que agora é
  // sempre a tela inteira: a lente não pode sair da área que será exportada
  // (ex.: seleção de janela parcialmente fora do desktop).
  var vb = screenshotSelectionClampedRect();
  if (vb && vb.w > 0 && vb.h > 0) return vb;
  return null;
}

function screenshotPointInRect(point, rect) {
  return !!point && !!rect && point.x >= rect.x && point.x <= rect.x + rect.w && point.y >= rect.y && point.y <= rect.y + rect.h;
}

var screenshotMagnifyFactor = 2.5;
var screenshotMagnifyMargin = 18;

// screenshotMagnifierRects calcula a lente (origem + destino) — usado tanto para
// desenhar quanto para o hit-test da borracha.
//
// Posicionamento: à direita da origem quando cabe; senão à esquerda; senão abaixo;
// por fim acima, sempre mantendo a lente dentro dos limites da anotação.
function screenshotMagnifierRects(shape) {
  var x = Math.min(shape.x1, shape.x2);
  var y = Math.min(shape.y1, shape.y2);
  var w = Math.abs(shape.x2 - shape.x1);
  var h = Math.abs(shape.y2 - shape.y1);
  if (w < 6 || h < 6) return null;
  var bounds = screenshotAnnotBounds();
  // Fator 2,5x; se a lente não couber na área anotada, reduz (mínimo 1,3x) para
  // não sair cortada pela imagem final.
  var factor = screenshotMagnifyFactor;
  if (bounds && bounds.w > 0 && bounds.h > 0) {
    var maxFactor = Math.min(bounds.w / w, bounds.h / h);
    if (maxFactor < factor) factor = Math.max(1.3, maxFactor);
  }
  var dw = w * factor;
  var dh = h * factor;
  var bx = bounds ? bounds.x : 0;
  var by = bounds ? bounds.y : 0;
  var bw = bounds ? bounds.w : x + w + dw + screenshotMagnifyMargin;
  var bh = bounds ? bounds.h : y + h + dh + screenshotMagnifyMargin;
  var margin = screenshotMagnifyMargin;
  var dx = x + w + margin;
  var dy = y;
  if (dx + dw > bx + bw) dx = x - dw - margin;
  if (dx < bx) {
    dx = x;
    dy = y + h + margin;
  }
  if (dy + dh > by + bh) dy = y - dh - margin;
  if (dy < by) dy = Math.min(Math.max(by, y), Math.max(by, by + bh - dh));
  if (dx < bx) dx = bx;
  if (dx + dw > bx + bw) dx = Math.max(bx, bx + bw - dw);
  return { src: { x: x, y: y, w: w, h: h }, dst: { x: dx, y: dy, w: dw, h: dh } };
}

// screenshotDrawMagnifier desenha a LENTE: o recorte da tela congelada ampliado
// (2,5x), com borda, linha conectora e um retângulo fino marcando a origem.
function screenshotDrawMagnifier(ctx, shape, source) {
  if (!source) return;
  var rects = screenshotMagnifierRects(shape);
  if (!rects) return;
  var s = rects.src;
  var d = rects.dst;
  var color = shape.color || "#ff3b30";
  var radius = Math.max(6, (shape.width || 6) * 1.5);
  ctx.save();
  ctx.strokeStyle = color;
  // Linha conectora: da origem até a lente.
  ctx.lineWidth = Math.max(1.5, (shape.width || 6) * 0.35);
  ctx.beginPath();
  ctx.moveTo(s.x + s.w, s.y + s.h / 2);
  ctx.lineTo(d.x, d.y + d.h / 2);
  ctx.stroke();
  // Conteúdo ampliado, recortado pelo contorno da lente.
  ctx.save();
  screenshotRoundedRectPath(ctx, d.x, d.y, d.w, d.h, radius);
  ctx.clip();
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(source, s.x, s.y, s.w, s.h, d.x, d.y, d.w, d.h);
  // Privacidade: se a região de origem tem borrão, a lente NÃO pode devolver o
  // conteúdo nítido ampliado — replica o mesmo efeito sobre a lente.
  var privacy = screenshotMagnifierPrivacyStrength(shape);
  if (privacy > 0) {
    screenshotPrivacyEffectInto(ctx, source, s.x, s.y, s.w, s.h, d.x, d.y, d.w, d.h, privacy);
  }
  ctx.restore();
  // Borda da lente e marcação da área de origem.
  screenshotRoundedRectPath(ctx, d.x, d.y, d.w, d.h, radius);
  ctx.lineWidth = Math.max(2, (shape.width || 6) * 0.6);
  ctx.stroke();
  ctx.lineWidth = Math.max(1.5, (shape.width || 6) * 0.35);
  ctx.strokeRect(s.x, s.y, s.w, s.h);
  ctx.restore();
}

// screenshotBlurRegion aplica borrão/pixelado NA RESOLUÇÃO DA FONTE.
//
// O recorte é copiado 1:1 para um canvas auxiliar (sem transformação), onde o
// raio do filtro vale pixels reais da imagem — antes o blur era desenhado no ctx
// já transformado, então o raio efetivo variava com o zoom/seleção e o resultado
// saía lavado (ou borrado demais) em seleções grandes. Só depois o recorte
// tratado volta escalado para o destino, o que mantém a suavidade em qualquer
// tamanho de saída (overlay em preview ou imagem anotada final).
var screenshotBlurFilterSupport = null;
var screenshotBlurMaxPixels = 12 * 1000 * 1000; // teto do canvas auxiliar

function screenshotBlurSupportsFilter() {
  if (screenshotBlurFilterSupport !== null) return screenshotBlurFilterSupport;
  var probe = null;
  try {
    probe = document.createElement("canvas").getContext("2d");
    probe.filter = "blur(1px)";
    screenshotBlurFilterSupport = probe.filter === "blur(1px)";
  } catch (_) {
    screenshotBlurFilterSupport = false;
  }
  return screenshotBlurFilterSupport;
}

// Intensidade do borrão: a espessura escolhida escala o tamanho da célula do
// mosaico e o raio do desfoque (1x na espessura padrão = 6).
var screenshotBlurDefaultCell = 12; // px da fonte por célula do mosaico (1x)
var screenshotBlurDefaultRadius = 14; // raio do desfoque em px da fonte (1x)

function screenshotBlurRegion(ctx, shape, source, drawScale) {
  if (!source) return;
  var x = Math.min(shape.x1, shape.x2);
  var y = Math.min(shape.y1, shape.y2);
  var w = Math.abs(shape.x2 - shape.x1);
  var h = Math.abs(shape.y2 - shape.y1);
  if (w < 4 || h < 4) return;
  screenshotPrivacyEffectInto(ctx, source, x, y, w, h, x, y, w, h, Math.max(1, (shape.width || 6) / 6));
}

// screenshotPrivacyEffectInto aplica o efeito de privacidade (mosaico forte +
// duas passadas de desfoque) lendo uma região da FONTE e escrevendo em um
// destino qualquer. O borrão usa src == dst; a LUPA usa src = região de origem e
// dst = lente, para não reexpor ampliado justamente o que o usuário borrou.
function screenshotPrivacyEffectInto(ctx, source, srcX, srcY, srcW, srcH, dstX, dstY, dstW, dstH, strength) {
  if (!source || srcW < 4 || srcH < 4) return;
  // O recorte é feito em pixels da fonte; para regiões gigantes o auxiliar é
  // reduzido (e o raio, junto) para não alocar canvas exagerado.
  var shrink = Math.min(1, Math.sqrt(screenshotBlurMaxPixels / (srcW * srcH)));
  var cw = Math.max(4, Math.round(srcW * shrink));
  var ch = Math.max(4, Math.round(srcH * shrink));
  var temp = document.createElement("canvas");
  temp.width = cw;
  temp.height = ch;
  var tctx = temp.getContext("2d");

  // 1) MOSAICO FORTE primeiro: reduz o recorte a ~1/12 (célula de 12 px da
  //    fonte, no mínimo 8 px) e devolve ampliado. Isso já destrói texto e
  //    detalhes finos — a garantia de privacidade não depende de ctx.filter.
  var cell = Math.max(8, screenshotBlurDefaultCell * strength);
  var mosaicW = Math.max(6, Math.round(cw / cell));
  var mosaicH = Math.max(4, Math.round((ch / cw) * mosaicW));
  var mosaic = document.createElement("canvas");
  mosaic.width = mosaicW;
  mosaic.height = mosaicH;
  var mctx = mosaic.getContext("2d");
  mctx.imageSmoothingEnabled = true;
  mctx.imageSmoothingQuality = "high";
  mctx.drawImage(source, srcX, srcY, srcW, srcH, 0, 0, mosaicW, mosaicH);
  tctx.imageSmoothingEnabled = true;
  tctx.imageSmoothingQuality = "high";
  tctx.drawImage(mosaic, 0, 0, mosaicW, mosaicH, 0, 0, cw, ch);

  // 2) DESFOQUE em duas passadas (raio em pixels REAIS da fonte): apaga as
  //    quinas dos blocos e embaralha o que sobrou do mosaico. Duas passadas
  //    equivalem a um raio bem maior, sem o custo/traço de um raio gigante.
  if (screenshotBlurSupportsFilter()) {
    var radius = Math.max(8, screenshotBlurDefaultRadius * strength) * shrink;
    var filter = "blur(" + radius.toFixed(2) + "px)";
    var second = document.createElement("canvas");
    second.width = cw;
    second.height = ch;
    var sctx = second.getContext("2d");
    sctx.filter = filter;
    sctx.drawImage(temp, 0, 0);
    sctx.filter = "none";
    tctx.filter = filter;
    tctx.drawImage(second, 0, 0);
    tctx.filter = "none";
  }

  ctx.save();
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(temp, 0, 0, cw, ch, dstX, dstY, dstW, dstH);
  ctx.restore();
}

// screenshotMagnifierPrivacyStrength devolve a intensidade do borrão que cobre a
// região de origem da lupa (0 = nenhum). Sem isso a lupa mostrava NÍTIDO e
// ampliado o conteúdo que o usuário havia borrado.
function screenshotMagnifierPrivacyStrength(shape) {
  var state = screenshotOverlayState;
  if (!state || !state.annotations) return 0;
  var ax1 = Math.min(shape.x1, shape.x2);
  var ay1 = Math.min(shape.y1, shape.y2);
  var ax2 = Math.max(shape.x1, shape.x2);
  var ay2 = Math.max(shape.y1, shape.y2);
  var strength = 0;
  for (var i = 0; i < state.annotations.length; i += 1) {
    var other = state.annotations[i];
    if (!other || other === shape || other.type !== "blur") continue;
    var bx1 = Math.min(other.x1, other.x2);
    var by1 = Math.min(other.y1, other.y2);
    var bx2 = Math.max(other.x1, other.x2);
    var by2 = Math.max(other.y1, other.y2);
    var overlaps = Math.min(ax2, bx2) > Math.max(ax1, bx1) && Math.min(ay2, by2) > Math.max(ay1, by1);
    if (overlaps) strength = Math.max(strength, Math.max(1, (other.width || 6) / 6));
  }
  return strength;
}

// Passo numerado (círculo colorido com o número).
function screenshotDrawStep(ctx, shape, index) {
  var radius = Math.max(13, (shape.width || 6) * 3);
  ctx.beginPath();
  ctx.arc(shape.x, shape.y, radius, 0, Math.PI * 2);
  ctx.fillStyle = shape.color || "#ff3b30";
  ctx.fill();
  ctx.strokeStyle = "rgba(0,0,0,0.55)";
  ctx.lineWidth = Math.max(2, radius / 6);
  ctx.stroke();
  ctx.fillStyle = "#ffffff";
  ctx.font = "bold " + Math.round(radius * 1.25) + "px sans-serif";
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillText(String(index || 1), shape.x, shape.y + 1);
  ctx.textAlign = "start";
}

function screenshotDrawArrow(ctx, x1, y1, x2, y2) {
  var head = Math.max(12, (ctx.lineWidth || 6) * 4);
  var angle = Math.atan2(y2 - y1, x2 - x1);
  ctx.beginPath();
  ctx.moveTo(x1, y1);
  ctx.lineTo(x2, y2);
  ctx.stroke();
  ctx.beginPath();
  ctx.moveTo(x2, y2);
  ctx.lineTo(x2 - head * Math.cos(angle - Math.PI / 7), y2 - head * Math.sin(angle - Math.PI / 7));
  ctx.lineTo(x2 - head * Math.cos(angle + Math.PI / 7), y2 - head * Math.sin(angle + Math.PI / 7));
  ctx.closePath();
  ctx.fill();
}

// screenshotBuildAnnotatedDataUrl compõe o recorte + anotações no tamanho final.
function screenshotBuildAnnotatedDataUrl() {
  var state = screenshotOverlayState;
  var img = screenshotOverlayImg();
  var clip = screenshotSelectionImageRect();
  if (!state || !img || !clip) throw new Error("sem selecao");
  // 3840 px (mesmo teto do backend = nativo até 4K): textos pequenos legíveis.
  var maxDim = 3840;
  var scale = Math.min(1, maxDim / Math.max(clip.w, clip.h));
  var outW = Math.max(1, Math.round(clip.w * scale));
  var outH = Math.max(1, Math.round(clip.h * scale));
  var canvas = document.createElement("canvas");
  canvas.width = outW;
  canvas.height = outH;
  var ctx = canvas.getContext("2d");
  ctx.fillStyle = "#000";
  ctx.fillRect(0, 0, outW, outH);
  ctx.drawImage(img, clip.x, clip.y, clip.w, clip.h, 0, 0, outW, outH);
  var factor = outW / clip.w;
  ctx.save();
  ctx.translate(-clip.x * factor, -clip.y * factor);
  ctx.scale(factor, factor);
  var step = 0;
  state.annotations.forEach(function (shape) {
    if (shape.type === "step") step += 1;
    screenshotDrawShape(ctx, shape, img, shape.type === "step" ? step : 0, factor);
  });
  ctx.restore();
  // Modo evidência: faixa com data/hora UTC, hostname e SHA-256 curto.
  screenshotDrawEvidenceWatermark(ctx, outW, outH, state.evidence);
  // SEMPRE PNG: a imagem anotada é o que o usuário lê e envia ao LLM — o
  // backend reencoda lossless (WebP/PNG) depois, dentro dos limites de payload.
  return canvas.toDataURL("image/png");
}

function screenshotConfirmSelection() {
  var state = screenshotOverlayState;
  if (!state || !state.selection) return;
  var selection = {
    kind: state.selection.kind,
    x: state.selection.x,
    y: state.selection.y,
    width: state.selection.width,
    height: state.selection.height,
    windowHandle: state.selection.windowHandle || 0,
    monitorIndex: state.selection.monitorIndex || 0,
  };
  // Com anotações OU modo evidência a imagem final é composta aqui (a faixa de
  // evidência também precisa entrar no arquivo quando não há traços).
  if (state.annotations.length > 0 || (state.evidence && state.evidence.enabled)) {
    try {
      selection.annotatedDataUrl = screenshotBuildAnnotatedDataUrl();
    } catch (err) {
      screenshotFeedback(screenshotT("screenshot.annotComposeFailed", { error: String(err) }), true);
      return;
    }
  }
  screenshotFinishSelection(selection);
}

// ── Destino do print capturado no overlay ────────────────────────────────
//
// O overlay roda na JANELA DEDICADA (screenshot-overlay.html) e não
// compartilha DOM nem estado com esta janela. Cada captura leva um destino no
// payload e o resultado é reencaminhado por evento global:
//   - "supportChat"   → janela do chat de suporte (upload presigned);
//   - "ticketComment" → composer de comentário do chamado (janela principal);
//   - "llm"/"aiChat"  → janela principal (bolha de transparência ou anexo).

// screenshotPreparePending cobre o intervalo em que o backend ainda congela a
// tela (round-trip do binding PrepareScreenshotOverlay, ~1 s): sem ela, cada
// clique nesse intervalo abria uma captura nova.
var screenshotPreparePending = false;
// screenshotPrepareToken sobrevive ao cancelamento para que o watchdog sempre
// consiga liberar screenshotPreparePending.
var screenshotPrepareToken = null;

function screenshotDestinationDefault() {
  return "aiChat";
}

// supportChatForwardScreenshot entrega o print anotado à janela do chat.
function supportChatForwardScreenshot(result) {
  if (!result || !result.dataUrl) return;
  if (!window.wails || typeof window.wails.emit !== "function") return;
  window.wails.emit("supportchat:screenshot", {
    dataUrl: result.dataUrl,
    at: Date.now(),
  });
}

// screenshotEmitOverlayResult reencaminha o print anotado para a janela que
// pediu a captura. O evento é global; só a janela principal o trata.
function screenshotEmitOverlayResult(result, destination, byLlm, evidence) {
  if (!result || !result.dataUrl) return;
  if (!window.wails || typeof window.wails.emit !== "function") return;
  var payload = {
    destination: destination || screenshotDestinationDefault(),
    byLlm: !!byLlm,
    dataUrl: result.dataUrl,
    width: result.width || 0,
    height: result.height || 0,
    auditId: result.auditId || 0,
    at: Date.now(),
  };
  // Bloco de evidência: a janela principal anexa na bolha de info do print.
  if (evidence && evidence.enabled) payload.evidence = evidence;
  window.wails.emit(SCREENSHOT_OVERLAY_RESULT_EVENT, payload);
}

function screenshotFinishSelection(selection) {
  var state = screenshotOverlayState;
  if (!state) return;
  var api = screenshotApi();
  var payload = state.payload;
  var byLLM = !!payload.requestedByLlm;
  // Guardado ANTES de esconder: screenshotHideOverlay zera o estado global.
  var evidence = state.evidence && state.evidence.enabled ? state.evidence : null;
  if (!api || typeof api.FinishScreenshotOverlay !== "function") {
    // Build antigo sem o binding: cancela no backend (restaura a janela) em vez
    // de só esconder o overlay — senão a janela ficaria cobrindo o desktop.
    screenshotFeedback(screenshotT("screenshot.unavailable"), true);
    screenshotCancelOverlay();
    return;
  }
  screenshotHideOverlay();
  api.FinishScreenshotOverlay(payload.session, JSON.stringify(selection)).then(function (raw) {
    var result = screenshotParse(raw);
    if (!result || !result.ok) {
      screenshotFeedback(screenshotT("screenshot.failed"), true);
      screenshotCancelBackendSession(payload.session);
      supportChatNotifyCaptureFailed("captura recusada pelo agent");
      return;
    }
    // O destino vem no PAYLOAD da sessão (o Go o define ao abrir o overlay
    // para o chat de suporte/comentário). A variável local só cobre capturas
    // pedidas pelo próprio frontend. Sem considerar o payload, uma captura
    // pedida PELO GO para o chat de suporte voltava com destino "aiChat" e o
    // print nunca chegava ao atendente — ficava "enviando" para sempre.
    var destination = (state.payload && state.payload.destination)
      || screenshotOverlayDestination
      || screenshotDestinationDefault();
    // Consome o destino: cada captura define o seu.
    screenshotOverlayDestination = "";
    // Política: copiar a imagem automaticamente ao confirmar (cache com TTL).
    if (destination !== "supportChat" && destination !== "ticketComment") {
      screenshotCopyOnConfirm(result.dataUrl);
    }
    if (destination === "supportChat") {
      supportChatForwardScreenshot(result);
    } else {
      // ticketComment / aiChat / llm: a janela principal aplica o print (o
      // overlay é uma janela própria e não tem o composer nem o chat da IA).
      screenshotEmitOverlayResult(result, destination, byLLM, evidence);
    }
  }).catch(function (err) {
    screenshotFeedback(screenshotT("screenshot.failed") + ": " + err, true);
    // A sessão pode não existir mais no backend (id de uma captura abortada por
    // fechar/ocultar a janela, por exemplo). Cancelar é idempotente e é o que
    // devolve a janela aos bounds normais — sem isto ela continuava frameless e
    // sempre-no-topo cobrindo o desktop inteiro (a "tela toda preta").
    screenshotCancelBackendSession(payload.session);
    supportChatNotifyCaptureFailed(err);
  });
}

// screenshotCancelBackendSession encerra uma sessão do overlay pelo id, sem
// depender do estado local (que o screenshotHideOverlay já zerou).
function screenshotCancelBackendSession(session) {
  if (!session) return;
  var api = screenshotApi();
  if (!api || typeof api.CancelScreenshotOverlay !== "function") return;
  try {
    var pending = api.CancelScreenshotOverlay(session);
    if (pending && typeof pending.catch === "function") pending.catch(function () {});
  } catch (_) {}
}

// supportChatNotifyCaptureFailed avisa a janela do chat de suporte que a
// captura pedida por ela não produziu print: lá o botão fica desabilitado
// esperando o evento "supportchat:screenshot" e, sem este aviso, só voltaria
// pelo watchdog.
function supportChatNotifyCaptureFailed(reason) {
  if (screenshotOverlayDestination !== "supportChat") return;
  screenshotOverlayDestination = "";
  if (!window.wails || typeof window.wails.emit !== "function") return;
  window.wails.emit("supportchat:screenshot_failed", { error: String(reason || "") });
}

function screenshotCancelOverlay() {
  supportChatNotifyCaptureFailed("captura cancelada");
  var state = screenshotOverlayState;
  if (!state) {
    // Cancelamento durante o "preparando": não há sessão ainda. Marca para
    // cancelar/descartar assim que o payload chegar e devolve o chrome agora.
    if (screenshotCaptureToken) {
      screenshotCaptureAborted = true;
      screenshotHideOverlay();
    }
    return;
  }
  var api = screenshotApi();
  var session = state.payload.session;
  screenshotHideOverlay();
  if (api && typeof api.CancelScreenshotOverlay === "function") {
    try {
      var pending = api.CancelScreenshotOverlay(session);
      if (pending && typeof pending.catch === "function") pending.catch(function () {});
    } catch (_) {}
  }
}

// ── Entrada manual + eventos do backend ──────────────────────────────────

/**
 * Abre o overlay de captura assistida.
 *
 * `options.supportChat` marca o destino do resultado: quando vem da janela do
 * chat de suporte, o print anotado é reemitido para ela em vez de virar anexo do
 * chat com a IA. A marca só é aplicada quando o overlay ABRE de fato — se
 * ficasse ligada numa falha, a próxima captura manual do chat com a IA seria
 * entregue ao chat de suporte por engano.
 */
function openScreenshotCapture(options) {
  var api = screenshotApi();
  if (!api || typeof api.PrepareScreenshotOverlay !== "function") {
    screenshotFeedback(screenshotT("screenshot.unavailable"), true);
    return;
  }
  // Já existe uma captura em andamento: não dispara outra. Sem esta guarda,
  // cada clique durante a preparação (~1 s) iniciava uma captura nova — era a
  // origem das 16 chamadas em ~3 s do log e do id órfão no Finish.
  if (screenshotOverlayState || screenshotPreparePending) return;
  screenshotPreparePending = true;
  screenshotFeedback(screenshotT("screenshot.capturing"), false);

  // Destino do print: o overlay é uma JANELA DEDICADA e devolve o resultado
  // por evento; o "para onde" vai no pedido.
  var destination = options && options.supportChat ? "supportChat"
    : options && options.ticketComment ? "ticketComment"
    : "aiChat";

  // Watchdog: a captura roda na janela do overlay; aqui só liberamos o pedido
  // se o backend não responder.
  var token = {};
  screenshotPrepareToken = token;
  setTimeout(function () {
    if (screenshotPrepareToken !== token) return;
    screenshotPrepareToken = null;
    screenshotPreparePending = false;
    if (destination === "supportChat") {
      screenshotOverlayDestination = "supportChat";
      supportChatNotifyCaptureFailed("timeout");
    }
    screenshotFeedback(screenshotT("screenshot.prepareTimeout"), true);
  }, 30000);

  // ESTA janela não desenha mais o overlay: o Go congela a tela e entrega a
  // seleção à janela dedicada (funciona com o agent no tray). Só pedimos.
  api.PrepareScreenshotOverlay("captura manual do usuario", destination).then(function () {
    if (screenshotPrepareToken !== token) return;
    screenshotPrepareToken = null;
    screenshotPreparePending = false;
  }).catch(function (err) {
    if (screenshotPrepareToken !== token) return;
    screenshotPrepareToken = null;
    screenshotPreparePending = false;
    if (destination === "supportChat") {
      screenshotOverlayDestination = "supportChat";
      supportChatNotifyCaptureFailed(err);
    }
    screenshotFeedback(screenshotT("screenshot.failed") + ": " + err, true);
  });
}

// Handler do evento "screenshot:request" (tool MCP capture_screenshot interativa).
function onScreenshotRequest(data) {
  var payload = screenshotParse(data);
  if (!payload || !payload.session) return;
  if (!screenshotIsFresh(payload.at)) return;
  if (screenshotOverlayState && screenshotOverlayState.payload.session === payload.session) return;
  screenshotShowOverlay(payload);
}

// Handler do evento "screenshot:captured": captura automática (sem overlay)
// concluída pela IA. Mostra a miniatura no chat para o usuário ver o que foi
// capturado — transparência exigida pelo modelo de consentimento.
function onScreenshotCaptured(data) {
  var info = screenshotParse(data);
  if (!info || !info.dataUrl) return;
  if (!screenshotIsFresh(info.at)) return;
  var label = info.byLlm ? screenshotT("screenshot.llmCaptured") : screenshotT("screenshot.capturedNow");
  if (info.window && info.window.title) label += " — " + info.window.title;
  screenshotAppendInfoBubble({ dataUrl: info.dataUrl, auditId: info.auditId }, label);
}

// Handler do evento "screenshot:overlay_close" (backend fechou a sessão).
function onScreenshotOverlayClose(data) {
  if (!screenshotOverlayState) return;
  var info = screenshotParse(data);
  if (!info || !info.session || info.session === screenshotOverlayState.payload.session) {
    // A sessão foi encerrada pelo backend (ex.: janela fechada/ocultada durante
    // a captura). Se o print vinha do chat de suporte, avisa para o botão de lá
    // não ficar preso até o watchdog.
    if (info && info.ok === false) supportChatNotifyCaptureFailed("captura encerrada pelo agent");
    screenshotHideOverlay();
  }
}

// ── Painel de privacidade (consentimento + política + auditoria) ─────────

function screenshotModalEl(id) {
  return document.getElementById(id);
}

function openScreenshotPrivacyModal() {
  var modal = screenshotModalEl("chatPrivacyModal");
  if (!modal) return;
  modal.classList.remove("hidden");
  screenshotLoadPrivacy();
}

function closeScreenshotPrivacyModal() {
  var modal = screenshotModalEl("chatPrivacyModal");
  if (modal) modal.classList.add("hidden");
}

function screenshotLoadPrivacy() {
  var api = screenshotApi();
  if (!api || typeof api.GetScreenshotAuditPanel !== "function") {
    screenshotFeedback(screenshotT("screenshot.unavailable"), true);
    return;
  }
  api.GetScreenshotAuditPanel()
    .then(function (raw) {
      var data = screenshotParse(raw);
      if (data) screenshotRenderPrivacy(data);
    })
    .catch(function (err) {
      screenshotFeedback(screenshotT("screenshot.loadFailed", { error: err }), true);
    });
}

// screenshotConsentLabel descreve o modelo de consentimento: POR CAPTURA é o
// padrão; "permitir sempre" é uma permissão PERMANENTE opt-in (desligada por
// padrão, persistida na política) — revogável a qualquer momento pelo usuário.
function screenshotConsentLabel(data) {
  if (data && data.aiCaptureEnabled === false) {
    return screenshotT("screenshot.consentDisabled");
  }
  if (data && data.sessionAllow === true) {
    return screenshotT("screenshot.consentSession");
  }
  return screenshotT("screenshot.consentPerCapture");
}

function screenshotRenderPrivacy(data) {
  var stateEl = screenshotModalEl("chatPrivacyState");
  if (stateEl) {
    stateEl.textContent = screenshotConsentLabel(data);
    var sessionOn = data.sessionAllow === true;
    stateEl.classList.toggle("allowed", data.aiCaptureEnabled !== false && !sessionOn);
    stateEl.classList.toggle("session", data.aiCaptureEnabled !== false && sessionOn);
    stateEl.classList.toggle("denied", data.aiCaptureEnabled === false);
  }
  var allowAiEl = screenshotModalEl("chatPrivacyAllowAi");
  if (allowAiEl) allowAiEl.checked = data.aiCaptureEnabled !== false;
  var allowSessionEl = screenshotModalEl("chatPrivacyAllowSession");
  if (allowSessionEl) {
    allowSessionEl.checked = data.sessionAllow === true;
    // Sem pedidos da IA a permissão permanente não tem efeito: deixar marcar
    // criaria uma autorização "pendente" que valeria ao religar os pedidos.
    allowSessionEl.disabled = data.aiCaptureEnabled === false;
  }
  var usageEl = screenshotModalEl("chatPrivacyUsage");
  if (usageEl && data.usage) {
    usageEl.textContent = screenshotT("screenshot.usage", {
      used: data.usage.used,
      max: data.usage.max,
      minutes: data.usage.windowMinutes,
    });
  }
  var policy = data.policy || {};
  var blockedEl = screenshotModalEl("chatPrivacyBlocked");
  if (blockedEl) blockedEl.value = (policy.blockedProcesses || []).join("\n");
  var allowFullEl = screenshotModalEl("chatPrivacyAllowFull");
  if (allowFullEl) allowFullEl.checked = policy.allowFullScreen !== false;
  var requireWindowEl = screenshotModalEl("chatPrivacyRequireWindow");
  if (requireWindowEl) requireWindowEl.checked = policy.requireWindow === true;
  var maxEl = screenshotModalEl("chatPrivacyMax");
  if (maxEl) maxEl.value = String(policy.maxCapturesPerWindow || 10);
  var windowEl = screenshotModalEl("chatPrivacyWindow");
  if (windowEl) windowEl.value = String(policy.windowMinutes || 5);
  var hideWindowEl = screenshotModalEl("chatPrivacyHideWindow");
  if (hideWindowEl) hideWindowEl.checked = policy.hideAgentWindow !== false;
  var formatEl = screenshotModalEl("chatPrivacyImageFormat");
  if (formatEl) {
    var fmt = policy.imageFormat === "png" || policy.imageFormat === "webp" ? policy.imageFormat : "auto";
    formatEl.value = fmt;
  }
  screenshotRenderAudit(data.audit || []);
}

function screenshotRenderAudit(items) {
  screenshotHideHoverPreview();
  var container = screenshotModalEl("chatPrivacyAudit");
  if (!container) return;
  container.innerHTML = "";
  if (!items.length) {
    var empty = document.createElement("div");
    empty.className = "meta";
    empty.textContent = screenshotT("screenshot.auditEmpty");
    container.appendChild(empty);
    return;
  }
  items.forEach(function (item) {
    var row = document.createElement("div");
    row.className = "chat-privacy-item";
    if (item.thumbnail) {
      var img = document.createElement("img");
      img.className = "chat-privacy-thumb";
      img.src = item.thumbnail;
      img.alt = "print";
      img.title = screenshotT("screenshot.viewLarger");
      if (item.id) img.setAttribute("data-screenshot-id", String(item.id));
      row.appendChild(img);
    }
    var info = document.createElement("div");
    info.className = "chat-privacy-info";
    var who = item.byLlm ? screenshotT("screenshot.auditByLlm") : screenshotT("screenshot.auditManual");
    var decision =
      item.decision === "granted" || item.decision === "granted_session"
        ? item.sessionAuthorized
          ? screenshotT("screenshot.auditGrantedSession")
          : screenshotT("screenshot.auditGranted")
        : item.decision === "denied"
          ? screenshotT("screenshot.auditDenied")
          : screenshotT("screenshot.auditCancelled");
    var when = "";
    try {
      when = item.at ? new Date(item.at).toLocaleString() : "";
    } catch (_) {}
    var head = document.createElement("div");
    head.className = "chat-privacy-line";
    head.textContent =
      when + " · " + item.mode + " · " + who + " · " + decision + " · " + Math.round((item.bytes || 0) / 1024) + " KB";
    var detail = document.createElement("div");
    detail.className = "meta";
    detail.textContent = item.detail || "";
    info.appendChild(head);
    info.appendChild(detail);
    row.appendChild(info);
    container.appendChild(row);
  });
}

function screenshotSavePrivacyPolicy() {
  var api = screenshotApi();
  if (!api || typeof api.SaveScreenshotPolicy !== "function") {
    screenshotFeedback(screenshotT("screenshot.unavailable"), true);
    return;
  }
  var blockedEl = screenshotModalEl("chatPrivacyBlocked");
  var allowFullEl = screenshotModalEl("chatPrivacyAllowFull");
  var requireWindowEl = screenshotModalEl("chatPrivacyRequireWindow");
  var maxEl = screenshotModalEl("chatPrivacyMax");
  var windowEl = screenshotModalEl("chatPrivacyWindow");
  var hideWindowEl = screenshotModalEl("chatPrivacyHideWindow");
  var formatEl = screenshotModalEl("chatPrivacyImageFormat");
  var payload = {
    blockedProcesses: blockedEl ? blockedEl.value.split(/\r?\n/) : [],
    allowFullScreen: allowFullEl ? !!allowFullEl.checked : true,
    requireWindow: requireWindowEl ? !!requireWindowEl.checked : false,
    maxCapturesPerWindow: maxEl ? Number(maxEl.value) || 0 : 0,
    windowMinutes: windowEl ? Number(windowEl.value) || 0 : 0,
    hideAgentWindow: hideWindowEl ? !!hideWindowEl.checked : true,
    imageFormat: formatEl ? formatEl.value : "auto",
  };
  // Campos que esta tela ainda não edita (copyOnConfirm/galeria): preservados da
  // política vigente para o "Salvar política" não voltá-los ao padrão.
  var copyEl = screenshotModalEl("chatPrivacyCopyOnConfirm");
  var galleryEl = screenshotModalEl("chatPrivacyGalleryEnabled");
  var galleryMaxEl = screenshotModalEl("chatPrivacyGalleryMax");
  // Se a leitura da política falhar, o salvamento segue SEM os campos que esta
  // tela não edita: o backend preserva a política vigente. Antes, um fallback
  // (raw vazio) enviava copyOnConfirm=false/galleryEnabled=true e desligava a
  // cópia ao confirmar (e religava a galeria) em silêncio.
  screenshotLoadPolicy(true)
    .catch(function () { return null; })
    .then(function (policy) {
      var raw = policy && policy.raw ? policy.raw : {};
      var known = !!(policy && policy.loaded);

      if (copyEl) payload.copyOnConfirm = !!copyEl.checked;
      else if (known) payload.copyOnConfirm = raw.copyOnConfirm === true;

      if (galleryEl) payload.galleryEnabled = !!galleryEl.checked;
      else if (known) payload.galleryEnabled = raw.galleryEnabled !== false;

      var maxItems = galleryMaxEl
        ? Number(galleryMaxEl.value)
        : (known ? Number(raw.galleryMaxItems) : NaN);
      if (maxItems > 0) payload.galleryMaxItems = maxItems;

      return api.SaveScreenshotPolicy(JSON.stringify(payload));
    })
    .then(function () {
      // A próxima captura precisa ver copyOnConfirm/galeria já atualizados.
      screenshotInvalidatePolicyCache();
      screenshotFeedback(screenshotT("screenshot.policySaved"), false);
      screenshotLoadPrivacy();
    })
    .catch(function (err) {
      screenshotFeedback(screenshotT("screenshot.saveFailed", { error: err }), true);
    });
}

// screenshotSetSessionAllow liga/desliga a permissão "permitir sempre". Opt-in e
// desligada por padrão: ligada, a IA deixa de abrir a pergunta por captura até o
// usuário desmarcar (o valor é persistido e sobrevive a reinício do agente).
function screenshotSetSessionAllow(enabled) {
  var api = screenshotApi();
  if (!api || typeof api.SetScreenshotSessionAllow !== "function") {
    screenshotFeedback(screenshotT("screenshot.unavailable"), true);
    return;
  }
  var pending = api.SetScreenshotSessionAllow(!!enabled);
  if (pending && typeof pending.then === "function") {
    pending
      .then(function () {
        screenshotLoadPrivacy();
      })
      .catch(function (err) {
        // Recarrega também no erro para o checkbox não ficar com estado fantasma.
        screenshotFeedback(String(err), true);
        screenshotLoadPrivacy();
      });
  } else {
    screenshotLoadPrivacy();
  }
}

// screenshotResetPrivacyPolicy volta a política local aos padrões (o valor
// padrão vem do Go — fonte única) e revoga a autorização de sessão.
function screenshotResetPrivacyPolicy() {
  var api = screenshotApi();
  if (!api || typeof api.ResetScreenshotPolicy !== "function") {
    screenshotFeedback(screenshotT("screenshot.unavailable"), true);
    return;
  }
  if (typeof window !== "undefined" && typeof window.confirm === "function" &&
    !window.confirm(screenshotT("screenshot.resetPolicyConfirm"))) {
    // Cancelou: recarrega para o formulário voltar ao estado persistido.
    screenshotLoadPrivacy();
    return;
  }
  api.ResetScreenshotPolicy()
    .then(function () {
      screenshotInvalidatePolicyCache();
      screenshotFeedback(screenshotT("screenshot.policyReset"), false);
      screenshotLoadPrivacy();
    })
    .catch(function (err) {
      screenshotFeedback(screenshotT("screenshot.resetFailed", { error: err }), true);
      screenshotLoadPrivacy();
    });
}

// screenshotSetAiCapture liga/desliga os PEDIDOS de captura da IA. Não é
// autorização: ligado, a IA continua perguntando em cada captura.
function screenshotSetAiCapture(enabled) {
  var api = screenshotApi();
  if (!api || typeof api.SetScreenshotAiCaptureEnabled !== "function") {
    screenshotFeedback(screenshotT("screenshot.unavailable"), true);
    return;
  }
  var pending = api.SetScreenshotAiCaptureEnabled(!!enabled);
  if (pending && typeof pending.then === "function") {
    pending
      .then(function () {
        screenshotLoadPrivacy();
      })
      .catch(function (err) {
        // Recarrega também no erro: o checkbox voltaria marcado sem a política
        // ter sido persistida (estado fantasma na UI).
        screenshotFeedback(String(err), true);
        screenshotLoadPrivacy();
      });
  } else {
    screenshotLoadPrivacy();
  }
}

// ── Lightbox / prévia ampliada das miniaturas ────────────────────────────

// Cache id→dataUrl: clicar duas vezes na mesma miniatura não refaz o fetch.
var screenshotLightboxCache = {};

function screenshotCacheLightboxImage(id, dataUrl) {
  if (!id || !dataUrl) return;
  var keys = Object.keys(screenshotLightboxCache);
  if (keys.length >= 8 && !screenshotLightboxCache[id]) {
    delete screenshotLightboxCache[keys[0]];
  }
  screenshotLightboxCache[id] = dataUrl;
}

function screenshotEnsureLightbox() {
  if (document.getElementById("screenshotLightbox")) return;
  var box = document.createElement("div");
  box.id = "screenshotLightbox";
  box.className = "screenshot-lightbox hidden";
  box.innerHTML = [
    '<div class="screenshot-lightbox-card">',
    '  <div class="screenshot-lightbox-head">',
    '    <span id="screenshotLightboxCaption" class="screenshot-lightbox-caption"></span>',
    '    <span class="screenshot-lightbox-buttons">',
    '      <span id="screenshotLightboxZoom" class="screenshot-lightbox-zoom">100%</span>',
    '      <button id="screenshotLightboxZoomFit" class="btn subtle" type="button" data-i18n-title="screenshot.zoomFit">⤢</button>',
    '      <button id="screenshotLightboxOcr" class="btn subtle" type="button"></button>',
    '      <button id="screenshotLightboxCopy" class="btn subtle" type="button"></button>',
    '      <button id="screenshotLightboxClose" class="btn danger" type="button"></button>',
    '    </span>',
    '  </div>',
    '  <img id="screenshotLightboxImage" alt="print" />',
    '</div>',
  ].join("");
  document.body.appendChild(box);
  var close = document.getElementById("screenshotLightboxClose");
  if (close) {
    close.textContent = screenshotT("action.close");
    close.addEventListener("click", screenshotCloseLightbox);
  }
  var copy = document.getElementById("screenshotLightboxCopy");
  if (copy) {
    copy.textContent = screenshotT("screenshot.copy");
    copy.addEventListener("click", screenshotCopyLightboxImage);
  }
  var ocrBtn = document.getElementById("screenshotLightboxOcr");
  if (ocrBtn) {
    ocrBtn.textContent = screenshotT("screenshot.ocrAction");
    ocrBtn.title = screenshotT("screenshot.ocrAction");
    ocrBtn.addEventListener("click", function () {
      var img = document.getElementById("screenshotLightboxImage");
      var src = img ? img.getAttribute("src") || img.src : "";
      var cap = document.getElementById("screenshotLightboxCaption");
      screenshotOpenOcr(src, cap ? cap.textContent || "" : "");
    });
  }
  var fit = document.getElementById("screenshotLightboxZoomFit");
  if (fit) fit.addEventListener("click", screenshotResetLightboxView);
  var lightboxImg = document.getElementById("screenshotLightboxImage");
  if (lightboxImg) {
    // Zoom na roda (ancorado no cursor), arrastar para mover e duplo clique
    // para voltar ao tamanho ajustado.
    lightboxImg.addEventListener("wheel", screenshotLightboxWheel, { passive: false });
    lightboxImg.addEventListener("mousedown", screenshotLightboxDragStart);
    lightboxImg.addEventListener("dblclick", screenshotResetLightboxView);
  }
  box.addEventListener("click", function (event) {
    if (event.target === box) screenshotCloseLightbox();
  });
}

function screenshotOpenLightbox(dataUrl, caption) {
  if (!dataUrl) return;
  screenshotEnsureLightbox();
  var box = document.getElementById("screenshotLightbox");
  var img = document.getElementById("screenshotLightboxImage");
  var cap = document.getElementById("screenshotLightboxCaption");
  if (!box || !img) return;
  screenshotResetLightboxView();
  img.src = dataUrl;
  if (cap) cap.textContent = caption || "";
  box.classList.remove("hidden");
}

function screenshotCloseLightbox() {
  var box = document.getElementById("screenshotLightbox");
  if (box) box.classList.add("hidden");
}

// ── Zoom/pan do lightbox (leitura do print já capturado) ─────────────────
//
// Aqui não há matemática de coordenadas: a imagem só é exibida, então o zoom é
// um transform (com origem no cursor para o zoom ancorado) e o arraste move.
var screenshotLightboxView = { zoom: 1, x: 0, y: 0 };
var screenshotLightboxZoomMin = 1;
var screenshotLightboxZoomMax = 8;
var screenshotLightboxZoomStep = 1.2;

function screenshotLightboxImage() {
  return document.getElementById("screenshotLightboxImage");
}

function screenshotApplyLightboxView() {
  var img = screenshotLightboxImage();
  if (!img) return;
  var view = screenshotLightboxView;
  view.zoom = Math.max(screenshotLightboxZoomMin, Math.min(screenshotLightboxZoomMax, view.zoom));
  img.style.transform = "translate(" + view.x + "px," + view.y + "px) scale(" + view.zoom + ")";
  img.style.cursor = view.zoom > 1 ? "grab" : "zoom-in";
  var badge = document.getElementById("screenshotLightboxZoom");
  if (badge) badge.textContent = Math.round(view.zoom * 100) + "%";
}

function screenshotResetLightboxView() {
  var view = screenshotLightboxView;
  view.zoom = 1;
  view.x = 0;
  view.y = 0;
  var img = screenshotLightboxImage();
  if (img) img.style.transformOrigin = "50% 50%";
  screenshotApplyLightboxView();
}

// screenshotLightboxWheel dá zoom ancorado no cursor: a origem do transform vai
// para o ponto sob o mouse (em % da imagem), então ele fica parado ao escalar.
function screenshotLightboxWheel(event) {
  var img = screenshotLightboxImage();
  if (!img || !img.naturalWidth) return;
  event.preventDefault();
  var rect = img.getBoundingClientRect();
  if (rect.width <= 0 || rect.height <= 0) return;
  var originX = ((event.clientX - rect.left) / rect.width) * 100;
  var originY = ((event.clientY - rect.top) / rect.height) * 100;
  img.style.transformOrigin = originX.toFixed(2) + "% " + originY.toFixed(2) + "%";
  var factor = event.deltaY < 0 ? screenshotLightboxZoomStep : 1 / screenshotLightboxZoomStep;
  screenshotLightboxView.zoom *= factor;
  if (screenshotLightboxView.zoom <= 1.001) {
    screenshotResetLightboxView();
    return;
  }
  screenshotApplyLightboxView();
}

// screenshotLightboxDragStart move o print ampliado (limitado para não perder a
// imagem de vista).
function screenshotLightboxDragStart(event) {
  var view = screenshotLightboxView;
  var img = screenshotLightboxImage();
  if (!img || event.button !== 0 || view.zoom <= 1) return;
  event.preventDefault();
  var rect = img.getBoundingClientRect();
  var maxX = Math.max(60, rect.width * 0.6);
  var maxY = Math.max(60, rect.height * 0.6);
  var startX = event.clientX;
  var startY = event.clientY;
  var x0 = view.x;
  var y0 = view.y;
  function onMove(ev) {
    view.x = Math.max(-maxX, Math.min(maxX, x0 + (ev.clientX - startX)));
    view.y = Math.max(-maxY, Math.min(maxY, y0 + (ev.clientY - startY)));
    screenshotApplyLightboxView();
  }
  function onUp() {
    document.removeEventListener("mousemove", onMove);
    document.removeEventListener("mouseup", onUp);
  }
  document.addEventListener("mousemove", onMove);
  document.addEventListener("mouseup", onUp);
}

// screenshotOpenLightboxById busca a imagem cheia da captura (auditId) — o chat
// guarda só a miniatura; o backend mantém as últimas capturas em memória.
function screenshotOpenLightboxById(id) {
  var cached = screenshotLightboxCache[id];
  if (cached) {
    screenshotOpenLightbox(cached, "");
    return;
  }
  var api = screenshotApi();
  if (!api || typeof api.GetScreenshotImage !== "function") {
    screenshotFeedback(screenshotT("screenshot.imageExpired"), true);
    return;
  }
  api.GetScreenshotImage(Number(id))
    .then(function (raw) {
      var data = screenshotParse(raw);
      if (!data || !data.dataUrl) {
        screenshotFeedback(screenshotT("screenshot.imageExpired"), true);
        return;
      }
      screenshotCacheLightboxImage(id, data.dataUrl);
      screenshotOpenLightbox(data.dataUrl, (data.width || 0) + "\u00d7" + (data.height || 0));
    })
    .catch(function () {
      screenshotFeedback(screenshotT("screenshot.imageExpired"), true);
    });
}

// screenshotCopyLightboxImage copia a imagem aberta no lightbox para a área de
// transferência do Windows (CF_DIB via binding CopyImageDataURLToClipboard).
function screenshotCopyLightboxImage() {
  var img = document.getElementById("screenshotLightboxImage");
  var api = screenshotApi();
  var src = img ? img.getAttribute("src") || img.src : "";
  if (!src) return;
  if (!api || typeof api.CopyImageDataURLToClipboard !== "function") {
    screenshotFeedback(screenshotT("screenshot.unavailable"), true);
    return;
  }
  api.CopyImageDataURLToClipboard(src)
    .then(function () {
      screenshotFeedback(screenshotT("screenshot.copied"), false);
    })
    .catch(function (err) {
      screenshotFeedback(screenshotT("screenshot.copyFailed") + ": " + err, true);
    });
}

function screenshotOpenThumb(element) {
  if (!element) return;
  var id = element.getAttribute("data-screenshot-id");
  if (id) {
    screenshotOpenLightboxById(id);
    return;
  }
  screenshotOpenLightbox(element.getAttribute("src") || element.src || "", element.getAttribute("alt") || "");
}

// screenshotThumbFromEvent: alvos CLICÁVEIS (timeline, anexos do composer e
// auditoria). O clique abre o lightbox com a imagem cheia.
function screenshotThumbFromEvent(event) {
  var target = event.target;
  if (!target || !target.closest) return null;
  return target.closest(".chat-msg-image, .chat-attachment img, .chat-privacy-thumb");
}

// screenshotHoverTargetFromEvent: prévia ao passar o mouse SOMENTE na imagem
// anexada na caixa de texto (antes de enviar). Imagens da timeline do chat não
// abrem prévia no hover — nelas a visualização é por CLIQUE (lightbox), para não
// cobrir a conversa enquanto o usuário lê.
function screenshotHoverTargetFromEvent(event) {
  var target = event.target;
  if (!target || !target.closest) return null;
  return target.closest("#chatAttachments .chat-attachment img");
}

function screenshotEnsureHoverPreview() {
  if (document.getElementById("screenshotHoverPreview")) return;
  var box = document.createElement("div");
  box.id = "screenshotHoverPreview";
  box.className = "screenshot-hover-preview hidden";
  box.innerHTML = '<img id="screenshotHoverPreviewImage" alt="preview" />';
  document.body.appendChild(box);
}

function screenshotShowHoverPreview(element) {
  var src = element.getAttribute("src") || element.src;
  if (!src) return;
  screenshotEnsureHoverPreview();
  var box = document.getElementById("screenshotHoverPreview");
  var img = document.getElementById("screenshotHoverPreviewImage");
  if (!box || !img) return;
  img.src = src;
  var rect = element.getBoundingClientRect();
  box.classList.remove("hidden");
  var left = rect.right + 12;
  if (left + 360 > window.innerWidth) left = Math.max(12, rect.left - 372);
  box.style.left = left + "px";
  box.style.top = Math.max(12, Math.min(rect.top - 10, window.innerHeight - 300)) + "px";
}

function screenshotHideHoverPreview() {
  var box = document.getElementById("screenshotHoverPreview");
  if (box) box.classList.add("hidden");
}

function initScreenshotLightbox() {
  document.addEventListener("click", function (event) {
    var thumb = screenshotThumbFromEvent(event);
    if (!thumb) return;
    event.preventDefault();
    screenshotOpenThumb(thumb);
  });
  document.addEventListener("mouseover", function (event) {
    var hoverTarget = screenshotHoverTargetFromEvent(event);
    if (hoverTarget) screenshotShowHoverPreview(hoverTarget);
  });
  document.addEventListener("mouseout", function (event) {
    if (screenshotHoverTargetFromEvent(event)) screenshotHideHoverPreview();
  });
  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape") screenshotCloseLightbox();
  });
}

function initScreenshotPrivacy() {
  var button = document.getElementById("chatPrivacyBtn");
  if (button) button.addEventListener("click", openScreenshotPrivacyModal);
  var closeBtn = document.getElementById("chatPrivacyCloseBtn");
  if (closeBtn) closeBtn.addEventListener("click", closeScreenshotPrivacyModal);
  var refreshBtn = document.getElementById("chatPrivacyRefreshBtn");
  if (refreshBtn) refreshBtn.addEventListener("click", screenshotLoadPrivacy);
  var resetBtn = document.getElementById("chatPrivacyResetPolicy");
  if (resetBtn) resetBtn.addEventListener("click", screenshotResetPrivacyPolicy);
  var saveBtn = document.getElementById("chatPrivacySavePolicy");
  if (saveBtn) saveBtn.addEventListener("click", screenshotSavePrivacyPolicy);
  var allowAi = document.getElementById("chatPrivacyAllowAi");
  if (allowAi) {
    allowAi.addEventListener("change", function () { screenshotSetAiCapture(!!allowAi.checked); });
  }
  var allowSession = document.getElementById("chatPrivacyAllowSession");
  if (allowSession) {
    allowSession.addEventListener("change", function () { screenshotSetSessionAllow(!!allowSession.checked); });
  }
  var modal = screenshotModalEl("chatPrivacyModal");
  if (modal) {
    modal.addEventListener("click", function (event) {
      if (event.target === modal) closeScreenshotPrivacyModal();
    });
  }
}

function initScreenshotCapture() {
  // A janela DEDICADA do overlay não tem botão de câmera nem composer: ela só
  // desenha a seleção (o boot é feito por app-screenshot-overlay.js).
  if (screenshotOverlayHost) return;

  var button = document.getElementById("chatScreenshotBtn");
  if (button) {
    button.addEventListener("click", function () { openScreenshotCapture(); });
  }
  // Menu da câmera (atraso/rolagem) + acesso à galeria de capturas.
  screenshotEnsureComposerControls();

  if (window.wails && typeof window.wails.on === "function") {
    // Print anotado produzido na janela DEDICADA do overlay. O pedido do chat
    // de suporte não passa mais por aqui: o Go conduz a captura direto e a
    // janela do chat recebe "supportchat:screenshot".
    window.wails.on(SCREENSHOT_OVERLAY_RESULT_EVENT, onScreenshotOverlayResult);
  }
  initScreenshotPrivacy();
  initScreenshotLightbox();
  initScreenshotPaste();
  screenshotRenderAttachments();
}

// onScreenshotOverlayResult aplica o print anotado que veio da janela dedicada
// do overlay: anexo do chat com a IA, comentário de chamado ou bolha de
// transparência (captura pedida pela IA).
function onScreenshotOverlayResult(data) {
  var info = screenshotParse(data);
  if (!info || !info.dataUrl) return;
  if (!screenshotIsFresh(info.at)) return;
  if (info.destination === "supportChat") return; // tratado na janela do chat
  if (info.destination === "ticketComment") {
    if (typeof window.dsvTicketCommentScreenshot === "function") {
      window.dsvTicketCommentScreenshot(info.dataUrl);
    }
    return;
  }
  // Modo evidência: o print é documentado na timeline (com o bloco de
  // evidência) em vez de virar anexo solto do composer.
  if (info.evidence) {
    screenshotAppendInfoBubble(
      { dataUrl: info.dataUrl, auditId: info.auditId || 0 },
      screenshotT("screenshot.evidenceCaptured"),
      info.evidence,
    );
    return;
  }
  if (info.byLlm) {
    screenshotAppendInfoBubble(
      { dataUrl: info.dataUrl, auditId: info.auditId || 0 },
      screenshotT("screenshot.llmCaptured"),
    );
    return;
  }
  screenshotAttach({ dataUrl: info.dataUrl, width: info.width || 0, height: info.height || 0 });
}

// ── Barra de dicas e bind das extensões do overlay ───────────────────────

function screenshotSetShortcutBarVisible(visible) {
  var bar = document.getElementById("screenshotShortcutBar");
  if (!bar) return;
  bar.classList.toggle("hidden", !visible);
}

function screenshotShortcutHintText() {
  return screenshotT("screenshot.shortcuts");
}

// screenshotApplyOverlayTranslations aplica os textos/títulos/aria do overlay
// manualmente: o overlay é uma janela própria e pode não ter o observer de i18n
// da janela principal rodando quando o conteúdo é montado por JS.
function screenshotApplyOverlayTranslations() {
  var root = screenshotOverlayRoot();
  if (!root || !root.querySelectorAll) return;
  Array.prototype.forEach.call(root.querySelectorAll("[data-i18n]"), function (el) {
    el.textContent = screenshotT(el.getAttribute("data-i18n"));
  });
  Array.prototype.forEach.call(root.querySelectorAll("[data-i18n-title]"), function (el) {
    el.setAttribute("title", screenshotT(el.getAttribute("data-i18n-title")));
  });
  Array.prototype.forEach.call(root.querySelectorAll("[data-i18n-aria-label]"), function (el) {
    el.setAttribute("aria-label", screenshotT(el.getAttribute("data-i18n-aria-label")));
  });
  Array.prototype.forEach.call(root.querySelectorAll("[data-i18n-placeholder]"), function (el) {
    el.setAttribute("placeholder", screenshotT(el.getAttribute("data-i18n-placeholder")));
  });
}

// screenshotInitOverlayExtras liga os controles criados junto do overlay:
// salvar/copiar caminho/evidência, dicas de atalho e o seletor de janelas.
function screenshotInitOverlayExtras() {
  var saveBtn = document.getElementById("screenshotAnnotSave");
  if (saveBtn) saveBtn.addEventListener("click", screenshotOverlaySave);
  var pathBtn = document.getElementById("screenshotAnnotCopyPath");
  if (pathBtn) pathBtn.addEventListener("click", screenshotOverlayCopyPath);
  var evidenceBtn = document.getElementById("screenshotAnnotEvidence");
  if (evidenceBtn) evidenceBtn.addEventListener("click", screenshotToggleEvidence);
  screenshotUpdateCopyPathButton();

  var bar = document.getElementById("screenshotShortcutBar");
  if (bar) bar.textContent = screenshotShortcutHintText();

  var pickerClose = document.getElementById("screenshotWindowPickerClose");
  if (pickerClose) {
    pickerClose.title = screenshotT("action.close");
    pickerClose.setAttribute("aria-label", screenshotT("action.close"));
    pickerClose.addEventListener("click", function () { screenshotToggleWindowPicker(false); });
  }
  var picker = document.getElementById("screenshotWindowPicker");
  if (picker) picker.setAttribute("aria-label", screenshotT("screenshot.windowPickerTitle"));
  var search = document.getElementById("screenshotWindowSearch");
  if (search) {
    search.placeholder = screenshotT("screenshot.windowSearch");
    search.setAttribute("aria-label", screenshotT("screenshot.windowSearch"));
    search.addEventListener("input", function () { screenshotRenderWindowList(search.value); });
    search.addEventListener("keydown", function (event) {
      // Escape/Enter no campo pertencem ao seletor, não ao overlay.
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        screenshotToggleWindowPicker(false);
      } else if (event.key === "Enter") {
        event.preventDefault();
        event.stopPropagation();
        screenshotWindowPickerChooseFirst();
      }
    });
  }
  screenshotApplyOverlayTranslations();
}

// ── Composer: menu da câmera (atraso/rolagem) e botão da galeria ─────────

function screenshotMenuEl() {
  return document.getElementById("screenshotComposerMenu");
}

function screenshotCloseComposerMenu() {
  var menu = screenshotMenuEl();
  if (menu) menu.classList.add("hidden");
}

function screenshotMenuAction(labelKey, onClick) {
  var item = document.createElement("button");
  item.type = "button";
  item.className = "screenshot-menu-item";
  item.setAttribute("role", "menuitem");
  item.textContent = screenshotT(labelKey);
  item.addEventListener("click", function () {
    screenshotCloseComposerMenu();
    onClick();
  });
  return item;
}

function screenshotOpenComposerMenu() {
  var menu = screenshotMenuEl();
  var camera = document.getElementById("chatScreenshotBtn");
  if (!menu) return;
  if (!menu.classList.contains("hidden")) {
    screenshotCloseComposerMenu();
    return;
  }
  var rect = camera ? camera.getBoundingClientRect() : { left: 16, top: 16, bottom: 16, right: 16 };
  menu.style.left = Math.max(8, Math.min(rect.left, window.innerWidth - 230)) + "px";
  menu.style.top = Math.max(8, rect.top - 8) + "px";
  menu.style.bottom = "auto";
  // Abre ACIMA da câmera quando não há espaço abaixo.
  var height = menu.offsetHeight || 220;
  if (rect.bottom + 8 + height > window.innerHeight) {
    menu.style.top = Math.max(8, rect.top - height - 8) + "px";
  }
  menu.classList.remove("hidden");
}

// screenshotEnsureComposerControls injeta o botão de menu da câmera (atraso,
// rolagem e galeria) ao lado do ícone de câmera do composer (sem tocar no HTML do chat).
function screenshotEnsureComposerControls() {
  if (screenshotOverlayHost) return;
  var camera = document.getElementById("chatScreenshotBtn");
  if (!camera) return;

  if (!document.getElementById("chatScreenshotMenuBtn")) {
    var menuBtn = document.createElement("button");
    menuBtn.id = "chatScreenshotMenuBtn";
    menuBtn.type = "button";
    menuBtn.className = "chat-icon-btn chat-screenshot-menu-btn";
    // Ícone de chevron em SVG (nítido em qualquer DPI, herda a cor do tema).
    menuBtn.innerHTML =
      '<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">' +
      '<path d="M6 9.5 12 15.5 18 9.5" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/>' +
      "</svg>";
    menuBtn.setAttribute("aria-haspopup", "menu");
    menuBtn.setAttribute("aria-expanded", "false");
    menuBtn.setAttribute("aria-label", screenshotT("screenshot.menuLabel"));
    menuBtn.title = screenshotT("screenshot.menuLabel");
    menuBtn.addEventListener("click", function () {
      var open = !screenshotMenuEl() || screenshotMenuEl().classList.contains("hidden");
      screenshotOpenComposerMenu();
      menuBtn.setAttribute("aria-expanded", open ? "true" : "false");
    });
    camera.insertAdjacentElement("afterend", menuBtn);
  }

  // A galeria NÃO tem botão próprio no composer: fica apenas no item
  // "Últimas capturas" do menu (a política que a esconde continua valendo lá).

  if (!screenshotMenuEl()) {
    var menu = document.createElement("div");
    menu.id = "screenshotComposerMenu";
    menu.className = "screenshot-menu hidden";
    menu.setAttribute("role", "menu");
    menu.appendChild(screenshotMenuAction("screenshot.menuCaptureNow", function () {
      openScreenshotCapture({});
    }));
    menu.appendChild(screenshotMenuAction("screenshot.delay3", function () { screenshotStartDelayedCapture(3); }));
    menu.appendChild(screenshotMenuAction("screenshot.delay5", function () { screenshotStartDelayedCapture(5); }));
    menu.appendChild(screenshotMenuAction("screenshot.delay10", function () { screenshotStartDelayedCapture(10); }));
    var sep = document.createElement("div");
    sep.className = "screenshot-menu-sep";
    menu.appendChild(sep);
    menu.appendChild(screenshotMenuAction("screenshot.scrollingCapture", screenshotCaptureScrolling));
    menu.appendChild(screenshotMenuAction("screenshot.galleryButton", screenshotOpenGallery));
    document.body.appendChild(menu);
  }

  // Fecha o menu ao clicar fora (e no Esc).
  if (document.body.dataset.screenshotMenuBound !== "1") {
    document.body.dataset.screenshotMenuBound = "1";
    document.addEventListener("click", function (event) {
      var menu = screenshotMenuEl();
      if (!menu || menu.classList.contains("hidden")) return;
      if (menu.contains(event.target)) return;
      if (event.target && event.target.closest && event.target.closest("#chatScreenshotMenuBtn")) return;
      screenshotCloseComposerMenu();
    });
    document.addEventListener("keydown", function (event) {
      if (event.key === "Escape") screenshotCloseComposerMenu();
    });
  }
}

// ── Captura com atraso (3/5/10 s) ────────────────────────────────────────

var screenshotDelayState = null;

function screenshotDelayOverlayEl() {
  var el = document.getElementById("screenshotDelayOverlay");
  if (el) return el;
  el = document.createElement("div");
  el.id = "screenshotDelayOverlay";
  el.className = "screenshot-delay-overlay hidden";
  el.setAttribute("role", "status");
  el.setAttribute("aria-live", "polite");
  el.innerHTML = [
    '<div class="screenshot-delay-card">',
    '  <div id="screenshotDelayCount" class="screenshot-delay-count">3</div>',
    '  <div id="screenshotDelayHint" class="screenshot-delay-hint"></div>',
    '  <button id="screenshotDelayCancel" type="button" class="btn subtle"></button>',
    '</div>',
  ].join("");
  document.body.appendChild(el);
  var cancel = document.getElementById("screenshotDelayCancel");
  if (cancel) cancel.addEventListener("click", screenshotCancelDelayedCapture);
  return el;
}

function screenshotCancelDelayedCapture() {
  if (!screenshotDelayState) return;
  var state = screenshotDelayState;
  screenshotDelayState = null;
  clearInterval(state.timer);
  if (state.onKey) document.removeEventListener("keydown", state.onKey);
  var el = document.getElementById("screenshotDelayOverlay");
  if (el) el.classList.add("hidden");
}

function screenshotStartDelayedCapture(seconds) {
  var total = Math.max(1, Math.round(Number(seconds) || 3));
  screenshotCancelDelayedCapture();
  var el = screenshotDelayOverlayEl();
  var count = document.getElementById("screenshotDelayCount");
  var hint = document.getElementById("screenshotDelayHint");
  var cancel = document.getElementById("screenshotDelayCancel");
  if (hint) hint.textContent = screenshotT("screenshot.delayHint", { seconds: total });
  if (cancel) cancel.textContent = screenshotT("screenshot.delayCancel");
  el.classList.remove("hidden");
  var state = { remaining: total, timer: null, onKey: null };
  function render() {
    if (count) count.textContent = String(Math.max(0, state.remaining));
    if (hint) hint.textContent = screenshotT("screenshot.delayHint", { seconds: Math.max(0, state.remaining) });
  }
  render();
  state.onKey = function (event) {
    if (event.key !== "Escape") return;
    event.preventDefault();
    screenshotCancelDelayedCapture();
  };
  document.addEventListener("keydown", state.onKey);
  state.timer = setInterval(function () {
    if (!screenshotDelayState) return;
    state.remaining -= 1;
    if (state.remaining <= 0) {
      screenshotCancelDelayedCapture();
      openScreenshotCapture({});
      return;
    }
    render();
  }, 1000);
  screenshotDelayState = state;
}

// ── Captura de rolagem ("captura longa") ─────────────────────────────────

var screenshotScrollingPending = false;

function screenshotCaptureScrolling() {
  if (screenshotScrollingPending) return;
  var capture = screenshotApiMethod("CaptureScrollingScreenshot");
  if (!capture) {
    screenshotFeedback(screenshotT("screenshot.scrollingUnavailable"), true);
    return;
  }
  screenshotScrollingPending = true;
  screenshotFeedback(screenshotT("screenshot.scrollingPreparing"), false);
  Promise.resolve()
    .then(function () { return capture("captura longa manual", 10); })
    .then(function (raw) {
      var data = screenshotParse(raw);
      if (!data || !data.imageDataUrl) {
        screenshotFeedback(screenshotT("screenshot.scrollingFailed"), true);
        return;
      }
      // O anexo usa o MESMO caminho do print comum (screenshotAttach).
      screenshotAttach({
        dataUrl: data.imageDataUrl,
        width: data.width || 0,
        height: data.height || 0,
        label: screenshotT("screenshot.scrollingLabel", { scrolls: data.scrolls || 1 }),
      });
      if (data.truncated) {
        screenshotFeedback(screenshotT("screenshot.scrollingTruncated"), true);
      }
    })
    .catch(function (err) {
      screenshotFeedback(screenshotT("screenshot.scrollingFailed") + ": " + String(err), true);
    })
    .then(function () {
      screenshotScrollingPending = false;
    });
}

// ── OCR do print (extrair texto) ─────────────────────────────────────────

// screenshotCopyPlainText copia texto puro (OCR) com retorno ao usuário.
function screenshotCopyPlainText(text) {
  var value = String(text || "");
  if (!value) return Promise.resolve(false);
  return screenshotClipboardWriteText(value).then(function (ok) {
    screenshotFeedback(
      ok ? screenshotT("screenshot.ocrCopied") : screenshotT("screenshot.ocrCopyFailed"),
      !ok,
    );
    return ok;
  });
}

function screenshotOcrLanguage() {
  try {
    var locale = typeof getAppLocale === "function" ? getAppLocale() : "pt-br";
    return locale === "pt-br" ? "pt-BR" : "en-US";
  } catch (_) {
    return "pt-BR";
  }
}

function screenshotEnsureOcrPanel() {
  if (document.getElementById("screenshotOcrPanel")) return;
  var panel = document.createElement("div");
  panel.id = "screenshotOcrPanel";
  panel.className = "screenshot-ocr-panel hidden";
  panel.setAttribute("role", "dialog");
  panel.setAttribute("aria-modal", "false");
  panel.setAttribute("aria-label", "OCR");
  panel.innerHTML = [
    '<div class="screenshot-ocr-head">',
    '  <span id="screenshotOcrCaption" class="screenshot-ocr-caption"></span>',
    '  <button id="screenshotOcrCopy" type="button" class="btn subtle"></button>',
    '  <button id="screenshotOcrClose" type="button" class="btn danger"></button>',
    '</div>',
    '<textarea id="screenshotOcrText" class="screenshot-ocr-text" readonly spellcheck="false"></textarea>',
    '<div id="screenshotOcrStatus" class="screenshot-ocr-status"></div>',
  ].join("");
  document.body.appendChild(panel);
  var close = document.getElementById("screenshotOcrClose");
  if (close) {
    close.textContent = screenshotT("action.close");
    close.addEventListener("click", screenshotCloseOcr);
  }
  var copy = document.getElementById("screenshotOcrCopy");
  if (copy) {
    copy.textContent = screenshotT("screenshot.ocrCopy");
    copy.addEventListener("click", function () {
      var area = document.getElementById("screenshotOcrText");
      if (area) screenshotCopyPlainText(area.value);
    });
  }
  panel.addEventListener("click", function (event) {
    if (event.target === panel) screenshotCloseOcr();
  });
}

function screenshotCloseOcr() {
  var panel = document.getElementById("screenshotOcrPanel");
  if (panel) panel.classList.add("hidden");
}

// screenshotOpenOcr extrai o texto do print pelo binding OcrScreenshotImage e
// mostra num painel com botão de copiar (ou a mensagem de engine indisponível).
function screenshotOpenOcr(dataUrl, caption) {
  if (!dataUrl) return;
  if (screenshotOcrPending) return;
  screenshotEnsureOcrPanel();
  var panel = document.getElementById("screenshotOcrPanel");
  var area = document.getElementById("screenshotOcrText");
  var status = document.getElementById("screenshotOcrStatus");
  var cap = document.getElementById("screenshotOcrCaption");
  if (!panel || !area) return;
  if (cap) cap.textContent = caption ? caption + " · OCR" : "OCR";
  area.value = "";
  if (status) status.textContent = screenshotT("screenshot.ocrRunning");
  panel.classList.remove("hidden");
  var ocrCloseBtn = document.getElementById("screenshotOcrClose");
  if (ocrCloseBtn) ocrCloseBtn.focus();

  var ocr = screenshotApiMethod("OcrScreenshotImage");
  if (!ocr) {
    if (status) status.textContent = screenshotT("screenshot.ocrUnavailable");
    return;
  }
  screenshotOcrPending = true;
  Promise.resolve()
    .then(function () { return ocr(dataUrl, screenshotOcrLanguage()); })
    .then(function (raw) {
      var data = screenshotParse(raw);
      if (!data || data.engineAvailable === false) {
        if (status) {
          status.textContent = screenshotT("screenshot.ocrEngineUnavailable") +
            (data && data.error ? " (" + data.error + ")" : "");
        }
        return;
      }
      var text = String(data.text || "").trim();
      area.value = text;
      if (status) status.textContent = text ? "" : screenshotT("screenshot.ocrEmpty");
    })
    .catch(function (err) {
      if (status) status.textContent = screenshotT("screenshot.ocrFailed", { error: String(err) });
    })
    .then(function () {
      screenshotOcrPending = false;
    });
}

// ── Galeria local de capturas ────────────────────────────────────────────

function screenshotGalleryItems() {
  var raw = screenshotStorageRead(SCREENSHOT_GALLERY_KEY);
  if (!raw) return [];
  try {
    var list = JSON.parse(raw);
    return Array.isArray(list) ? list.filter(function (item) { return item && item.path; }) : [];
  } catch (_) {
    return [];
  }
}

function screenshotGalleryWrite(items) {
  var list = items || [];
  if (screenshotStorageWrite(SCREENSHOT_GALLERY_KEY, JSON.stringify(list))) return true;
  // Cota cheia: a gravação falhava em SILÊNCIO e o print salvo em disco
  // desaparecia da galeria na próxima abertura. A lista é podada (metade por
  // vez, preservando os mais recentes, que ficam no início) até caber; o
  // usuário é avisado de que itens antigos saíram.
  var kept = list.slice();
  while (kept.length > 1) {
    kept = kept.slice(0, Math.max(1, Math.floor(kept.length / 2)));
    if (screenshotStorageWrite(SCREENSHOT_GALLERY_KEY, JSON.stringify(kept))) {
      screenshotFeedback(screenshotT("screenshot.galleryTrimmed", { count: kept.length }), true);
      return true;
    }
  }
  screenshotFeedback(screenshotT("screenshot.galleryStorageFull"), true);
  return false;
}

function screenshotGalleryAdd(saved) {
  if (!saved || !saved.path) return;
  screenshotLoadPolicy(false).then(function (policy) {
    if (!policy.galleryEnabled) return;
    var entry = {
      path: String(saved.path),
      fileName: String(saved.fileName || ""),
      directory: String(saved.directory || ""),
      bytes: Number(saved.bytes) || 0,
      width: Number(saved.width) || 0,
      height: Number(saved.height) || 0,
      sha256: String(saved.sha256 || ""),
      format: String(saved.format || ""),
      at: Date.now(),
    };
    var items = screenshotGalleryItems().filter(function (item) { return item.path !== entry.path; });
    items.unshift(entry);
    // Teto de itens vem da política (galleryMaxItems).
    items = items.slice(0, Math.max(1, policy.galleryMaxItems));
    screenshotGalleryWrite(items);
    var panel = document.getElementById("screenshotGalleryPanel");
    if (panel && !panel.classList.contains("hidden")) screenshotRenderGallery();
  });
}

function screenshotGalleryRemove(path) {
  var items = screenshotGalleryItems().filter(function (item) { return item.path !== path; });
  screenshotGalleryWrite(items);
}

function screenshotGalleryFileName(item) {
  if (item && item.fileName) return item.fileName;
  var path = String(item && item.path ? item.path : "");
  var parts = path.split(/[\\/]/);
  return parts[parts.length - 1] || path;
}

function screenshotFormatBytes(bytes) {
  var value = Number(bytes) || 0;
  if (value >= 1048576) return (value / 1048576).toFixed(1) + " MB";
  if (value >= 1024) return (value / 1024).toFixed(0) + " KB";
  return value + " B";
}

function screenshotGalleryDate(at) {
  try {
    return at ? new Date(at).toLocaleString() : "";
  } catch (_) {
    return "";
  }
}

// screenshotEnsureGalleryPanel monta o painel "Últimas capturas".
function screenshotEnsureGalleryPanel() {
  if (document.getElementById("screenshotGalleryPanel")) return;
  var panel = document.createElement("div");
  panel.id = "screenshotGalleryPanel";
  panel.className = "screenshot-gallery-panel hidden";
  panel.setAttribute("role", "dialog");
  panel.setAttribute("aria-modal", "false");
  panel.innerHTML = [
    '<div class="screenshot-gallery-head">',
    '  <span class="screenshot-gallery-title" id="screenshotGalleryTitle"></span>',
    '  <span class="screenshot-gallery-head-actions">',
    '    <button id="screenshotGalleryFolder" type="button" class="btn subtle"></button>',
    '    <button id="screenshotGalleryClose" type="button" class="btn danger"></button>',
    '  </span>',
    '</div>',
    '<div id="screenshotGalleryList" class="screenshot-gallery-list"></div>',
    '<div id="screenshotGalleryStatus" class="screenshot-gallery-status" role="status"></div>',
  ].join("");
  document.body.appendChild(panel);
  panel.setAttribute("aria-labelledby", "screenshotGalleryTitle");
  var title = document.getElementById("screenshotGalleryTitle");
  if (title) title.textContent = screenshotT("screenshot.galleryTitle");
  var close = document.getElementById("screenshotGalleryClose");
  if (close) {
    close.textContent = screenshotT("action.close");
    close.addEventListener("click", screenshotCloseGallery);
  }
  var folder = document.getElementById("screenshotGalleryFolder");
  if (folder) {
    folder.textContent = screenshotT("screenshot.galleryFolder");
    folder.addEventListener("click", function () {
      var getFolder = screenshotApiMethod("GetScreenshotGalleryFolder");
      if (!getFolder) {
        screenshotFeedback(screenshotT("screenshot.unavailable"), true);
        return;
      }
      Promise.resolve().then(function () { return getFolder(); }).then(function (path) {
        screenshotCopyPathToClipboard(path || "");
      }).catch(function () { screenshotFeedback(screenshotT("screenshot.unavailable"), true); });
    });
  }
  panel.addEventListener("click", function (event) {
    if (event.target === panel) screenshotCloseGallery();
  });
}

function screenshotCloseGallery() {
  var panel = document.getElementById("screenshotGalleryPanel");
  if (panel) panel.classList.add("hidden");
}

function screenshotOpenGallery() {
  screenshotLoadPolicy(false).then(function (policy) {
    if (!policy.galleryEnabled) {
      screenshotFeedback(screenshotT("screenshot.galleryDisabled"), true);
      return;
    }
    screenshotEnsureGalleryPanel();
    var panel = document.getElementById("screenshotGalleryPanel");
    if (!panel) return;
    panel.classList.remove("hidden");
    screenshotRenderGallery();
    var focusTarget = document.getElementById("screenshotGalleryClose");
    if (focusTarget) focusTarget.focus();
  });
}

function screenshotGalleryStatus(message, isError) {
  var status = document.getElementById("screenshotGalleryStatus");
  if (!status) return;
  status.textContent = message || "";
  status.classList.toggle("error", !!isError);
}

// screenshotGalleryActionButton cria um botão de ação de um item da galeria.
function screenshotGalleryActionButton(labelKey, onClick, disabled) {
  var btn = document.createElement("button");
  btn.type = "button";
  btn.className = "screenshot-gallery-action";
  btn.textContent = screenshotT(labelKey);
  btn.title = screenshotT(labelKey);
  btn.disabled = !!disabled;
  if (!disabled) btn.addEventListener("click", onClick);
  return btn;
}

// screenshotGalleryReadImage lê o arquivo da captura como data URL (thumbnail
// ou imagem cheia), degradando com mensagem quando o binding não existe.
function screenshotGalleryReadImage(path, maxBytes) {
  var read = screenshotApiMethod("ReadScreenshotImageFile");
  if (!read) return Promise.resolve("");
  return Promise.resolve()
    .then(function () { return read(path, maxBytes || SCREENSHOT_THUMB_MAX_BYTES); })
    .then(function (raw) {
      // O binding devolve a própria data URL (string), não JSON.
      if (typeof raw === "string" && raw.indexOf("data:") === 0) return raw;
      var value = screenshotParse(raw);
      if (typeof value === "string") return value;
      if (value && value.dataUrl) return value.dataUrl;
      return "";
    })
    .catch(function () { return ""; });
}

function screenshotRenderGallery() {
  var list = document.getElementById("screenshotGalleryList");
  if (!list) return;
  list.innerHTML = "";
  var policy = screenshotPolicySync();
  var maxItems = policy.galleryMaxItems || 50;
  var allItems = screenshotGalleryItems();
  // Teto da política vale também para o que já estava salvo (ex.: teto reduzido
  // depois): poda o excedente ao abrir a galeria.
  if (allItems.length > maxItems) {
    allItems = allItems.slice(0, maxItems);
    screenshotGalleryWrite(allItems);
  }
  var items = allItems.slice(0, SCREENSHOT_GALLERY_RENDER_MAX);
  var empty = document.getElementById("screenshotGalleryEmpty");
  if (!items.length) {
    if (!empty) {
      empty = document.createElement("div");
      empty.id = "screenshotGalleryEmpty";
      empty.className = "screenshot-gallery-empty";
      empty.textContent = screenshotT("screenshot.galleryEmpty");
    }
    list.appendChild(empty);
    screenshotGalleryStatus(screenshotT("screenshot.galleryLimit", { max: maxItems }), false);
    return;
  }
  if (allItems.length > items.length) {
    // O teto de RENDERIZAÇÃO é menor que o teto da política: sem avisar, itens
    // 25+ pareciam inexistentes.
    screenshotGalleryStatus(
      screenshotT("screenshot.galleryShowing", { shown: items.length, total: allItems.length }),
      false);
  }

  items.forEach(function (item) {
    var row = document.createElement("div");
    row.className = "screenshot-gallery-item";

    var thumbBtn = document.createElement("button");
    thumbBtn.type = "button";
    thumbBtn.className = "screenshot-gallery-thumb";
    thumbBtn.title = screenshotT("screenshot.viewLarger");
    thumbBtn.setAttribute("aria-label", screenshotT("screenshot.viewLarger"));
    var ph = document.createElement("span");
    ph.className = "screenshot-gallery-thumb-placeholder";
    ph.textContent = "🖼";
    thumbBtn.appendChild(ph);
    thumbBtn.addEventListener("click", function () {
      screenshotGalleryReadImage(item.path, 8 << 20).then(function (dataUrl) {
        if (dataUrl) screenshotOpenLightbox(dataUrl, screenshotGalleryFileName(item));
      });
    });
    row.appendChild(thumbBtn);

    var info = document.createElement("div");
    info.className = "screenshot-gallery-info";
    var name = document.createElement("div");
    name.className = "screenshot-gallery-name";
    name.textContent = screenshotGalleryFileName(item);
    name.title = item.path;
    var meta = document.createElement("div");
    meta.className = "screenshot-gallery-meta";
    var metaParts = [];
    if (item.width && item.height) metaParts.push(item.width + "×" + item.height);
    if (item.bytes) metaParts.push(screenshotFormatBytes(item.bytes));
    metaParts.push(screenshotGalleryDate(item.at));
    meta.textContent = metaParts.filter(Boolean).join(" · ");
    info.appendChild(name);
    info.appendChild(meta);

    var actions = document.createElement("div");
    actions.className = "screenshot-gallery-actions";
    actions.appendChild(screenshotGalleryActionButton("screenshot.galleryReattach", function () {
      screenshotGalleryReadImage(item.path, 8 << 20).then(function (dataUrl) {
        if (!dataUrl) {
          screenshotGalleryStatus(screenshotT("screenshot.galleryReadFailed"), true);
          return;
        }
        screenshotAttach({ dataUrl: dataUrl, width: item.width || 0, height: item.height || 0 });
        screenshotCloseGallery();
      });
    }));
    actions.appendChild(screenshotGalleryActionButton("screenshot.copyPath", function () {
      screenshotCopyPathToClipboard(item.path);
    }));
    actions.appendChild(screenshotGalleryActionButton("screenshot.gallerySaveAs", function () {
      screenshotGalleryReadImage(item.path, 8 << 20).then(function (dataUrl) {
        if (!dataUrl) {
          screenshotGalleryStatus(screenshotT("screenshot.galleryReadFailed"), true);
          return;
        }
        screenshotSaveImageDataUrl(dataUrl, {
          fileName: screenshotGalleryFileName(item),
          saveAs: true,
          directory: "",
        });
      });
    }));
    actions.appendChild(screenshotGalleryActionButton("screenshot.galleryDelete", function () {
      var remove = screenshotApiMethod("DeleteScreenshotImageFile");
      if (!remove) {
        screenshotGalleryStatus(screenshotT("screenshot.unavailable"), true);
        return;
      }
      if (typeof window.confirm === "function" && !window.confirm(screenshotT("screenshot.galleryDeleteConfirm"))) {
        return;
      }
      Promise.resolve()
        .then(function () { return remove(item.path); })
        .then(function () {
          screenshotGalleryRemove(item.path);
          screenshotRenderGallery();
          screenshotGalleryStatus(screenshotT("screenshot.galleryDeleted"), false);
        })
        .catch(function (err) {
          screenshotGalleryStatus(screenshotT("screenshot.galleryDeleteFailed", { error: String(err) }), true);
        });
    }));

    info.appendChild(actions);
    row.appendChild(info);
    list.appendChild(row);

    // Miniatura carregada sob demanda (o arquivo pode ter sido apagado fora do
    // agent: nesse caso o placeholder permanece e as ações seguem disponíveis).
    screenshotGalleryReadImage(item.path, SCREENSHOT_THUMB_MAX_BYTES).then(function (dataUrl) {
      if (!dataUrl) return;
      var img = document.createElement("img");
      img.src = dataUrl;
      img.alt = screenshotGalleryFileName(item);
      thumbBtn.innerHTML = "";
      thumbBtn.appendChild(img);
    });
  });
  screenshotGalleryStatus(screenshotT("screenshot.galleryLimit", { max: maxItems }), false);
}

// ── data URL → File (usado pelo fluxo de anexos do chamado) ──────────────

// screenshotDataUrlToFile converte um PNG dataURL em File para reaproveitar os
// mesmos handlers do input de arquivos (validação de tipo/tamanho e prévia).
function screenshotDataUrlToFile(dataUrl, fileName) {
  var raw = String(dataUrl || "");
  var match = /^data:([^;,]*)(;base64)?,([\s\S]*)$/.exec(raw);
  if (!match) return null;
  var mime = match[1] || "image/png";
  var isBase64 = !!match[2];
  var payload = match[3] || "";
  var binary = "";
  try {
    binary = isBase64 ? window.atob(payload) : decodeURIComponent(payload);
  } catch (_) {
    return null;
  }
  var bytes = new Uint8Array(binary.length);
  for (var i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  var name = fileName || screenshotDefaultFileName();
  var extension = mime.indexOf("webp") >= 0 ? "webp" : (mime.indexOf("jpeg") >= 0 ? "jpg" : "png");
  if (!/\./.test(name)) name += "." + extension;
  try {
    return new File([bytes], name, { type: mime });
  } catch (_) {
    // Builds sem o construtor File: devolve o Blob (FileReader aceita ambos).
    try {
      return new Blob([bytes], { type: mime });
    } catch (_) {
      return null;
    }
  }
}


// ══════════════════════════════════════════════════════════════════════════
// WP2 — UX da captura de tela
//
// Salvar em arquivo + copiar caminho, atalhos de teclado, copiar ao confirmar,
// galeria local, captura com atraso/rolagem, OCR, modo evidência e seletor de
// janelas. Tudo defensivo: bindings novos podem não existir em builds antigos
// e, nesse caso, a ação degrada com aviso em vez de quebrar a captura.
// ══════════════════════════════════════════════════════════════════════════

var SCREENSHOT_GALLERY_KEY = "dsv.screenshot.gallery";
var SCREENSHOT_LAST_DIR_KEY = "dsv.screenshot.lastDir";
var SCREENSHOT_POLICY_TTL_MS = 60000;
var SCREENSHOT_GALLERY_RENDER_MAX = 24;
var SCREENSHOT_THUMB_MAX_BYTES = 3 * 1024 * 1024;

var screenshotPolicyCache = { at: 0, data: null };
var screenshotPolicyPending = null;
var screenshotLastSavedPath = "";
var screenshotOcrPending = false;

// screenshotApiMethod devolve a função do binding (ou null). Centraliza o
// "typeof api.X === 'function'" para todo o módulo.
function screenshotApiMethod(name) {
  var api = screenshotApi();
  if (!api || !name) return null;
  var fn = api[name];
  return typeof fn === "function" ? fn : null;
}

function screenshotStorageRead(key) {
  try {
    return window.localStorage ? window.localStorage.getItem(key) : null;
  } catch (_) {
    return null;
  }
}

// Devolve true quando o valor foi realmente gravado: cota cheia/modo privado
// não podem ser confundidos com sucesso (era o que fazia a galeria perder
// capturas sem nenhum aviso).
function screenshotStorageWrite(key, value) {
  try {
    if (!window.localStorage) return false;
    if (value === null || value === undefined) {
      window.localStorage.removeItem(key);
    } else {
      window.localStorage.setItem(key, value);
    }
    return true;
  } catch (_) {
    return false;
  }
}

// ── Política (cache com TTL) ─────────────────────────────────────────────

// screenshotPolicyNormalize garante os campos novos mesmo em builds antigos.
function screenshotPolicyNormalize(raw) {
  var policy = raw && typeof raw === "object" ? raw : {};
  var maxItems = Number(policy.galleryMaxItems);
  if (!(maxItems > 0)) maxItems = 50;
  return {
    copyOnConfirm: policy.copyOnConfirm === true,
    galleryEnabled: policy.galleryEnabled !== false,
    galleryMaxItems: maxItems,
    // "loaded" separa "política real do backend" de "fallback sem binding": no
    // fallback não dá para saber os valores e Salvar política NÃO pode inventá-los.
    loaded: Object.keys(policy).length > 0,
    raw: policy,
  };
}

// screenshotLoadPolicy busca GetScreenshotPolicy com cache curto (a captura
// não pode esperar o disco a cada confirmação, mas também não pode ficar presa
// a uma política alterada na tela de privacidade).
function screenshotLoadPolicy(force) {
  var cached = screenshotPolicyCache.data;
  if (!force && cached && Date.now() - screenshotPolicyCache.at < SCREENSHOT_POLICY_TTL_MS) {
    return Promise.resolve(cached);
  }
  if (screenshotPolicyPending) return screenshotPolicyPending;
  var getPolicy = screenshotApiMethod("GetScreenshotPolicy");
  if (!getPolicy) {
    var fallback = screenshotPolicyNormalize(null);
    screenshotPolicyCache = { at: Date.now(), data: fallback };
    return Promise.resolve(fallback);
  }
  screenshotPolicyPending = Promise.resolve()
    .then(function () { return getPolicy(); })
    .then(function (raw) {
      var data = screenshotPolicyNormalize(screenshotParse(raw));
      screenshotPolicyCache = { at: Date.now(), data: data };
      return data;
    })
    .catch(function () {
      // Falha de leitura não invalida o cache anterior; sem cache, padrões.
      var data = screenshotPolicyCache.data || screenshotPolicyNormalize(null);
      screenshotPolicyCache = { at: Date.now(), data: data };
      return data;
    })
    .then(function (data) {
      screenshotPolicyPending = null;
      return data;
    });
  return screenshotPolicyPending;
}

function screenshotPolicySync() {
  return screenshotPolicyCache.data || screenshotPolicyNormalize(null);
}

// screenshotInvalidatePolicyCache é chamado depois de salvar a política na
// tela de privacidade para a próxima captura já usar os valores novos.
function screenshotInvalidatePolicyCache() {
  screenshotPolicyCache = { at: 0, data: null };
  screenshotPolicyPending = null;
}

// ── Notificação dentro do overlay ────────────────────────────────────────
//
// showFeedback na janela do overlay só escreve no console (não há barra de
// status lá). Este aviso flutuante garante retorno visível para salvar/copiar.
function screenshotOverlayFlash(message, isError) {
  screenshotFeedback(message, isError);
  if (!screenshotOverlayHost && !screenshotOverlayRoot()) return;
  var el = document.getElementById("screenshotOverlayFlash");
  if (!el) {
    el = document.createElement("div");
    el.id = "screenshotOverlayFlash";
    el.className = "screenshot-overlay-flash";
    el.setAttribute("role", "status");
    el.setAttribute("aria-live", "polite");
    document.body.appendChild(el);
  }
  el.textContent = String(message || "");
  el.classList.toggle("error", !!isError);
  el.classList.add("visible");
  clearTimeout(screenshotOverlayFlash._timer);
  screenshotOverlayFlash._timer = setTimeout(function () {
    el.classList.remove("visible");
  }, 3200);
}

// ── Salvar em arquivo + copiar caminho ──────────────────────────────────

function screenshotLastDirectory() {
  return screenshotStorageRead(SCREENSHOT_LAST_DIR_KEY) || "";
}

function screenshotRememberDirectory(directory) {
  if (directory) screenshotStorageWrite(SCREENSHOT_LAST_DIR_KEY, String(directory));
}

// screenshotDefaultFileName devolve o nome/prefixo padrão do print. O Go
// acrescenta timestamp e sufixo ao salvar; em anexos, a extensão é completada
// pelo MIME quando o nome não tem uma.
function screenshotDefaultFileName() {
  return "print";
}

// screenshotClipboardFallback copia via seleção de um textarea oculto (WebView
// sem a Clipboard API).
function screenshotClipboardFallback(text) {
  try {
    var area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "readonly");
    area.style.position = "fixed";
    area.style.left = "-9999px";
    document.body.appendChild(area);
    area.select();
    var ok = document.execCommand && document.execCommand("copy");
    document.body.removeChild(area);
    return !!ok;
  } catch (_) {
    return false;
  }
}

// screenshotClipboardWriteText escreve texto na área de transferência com o
// fallback — usado tanto pelo caminho do arquivo quanto pelo texto do OCR.
function screenshotClipboardWriteText(text) {
  var clipboard = typeof navigator !== "undefined" ? navigator.clipboard : null;
  if (clipboard && typeof clipboard.writeText === "function") {
    return clipboard.writeText(text).then(function () { return true; }).catch(function () {
      return screenshotClipboardFallback(text);
    });
  }
  return Promise.resolve(screenshotClipboardFallback(text));
}

// screenshotCopyPathToClipboard copia o caminho do print salvo.
function screenshotCopyPathToClipboard(path) {
  var text = String(path || "");
  if (!text) {
    screenshotOverlayFlash(screenshotT("screenshot.copyPathEmpty"), true);
    return Promise.resolve(false);
  }
  return screenshotClipboardWriteText(text).then(function (ok) {
    screenshotOverlayFlash(
      ok ? screenshotT("screenshot.pathCopied", { path: text }) : screenshotT("screenshot.copyPathFailed"),
      !ok,
    );
    return ok;
  });
}

// screenshotSaveImageDataUrl salva um print em arquivo pelo binding
// SaveScreenshotImage e registra o resultado na galeria local.
function screenshotSaveImageDataUrl(dataUrl, options) {
  var opts = options || {};
  var save = screenshotApiMethod("SaveScreenshotImage");
  if (!save) {
    screenshotOverlayFlash(screenshotT("screenshot.saveUnavailable"), true);
    return Promise.resolve(null);
  }
  if (!dataUrl) {
    screenshotOverlayFlash(screenshotT("screenshot.saveFileFailed", { error: "imagem vazia" }), true);
    return Promise.resolve(null);
  }
  // Contrato de SaveScreenshotImage: {format, directory, prefix}. O nome final é
  // gerado pelo Go (prefixo + timestamp + sufixo), então o "fileName" da UI vira
  // apenas o prefixo — e o diretório vazio cai na pasta padrão da galeria.
  var requestedName = String(opts.fileName || screenshotDefaultFileName());
  var payload = {
    prefix: requestedName.replace(/\.[^.]+$/, ""),
    directory: opts.directory === undefined ? screenshotLastDirectory() : String(opts.directory || ""),
  };
  return Promise.resolve()
    .then(function () { return save(dataUrl, JSON.stringify(payload)); })
    .then(function (raw) {
      var saved = screenshotParse(raw);
      if (!saved || !saved.path) {
        screenshotOverlayFlash(screenshotT("screenshot.saveFileFailed", { error: "resposta vazia" }), true);
        return null;
      }
      screenshotLastSavedPath = String(saved.path);
      screenshotRememberDirectory(saved.directory || "");
      screenshotGalleryAdd(saved);
      screenshotOverlayFlash(screenshotT("screenshot.savedTo", { path: saved.path }), false);
      screenshotUpdateCopyPathButton();
      return saved;
    })
    .catch(function (err) {
      screenshotOverlayFlash(screenshotT("screenshot.saveFileFailed", { error: String(err) }), true);
      return null;
    });
}

// screenshotOutputDataUrl compõe a imagem que seria confirmada (com anotações,
// quando houver). Usada por salvar/copiar/OCR dentro do overlay.
function screenshotOutputDataUrl() {
  var state = screenshotOverlayState;
  if (!state || !state.selection) return "";
  if ((state.annotations && state.annotations.length > 0) || (state.evidence && state.evidence.enabled)) {
    try {
      return screenshotBuildAnnotatedDataUrl();
    } catch (_) {
      return "";
    }
  }
  return screenshotSelectionSourceDataUrl();
}

// screenshotSelectionSourceDataUrl recorta a tela congelada SEM anotações —
// base do hash de evidência e do OCR quando não há composição anotada.
function screenshotSelectionSourceDataUrl() {
  var img = screenshotOverlayImg();
  var clip = screenshotSelectionImageRect();
  if (!img || !clip) return "";
  try {
    var canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(clip.w));
    canvas.height = Math.max(1, Math.round(clip.h));
    var ctx = canvas.getContext("2d");
    ctx.drawImage(img, clip.x, clip.y, clip.w, clip.h, 0, 0, canvas.width, canvas.height);
    return canvas.toDataURL("image/png");
  } catch (_) {
    return "";
  }
}

function screenshotUpdateCopyPathButton() {
  var btn = document.getElementById("screenshotAnnotCopyPath");
  if (!btn) return;
  var hasPath = !!screenshotLastSavedPath;
  btn.classList.toggle("has-path", hasPath);
  btn.disabled = !hasPath;
  btn.setAttribute("aria-disabled", hasPath ? "false" : "true");
}

function screenshotOverlaySave() {
  if (!screenshotOverlayState || !screenshotOverlayState.selection) return;
  var dataUrl = screenshotOutputDataUrl();
  if (!dataUrl) {
    screenshotOverlayFlash(screenshotT("screenshot.saveFileFailed", { error: "sem imagem" }), true);
    return;
  }
  screenshotSaveImageDataUrl(dataUrl, {});
}

function screenshotOverlayCopyPath() {
  if (!screenshotLastSavedPath) {
    screenshotOverlayFlash(screenshotT("screenshot.copyPathEmpty"), true);
    return;
  }
  screenshotCopyPathToClipboard(screenshotLastSavedPath);
}

// ── Copiar ao confirmar (policy.copyOnConfirm) ───────────────────────────

function screenshotCopyOnConfirm(dataUrl) {
  if (!dataUrl) return;
  screenshotLoadPolicy(false).then(function (policy) {
    if (!policy || !policy.copyOnConfirm) return;
    var copy = screenshotApiMethod("CopyImageDataURLToClipboard");
    if (!copy) return;
    Promise.resolve()
      .then(function () { return copy(dataUrl); })
      .then(function () {
        screenshotOverlayFlash(screenshotT("screenshot.copied"), false);
      })
      .catch(function () {
        // Falha ao copiar não invalida a captura (o print já foi confirmado).
      });
  });
}

// ── Modo evidência (watermark + bloco na bolha de info) ──────────────────

function screenshotEvidenceFallbackDate() {
  try {
    return new Date().toISOString();
  } catch (_) {
    return "";
  }
}

// screenshotResolveHostname tenta o status do agent (fonte do hostname na UI)
// e cai para o payload, se algum build já o trouxer.
function screenshotResolveHostname(callback) {
  var payload = screenshotOverlayState && screenshotOverlayState.payload;
  var fromPayload = payload && (payload.hostname || payload.machineName || payload.computerName);
  if (fromPayload) {
    callback(String(fromPayload));
    return;
  }
  if (screenshotHostnameCache) {
    callback(screenshotHostnameCache);
    return;
  }
  var getStatus = screenshotApiMethod("GetStatusOverview");
  if (!getStatus) {
    callback("");
    return;
  }
  Promise.resolve()
    .then(function () { return getStatus(); })
    .then(function (status) {
      var host = status && (status.hostname || status.computerName || status.machineName);
      screenshotHostnameCache = host ? String(host) : "";
      callback(screenshotHostnameCache);
    })
    .catch(function () { callback(""); });
}

var screenshotHostnameCache = "";

// screenshotPrimeEvidence preenche o bloco de evidência: SHA-256 vindo de
// GetScreenshotEvidence (8 primeiros caracteres exibidos) + data/hora UTC +
// hostname. Sem o binding, o watermark ainda sai com data/hora e aviso.
function screenshotPrimeEvidence(callback) {
  var source = screenshotSelectionSourceDataUrl();
  var capturedAt = screenshotEvidenceFallbackDate();
  function finish(sha, bytes, width, height, atUtc, unavailable) {
    screenshotResolveHostname(function (hostname) {
      callback({
        sha256: sha || "",
        shortSha: (sha || "").slice(0, 8),
        capturedAtUtc: atUtc || capturedAt,
        bytes: bytes || 0,
        width: width || 0,
        height: height || 0,
        hostname: hostname || "",
        unavailable: !!unavailable,
      });
    });
  }
  var evidence = screenshotApiMethod("GetScreenshotEvidence");
  if (!evidence || !source) {
    finish("", 0, 0, 0, capturedAt, true);
    return;
  }
  Promise.resolve()
    .then(function () { return evidence(source); })
    .then(function (raw) {
      var info = screenshotParse(raw);
      if (!info || !info.sha256) {
        finish("", 0, 0, 0, capturedAt, true);
        return;
      }
      finish(info.sha256, info.bytes, info.width, info.height, info.capturedAtUtc, false);
    })
    .catch(function () {
      finish("", 0, 0, 0, capturedAt, true);
    });
}

function screenshotSetEvidenceButton(active) {
  var btn = document.getElementById("screenshotAnnotEvidence");
  if (!btn) return;
  btn.classList.toggle("active", !!active);
  btn.setAttribute("aria-pressed", active ? "true" : "false");
}

function screenshotToggleEvidence() {
  var state = screenshotOverlayState;
  if (!state) return;
  if (state.evidence && state.evidence.enabled) {
    state.evidence = null;
    screenshotSetEvidenceButton(false);
    screenshotAnnotRedraw();
    screenshotOverlayFlash(screenshotT("screenshot.evidenceOff"), false);
    return;
  }
  screenshotOverlayFlash(screenshotT("screenshot.evidencePreparing"), false);
  // A evidência (binding + hostname) é assíncrona: guarda a sessão que a pediu
  // para um resultado atrasado não ser aplicado na captura SEGUINTE — antes o
  // print novo saía com data/hash de outra tela.
  var session = state.payload ? state.payload.session : "";
  screenshotPrimeEvidence(function (info) {
    var current = screenshotOverlayState;
    if (!current || !current.payload || current.payload.session !== session) return;
    info.enabled = true;
    current.evidence = info;
    screenshotSetEvidenceButton(true);
    screenshotAnnotRedraw();
    if (info.unavailable) {
      screenshotOverlayFlash(screenshotT("screenshot.evidenceNoHash"), true);
    } else {
      screenshotOverlayFlash(screenshotT("screenshot.evidenceOn", { sha: info.shortSha }), false);
    }
  });
}

// screenshotEvidenceSummary devolve o texto multilinha usado no bloco da
// bolha de info (mesmos campos do watermark).
function screenshotEvidenceSummary(evidence) {
  if (!evidence) return "";
  var when = String(evidence.capturedAtUtc || "");
  try {
    if (when) when = new Date(when).toISOString().replace("T", " ").replace(".000Z", " UTC");
  } catch (_) {}
  var lines = [];
  lines.push(screenshotT("screenshot.evidenceWhen") + ": " + (when || "-"));
  lines.push(screenshotT("screenshot.evidenceHost") + ": " + (evidence.hostname || "-"));
  lines.push(
    screenshotT("screenshot.evidenceSha") + ": " +
    (evidence.shortSha ? evidence.shortSha + "…" : screenshotT("screenshot.evidenceShaUnavailable")),
  );
  if (evidence.width && evidence.height) {
    lines.push(screenshotT("screenshot.evidenceSize") + ": " + evidence.width + "×" + evidence.height);
  }
  return lines.join("\n");
}

// screenshotDrawEvidenceWatermark desenha a faixa de evidência no canto
// inferior da imagem final (data/hora UTC, hostname e SHA-256 curto).
function screenshotDrawEvidenceWatermark(ctx, outW, outH, evidence) {
  if (!ctx || !evidence || !evidence.enabled) return;
  var scale = Math.max(0.6, Math.min(2, outW / 1200));
  var fontSize = Math.round(13 * scale);
  var pad = Math.round(8 * scale);
  var lines = [
    screenshotT("screenshot.evidenceBadge"),
    screenshotT("screenshot.evidenceWhen") + ": " + (evidence.capturedAtUtc || "-"),
    screenshotT("screenshot.evidenceHost") + ": " + (evidence.hostname || "-") +
      "  ·  " + screenshotT("screenshot.evidenceSha") + ": " + (evidence.shortSha || "-"),
  ];
  ctx.save();
  ctx.font = "600 " + fontSize + "px sans-serif";
  var maxWidth = 0;
  lines.forEach(function (line) {
    maxWidth = Math.max(maxWidth, ctx.measureText(line).width);
  });
  var boxW = Math.min(outW - pad * 2, maxWidth + pad * 2);
  var boxH = lines.length * (fontSize + Math.round(4 * scale)) + pad * 2;
  var x = pad;
  var y = outH - boxH - pad;
  ctx.fillStyle = "rgba(0, 0, 0, 0.72)";
  ctx.fillRect(x, y, boxW, boxH);
  ctx.strokeStyle = "rgba(255, 255, 255, 0.45)";
  ctx.lineWidth = Math.max(1, scale);
  ctx.strokeRect(x, y, boxW, boxH);
  ctx.fillStyle = "#ffffff";
  ctx.textBaseline = "top";
  lines.forEach(function (line, idx) {
    ctx.fillText(line, x + pad, y + pad + idx * (fontSize + Math.round(4 * scale)));
  });
  ctx.restore();
}

// ── Atalhos de teclado do overlay ────────────────────────────────────────

var SCREENSHOT_TOOL_KEYS = {
  r: "rect",
  s: "spotlight",
  a: "arrow",
  p: "pen",
  t: "text",
  b: "blur",
  e: "eraser",
  n: "step",
};

var SCREENSHOT_COLOR_KEYS = ["#ff3b30", "#ffcc00", "#34c759", "#0a84ff"];

function screenshotIsEditableTarget(target) {
  if (!target) return false;
  var tag = target.tagName ? String(target.tagName).toUpperCase() : "";
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  return target.isContentEditable === true;
}

function screenshotOverlayCopyImage() {
  var dataUrl = screenshotOutputDataUrl();
  if (!dataUrl) {
    screenshotOverlayFlash(screenshotT("screenshot.copyFailed"), true);
    return;
  }
  var copy = screenshotApiMethod("CopyImageDataURLToClipboard");
  if (!copy) {
    screenshotOverlayFlash(screenshotT("screenshot.unavailable"), true);
    return;
  }
  Promise.resolve()
    .then(function () { return copy(dataUrl); })
    .then(function () {
      screenshotOverlayFlash(screenshotT("screenshot.copied"), false);
    })
    .catch(function (err) {
      screenshotOverlayFlash(screenshotT("screenshot.copyFailed") + ": " + String(err), true);
    });
}

// screenshotOverlayKeydown concentra os atalhos do overlay. Ignora tudo quando
// o foco está num campo de texto (anotação/busca) para não roubar a digitação.
function screenshotOverlayKeydown(event) {
  if (!event) return;
  // Durante o "preparando" ainda não há estado, mas a janela já cobre o
  // desktop: Esc/clique no X precisa poder cancelar a captura.
  if (!screenshotOverlayState && !screenshotCaptureToken) return;
  if (screenshotIsEditableTarget(event.target)) return;

  var key = String(event.key || "");
  var lower = key.toLowerCase();
  var mod = event.ctrlKey || event.metaKey;

  // Seletor de janelas aberto: Esc fecha o seletor (sem cancelar a captura) e
  // Enter escolhe a primeira janela da lista filtrada.
  if (screenshotWindowPickerOpen()) {
    if (key === "Escape") {
      event.preventDefault();
      screenshotToggleWindowPicker(false);
      return;
    }
    if (key === "Enter") {
      event.preventDefault();
      screenshotWindowPickerChooseFirst();
      return;
    }
    if (lower === "w") {
      event.preventDefault();
      screenshotToggleWindowPicker(false);
      return;
    }
  }

  if (key === "Escape") {
    event.preventDefault();
    screenshotCancelOverlay();
    return;
  }
  if (mod && lower === "z") {
    event.preventDefault();
    screenshotAnnotUndo();
    return;
  }
  if (mod && lower === "c") {
    event.preventDefault();
    screenshotOverlayCopyImage();
    return;
  }
  if (mod && lower === "s") {
    event.preventDefault();
    screenshotOverlaySave();
    return;
  }
  if (mod || event.altKey) return;

  var state = screenshotOverlayState;
  // Sem seleção travada, W ainda abre o seletor de janelas (escolher já trava).
  if (lower === "w") {
    event.preventDefault();
    screenshotToggleWindowPicker();
    return;
  }
  // screenshotOverlayState pode ser nulo no "preparando" (só o token existe).
  if (key === "Enter" && state && state.selection) {
    event.preventDefault();
    screenshotConfirmSelection();
    return;
  }
  if (state && state.selection && SCREENSHOT_TOOL_KEYS[lower]) {
    event.preventDefault();
    screenshotSetAnnotTool(SCREENSHOT_TOOL_KEYS[lower]);
    return;
  }
  if (key >= "1" && key <= "4") {
    event.preventDefault();
    screenshotSetAnnotColor(SCREENSHOT_COLOR_KEYS[Number(key) - 1]);
    return;
  }
  // Zoom/pan por teclado (mesmos passos da barra de zoom).
  if (key === "+" || key === "=") {
    event.preventDefault();
    screenshotSetZoom(screenshotOverlayView.zoom * screenshotZoomStep);
    return;
  }
  if (key === "-" || key === "_") {
    event.preventDefault();
    screenshotSetZoom(screenshotOverlayView.zoom / screenshotZoomStep);
    return;
  }
  if (key === "0") {
    event.preventDefault();
    screenshotResetOverlayView();
    return;
  }
  if (key === "ArrowLeft") { event.preventDefault(); screenshotPanBy(-80, 0); return; }
  if (key === "ArrowRight") { event.preventDefault(); screenshotPanBy(80, 0); return; }
  if (key === "ArrowUp") { event.preventDefault(); screenshotPanBy(0, -80); return; }
  if (key === "ArrowDown") { event.preventDefault(); screenshotPanBy(0, 80); }
}

// ── Seletor de janelas (tecla W) ─────────────────────────────────────────

function screenshotWindowPickerOpen() {
  var picker = document.getElementById("screenshotWindowPicker");
  return !!picker && !picker.classList.contains("hidden");
}

// screenshotSelectableWindows lista as janelas do payload que podem ser
// capturadas (mesmo filtro do destaque/da janela em foco).
function screenshotSelectableWindows() {
  var state = screenshotOverlayState;
  var list = state && state.payload && state.payload.windows ? state.payload.windows : [];
  return list.filter(function (w) {
    return w && !w.isSelf && !w.blocked && !w.minimized && w.width > 0 && w.height > 0;
  });
}

function screenshotWindowLabel(win) {
  var title = String((win && (win.title || win.processName)) || "").trim();
  if (!title) title = screenshotT("screenshot.windowUntitled");
  var process = win && win.processName ? String(win.processName) : "";
  return process && title.indexOf(process) === -1 ? title + " — " + process : title;
}

function screenshotRenderWindowList(filter) {
  var list = document.getElementById("screenshotWindowList");
  if (!list) return;
  var query = String(filter || "").trim().toLowerCase();
  var windows = screenshotSelectableWindows();
  if (query) {
    windows = windows.filter(function (w) {
      return screenshotWindowLabel(w).toLowerCase().indexOf(query) >= 0;
    });
  }
  list.innerHTML = "";
  if (!windows.length) {
    var empty = document.createElement("div");
    empty.className = "screenshot-window-empty";
    empty.textContent = screenshotT("screenshot.windowSearchEmpty");
    list.appendChild(empty);
    return;
  }
  windows.forEach(function (win) {
    var item = document.createElement("button");
    item.type = "button";
    item.className = "screenshot-window-item";
    item.setAttribute("role", "option");
    var geometry = document.createElement("span");
    geometry.className = "screenshot-window-geometry";
    geometry.textContent = win.width + "×" + win.height;
    var label = document.createElement("span");
    label.className = "screenshot-window-title";
    label.textContent = screenshotWindowLabel(win);
    item.appendChild(label);
    item.appendChild(geometry);
    item.addEventListener("click", function () {
      screenshotChooseWindow(win);
    });
    list.appendChild(item);
  });
}

function screenshotChooseWindow(win) {
  if (!win) return;
  screenshotToggleWindowPicker(false);
  screenshotLockSelection({
    kind: "window",
    windowHandle: win.handle,
    x: win.x,
    y: win.y,
    width: win.width,
    height: win.height,
    monitorIndex: 0,
  });
}

function screenshotWindowPickerChooseFirst() {
  var list = document.getElementById("screenshotWindowList");
  if (!list) return;
  var first = list.querySelector(".screenshot-window-item");
  if (first) first.click();
}

function screenshotToggleWindowPicker(force) {
  var picker = document.getElementById("screenshotWindowPicker");
  if (!picker) return;
  var show = typeof force === "boolean" ? force : picker.classList.contains("hidden");
  if (show && !screenshotOverlayState) return;
  picker.classList.toggle("hidden", !show);
  if (!show) return;
  var search = document.getElementById("screenshotWindowSearch");
  var list = document.getElementById("screenshotWindowList");
  if (list) list.innerHTML = "";
  screenshotRenderWindowList("");
  if (search) {
    search.value = "";
    search.placeholder = screenshotT("screenshot.windowSearch");
    search.focus();
  }
}

