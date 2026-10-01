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
    var meta = document.createElement("span");
    meta.className = "chat-attachment-meta";
    meta.textContent = att.label || "print";
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
    chip.appendChild(remove);
    bar.appendChild(chip);
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

function screenshotAttach(result) {
  if (!result || !result.dataUrl) return;
  if (screenshotAttachments.length >= 3) {
    screenshotFeedback(screenshotT("chat.screenshotLimit"), true);
    return;
  }
  screenshotAttachments.push({
    dataUrl: result.dataUrl,
    label: (result.width || 0) + "x" + (result.height || 0),
  });
  screenshotRenderAttachments();
  screenshotFeedback(screenshotT("chat.screenshotAttached"), false);
}

// screenshotRestoreAttachments devolve anexos que não puderam ser enviados
// (binding ausente ou falha no dispatch) — o usuário não perde o print.
function screenshotRestoreAttachments(images) {
  if (!images || !images.length) return;
  images.forEach(function (src) {
    if (!src || screenshotAttachments.length >= 3) return;
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

function screenshotAppendInfoBubble(result, label) {
  if (!result || !result.dataUrl) return;
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
  if (result.auditId) img.setAttribute("data-screenshot-id", String(result.auditId));
  div.appendChild(caption);
  div.appendChild(img);
  container.appendChild(div);
  if (typeof scheduleChatScrollToBottom === "function") scheduleChatScrollToBottom();
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
    '<div class="screenshot-overlay-toolbar">',
    '  <span id="screenshotOverlayHint" class="screenshot-overlay-hint"></span>',
    '  <span id="screenshotOverlaySize" class="screenshot-overlay-size"></span>',
    '  <button id="screenshotModeFull" type="button" class="btn subtle"></button>',
    '  <button id="screenshotModeWindow" type="button" class="btn subtle"></button>',
    '  <button id="screenshotModeCancel" type="button" class="btn danger"></button>',
    '</div>',
    '<img id="screenshotOverlayImage" class="screenshot-overlay-image" alt="tela congelada" draggable="false" />',
    '<canvas id="screenshotAnnotCanvas" class="screenshot-annot-canvas hidden"></canvas>',
    '<input id="screenshotAnnotTextInput" class="screenshot-annot-input hidden" type="text" maxlength="120" />',
    '<div id="screenshotOverlayWindow" class="screenshot-overlay-window hidden"></div>',
    '<div id="screenshotOverlaySelection" class="screenshot-overlay-selection hidden"></div>',
    '<div id="screenshotAnnotToolbar" class="screenshot-annot-toolbar hidden">',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="rect" data-i18n-title="screenshot.annotRect">▭</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="arrow" data-i18n-title="screenshot.annotArrow">↗</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="line" data-i18n-title="screenshot.annotLine">╱</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="pen" data-i18n-title="screenshot.annotPen">✎</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="marker" data-i18n-title="screenshot.annotMarker">🖍</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="blur" data-i18n-title="screenshot.annotBlur">▦</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="step" data-i18n-title="screenshot.annotStep">①</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="text" data-i18n-title="screenshot.annotText">T</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-tool="eraser" data-i18n-title="screenshot.annotEraser">🧽</button>',
    '  <span class="screenshot-annot-sep"></span>',
    '  <button type="button" class="screenshot-annot-color" data-annot-color="#ff3b30" style="background:#ff3b30"></button>',
    '  <button type="button" class="screenshot-annot-color" data-annot-color="#ffcc00" style="background:#ffcc00"></button>',
    '  <button type="button" class="screenshot-annot-color" data-annot-color="#34c759" style="background:#34c759"></button>',
    '  <button type="button" class="screenshot-annot-color" data-annot-color="#0a84ff" style="background:#0a84ff"></button>',
    '  <span class="screenshot-annot-sep"></span>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-width="3" data-i18n-title="screenshot.widthThin">•</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-width="6" data-i18n-title="screenshot.widthMedium">●●</button>',
    '  <button type="button" class="screenshot-annot-btn" data-annot-width="10" data-i18n-title="screenshot.widthThick">●●●</button>',
    '  <span class="screenshot-annot-sep"></span>',
    '  <button type="button" class="screenshot-annot-btn" id="screenshotAnnotUndo" data-i18n-title="screenshot.annotUndo">↺</button>',
    '  <button type="button" class="screenshot-annot-btn" id="screenshotAnnotClear" data-i18n-title="screenshot.annotClear">🧹</button>',
    '  <button type="button" class="screenshot-annot-btn" id="screenshotAnnotReselect" data-i18n-title="screenshot.annotReselect">⟲</button>',
    '  <button type="button" class="screenshot-annot-confirm" id="screenshotAnnotConfirm" data-i18n-title="screenshot.annotConfirm">✓</button>',
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
  canvas.addEventListener("mouseup", screenshotAnnotUp);
  canvas.addEventListener("mouseleave", screenshotAnnotUp);
  Array.prototype.forEach.call(root.querySelectorAll("[data-annot-tool]"), function (btn) {
    btn.addEventListener("click", function () { screenshotSetAnnotTool(btn.getAttribute("data-annot-tool")); });
  });
  Array.prototype.forEach.call(root.querySelectorAll("[data-annot-color]"), function (btn) {
    btn.addEventListener("click", function () { screenshotSetAnnotColor(btn.getAttribute("data-annot-color")); });
  });
  Array.prototype.forEach.call(root.querySelectorAll("[data-annot-width]"), function (btn) {
    btn.addEventListener("click", function () { screenshotSetAnnotWidth(Number(btn.getAttribute("data-annot-width")) || 6); });
  });
  var undoBtn = document.getElementById("screenshotAnnotUndo");
  if (undoBtn) undoBtn.addEventListener("click", screenshotAnnotUndo);
  var clearBtn = document.getElementById("screenshotAnnotClear");
  if (clearBtn) clearBtn.addEventListener("click", screenshotAnnotClear);
  var reselectBtn = document.getElementById("screenshotAnnotReselect");
  if (reselectBtn) reselectBtn.addEventListener("click", screenshotAnnotReselect);
  var confirmBtn = document.getElementById("screenshotAnnotConfirm");
  if (confirmBtn) confirmBtn.addEventListener("click", screenshotConfirmSelection);
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

  document.addEventListener("keydown", function (event) {
    if (!screenshotOverlayState) return;
    if (event.key === "Escape") { event.preventDefault(); screenshotCancelOverlay(); return; }
    if (event.key === "Enter" && screenshotOverlayState.selection) { event.preventDefault(); screenshotConfirmSelection(); return; }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "z") { event.preventDefault(); screenshotAnnotUndo(); }
  });
}

function screenshotShowOverlay(payload) {
  if (!payload || !payload.session) return;
  screenshotEnsureOverlayEls();
  var root = screenshotOverlayRoot();
  var img = document.getElementById("screenshotOverlayImage");
  if (!root || !img) return;
  root.classList.remove("hidden");
  // Esconde o chrome do app durante a captura (a janela está em modo overlay).
  document.body.classList.add("screenshot-active");
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
  // Estado inicial das ferramentas (destaque visual nos botões).
  screenshotSetAnnotTool("rect");
  screenshotSetAnnotColor("#ff3b30");
  screenshotSetAnnotWidth(6);
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
  var hint = document.getElementById("screenshotOverlayHint");
  if (hint) {
    hint.textContent = payload.requestedByLlm
      ? screenshotT("screenshot.llmHint", { reason: payload.reason || "" })
      : screenshotT("screenshot.overlayHint");
  }
  // Política: sem tela inteira permitida, o atalho nem aparece.
  var fullBtn = document.getElementById("screenshotModeFull");
  if (fullBtn) fullBtn.classList.toggle("hidden", payload.policyFullScreenAllowed === false);
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
  screenshotAnnotRedraw();
}

function screenshotHideOverlay() {
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
  screenshotOverlayState = null;
}

function screenshotMetrics() {
  var img = document.getElementById("screenshotOverlayImage");
  var rect = img.getBoundingClientRect();
  var sx = img.naturalWidth > 0 && rect.width > 0 ? img.naturalWidth / rect.width : 1;
  var sy = img.naturalHeight > 0 && rect.height > 0 ? img.naturalHeight / rect.height : 1;
  return { rect: rect, sx: sx, sy: sy };
}

function screenshotWindowAt(cssX, cssY, metrics) {
  var state = screenshotOverlayState;
  if (!state || !state.payload.windows) return null;
  var p = state.payload;
  for (var i = 0; i < p.windows.length; i += 1) {
    var w = p.windows[i];
    if (w.isSelf || w.blocked || w.minimized || !w.width || !w.height) continue;
    var x = (w.x - p.virtualX) / metrics.sx;
    var y = (w.y - p.virtualY) / metrics.sy;
    var ww = w.width / metrics.sx;
    var hh = w.height / metrics.sy;
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
  state.dragging = true;
  state.startX = event.clientX;
  state.startY = event.clientY;
}

function screenshotOnMouseMove(event) {
  var state = screenshotOverlayState;
  if (!state || !state.ready || state.selection) return;
  var metrics = screenshotMetrics();
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
    if (size) size.textContent = Math.round(width * metrics.sx) + " \u00d7 " + Math.round(height * metrics.sy);
    if (highlight) highlight.classList.add("hidden");
    return;
  }
  var hover = screenshotWindowAt(event.clientX - metrics.rect.left, event.clientY - metrics.rect.top, metrics);
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
  var moved = Math.abs(event.clientX - state.startX) + Math.abs(event.clientY - state.startY);
  state.dragging = false;
  if (moved < 6) {
    var hover = screenshotWindowAt(event.clientX - metrics.rect.left, event.clientY - metrics.rect.top, metrics);
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
  screenshotLockSelection({
    kind: "region",
    x: p.virtualX + Math.round(left * metrics.sx),
    y: p.virtualY + Math.round(top * metrics.sy),
    width: Math.round(width * metrics.sx),
    height: Math.round(height * metrics.sy),
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
  var metrics = screenshotMetrics();
  state.selection = selection;
  state.annotations = [];
  state.draft = null;
  var box = document.getElementById("screenshotOverlaySelection");
  if (box) {
    box.classList.remove("hidden");
    box.style.left = (selection.x - state.payload.virtualX) / metrics.sx + "px";
    box.style.top = (selection.y - state.payload.virtualY) / metrics.sy + "px";
    box.style.width = selection.width / metrics.sx + "px";
    box.style.height = selection.height / metrics.sy + "px";
  }
  var highlight = document.getElementById("screenshotOverlayWindow");
  if (highlight) highlight.classList.add("hidden");
  var canvas = screenshotOverlayCanvas();
  if (canvas) canvas.classList.remove("hidden");
  screenshotSetAnnotToolbarVisible(true);
  screenshotAnnotRedraw();
}

function screenshotAnnotReselect() {
  var state = screenshotOverlayState;
  if (!state) return;
  state.selection = null;
  state.annotations = [];
  state.draft = null;
  var box = document.getElementById("screenshotOverlaySelection");
  if (box) box.classList.add("hidden");
  var canvas = screenshotOverlayCanvas();
  if (canvas) canvas.classList.add("hidden");
  var textInput = document.getElementById("screenshotAnnotTextInput");
  if (textInput) textInput.classList.add("hidden");
  screenshotSetAnnotToolbarVisible(false);
}

function screenshotSetAnnotToolbarVisible(visible) {
  var toolbar = document.getElementById("screenshotAnnotToolbar");
  if (toolbar) toolbar.classList.toggle("hidden", !visible);
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

function screenshotSetAnnotColor(color) {
  var state = screenshotOverlayState;
  if (!state || !color) return;
  state.color = color;
  var root = screenshotOverlayRoot();
  if (!root) return;
  Array.prototype.forEach.call(root.querySelectorAll("[data-annot-color]"), function (btn) {
    btn.classList.toggle("active", btn.getAttribute("data-annot-color") === color);
  });
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
  return tool === "rect" || tool === "arrow" || tool === "line" || tool === "blur";
}

function screenshotAnnotDown(event) {
  var state = screenshotOverlayState;
  if (!state || !state.selection) return;
  event.preventDefault();
  var point = screenshotAnnotPoint(event);
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
function screenshotShapeDistance(shape, point) {
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
  if (shape.type === "blur") {
    // O borrão cobre a área inteira: qualquer ponto dentro apaga.
    var bx = Math.min(shape.x1, shape.x2);
    var by = Math.min(shape.y1, shape.y2);
    var bw = Math.abs(shape.x2 - shape.x1);
    var bh = Math.abs(shape.y2 - shape.y1);
    var inside = point.x >= bx && point.x <= bx + bw && point.y >= by && point.y <= by + bh;
    return inside ? 0 : Infinity;
  }
  if (shape.points && shape.points.length) {
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
function screenshotEraseAt(point) {
  var state = screenshotOverlayState;
  if (!state) return;
  var tolerance = 16;
  var bestIndex = -1;
  var bestDistance = Infinity;
  for (var i = 0; i < state.annotations.length; i += 1) {
    var distance = screenshotShapeDistance(state.annotations[i], point);
    if (distance <= tolerance && distance <= bestDistance) {
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
  input.style.left = (clientX - metrics.rect.left) + "px";
  input.style.top = (clientY - metrics.rect.top) + "px";
  input.value = "";
  input.classList.remove("hidden");
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
  } else if (shape.type === "line") {
    ctx.beginPath();
    ctx.moveTo(shape.x1, shape.y1);
    ctx.lineTo(shape.x2, shape.y2);
    ctx.stroke();
  } else if (shape.type === "arrow") {
    screenshotDrawArrow(ctx, shape.x1, shape.y1, shape.x2, shape.y2);
  } else if (shape.type === "blur") {
    screenshotBlurRegion(ctx, shape, source, drawScale);
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
  ctx.beginPath();
  ctx.moveTo(x + radius, y);
  ctx.lineTo(x + w - radius, y);
  ctx.quadraticCurveTo(x + w, y, x + w, y + radius);
  ctx.lineTo(x + w, y + h - radius);
  ctx.quadraticCurveTo(x + w, y + h, x + w - radius, y + h);
  ctx.lineTo(x + radius, y + h);
  ctx.quadraticCurveTo(x, y + h, x, y + h - radius);
  ctx.lineTo(x, y + radius);
  ctx.quadraticCurveTo(x, y, x + radius, y);
  ctx.closePath();
  ctx.stroke();
}

// Borrão/pixelado: reduz a região em um canvas auxiliar e devolve ampliada com
// suavização desligada. Funciona tanto no canvas do overlay quanto na composição
// final (o ctx já está transformado em pixels da imagem).
function screenshotBlurRegion(ctx, shape, source, drawScale) {
  if (!source) return;
  var x = Math.min(shape.x1, shape.x2);
  var y = Math.min(shape.y1, shape.y2);
  var w = Math.abs(shape.x2 - shape.x1);
  var h = Math.abs(shape.y2 - shape.y1);
  if (w < 4 || h < 4) return;
  var scale = drawScale > 0 ? drawScale : 1;
  var supportsFilter = false;
  try {
    ctx.filter = "blur(1px)";
    supportsFilter = ctx.filter === "blur(1px)";
    ctx.filter = "none";
  } catch (_) {
    supportsFilter = false;
  }
  if (supportsFilter) {
    // Filtro gaussiano nativo: sem "blocos" visíveis em zoom.
    var radius = Math.max(5, (shape.width || 6) * 2.5) * scale;
    ctx.save();
    ctx.filter = "blur(" + radius.toFixed(2) + "px)";
    ctx.drawImage(source, x, y, w, h, x, y, w, h);
    ctx.restore();
    return;
  }
  // Fallback (ambiente sem ctx.filter): reduz e devolve suavizado.
  var blocks = Math.max(5, Math.round(w / 22));
  var blockH = Math.max(3, Math.round((h / w) * blocks));
  var temp = document.createElement("canvas");
  temp.width = blocks;
  temp.height = blockH;
  var tctx = temp.getContext("2d");
  tctx.drawImage(source, x, y, w, h, 0, 0, blocks, blockH);
  ctx.save();
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(temp, 0, 0, blocks, blockH, x, y, w, h);
  ctx.restore();
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
  // 2560 px (mesmo teto do backend): preserva a legibilidade de textos pequenos.
  var maxDim = 2560;
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
  // WebP lossy q0.95 quando o backend suporta (a tela congelada veio em WebP):
  // mesma percepção de qualidade com uma fração dos bytes do PNG.
  if (state.payload.imageMime === "image/webp") {
    try {
      var webpUrl = canvas.toDataURL("image/webp", 0.95);
      if (webpUrl && webpUrl.indexOf("data:image/webp") === 0) return webpUrl;
    } catch (_) {}
  }
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
  if (state.annotations.length > 0) {
    try {
      selection.annotatedDataUrl = screenshotBuildAnnotatedDataUrl();
    } catch (err) {
      screenshotFeedback("Falha ao compor as anotacoes: " + err, true);
      return;
    }
  }
  screenshotFinishSelection(selection);
}

function screenshotFinishSelection(selection) {
  var state = screenshotOverlayState;
  if (!state) return;
  var api = screenshotApi();
  var payload = state.payload;
  var byLLM = !!payload.requestedByLlm;
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
      return;
    }
    if (byLLM) {
      screenshotAppendInfoBubble(result, screenshotT("screenshot.llmCaptured"));
    } else {
      screenshotAttach(result);
    }
  }).catch(function (err) {
    screenshotFeedback(screenshotT("screenshot.failed") + ": " + err, true);
  });
}

function screenshotCancelOverlay() {
  var state = screenshotOverlayState;
  if (!state) return;
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

function openScreenshotCapture() {
  var api = screenshotApi();
  if (!api || typeof api.PrepareScreenshotOverlay !== "function") {
    screenshotFeedback(screenshotT("screenshot.unavailable"), true);
    return;
  }
  if (screenshotOverlayState) return;
  screenshotFeedback(screenshotT("screenshot.capturing"), false);
  api.PrepareScreenshotOverlay("captura manual do usuario").then(function (raw) {
    var payload = screenshotParse(raw);
    if (!payload || !payload.session) {
      screenshotFeedback(screenshotT("screenshot.failed"), true);
      return;
    }
    screenshotShowOverlay(payload);
  }).catch(function (err) {
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
      screenshotFeedback("Falha ao carregar privacidade: " + err, true);
    });
}

function screenshotConsentLabel(decision) {
  switch (decision) {
    case "session":
      return screenshotT("screenshot.consentSession");
    case "always":
      return screenshotT("screenshot.consentAlways");
    case "denied":
      return screenshotT("screenshot.consentDenied");
    default:
      return screenshotT("screenshot.consentUndecided");
  }
}

function screenshotRenderPrivacy(data) {
  var stateEl = screenshotModalEl("chatPrivacyState");
  if (stateEl) {
    stateEl.textContent = screenshotConsentLabel(data.decision);
    stateEl.classList.toggle("allowed", data.decision === "session" || data.decision === "always");
    stateEl.classList.toggle("denied", data.decision === "denied");
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
  if (formatEl) formatEl.value = policy.imageFormat === "png" ? "png" : "auto";
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
      if (item.id) img.setAttribute("data-screenshot-id", String(item.id));
      row.appendChild(img);
    }
    var info = document.createElement("div");
    info.className = "chat-privacy-info";
    var who = item.byLlm ? screenshotT("screenshot.auditByLlm") : screenshotT("screenshot.auditManual");
    var when = "";
    try {
      when = item.at ? new Date(item.at).toLocaleString() : "";
    } catch (_) {}
    var head = document.createElement("div");
    head.className = "chat-privacy-line";
    head.textContent =
      when + " · " + item.mode + " · " + who + " · " + Math.round((item.bytes || 0) / 1024) + " KB";
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
  api.SaveScreenshotPolicy(JSON.stringify(payload))
    .then(function () {
      screenshotFeedback(screenshotT("screenshot.policySaved"), false);
      screenshotLoadPrivacy();
    })
    .catch(function (err) {
      screenshotFeedback("Falha ao salvar politica: " + err, true);
    });
}

function screenshotSetConsent(decision) {
  var api = screenshotApi();
  if (!api || typeof api.SetScreenshotPermission !== "function") {
    screenshotFeedback(screenshotT("screenshot.unavailable"), true);
    return;
  }
  var pending = api.SetScreenshotPermission(decision);
  if (pending && typeof pending.then === "function") {
    pending
      .then(function () {
        screenshotLoadPrivacy();
      })
      .catch(function (err) {
        screenshotFeedback(String(err), true);
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
  img.src = dataUrl;
  if (cap) cap.textContent = caption || "";
  box.classList.remove("hidden");
}

function screenshotCloseLightbox() {
  var box = document.getElementById("screenshotLightbox");
  if (box) box.classList.add("hidden");
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

function screenshotThumbFromEvent(event) {
  var target = event.target;
  if (!target || !target.closest) return null;
  return target.closest(".chat-msg-image, .chat-attachment img, .chat-privacy-thumb");
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
    var thumb = screenshotThumbFromEvent(event);
    if (thumb) screenshotShowHoverPreview(thumb);
  });
  document.addEventListener("mouseout", function (event) {
    if (screenshotThumbFromEvent(event)) screenshotHideHoverPreview();
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
  var saveBtn = document.getElementById("chatPrivacySavePolicy");
  if (saveBtn) saveBtn.addEventListener("click", screenshotSavePrivacyPolicy);
  var allowSession = document.getElementById("chatPrivacyAllowSession");
  if (allowSession) {
    allowSession.addEventListener("click", function () { screenshotSetConsent("session"); });
  }
  var allowAlways = document.getElementById("chatPrivacyAllowAlways");
  if (allowAlways) {
    allowAlways.addEventListener("click", function () { screenshotSetConsent("always"); });
  }
  var deny = document.getElementById("chatPrivacyDeny");
  if (deny) deny.addEventListener("click", function () { screenshotSetConsent("denied"); });
  var modal = screenshotModalEl("chatPrivacyModal");
  if (modal) {
    modal.addEventListener("click", function (event) {
      if (event.target === modal) closeScreenshotPrivacyModal();
    });
  }
}

function initScreenshotCapture() {
  var button = document.getElementById("chatScreenshotBtn");
  if (button) {
    button.addEventListener("click", openScreenshotCapture);
  }
  initScreenshotPrivacy();
  initScreenshotLightbox();
  screenshotRenderAttachments();
}
