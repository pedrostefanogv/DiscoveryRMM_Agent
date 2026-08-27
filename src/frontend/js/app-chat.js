"use strict";

var chatSending = false;
var chatStopRequested = false;
var chatThinkingPollId = null;
// Timer de segurança: se o stream não terminar em X segundos, força o
// encerramento da bolha "Pensando..." para não travar a interface.
var chatStreamTimeoutId = null;
var CHAT_STREAM_TIMEOUT_MS = 60000; // 60s

// Streaming state
var streamingBubble = null;
var streamingRawContent = "";
var streamingRafPending = false;

// Funções de polling do chat. São DECLARADAS aqui (escopo global) e
// ATRIBUÍDAS dentro do IIFE registerChatStreamEvents, pois dependem do estado
// interno daquele IIFE (pollTimerId, POLL_INTERVAL_MS, routeChatEvent, etc.).
// Os handlers globais (onStreamDone/onStreamError/onStreamStopped/sendChatMessage)
// precisam chamá-las — se ficassem só dentro do IIFE, o "use strict" lançaria
// ReferenceError e o polling nunca iniciaria (bug corrigido em 2026-08-27).
var startPollingLoop;
var stopPollingLoop;

// Estado do filtro de blocos A2UI nos tokens visíveis.
// O servidor emite os tokens em tempo real e só extrai o bloco ```a2ui no
// final do stream. Para o usuário não ver o JSON cru, filtramos o conteúdo
// entre ```a2ui e ``` conforme os tokens chegam (de forma incremental).
var a2uiTokenFilter = {
  inBlock: false,
  buffer: "",
};

// Filtra um fragmento de token, removendo qualquer bloco ```a2ui ... ```.
// Retorna o texto "limpo" que deve ser exibido ao usuário.
//
// Estratégia: mantém um buffer pequeno (sufixo potencial de um marcador) e só
// emite texto quando temos certeza de que ele não faz parte de um bloco a2ui.
function filterA2uiTokens(token) {
  var f = a2uiTokenFilter;
  f.buffer += token;

  var out = "";
  var i = 0;
  while (i < f.buffer.length) {
    if (!f.inBlock) {
      // Procura a abertura do bloco a2ui.
      var openIdx = f.buffer.indexOf("```a2ui", i);
      if (openIdx === -1) {
        // Sem abertura: emite tudo até o fim, mantendo os últimos 6 chars no
        // buffer (pode ser o início de ```a2ui). Não limpa o buffer aqui.
        var keep = Math.min(6, f.buffer.length - i);
        out += f.buffer.slice(i, f.buffer.length - keep);
        f.buffer = f.buffer.slice(f.buffer.length - keep);
        break;
      }
      // Texto antes da abertura é visível.
      out += f.buffer.slice(i, openIdx);
      f.inBlock = true;
      i = openIdx + 6; // pula "```a2ui"
    } else {
      // Dentro do bloco: procura o fechamento ```.
      var closeIdx = f.buffer.indexOf("```", i);
      if (closeIdx === -1) {
        // Sem fechamento ainda: descarta tudo até o fim, mantendo os últimos
        // 3 chars no buffer (pode ser o início de ```).
        var keepClose = Math.min(3, f.buffer.length - i);
        f.buffer = f.buffer.slice(f.buffer.length - keepClose);
        break;
      }
      // Fecha o bloco e descarta o conteúdo.
      f.inBlock = false;
      i = closeIdx + 3; // pula "```"
    }
  }

  return out;
}

function resetA2uiTokenFilter() {
  a2uiTokenFilter.inBlock = false;
  a2uiTokenFilter.buffer = "";
}

function onStreamToken(token) {
  streamingRawContent += filterA2uiTokens(token);
  if (document.hidden || window.__discoveryUISuspended) {
    return;
  }
  if (!streamingRafPending) {
    streamingRafPending = true;
    requestAnimationFrame(flushStreamingContent);
  }
}

function flushStreamingContent() {
  streamingRafPending = false;
  if (!streamingBubble) return;
  var contentEl = streamingBubble.querySelector(".stream-content");
  if (!contentEl) {
    contentEl = document.createElement("div");
    contentEl.className = "stream-content";
    var thinkingEl = streamingBubble.querySelector(".stream-thinking");
    if (thinkingEl) {
      streamingBubble.insertBefore(contentEl, thinkingEl);
      thinkingEl.style.display = "none";
    } else {
      streamingBubble.appendChild(contentEl);
    }
  }
  contentEl.innerHTML = renderAssistantMarkdown(streamingRawContent);
  syncColorMode();
  bindInternalChatLinks(contentEl);
  // Force reflow to ensure the bubble background expands with the content.
  void streamingBubble.offsetHeight;
  scheduleChatScrollToBottom();
}

function setChatBusy(isBusy) {
  chatSending = !!isBusy;
  if (chatSendBtn) chatSendBtn.disabled = !!isBusy;
  if (chatStopBtn) {
    chatStopBtn.classList.toggle("hidden", !isBusy);
    chatStopBtn.disabled = !isBusy;
    chatStopBtn.textContent = translate("action.stop");
  }
}

// clearChatStreamTimeout cancela o timer de segurança do stream, se ativo.
function clearChatStreamTimeout() {
  if (chatStreamTimeoutId) {
    clearTimeout(chatStreamTimeoutId);
    chatStreamTimeoutId = null;
  }
}

// armChatStreamTimeout inicia o timer de segurança. Se o stream não terminar
// dentro de CHAT_STREAM_TIMEOUT_MS, força onStreamError para não deixar o
// usuário preso em "Pensando..." indefinidamente.
function armChatStreamTimeout() {
  clearChatStreamTimeout();
  chatStreamTimeoutId = setTimeout(function () {
    chatStreamTimeoutId = null;
    if (chatSending) {
      console.warn("[chat] timeout de segurança do stream atingido; encerrando");
      onStreamError(translate("chat.timeout"));
    }
  }, CHAT_STREAM_TIMEOUT_MS);
}

function requestStopChatStream() {
  if (!chatSending) return;
  chatStopRequested = true;
  if (chatStopBtn) {
    chatStopBtn.disabled = true;
    chatStopBtn.textContent = translate("chat.stopping");
  }
  try {
    appApi()
      .StopChatStream()
      .catch(function () {
        // If backend stop fails, UI still waits stream terminal event.
      });
  } catch (_) {
    // ignore
  }
}

function onStreamThinking(status) {
  if (document.hidden || window.__discoveryUISuspended) return;
  if (!streamingBubble) return;
  var thinkingEl = streamingBubble.querySelector(".stream-thinking");
  if (!thinkingEl) return;
  if (!streamingRawContent) {
    thinkingEl.style.display = "";
    thinkingEl.textContent = status || translate("chat.thinking");
    scheduleChatScrollToBottom();
  }
}

function finaliseStreamingBubble() {
  if (!streamingBubble) return;
  // Libera qualquer texto residual que ficou retido no buffer do filtro A2UI
  // (ex.: os últimos caracteres de uma resposta sem bloco a2ui). Se ainda
  // estivermos DENTRO de um bloco a2ui (stream interrompido no meio do JSON),
  // o residual é JSON truncado e deve ser descartado, não exibido.
  if (!a2uiTokenFilter.inBlock) {
    streamingRawContent += a2uiTokenFilter.buffer;
  }
  a2uiTokenFilter.buffer = "";
  a2uiTokenFilter.inBlock = false;

  // Flush any remaining buffered content immediately.
  streamingRafPending = false;
  flushStreamingContent();

  // Remove streaming indicators.
  var thinkingEl = streamingBubble.querySelector(".stream-thinking");
  if (thinkingEl) thinkingEl.remove();
  var cursor = streamingBubble.querySelector(".stream-cursor");
  if (cursor) cursor.remove();
  streamingBubble.classList.remove("streaming");

  // Add quick-action suggestion buttons if the assistant wrote "- " lines.
  var finalContent = streamingRawContent;
  var dynamicActions = extractChatActionOptions(finalContent);
  if (dynamicActions.length > 0) {
    appendChatQuickActions(streamingBubble, dynamicActions);
  }

  streamingBubble = null;
  streamingRawContent = "";
  scheduleChatScrollToBottom();
}

function onStreamDone() {
  stopPollingLoop();
  stopThinkingStatusUpdates();
  clearChatStreamTimeout();
  finaliseStreamingBubble();
  chatStopRequested = false;
  setChatBusy(false);
  if (chatInputEl) chatInputEl.focus();
}

function onStreamError(errMsg) {
  stopPollingLoop();
  stopThinkingStatusUpdates();
  clearChatStreamTimeout();

  if (chatStopRequested) {
    if (streamingBubble && !streamingRawContent) {
      streamingRawContent = translate("chat.responseInterrupted");
    }
    finaliseStreamingBubble();
    chatStopRequested = false;
    setChatBusy(false);
    if (chatInputEl) chatInputEl.focus();
    return;
  }

  if (streamingBubble) {
    // Show whatever content arrived; fallback to error text if nothing came.
    if (!streamingRawContent) {
      streamingRawContent = translate("chat.errorUnknown", {
        error: String(errMsg || translate("common.unknown")),
      });
    }
    finaliseStreamingBubble();
  } else {
    addChatMessage(
      "assistant",
      translate("chat.errorUnknown", {
        error: String(errMsg || translate("common.unknown")),
      }),
    );
  }
  setChatBusy(false);
  if (chatInputEl) chatInputEl.focus();
}

function onStreamStopped() {
  stopPollingLoop();
  stopThinkingStatusUpdates();
  clearChatStreamTimeout();
  if (streamingBubble && !streamingRawContent) {
    streamingRawContent = translate("chat.responseInterrupted");
  }
  finaliseStreamingBubble();
  chatStopRequested = false;
  setChatBusy(false);
  if (chatInputEl) chatInputEl.focus();
}

// ─── Mini-Questionário Interativo (ask_user MCP) ───

function onChatQuestion(data) {
  try {
    var q = typeof data === "string" ? JSON.parse(data) : data;
    if (!q || !q.id) return;
    showChatQuestion(q);
  } catch (e) {
    console.error("chat:question parse error:", e);
  }
}

function showChatQuestion(question) {
  if (!chatMessagesEl) return;

  var div = document.createElement("div");
  div.className = "chat-msg assistant chat-question";
  div.dataset.questionId = question.id;

  // Pergunta renderizada com markdown
  var contentEl = document.createElement("div");
  contentEl.className = "stream-content";
  contentEl.innerHTML = renderAssistantMarkdown(question.question);
  div.appendChild(contentEl);

  // Container de ações
  var actions = document.createElement("div");
  actions.className = "chat-msg-actions";

  // Opções como botões
  if (question.options && question.options.length > 0) {
    question.options.forEach(function (opt) {
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "btn subtle btn-xs";
      btn.innerHTML = formatInlineChatMarkdown(opt);
      btn.addEventListener("click", function () {
        answerChatQuestion(question.id, opt);
        disableQuestionButtons(div);
      });
      actions.appendChild(btn);
    });
  }

  // Campo de texto livre (sempre visível se allowText=true ou sem opções)
  if (question.allowText || !question.options || question.options.length === 0) {
    var textRow = document.createElement("div");
    textRow.className = "chat-question-text-row";

    var input = document.createElement("input");
    input.type = "text";
    input.className = "chat-question-input";
    input.placeholder = "Digite sua resposta...";

    var sendBtn = document.createElement("button");
    sendBtn.type = "button";
    sendBtn.className = "btn primary btn-xs chat-question-send-btn";
    sendBtn.textContent = "Enviar";

    sendBtn.addEventListener("click", function () {
      var answer = input.value.trim();
      if (!answer) return;
      answerChatQuestion(question.id, answer);
      disableQuestionButtons(div);
    });

    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        sendBtn.click();
      }
    });

    textRow.appendChild(input);
    textRow.appendChild(sendBtn);
    actions.appendChild(textRow);
  }

  div.appendChild(actions);
  chatMessagesEl.appendChild(div);
  syncColorMode();
  scheduleChatScrollToBottom();
}

function answerChatQuestion(questionId, answer) {
  try {
    appApi().AnswerChatQuestion(questionId, answer);
  } catch (e) {
    console.error("answerChatQuestion error:", e);
  }
}

function disableQuestionButtons(container) {
  container.querySelectorAll("button").forEach(function (btn) {
    btn.disabled = true;
  });
  container.querySelectorAll("input").forEach(function (input) {
    input.disabled = true;
  });
}

// ─── A2UI (Agent-to-User Interface) — interfaces ricas geradas por IA ───

// Estado da surface A2UI ativa no chat.
var a2uiSurfaceHandle = null;
var a2uiSurfaceBubble = null;

// onChatA2ui recebe uma mensagem A2UI (JSON) emitida pelo agent via "chat:a2ui".
// A primeira mensagem (createSurface) cria a bolha/surface; as demais
// (updateComponents/updateDataModel) alimentam o MessageProcessor.
function onChatA2ui(data) {
  var msg = typeof data === "string" ? data : JSON.stringify(data || "");
  if (!msg) return;

  // Garante que o bundle A2UI foi carregado (window.A2uiChat).
  if (!window.A2uiChat || typeof window.A2uiChat.createSurface !== "function") {
    console.warn("[a2ui] bundle A2UI não carregado; exibindo fallback");
    // Fallback visual: em vez de ignorar silenciosamente (que deixava o
    // usuário preso em "Pensando..." sem resposta), mostra uma mensagem
    // amigável. O texto markdown normal (fora do bloco a2ui) já foi exibido
    // pelo streaming, então não há perda de conteúdo relevante.
    fallbackA2uiToMarkdown(msg);
    return;
  }

  try {
    var parsed = JSON.parse(msg);
    // Defensivo contra dupla codificação: se o JSON veio como string aninhada
    // (ex.: "{\"version\":...}"), parseia novamente. Isso garante robustez
    // mesmo que o agente/servidor mude a serialização no futuro.
    if (typeof parsed === "string") {
      parsed = JSON.parse(parsed);
    }
    if (!parsed || !parsed.version) {
      console.warn("[a2ui] mensagem sem version; ignorando:", msg);
      return;
    }

    // Determina o surfaceId da mensagem (createSurface/updateComponents/...).
    var msgSurfaceId = null;
    if (parsed.createSurface) {
      msgSurfaceId = parsed.createSurface.surfaceId || "discovery-chat-surface";
    } else if (parsed.updateComponents) {
      msgSurfaceId = parsed.updateComponents.surfaceId || null;
    } else if (parsed.updateDataModel) {
      msgSurfaceId = parsed.updateDataModel.surfaceId || null;
    } else if (parsed.deleteSurface) {
      msgSurfaceId = parsed.deleteSurface.surfaceId || null;
    }

    // Se a mensagem referencia uma surface diferente da ativa, ignora (defensivo).
    if (
      msgSurfaceId &&
      a2uiSurfaceHandle &&
      a2uiSurfaceHandle.surfaceId !== msgSurfaceId
    ) {
      console.warn("[a2ui] mensagem para surface diferente; ignorando:", msgSurfaceId);
      return;
    }

    // createSurface → cria a bolha e a surface. O ensureA2uiSurface já envia
    // a mensagem createSurface ao processor (via entry.js), então não reenviamos
    // aqui para evitar duplicação.
    var isCreateSurface = !!parsed.createSurface;
    if (isCreateSurface) {
      ensureA2uiSurface(msgSurfaceId || "discovery-chat-surface");
    }

    if (!a2uiSurfaceHandle) {
      // Sem surface ativa, cria uma default (defensivo).
      ensureA2uiSurface("discovery-chat-surface");
    }

    // Para createSurface, o ensureA2uiSurface já enviou a mensagem ao processor.
    // Para as demais (updateComponents/updateDataModel/deleteSurface), envia.
    if (!isCreateSurface) {
      a2uiSurfaceHandle.processMessages([parsed]);
    }
    scheduleChatScrollToBottom();
  } catch (e) {
    // Fallback: se o A2UI falhar (JSON inválido, catalog/surface error, etc.),
    // não deixamos o usuário sem resposta. Renderiza o conteúdo bruto como
    // markdown normal e limpa a surface parcial, se houver.
    console.error("[a2ui] erro ao processar mensagem:", e);
    fallbackA2uiToMarkdown(msg);
  }
}

// fallbackA2uiToMarkdown é chamado quando o renderer A2UI falha. Em vez de
// exibir o JSON cru (lixo para o usuário), remove a surface parcial e mostra
// uma mensagem amigável. O texto markdown normal (fora do bloco a2ui) já foi
// exibido pelo streaming, então não há perda de conteúdo relevante.
function fallbackA2uiToMarkdown(rawMsg) {
  try {
    if (a2uiSurfaceHandle) {
      try { a2uiSurfaceHandle.destroy(); } catch (_) {}
      a2uiSurfaceHandle = null;
    }
    if (a2uiSurfaceBubble) {
      a2uiSurfaceBubble.remove();
      a2uiSurfaceBubble = null;
    }
    // Loga o payload bruto para diagnóstico, mas não o exibe ao usuário.
    console.warn("[a2ui] payload que falhou:", String(rawMsg || ""));
    if (!chatMessagesEl) return;
    var div = document.createElement("div");
    div.className = "chat-msg assistant";
    div.innerHTML = renderAssistantMarkdown(
      "Não foi possível exibir a interface interativa gerada.",
    );
    syncColorMode();
    bindInternalChatLinks(div);
    chatMessagesEl.appendChild(div);
    scheduleChatScrollToBottom();
  } catch (_) {
    // Nunca lançar a partir de um handler de evento.
  }
}

// ensureA2uiSurface cria a bolha de mensagem e a surface A2UI dentro dela.
function ensureA2uiSurface(surfaceId) {
  if (a2uiSurfaceHandle && a2uiSurfaceHandle.surfaceId === surfaceId) return;
  if (!chatMessagesEl) return;

  // Destrói surface anterior, se houver.
  if (a2uiSurfaceHandle) {
    try { a2uiSurfaceHandle.destroy(); } catch (_) {}
    a2uiSurfaceHandle = null;
  }

  var div = document.createElement("div");
  div.className = "chat-msg assistant chat-a2ui";
  div.dataset.surfaceId = surfaceId;

  var contentEl = document.createElement("div");
  contentEl.className = "a2ui-container";
  div.appendChild(contentEl);

  chatMessagesEl.appendChild(div);
  a2uiSurfaceBubble = div;

  try {
    a2uiSurfaceHandle = window.A2uiChat.createSurface(contentEl, surfaceId);
    a2uiSurfaceHandle.onUserAction(function (action) {
      handleA2uiUserAction(surfaceId, action);
    });
  } catch (e) {
    console.error("[a2ui] falha ao criar surface:", e);
    div.remove();
    a2uiSurfaceHandle = null;
    a2uiSurfaceBubble = null;
  }

  syncColorMode();
  scheduleChatScrollToBottom();
}

// handleA2uiUserAction encaminha uma ação do usuário (clique/input) ao agent.
// Registra a ação via AnswerA2uiAction e dispara um novo StartStream para que
// o agent processe a ação como um tool result no próximo round (resposta
// imediata ao clique, sem exigir que o usuário digite outra mensagem).
function handleA2uiUserAction(surfaceId, action) {
  if (!action || !action.name) return;
  try {
    var payload = {
      surfaceId: surfaceId,
      name: action.name,
      context: action.context || {},
    };
    appApi().AnswerA2uiAction(JSON.stringify(payload));
    // Dispara o processamento da ação. Usa uma mensagem-sentinela que o agent
    // reconhece como "ação A2UI" (a mensagem em si é ignorada; o que importa é
    // o tool result injetado). Evita duplicar se já houver um stream ativo.
    if (!chatSending) {
      sendChatMessageWithA2uiAction();
    }
  } catch (e) {
    console.error("[a2ui] falha ao enviar userAction:", e);
  }
}

// sendChatMessageWithA2uiAction dispara o processamento de uma ação A2UI.
// Não adiciona uma bolha de usuário (a ação não é uma mensagem digitada) e
// usa uma sentinela interna que o agent converte em tool result.
function sendChatMessageWithA2uiAction() {
  if (chatSending || !chatInputEl) return;

  chatStopRequested = false;
  setChatBusy(true);
  resetA2uiTokenFilter();

  // Cria a bolha de streaming para a resposta do agent à ação.
  streamingRawContent = "";
  streamingRafPending = false;
  streamingBubble = document.createElement("div");
  streamingBubble.className = "chat-msg assistant streaming";

  var thinkingEl = document.createElement("div");
  thinkingEl.className = "stream-thinking";
  thinkingEl.textContent = translate("chat.thinking");
  streamingBubble.appendChild(thinkingEl);

  var cursorEl = document.createElement("span");
  cursorEl.className = "stream-cursor";
  streamingBubble.appendChild(cursorEl);

  if (chatMessagesEl) chatMessagesEl.appendChild(streamingBubble);
  scheduleChatScrollToBottom();

  // Timer de segurança: também cobre o stream disparado por ação A2UI, para
  // não deixar "Pensando..." preso se o evento chat:done nunca chegar.
  armChatStreamTimeout();

  try {
    appApi()
      .StartChatStream("__a2ui_action__")
      .then(function () {
        if (window.__wailsV3Bridge) {
          startPollingLoop();
        }
      })
      .catch(function (err) {
        onStreamError(String(err));
      });
  } catch (err) {
    onStreamError(String(err));
  }
}

// Limpa a surface A2UI quando o chat é limpo.
function clearA2uiSurface() {
  if (a2uiSurfaceHandle) {
    try { a2uiSurfaceHandle.destroy(); } catch (_) {}
    a2uiSurfaceHandle = null;
  }
  a2uiSurfaceBubble = null;
}

// Register Wails event listeners once the runtime is ready.
//
// IMPORTANTE sobre a entrega de eventos no app nativo (Wails v3 beta):
// ver chat-native-event-loss.md. O caminho confiável é o broker SSE
// dedicado (`/api/chat-events` em 127.0.0.1), que agora é sempre iniciado
// no backend (não só em modo debug). O GetDebugHTTPPort retorna a porta do
// SSE dedicado mesmo fora do modo debug.
//
// Estratégia (2026-08-26 #2):
//   EventSource SSE é bloqueado pelo WebView2 como mixed-content
//   (https://wails.localhost → http://127.0.0.1). O Wails v3 beta.11 NÃO lê
//   WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS. Portanto, no runtime nativo usamos
//   POLLING via bindings Wails (PollChatEvents) — transporte IPC confiável.
//
//   1. Aguardar __wailsV3Bridge ser definido (race condition do carregamento).
//   2. No runtime nativo: polling via appApi().PollChatEvents() a cada 100ms.
//   3. No navegador: o debug-http-bridge conecta ao SSE via window.wails.on.
//   4. Fallback: listeners nativos (Events.On) se polling/bindings falharem.
(function registerChatStreamEvents() {
  // Mapeia cada evento de chat ao seu handler.
  var CHAT_EVENT_HANDLERS = {
    "chat:token": onStreamToken,
    "chat:thinking": onStreamThinking,
    "chat:done": onStreamDone,
    "chat:error": onStreamError,
    "chat:stopped": onStreamStopped,
    "chat:question": onChatQuestion,
    "chat:a2ui": onChatA2ui,
  };

  var nativeListenersRegistered = false;
  // Transport indicator
  var transportIndicatorId = null;
  // Polling state
  var pollTimerId = null;
  var POLL_INTERVAL_MS = 80; // ~12 polls/segundo — baixa latência, baixo overhead
  // Stream terminal events — paramos o polling ao receber qualquer um deles.
  var STREAM_TERMINAL_EVENTS = { "chat:done": true, "chat:error": true, "chat:stopped": true };

  // Exibe/atualiza um indicador visual de qual transporte de chat está ativo.
  // Modos: "poll" (verde), "sse" (verde), "native" (amarelo), "error" (vermelho), "none" (cinza).
  function updateTransportIndicator(mode, detail) {
    var el = document.getElementById("chatTransportIndicator");
    if (!el) {
      el = document.createElement("div");
      el.id = "chatTransportIndicator";
      el.style.cssText =
        "position:fixed;bottom:4px;right:4px;z-index:9999;" +
        "padding:2px 8px;border-radius:4px;font-size:10px;" +
        "font-family:monospace;opacity:0.85;pointer-events:none;";
      document.body.appendChild(el);
    }
    var colors = { poll: "#22c55e", sse: "#22c55e", native: "#eab308", error: "#ef4444", none: "#6b7280" };
    el.style.background = colors[mode] || colors.none;
    el.style.color = "#fff";
    el.textContent = "chat:" + mode + (detail ? " " + detail : "");
    if (transportIndicatorId) clearTimeout(transportIndicatorId);
    transportIndicatorId = setTimeout(function () {
      var e = document.getElementById("chatTransportIndicator");
      if (e) e.style.opacity = "0.3";
    }, 5000);
  }

  function routeChatEvent(evt) {
    var cb = CHAT_EVENT_HANDLERS[evt && evt.event];
    if (!cb) return;
    try {
      cb(evt.data);
    } catch (e) {
      console.error("[chat] erro no handler " + (evt && evt.event) + ":", e);
    }
  }

  // ── Polling via bindings Wails (transporte IPC confiável) ──
  // Alternativa ao EventSource quando o WebView2 bloqueia SSE por mixed-content.
  // PollChatEvents() retorna um array JSON de eventos pendentes no backend.
  startPollingLoop = function () {
    if (pollTimerId) return; // já rodando
    console.log("[chat] polling via PollChatEvents iniciado (intervalo=" + POLL_INTERVAL_MS + "ms)");
    updateTransportIndicator("poll", POLL_INTERVAL_MS + "ms");

    function poll() {
      if (!chatSending) {
        // Stream não está mais ativo — para o polling
        stopPollingLoop();
        return;
      }

      appApi()
        .PollChatEvents()
        .then(function (result) {
          var events;
          if (typeof result === "string") {
            try { events = JSON.parse(result); } catch (_) { events = []; }
          } else if (Array.isArray(result)) {
            events = result;
          } else {
            events = [];
          }

          var foundTerminal = false;
          for (var i = 0; i < events.length; i++) {
            var raw = events[i];
            var evt;
            if (typeof raw === "string") {
              try { evt = JSON.parse(raw); } catch (_) { continue; }
            } else {
              evt = raw;
            }
            if (!evt || !evt.event) continue;
            routeChatEvent(evt);
            if (STREAM_TERMINAL_EVENTS[evt.event]) {
              foundTerminal = true;
            }
          }

          if (foundTerminal) {
            stopPollingLoop();
            return;
          }

          // Agenda próxima iteração
          pollTimerId = setTimeout(poll, POLL_INTERVAL_MS);
        })
        .catch(function (err) {
          console.warn("[chat] PollChatEvents falhou:", err);
          // Agenda próxima iteração mesmo com erro (pode ser transitório)
          pollTimerId = setTimeout(poll, POLL_INTERVAL_MS);
        });
    }

    poll();
  }

  stopPollingLoop = function () {
    if (pollTimerId) {
      clearTimeout(pollTimerId);
      pollTimerId = null;
    }
  };

  // Registra os listeners nativos do Wails (fallback quando polling não está disponível).
  function registerNativeListeners() {
    if (nativeListenersRegistered) return;
    if (window.wails && typeof window.wails.on === "function") {
      console.log("[chat] registrando listeners nativos Wails (fallback via Events.On)");
      updateTransportIndicator("native", "Events.On");
      Object.keys(CHAT_EVENT_HANDLERS).forEach(function (name) {
        window.wails.on(name, CHAT_EVENT_HANDLERS[name]);
      });
      nativeListenersRegistered = true;
    } else {
      console.error("[chat] fallback nativo indisponível: window.wails.on não é uma função");
      updateTransportIndicator("error", "no-transport");
    }
  }

  function doRegister() {
    var isNative = !!window.__wailsV3Bridge;

    // No navegador, o próprio debug-http-bridge já conecta ao SSE via window.wails.on.
    if (!isNative) {
      registerNativeListeners();
      return;
    }

    // Runtime nativo (WebView2): usar POLLING via bindings Wails em vez de
    // EventSource (bloqueado por mixed-content). O polling é iniciado sob
    // demanda em sendChatMessage / sendChatMessageWithA2uiAction.
    if (appApi() && typeof appApi().PollChatEvents === "function") {
      console.log("[chat] transporte: polling via PollChatEvents (IPC nativo)");
      updateTransportIndicator("poll", "ready");
    } else {
      console.warn("[chat] PollChatEvents indisponível; usando listeners nativos como fallback");
      registerNativeListeners();
    }
  }

  // Aguarda o bridge Wails v3 estar disponível antes de registrar.
  // O wails-bridge.js é um script type=module (deferred), que pode não ter
  // executado ainda quando app-chat.js roda (carregado como script clássico
  // via bootstrap-partials.js). Polling com timeout de 5s para evitar ficar
  // preso indefinidamente.
  function waitForBridgeThenRegister() {
    var waited = 0;
    var MAX_WAIT_MS = 5000;
    var POLL_MS = 50;

    function check() {
      if (window.__wailsV3Bridge || (window.wails && window.wails.Events)) {
        doRegister();
        return;
      }
      waited += POLL_MS;
      if (waited >= MAX_WAIT_MS) {
        console.warn("[chat] timeout aguardando bridge Wails v3; registrando assim mesmo");
        doRegister();
        return;
      }
      setTimeout(check, POLL_MS);
    }

    check();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", waitForBridgeThenRegister);
  } else {
    waitForBridgeThenRegister();
  }
})();

function scrollChatToBottom() {
  if (chatMessagesEl) chatMessagesEl.scrollTop = chatMessagesEl.scrollHeight;
  if (chatViewEl) chatViewEl.scrollTop = chatViewEl.scrollHeight;
}

function scheduleChatScrollToBottom() {
  if (document.hidden || window.__discoveryUISuspended) return;
  // Run after current and next paint to keep bottom lock even after dynamic layout updates.
  scrollChatToBottom();
  requestAnimationFrame(function () {
    scrollChatToBottom();
    requestAnimationFrame(scrollChatToBottom);
  });
}

function extractChatActionOptions(content) {
  var text = String(content || "");
  if (!text) return [];

  var lines = text.split(/\r?\n/);
  var options = [];
  var seen = new Set();

  function pushOption(raw) {
    var clean = String(raw || "")
      .replace(/^[-*]\s+/, "")
      .replace(/^\d+\.\s+/, "")
      .trim();
    if (!clean) return;

    var key = clean.toLowerCase();
    if (seen.has(key)) return;
    seen.add(key);

    var label = clean.length > 120 ? clean.slice(0, 117) + "..." : clean;
    // Remove markdown markers from the action value so the sent text is clean.
    var value = clean.replace(/[*_`~]/g, "").trim();
    options.push({ label: label, value: value || clean });
  }

  for (var i = 0; i < lines.length; i += 1) {
    var line = String(lines[i] || "").trim();
    if (/^[-*]\s+/.test(line) || /^\d+\.\s+/.test(line)) {
      pushOption(line);
    }
  }

  // Keep UI concise even if the assistant listed many alternatives.
  return options.slice(0, 6);
}

function appendChatQuickActions(containerEl, actionOptions) {
  if (!containerEl || !chatMessagesEl) return;
  var actions = document.createElement("div");
  actions.className = "chat-msg-actions";

  var options =
    actionOptions && actionOptions.length
      ? actionOptions
      : [
          { label: "Confirmar", value: "Confirmo. Pode prosseguir." },
          { label: "Cancelar", value: "Cancelar. Nao execute nenhuma acao." },
          { label: "Sim", value: "Sim, pode executar." },
          { label: "Nao", value: "Nao, por enquanto nao." },
        ];

  options.forEach(function (item) {
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn subtle btn-xs";
    btn.innerHTML = formatInlineChatMarkdown(item.label);
    btn.addEventListener("click", function () {
      if (chatSending || !chatInputEl) return;
      chatInputEl.value = item.value;
      sendChatMessage();
    });
    actions.appendChild(btn);
  });

  containerEl.appendChild(actions);
}

function parseChatProgressLine(line) {
  var raw = String(line || "");
  if (!raw.startsWith("[chat] ")) return "";
  var text = raw.replace(/^\[chat\]\s*/, "");

  if (text.indexOf("mensagem recebida") >= 0)
    return "Entendendo sua solicitacao...";
  if (text.indexOf("ferramentas disponiveis") >= 0)
    return "Preparando ferramentas...";
  if (text.indexOf("rodada de ferramentas") >= 0)
    return "Analisando e planejando a melhor acao...";
  if (text.indexOf("chamando ferramenta:") >= 0) {
    var name = text.split("chamando ferramenta:")[1] || "";
    name = name.trim();
    return name ? "Executando: " + name + "..." : "Executando ferramenta...";
  }
  if (text.indexOf("executada com sucesso") >= 0)
    return "Acao concluida com sucesso, preparando resposta...";
  if (text.indexOf("retornou erro") >= 0)
    return "Houve um erro na acao. Ajustando resposta...";
  if (text.indexOf("resposta final") >= 0) return "Finalizando resposta...";
  return "";
}

function stopThinkingStatusUpdates() {
  if (chatThinkingPollId) {
    clearInterval(chatThinkingPollId);
    chatThinkingPollId = null;
  }
}

function handleChatUISuspend() {
  stopThinkingStatusUpdates();
}

document.addEventListener("ui:suspend", handleChatUISuspend);

function startThinkingStatusUpdates(thinkingEl) {
  stopThinkingStatusUpdates();
  if (!thinkingEl) return;

  var busy = false;
  var lastStatus = "";
  chatThinkingPollId = setInterval(async function () {
    if (busy) return;
    busy = true;
    try {
      var lines = await appApi().GetLogs();
      var status = "";
      for (var i = (lines || []).length - 1; i >= 0; i -= 1) {
        status = parseChatProgressLine(lines[i]);
        if (status) break;
      }
      if (status && status !== lastStatus && thinkingEl.isConnected) {
        thinkingEl.textContent = status;
        lastStatus = status;
        scheduleChatScrollToBottom();
      }
    } catch (_) {
      // Keep default thinking text when log polling fails.
    } finally {
      busy = false;
    }
  }, 900);
}

function formatInlineChatMarkdown(text) {
  var escaped = escapeHtml(String(text || ""));
  var codeTokens = [];

  escaped = escaped.replace(/`([^`\n]+)`/g, function (_, code) {
    var token = "\x01CHAT_CODE_" + codeTokens.length + "\x01";
    codeTokens.push("<code>" + code + "</code>");
    return token;
  });

  escaped = escaped.replace(
    /\[([^\]]+)\]\(((?:https?:\/\/|(?:discovery|app):\/\/)[^\s)]+)\)/g,
    function (_, label, url) {
      var safeLabel = String(label || "").trim();
      if (/^(?:discovery|app):\/\//i.test(url)) {
        var parts = safeLabel
          .split("|")
          .map(function (p) {
            return p.trim();
          })
          .filter(Boolean);
        if (parts.length >= 2) {
          var title = parts[0];
          var subtitle = parts[1];
          var meta = parts.slice(2).join(" - ");
          return (
            '<a href="#" class="chat-internal-link chat-internal-card" data-internal-url="' +
            escapeHtmlAttr(url) +
            '">' +
            '<span class="chat-internal-card-title">' +
            title +
            "</span>" +
            '<span class="chat-internal-card-subtitle">' +
            subtitle +
            "</span>" +
            (meta
              ? '<span class="chat-internal-card-meta">' + meta + "</span>"
              : "") +
            "</a>"
          );
        }
        return (
          '<a href="#" class="chat-internal-link" data-internal-url="' +
          escapeHtmlAttr(url) +
          '">' +
          safeLabel +
          "</a>"
        );
      }
      return (
        '<a href="' +
        url +
        '" target="_blank" rel="noopener noreferrer">' +
        safeLabel +
        "</a>"
      );
    },
  );

  escaped = escaped
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/__([^_]+)__/g, "<strong>$1</strong>")
    .replace(/\*([^*\n]+)\*/g, "<em>$1</em>")
    .replace(/_([^_\n]+)_/g, "<em>$1</em>");

  for (var i = 0; i < codeTokens.length; i += 1) {
    escaped = escaped.replace("\x01CHAT_CODE_" + i + "\x01", codeTokens[i]);
  }

  return escaped;
}

function parseInternalAppRoute(url) {
  try {
    var parsed = new URL(String(url || ""));
    var scheme = (parsed.protocol || "").replace(":", "").toLowerCase();
    if (scheme !== "discovery" && scheme !== "app") return null;

    var segments = [];
    if (parsed.hostname) segments.push(parsed.hostname.toLowerCase());
    if (parsed.pathname) {
      segments = segments.concat(
        parsed.pathname
          .split("/")
          .filter(Boolean)
          .map(function (s) {
            return s.toLowerCase();
          }),
      );
    }

    var ticketId =
      parsed.searchParams.get("ticketId") ||
      parsed.searchParams.get("id") ||
      "";
    if (
      !ticketId &&
      segments[0] === "support" &&
      segments[1] === "ticket" &&
      segments[2]
    ) {
      ticketId = segments[2];
    }

    var tabBySegment;
    switch (segments[0]) {
      case "support":
      case "tickets":
        tabBySegment = "support";
        break;
      case "store":
        tabBySegment = "store";
        break;
      case "updates":
        tabBySegment = "updates";
        break;
      case "inventory":
        tabBySegment = "inventory";
        break;
      case "logs":
        tabBySegment = "logs";
        break;
      case "chat":
        tabBySegment = "chat";
        break;
      case "knowledge":
        tabBySegment = "knowledge";
        break;
      case "debug":
        tabBySegment = "debug";
        break;
      default:
        tabBySegment = undefined;
    }

    if (!tabBySegment) return null;
    return { tab: tabBySegment, ticketId: ticketId };
  } catch (_) {
    return null;
  }
}

async function navigateInternalAppRoute(url) {
  var route = parseInternalAppRoute(url);
  if (!route) {
    showToast(
      translate("chat.invalidInternalLink", { url: String(url || "") }),
      "error",
    );
    return;
  }

  setActiveTab(route.tab);

  if (route.tab === "support") {
    await loadSupportTickets();
    if (route.ticketId) {
      try {
        var ticket = await appApi().GetSupportTicketDetails(route.ticketId);
        showTicketDetail(ticket);
      } catch (err) {
        showToast(
          translate("chat.openTicketError", { error: String(err) }),
          "error",
        );
      }
    }
  }
}

function bindInternalChatLinks(containerEl) {
  if (!containerEl) return;
  var links = containerEl.querySelectorAll(
    "a.chat-internal-link[data-internal-url]",
  );
  links.forEach(function (link) {
    if (link.dataset.boundInternalClick === "1") return;
    link.dataset.boundInternalClick = "1";
    link.addEventListener("click", function (e) {
      e.preventDefault();
      var internalURL = link.getAttribute("data-internal-url") || "";
      navigateInternalAppRoute(internalURL);
    });
  });
}

function stripRawToolCalls(content) {
  // Remove XML-like tool call blocks that the server LLM may emit as raw text
  // instead of executing the tool. Matches both self-closing and paired tags.
  var s = String(content || "");
  // Self-closing: <toolname {"k":"v"} />
  s = s.replace(/<(\w+)\s+(\{[^}]*\})\s*\/>/g, "");
  // Paired: <toolname>{"k":"v"}</toolname>
  s = s.replace(/<(\w+)\s*>\s*(\{[^}]*\})\s*<\/\1>/g, "");
  // Self-closing without content: <toolname/>
  s = s.replace(/<(\w+)\s*\/>/g, "");
  return s;
}

function renderAssistantMarkdown(content) {
  content = stripRawToolCalls(content);
  var lines = String(content || "")
    .replace(/\r\n/g, "\n")
    .split("\n");
  var html = ['<div class="md-content">'];
  var inCode = false;
  var codeLang = "";
  var codeLines = [];
  var inUl = false;
  var inOl = false;

  function closeLists() {
    if (inUl) {
      html.push("</ul>");
      inUl = false;
    }
    if (inOl) {
      html.push("</ol>");
      inOl = false;
    }
  }

  function flushCodeBlock() {
    var langClass = codeLang
      ? ' class="lang-' + escapeHtmlAttr(codeLang) + '"'
      : "";
    html.push(
      '<pre class="chat-code"><code' +
        langClass +
        ">" +
        escapeHtml(codeLines.join("\n")) +
        "</code></pre>",
    );
    inCode = false;
    codeLang = "";
    codeLines = [];
  }

  function isTableRow(s) {
    return /^\|([^|\r\n]+\|)+\s*$/.test(s.trim());
  }

  function isSeparatorRow(s) {
    return /^\|(\s*:?-{2,}:?\s*\|)+\s*$/.test(s.trim());
  }

  function parseTableCells(s) {
    return s
      .trim()
      .replace(/^\|/, "")
      .replace(/\|\s*$/, "")
      .split("|")
      .map(function (c) {
        return c.trim();
      });
  }

  function parseTableAlign(s) {
    return parseTableCells(s).map(function (c) {
      if (/^:-+:$/.test(c)) return "center";
      if (/-+:$/.test(c)) return "right";
      return "left";
    });
  }

  function renderTable(startIdx) {
    var headerCells = parseTableCells(lines[startIdx]);
    var aligns = parseTableAlign(lines[startIdx + 1]);
    var out =
      '<div class="chat-table-wrap"><table class="chat-table"><thead><tr>';
    for (var c = 0; c < headerCells.length; c += 1) {
      out +=
        '<th style="text-align:' +
        (aligns[c] || "left") +
        '">' +
        formatInlineChatMarkdown(headerCells[c]) +
        "</th>";
    }
    out += "</tr></thead><tbody>";
    var r = startIdx + 2;
    while (r < lines.length && isTableRow(lines[r])) {
      var cells = parseTableCells(lines[r]);
      out += "<tr>";
      for (var c2 = 0; c2 < headerCells.length; c2 += 1) {
        out +=
          '<td style="text-align:' +
          (aligns[c2] || "left") +
          '">' +
          formatInlineChatMarkdown(cells[c2] || "") +
          "</td>";
      }
      out += "</tr>";
      r += 1;
    }
    out += "</tbody></table></div>";
    return { html: out, nextIndex: r };
  }

  for (var i = 0; i < lines.length; i += 1) {
    var raw = lines[i];

    if (inCode) {
      if (/^```/.test(raw.trim())) {
        flushCodeBlock();
      } else {
        codeLines.push(raw);
      }
      continue;
    }

    var fence = raw.trim().match(/^```([a-zA-Z0-9_-]+)?\s*$/);
    if (fence) {
      closeLists();
      inCode = true;
      codeLang = fence[1] || "";
      continue;
    }

    var line = raw.trim();
    if (!line) {
      closeLists();
      continue;
    }

    if (
      isTableRow(line) &&
      i + 1 < lines.length &&
      isSeparatorRow(lines[i + 1].trim())
    ) {
      closeLists();
      var tbl = renderTable(i);
      html.push(tbl.html);
      i = tbl.nextIndex - 1;
      continue;
    }

    var heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      closeLists();
      var level = heading[1].length;
      html.push(
        "<h" +
          level +
          ">" +
          formatInlineChatMarkdown(heading[2]) +
          "</h" +
          level +
          ">",
      );
      continue;
    }

    if (/^>\s+/.test(line)) {
      closeLists();
      html.push(
        "<blockquote>" +
          formatInlineChatMarkdown(line.replace(/^>\s+/, "")) +
          "</blockquote>",
      );
      continue;
    }

    if (/^[-*]\s+/.test(line)) {
      if (inOl) {
        html.push("</ol>");
        inOl = false;
      }
      if (!inUl) {
        html.push("<ul>");
        inUl = true;
      }
      html.push(
        "<li>" +
          formatInlineChatMarkdown(line.replace(/^[-*]\s+/, "")) +
          "</li>",
      );
      continue;
    }

    if (/^\d+\.\s+/.test(line)) {
      if (inUl) {
        html.push("</ul>");
        inUl = false;
      }
      if (!inOl) {
        html.push("<ol>");
        inOl = true;
      }
      html.push(
        "<li>" +
          formatInlineChatMarkdown(line.replace(/^\d+\.\s+/, "")) +
          "</li>",
      );
      continue;
    }

    closeLists();
    html.push("<p>" + formatInlineChatMarkdown(line) + "</p>");
  }

  if (inCode) {
    flushCodeBlock();
  }
  closeLists();

  html.push("</div>");
  return html.join("");
}

function addChatMessage(role, content) {
  if (!chatMessagesEl) return;
  var div = document.createElement("div");
  div.className = "chat-msg " + role;

  if (role === "assistant") {
    div.innerHTML = renderAssistantMarkdown(content);
    syncColorMode();
    bindInternalChatLinks(div);
  } else {
    div.textContent = content;
  }

  if (role === "assistant") {
    var dynamicActions = extractChatActionOptions(content);
    if (dynamicActions.length > 0) {
      appendChatQuickActions(div, dynamicActions);
    }
  }

  chatMessagesEl.appendChild(div);
  scheduleChatScrollToBottom();
  return div;
}

function removeChatThinking() {
  if (!chatMessagesEl) return;
  stopThinkingStatusUpdates();
  var thinking = chatMessagesEl.querySelector(".chat-msg.thinking");
  if (thinking) {
    thinking.remove();
    scheduleChatScrollToBottom();
  }
}

async function sendChatMessage() {
  if (chatSending || !chatInputEl) return;
  var text = chatInputEl.value.trim();
  if (!text) return;

  chatInputEl.value = "";
  addChatMessage("user", text);

  chatStopRequested = false;
  setChatBusy(true);
  resetA2uiTokenFilter();

  // Create the streaming bubble immediately.
  streamingRawContent = "";
  streamingRafPending = false;
  streamingBubble = document.createElement("div");
  streamingBubble.className = "chat-msg assistant streaming";

  var thinkingEl = document.createElement("div");
  thinkingEl.className = "stream-thinking";
  thinkingEl.textContent = translate("chat.thinking");
  streamingBubble.appendChild(thinkingEl);

  var cursorEl = document.createElement("span");
  cursorEl.className = "stream-cursor";
  streamingBubble.appendChild(cursorEl);

  if (chatMessagesEl) chatMessagesEl.appendChild(streamingBubble);
  scheduleChatScrollToBottom();

  // Timer de segurança: garante que "Pensando..." não fique preso se o
  // evento chat:done nunca chegar (erro de rede, goroutine perdida, etc.).
  armChatStreamTimeout();

  try {
    // StartChatStream returns immediately; response arrives via events.
    appApi()
      .StartChatStream(text)
      .then(function () {
        // Inicia o polling loop APENAS no runtime nativo (WebView2).
        // No navegador, o debug-http-bridge já conecta ao SSE via window.wails.on.
        if (window.__wailsV3Bridge) {
          startPollingLoop();
        }
      })
      .catch(function (err) {
        onStreamError(String(err));
      });
  } catch (err) {
    onStreamError(String(err));
  }
}

async function loadChatConfig() {
  try {
    var cfg = await appApi().GetChatConfig();
    if (chatEndpointEl) chatEndpointEl.value = cfg.endpoint || "";
    if (chatModelEl) chatModelEl.value = cfg.model || "";
    if (chatMaxTokensEl) {
      var maxTokens = Number(cfg.maxTokens || 0);
      chatMaxTokensEl.value = maxTokens > 0 ? String(maxTokens) : "";
    }
    if (chatSystemPromptEl) chatSystemPromptEl.value = cfg.systemPrompt || "";
    // Don't set API key - it's masked
  } catch (_) {}
}

async function saveChatConfig() {
  var endpoint = chatEndpointEl ? chatEndpointEl.value.trim() : "";
  var apiKey = chatApiKeyEl ? chatApiKeyEl.value.trim() : "";
  var model = chatModelEl ? chatModelEl.value.trim() : "";
  var maxTokensRaw = chatMaxTokensEl ? chatMaxTokensEl.value.trim() : "";
  var systemPrompt = chatSystemPromptEl ? chatSystemPromptEl.value.trim() : "";
  var maxTokens = 0;

  if (maxTokensRaw) {
    maxTokens = Number(maxTokensRaw);
    if (!Number.isFinite(maxTokens) || maxTokens < 0) {
      showFeedback(translate("chat.maxTokensValidation"), true);
      return;
    }
    maxTokens = Math.floor(maxTokens);
  }

  try {
    await appApi().SetChatConfig({
      endpoint: endpoint,
      apiKey: apiKey,
      model: model,
      systemPrompt: systemPrompt,
      maxTokens: maxTokens,
    });
    showFeedback(translate("chat.configSavedSuccess"));
    if (chatConfigPanel) chatConfigPanel.classList.add("hidden");
  } catch (err) {
    showFeedback(
      translate("chat.configSaveError", { error: String(err) }),
      true,
    );
  }
}

async function testChatConfig() {
  var endpoint = chatEndpointEl ? chatEndpointEl.value.trim() : "";
  var apiKey = chatApiKeyEl ? chatApiKeyEl.value.trim() : "";
  var model = chatModelEl ? chatModelEl.value.trim() : "";
  var maxTokensRaw = chatMaxTokensEl ? chatMaxTokensEl.value.trim() : "";
  var systemPrompt = chatSystemPromptEl ? chatSystemPromptEl.value.trim() : "";
  var maxTokens = 0;

  if (maxTokensRaw) {
    maxTokens = Number(maxTokensRaw);
    if (!Number.isFinite(maxTokens) || maxTokens < 0) {
      showFeedback(translate("chat.maxTokensValidation"), true);
      return;
    }
    maxTokens = Math.floor(maxTokens);
  }

  if (chatTestConfigBtn) chatTestConfigBtn.disabled = true;
  try {
    showFeedback(translate("chat.configTesting"));
    var reply = await appApi().TestChatConfig({
      endpoint: endpoint,
      apiKey: apiKey,
      model: model,
      systemPrompt: systemPrompt,
      maxTokens: maxTokens,
    });
    var normalized = String(reply || "").trim();
    showFeedback(
      translate("chat.configTestSuccess", {
        suffix: normalized ? ": " + normalized : "",
      }),
    );
  } catch (err) {
    showFeedback(
      translate("chat.configTestFailure", { error: String(err) }),
      true,
    );
  } finally {
    if (chatTestConfigBtn) chatTestConfigBtn.disabled = false;
  }
}

async function loadChatTools() {
  if (!chatToolsList) return;
  try {
    var tools = await appApi().GetAvailableTools();
    chatToolsList.innerHTML = (tools || [])
      .map(function (t) {
        return (
          '<span class="chat-tool-badge" title="' +
          escapeHtml(t.description) +
          '">' +
          escapeHtml(t.name) +
          "</span>"
        );
      })
      .join("");
  } catch (_) {
    chatToolsList.innerHTML =
      '<span class="meta">' +
      escapeHtml(translate("chat.toolsLoadError")) +
      "</span>";
  }
}

async function loadChatDebugLogs() {
  if (!chatLogsOutput) return;
  try {
    var lines = await appApi().GetLogs();
    var chatLines = (lines || []).filter(function (line) {
      return String(line).startsWith("[chat]");
    });
    chatLogsOutput.textContent = chatLines.length
      ? chatLines.join("\n")
      : translate("chat.noLogsYet");
    chatLogsOutput.scrollTop = chatLogsOutput.scrollHeight;
  } catch (err) {
    chatLogsOutput.textContent = translate("chat.logsLoadError", {
      error: String(err),
    });
  }
}

async function loadChatMemories() {
  if (!chatMemoriesList) return;
  try {
    var notes = await appApi().GetLocalMemories();
    if (!notes || !notes.length) {
      chatMemoriesList.innerHTML =
        '<div class="meta">' +
        escapeHtml(translate("chat.noMemoryFound")) +
        "</div>";
      return;
    }

    var html = notes
      .map(function (n) {
        var created = n.createdAt ? formatDate(n.createdAt, "") : "";
        var updated = n.updatedAt ? formatDate(n.updatedAt, "") : "";
        return (
          '<div class="chat-memory-item">' +
          '<div class="chat-memory-meta"><span>' +
          escapeHtml(created) +
          "</span>" +
          (updated && updated !== created
            ? " <span>" +
              escapeHtml(translate("chat.updatedAt", { date: updated })) +
              "</span>"
            : "") +
          "</div>" +
          '<div class="chat-memory-content">' +
          escapeHtml(n.content) +
          "</div>" +
          '<div class="chat-memory-actions">' +
          '<button class="btn danger chat-memory-delete-btn" data-id="' +
          escapeHtml(String(n.id)) +
          '">' +
          escapeHtml(translate("action.delete")) +
          "</button>" +
          "</div>" +
          "</div>"
        );
      })
      .join("");

    chatMemoriesList.innerHTML = html;

    // Attach delete handlers
    var deleteButtons = chatMemoriesList.querySelectorAll(
      ".chat-memory-delete-btn",
    );
    deleteButtons.forEach(function (btn) {
      btn.addEventListener("click", function () {
        var id = parseInt(btn.getAttribute("data-id"), 10);
        if (!Number.isFinite(id)) return;
        deleteChatMemory(id);
      });
    });
  } catch (err) {
    chatMemoriesList.innerHTML =
      '<div class="meta">' +
      escapeHtml(translate("chat.memoriesLoadError", { error: String(err) })) +
      "</div>";
  }
}

function openChatMemoriesModal() {
  if (!chatMemoriesModal) return;
  chatMemoriesModal.classList.remove("hidden");
  chatMemoriesModal.setAttribute("aria-hidden", "false");
  loadChatMemories();
}

function closeChatMemoriesModal() {
  if (!chatMemoriesModal) return;
  chatMemoriesModal.classList.add("hidden");
  chatMemoriesModal.setAttribute("aria-hidden", "true");
}

function deleteChatMemory(id) {
  if (!chatMemoriesList) return;
  appApi()
    .DeleteLocalMemory(id)
    .then(function () {
      loadChatMemories();
    })
    .catch(function (err) {
      showFeedback(
        translate("chat.memoryDeleteError", { error: String(err) }),
        true,
      );
    });
}

function openChatLogsModal() {
  if (!chatLogsModal) return;
  chatLogsModal.classList.remove("hidden");
  chatLogsModal.setAttribute("aria-hidden", "false");
  loadChatDebugLogs();
}

function closeChatLogsModal() {
  if (!chatLogsModal) return;
  chatLogsModal.classList.add("hidden");
  chatLogsModal.setAttribute("aria-hidden", "true");
}

function initChat() {
  if (chatSendBtn) {
    chatSendBtn.addEventListener("click", sendChatMessage);
  }
  if (chatStopBtn) {
    chatStopBtn.addEventListener("click", requestStopChatStream);
  }
  if (chatInputEl) {
    chatInputEl.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        sendChatMessage();
      }
    });
  }
  if (chatConfigBtn && chatConfigPanel) {
    chatConfigBtn.addEventListener("click", function () {
      chatConfigPanel.classList.toggle("hidden");
      if (chatToolsPanel) chatToolsPanel.classList.add("hidden");
      loadChatConfig();
    });
  }
  if (chatToolsBtn && chatToolsPanel) {
    chatToolsBtn.addEventListener("click", function () {
      chatToolsPanel.classList.toggle("hidden");
      if (chatConfigPanel) chatConfigPanel.classList.add("hidden");
      loadChatTools();
    });
  }
  if (chatLogsBtn) {
    chatLogsBtn.addEventListener("click", openChatLogsModal);
  }
  if (chatLogsCloseBtn) {
    chatLogsCloseBtn.addEventListener("click", closeChatLogsModal);
  }
  if (chatLogsRefreshBtn) {
    chatLogsRefreshBtn.addEventListener("click", loadChatDebugLogs);
  }
  if (chatLogsModal) {
    chatLogsModal.addEventListener("click", function (e) {
      if (e.target === chatLogsModal) closeChatLogsModal();
    });
  }

  if (chatMemoriesBtn) {
    chatMemoriesBtn.classList.toggle("hidden", !isDebugRuntimeMode());
    chatMemoriesBtn.addEventListener("click", openChatMemoriesModal);
  }
  if (chatMemoriesCloseBtn) {
    chatMemoriesCloseBtn.addEventListener("click", closeChatMemoriesModal);
  }
  if (chatMemoriesRefreshBtn) {
    chatMemoriesRefreshBtn.addEventListener("click", loadChatMemories);
  }
  if (chatMemoriesModal) {
    chatMemoriesModal.addEventListener("click", function (e) {
      if (e.target === chatMemoriesModal) closeChatMemoriesModal();
    });
  }

  if (chatClearBtn) {
    chatClearBtn.addEventListener("click", async function () {
      try {
        await appApi().ClearChatHistory();
        if (chatMessagesEl) chatMessagesEl.innerHTML = "";
        clearA2uiSurface();
        showFeedback(translate("chat.cleared"));
      } catch (err) {
        showFeedback(
          translate("chat.clearError", { error: String(err) }),
          true,
        );
      }
    });
  }
  if (chatSaveConfigBtn) {
    chatSaveConfigBtn.addEventListener("click", saveChatConfig);
  }
  if (chatTestConfigBtn) {
    chatTestConfigBtn.addEventListener("click", testChatConfig);
  }
}
