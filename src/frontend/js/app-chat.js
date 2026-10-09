"use strict";

var chatSending = false;
var chatStopRequested = false;
// Timer de segurança: se o stream não terminar em X segundos, a UI para de
// esperar passivamente e RECONCILIA com o backend (HasActiveChatStream) —
// antes ela se liberava na hora (60s) enquanto o core continuava processando,
// e o próximo send batia no TryLock do backend ("já existe uma resposta em
// andamento"). Turnos com 43 tools e múltiplos rounds passam de 60s com
// frequência; 120s dá folga antes do primeiro aviso, e a reconciliação em
// seguida cobre evento terminal perdido sem derrubar turnos longos.
var chatStreamTimeoutId = null;
var CHAT_STREAM_TIMEOUT_MS = 120000; // 120s até o primeiro aviso
var CHAT_STREAM_PROBE_MS = 30000;    // sonda de reconciliação subsequente

// Streaming state
var streamingBubble = null;
var streamingRawContent = "";
var streamingRafPending = false;
// Último round do agent loop visto no turno (usado para inserir a quebra entre
// as mensagens de rounds diferentes). Reson no dispatch de cada mensagem.
var streamingLastRound = 0;

// Funções de polling do chat. São DECLARADAS aqui (escopo global) e
// ATRIBUÍDAS dentro do IIFE registerChatStreamEvents, pois dependem do estado
// interno daquele IIFE (pollTimerId, POLL_INTERVAL_MS, routeChatEvent, etc.).
// Os handlers globais (onStreamDone/onStreamError/onStreamStopped/sendChatMessage)
// precisam chamá-las — se ficassem só dentro do IIFE, o "use strict" lançaria
// ReferenceError e o polling nunca iniciaria (bug corrigido em 2026-08-27).
var startPollingLoop;
var stopPollingLoop;

// Estado do filtro de blocos A2UI e vazamentos de tool calls nos tokens visíveis.
// O servidor emite os tokens em tempo real e só extrai o bloco ```a2ui no
// final do stream. Para o usuário não ver o JSON cru, filtramos o conteúdo
// entre ```a2ui e ``` conforme os tokens chegam (de forma incremental).
// Além disso, o LLM às vezes emite tool calls como TEXTO (blocos ```json com
// invokes, marcação DSML <｜DSML｜tool_invokes>...) — esses vazamentos também
// são filtrados durante o streaming.
//
// Estados da máquina:
//   ""        — texto normal (visível)
//   "a2ui"    — dentro de bloco ```a2ui ... ``` (descartado)
//   "json"    — dentro de bloco ```json ... ``` (retido até saber se é vazamento)
//   "dsml"    — dentro de marcação DSML (descartado)
//   "think"   — dentro de bloco de raciocínio <think>...</think> e variantes
//               (descartado — planejamento interno do modelo nunca é exibido)
var a2uiTokenFilter = {
  state: "",
  buffer: "",
  jsonBody: "",
};

// Rastreio da interface A2UI do turno. O servidor DESCARTA interfaces inválidas
// (validação contra o catálogo do renderer) — nesse caso nenhum evento
// "chat:a2ui" chega e o texto, que costuma anunciar um card, ficaria sem
// explicação. a2uiBlockSeen = o filtro viu um bloco ```a2ui; a2uiRendered =
// algum evento chat:a2ui foi recebido (mesmo que a renderização tenha falhado,
// o fallback visual já explica).
var a2uiBlockSeen = false;
var a2uiRendered = false;

// Aberturas reconhecidas (prefixo mais longo primeiro).
var LEAK_OPEN_A2UI = "```a2ui";
var LEAK_OPEN_JSON = "```json";
var LEAK_OPEN_FENCE = "```";
var DSML_OPEN_RE = /<[｜|]DSML[｜|][a-z_]*>/;
var DSML_CLOSE_RE = /<\/[｜|]DSML[｜|][a-z_]*>/;
// Padrões de vazamento dentro de blocos ```json (usados pelo filtro e pela
// sanitização final).
var INVOKE_ARRAY_RE = /^\s*\[\s*\{\s*"name"\s*:/;
var A2UI_ACTION_RE = /^\s*\{\s*"version"\s*:\s*"a2ui"/;
// 5. Blocos de raciocínio (<think>/<thinking>/<thought>/<reasoning>) que
//    alguns modelos emitem no MEIO do conteúdo — planejamento interno que
//    nunca deve aparecer na conversa (vazamento visto em produção em 20/09:
//    planejamento em inglês misturado à resposta, fragmentado por rounds).
var THINK_OPEN_RE = /<(?:think|thinking|thought|reasoning)>/i;
var THINK_CLOSE_RE = /<\/(?:think|thinking|thought|reasoning)>/i;
var THINK_PAIR_RE = /<(?:think|thinking|thought|reasoning)>[\s\S]*?<\/(?:think|thinking|thought|reasoning)>/gi;
var THINK_TAG_RE = /<\/?(?:think|thinking|thought|reasoning)>/gi;

// Filtra um fragmento de token, removendo blocos ```a2ui ... ```, blocos
// ```json que contenham invokes/A2UI (vazamentos de tool call) e marcação
// DSML. Retorna o texto "limpo" que deve ser exibido ao usuário.
//
// Estratégia: mantém um buffer pequeno (sufixo potencial de um marcador) e só
// emite texto quando temos certeza de que ele não faz parte de um vazamento.
// Blocos ```json são retidos até o fechamento: se o corpo for um array de
// invokes ou ação A2UI, é descartado; caso contrário, é liberado como texto
// legítimo (o usuário vê o bloco json aparecer de uma vez no fim).
function filterA2uiTokens(token) {
  var f = a2uiTokenFilter;
  f.buffer += token;

  var out = "";
  var progress = true;
  while (progress) {
    progress = false;
    if (f.state === "") {
      // Procura a abertura de bloco a2ui/json/fence ou tag DSML.
      var openA2ui = f.buffer.indexOf(LEAK_OPEN_A2UI);
      var openJson = f.buffer.indexOf(LEAK_OPEN_JSON);
      var openDsml = f.buffer.search(DSML_OPEN_RE);
      var openThink = f.buffer.search(THINK_OPEN_RE);
      // Escolhe a abertura mais próxima do início.
      var candidates = [];
      if (openA2ui !== -1) candidates.push({ idx: openA2ui, kind: "a2ui", len: LEAK_OPEN_A2UI.length });
      if (openJson !== -1) candidates.push({ idx: openJson, kind: "json", len: LEAK_OPEN_JSON.length });
      if (openDsml !== -1) candidates.push({ idx: openDsml, kind: "dsml", len: f.buffer.match(DSML_OPEN_RE)[0].length });
      if (openThink !== -1) {
        var thinkMatch = f.buffer.match(THINK_OPEN_RE)[0];
        candidates.push({ idx: openThink, kind: "think", len: thinkMatch.length });
      }
      candidates.sort(function (a, b) { return a.idx - b.idx; });

      if (candidates.length === 0) {
        // Sem abertura: emite tudo até o fim, mantendo os últimos 24 chars no
        // buffer (sufixo potencial da maior abertura: <｜DSML｜tool_invokes>
        // tem ~21 chars; ```a2ui/```json têm 7). O retido é liberado conforme
        // novos tokens chegam e na finalização da bolha.
        var keep = Math.min(24, f.buffer.length);
        out += f.buffer.slice(0, f.buffer.length - keep);
        f.buffer = f.buffer.slice(f.buffer.length - keep);
        break;
      }
      var c = candidates[0];
      // Texto antes da abertura é visível.
      out += f.buffer.slice(0, c.idx);
      f.buffer = f.buffer.slice(c.idx + c.len);
      f.state = c.kind;
      if (c.kind === "a2ui") a2uiBlockSeen = true;
      f.jsonBody = "";
      progress = true;
    } else if (f.state === "a2ui") {
      // Dentro do bloco a2ui: procura o fechamento ```.
      var closeIdx = f.buffer.indexOf(LEAK_OPEN_FENCE);
      if (closeIdx === -1) {
        // Sem fechamento ainda: descarta tudo, mantendo os últimos 3 chars.
        var keepClose = Math.min(3, f.buffer.length);
        f.buffer = f.buffer.slice(f.buffer.length - keepClose);
        break;
      }
      f.buffer = f.buffer.slice(closeIdx + 3);
      f.state = "";
      progress = true;
    } else if (f.state === "json") {
      // Dentro de bloco ```json: retém o corpo até o fechamento para decidir.
      var closeJ = f.buffer.indexOf(LEAK_OPEN_FENCE);
      if (closeJ === -1) {
        f.jsonBody += f.buffer;
        f.buffer = "";
        break;
      }
      f.jsonBody += f.buffer.slice(0, closeJ);
      f.buffer = f.buffer.slice(closeJ + 3);
      // Decide: vazamento (invokes/A2UI) é descartado; json legítimo é liberado.
      var body = f.jsonBody.trim();
      if (INVOKE_ARRAY_RE.test(body) || A2UI_ACTION_RE.test(body)) {
        // Vazamento — descarta silenciosamente.
      } else {
        // JSON legítimo — devolve o bloco completo ao texto visível.
        out += "```json" + f.jsonBody + "```";
      }
      f.jsonBody = "";
      f.state = "";
      progress = true;
    } else if (f.state === "think") {
      // Dentro de bloco de raciocínio: descarta tudo até o fechamento.
      var closeT = f.buffer.search(THINK_CLOSE_RE);
      if (closeT === -1) {
        // Sem fechamento ainda: descarta, mantendo o sufixo potencial do
        // fechamento ("</reasoning>" tem 12 chars).
        var keepT = Math.min(16, f.buffer.length);
        f.buffer = f.buffer.slice(f.buffer.length - keepT);
        break;
      }
      var matchT = f.buffer.match(THINK_CLOSE_RE)[0];
      f.buffer = f.buffer.slice(closeT + matchT.length);
      f.state = "";
      progress = true;
    } else if (f.state === "dsml") {
      // Dentro de marcação DSML: procura o fechamento </｜DSML｜...>.
      var closeD = f.buffer.search(DSML_CLOSE_RE);
      if (closeD === -1) {
        // Sem fechamento ainda: descarta tudo, mantendo os últimos 24 chars
        // (sufixo potencial de "</｜DSML｜tool_invokes>", ~21 chars).
        var keepD = Math.min(24, f.buffer.length);
        f.buffer = f.buffer.slice(f.buffer.length - keepD);
        break;
      }
      var matchD = f.buffer.match(DSML_CLOSE_RE)[0];
      f.buffer = f.buffer.slice(closeD + matchD.length);
      f.state = "";
      progress = true;
    }
  }

  return out;
}

// Padrões de vazamento de tool calls emitidos como TEXTO pelo LLM.
// 1. Marcação DSML nativa do modelo: <｜DSML｜tool_invokes>... (separador
//    ｜ fullwidth ou | ASCII).
var DSML_TAG_RE = /<\/?[｜|]DSML[｜|][a-z_]*>/g;
// 2. Tags <invoke>/<parameter>/<tool_invokes> soltas.
var INVOKE_TAG_RE = /<\/?[｜|]?(?:invoke|parameter|tool_invokes)[｜|]?(?:\s[^>]*)?>/g;
// 3. Bloco ```json cujo corpo é array de invokes ou ação A2UI.
var JSON_FENCE_RE = /```(?:json)?\s*([\s\S]*?)```/g;

// sanitizeLeakedToolCalls remove vazamentos de tool calls/marcações internas
// de um texto COMPLETO (não-streaming). Usado na finalização da bolha e em
// respostas que chegam de uma vez (fallback sync, etc.).
function sanitizeLeakedToolCalls(text) {
  if (!text) return text;
  var clean = text;
  // Remove tags DSML e invoke/parameter soltas (o conteúdo entre elas em
  // streaming é filtrado pelo buffer; aqui removemos o que sobrou).
  clean = clean.replace(DSML_TAG_RE, "");
  clean = clean.replace(INVOKE_TAG_RE, "");
  // Remove blocos de raciocínio <think>...</think> (e variantes) e tags
  // soltas — planejamento interno do modelo nunca deve vazar na conversa.
  clean = clean.replace(THINK_PAIR_RE, "");
  clean = clean.replace(THINK_TAG_RE, "");
  // Remove blocos ```json com invokes/A2UI.
  clean = clean.replace(JSON_FENCE_RE, function (m, body) {
    if (INVOKE_ARRAY_RE.test(body) || A2UI_ACTION_RE.test(body)) return "";
    return m;
  });
  // Remove linhas de parâmetro soltas tipo <parameter name="agentId">...</parameter>
  // já cobertas acima; colapsa linhas vazias em excesso.
  clean = clean.replace(/\n{3,}/g, "\n\n").trim();
  return clean;
}

// stripLeakMarkersFromBuffer remove marcadores de vazamento de um buffer de
// streaming parcial. Como os vazamentos podem chegar fragmentados entre
// tokens, aplicamos de forma tolerante: removemos tags completas e mantemos
// sufixos parciais no buffer (o chamador já gerencia o buffer).
function stripLeakMarkersFromBuffer(buf) {
  return buf
    .replace(DSML_TAG_RE, "")
    .replace(INVOKE_TAG_RE, "")
    .replace(THINK_TAG_RE, "");
}

function resetA2uiTokenFilter() {
  a2uiTokenFilter.state = "";
  a2uiTokenFilter.buffer = "";
  a2uiTokenFilter.jsonBody = "";
  a2uiBlockSeen = false;
  a2uiRendered = false;
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
  var thinkingEl = streamingBubble.querySelector(".stream-thinking");
  var contentEl = streamingBubble.querySelector(".stream-content");
  if (!contentEl) {
    contentEl = document.createElement("div");
    contentEl.className = "stream-content";
    if (thinkingEl) {
      streamingBubble.insertBefore(contentEl, thinkingEl);
      thinkingEl.style.display = "none";
    } else {
      streamingBubble.appendChild(contentEl);
    }
  } else if (thinkingEl && !chatActivityToolPhase && thinkingEl.style.display !== "none") {
    // O conteúdo voltou a fluir: o widget reaberto pelo aviso de lentidão sai
    // de cena. Em fase de ferramenta ele PERMANECE — é o indicador de trabalho
    // real, e um rAF atrasado não pode apagá-lo no meio de um upgrade.
    thinkingEl.style.display = "none";
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
  // Enviar permanece habilitado durante o processamento: enquanto ocupado,
  // ele enfileira a mensagem (mesmo comportamento de outros chats/agentes).
  // Exceção: sem comunicação com o servidor o envio fica bloqueado.
  if (chatSendBtn) chatSendBtn.disabled = chatOfflineActive;
  if (chatStopBtn) {
    chatStopBtn.classList.toggle("hidden", !isBusy);
    chatStopBtn.disabled = !isBusy;
    chatStopBtn.title = translate("action.stop");
  }
}

// clearChatStreamTimeout cancela o timer de segurança do stream, se ativo.
function clearChatStreamTimeout() {
  if (chatStreamTimeoutId) {
    clearTimeout(chatStreamTimeoutId);
    chatStreamTimeoutId = null;
  }
}

// armChatStreamTimeout inicia o timer de segurança. Ao disparar, a UI NÃO se
// libera sozinha: mostra aviso na bolha e passa a reconciliar com o backend
// (HasActiveChatStream). Enquanto o core tiver turno ativo, a UI continua
// ocupada (sends entram na fila); quando o core reporta idle — evento terminal
// perdido ou goroutine morta — a bolha é encerrada localmente (onStreamStopped)
// e o chat volta a aceitar dispatch com segurança (TryLock do backend passa).
// O botão Parar continua disponível o tempo todo.
function armChatStreamTimeout() {
  clearChatStreamTimeout();
  chatStreamTimeoutId = setTimeout(function () {
    chatStreamTimeoutId = null;
    if (!chatSending) return;
    console.warn("[chat] stream demorando (" + Math.round(CHAT_STREAM_TIMEOUT_MS / 1000) + "s); reconciliando com o backend");
    // O aviso de lentidão é a rede de segurança do turno (120s sem evento
    // terminal): vale também quando o LLM já emitiu preâmbulo. Antes o gate
    // `!streamingRawContent` silenciava o aviso junto com o widget — no turno de
    // 2026-09-30 isso deixou ~5min sem QUALQUER sinal na tela.
    if (streamingBubble) {
      var thinkingEl = streamingBubble.querySelector(".stream-thinking");
      if (thinkingEl) {
        thinkingEl.style.display = "";
        // Em fase de ferramenta o rótulo de progresso ("Atualizando programa
        // (2 de 4)") diz mais que o aviso genérico — preserva o rótulo, mas
        // liga o cronômetro para o usuário ver que a execução segue viva.
        if (!chatActivityToolPhase) {
          setChatActivityText(thinkingEl, translate("chat.streamSlow"));
        }
        startChatActivityTimer(thinkingEl);
      }
    }
    probeBackendStreamIdle();
  }, CHAT_STREAM_TIMEOUT_MS);
}

// probeBackendStreamIdle sonda o backend: sem turno ativo + UI ocupada =
// evento terminal perdido; encerra a bolha localmente e libera o chat.
// Com turno ativo, continua sondando (turnos longos são legítimos — o loop
// multi-round tem orçamento de até 10min no core).
function probeBackendStreamIdle() {
  try {
    appApi()
      .HasActiveChatStream()
      .then(function (active) {
        if (!chatSending) return;
        if (!active) {
          console.warn("[chat] core sem turno ativo e terminal não chegou — encerrando bolha localmente");
          onStreamStopped();
          return;
        }
        chatStreamTimeoutId = setTimeout(probeBackendStreamIdle, CHAT_STREAM_PROBE_MS);
      })
      .catch(function () {
        if (!chatSending) return;
        // Sonda indisponível: continua aguardando o terminal (Parar funciona).
        chatStreamTimeoutId = setTimeout(probeBackendStreamIdle, CHAT_STREAM_PROBE_MS);
      });
  } catch (_) {
    // Binding lançou de forma síncrona (IPC indisponível neste instante): NÃO
    // abandona a reconciliação — reagenda a sonda, senão o chat fica ocupado
    // para sempre quando o evento terminal também não chega.
    if (chatSending && !chatStreamTimeoutId) {
      chatStreamTimeoutId = setTimeout(probeBackendStreamIdle, CHAT_STREAM_PROBE_MS);
    }
  }
}

function requestStopChatStream() {
  if (!chatSending) return;
  chatStopRequested = true;
  if (chatStopBtn) {
    chatStopBtn.disabled = true;
    chatStopBtn.title = translate("chat.stopping");
  }
  try {
    appApi()
      .StopChatStream()
      .then(function (stopped) {
        // Backend sem turno ativo = evento terminal se perdeu: encerra a
        // bolha localmente. O core está livre — dispatch subsequente é seguro
        // (não bate no TryLock do backend).
        if (!stopped && chatSending && chatStopRequested) {
          console.warn("[chat] StopChatStream=false: terminal do stream se perdeu — encerrando localmente");
          onStreamStopped();
        }
      })
      .catch(function () {
        // If backend stop fails, UI still waits stream terminal event.
      });
  } catch (_) {
    // ignore
  }
}

// ─── Recusa por turno em andamento (reenfileiramento graceful) ───
// Quando o backend recusa o send com "já existe uma resposta em andamento",
// em vez de exibir um erro bruto, a mensagem volta para a fila e o chat
// re-tenta em alguns segundos. O terminal do turno em curso re-dispacha a
// fila (maybeFlushChatQueue); o retry cobre o caso de o evento terminal se
// perder. Limitado a CHAT_BUSY_REQUEUE_MAX tentativas para não loopar.
var CHAT_BUSY_ERR_MARKER = "já existe uma resposta em andamento";
var CHAT_BUSY_REQUEUE_MAX = 10;
var CHAT_BUSY_REQUEUE_MS = 3000;
var chatBusyRequeueAttempts = 0;
var chatBusyRequeueTimerId = null;
var lastDispatchedChatText = "";
var lastDispatchedChatImages = [];

function isChatBusyError(errMsg) {
  return String(errMsg || "").indexOf(CHAT_BUSY_ERR_MARKER) !== -1;
}

function clearChatBusyRequeue() {
  if (chatBusyRequeueTimerId) {
    clearTimeout(chatBusyRequeueTimerId);
    chatBusyRequeueTimerId = null;
  }
  chatBusyRequeueAttempts = 0;
  lastDispatchedChatText = "";
  lastDispatchedChatImages = [];
}

function scheduleChatBusyRequeue() {
  if (chatBusyRequeueTimerId) clearTimeout(chatBusyRequeueTimerId);
  chatBusyRequeueTimerId = setTimeout(function () {
    chatBusyRequeueTimerId = null;
    if (chatSending) return; // outro turno assumiu; o terminal dele re-dispacha
    maybeFlushChatQueue();
  }, CHAT_BUSY_REQUEUE_MS);
}

// ─── Fila de mensagens (enquanto o chat processa outra resposta) ───
// Se o usuário digitar e enviar durante o processamento, a mensagem entra em
// fila, aparece como bolha "na fila" (com cancelamento individual) e é
// despachada ao contexto do chat na primeira oportunidade em que o chat fica
// livre — mesmo comportamento de outros chats/agentes.
//
// ORDEM: a fila é FIFO (shift no flush). Quem entra depois sai depois. Dois
// pontos precisam respeitar isso para a conversa não perder a ordem:
//   1. o envio direto só pode furar a fila quando ela está VAZIA
//      (sendChatMessage testa chatMessageQueue.length, não só chatSending);
//   2. mensagem reenfileirada por "turno em andamento" volta para a FRENTE
//      (queueChatMessage(..., atFront=true)), pois foi enviada antes.

var chatMessageQueue = [];
var CHAT_MESSAGE_QUEUE_MAX = 10;

// autoGrowChatInput ajusta a altura do textarea ao conteúdo: base de 3 linhas
// (60px, piso no CSS .chat-input) crescendo até 6 linhas (cap de 120px, o
// mesmo valor do max-height no CSS). Textos maiores rolam internamente.
function autoGrowChatInput() {
  if (!chatInputEl) return;
  chatInputEl.style.height = "auto";
  // Base de 3 linhas (60px) com cap de 6 linhas (120px) — espelhado no
  // min/max-height de .chat-input.
  chatInputEl.style.height = Math.max(Math.min(chatInputEl.scrollHeight, 120), 60) + "px";
}

// Devolve false quando a mensagem NÃO entrou na fila (sem espaço): quem chamou
// precisa devolver os anexos ao composer, senão eles somem sem nunca serem
// enviados (o take acontece antes do enfileiramento).
//
// atFront=true insere no INÍCIO da fila. É o caso da mensagem que o backend
// recusou com "já existe uma resposta em andamento": ela foi enviada ANTES das
// que já estão na fila, então precisa voltar para a frente — empurrá-la para o
// fim invertia a ordem da conversa.
function queueChatMessage(text, images, atFront) {
  if (!chatMessagesEl) return false;
  if (chatMessageQueue.length >= CHAT_MESSAGE_QUEUE_MAX) {
    showFeedback(translate("chat.queueFull", { max: CHAT_MESSAGE_QUEUE_MAX }), true);
    return false;
  }

  // Os anexos ficam PRESOS na mensagem enfileirada (snapshot): sem isso a fila
  // despachava apenas o texto e o print acabava indo com a próxima mensagem —
  // ou se perdia se o usuário trocasse os anexos do composer enquanto esperava.
  var item = { text: text, el: null, images: images || [] };
  var div = document.createElement("div");
  div.className = "chat-msg user chat-queued";
  div.title = text;

  var textEl = document.createElement("span");
  textEl.className = "chat-queued-text";
  textEl.textContent = text;
  div.appendChild(textEl);

  if (item.images.length > 0) {
    var thumbs = document.createElement("span");
    thumbs.className = "chat-queued-images";
    item.images.forEach(function (src) {
      var img = document.createElement("img");
      img.className = "chat-queued-image";
      img.src = src;
      img.alt = "anexo na fila";
      thumbs.appendChild(img);
    });
    div.appendChild(thumbs);
  }

  var tag = document.createElement("span");
  tag.className = "chat-queued-tag";
  tag.textContent = "⏳ " + translate("chat.queuedTag");
  div.appendChild(tag);

  var cancel = document.createElement("button");
  cancel.type = "button";
  cancel.className = "chat-queued-cancel";
  cancel.textContent = "×";
  cancel.title = translate("action.cancel");
  cancel.setAttribute("aria-label", translate("action.cancel"));
  cancel.addEventListener("click", function () {
    removeQueuedChatMessage(item);
  });
  div.appendChild(cancel);

  // A bolha "na fila" entra no ponto correspondente da fila (frente ou fim),
  // na mesma ordem em que será despachada — antes, uma mensagem reenfileirada
  // aparecia depois das que seriam enviadas antes dela.
  var frontEl = atFront && chatMessageQueue.length > 0 ? chatMessageQueue[0].el : null;
  if (frontEl && frontEl.parentNode === chatMessagesEl) {
    chatMessagesEl.insertBefore(div, frontEl);
  } else {
    chatMessagesEl.appendChild(div);
  }
  item.el = div;
  if (atFront) {
    chatMessageQueue.unshift(item);
  } else {
    chatMessageQueue.push(item);
  }
  scheduleChatScrollToBottom();
  return true;
}

function removeQueuedChatMessage(item) {
  var idx = chatMessageQueue.indexOf(item);
  if (idx !== -1) chatMessageQueue.splice(idx, 1);
  if (item.el && item.el.parentNode) item.el.parentNode.removeChild(item.el);
  // Cancelar a mensagem devolve os prints ao composer (nada é perdido por ter
  // sido enviado junto dela).
  if (item.images && item.images.length && typeof screenshotRestoreAttachments === "function") {
    screenshotRestoreAttachments(item.images);
  }
}

// maybeFlushChatQueue despacha a próxima mensagem em fila quando o chat fica
// livre. É chamada nos terminais do stream (done/error/stop), sempre APÓS
// maybeProcessPendingA2uiAction — a ação A2UI solicitada pelo assistente tem
// prioridade; a fila aguarda o terminal do processamento dela.
function maybeFlushChatQueue() {
  if (chatSending || !chatMessageQueue.length) return;
  // Sem comunicação o chat está indisponível: mantém a fila intacta até
  // reconectar (não descarta a mensagem do usuário).
  if (chatOfflineActive) return;
  var next = chatMessageQueue.shift();
  if (next.el && next.el.parentNode) next.el.parentNode.removeChild(next.el);
  dispatchChatMessage(next.text, next.images);
}

// ─── Activity do agent (status polido: conectar → ferramentas → resposta) ───
// O core envia status crus em pt-BR ("Um instante...",
// "Analisando sua solicitacao...", "Executando: list_tickets, ..."). Este
// bloco converte em
// rótulos amigáveis, humaniza nomes de tools MCP e mantém uma trilha de
// passos (chips ✓) — em vez do texto único que saltava a cada evento.
var CHAT_ACTIVITY_MAX_STEPS = 4;
var chatActivityRoundMax = 0;
// Maior round do turno já exibido na pill DESTA bolha. É o guard de
// monotonicidade: heartbeat que não avança (ou retrocede) é valor degenerado e
// não reescreve a pill — era exatamente o sintoma do contador por-request do
// servidor, que mandava round=1 para sempre ("Etapa 1 de N" travada).
var chatActivityRoundShown = 0;
// chatActivityToolPhase fica true quando o turno entra na fase de execução de
// ferramentas (tools/compose/rescue). Diferente das fases "sociais" (conectar,
// planejar), essa fase continua merecendo indicador mesmo depois que o LLM já
// emitiu texto — ver chatActivityShowsWhileText.
var chatActivityToolPhase = false;
// Cronômetro de tempo decorrido do widget: uma única chamada de winget/choco
// pode levar 1-2min sem emitir nenhum evento (o log de 2026-09-30 registra
// 152s só para atualizar o Chrome). Sem contador vivo o usuário não distingue
// "executando" de "travado".
var chatActivityTimerId = null;
var chatActivityStartedAt = 0;

// Rótulos humanos das tools MCP (pt-BR, coerente com os status do core).
// Ferramenta fora do dicionário cai no prettify do nome bruto.
var CHAT_TOOL_LABELS = {
  "get_agent_info": "Coletando dados da máquina",
  "get_inventory": "Coletando inventário",
  "export_inventory_markdown": "Gerando relatório (Markdown)",
  "export_inventory_pdf": "Gerando relatório (PDF)",
  "get_osquery_status": "Verificando osquery",
  "get_logs": "Lendo logs",
  "query_event_log": "Consultando eventos do Windows",
  "list_installed_packages": "Listando programas instalados",
  "search_packages": "Pesquisando programas",
  "install_package": "Instalando programa",
  "uninstall_package": "Desinstalando programa",
  "upgrade_package": "Atualizando programa",
  "upgrade_all_packages": "Atualizando todos os programas",
  "get_pending_updates": "Verificando atualizações",
  "get_package_actions": "Verificando ações do pacote",
  "list_printers": "Listando impressoras",
  "install_printer": "Instalando impressora",
  "remove_printer": "Removendo impressora",
  "get_printer_config": "Consultando impressora",
  "list_print_jobs": "Verificando fila de impressão",
  "remove_print_job": "Cancelando job de impressão",
  "spooler_status": "Verificando serviço de impressão",
  "restart_spooler": "Reiniciando serviço de impressão",
  "clear_queue": "Limpando fila de impressão",
  "list_drivers": "Listando drivers",
  "list_tickets": "Consultando chamados",
  "get_ticket_details": "Buscando detalhes do chamado",
  "create_ticket": "Abrindo chamado",
  "add_ticket_comment": "Comentando no chamado",
  "close_ticket": "Encerrando chamado",
  "reopen_ticket": "Reabrindo chamado",
  "rate_ticket": "Registrando avaliação",
  "ping_host": "Testando conectividade (ping)",
  "flush_dns": "Limpando cache DNS",
  "memory_list": "Consultando memórias",
  "memory_create": "Salvando anotação",
  "memory_delete": "Removendo anotação",
  "get_internal_navigation_routes": "Mapeando telas do app",
  "build_internal_navigation_link": "Criando link interno",
  "ask_user": "Aguardando sua resposta",
  "get_performance_snapshot": "Capturando desempenho",
  "get_top_processes": "Analisando processos",
  "get_disk_health": "Verificando saúde dos discos",
  "get_recent_errors": "Buscando erros recentes",
};

function humanizeToolName(name) {
  var raw = String(name || "").trim();
  if (!raw) return "";
  if (CHAT_TOOL_LABELS[raw]) return CHAT_TOOL_LABELS[raw];
  // Fallback: "get_top_processes" → "Get top processes"
  return raw
    .replace(/[_-]+/g, " ")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/^./, function (c) { return c.toUpperCase(); });
}

// describeChatActivity converte o status cru do core (chat:thinking) em
// {key, kind} — key: chave i18n do rótulo; kind: connect|plan|tools|compose|
// rescue|fallback (controla a trilha de passos). Padrões desconhecidos voltam
// como texto original (kind info).
function describeChatActivity(status) {
  var s = String(status || "").trim();
  if (!s) return null;
  if (s.indexOf("Um instante") === 0 || s.indexOf("Conectando ao servidor") === 0) {
    // "Conectando ao servidor" = status cru de cores antigas do agent;
    // mantido no match por compatibilidade retroativa.
    return { key: "chat.activity.connecting", kind: "connect" };
  }
  if (s.indexOf("Analisando sua solicitacao") === 0 || s.indexOf("Consultando o modelo") === 0) {
    // Pós-conexão: LLM planejando/executando (o rótulo troca após o
    // handshake; "Consultando o modelo" = cru antigo, aceito por
    // compatibilidade retroativa).
    return { key: "chat.activity.model", kind: "connect" };
  }
  if (/^Round\s+\d+/.test(s)) {
    return { key: "chat.activity.planning", kind: "plan" };
  }
  if (s.indexOf("Executando:") === 0) {
    // O core pode anexar progresso ao status de cada tool do lote:
    // "Executando: upgrade_package (2/4) [Google.Chrome.EXE]..." — ver
    // formatToolProgressStatus em app/core/ai/chat_multi_round.go. Tanto o
    // contador "(i/n)" quanto o bloco "[hint]" vêm sempre no fim, então são
    // extraídos antes do prettify do nome da tool.
    var index = 0;
    var total = 0;
    var detail = "";
    var tools = s
      .slice("Executando:".length)
      .replace(/\.+$/, "")
      .split(",")
      .map(function (x) {
        var part = x.trim();
        if (!part) return "";
        var withDetail = /\((\d+)\/(\d+)\)\s*\[([^\]]*)\]\s*$/.exec(part);
        var m = withDetail || /\((\d+)\/(\d+)\)\s*$/.exec(part);
        if (m) {
          index = parseInt(m[1], 10) || 0;
          total = parseInt(m[2], 10) || 0;
          if (withDetail) detail = (withDetail[3] || "").trim();
          part = part.slice(0, m.index).trim();
        }
        return part;
      })
      .filter(Boolean);
    return { kind: "tools", tools: tools, index: index, total: total, detail: detail };
  }
  if (s.indexOf("Concluindo ação solicitada") === 0) {
    return { key: "chat.activity.composing", kind: "compose" };
  }
  if (s.indexOf("Reconectando") === 0) {
    return { key: "chat.activity.rescue", kind: "rescue" };
  }
  if (s.indexOf("Alternando para resposta") === 0) {
    return { key: "chat.activity.fallback", kind: "fallback" };
  }
  return { raw: s, kind: "info" };
}

function buildChatActivityElement() {
  chatActivityRoundMax = 0;
  chatActivityRoundShown = 0;
  chatActivityToolPhase = false;
  // Cada bolha tem o SEU cronômetro. Sem parar o intervalo aqui, o split
  // pós-pergunta (splitStreamingBubbleAfterQuestion descarta a bolha antiga e
  // cria outra) deixaria o timer antigo escrevendo na bolha nova.
  stopChatActivityTimer();
  chatActivityStartedAt = 0;
  var root = document.createElement("div");
  root.className = "stream-thinking chat-activity";

  var head = document.createElement("div");
  head.className = "chat-activity-head";
  var spinner = document.createElement("span");
  spinner.className = "chat-activity-spinner";
  spinner.setAttribute("aria-hidden", "true");
  head.appendChild(spinner);
  var label = document.createElement("span");
  label.className = "chat-activity-label";
  // Live region "polite": leitores de tela anunciam a troca de rótulo
  // (ex.: "Atualizando programa (2 de 4)…") sem que o cronômetro — que muda a
  // cada segundo e está aria-hidden — vire spam.
  label.setAttribute("role", "status");
  label.textContent = translate("chat.thinking");
  head.appendChild(label);

  var elapsed = document.createElement("span");
  elapsed.className = "chat-activity-elapsed";
  elapsed.setAttribute("aria-hidden", "true");
  // Semeia o chip: sem isto ele fica como pílula vazia até o próximo evento —
  // visível no split pós-pergunta, que cria bolha nova no meio do turno.
  elapsed.textContent = formatChatActivityElapsed(0);
  head.appendChild(elapsed);
  root.appendChild(head);

  var steps = document.createElement("div");
  steps.className = "chat-activity-steps";
  root.appendChild(steps);

  var pill = document.createElement("span");
  pill.className = "chat-activity-pill";
  root.appendChild(pill);
  return root;
}

function chatActivityLabelEl(root) {
  return root ? root.querySelector(".chat-activity-label") : null;
}

// setChatActivityText atualiza o rótulo do widget; em estruturas antigas
// (widget ausente), cai para textContent simples.
function setChatActivityText(root, text) {
  var el = chatActivityLabelEl(root);
  if (el) {
    el.textContent = text || "";
  } else if (root) {
    root.textContent = text || "";
  }
}

function chatActivityStepsEl(root) {
  return root ? root.querySelector(".chat-activity-steps") : null;
}

// formatChatActivityElapsed devolve mm:ss (neutro de idioma): leitura rápida de
// "há quanto tempo" sem inflar o rótulo do widget.
function formatChatActivityElapsed(ms) {
  var total = Math.max(0, Math.floor(Number(ms) / 1000));
  var min = Math.floor(total / 60);
  var sec = total % 60;
  return min + ":" + (sec < 10 ? "0" + sec : String(sec));
}

// startChatActivityTimer liga o contador de tempo decorrido do widget (uma vez
// por bolha). Cobre o caso em que não há NENHUM evento intermediário possível:
// winget/choco são um único processo externo por pacote.
function startChatActivityTimer(root) {
  if (chatActivityTimerId) return;
  // Só define o marco zero na primeira exibição DESTA bolha: após
  // suspender/restaurar a UI o contador retoma de onde parou em vez de voltar
  // para 0:00. O reset entre turnos/bolhas é feito em
  // buildChatActivityElement.
  if (!chatActivityStartedAt) chatActivityStartedAt = Date.now();
  var el = root ? root.querySelector(".chat-activity-elapsed") : null;
  // Semeia com o tempo JÁ decorrido: após suspender/restaurar a UI o marco zero
  // é preservado, e um "0:00" fixo aqui piscava antes de saltar de volta.
  if (el) {
    el.textContent = formatChatActivityElapsed(
      chatActivityStartedAt ? Date.now() - chatActivityStartedAt : 0,
    );
  }
  chatActivityTimerId = setInterval(function () {
    var node = streamingBubble
      ? streamingBubble.querySelector(".chat-activity-elapsed")
      : null;
    if (!node) return;
    node.textContent = formatChatActivityElapsed(Date.now() - chatActivityStartedAt);
  }, 1000);
}

function stopChatActivityTimer() {
  if (chatActivityTimerId) {
    clearInterval(chatActivityTimerId);
    chatActivityTimerId = null;
  }
  // NÃO zera chatActivityStartedAt: uma suspensão/restauração da UI deve
  // retomar o tempo decorrido, não reiniciá-lo. O reset é por bolha.
}

// pushChatActivityStep move a step "current" anterior para "done" e adiciona
// a nova. A trilha mantém no máximo CHAT_ACTIVITY_MAX_STEPS concluídos.
function pushChatActivityStep(root, text, state) {
  var stepsEl = chatActivityStepsEl(root);
  if (!stepsEl) return;
  var prev = stepsEl.querySelector(".chat-step.current");
  // Mesmo passo reemitido (retry forçado ou resgate após reconexão): atualiza o
  // chip existente em vez de empilhar um duplicado idêntico.
  var prevName = prev ? prev.querySelector(".chat-step-name") : null;
  if (prevName && prevName.textContent === text) return;
  if (prev) {
    prev.classList.remove("current");
    prev.classList.add("done");
    var prevIcon = prev.querySelector(".chat-step-icon");
    if (prevIcon) prevIcon.textContent = "✓";
  }
  var chip = document.createElement("span");
  chip.className = "chat-step" + (state === "current" ? " current" : " done");
  var icon = document.createElement("span");
  icon.className = "chat-step-icon";
  icon.textContent = state === "current" ? "•" : "✓";
  chip.appendChild(icon);
  var name = document.createElement("span");
  name.className = "chat-step-name";
  name.textContent = text;
  name.title = text;
  chip.appendChild(name);
  stepsEl.appendChild(chip);
  var doneChips = stepsEl.querySelectorAll(".chat-step.done");
  if (doneChips.length > CHAT_ACTIVITY_MAX_STEPS) {
    stepsEl.removeChild(doneChips[0]);
  }
  scheduleChatScrollToBottom();
}

function updateChatActivityRound(root, round, maxRounds) {
  var pill = root ? root.querySelector(".chat-activity-pill") : null;
  if (!pill) return;
  if (round <= 0) return;
  // Nunca anda para trás: reemissão do mesmo round (retry/resgate) é no-op e um
  // valor menor que o já exibido é descartado.
  if (round < chatActivityRoundShown) return;
  chatActivityRoundShown = round;
  var max = maxRounds > 0 ? maxRounds : chatActivityRoundMax;
  pill.textContent = max > 0
    ? translate("chat.activity.round", { round: round, max: max })
    : translate("chat.activity.roundOnly", { round: round });
  if (maxRounds > 0) chatActivityRoundMax = maxRounds;
  pill.classList.add("visible");
}

// chatActivityShowsWhileText decide se o widget continua valendo DEPOIS que o
// LLM já emitiu texto no turno (ex.: preâmbulo "Vou atualizar os programas…").
//
// Fases "sociais" (conectar, analisar, planejar) ficam ocultas para não duplicar
// o que o texto já diz. Execução de ferramenta é trabalho real e demorado: o
// widget TEM de reaparecer, senão o turno parece travado. Foi exatamente esse o
// gate antigo (`if (streamingRawContent) return`) que escondeu ~5min de
// upgrades via winget no turno de 2026-09-30 — o preâmbulo do round 0 desligou
// o indicador para o turno inteiro (ver chat_logs.jsonl, linhas 666-700).
function chatActivityShowsWhileText(desc) {
  if (!desc) return false;
  return desc.kind === "tools" || desc.kind === "compose" || desc.kind === "rescue";
}

function onStreamThinking(status) {
  if (document.hidden || window.__discoveryUISuspended) return;
  if (!streamingBubble) return;
  var thinkingEl = streamingBubble.querySelector(".stream-thinking");
  if (!thinkingEl) return;

  var desc = describeChatActivity(status);
  // Reclassifica a fase a CADA status (não "trava" em true para sempre): todo
  // round começa com um status social ("Um instante..."/"Round N — ..."), e é
  // isso que volta a ocultar o widget quando a resposta final começa a fluir.
  // Com o latch anterior, o widget ficava preso abaixo da resposta com o rótulo
  // da última ferramenta e o spinner girando durante todo o texto final.
  //
  // Seguro contra corrida: o loop de tools é síncrono no cliente, então nenhum
  // status social pode chegar no meio de uma execução — só os "Executando: ...".
  chatActivityToolPhase = chatActivityShowsWhileText(desc);
  if (streamingRawContent && !chatActivityToolPhase) {
    thinkingEl.style.display = "none";
    return;
  }
  thinkingEl.style.display = "";
  startChatActivityTimer(thinkingEl);

  var label = chatActivityLabelEl(thinkingEl);
  if (!label) {
    // Estrutura antiga (sem widget): fallback texto simples.
    thinkingEl.textContent = status || translate("chat.thinking");
    scheduleChatScrollToBottom();
    return;
  }
  if (!desc) {
    setChatActivityText(thinkingEl, translate("chat.thinking"));
    return;
  }
  if (desc.raw) {
    setChatActivityText(thinkingEl, desc.raw);
    scheduleChatScrollToBottom();
    return;
  }
  if (desc.kind === "tools" && desc.tools && desc.tools.length) {
    var names = desc.tools.map(humanizeToolName).filter(Boolean);
    if (!names.length) {
      setChatActivityText(thinkingEl, translate("chat.activity.planning"));
      return;
    }
    // Progresso por item do lote ("(2/4)"), quando o core mandou.
    var inBatch = desc.total > 1 && desc.index > 0;
    setChatActivityText(
      thinkingEl,
      inBatch && names.length === 1
        ? translate("chat.activity.executingStep", {
            tool: names[0],
            done: desc.index,
            total: desc.total,
          })
        : names.length === 1
          ? translate("chat.activity.executingOne", { tool: names[0] })
          : translate("chat.activity.executingMany", { count: names.length }),
    );
    // Com o alvo (id do pacote) disponível, o chip mostra QUAL item está em
    // execução em vez de repetir o rótulo genérico da ferramenta.
    var shown = desc.detail || names.slice(0, 2).join(", ");
    if (!desc.detail && names.length > 2) shown += " +" + (names.length - 2);
    pushChatActivityStep(thinkingEl, shown, "current");
    return;
  }
  if (desc.kind === "compose") {
    setChatActivityText(thinkingEl, translate(desc.key));
    pushChatActivityStep(thinkingEl, translate("chat.activity.composingStep"), "current");
    return;
  }
  if (desc.kind === "plan") {
    setChatActivityText(thinkingEl, translate(desc.key));
    scheduleChatScrollToBottom();
    return;
  }
  setChatActivityText(thinkingEl, translate(desc.key));
  scheduleChatScrollToBottom();
}

// onChatBudget mostra no chat o estado do ORÇAMENTO DE ROUNDS do agent loop.
// "exhausted": o servidor atingiu o limite de ferramentas do turno e vai pedir
//   autorização ao usuário antes de continuar (as ações clicadas ainda NÃO
//   executaram). "renewed": o usuário autorizou (ou começou um turno novo) e as
//   ações pendentes serão retomadas.
// Sem este aviso a interface ficava muda enquanto o backend já estava
// bloqueado — foi o caso do clique no YogaDNS (2026-10-08).
function onChatBudget(data) {
  var payload = data;
  if (typeof payload === "string") {
    try { payload = JSON.parse(payload); } catch (_) { payload = { message: payload }; }
  }
  var state = (payload && payload.state) || "";
  var round = payload && payload.round ? payload.round : 0;
  var maxRounds = payload && payload.maxRounds ? payload.maxRounds : 0;
  var msg = (payload && payload.message) || "";
  if (!msg) {
    msg = state === "renewed"
      ? "Orçamento de rounds renovado — retomando as ações pendentes."
      : "Limite de rounds de ferramentas atingido — aguardando sua autorização.";
  }
  console.log("[chat] budget " + state + " (" + round + "/" + maxRounds + "): " + msg);
  // A bolha de streaming segue aberta enquanto o modelo pergunta: usa o widget
  // de activity para o aviso ficar visível junto do progresso.
  if (streamingBubble) {
    var thinkingEl = streamingBubble.querySelector(".stream-thinking");
    if (thinkingEl) {
      thinkingEl.style.display = "";
      setChatActivityText(thinkingEl, msg);
      if (state !== "renewed") startChatActivityTimer(thinkingEl);
    }
  }
  if (typeof showFeedback === "function") showFeedback(msg, false);
}
// onChatLoopProgress recebe o progresso do agent loop. O round vem do AGENTE
// (contador do turno, ver chat_multi_round.go) — não do heartbeat cru do
// servidor, cujo contador é por-request e travava em 1 nas tool chains MCP.
// maxRounds pode chegar 0: sem teto confiável, a pill mostra "Etapa N".
// Elimina a sensação de travamento durante tool chains longas. Payload pode
// chegar como objeto {round, maxRounds} (Wails) ou string JSON (SSE/publish).
function onChatLoopProgress(data) {
  var payload = data;
  if (typeof data === "string") {
    try { payload = JSON.parse(data); } catch (e) { return; }
  }
  if (!payload || typeof payload !== "object") return;
  var round = Number(payload.round) || 0;
  var maxRounds = Number(payload.maxRounds) || 0;
  // Separação entre rounds: cada round é uma NOVA mensagem do LLM. Sem a
  // quebra, o texto de dois rounds fica colado no mesmo parágrafo (visto no
  // turno de 2026-10-01 12:01Z: "...janelas abertas primeiro.Vou dar uma...").
  // Roda mesmo com a janela oculta (o buffer continua sendo montado).
  if (round > streamingLastRound) {
    streamingLastRound = round;
    if (round > 1 && streamingRawContent.trim() && !/\n\s*$/.test(streamingRawContent)) {
      onStreamToken("\n\n");
    }
  }
  if (document.hidden || window.__discoveryUISuspended) return;
  if (!streamingBubble) return;
  if (round <= 0) return;
  // Mesma regra do onStreamThinking: com texto já visível a pill de etapa só
  // reaparece se o turno está em fase de execução de ferramentas.
  if (streamingRawContent && !chatActivityToolPhase) return;
  var thinkingEl = streamingBubble.querySelector(".stream-thinking");
  if (!thinkingEl) return;
  thinkingEl.style.display = "";
  startChatActivityTimer(thinkingEl);
  // Progresso do agent loop vira uma pill discreta ("Etapa 2 de 10") em vez
  // de reescrever o rótulo principal — o status corrente permanece visível.
  updateChatActivityRound(thinkingEl, round, maxRounds);
}

function finaliseStreamingBubble() {
  if (!streamingBubble) return;
  var filterStateAtEnd = a2uiTokenFilter.state;
  // Libera qualquer texto residual que ficou retido no buffer do filtro
  // (ex.: os últimos caracteres de uma resposta sem vazamentos). Se o stream
  // morreu DENTRO de um bloco a2ui/json/DSML, o residual é conteúdo truncado
  // e deve ser descartado, não exibido — exceto json legítimo já validado.
  if (a2uiTokenFilter.state === "") {
    streamingRawContent += a2uiTokenFilter.buffer;
  } else if (a2uiTokenFilter.state === "json") {
    // Bloco ```json não fechado: se o corpo parcial já é claramente um
    // vazamento (invokes/A2UI), descarta; caso contrário, libera como texto
    // legítimo truncado (o usuário merece ver o que o LLM escreveu).
    var partialBody = a2uiTokenFilter.jsonBody.trim();
    if (!INVOKE_ARRAY_RE.test(partialBody) && !A2UI_ACTION_RE.test(partialBody)) {
      streamingRawContent += "```json" + a2uiTokenFilter.jsonBody;
    }
  } else if (a2uiTokenFilter.state === "a2ui") {
    // Stream morreu DENTRO do bloco a2ui: o evento chat:a2ui nunca chegará
    // (o servidor só extrai o bloco no fim). Com texto visível, anexa um aviso
    // curto — antes o usuário via o texto anunciando o card e nada acontecia.
    //
    // O createSurface pode ter chegado antes do corte: a surface ficaria presa
    // em "Loading surface..." até o watchdog (12s). Aqui o corte é CERTO, então
    // remove as surfaces deste turno que não receberam componentes.
    a2uiDropOrphanSurfaces();
    if (streamingRawContent.trim()) {
      streamingRawContent += "\n\n" + translate("chat.a2uiInterrupted");
    } else {
      streamingRawContent = translate("chat.responseInterrupted");
    }
  } else if (!streamingRawContent.trim()) {
    // Stream morreu no meio de um bloco DSML e não há texto visível: o evento
    // chat:a2ui nunca chegará. Mostra um aviso em vez de deixar o usuário sem
    // resposta.
    streamingRawContent = translate("chat.responseInterrupted");
  }
  // Bloco a2ui visto no stream, mas NENHUM evento chat:a2ui recebido: o
  // servidor descartou a interface (validação do catálogo). Avisa para o texto
  // que anunciava um card não ficar sem explicação. O caso de stream truncado
  // dentro do bloco já foi avisado acima (filterStateAtEnd === "a2ui").
  if (filterStateAtEnd !== "a2ui" && a2uiBlockSeen && !a2uiRendered) {
    streamingRawContent += "\n\n" + translate("chat.a2uiNotShown");
  }

  a2uiTokenFilter.buffer = "";
  a2uiTokenFilter.state = "";
  a2uiTokenFilter.jsonBody = "";

  // Sanitiza vazamentos de tool calls emitidos como texto (DSML, blocos
  // ```json com invokes, ações A2UI cruas). O filtro incremental cobre os
  // casos comuns, mas vazamentos fragmentados entre tokens podem escapar —
  // a sanitização final garante que nada cru chegue ao usuário.
  streamingRawContent = sanitizeLeakedToolCalls(streamingRawContent);

  // Opções de continuação (bloco final de "- ") viram botões e SAEM do corpo
  // do texto — sem isto, o mesmo conteúdo aparecia duas vezes (lista no texto
  // + chips de botão). A extração roda sobre o conteúdo final; se houver
  // opções, o corpo é re-renderizado sem as linhas viradas em botão.
  var actionSplit = splitTrailingChatActionOptions(streamingRawContent);
  if (actionSplit.options.length > 0) {
    streamingRawContent = actionSplit.content;
  }

  // Flush any remaining buffered content immediately.
  streamingRafPending = false;
  flushStreamingContent();

  // Remove streaming indicators.
  var thinkingEl = streamingBubble.querySelector(".stream-thinking");
  if (thinkingEl) thinkingEl.remove();
  var cursor = streamingBubble.querySelector(".stream-cursor");
  if (cursor) cursor.remove();
  streamingBubble.classList.remove("streaming");

  // Add quick-action suggestion buttons (bloco final de "- " da resposta).
  if (actionSplit.options.length > 0) {
    appendChatQuickActions(streamingBubble, actionSplit.options);
  }

  streamingBubble = null;
  streamingRawContent = "";
  scheduleChatScrollToBottom();
}

// safeFinaliseStreamingBubble: finalisa a bolha SEM deixar o chat ocupado
// para sempre se a renderização final falhar (exceção em markdown/tabela/
// botões). onStreamDone/onStreamError/onStreamStopped sempre liberam o estado
// (setChatBusy(false) + fila) logo depois dela — uma exceção aqui não pode
// travar o chat (bug de travamento visto em produção em 20/09).
function safeFinaliseStreamingBubble() {
  try {
    finaliseStreamingBubble();
  } catch (e) {
    console.error("[chat] falha ao finalizar bolha de streaming:", e);
  }
}

function onStreamDone() {
  stopPollingLoop();
  stopThinkingStatusUpdates();
  clearChatStreamTimeout();
  chatPendingQuestionCount = 0;
  clearChatBusyRequeue();
  // Rede de segurança: a resposta final precisa ser a última bolha do turno.
  ensureStreamingBubbleAtEnd();
  safeFinaliseStreamingBubble();
  chatStopRequested = false;
  setChatBusy(false);
  if (chatInputEl) chatInputEl.focus();
  // Ação A2UI que chegou durante o stream: processa agora que o chat está livre.
  maybeProcessPendingA2uiAction();
  // Fila de mensagens: despacha a próxima na primeira oportunidade.
  maybeFlushChatQueue();
  // Sem turno nem ação pendente: encerra o estado ocupado das surfaces A2UI.
  a2uiSettleBusyIfIdle();
  // Surface que ficou sem componentes: avisa agora (não espera o watchdog).
  a2uiFinalizeIncompleteSurfaces();
}

function onStreamError(errMsg) {
  stopPollingLoop();
  stopThinkingStatusUpdates();
  clearChatStreamTimeout();
  chatPendingQuestionCount = 0;

  // Recusa do backend por turno em andamento: devolve a mensagem para a fila
  // em vez de exibir o erro bruto. O turno em curso libera o chat no terminal
  // dele; o retry em 3s cobre o caso de evento terminal perdido.
  if (isChatBusyError(errMsg)) {
    var requeueText = lastDispatchedChatText;
    if (streamingBubble && !streamingRawContent) {
      streamingBubble.remove();
      streamingBubble = null;
      streamingRawContent = "";
      resetA2uiTokenFilter();
    }
    setChatBusy(false);
    if (requeueText && chatBusyRequeueAttempts < CHAT_BUSY_REQUEUE_MAX) {
      chatBusyRequeueAttempts++;
      console.warn("[chat] send recusado (turno em andamento) — reenfileirado (tentativa " + chatBusyRequeueAttempts + "/" + CHAT_BUSY_REQUEUE_MAX + ")");
      // A mensagem recusada foi enviada ANTES das que já estavam na fila:
      // volta para a FRENTE para não inverter a ordem da conversa.
      if (!queueChatMessage(requeueText, lastDispatchedChatImages, true)) {
        // Sem espaço na fila: os prints voltam ao composer em vez de sumirem.
        if (lastDispatchedChatImages.length > 0 && typeof screenshotRestoreAttachments === "function") {
          screenshotRestoreAttachments(lastDispatchedChatImages);
        }
      }
      scheduleChatBusyRequeue();
      return;
    }
    // Acima do limite de tentativas: segue para o tratamento normal de erro.
  }

  clearChatBusyRequeue();

  if (chatStopRequested) {
    if (streamingBubble && !streamingRawContent) {
      streamingRawContent = translate("chat.responseInterrupted");
    }
    ensureStreamingBubbleAtEnd();
    safeFinaliseStreamingBubble();
    chatStopRequested = false;
    setChatBusy(false);
    if (chatInputEl) chatInputEl.focus();
    // Ação A2UI pendente não deve "vazar" para a próxima mensagem digitada:
    // processa agora que o chat está livre (mesmo após stop).
    maybeProcessPendingA2uiAction();
    // Fila: despacha a próxima mensagem pendente mesmo após stop.
    maybeFlushChatQueue();
    a2uiSettleBusyIfIdle();
    return;
  }

  if (streamingBubble) {
    // Show whatever content arrived; fallback to error text if nothing came.
    if (!streamingRawContent) {
      streamingRawContent = translate("chat.errorUnknown", {
        error: String(errMsg || translate("common.unknown")),
      });
    }
    ensureStreamingBubbleAtEnd();
    safeFinaliseStreamingBubble();
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
  // Processa ação A2UI pendente também após erro de stream (evita vazamento
  // da ação para a próxima mensagem digitada).
  maybeProcessPendingA2uiAction();
  // Fila: despacha a próxima mensagem pendente após o erro.
  maybeFlushChatQueue();
  a2uiSettleBusyIfIdle();
  // Surface que ficou sem componentes: avisa agora (não espera o watchdog).
  a2uiFinalizeIncompleteSurfaces();
}

function onStreamStopped() {
  stopPollingLoop();
  stopThinkingStatusUpdates();
  clearChatStreamTimeout();
  chatPendingQuestionCount = 0;
  clearChatBusyRequeue();
  if (streamingBubble && !streamingRawContent) {
    streamingRawContent = translate("chat.responseInterrupted");
  }
  ensureStreamingBubbleAtEnd();
  safeFinaliseStreamingBubble();
  chatStopRequested = false;
  setChatBusy(false);
  if (chatInputEl) chatInputEl.focus();
  // Processa ação A2UI pendente também após stop manual.
  maybeProcessPendingA2uiAction();
  // Fila: despacha a próxima mensagem pendente após o stop.
  maybeFlushChatQueue();
  a2uiSettleBusyIfIdle();
  // Surface que ficou sem componentes: avisa agora (não espera o watchdog).
  a2uiFinalizeIncompleteSurfaces();
}

// ─── Mini-Questionário Interativo (ask_user MCP) ───

// Perguntas pendentes: enquanto houver, o timer de segurança do stream (60s)
// fica PAUSADO — o backend espera o usuário responder sem limite de tempo, e
// a UI não deve dar o stream por morto nem enfileirar/despachar por conta
// própria nesse período.
var chatPendingQuestionCount = 0;

// ─── Dock de pergunta na área de digitação (ask_user) ───
// A pergunta interativa não renderiza controles dentro da bolha: ela ocupa o
// lugar da área de digitação (estilo assistente interativo), com opções como
// botões e SEMPRE um campo de texto livre para o usuário digitar a própria
// resposta, mesmo quando opções são sugeridas.
var chatQuestionDock = null;
var chatQuestionDockText = null;
var chatQuestionDockOptions = null;
var chatQuestionDockInput = null;
var chatQuestionDockSendBtn = null;
var chatQuestionDockStopBtn = null;
var chatInputWrapEl = null;
// Fila de perguntas pendentes: o backend pode emitir mais de uma; exibimos a
// mais antiga no dock e avançamos conforme as respostas chegam.
var chatQuestionQueue = [];

function onChatQuestion(data) {
  try {
    var q = typeof data === "string" ? JSON.parse(data) : data;
    if (!q || !q.id) return;
    chatPendingQuestionCount += 1;
    // Pausa o timeout de segurança: a espera pela resposta não tem prazo.
    clearChatStreamTimeout();
    // Indica no indicador de streaming que o chat está aguardando o usuário.
    // Sem o gate `!streamingRawContent`: com um preâmbulo antes do ask_user o
    // widget já está visível e ficaria com o rótulo da ferramenta ("Atualizando
    // programa…") em vez de "Aguardando sua resposta".
    if (streamingBubble) {
      var thinkingEl = streamingBubble.querySelector(".stream-thinking");
      if (thinkingEl) {
        thinkingEl.style.display = "";
        setChatActivityText(thinkingEl, translate("chat.waitingAnswer"));
      }
    }
    showChatQuestion(q);
  } catch (e) {
    console.error("chat:question parse error:", e);
  }
}

// endPendingQuestion decrementa o contador de perguntas pendentes e reativa o
// timer de segurança se o stream ainda estiver em execução.
function endPendingQuestion() {
  if (chatPendingQuestionCount > 0) chatPendingQuestionCount -= 1;
  if (chatPendingQuestionCount === 0 && chatSending) {
    armChatStreamTimeout();
  }
}

// ensureChatQuestionDockEls resolve os elementos do dock sob demanda e faz o
// binding único dos handlers (Enter/Esc no input, botão Enviar e Parar).
function ensureChatQuestionDockEls() {
  // Revalida a referência cacheada: se o partial do chat for reinjetado
  // (hot-reload/dev), o nó antigo fica órfão e o dock pararia de responder.
  if (chatQuestionDock !== null && document.body.contains(chatQuestionDock)) return;
  chatQuestionDockText = null;
  chatQuestionDockOptions = null;
  chatQuestionDockInput = null;
  chatQuestionDockSendBtn = null;
  chatQuestionDockStopBtn = null;
  chatQuestionDock = document.getElementById("chatQuestionDock");
  chatInputWrapEl = document.getElementById("chatInputWrap");
  if (!chatQuestionDock) return;
  chatQuestionDockText = chatQuestionDock.querySelector("#chatQuestionDockText");
  chatQuestionDockOptions = chatQuestionDock.querySelector("#chatQuestionDockOptions");
  chatQuestionDockInput = chatQuestionDock.querySelector("#chatQuestionDockInput");
  chatQuestionDockSendBtn = chatQuestionDock.querySelector("#chatQuestionDockSendBtn");
  chatQuestionDockStopBtn = chatQuestionDock.querySelector("#chatQuestionDockStopBtn");
  if (chatQuestionDockInput && !chatQuestionDockInput.dataset.bound) {
    chatQuestionDockInput.dataset.bound = "1";
    chatQuestionDockInput.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault();
        submitChatQuestionDockText();
        return;
      }
      // Esc cancela a pergunta pelo mesmo caminho do botão Parar.
      if (e.key === "Escape") {
        e.preventDefault();
        requestStopChatStream();
      }
    });
  }
  if (chatQuestionDockSendBtn && !chatQuestionDockSendBtn.dataset.bound) {
    chatQuestionDockSendBtn.dataset.bound = "1";
    chatQuestionDockSendBtn.addEventListener("click", submitChatQuestionDockText);
  }
  if (chatQuestionDockStopBtn && !chatQuestionDockStopBtn.dataset.bound) {
    chatQuestionDockStopBtn.dataset.bound = "1";
    chatQuestionDockStopBtn.addEventListener("click", function () {
      // Cancela o turno no backend, que emite chat:question_cancelled e
      // restaura a área de digitação (mesmo caminho do botão Parar original).
      requestStopChatStream();
    });
  }
}

// submitChatQuestionDockText envia o texto digitado no dock para a pergunta em
// exibição. Sempre disponível — mesmo com opções sugeridas, o usuário pode
// responder com as próprias palavras.
function submitChatQuestionDockText() {
  ensureChatQuestionDockEls();
  if (!chatQuestionDockInput || chatQuestionQueue.length === 0) return;
  var answer = chatQuestionDockInput.value.trim();
  if (!answer) return;
  var question = chatQuestionQueue[0];
  chatQuestionDockInput.value = "";
  answerChatQuestion(question.id, answer);
}

// normalizeChatQuestionOptions aceita options como array (formato atual do
// evento chat:question), string JSON ("[\"a\",\"b\"]") ou string delimitada
// ("a; b") — assim a pergunta nunca fica sem opções por diferença de versão
// entre backend/UI ou por reemissão. Deduplica (case-insensitive) e descarta
// entradas vazias.
function normalizeChatQuestionOptions(options) {
  var raw = options;
  if (typeof raw === "string") {
    var text = raw.trim();
    if (!text) return [];
    if (text.charAt(0) === "[") {
      try {
        raw = JSON.parse(text);
      } catch (_) {
        raw = null;
      }
    }
    if (!Array.isArray(raw)) raw = text.split(/[;\n]/);
  }
  if (!Array.isArray(raw)) return [];
  var out = [];
  var seen = {};
  for (var i = 0; i < raw.length; i += 1) {
    var label = String(raw[i] == null ? "" : raw[i]).trim();
    if (!label) continue;
    var key = label.toLowerCase();
    if (seen[key]) continue;
    seen[key] = 1;
    out.push(label);
  }
  return out;
}

// renderChatQuestionDock exibe a pergunta mais antiga pendente no dock,
// escondendo a área de digitação; sem pendências, restaura o compositor.
function renderChatQuestionDock() {
  ensureChatQuestionDockEls();
  if (!chatQuestionDock || !chatInputWrapEl) return;
  if (chatQuestionQueue.length === 0) {
    chatQuestionDock.classList.add("hidden");
    chatInputWrapEl.classList.remove("hidden");
    return;
  }
  var question = chatQuestionQueue[0];

  chatQuestionDockText.innerHTML = renderAssistantMarkdown(question.question);
  chatQuestionDockOptions.innerHTML = "";

  // Opções como botões de resposta rápida (sempre normalizadas antes).
  var questionOptions = normalizeChatQuestionOptions(question.options);
  if (questionOptions.length > 0) {
    questionOptions.forEach(function (opt) {
      var btn = document.createElement("button");
      btn.type = "button";
      // Classe própria do dock: NÃO usa btn-xs (que força width:100% e
      // transforma cada opção numa barra de largura total, estourando o card).
      btn.className = "btn subtle chat-question-option";
      // Rótulo longo ganha tooltip com o texto completo.
      btn.title = String(opt);
      btn.innerHTML = formatInlineChatMarkdown(opt);
      btn.addEventListener("click", function () {
        btn.classList.add("chat-question-option-selected");
        answerChatQuestion(question.id, opt);
      });
      chatQuestionDockOptions.appendChild(btn);
    });
  }

  // O campo de texto livre fica SEMPRE visível: o usuário escolhe entre as
  // opções sugeridas ou digita a própria resposta.
  chatQuestionDock.classList.remove("hidden");
  chatInputWrapEl.classList.add("hidden");
  syncColorMode();
  scheduleChatScrollToBottom();
  if (chatQuestionDockInput) chatQuestionDockInput.focus();
}

function showChatQuestion(question) {
  if (!chatMessagesEl) return;

  // Dedupe: reemissão do mesmo id (retry/retransmissão do backend) não cria
  // uma segunda bolha nem reenfileira a pergunta no dock.
  var existingQuestions = chatMessagesEl.querySelectorAll(".chat-question");
  for (var qi = 0; qi < existingQuestions.length; qi += 1) {
    if (existingQuestions[qi].dataset.questionId === question.id) return;
  }
  for (var qq = 0; qq < chatQuestionQueue.length; qq += 1) {
    if (chatQuestionQueue[qq].id === question.id) return;
  }

  // Bolha na conversa: mantém a pergunta no histórico, SEM controles — a
  // interação acontece no dock que substitui a área de digitação.
  var div = document.createElement("div");
  div.className = "chat-msg assistant chat-question chat-question-in-dock";
  div.dataset.questionId = question.id;

  // Pergunta renderizada com markdown
  var contentEl = document.createElement("div");
  contentEl.className = "stream-content";
  contentEl.innerHTML = renderAssistantMarkdown(question.question);
  div.appendChild(contentEl);

  // Dica apontando para o dock de resposta.
  var hint = document.createElement("div");
  hint.className = "meta chat-question-dock-hint";
  hint.textContent = translate("chat.questionDockHint");
  div.appendChild(hint);

  chatMessagesEl.appendChild(div);

  // Enfileira e exibe no dock (área do compositor).
  chatQuestionQueue.push(question);
  renderChatQuestionDock();
  syncColorMode();
  scheduleChatScrollToBottom();
}

// appendChatQuestionAnswer exibe a resposta do usuário à pergunta como uma
// bolha destacada (mesma linguagem visual dos outros chats).
function appendChatQuestionAnswer(text) {
  if (!chatMessagesEl) return;
  var div = document.createElement("div");
  div.className = "chat-msg user chat-question-answer";

  var tag = document.createElement("span");
  tag.className = "chat-question-answer-tag";
  tag.textContent = translate("chat.answerTag");
  div.appendChild(tag);

  var textEl = document.createElement("span");
  textEl.className = "chat-question-answer-text";
  textEl.textContent = text;
  div.appendChild(textEl);

  chatMessagesEl.appendChild(div);
  scheduleChatScrollToBottom();
}

// splitStreamingBubbleAfterQuestion fecha a bolha de streaming atual e abre
// uma nova no fim da conversa. A bolha original nasce no INÍCIO do turno
// (antes da pergunta ask_user); ao retomar o stream após a resposta do
// usuário, o texto continuaria nela — aparecendo ACIMA da pergunta/resposta
// e quebrando a cronologia. Com o split, a ordem fica:
//   [resposta parcial] [pergunta] [sua resposta] [continuação da resposta]
// O conteúdo pré-pergunta permanece na bolha antiga; streamingRawContent é
// zerado para a continuação não duplicar o texto.
function splitStreamingBubbleAfterQuestion() {
  if (streamingBubble) {
    var hadContent = !!streamingRawContent.trim();
    var oldThinking = streamingBubble.querySelector(".stream-thinking");
    if (oldThinking) oldThinking.remove();
    var oldCursor = streamingBubble.querySelector(".stream-cursor");
    if (oldCursor) oldCursor.remove();
    streamingBubble.classList.remove("streaming");
    if (!hadContent) streamingBubble.remove();
    streamingBubble = null;
  }
  streamingRawContent = "";
  streamingRafPending = false;
  if (!chatMessagesEl) return;
  streamingBubble = document.createElement("div");
  streamingBubble.className = "chat-msg assistant streaming";
  streamingBubble.appendChild(buildChatActivityElement());
  var cursorEl = document.createElement("span");
  cursorEl.className = "stream-cursor";
  streamingBubble.appendChild(cursorEl);
  chatMessagesEl.appendChild(streamingBubble);
  scheduleChatScrollToBottom();
}

function answerChatQuestion(questionId, answer) {
  // Remove a pergunta da fila do dock e avança para a próxima pendente (ou
  // devolve a área de digitação) antes do feedback visual da resposta.
  for (var qi = 0; qi < chatQuestionQueue.length; qi += 1) {
    if (chatQuestionQueue[qi].id === questionId) {
      chatQuestionQueue.splice(qi, 1);
      break;
    }
  }
  renderChatQuestionDock();
  // Feedback imediato: bolha destacada com a resposta do usuário + reativa o
  // timer de segurança (a espera pela pergunta terminou).
  appendChatQuestionAnswer(answer);
  endPendingQuestion();
  // O stream retoma escrevendo na bolha de streaming — que nasceu ANTES da
  // pergunta. Fecha-a e abre uma nova depois do Q&A para a continuação da
  // resposta aparecer na ordem certa da conversa.
  splitStreamingBubbleAfterQuestion();
  // O processamento retomou: volta o indicador para "Pensando...".
  if (streamingBubble && !streamingRawContent) {
    var thinkingEl = streamingBubble.querySelector(".stream-thinking");
    if (thinkingEl) setChatActivityText(thinkingEl, translate("chat.thinking"));
  }
  try {
    // B15: promise nao aguardada - captura a rejeicao com .catch.
    appApi().AnswerChatQuestion(questionId, answer).catch(function (e) {
      console.error("answerChatQuestion error:", e);
    });
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

// onChatQuestionCancelled recebe {id} quando a pergunta pendente foi
// cancelada no backend (usuário parou o chat / app encerrado): desabilita a
// bolha da pergunta para o usuário não responder a uma pergunta morta.
function onChatQuestionCancelled(data) {
  try {
    var payload = typeof data === "string" ? JSON.parse(data) : data;
    // A espera backend terminou — libera o estado de pergunta pendente.
    if (chatPendingQuestionCount > 0) {
      chatPendingQuestionCount -= 1;
      if (chatPendingQuestionCount === 0 && chatSending) armChatStreamTimeout();
    }
    if (!payload || !payload.id) return;

    // Remove a pergunta do dock (se estava em exibição/fila) e restaura a
    // área de digitação quando não houver outra pendente.
    for (var qi = 0; qi < chatQuestionQueue.length; qi += 1) {
      if (chatQuestionQueue[qi].id === payload.id) {
        chatQuestionQueue.splice(qi, 1);
        break;
      }
    }
    renderChatQuestionDock();

    if (!chatMessagesEl) return;
    var div = chatMessagesEl.querySelector(
      '.chat-msg.chat-question[data-question-id="' + String(payload.id).replace(/"/g, '\\"') + '"]'
    );
    if (!div) return;
    disableQuestionButtons(div);
    var note = document.createElement("div");
    note.className = "meta chat-question-cancelled-note";
    note.textContent = translate("chat.questionCancelled");
    div.appendChild(note);
  } catch (e) {
    console.error("chat:question_cancelled parse error:", e);
  }
}

// ─── Ponte de log do frontend → logs.db (R8 da revisão 2026-10-08) ───
// Os console.warn/error do WebView não apareciam em logs.db (fontes só "agent" e
// "service"): falhas de render do A2UI eram invisíveis no diagnóstico. Aqui os
// avisos de [a2ui]/[chat] são encaminhados ao agent (throttle local + no Go).
// A chamada é defensiva: exe antigo sem o binding LogFrontend apenas ignora.
// Janela deslizante de 10 min (o teto não pode ser vitalício: um app aberto por
// dias pararia de registrar avisos justo quando o diagnóstico é necessário).
var frontendLogBridge = {
  seen: Object.create(null),
  count: 0,
  max: 100,
  windowMs: 600000,
  windowAt: 0,
};

function forwardFrontendLog(level, args) {
  try {
    if (frontendLogBridge.count >= frontendLogBridge.max) return;
    var text = "";
    for (var i = 0; i < args.length; i++) {
      var a = args[i];
      var part;
      if (a && a.message) part = a.message;
      else if (typeof a === "object" && a !== null) {
        try { part = JSON.stringify(a); } catch (_) { part = String(a); }
      } else part = String(a);
      text += (i ? " " : "") + part;
    }
    if (!text) return;
    if (text.indexOf("[a2ui]") === -1 && text.indexOf("[chat]") === -1) return;
    // Um payload A2UI que falhou pode ter dezenas de KB: não trafega inteiro no IPC.
    if (text.length > 800) text = text.slice(0, 800) + "…";
    var now = Date.now();
    if (!frontendLogBridge.windowAt || now - frontendLogBridge.windowAt > frontendLogBridge.windowMs) {
      frontendLogBridge.windowAt = now;
      frontendLogBridge.count = 0;
      frontendLogBridge.seen = Object.create(null);
    }
    if (frontendLogBridge.count >= frontendLogBridge.max) return;
    if (frontendLogBridge.seen[text] && now - frontendLogBridge.seen[text] < 10000) return;
    frontendLogBridge.seen[text] = now;
    frontendLogBridge.count++;
    var api = appApi();
    if (api && typeof api.LogFrontend === "function") {
      try { api.LogFrontend(level, text); } catch (_) {}
    }
  } catch (_) {
    // Nunca lançar a partir do console.
  }
}

(function installFrontendLogBridge() {
  if (typeof console === "undefined" || console.__a2uiBridged) return;
  console.__a2uiBridged = true;
  var origWarn = console.warn ? console.warn.bind(console) : function () {};
  var origError = console.error ? console.error.bind(console) : function () {};
  console.warn = function () {
    forwardFrontendLog("warn", arguments);
    return origWarn.apply(null, arguments);
  };
  console.error = function () {
    forwardFrontendLog("error", arguments);
    return origError.apply(null, arguments);
  };
})();

// ─── A2UI (Agent-to-User Interface) — interfaces ricas geradas por IA ───

// Mapa das surfaces A2UI ativas por surfaceId. Antes havia UMA surface ativa:
// ao chegar um card novo a anterior era destruída e seus botões morriam. Agora
// cada surfaceId conserva a sua bolha/handle (um card antigo continua clicável).
var a2uiSurfaces = Object.create(null);

// ─── Tema visual das surfaces A2UI ───
// Os componentes do catálogo A2UI (@a2ui/lit) renderizam em SHADOW DOM: os
// seletores de responsive-part-b.css NÃO atravessam a fronteira do shadow root
// e o card traz "border: 1px solid #ccc" INLINE — era por isso que o visual
// ficava pobre (borda cinza, sem cor, sem hover). O tema abaixo é injetado em
// CADA shadow root aberto da surface e reaplicado conforme o Lit cria novos
// hosts (updateComponents/updateDataModel criam elementos novos).
var A2UI_THEME_CSS = [
  // :host display:block é essencial: os componentes do catálogo Lit são
  // elementos INLINE por padrão, então o card encolhia para o tamanho do
  // conteúdo (os "cards finos" do exemplo do ChoicePicker).
  // ATENÇÃO: NÃO usar width:100% no :host. Este CSS é injetado em TODOS os
  // shadow roots, inclusive no de cada botão; com width:100% cada botão virava
  // uma caixa de largura total e, como .a2ui-row usa flex-wrap:wrap, os botões
  // de um Row QUEBRAVAM para linhas separadas ("Voltar" em cima de "Avançar",
  // layout verificado em navegador em 2026-10-08). O display:block sozinho já
  // faz o componente ocupar a largura disponível quando é filho de Column/Card;
  // como item de Row, a largura fica no tamanho do conteúdo (lado a lado).
  ":host{display:block;box-sizing:border-box;font-family:inherit;color:var(--text,#e6ecf5);}",
  "@keyframes a2ui-card-in{from{opacity:0;transform:translateY(6px)}to{opacity:1;transform:none}}",
  ".a2ui-card{display:block;position:relative;padding:16px 18px!important;",
  "border:1px solid color-mix(in srgb,var(--accent,#4f8cff) 45%,transparent)!important;",
  "border-radius:14px!important;",
  "background:linear-gradient(145deg,color-mix(in srgb,var(--accent,#4f8cff) 16%,transparent),transparent 55%),",
  "var(--card-bg,rgba(255,255,255,.05))!important;",
  "box-shadow:0 10px 26px rgba(0,0,0,.22),inset 0 1px 0 rgba(255,255,255,.06);",
  "overflow:hidden;animation:a2ui-card-in .22s ease-out;}",
  ".a2ui-card::before{content:\"\";position:absolute;inset:0 auto 0 0;width:3px;",
  "background:linear-gradient(180deg,var(--accent,#4f8cff),transparent);}",
  ".a2ui-column{display:flex;flex-direction:column;gap:10px;}",
  ".a2ui-row{display:flex;flex-direction:row;gap:10px;align-items:center;flex-wrap:wrap;}",
  // Botões dentro de um Row NUNCA devem esticar/ocupar a linha inteira: ficam
  // lado a lado (o Row quebra a linha apenas quando realmente não couber).
  ".a2ui-row>a2ui-basic-button,.a2ui-row>a2ui-button{width:auto;flex:0 0 auto;}",
  "p,h1,h2,h3,h4,h5{margin:0 0 6px;line-height:1.5;color:inherit;}",
  "h1,h2,h3{font-weight:600;letter-spacing:.01em;}",
  "p{font-size:.92rem;}",
  ".a2ui-caption{display:block;font-size:.82rem;line-height:1.45;opacity:.75;}",
  "button.a2ui-button{display:inline-flex;align-items:center;justify-content:center;gap:6px;",
  "font:inherit;font-weight:600;font-size:.86rem;padding:8px 14px;border-radius:10px;white-space:nowrap;",
  "border:1px solid color-mix(in srgb,var(--accent,#4f8cff) 55%,transparent);",
  "background:linear-gradient(180deg,color-mix(in srgb,var(--accent,#4f8cff) 88%,#fff 12%),var(--accent,#4f8cff));",
  "color:var(--on-accent,#fff);cursor:pointer;",
  "box-shadow:0 4px 12px color-mix(in srgb,var(--accent,#4f8cff) 32%,transparent);",
  "transition:transform .12s ease,box-shadow .18s ease,filter .18s ease;}",
  "button.a2ui-button:hover:not([disabled]){transform:translateY(-1px);filter:brightness(1.08);",
  "box-shadow:0 8px 20px color-mix(in srgb,var(--accent,#4f8cff) 42%,transparent);}",
  "button.a2ui-button:active:not([disabled]){transform:translateY(0) scale(.985);}",
  "button.a2ui-button:focus-visible{outline:2px solid color-mix(in srgb,var(--accent,#4f8cff) 70%,#fff 30%);outline-offset:2px;}",
  "button.a2ui-button[disabled]{opacity:.5;cursor:default;box-shadow:none;}",
  "button.a2ui-button-borderless{background:transparent;color:var(--accent,#4f8cff);border-color:transparent;box-shadow:none;}",
  "button.a2ui-button-borderless:hover:not([disabled]){background:color-mix(in srgb,var(--accent,#4f8cff) 12%,transparent);}",
  ".a2ui-divider{border:0;border-top:1px solid color-mix(in srgb,var(--border,#fff) 18%,transparent);margin:10px 0;}",
  ".a2ui-checkbox{display:flex;align-items:center;gap:8px;margin:4px 0;}",
  ".a2ui-checkbox input{accent-color:var(--accent,#4f8cff);}",
  "input[type=text],input[type=number],input[type=email]{width:100%;padding:8px 12px;border-radius:10px;",
  "border:1px solid color-mix(in srgb,var(--border,#fff) 22%,transparent);",
  "background:var(--input-bg,color-mix(in srgb,var(--text,#888) 8%,transparent));color:inherit;font:inherit;font-size:.88rem;}",
  "input[type=text]:focus,input[type=number]:focus,input[type=email]:focus{outline:none;",
  "border-color:var(--accent,#4f8cff);box-shadow:0 0 0 3px color-mix(in srgb,var(--accent,#4f8cff) 25%,transparent);}",
  ".a2ui-list{display:flex;flex-direction:column;gap:8px;}",
  ".a2ui-list-item{padding:8px 10px;border-radius:10px;background:color-mix(in srgb,var(--text,#fff) 5%,transparent);}",
  ".a2ui-slider input[type=range]{width:100%;accent-color:var(--accent,#4f8cff);}",
  ".a2ui-choicepicker,.a2ui-tabs{display:flex;gap:8px;flex-wrap:wrap;}",
  ".a2ui-modal{border-radius:14px;border:1px solid color-mix(in srgb,var(--accent,#4f8cff) 35%,transparent);",
  "background:var(--card-bg,rgba(20,24,32,.98));padding:16px;box-shadow:0 18px 40px rgba(0,0,0,.4);}",
  ".a2ui-video,.a2ui-audioplayer{width:100%;border-radius:12px;}",
  // ─── Cobertura do markup REAL do catálogo basic (@a2ui/lit 0.9.1) ───
  // Os nomes de classe abaixo foram conferidos no a2ui-bundle.js: TextField usa
  // input/textarea.a2ui-textfield dentro de .a2ui-textfield-container; ChoicePicker
  // é .a2ui-choicepicker > label + .options > label > input; Divider horizontal é
  // <hr class="a2ui-divider">. Sem isso os campos caíam no visual nativo do WebView.
  "*,*::before,*::after{box-sizing:border-box;}",
  ".a2ui-column{width:100%;}",
  "button.a2ui-button-primary{background:linear-gradient(180deg,color-mix(in srgb,var(--accent,#4f8cff) 88%,#fff 12%),var(--accent,#4f8cff));}",
  "button.a2ui-button-default{background:color-mix(in srgb,var(--text,#fff) 8%,transparent);color:inherit;box-shadow:none;border-color:color-mix(in srgb,var(--border,#fff) 26%,transparent);}",
  "button.a2ui-button-default:hover:not([disabled]){background:color-mix(in srgb,var(--text,#fff) 14%,transparent);}",
  ".a2ui-textfield-container{display:flex;flex-direction:column;gap:4px;width:100%;}",
  ".a2ui-textfield-container>label{font-size:.78rem;opacity:.8;}",
  ".a2ui-textfield{width:100%;padding:8px 12px;border-radius:10px;border:1px solid color-mix(in srgb,var(--border,#fff) 24%,transparent);background:var(--input-bg,color-mix(in srgb,var(--text,#888) 8%,transparent));color:inherit;font:inherit;font-size:.88rem;}",
  ".a2ui-textfield:focus{outline:none;border-color:var(--accent,#4f8cff);box-shadow:0 0 0 3px color-mix(in srgb,var(--accent,#4f8cff) 25%,transparent);}",
  ".a2ui-textfield.invalid{border-color:var(--danger,#e5484d);}",
  "textarea.a2ui-textfield{min-height:76px;resize:vertical;}",
  ".a2ui-textfield-container .error{color:var(--danger,#e5484d);font-size:.78rem;}",
  ".a2ui-choicepicker{display:flex;flex-direction:column;gap:6px;width:100%;}",
  ".a2ui-choicepicker>label{font-size:.78rem;opacity:.8;}",
  ".a2ui-choicepicker .options{display:flex;flex-wrap:wrap;gap:10px;}",
  ".a2ui-choicepicker .options label{display:inline-flex;align-items:center;gap:6px;font-size:.86rem;padding:5px 9px;border-radius:9px;border:1px solid color-mix(in srgb,var(--border,#fff) 22%,transparent);cursor:pointer;transition:background .15s ease,border-color .15s ease;}",
  ".a2ui-choicepicker .options label:hover{background:color-mix(in srgb,var(--accent,#4f8cff) 12%,transparent);border-color:color-mix(in srgb,var(--accent,#4f8cff) 45%,transparent);}",
  ".a2ui-choicepicker input[type=radio],.a2ui-choicepicker input[type=checkbox]{accent-color:var(--accent,#4f8cff);margin:0;}",
  ".a2ui-slider{display:flex;flex-direction:column;gap:6px;width:100%;}",
  ".a2ui-slider>label{font-size:.78rem;opacity:.8;}",
  ".a2ui-slider>span{font-size:.8rem;opacity:.75;}",
  ".a2ui-datetime{display:flex;flex-direction:column;gap:4px;width:100%;}",
  ".a2ui-datetime>label{font-size:.78rem;opacity:.8;}",
  ".a2ui-datetime input{width:100%;padding:8px 12px;border-radius:10px;border:1px solid color-mix(in srgb,var(--border,#fff) 24%,transparent);background:var(--input-bg,color-mix(in srgb,var(--text,#888) 8%,transparent));color:inherit;font:inherit;font-size:.88rem;}",
  ".a2ui-datetime input:focus{outline:none;border-color:var(--accent,#4f8cff);box-shadow:0 0 0 3px color-mix(in srgb,var(--accent,#4f8cff) 25%,transparent);}",
  ".a2ui-list{display:flex;flex-direction:column;gap:8px;width:100%;}",
  ".a2ui-tabs{display:flex;flex-direction:column;gap:10px;width:100%;}",
  ".a2ui-tab-headers{display:flex;gap:6px;flex-wrap:wrap;}",
  ".a2ui-tab-headers button{border:1px solid color-mix(in srgb,var(--border,#fff) 22%,transparent);background:transparent;color:inherit;padding:6px 12px;border-radius:9px;cursor:pointer;}",
  ".a2ui-tab-headers button:hover{background:color-mix(in srgb,var(--accent,#4f8cff) 12%,transparent);}",
  // Tabs do pacote usam style INLINE (padding/background #eee/border:none), que
  // vence qualquer regra sem !important: sem isto a aba ATIVA ficava cinza-claro
  // (#eee) com o texto claro do tema escuro — ilegível. O seletor por atributo
  // identifica a aba ativa pela cor inline; se o pacote mudar a cor, a regra
  // apenas deixa de casar (volta ao visual anterior, sem quebrar).
  ".a2ui-tab-headers{border-bottom:1px solid color-mix(in srgb,var(--border,#fff) 30%,transparent)!important;margin-bottom:12px!important;flex-wrap:wrap!important;}",
  ".a2ui-tab-headers button{padding:6px 14px!important;border:none!important;border-bottom:2px solid transparent!important;background:transparent!important;color:inherit!important;font:inherit!important;font-weight:600!important;font-size:.86rem!important;cursor:pointer!important;opacity:.7!important;}",
  ".a2ui-tab-headers button[style*='#eee']{opacity:1!important;border-bottom-color:var(--accent,#4f8cff)!important;background:color-mix(in srgb,var(--accent,#4f8cff) 12%,transparent)!important;color:var(--accent,#4f8cff)!important;}",
  "hr.a2ui-divider{width:100%;border:0;border-top:1px solid color-mix(in srgb,var(--border,#fff) 22%,transparent);margin:10px 0;}",
  ".a2ui-divider-vertical{width:1px;align-self:stretch;background:color-mix(in srgb,var(--border,#fff) 22%,transparent);margin:0 10px;}",
  // Select = dropdown próprio do Discovery (components/Select.js no bundle).
  ".a2ui-select{display:flex;flex-direction:column;gap:4px;width:100%;}",
  ".a2ui-select>label{font-size:.78rem;opacity:.8;}",
  ".a2ui-select select{width:100%;padding:8px 12px;border-radius:10px;border:1px solid color-mix(in srgb,var(--border,#fff) 24%,transparent);background:var(--input-bg,color-mix(in srgb,var(--text,#888) 8%,transparent));color:inherit;font:inherit;font-size:.88rem;cursor:pointer;}",
  ".a2ui-select select:focus{outline:none;border-color:var(--accent,#4f8cff);box-shadow:0 0 0 3px color-mix(in srgb,var(--accent,#4f8cff) 25%,transparent);}",
].join("");

// a2uiInjectThemeIn percorre o container E os shadow roots (recursivo) e injeta
// o tema em cada root novo. Também observa cada root descoberto: mutações
// DENTRO de um shadow root não são vistas pelo observer do container externo.
// collectAddedNodes extrai os ELEMENTOS adicionados de um lote de mutações.
// Devolve null quando não há nada novo (nesse caso a varredura é completa).
function collectAddedNodes(mutations) {
  var added = null;
  for (var i = 0; i < mutations.length; i++) {
    var nodes = mutations[i].addedNodes;
    for (var j = 0; j < nodes.length; j++) {
      var n = nodes[j];
      if (n && n.nodeType === 1) {
        if (!added) added = [];
        added.push(n);
      }
    }
  }
  return added;
}

// a2uiInjectThemeIn percorre o container E os shadow roots (recursivo) e injeta
// o tema em cada root novo. `startNodes` (opcional) limita a varredura às
// subárvores que MUDARAM: o observer enfileira só os nós adicionados, então uma
// mutação pequena não paga o custo da surface inteira. Sem startNodes, varre
// tudo (anexo inicial, timers de segurança e a2uiThemeRefresh).
function a2uiInjectThemeIn(containerEl, state, startNodes) {
  // Percorre LIGHT DOM e SHADOW DOM. O container (.a2ui-container) é um <div>
  // comum, sem shadow root: a versão anterior fazia `if (!sr) continue`, ou
  // seja, só descia por shadow roots — partindo de um nó sem shadow root ela
  // terminava sem visitar NADA e o tema nunca era injetado (os componentes,
  // inclusive o botão, ficavam com o visual nativo do WebView).
  var stack = [];
  if (startNodes && startNodes.length) {
    for (var s = 0; s < startNodes.length; s++) stack.push(startNodes[s]);
  } else {
    stack.push(containerEl);
  }
  while (stack.length) {
    var el = stack.pop();
    if (!el || el.nodeType !== 1) continue;
    var sr = el.shadowRoot;
    if (sr) {
      var st = sr.__a2uiThemeEl;
      if (!(st && st.isConnected && st.parentNode === sr)) {
        try {
          st = document.createElement("style");
          st.setAttribute("data-a2ui-theme", "1");
          st.textContent = A2UI_THEME_CSS;
          sr.insertBefore(st, sr.firstChild);
          sr.__a2uiThemeEl = st;
        } catch (e) {
          console.warn("[a2ui] falha ao injetar tema no shadow root:", e);
          st = null;
        }
        if (st && state && window.MutationObserver) {
          // Mutações DENTRO de um shadow root não são vistas pelo observer do
          // container: cada root novo ganha o seu (componentes criados por
          // updateComponents/updateDataModel re-renderizam aqui).
          var obs = new MutationObserver(function (mutations) {
            a2uiScheduleTheme(containerEl, state, collectAddedNodes(mutations));
          });
          try { obs.observe(sr, { childList: true, subtree: true }); state.observers.push(obs); } catch (_) {}
        }
      }
      for (var i = 0; i < sr.children.length; i++) stack.push(sr.children[i]);
    }
    // Filhos em light DOM: aqui ficam o .a2ui-surface-host e o <a2ui-surface>.
    for (var j = 0; j < el.children.length; j++) stack.push(el.children[j]);
  }
}

function a2uiScheduleTheme(containerEl, state, addedNodes) {
  if (!state) return;
  if (addedNodes && addedNodes.length) {
    if (!state.pending) state.pending = [];
    for (var p = 0; p < addedNodes.length; p++) state.pending.push(addedNodes[p]);
  }
  if (state.scheduled) return;
  state.scheduled = true;
  var run = function () {
    state.scheduled = false;
    var pending = state.pending || [];
    state.pending = [];
    try { a2uiInjectThemeIn(containerEl, state, pending); } catch (e) { console.warn("[a2ui] tema falhou:", e); }
  };
  if (window.requestAnimationFrame) window.requestAnimationFrame(run);
  else setTimeout(run, 16);
}

// a2uiThemeAttach liga o tema a uma surface recém-criada.
function a2uiThemeAttach(containerEl, entry) {
  var state = { scheduled: false, observers: [], timers: [], pending: [] };
  entry.theme = state;
  a2uiScheduleTheme(containerEl, state);
  // O Lit anexa o shadow root no primeiro update (microtask) e cria hosts novos
  // a cada updateComponents — reaplica em alguns instantes para cobrir as
  // primeiras renderizações sem depender de observer no root ainda inexistente.
  [120, 400, 1200, 2500].forEach(function (ms) {
    state.timers.push(setTimeout(function () { a2uiScheduleTheme(containerEl, state); }, ms));
  });
  if (window.MutationObserver) {
    var obs = new MutationObserver(function (mutations) {
      a2uiScheduleTheme(containerEl, state, collectAddedNodes(mutations));
    });
    try { obs.observe(containerEl, { childList: true, subtree: true }); state.observers.push(obs); } catch (_) {}
  }
  return state;
}

function a2uiThemeDispose(entry) {
  var state = entry && entry.theme;
  if (!state) return;
  (state.observers || []).forEach(function (o) { try { o.disconnect(); } catch (_) {} });
  (state.timers || []).forEach(function (t) { clearTimeout(t); });
  state.observers = [];
  state.timers = [];
  entry.theme = null;
}

function a2uiThemeRefresh(surfaceId) {
  var entry = a2uiSurfaces[surfaceId];
  if (!entry || !entry.theme) return;
  var contentEl = entry.bubble ? entry.bubble.querySelector(".a2ui-container") : null;
  if (contentEl) a2uiScheduleTheme(contentEl, entry.theme);
}

// ─── Watchdog de surface incompleta ───
// Uma surface criada sem updateComponents fica presa em "Loading surface..."
// para sempre: o renderer do catálogo só mostra o slot de loading quando o
// componente root não existe. Foi o que aconteceu quando a resposta foi
// interrompida no meio do bloco a2ui. O watchdog troca o card vazio por um
// aviso claro em vez de deixar "Loading surface..." na tela.
var A2UI_SURFACE_WATCHDOG_MS = 12000;

function a2uiClearSurfaceWatchdog(entry) {
  if (entry && entry.watchdog) {
    clearTimeout(entry.watchdog);
    entry.watchdog = null;
  }
}

function a2uiArmSurfaceWatchdog(surfaceId, entry) {
  a2uiClearSurfaceWatchdog(entry);
  entry.watchdog = setTimeout(function () {
    entry.watchdog = null;
    if (a2uiSurfaces[surfaceId] !== entry) return;
    // Turno ainda em andamento: os updateComponents podem estar a caminho (o
    // createSurface e o updateComponents são mensagens distintas do stream e um
    // turno lento pode separá-las por mais que o timeout). Só desiste quando o
    // chat está livre — antes um turno lento matava o card e mostrava
    // "interface não concluída" com a interface chegando logo depois.
    if (chatSending || pendingA2uiAction > 0) {
      a2uiArmSurfaceWatchdog(surfaceId, entry);
      return;
    }
    console.warn("[a2ui] surface " + surfaceId + " sem componentes após " + A2UI_SURFACE_WATCHDOG_MS + "ms — resposta interrompida?");
    a2uiSurfaceDestroy(surfaceId);
    showA2uiIncompleteNotice();
  }, A2UI_SURFACE_WATCHDOG_MS);
}

// showA2uiIncompleteNotice avisa que a interface não veio completa — o texto
// markdown normal do turno já foi exibido, então não há perda de conteúdo.
function showA2uiIncompleteNotice() {
  if (!chatMessagesEl) return;
  var div = document.createElement("div");
  div.className = "chat-msg assistant";
  div.innerHTML = renderAssistantMarkdown(
    translate("chat.a2uiIncomplete"),
  );
  syncColorMode();
  bindInternalChatLinks(div);
  chatMessagesEl.appendChild(div);
  scheduleChatScrollToBottom();
}

// ─── Estado ocupado e rastro do clique na conversa ───
// Dedupe de clique repetido: mesma ação (surface + nome + contexto) dentro de
// A2UI_ACTION_DEDUPE_MS é ignorada. Antes existia um flag GLOBAL que engolia a
// bolha de QUALQUER clique feito durante um turno — o usuário clicava em outro
// programa e a interface não mostrava nada (caso YogaDNS 2026-10-08), mesmo com
// o comando já enfileirado no agent.
var A2UI_ACTION_DEDUPE_MS = 1200;
var a2uiLastActionKey = "";
var a2uiLastActionAt = 0;

// formatA2uiActionLabel dá nome legível ao clique, para o turno disparado pela
// interface não ficar sem rastro na conversa.
function formatA2uiActionLabel(action) {
  var name = String((action && action.name) || "").trim();
  if (!name) return "▶ ação na interface";
  return "▶ " + name.replace(/[._]+/g, " ").replace(/\s+/g, " ").trim();
}

function a2uiSetBusy(surfaceId, busy) {
  var entry = a2uiSurfaces[surfaceId];
  if (!entry || !entry.bubble) return;
  entry.bubble.classList.toggle("a2ui-busy", !!busy);
  entry.bubble.setAttribute("aria-busy", busy ? "true" : "false");
}

function clearA2uiBusy() {
  Object.keys(a2uiSurfaces).forEach(function (surfaceId) { a2uiSetBusy(surfaceId, false); });
}

// a2uiSettleBusyIfIdle encerra o estado ocupado só quando NÃO há mais turno
// nem ação A2UI na fila; chamada nos terminais do stream.
function a2uiSettleBusyIfIdle() {
  if (chatSending || pendingA2uiAction > 0) return;
  clearA2uiBusy();
  // Turno encerrado: libera a chave de dedupe. Sem isto, um segundo clique
  // LEGÍTIMO no mesmo botão (ex.: avançar 2 passos) podia ser engolido pela
  // janela de 1,2s mesmo depois de o turno terminar. Duplo clique DURANTE o
  // turno continua protegido (o estado busy é setado no dispatch do 1º clique).
  a2uiLastActionKey = "";
}

// a2uiDropOrphanSurfaces remove as surfaces que ficaram sem componentes (o
// createSurface chegou, o updateComponents não). Usado quando o stream termina
// dentro do bloco a2ui — evita o card eterno em "Loading surface...".
function a2uiDropOrphanSurfaces() {
  Object.keys(a2uiSurfaces).forEach(function (surfaceId) {
    var entry = a2uiSurfaces[surfaceId];
    if (entry && !entry.hasComponents) {
      a2uiSurfaceDestroy(surfaceId);
    }
  });
}

// a2uiFinalizeIncompleteSurfaces roda no ENCERRAMENTO do turno. As mensagens
// a2ui do servidor chegam SEMPRE antes do evento terminal (chat:done/error/
// stopped), então se a surface continua sem updateComponents foi porque a linha
// de componentes não chegou (JSON inválido descartado no servidor, validação,
// stream cortado). Não vale esperar os 12s do watchdog: remove a bolha vazia e
// avisa o usuário, em vez de deixar "Loading surface..." na tela.
function a2uiDestroyIncompleteSurfaces() {
  var removed = 0;
  Object.keys(a2uiSurfaces).forEach(function (surfaceId) {
    var entry = a2uiSurfaces[surfaceId];
    if (!entry || entry.hasComponents) return;
    // Rastro no logs.db: o bridge do console encaminha textos com [a2ui].
    // Sem isto o caminho comum (createSurface + updateDataModel sem
    // updateComponents) era destruído sem nenhum registro — o watchdog que
    // logava não chega a disparar porque a surface já não existe.
    console.warn("[a2ui] surface " + surfaceId + " sem updateComponents com root — o card não foi exibido");
    a2uiSurfaceDestroy(surfaceId);
    removed++;
  });
  return removed;
}

function a2uiFinalizeIncompleteSurfaces() {
  // Mesma condição do watchdog: com turno ou ação A2UI em andamento os
  // componentes ainda podem chegar (o terminal pode ter sido processado antes
  // de um lote da fila de polling).
  if (chatSending || pendingA2uiAction > 0) return;
  if (a2uiDestroyIncompleteSurfaces() > 0) showA2uiIncompleteNotice();
}

// onChatA2uiIncomplete recebe o diagnóstico do SERVIDOR (chunk
// "a2ui_incomplete" → evento "chat:a2ui_incomplete"): a surface foi criada mas
// a definição (updateComponents com root) não chegou nem depois da reemissão do
// M2. Aqui a bolha vazia é removida e o usuário vê QUAIS surfaces falharam, em
// vez do aviso genérico do finalizador/watchdog.
function onChatA2uiIncomplete(data) {
  var surfaces = String(data || "").trim();
  console.warn("[a2ui] servidor reportou interface sem definição:", surfaces || "(sem surfaceId)");
  var removed = a2uiDestroyIncompleteSurfaces();
  if (removed === 0 && !surfaces) return;
  if (!chatMessagesEl) return;
  var div = document.createElement("div");
  div.className = "chat-msg assistant";
  div.innerHTML = renderAssistantMarkdown(
    surfaces
      ? translate("chat.a2uiIncompleteDetail", { surfaces: surfaces })
      : translate("chat.a2uiIncomplete"),
  );
  syncColorMode();
  bindInternalChatLinks(div);
  chatMessagesEl.appendChild(div);
  scheduleChatScrollToBottom();
}

// a2uiSurfaceDestroy remove a bolha e destrói o handle de UMA surface.
function a2uiSurfaceDestroy(surfaceId) {
  var entry = a2uiSurfaces[surfaceId];
  if (!entry) return;
  a2uiClearSurfaceWatchdog(entry);
  a2uiThemeDispose(entry);
  try { entry.handle.destroy(); } catch (_) {}
  if (entry.bubble) { try { entry.bubble.remove(); } catch (_) {} }
  delete a2uiSurfaces[surfaceId];
}
// a2uiWarnUnsupportedComponents detecta componentes que o catálogo EMBARCADO
// nesta versão do app não conhece (ex.: API publicada antes do exe novo, com um
// componente recém-criado como `Select`). O renderer ignora o tipo em silêncio
// e o card sai sem o componente; aqui o usuário recebe o aviso no próprio card.
function a2uiWarnUnsupportedComponents(msg, entry) {
  var comps = msg && msg.updateComponents && msg.updateComponents.components;
  if (!Array.isArray(comps)) return;
  var cat = window.A2uiChat && window.A2uiChat.catalog;
  if (!cat || !cat.components || typeof cat.components.has !== "function") return;
  var missing = [];
  for (var i = 0; i < comps.length; i++) {
    var t = comps[i] && comps[i].component;
    if (t && !cat.components.has(t) && missing.indexOf(t) === -1) missing.push(t);
  }
  if (!missing.length) {
    // Payload corrigido numa atualização seguinte: o aviso antigo sai da tela.
    if (entry && entry.bubble) {
      var stale = entry.bubble.querySelector(".a2ui-unsupported");
      if (stale) stale.remove();
      entry.unsupportedNotified = "";
    }
    return;
  }
  console.warn("[a2ui] componentes não suportados pelo renderer desta versão:", missing.join(", "));
  if (!entry || !entry.bubble) return;
  var key = missing.join(",");
  if (entry.unsupportedNotified === key) return;
  entry.unsupportedNotified = key;
  var note = entry.bubble.querySelector(".a2ui-unsupported");
  if (!note) {
    note = document.createElement("div");
    note.className = "a2ui-unsupported";
    entry.bubble.appendChild(note);
  }
  note.textContent = translate("chat.a2uiUnsupported", { components: missing.join(", ") });
  scheduleChatScrollToBottom();
}

// CONTADOR (não bool) de cliques enfileirados durante um stream ativo.
//
// O agent consome EXATAMENTE UMA ação A2UI por turno-sentinela. Com um bool, 3
// cliques durante um turno geravam UM turno e as outras 2 ações ficavam
// engavetadas no agent; o clique seguinte consumia a ação VELHA (fila FIFO) e a
// interface reagia ao pedido anterior, deixando o novo preso na fila (bug
// identificado na revisão de 2026-10-08). Cada ação pendente precisa do seu
// turno.
var pendingA2uiAction = 0;

// maybeProcessPendingA2uiAction dispara o processamento de UMA ação A2UI que
// ficou pendente durante um stream. Chamado nos eventos terminais: o terminal
// do turno seguinte chama de novo, drenando a fila um clique por turno.
function maybeProcessPendingA2uiAction() {
  if (pendingA2uiAction <= 0 || chatSending) return;
  pendingA2uiAction--;
  sendChatMessageWithA2uiAction();
}

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
    fallbackA2uiToMarkdown(msg, null);
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
    // O evento chegou: a interface TENTOU renderizar. Em caso de falha o
    // fallback abaixo já explica — não somar o aviso de "não exibida".
    a2uiRendered = true;

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

    // Roteia por surfaceId: cada surface tem a sua bolha/handle, então uma
    // mensagem nunca é descartada por "surface diferente" (era o que fazia os
    // cards anteriores — e seus botões — sumirem).
    var targetSurfaceId = msgSurfaceId || "discovery-chat-surface";

    // deleteSurface: remove a bolha local também. O MessageProcessor destrói a
    // surface interna, mas a bolha ficaria vazia no chat (e recriar uma surface
    // para uma mensagem de exclusão só piorava).
    if (parsed.deleteSurface) {
      a2uiSurfaceDestroy(targetSurfaceId);
      scheduleChatScrollToBottom();
      return;
    }

    // createSurface → cria a bolha e a surface. O ensureA2uiSurface já envia
    // a mensagem createSurface ao processor (via entry.js), então não reenviamos
    // aqui para evitar duplicação.
    var isCreateSurface = !!parsed.createSurface;
    if (isCreateSurface) {
      ensureA2uiSurface(targetSurfaceId);
    }

    var entry = a2uiSurfaces[targetSurfaceId];
    if (!entry) {
      // Mensagem de surface ainda não criada (ex.: createSurface perdido):
      // cria a default para não perder a interface.
      ensureA2uiSurface(targetSurfaceId);
      entry = a2uiSurfaces[targetSurfaceId];
    }

    if (!entry) {
      // Sem surface não há como renderizar. ANTES isso passava em silêncio
      // (um TypeError no bundle engolia o card sem nenhum aviso ao usuário).
      // Agora avisa e limpa a bolha parcial.
      console.error("[a2ui] surface não pôde ser criada:", targetSurfaceId);
      fallbackA2uiToMarkdown(msg, targetSurfaceId);
      return;
    }

    // Para createSurface, o ensureA2uiSurface já enviou a mensagem ao processor.
    // Para as demais (updateComponents/updateDataModel/deleteSurface), envia.
    if (!isCreateSurface && entry) {
      entry.handle.processMessages([parsed]);
      // SÓ updateComponents COM o componente raiz preenche a surface.
      // updateDataModel NÃO conta como componentes (marcar hasComponents ali
      // cancelava o watchdog e a bolha ficava presa em "Loading surface..."
      // para sempre — caso real de 2026-10-08, surface paper_jam_wizard6).
      // Sem root o renderer também fica em loading: exige o id "root" em vez
      // de confiar só no verbo (fecha o caso de updateComponents vazio ou sem
      // root vindo de servidor antigo/caminho síncrono).
      var comps = parsed.updateComponents && parsed.updateComponents.components;
      var hasRoot = Array.isArray(comps) && comps.some(function (c) { return c && c.id === "root"; });
      if (hasRoot) {
        entry.hasComponents = true;
        a2uiClearSurfaceWatchdog(entry);
      } else if (!entry.hasComponents) {
        // Surface ainda incompleta: mantém o watchdog armado. Se ela JÁ estava
        // completa, um update incremental (sem root) não pode rearmar nada.
        a2uiArmSurfaceWatchdog(targetSurfaceId, entry);
      }
    }
    // Tipo desconhecido pelo bundle embarcado seria ignorado em silêncio.
    if (parsed.updateComponents && entry) {
      a2uiWarnUnsupportedComponents(parsed, entry);
    }
    // updateComponents/updateDataModel criam hosts novos (e novos shadow roots):
    // reaplica o tema depois do processamento.
    a2uiThemeRefresh(targetSurfaceId);
    scheduleChatScrollToBottom();
  } catch (e) {
    // Fallback: se o A2UI falhar (JSON inválido, catalog/surface error, etc.),
    // não deixamos o usuário sem resposta. Renderiza o conteúdo bruto como
    // markdown normal e limpa a surface parcial, se houver.
    console.error("[a2ui] erro ao processar mensagem:", e);
    fallbackA2uiToMarkdown(msg, targetSurfaceId || null);
  }
}

// fallbackA2uiToMarkdown é chamado quando o renderer A2UI falha. Em vez de
// exibir o JSON cru (lixo para o usuário), remove a surface parcial (a do
// surfaceId informado) e mostra uma mensagem amigável. O texto markdown normal
// (fora do bloco a2ui) já foi exibido pelo streaming, então não há perda.
function fallbackA2uiToMarkdown(rawMsg, surfaceId) {
  try {
    if (surfaceId) {
      a2uiSurfaceDestroy(surfaceId);
    }
    // Loga o payload bruto para diagnóstico, mas não o exibe ao usuário.
    console.warn("[a2ui] payload que falhou:", String(rawMsg || ""));
    if (!chatMessagesEl) return;
    var div = document.createElement("div");
    div.className = "chat-msg assistant";
    div.innerHTML = renderAssistantMarkdown(
      translate("chat.a2uiRenderFailed"),
    );
    syncColorMode();
    bindInternalChatLinks(div);
    chatMessagesEl.appendChild(div);
    scheduleChatScrollToBottom();
  } catch (_) {
    // Nunca lançar a partir de um handler de evento.
  }
}

// ensureA2uiSurface cria a bolha de mensagem e a surface A2UI dentro dela para
// o surfaceId informado (reutiliza a existente). Cada surface tem a sua bolha:
// um card novo NÃO destrói os anteriores.
function ensureA2uiSurface(surfaceId) {
  var existing = a2uiSurfaces[surfaceId];
  if (existing) return existing.handle;
  if (!chatMessagesEl) return null;

  var div = document.createElement("div");
  div.className = "chat-msg assistant chat-a2ui";
  div.dataset.surfaceId = surfaceId;

  var contentEl = document.createElement("div");
  contentEl.className = "a2ui-container";
  div.appendChild(contentEl);

  chatMessagesEl.appendChild(div);

  var handle = null;
  try {
    handle = window.A2uiChat.createSurface(contentEl, surfaceId);
    handle.onUserAction(function (action) {
      handleA2uiUserAction(surfaceId, action);
    });
  } catch (e) {
    console.error("[a2ui] falha ao criar surface:", e);
    div.remove();
    return null;
  }

  // hasComponents fica true APENAS quando chega updateComponents (a mensagem
  // que realmente define a árvore). updateDataModel não conta: sem isso, uma
  // surface criada e nunca preenchida ficava presa em "Loading surface..."
  // (resposta truncada no meio do bloco a2ui ou linha updateComponents
  // descartada no servidor por JSON inválido).
  var entry = { handle: handle, bubble: div, theme: null, hasComponents: false };
  a2uiSurfaces[surfaceId] = entry;
  // Tema visual: injetado nos shadow roots do catálogo Lit (o CSS do app não
  // atravessa a fronteira do shadow root — ver A2UI_THEME_CSS).
  a2uiThemeAttach(contentEl, entry);
  // Surface sem updateComponents = card preso em "Loading surface...".
  a2uiArmSurfaceWatchdog(surfaceId, entry);
  syncColorMode();
  scheduleChatScrollToBottom();
  return handle;
}

// ─── Ações LOCAIS (prefixo ui.) — interação DENTRO do card, sem turno ───
//
// Todo clique numa ação normal vira turno no chat (bolha + LLM). Para botões que
// só mudam o que aparece no card (passo a passo, contador, mostrar/ocultar), o
// prefixo `ui.` faz o app resolver a mudança no data model da própria surface:
// instantâneo, sem custo de LLM, sem bolha e funcionando offline. O componente
// precisa estar ligado ao caminho (ex.: {"text":{"path":"/etapaTexto"}}).
//
// SEGURANÇA: aqui só passa estado de APRESENTAÇÃO. Ações com efeito colateral
// (instalar, remover, abrir chamado, executar comando) continuam sendo ações
// normais e SEMPRE passam pelo agente (com a autorização já existente).
var A2UI_LOCAL_ACTION_PREFIX = "ui.";

function isLocalA2uiAction(name) {
  return String(name || "").indexOf(A2UI_LOCAL_ACTION_PREFIX) === 0;
}

function applyLocalA2uiAction(surfaceId, action) {
  var entry = a2uiSurfaces[surfaceId];
  var handle = entry && entry.handle;
  if (!handle || typeof handle.updateDataModel !== "function") {
    console.warn("[a2ui] ação local ignorada (surface/handle sem updateDataModel):", action.name);
    return;
  }
  var ctx = action.context || {};
  // ATENÇÃO: a chave NÃO pode ser `path`. O bind do renderer resolve um objeto
  // com a chave `path` como DataBinding — um context `{"path":"/x"}` tem a
  // chave REMOVIDA do context entregue (verificado no bundle em 2026-10-08: o
  // clique chegou com context {}) e a ação não dispara. Por isso o parâmetro é
  // `target` — não existe fallback para ctx.path.
  var path = typeof ctx.target === "string" ? ctx.target : "";
  if (!path) {
    console.warn("[a2ui] ação local sem 'target' no context:", action.name);
    return;
  }
  var op = String(action.name).slice(A2UI_LOCAL_ACTION_PREFIX.length);
  var current = typeof handle.getDataModel === "function" ? handle.getDataModel(path) : undefined;
  var min = Number(ctx.min);
  var max = Number(ctx.max);
  var next;
  if (op === "set") {
    // Sem 'value' não há o que gravar: gravar undefined apagaria a chave do data
    // model (DataModel.set remove a propriedade) e o componente perderia o valor.
    if (ctx.value === undefined || ctx.value === null) {
      console.warn("[a2ui] ui.set sem 'value' no context (ignorado):", action.name);
      return;
    }
    next = ctx.value;
  } else if (op === "toggle") {
    next = !(current === true || current === "true");
  } else if (op === "next" || op === "prev") {
    var num = Number(current);
    if (!isFinite(num)) num = 0;
    var step = Number(ctx.step);
    if (!isFinite(step) || step === 0) step = 1;
    next = num + (op === "next" ? step : -step);
    if (isFinite(min)) next = Math.max(min, next);
    if (isFinite(max)) next = Math.min(max, next);
    // Preserva o tipo do valor atual: string numérica continua string (os
    // componentes ligados ao mesmo caminho re-renderizam com o novo valor).
    if (typeof current === "string") next = String(next);
  } else {
    console.warn("[a2ui] operação local desconhecida (ignorada):", action.name);
    return;
  }

  try {
    handle.updateDataModel(path, next);

    // 'states' (opcional): um mapa de caminho→valor POR valor de /path. É assim
    // que um passo a passo troca o conteúdo de cada etapa sem ida ao servidor:
    // o cliente escolhe o mapa pelo índice do novo valor.
    //
    // 'statesPath' é a alternativa recomendada: o array fica UMA única vez no
    // data model (via updateDataModel) e cada botão só referencia o caminho.
    // Sem isso o LLM repetia o array inteiro em TODOS os botões — foi o payload
    // gigante que quebrou o JSON do wizard de papel atolado em 2026-10-08.
    var states = Array.isArray(ctx.states) ? ctx.states : null;
    if (!states && typeof ctx.statesPath === "string" && ctx.statesPath) {
      var loadedStates = typeof handle.getDataModel === "function" ? handle.getDataModel(ctx.statesPath) : undefined;
      if (Array.isArray(loadedStates)) {
        states = loadedStates;
      } else {
        console.warn("[a2ui] " + action.name + ": statesPath sem array no data model:", ctx.statesPath);
      }
    }
    if (states && states.length) {
      var base = isFinite(min) ? min : 1;
      var idx = Number(next) - base;
      var stateMap = idx >= 0 && idx < states.length ? states[idx] : null;
      if (stateMap && typeof stateMap === "object") {
        Object.keys(stateMap).forEach(function (key) {
          var p = key.charAt(0) === "/" ? key : "/" + key;
          try {
            handle.updateDataModel(p, stateMap[key]);
          } catch (e2) {
            console.warn("[a2ui] falha ao aplicar estado local em " + p + ":", e2);
          }
        });
      }
    }
  } catch (e) {
    console.warn("[a2ui] falha ao aplicar ação local:", e);
  }
}

// handleA2uiUserAction encaminha uma ação do usuário (clique/input) ao agent.
// Registra a ação via AnswerA2uiAction e dispara um novo StartStream para que
// o agent processe a ação como um tool result no próximo round (resposta
// imediata ao clique, sem exigir que o usuário digite outra mensagem).
//
// Se já houver um stream ativo, a ação fica PENDENTE no agent e é processada
// assim que o stream atual terminar (pendingA2uiAction). Antes, a ação ficava
// solta e era consumida pela PRÓXIMA mensagem digitada — podendo "vazar" para
// um turno sobre outro assunto.
function handleA2uiUserAction(surfaceId, action) {
  if (!action || !action.name) return;

  // Ação LOCAL (ui.*): muda só o card. Não gera bolha, não fica busy e não
  // dispara turno — é o caminho para botões de navegação/estado da interface.
  if (isLocalA2uiAction(action.name)) {
    applyLocalA2uiAction(surfaceId, action);
    return;
  }

  // Clique repetido no MESMO botão (duplo clique, ou clique enquanto o anterior
  // ainda está na fila) é ignorado: sem isso a mesma ação seria enfileirada
  // duas vezes e o programa seria atualizado/instalado duas vezes.
  var actionKey = surfaceId + "|" + action.name + "|" + JSON.stringify(action.context || {});
  var now = Date.now();
  if (a2uiLastActionKey === actionKey && now - a2uiLastActionAt < A2UI_ACTION_DEDUPE_MS) {
    console.warn("[a2ui] clique repetido ignorado (mesma ação em <" + A2UI_ACTION_DEDUPE_MS + "ms):", action.name);
    return;
  }
  a2uiLastActionKey = actionKey;
  a2uiLastActionAt = now;

  try {
    var payload = {
      surfaceId: surfaceId,
      name: action.name,
      context: action.context || {},
    };
    appApi().AnswerA2uiAction(JSON.stringify(payload));
    // Rastro SEMPRE: o clique vira bolha do usuário, inclusive quando entrou na
    // fila durante um turno ativo (antes o flag global escondia esse clique e a
    // ação era processada sem nenhum sinal na interface).
    addChatMessage("user", formatA2uiActionLabel(action));
    // Estado ocupado no card: o componente fica esmaecido enquanto o agente
    // processa a ação (antes o clique não dava nenhum retorno visual imediato).
    a2uiSetBusy(surfaceId, true);
    if (chatSending) {
      // Stream ativo: guarda a ação para disparar o processamento no chat:done.
      // Contador: um turno-sentinela por clique (o agent consome 1 por turno).
      pendingA2uiAction++;
      return;
    }
    sendChatMessageWithA2uiAction();
  } catch (e) {
    console.error("[a2ui] falha ao enviar userAction:", e);
    a2uiSetBusy(surfaceId, false);
  }
}

// sendChatMessageWithA2uiAction dispara o processamento de uma ação A2UI.
// Não adiciona uma bolha de usuário (a ação não é uma mensagem digitada) e
// usa uma sentinela interna que o agent converte em tool result.
function sendChatMessageWithA2uiAction() {
  if (chatSending) return;
  // Sem comunicação com o servidor o chat está indisponível.
  if (chatOfflineActive) {
    showChatOfflineBannerNow();
    return;
  }

  chatStopRequested = false;
  // A sentinela "__a2ui_action__" não é reenfileirável: se o backend recusar,
  // o fluxo A2UI trata pelo terminal do turno em curso (pendingA2uiAction).
  lastDispatchedChatText = "";
  setChatBusy(true);
  resetA2uiTokenFilter();

  // Cria a bolha de streaming para a resposta do agent à ação.
  streamingRawContent = "";
  streamingRafPending = false;
  streamingBubble = document.createElement("div");
  streamingBubble.className = "chat-msg assistant streaming";

  // Widget de activity (mesma estrutura do dispatch por mensagem).
  var thinkingEl = buildChatActivityElement();
  streamingBubble.appendChild(thinkingEl);

  var cursorEl = document.createElement("span");
  cursorEl.className = "stream-cursor";
  streamingBubble.appendChild(cursorEl);

  if (chatMessagesEl) chatMessagesEl.appendChild(streamingBubble);
  scheduleChatScrollToBottom();

  // Timer de segurança: também cobre o stream disparado por ação A2UI, para
  // não deixar "Pensando..." preso se o evento chat:done nunca chegar.
  armChatStreamTimeout();

  // Polling já no dispatch (mesma justificativa do dispatch por mensagem):
  // a resolução do binding não pode ser pré-condição para consumir eventos.
  if (window.__wailsV3Bridge) {
    startPollingLoop();
  }

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

// Limpa TODAS as surfaces A2UI quando o chat é limpo.
function clearA2uiSurface() {
  Object.keys(a2uiSurfaces).forEach(function (surfaceId) {
    a2uiSurfaceDestroy(surfaceId);
  });
  a2uiSurfaces = Object.create(null);
  pendingA2uiAction = 0;
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
    "chat:loop_progress": onChatLoopProgress,
    "chat:budget": onChatBudget,
    "chat:done": onStreamDone,
    "chat:error": onStreamError,
    "chat:stopped": onStreamStopped,
    "chat:question": onChatQuestion,
    "chat:question_cancelled": onChatQuestionCancelled,
    "chat:a2ui": onChatA2ui,
    "chat:a2ui_incomplete": onChatA2uiIncomplete,
    // Captura de tela assistida (app-screenshot.js).
    "screenshot:request": onScreenshotRequest,
    "screenshot:overlay_close": onScreenshotOverlayClose,
    "screenshot:captured": onScreenshotCaptured,
  };

  var nativeListenersRegistered = false;
  // Polling state
  var pollTimerId = null;
  var POLL_INTERVAL_MS = 80; // ~12 polls/segundo — baixa latência, baixo overhead
  // Stream terminal events — paramos o polling ao receber qualquer um deles.
  var STREAM_TERMINAL_EVENTS = { "chat:done": true, "chat:error": true, "chat:stopped": true };

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

    // routePollResult processa o lote retornado por PollChatEvents e informa
    // se algum evento terminal (done/error/stopped) foi processado.
    function routePollResult(result) {
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
      return foundTerminal;
    }

    function poll() {
      if (!chatSending) {
        // Drenagem final ANTES de parar: eventos ainda no buffer do broker
        // (inclusive o terminal) não podem ficar órfãos — chatSending pode
        // ter virado false por outro caminho enquanto o lote estava em voo.
        appApi()
          .PollChatEvents()
          .then(function (result) {
            routePollResult(result);
            stopPollingLoop();
          })
          .catch(function () {
            stopPollingLoop();
          });
        return;
      }

      appApi()
        .PollChatEvents()
        .then(function (result) {
          if (routePollResult(result)) {
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
      Object.keys(CHAT_EVENT_HANDLERS).forEach(function (name) {
        window.wails.on(name, CHAT_EVENT_HANDLERS[name]);
      });
      nativeListenersRegistered = true;
    } else {
      console.error("[chat] fallback nativo indisponível: window.wails.on não é uma função");
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

// __discoveryFocusChatView é chamado pelo backend (ExecJS) quando o usuário
// clica na notificação nativa (resposta concluída ou pergunta aguardando): a
// janela é trazida para frente no Go e aqui a aba de chat é selecionada.
// Quando há uma pergunta pendente, o foco vai para o dock de resposta — é o
// campo que o usuário precisa usar para destravar o turno.
window.__discoveryFocusChatView = function () {
  try {
    if (typeof setActiveTab === "function") setActiveTab("chat");
    var dockVisible =
      !!chatQuestionDock && !chatQuestionDock.classList.contains("hidden");
    if (dockVisible && chatQuestionDockInput) {
      chatQuestionDockInput.focus();
    } else if (chatInputEl) {
      chatInputEl.focus();
    }
    scheduleChatScrollToBottom();
  } catch (e) {
    console.warn("[chat] falha ao focar a aba de chat:", e);
  }
};

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

// ─── Opções de continuação (botões) ────────────────────────────────────────
// O core pede ao modelo para listar opções finais com "- " — o frontend as
// converte em botões e REMOVE as linhas do corpo da mensagem. Sem isso, o
// mesmo conteúdo aparecia duas vezes: como lista no texto e como chips de
// botão abaixo. Regras:
//  - Só o bloco FINAL de linhas "- "/"* " vira botão; listas descritivas no
//    meio do texto (ex.: detalhes de um chamado) permanecem como texto.
//  - Listas numeradas ("1. 2. 3.") são passos sequenciais: NUNCA viram botão
//    (antes disto, passos viravam botões E ficavam no texto — duplicados).
//  - Bloco final com mais de 6 linhas é tratado como lista descritiva.
//  - Normaliza o texto antes (normalizeGluedMarkdownLines) para que a extração
//    veja as mesmas linhas que o renderizador enxerga.
function splitTrailingChatActionOptions(content) {
  var text = String(content || "");
  if (!text.trim()) return { options: [], content: text };

  var lines = normalizeGluedMarkdownLines(text);
  var end = lines.length;
  while (end > 0 && !lines[end - 1].trim()) end -= 1;

  var start = end;
  while (start > 0 && /^[-*]\s+/.test(lines[start - 1].trim())) start -= 1;

  var optionLines = lines.slice(start, end);
  if (!optionLines.length || optionLines.length > 6) {
    return { options: [], content: text };
  }

  var options = [];
  var seen = new Set();
  for (var i = 0; i < optionLines.length; i += 1) {
    var clean = optionLines[i].replace(/^[-*]\s+/, "").trim();
    if (!clean) continue;
    var key = clean.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    var label = clean.length > 120 ? clean.slice(0, 117) + "..." : clean;
    // Remove markdown markers from the action value so the sent text is clean.
    var value = clean.replace(/[*_`~]/g, "").trim();
    options.push({ label: label, value: value || clean });
  }
  if (!options.length) return { options: [], content: text };

  var bodyLines = lines.slice(0, start);
  while (bodyLines.length && !bodyLines[bodyLines.length - 1].trim()) bodyLines.pop();
  return { options: options, content: bodyLines.join("\n") };
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

function stopThinkingStatusUpdates() {
  // Ponto único de teardown do turno: chamado nos terminais (done/error/stopped)
  // e na suspensão da UI. O polling de logs que alimentava o antigo
  // "Pensando..." foi removido como dead code (B18); o que resta desligar aqui é
  // o cronômetro do widget de atividade.
  stopChatActivityTimer();
}

function handleChatUISuspend() {
  stopThinkingStatusUpdates();
}

document.addEventListener("ui:suspend", handleChatUISuspend);

function formatInlineChatMarkdown(text) {
  // Espaço após rótulo em negrito colado ao valor ("**ID:**01a0557e") —
  // delta de espaço perdido no stream; insere espaço quando o ":" fecha o
  // negrito e o caractere seguinte não é espaço/asterisco.
  var raw = String(text || "").replace(
    /(\*\*[^*]+?:\*\*)(?=[^\s*])/g,
    "$1 ",
  );
  var escaped = escapeHtml(raw);
  var codeTokens = [];

  // Token de placeholder SEM underscores/asteriscos/colchetes: o token antigo
  // (\x01CHAT_CODE_N\x01) continha "_CODE_" e era DESTRUIDO pelo regex de
  // itálico (/_..._/g) que roda depois da tokenização — o restore falhava e o
  // texto cru ("CHATCODE0") vazava para o usuário. \u0001C + índice + \u0001
  // não colide com nenhum dos regex seguintes.
  escaped = escaped.replace(/`([^`\n]+)`/g, function (_, code) {
    var token = "\u0001C" + codeTokens.length + "\u0001";
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
    // Replacer por FUNÇÃO: o HTML do código é inserido literalmente (conteúdo
    // com "$&", "$1" etc. não é interpretado como padrão de substituição).
    var tokenHTML = codeTokens[i];
    escaped = escaped.replace("\u0001C" + i + "\u0001", function () {
      return tokenHTML;
    });
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

    // Deep link para um artigo especifico da Base de Conhecimento:
    // discovery://knowledge/article/<articleId> (ou ?articleId=/<id>).
    var articleId =
      parsed.searchParams.get("articleId") ||
      parsed.searchParams.get("article") ||
      "";
    if (
      !articleId &&
      segments[0] === "knowledge" &&
      segments[1] === "article" &&
      segments[2]
    ) {
      articleId = segments[2];
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
    return { tab: tabBySegment, ticketId: ticketId, articleId: articleId };
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

  if (route.tab === "knowledge" && route.articleId) {
    try {
      var opened = await openKnowledgeArticleById(route.articleId);
      if (!opened) {
        showToast(translate("chat.openKnowledgeArticleError"), "error");
      }
    } catch (err) {
      showToast(translate("chat.openKnowledgeArticleError"), "error");
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

// ─── Normalização de texto "colado" (defesa em profundidade) ───────────────
// Quando um delta de whitespace do LLM se perde no stream (causa raiz
// corrigida na API — AiChatStreamingOrchestrator descartava tokens só de
// espaço/quebra), o markdown chega com blocos colados: título na mesma linha
// da tabela, itens de lista ordenada grudados no parágrafo e rótulos em
// negrito colados ao próximo item. Isto repara os casos conhecidos sem
// alterar texto íntegro. Conteúdo dentro de fences ``` nunca é alterado.
function normalizeGluedMarkdownLines(content) {
  var lines = String(content || "").replace(/\r\n/g, "\n").split("\n");
  var out = [];
  var inCode = false;
  for (var i = 0; i < lines.length; i += 1) {
    var line = lines[i];
    if (/^\s*```/.test(line)) {
      inCode = !inCode;
      out.push(line);
      continue;
    }
    if (inCode) {
      out.push(line);
      continue;
    }

    // a) Tabela colada ao texto/título: "### Título| Col A | Col B |" ou
    //    "...foram: | Col A | Col B |" → quebra antes da tabela. O sufixo
    //    precisa de >= 3 pipes e o prefixo não pode conter "|" — assim
    //    linhas de tabela legítimas (começam com "|") nunca são divididas.
    var gluedTable = line.match(/^([^|\r\n]*[^\s|])\s*(\|.+\|.+\|.*)$/);
    // a2) O prefixo (com tabela) ou a própria linha (sem tabela) passa pelas
    //     regras de itens colados; o trecho da tabela sai intacto.
    pushSplitGluedItems(gluedTable ? gluedTable[1] : line);
    if (gluedTable) {
      out.push(gluedTable[2]);
      continue;
    }
  }
  return out;

  // splitGluedItems aplica as regras de "itens colados" a um trecho de texto:
  //
  //  b) Itens de lista ordenada colados: "...travando.2. **Item**..." e
  //     "lento1. **Item**..." (também "#### Título1. **Item**..."). Exige o
  //     item em negrito (\d+\. **), sem espaço antes do número — texto
  //     íntegro como "versão 2. **X**" não é afetado.
  //  c) Rótulo em negrito colado ao próximo item: "...sistema**- **ID:**..."
  //     e "...d2ddf- **Categoria:**..." → quebra antes de cada "- **...**".
  //  d) Item em negrito introduzido por ":"/"." no meio do parágrafo:
  //     "...foram: - **OnScreen Control**..." — as quebras de linha do
  //     modelo se perderam no stream, mas os espaços ficaram; quebra antes
  //     do item para restaurar a lista.
  function splitGluedItems(seg) {
    seg = seg.replace(/([a-zà-ÿA-ZÀ-þ)\]])(\.?)(\d+\.\s+\*\*)/g, "$1$2\n$3");
    seg = seg.replace(/(\*\*)(- \*\*[^*]+?\*\*)/g, "$1\n$2");
    seg = seg.replace(/([^\s*|])(- \*\*[^*]+?\*\*)/g, "$1\n$2");
    seg = seg.replace(/([:.])\s+(- \*\*[^*]+?\*\*)/g, "$1\n$2");
    return seg;
  }

  function pushSplitGluedItems(seg) {
    seg = splitGluedItems(seg);
    if (seg.indexOf("\n") >= 0) {
      var parts = seg.split("\n");
      for (var p = 0; p < parts.length; p += 1) out.push(parts[p]);
      return;
    }
    out.push(seg);
  }
}

function renderAssistantMarkdown(content) {
  content = stripRawToolCalls(content);
  var lines = normalizeGluedMarkdownLines(content);
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

  // Bloco ```json/jsonl/a2ui em que TODAS as linhas são mensagens A2UI v0.9
  // (version + um dos verbos). É serialização interna do protocolo: em vez de
  // despejar uma parede de JSON na conversa, o bloco fica recolhido — o usuário
  // vê a interface e abre o JSON só se quiser.
  function isA2uiJsonBlock(lang, lines) {
    if (!/^(json|jsonl|a2ui)$/i.test(lang || "")) return false;
    var count = 0;
    for (var i = 0; i < lines.length; i++) {
      var s = String(lines[i]).trim();
      if (!s) continue;
      if (s.charAt(0) !== "{") return false;
      if (!/"version"\s*:/.test(s)) return false;
      if (!/"(createSurface|updateComponents|updateDataModel|deleteSurface)"\s*:/.test(s)) return false;
      count++;
    }
    return count > 0;
  }

  function flushCodeBlock() {
    var langClass = codeLang
      ? ' class="lang-' + escapeHtmlAttr(codeLang) + '"'
      : "";
    var codeHtml =
      '<pre class="chat-code"><code' +
      langClass +
      ">" +
      escapeHtml(codeLines.join("\n")) +
      "</code></pre>";
    if (isA2uiJsonBlock(codeLang, codeLines)) {
      html.push(
        '<details class="chat-a2ui-json"><summary>' +
          escapeHtml(translate("chat.a2uiJsonToggle")) +
          "</summary>" +
          codeHtml +
          "</details>",
      );
    } else {
      html.push(codeHtml);
    }
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

  // findTableSeparator localiza a linha separadora de uma tabela cujo
  // cabeçalho está em startIdx, tolerando linhas em branco entre elas
  // (modelos LLM emitem "| a | b |\n\n|---|---|" com frequência — sem esta
  // tolerância a tabela não era reconhecida e os pipes apareciam crus).
  function findTableSeparator(startIdx) {
    for (var k = startIdx + 1; k < lines.length && k <= startIdx + 4; k += 1) {
      var candidate = lines[k].trim();
      if (!candidate) continue;
      return isSeparatorRow(candidate) ? k : -1;
    }
    return -1;
  }

  function renderTable(startIdx, sepIdx) {
    var headerCells = parseTableCells(lines[startIdx]);
    var aligns = parseTableAlign(lines[sepIdx]);
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
    // Corpo tolerante a linhas em branco entre as rows (mesmo caso do
    // cabeçalho): 1 blank não encerra; a tabela acaba em linha não-vazia
    // que não seja row, ou em 2+ blanks consecutivos.
    var r = sepIdx + 1;
    var blanks = 0;
    while (r < lines.length) {
      var rowLine = lines[r].trim();
      if (!rowLine) {
        blanks += 1;
        if (blanks >= 2) break;
        r += 1;
        continue;
      }
      blanks = 0;
      if (!isTableRow(rowLine)) break;
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

    if (isTableRow(line)) {
      var sepIdx = findTableSeparator(i);
      if (sepIdx > 0) {
        closeLists();
        var tbl = renderTable(i, sepIdx);
        html.push(tbl.html);
        i = tbl.nextIndex - 1;
        continue;
      }
    }

    // Aceita headings com ou sem espaco apos os # (ex.: "##📊 Titulo" ou "## Titulo").
    // Modelos LLM frequentemente geram headings sem espaco quando seguidos de emoji.
    var heading = line.match(/^(#{1,6})\s*(.+)$/);
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

// insertBeforeStreamingBubble insere um nó ANTES da bolha de streaming ativa.
// A bolha nasce no início do turno e só recebe o texto final no fim; sem isso,
// artefatos do meio do turno (prints capturados pela IA, cards informativos)
// apareceriam DEPOIS da resposta final — foi a ordem quebrada vista no turno
// de 2026-10-01 12:01Z (2 prints listados após a resposta).
function insertBeforeStreamingBubble(node) {
  if (!chatMessagesEl || !node) return;
  if (streamingBubble && streamingBubble.parentNode === chatMessagesEl) {
    chatMessagesEl.insertBefore(node, streamingBubble);
  } else {
    chatMessagesEl.appendChild(node);
  }
  scheduleChatScrollToBottom();
}

// ensureStreamingBubbleAtEnd é a rede de segurança do fim do turno: garante que
// a bolha com a resposta final seja o último elemento da conversa.
// ensureStreamingBubbleAtEnd reposiciona a bolha do turno no fim SOMENTE quando
// o que veio depois dela são artefatos do meio do turno (prints da IA, respostas
// do dock de pergunta). Se houver algo que pertence ao FIM da resposta — em
// especial superfícies A2UI (.chat-a2ui, o card interativo) — a bolha fica onde
// está, senão o texto final passaria por cima do card.
function ensureStreamingBubbleAtEnd() {
  if (!chatMessagesEl || !streamingBubble) return;
  if (streamingBubble.parentNode !== chatMessagesEl) return;
  var node = streamingBubble.nextElementSibling;
  var artifact = false;
  while (node) {
    var isArtifact =
      node.classList &&
      (node.classList.contains("screenshot-info") || node.classList.contains("chat-question-answer"));
    if (!isArtifact) return;
    artifact = true;
    node = node.nextElementSibling;
  }
  if (artifact) {
    chatMessagesEl.appendChild(streamingBubble);
  }
}

function addChatMessage(role, content) {
  if (!chatMessagesEl) return;
  var div = document.createElement("div");
  div.className = "chat-msg " + role;

  var actionSplit = { options: [], content: content };
  if (role === "assistant") {
    actionSplit = splitTrailingChatActionOptions(content);
    div.innerHTML = renderAssistantMarkdown(
      actionSplit.options.length > 0 ? actionSplit.content : content,
    );
    syncColorMode();
    bindInternalChatLinks(div);
  } else {
    div.textContent = content;
  }

  if (role === "assistant" && actionSplit.options.length > 0) {
    appendChatQuickActions(div, actionSplit.options);
  }

  chatMessagesEl.appendChild(div);
  scheduleChatScrollToBottom();
  return div;
}

async function sendChatMessage() {
  if (!chatInputEl) return;
  var text = chatInputEl.value.trim();
  if (!text) return;

  // Sem comunicação com o servidor o chat está indisponível: não despacha.
  if (chatOfflineActive) {
    showChatOfflineBannerNow();
    return;
  }

  // Envio manual já desconectado: mostra o aviso na hora (sem esperar a
  // histerese) e bloqueia o composer.
  var lastConn = typeof window.__lastConnectivityState === "function"
    ? window.__lastConnectivityState()
    : null;
  if (lastConn && lastConn.connected === false) {
    showChatOfflineBannerNow();
    return;
  }

  // Fila estrita (FIFO): se existe QUALQUER mensagem esperando — inclusive no
  // intervalo em que o chat acabou de ficar livre e o flush ainda não rodou —
  // a nova mensagem entra ATRÁS dela. Antes o teste era apenas chatSending,
  // então um envio recém-digitado ultrapassava a fila (LIFO na prática, apesar
  // de o shift() da fila ser FIFO) e a conversa perdia a ordem.
  if (chatSending || chatMessageQueue.length > 0) {
    // Chat processando: enfileira e limpa o input. A mensagem entra no
    // contexto na primeira oportunidade (maybeFlushChatQueue) e leva junto os
    // prints que estavam anexados no momento do envio.
    var queuedImages =
      typeof screenshotTakePendingImages === "function" ? screenshotTakePendingImages() : [];
    if (!queueChatMessage(text, queuedImages)) {
      // Fila cheia: os prints voltam para o composer e o texto fica no input, em
      // vez de os anexos serem consumidos e nunca enviados.
      if (queuedImages.length > 0 && typeof screenshotRestoreAttachments === "function") {
        screenshotRestoreAttachments(queuedImages);
      }
      return;
    }
    chatInputEl.value = "";
    autoGrowChatInput();
    // Chat livre com fila pendente (janela entre o terminal do stream e o
    // flush): despacha a MAIS ANTIGA — a ordem de chegada é preservada.
    if (!chatSending) maybeFlushChatQueue();
    return;
  }

  chatInputEl.value = "";
  dispatchChatMessage(text);
}

// dispatchChatMessage faz o dispatch real de uma mensagem do usuário (bolha +
// StartChatStream). Quem chama garante que o chat está livre (chatSending=false).
// `images` é o snapshot de anexos que veio junto da mensagem enfileirada; quando
// ausente (envio direto), pega o que está pendente no composer.
function dispatchChatMessage(text, images) {
  // Ponto único de dispatch: cobre o envio direto, a ação A2UI e a fila.
  if (chatOfflineActive) {
    showChatOfflineBannerNow();
    return;
  }
  addChatMessage("user", text);

  // Prints anexados pelo usuário (ícone de câmera do composer): vão junto do
  // texto no primeiro round e viram conteúdo multimodal no servidor.
  var attachedImages =
    images && images.length
      ? images
      : typeof screenshotTakePendingImages === "function"
        ? screenshotTakePendingImages()
        : [];
  if (attachedImages.length > 0 && typeof screenshotRenderSentImages === "function") {
    screenshotRenderSentImages(attachedImages);
  }

  chatStopRequested = false;
  lastDispatchedChatText = text;
  // Guardado para o reenfileiramento por "turno em andamento": sem isso o print
  // era perdido quando o backend recusava o envio.
  lastDispatchedChatImages = attachedImages;
  setChatBusy(true);
  resetA2uiTokenFilter();

  // Create the streaming bubble immediately.
  streamingRawContent = "";
  streamingRafPending = false;
  streamingLastRound = 0;
  streamingBubble = document.createElement("div");
  streamingBubble.className = "chat-msg assistant streaming";

  // Widget de activity: spinner + rótulo + trilha de passos + pill de etapa.
  var thinkingEl = buildChatActivityElement();
  streamingBubble.appendChild(thinkingEl);

  var cursorEl = document.createElement("span");
  cursorEl.className = "stream-cursor";
  streamingBubble.appendChild(cursorEl);

  if (chatMessagesEl) chatMessagesEl.appendChild(streamingBubble);
  scheduleChatScrollToBottom();

  // Timer de segurança: garante que "Pensando..." não fique preso se o
  // evento chat:done nunca chegar (erro de rede, goroutine perdida, etc.).
  armChatStreamTimeout();

  // Polling inicia JÁ no dispatch (não dentro do .then do StartChatStream):
  // se a resolução do binding se perder (Wails v3 beta), o transporte de
  // eventos ficaria sem consumidor e o turno inteiro não apareceria na tela —
  // travamento visto em produção em 20/09 ("Ele nao esta abrindo").
  // startPollingLoop é idempotente (guard de pollTimerId).
  if (window.__wailsV3Bridge) {
    startPollingLoop();
  }

  try {
    // StartChatStream returns immediately; response arrives via events.
    var api = appApi();
    var sendPromise;
    var imagesSent = false;
    if (attachedImages.length > 0) {
      if (typeof api.StartChatStreamWithImages === "function") {
        sendPromise = api.StartChatStreamWithImages(text, JSON.stringify(attachedImages));
        imagesSent = true;
      } else if (typeof screenshotRestoreAttachments === "function") {
        // Build antigo: devolve os prints ao composer em vez de descartá-los.
        screenshotRestoreAttachments(attachedImages);
      }
    }
    if (!sendPromise) {
      sendPromise = api.StartChatStream(text);
    }
    sendPromise
      .then(function () {
        // Runtime nativo (WebView2) usa polling; no navegador, o
        // debug-http-bridge já conecta ao SSE via window.wails.on.
        if (window.__wailsV3Bridge) {
          startPollingLoop();
        }
      })
      .catch(function (err) {
        // O envio falhou: não perder os prints anexados. Exceção: recusa por
        // "turno em andamento" — nesse caso o onStreamError reenfileira a
        // mensagem COM as imagens, então devolvê-las ao composer duplicaria.
        if (imagesSent && typeof screenshotRestoreAttachments === "function" && !isChatBusyError(String(err))) {
          screenshotRestoreAttachments(attachedImages);
        }
        // Falha de transporte/servidor no dispatch: reforça o aviso mesmo que
        // o evento de conectividade não tenha chegado — mas nunca quando o
        // estado real já é sabidamente online (timeout de turno longo).
        if (isLikelyOfflineChatError(String(err)) && shouldFlagChatOffline()) {
          showChatOfflineBannerNow();
        }
        onStreamError(String(err));
      });
  } catch (err) {
    if (attachedImages.length > 0 && typeof screenshotRestoreAttachments === "function") {
      screenshotRestoreAttachments(attachedImages);
    }
    if (isLikelyOfflineChatError(String(err)) && shouldFlagChatOffline()) {
      showChatOfflineBannerNow();
    }
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
    // Privacidade: campo ausente (config antiga) = prévia habilitada.
    if (chatNotifyPreviewEl) chatNotifyPreviewEl.checked = cfg.notifyPreview !== false;
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
      notifyPreview: chatNotifyPreviewEl ? !!chatNotifyPreviewEl.checked : true,
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

// ── Aviso de conectividade do chat ─────────────────────────────────────────
// O chat permanece disponível offline (histórico/digitação), mas as respostas
// da IA dependem do servidor. Sem aviso, a única pista era a bolha de erro
// depois do envio.
var chatOfflineBannerTimer = null;

// offlineNow mostra o aviso na hora (sem a histerese de 15s do indicador) —
// usado quando o próprio usuário tenta enviar já desconectado. Passa pelo
// applyChatOfflineBanner para o texto acompanhar o canal que caiu.
function showChatOfflineBannerNow() {
  applyChatOfflineBanner(false);
}

// chatOfflineActive guarda o estado para que setChatBusy/send não reabilitem o
// composer enquanto não houver comunicação com o servidor.
var chatOfflineActive = false;

// applyChatOfflineMode deixa o chat INDISPONÍVEL offline: sem comunicação o
// turno não chega ao servidor, então o composer é bloqueado (além do aviso).
function applyChatOfflineMode(offline) {
  chatOfflineActive = !!offline;
  if (chatInputEl) chatInputEl.disabled = chatOfflineActive;
  if (chatSendBtn) chatSendBtn.disabled = chatOfflineActive;
  ["chatScreenshotBtn", "chatQuestionDockSendBtn", "chatQuestionDockInput"].forEach(function (id) {
    var el = document.getElementById(id);
    if (el) el.disabled = chatOfflineActive;
  });
}

// applyChatSendAffordance deixa explícito no próprio botão de envio que o
// servidor está inacessível (title/aria), além do banner.
function applyChatSendAffordance(offline) {
  if (!chatSendBtn) return;
  var label = offline ? translate("chat.sendOfflineHint") : translate("action.send");
  chatSendBtn.title = label;
  chatSendBtn.setAttribute("aria-label", label);
}

// isLikelyOfflineChatError reconhece falha de transporte/servidor no dispatch
// (mesmo que o evento de conectividade não tenha chegado).
function isLikelyOfflineChatError(message) {
  var m = String(message || "").toLowerCase();
  if (!m) return false;
  return (
    m.indexOf("deadline exceeded") >= 0 ||
    m.indexOf("timeout") >= 0 ||
    m.indexOf("connection refused") >= 0 ||
    m.indexOf("no such host") >= 0 ||
    m.indexOf("unreachable") >= 0 ||
    m.indexOf("network is") >= 0 ||
    m.indexOf("unexpected eof") >= 0 ||
    m.indexOf("connection closed") >= 0 ||
    m.indexOf("broken pipe") >= 0 ||
    m.indexOf("http 502") >= 0 ||
    m.indexOf("http 503") >= 0 ||
    m.indexOf("http 504") >= 0
  );
}

// shouldFlagChatOffline: uma falha no dispatch só justifica o aviso de "sem
// comunicação" quando o estado real NÃO é sabidamente online. Ex.: "timeout"
// casa com isLikelyOfflineChatError mas também acontece em turnos longos com a
// conexão perfeita — antes isso piscava o banner de offline por até 5s.
function shouldFlagChatOffline() {
  var last = typeof window.__lastConnectivityState === "function"
    ? window.__lastConnectivityState()
    : null;
  if (!last) return true; // estado desconhecido: mantém o reforço do aviso
  return last.connected !== true;
}

// chatOfflineBannerKey escolhe o aviso conforme o canal que caiu:
//   - API HTTP inacessível (NATS de pé) → "servidor de gestão indisponível";
//   - transporte fora → "sem conexão com o servidor";
//   - sem detalhe (evento antigo/serviço ausente) → aviso genérico.
function chatOfflineBannerKey(detail) {
  if (detail && detail.apiReachable === false) return "chat.offlineBannerApi";
  if (detail && detail.transport) return "chat.offlineBannerTransport";
  return "chat.offlineBanner";
}

// __connectivityPollUpdate é chamado pelo poll de status do app-window.js
// (GetStatusOverview a cada 4s) com o estado REAL do core. É o caminho
// confiável no runtime nativo, onde a entrega de eventos custom do Wails pode
// falhar — sem ele o aviso do chat podia nunca refletir uma queda/volta real.
window.__connectivityPollUpdate = function (status) {
  if (!status || typeof status.connected !== "boolean") return;
  var detail = { transport: String(status.connectionType || status.transport || "") };
  if (!status.connected && status.transportConnected) {
    // Transporte de pé e agente offline ⇒ quem caiu foi a API HTTP.
    detail.apiReachable = false;
  }
  applyChatOfflineBanner(status.connected, detail);
};

// applyChatOfflineBanner sincroniza o aviso e o bloqueio do chat.
// IMPORTANTE: usa o estado BRUTO de conectividade (sem a histerese de 15s do
// indicador global) — o chat precisa ficar indisponível assim que não houver
// comunicação, não 15s depois. A histerese continua valendo só para o
// indicador de status.
function applyChatOfflineBanner(rawConnected, detail) {
  var banner = document.getElementById("chatOfflineBanner");
  var connected;
  var connDetail = detail || null;
  if (typeof rawConnected === "boolean") {
    connected = rawConnected;
  } else {
    var last = typeof window.__lastConnectivityState === "function"
      ? window.__lastConnectivityState()
      : null;
    if (!last) {
      // Ainda sem estado conhecido: não bloqueia nem avisa.
      if (banner) banner.classList.add("hidden");
      applyChatSendAffordance(false);
      return;
    }
    connected = !!last.connected;
    connDetail = last;
  }
  if (banner) {
    banner.classList.toggle("hidden", !!connected);
    if (!connected) {
      var key = chatOfflineBannerKey(connDetail);
      // Mantém data-i18n em sincronia: trocar o idioma reaplica o aviso certo.
      if (banner.getAttribute("data-i18n") !== key) {
        banner.setAttribute("data-i18n", key);
      }
      banner.textContent = translate(key);
      var reason = connDetail && connDetail.reason ? String(connDetail.reason) : "";
      if (reason) {
        banner.title = reason;
      } else {
        banner.removeAttribute("title");
      }
    } else if (banner.getAttribute("data-i18n") !== "chat.offlineBanner") {
      // Online: volta ao texto genérico para não deixar um aviso específico
      // "congelado" no elemento para o próximo idioma/estado.
      banner.setAttribute("data-i18n", "chat.offlineBanner");
      banner.textContent = translate("chat.offlineBanner");
      banner.removeAttribute("title");
    }
  }
  applyChatSendAffordance(!connected);
  applyChatOfflineMode(!connected);
}

var chatConnectivityBound = false;

function initChatConnectivityBanner() {
  applyChatOfflineBanner();
  // Idempotente: initChat pode ser reexecutado sem duplicar listeners.
  if (chatConnectivityBound) return;
  if (window.wails && typeof window.wails.on === "function") {
    chatConnectivityBound = true;
    // Só aplica com booleano explícito do backend: evento sem o campo
    // "connected" (payload parcial/versão antiga) NÃO pode ser lido como
    // offline — era esse o sintoma de "servidor offline" com o servidor no ar.
    var handler = function (data) {
      if (!data || typeof data.connected !== "boolean") return;
      applyChatOfflineBanner(data.connected, data);
    };
    window.wails.on("agent:connectivity", handler);
    window.wails.on("agent:status_snapshot", handler);
  }
  // Fallback: reavalia periodicamente (o poll de status também roda ~5s),
  // mantendo o aviso em sincronia mesmo se um evento se perder.
  if (!chatOfflineBannerTimer) {
    chatOfflineBannerTimer = setInterval(applyChatOfflineBanner, 5000);
  }
}

// chat:info — mensagem informativa do backend FORA de um turno (ex.: a acao de
// energia foi CANCELADA pelo usuario depois que o turno terminal ja tinha
// encerrado a conversa). Sem isso o usuario nao recebia nenhum retorno.
var chatInfoBound = false;

function initChatInfoListener() {
  if (chatInfoBound) return;
  if (!(window.wails && typeof window.wails.on === "function")) return;
  chatInfoBound = true;
  window.wails.on("chat:info", function (data) {
    var text = "";
    if (data && typeof data === "object") {
      text = String(data.text || "");
    } else if (typeof data === "string") {
      text = data;
    }
    if (!text.trim()) return;
    try {
      addChatMessage("assistant", text);
    } catch (e) {
      console.error("[chat] falha ao exibir chat:info:", e);
    }
  });
}

function initChat() {
  initChatConnectivityBanner();
  initChatInfoListener();
  if (chatSendBtn) {
    chatSendBtn.addEventListener("click", sendChatMessage);
  }
  // Captura de tela (ícone de câmera + anexos do composer).
  if (typeof initScreenshotCapture === "function") {
    initScreenshotCapture();
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
    // Auto-grow do composer (cap 200px via CSS; depois rola internamente).
    chatInputEl.addEventListener("input", autoGrowChatInput);
    autoGrowChatInput();
  }
  if (chatConfigBtn && chatConfigPanel) {
    chatConfigBtn.addEventListener("click", function () {
      chatConfigPanel.classList.toggle("hidden");
      if (chatToolsPanel) chatToolsPanel.classList.add("hidden");
      loadChatConfig();
    });
  }
  // Ferramentas do chat: painel de diagnóstico — só em modo debug (mesma
  // regra do botão Memórias; a visibilidade dinâmica também é tratada em
  // applyRuntimeTabVisibility).
  if (chatToolsBtn) {
    chatToolsBtn.classList.toggle("hidden", !isDebugRuntimeMode());
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
        // Turno em curso: sem isto o cronômetro do widget continuaria rodando a
        // cada segundo sobre uma bolha já destacada (e streamingBubble seguiria
        // apontando para um nó fora do documento) até o evento terminal.
        stopThinkingStatusUpdates();
        clearChatStreamTimeout();
        streamingBubble = null;
        streamingRawContent = "";
        streamingRafPending = false;
        resetA2uiTokenFilter();
        if (chatMessagesEl) chatMessagesEl.innerHTML = "";
        if (typeof screenshotClearAttachments === "function") {
          screenshotClearAttachments();
        }
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
