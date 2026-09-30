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
  div.appendChild(caption);
  div.appendChild(img);
  container.appendChild(div);
  if (typeof scheduleChatScrollToBottom === "function") scheduleChatScrollToBottom();
}

// ── Overlay de seleção ───────────────────────────────────────────────────

function screenshotOverlayRoot() {
  return document.getElementById("screenshotOverlay");
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
    '<div id="screenshotOverlayWindow" class="screenshot-overlay-window hidden"></div>',
    '<div id="screenshotOverlaySelection" class="screenshot-overlay-selection hidden"></div>',
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

  if (full) full.addEventListener("click", function () { screenshotFinishFull(); });
  if (winBtn) winBtn.addEventListener("click", function () { screenshotFinishFrontWindow(); });
  if (cancel) cancel.addEventListener("click", function () { screenshotCancelOverlay(); });

  var img = document.getElementById("screenshotOverlayImage");
  img.addEventListener("mousedown", screenshotOnMouseDown);
  img.addEventListener("mousemove", screenshotOnMouseMove);
  img.addEventListener("mouseup", screenshotOnMouseUp);
  img.addEventListener("mouseleave", function () {
    var highlight = document.getElementById("screenshotOverlayWindow");
    if (highlight) highlight.classList.add("hidden");
  });
  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape" && screenshotOverlayState) {
      event.preventDefault();
      screenshotCancelOverlay();
    }
  });
}

function screenshotShowOverlay(payload) {
  if (!payload || !payload.session) return;
  screenshotEnsureOverlayEls();
  var root = screenshotOverlayRoot();
  var img = document.getElementById("screenshotOverlayImage");
  if (!root || !img) return;
  root.classList.remove("hidden");
  // ready=false até a imagem da tela congelada carregar: sem isso, um movimento
  // de mouse antes do onload mapearia coordenadas com naturalWidth=0 (escala 1)
  // e a seleção sairia deslocada.
  screenshotOverlayState = { payload: payload, dragging: false, startX: 0, startY: 0, hover: null, ready: false };
  img.onload = function () {
    if (screenshotOverlayState && screenshotOverlayState.payload.session === payload.session) {
      screenshotOverlayState.ready = true;
    }
  };
  img.onerror = function () {
    screenshotFeedback(screenshotT("screenshot.failed"), true);
    screenshotCancelOverlay();
  };
  img.src = payload.imageDataUrl || "";
  if (img.complete && img.naturalWidth > 0 && screenshotOverlayState) {
    screenshotOverlayState.ready = true;
  }
  var hint = document.getElementById("screenshotOverlayHint");
  if (hint) {
    hint.textContent = payload.requestedByLlm
      ? screenshotT("screenshot.llmHint", { reason: payload.reason || "" })
      : screenshotT("screenshot.overlayHint");
  }
}

function screenshotHideOverlay() {
  var root = screenshotOverlayRoot();
  if (root) root.classList.add("hidden");
  var selection = document.getElementById("screenshotOverlaySelection");
  if (selection) selection.classList.add("hidden");
  var highlight = document.getElementById("screenshotOverlayWindow");
  if (highlight) highlight.classList.add("hidden");
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
    if (w.isSelf || w.minimized || !w.width || !w.height) continue;
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
  if (!state || !state.ready) return;
  state.dragging = true;
  state.startX = event.clientX;
  state.startY = event.clientY;
}

function screenshotOnMouseMove(event) {
  var state = screenshotOverlayState;
  if (!state || !state.ready) return;
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
      screenshotFinishSelection({
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
  screenshotFinishSelection({
    kind: "region",
    x: p.virtualX + Math.round(left * metrics.sx),
    y: p.virtualY + Math.round(top * metrics.sy),
    width: Math.round(width * metrics.sx),
    height: Math.round(height * metrics.sy),
    windowHandle: 0,
    monitorIndex: 0,
  });
}

function screenshotFinishFull() {
  var state = screenshotOverlayState;
  if (!state) return;
  var p = state.payload;
  screenshotFinishSelection({
    kind: "region",
    x: p.virtualX,
    y: p.virtualY,
    width: p.virtualWidth,
    height: p.virtualHeight,
    windowHandle: 0,
    monitorIndex: 0,
  });
}

function screenshotFinishFrontWindow() {
  var state = screenshotOverlayState;
  if (!state || !state.payload.windows) return;
  for (var i = 0; i < state.payload.windows.length; i += 1) {
    var w = state.payload.windows[i];
    if (w.isSelf || w.minimized || !w.width || !w.height) continue;
    screenshotFinishSelection({
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
  screenshotAppendInfoBubble({ dataUrl: info.dataUrl }, label);
}

// Handler do evento "screenshot:overlay_close" (backend fechou a sessão).
function onScreenshotOverlayClose(data) {
  if (!screenshotOverlayState) return;
  var info = screenshotParse(data);
  if (!info || !info.session || info.session === screenshotOverlayState.payload.session) {
    screenshotHideOverlay();
  }
}

function initScreenshotCapture() {
  var button = document.getElementById("chatScreenshotBtn");
  if (button) {
    button.addEventListener("click", openScreenshotCapture);
  }
  screenshotRenderAttachments();
}
