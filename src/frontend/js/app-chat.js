"use strict";

var chatSending = false;
var chatStopRequested = false;
var chatThinkingPollId = null;
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
  // Enviar permanece habilitado durante o processamento: enquanto ocupado,
  // ele enfileira a mensagem (mesmo comportamento de outros chats/agentes).
  if (chatSendBtn) chatSendBtn.disabled = false;
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
    if (streamingBubble && !streamingRawContent) {
      var thinkingEl = streamingBubble.querySelector(".stream-thinking");
      if (thinkingEl) {
        thinkingEl.style.display = "";
        setChatActivityText(thinkingEl, translate("chat.streamSlow"));
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

var chatMessageQueue = [];
var CHAT_MESSAGE_QUEUE_MAX = 10;

// autoGrowChatInput ajusta a altura do textarea ao conteúdo (cap 200px).
function autoGrowChatInput() {
  if (!chatInputEl) return;
  chatInputEl.style.height = "auto";
  // Cap de ~3 linhas (line-height 1.35 × 0.9rem ≈ 20px/linha; 60px ≈ 3 linhas),
  // espelhado no max-height de .chat-input — antes crescia até 200px, virando
  // um campo do tamanho de um parágrafo (reduzido de 88px para 60px).
  chatInputEl.style.height = Math.min(chatInputEl.scrollHeight, 60) + "px";
}

function queueChatMessage(text) {
  if (!chatMessagesEl) return;
  if (chatMessageQueue.length >= CHAT_MESSAGE_QUEUE_MAX) {
    showFeedback(translate("chat.queueFull", { max: CHAT_MESSAGE_QUEUE_MAX }), true);
    return;
  }

  var item = { text: text, el: null };
  var div = document.createElement("div");
  div.className = "chat-msg user chat-queued";
  div.title = text;

  var textEl = document.createElement("span");
  textEl.className = "chat-queued-text";
  textEl.textContent = text;
  div.appendChild(textEl);

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

  chatMessagesEl.appendChild(div);
  item.el = div;
  chatMessageQueue.push(item);
  scheduleChatScrollToBottom();
}

function removeQueuedChatMessage(item) {
  var idx = chatMessageQueue.indexOf(item);
  if (idx !== -1) chatMessageQueue.splice(idx, 1);
  if (item.el && item.el.parentNode) item.el.parentNode.removeChild(item.el);
}

// maybeFlushChatQueue despacha a próxima mensagem em fila quando o chat fica
// livre. É chamada nos terminais do stream (done/error/stop), sempre APÓS
// maybeProcessPendingA2uiAction — a ação A2UI solicitada pelo assistente tem
// prioridade; a fila aguarda o terminal do processamento dela.
function maybeFlushChatQueue() {
  if (chatSending || !chatMessageQueue.length) return;
  var next = chatMessageQueue.shift();
  if (next.el && next.el.parentNode) next.el.parentNode.removeChild(next.el);
  dispatchChatMessage(next.text);
}

// ─── Activity do agent (status polido: conectar → ferramentas → resposta) ───
// O core envia status crus em pt-BR ("Um instante...",
// "Analisando sua solicitacao...", "Executando: list_tickets, ..."). Este
// bloco converte em
// rótulos amigáveis, humaniza nomes de tools MCP e mantém uma trilha de
// passos (chips ✓) — em vez do texto único que saltava a cada evento.
var CHAT_ACTIVITY_MAX_STEPS = 4;
var chatActivityRoundMax = 0;

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
  "ping_host": "Testando conectividade (ping)",
  "flush_dns": "Limpando cache DNS",
  "memory/list": "Consultando memórias",
  "memory/create": "Salvando anotação",
  "memory/delete": "Removendo anotação",
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
    var tools = s
      .slice("Executando:".length)
      .replace(/\.+$/, "")
      .split(",")
      .map(function (x) { return x.trim(); })
      .filter(Boolean);
    return { kind: "tools", tools: tools };
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
  label.textContent = translate("chat.thinking");
  head.appendChild(label);
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

// pushChatActivityStep move a step "current" anterior para "done" e adiciona
// a nova. A trilha mantém no máximo CHAT_ACTIVITY_MAX_STEPS concluídos.
function pushChatActivityStep(root, text, state) {
  var stepsEl = chatActivityStepsEl(root);
  if (!stepsEl) return;
  var prev = stepsEl.querySelector(".chat-step.current");
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
  var max = maxRounds > 0 ? maxRounds : chatActivityRoundMax;
  pill.textContent = max > 0
    ? translate("chat.activity.round", { round: round, max: max })
    : translate("chat.activity.roundOnly", { round: round });
  if (maxRounds > 0) chatActivityRoundMax = maxRounds;
  pill.classList.add("visible");
}

function onStreamThinking(status) {
  if (document.hidden || window.__discoveryUISuspended) return;
  if (!streamingBubble) return;
  var thinkingEl = streamingBubble.querySelector(".stream-thinking");
  if (!thinkingEl) return;
  if (streamingRawContent) return; // resposta começou: activity oculta
  thinkingEl.style.display = "";

  var desc = describeChatActivity(status);
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
    setChatActivityText(
      thinkingEl,
      names.length === 1
        ? translate("chat.activity.executingOne", { tool: names[0] })
        : translate("chat.activity.executingMany", { count: names.length }),
    );
    var shown = names.slice(0, 2).join(", ");
    if (names.length > 2) shown += " +" + (names.length - 2);
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

// onChatLoopProgress recebe o progresso do agent loop emitido pelo servidor
// (chunk "loop_progress": round atual / máximo de rounds). Atualiza o
// indicador "Pensando..." para "Processando… round X/Y", eliminando a
// sensação de travamento durante tool chains longas. Payload pode chegar
// como objeto {round, maxRounds} (Wails) ou string JSON (SSE/publish).
function onChatLoopProgress(data) {
  if (document.hidden || window.__discoveryUISuspended) return;
  if (!streamingBubble) return;
  var payload = data;
  if (typeof data === "string") {
    try { payload = JSON.parse(data); } catch (e) { return; }
  }
  if (!payload || typeof payload !== "object") return;
  var round = Number(payload.round) || 0;
  var maxRounds = Number(payload.maxRounds) || 0;
  if (maxRounds <= 0 || round <= 0) return;
  if (streamingRawContent) return; // resposta começou: activity oculta
  var thinkingEl = streamingBubble.querySelector(".stream-thinking");
  if (!thinkingEl) return;
  thinkingEl.style.display = "";
  // Progresso do agent loop vira uma pill discreta ("Etapa 2 de 10") em vez
  // de reescrever o rótulo principal — o status corrente permanece visível.
  updateChatActivityRound(thinkingEl, round, maxRounds);
}

function finaliseStreamingBubble() {
  if (!streamingBubble) return;
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
  } else if (!streamingRawContent.trim()) {
    // Stream morreu no meio de um bloco a2ui/DSML e não há texto visível:
    // o evento chat:a2ui nunca chegará (o servidor só extrai o bloco no fim).
    // Mostra um aviso em vez de deixar o usuário sem resposta.
    streamingRawContent = translate("chat.responseInterrupted");
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
  safeFinaliseStreamingBubble();
  chatStopRequested = false;
  setChatBusy(false);
  if (chatInputEl) chatInputEl.focus();
  // Ação A2UI que chegou durante o stream: processa agora que o chat está livre.
  maybeProcessPendingA2uiAction();
  // Fila de mensagens: despacha a próxima na primeira oportunidade.
  maybeFlushChatQueue();
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
      queueChatMessage(requeueText);
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
    safeFinaliseStreamingBubble();
    chatStopRequested = false;
    setChatBusy(false);
    if (chatInputEl) chatInputEl.focus();
    // Ação A2UI pendente não deve "vazar" para a próxima mensagem digitada:
    // processa agora que o chat está livre (mesmo após stop).
    maybeProcessPendingA2uiAction();
    // Fila: despacha a próxima mensagem pendente mesmo após stop.
    maybeFlushChatQueue();
    return;
  }

  if (streamingBubble) {
    // Show whatever content arrived; fallback to error text if nothing came.
    if (!streamingRawContent) {
      streamingRawContent = translate("chat.errorUnknown", {
        error: String(errMsg || translate("common.unknown")),
      });
    }
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
  safeFinaliseStreamingBubble();
  chatStopRequested = false;
  setChatBusy(false);
  if (chatInputEl) chatInputEl.focus();
  // Processa ação A2UI pendente também após stop manual.
  maybeProcessPendingA2uiAction();
  // Fila: despacha a próxima mensagem pendente após o stop.
  maybeFlushChatQueue();
}

// ─── Mini-Questionário Interativo (ask_user MCP) ───

// Perguntas pendentes: enquanto houver, o timer de segurança do stream (60s)
// fica PAUSADO — o backend espera o usuário responder sem limite de tempo, e
// a UI não deve dar o stream por morto nem enfileirar/despachar por conta
// própria nesse período.
var chatPendingQuestionCount = 0;

function onChatQuestion(data) {
  try {
    var q = typeof data === "string" ? JSON.parse(data) : data;
    if (!q || !q.id) return;
    chatPendingQuestionCount += 1;
    // Pausa o timeout de segurança: a espera pela resposta não tem prazo.
    clearChatStreamTimeout();
    // Indica no indicador de streaming que o chat está aguardando o usuário.
    if (streamingBubble && !streamingRawContent) {
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

function showChatQuestion(question) {
  if (!chatMessagesEl) return;

  // Dedupe: reemissão do mesmo id (retry/retransmissão do backend) não cria
  // uma segunda bolha — a pergunta duplicada aparecendo DEPOIS da resposta do
  // usuário quebrava a ordem visual da conversa.
  var existingQuestions = chatMessagesEl.querySelectorAll(".chat-question");
  for (var qi = 0; qi < existingQuestions.length; qi += 1) {
    if (existingQuestions[qi].dataset.questionId === question.id) return;
  }

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
        // Destaca a opção escolhida antes de desabilitar o painel.
        btn.classList.add("chat-question-option-selected");
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
        e.preventDefault();
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
    if (!payload || !payload.id || !chatMessagesEl) return;
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

// ─── A2UI (Agent-to-User Interface) — interfaces ricas geradas por IA ───

// Estado da surface A2UI ativa no chat.
var a2uiSurfaceHandle = null;
var a2uiSurfaceBubble = null;
// True quando uma ação A2UI foi submetida durante um stream ativo e precisa
// ser processada (novo StartChatStream) assim que o stream atual terminar.
var pendingA2uiAction = false;

// maybeProcessPendingA2uiAction dispara o processamento de uma ação A2UI que
// ficou pendente durante um stream. Chamado nos eventos terminais do stream.
function maybeProcessPendingA2uiAction() {
  if (!pendingA2uiAction || chatSending) return;
  pendingA2uiAction = false;
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

  // Destrói surface anterior e REMOVE a bolha antiga do DOM — sem isso a
  // bolha antiga ficava órfã (sem handle) e se acumulava a cada nova surface.
  if (a2uiSurfaceHandle) {
    try { a2uiSurfaceHandle.destroy(); } catch (_) {}
    a2uiSurfaceHandle = null;
  }
  if (a2uiSurfaceBubble) {
    a2uiSurfaceBubble.remove();
    a2uiSurfaceBubble = null;
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
//
// Se já houver um stream ativo, a ação fica PENDENTE no agent e é processada
// assim que o stream atual terminar (pendingA2uiAction). Antes, a ação ficava
// solta e era consumida pela PRÓXIMA mensagem digitada — podendo "vazar" para
// um turno sobre outro assunto.
function handleA2uiUserAction(surfaceId, action) {
  if (!action || !action.name) return;
  try {
    var payload = {
      surfaceId: surfaceId,
      name: action.name,
      context: action.context || {},
    };
    appApi().AnswerA2uiAction(JSON.stringify(payload));
    if (chatSending) {
      // Stream ativo: guarda a ação para disparar o processamento no chat:done.
      pendingA2uiAction = true;
      return;
    }
    sendChatMessageWithA2uiAction();
  } catch (e) {
    console.error("[a2ui] falha ao enviar userAction:", e);
  }
}

// sendChatMessageWithA2uiAction dispara o processamento de uma ação A2UI.
// Não adiciona uma bolha de usuário (a ação não é uma mensagem digitada) e
// usa uma sentinela interna que o agent converte em tool result.
function sendChatMessageWithA2uiAction() {
  if (chatSending) return;

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

// Limpa a surface A2UI quando o chat é limpo.
function clearA2uiSurface() {
  if (a2uiSurfaceHandle) {
    try { a2uiSurfaceHandle.destroy(); } catch (_) {}
    a2uiSurfaceHandle = null;
  }
  if (a2uiSurfaceBubble) {
    a2uiSurfaceBubble.remove();
    a2uiSurfaceBubble = null;
  }
  pendingA2uiAction = false;
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
    "chat:done": onStreamDone,
    "chat:error": onStreamError,
    "chat:stopped": onStreamStopped,
    "chat:question": onChatQuestion,
    "chat:question_cancelled": onChatQuestionCancelled,
    "chat:a2ui": onChatA2ui,
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

// B18: startThinkingStatusUpdates removida — era dead code (sem callers).
// Se fosse ativada, faria GetLogs() a cada 900ms durante o "thinking".
// stopThinkingStatusUpdates permanece (usada pelo handler ui:suspend).

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
  if (!chatInputEl) return;
  var text = chatInputEl.value.trim();
  if (!text) return;

  if (chatSending) {
    // Chat processando: enfileira e limpa o input. A mensagem entra no
    // contexto na primeira oportunidade (maybeFlushChatQueue).
    queueChatMessage(text);
    chatInputEl.value = "";
    autoGrowChatInput();
    return;
  }

  chatInputEl.value = "";
  dispatchChatMessage(text);
}

// dispatchChatMessage faz o dispatch real de uma mensagem do usuário (bolha +
// StartChatStream). Quem chama garante que o chat está livre (chatSending=false).
function dispatchChatMessage(text) {
  addChatMessage("user", text);

  chatStopRequested = false;
  lastDispatchedChatText = text;
  setChatBusy(true);
  resetA2uiTokenFilter();

  // Create the streaming bubble immediately.
  streamingRawContent = "";
  streamingRafPending = false;
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
    appApi()
      .StartChatStream(text)
      .then(function () {
        // Runtime nativo (WebView2) usa polling; no navegador, o
        // debug-http-bridge já conecta ao SSE via window.wails.on.
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
