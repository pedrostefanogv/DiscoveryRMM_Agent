"use strict";

var chatSending = false;
var chatStopRequested = false;
var chatThinkingPollId = null;

// Streaming state
var streamingBubble = null;
var streamingRawContent = '';
var streamingRafPending = false;

function flushStreamingContent() {
  streamingRafPending = false;
  if (!streamingBubble) return;
  var contentEl = streamingBubble.querySelector('.stream-content');
  if (!contentEl) {
    contentEl = document.createElement('div');
    contentEl.className = 'stream-content';
    var thinkingEl = streamingBubble.querySelector('.stream-thinking');
    if (thinkingEl) {
      streamingBubble.insertBefore(contentEl, thinkingEl);
      thinkingEl.style.display = 'none';
    } else {
      streamingBubble.appendChild(contentEl);
    }
  }
  contentEl.innerHTML = renderAssistantMarkdown(streamingRawContent);
  bindInternalChatLinks(contentEl);
  scheduleChatScrollToBottom();
}

function setChatBusy(isBusy) {
  chatSending = !!isBusy;
  if (chatSendBtn) chatSendBtn.disabled = !!isBusy;
  if (chatStopBtn) {
    chatStopBtn.classList.toggle('hidden', !isBusy);
    chatStopBtn.disabled = !isBusy;
    chatStopBtn.textContent = translate('action.stop');
  }
}

function requestStopChatStream() {
  if (!chatSending) return;
  chatStopRequested = true;
  if (chatStopBtn) {
    chatStopBtn.disabled = true;
    chatStopBtn.textContent = translate('chat.stopping');
  }
  try {
    appApi().StopChatStream().catch(function () {
      // If backend stop fails, UI still waits stream terminal event.
    });
  } catch (_) {
    // ignore
  }
}

function onStreamToken(token) {
  streamingRawContent += token;
  if (document.hidden || window.__discoveryUISuspended) {
    return;
  }
  if (!streamingRafPending) {
    streamingRafPending = true;
    requestAnimationFrame(flushStreamingContent);
  }
}

function onStreamThinking(status) {
  if (document.hidden || window.__discoveryUISuspended) return;
  if (!streamingBubble) return;
  var thinkingEl = streamingBubble.querySelector('.stream-thinking');
  if (!thinkingEl) return;
  if (!streamingRawContent) {
    thinkingEl.style.display = '';
    thinkingEl.textContent = status || translate('chat.thinking');
    scheduleChatScrollToBottom();
  }
}

function finaliseStreamingBubble() {
  if (!streamingBubble) return;
  // Flush any remaining buffered content immediately.
  streamingRafPending = false;
  flushStreamingContent();

  // Remove streaming indicators.
  var thinkingEl = streamingBubble.querySelector('.stream-thinking');
  if (thinkingEl) thinkingEl.remove();
  var cursor = streamingBubble.querySelector('.stream-cursor');
  if (cursor) cursor.remove();
  streamingBubble.classList.remove('streaming');

  // Add quick-action buttons if applicable.
  var finalContent = streamingRawContent;
  var dynamicActions = extractChatActionOptions(finalContent);
  if (dynamicActions.length > 0) {
    appendChatQuickActions(streamingBubble, dynamicActions);
  } else if (shouldSuggestChatActions(finalContent)) {
    appendChatQuickActions(streamingBubble, null);
  }

  streamingBubble = null;
  streamingRawContent = '';
  scheduleChatScrollToBottom();
}

function onStreamDone() {
  stopThinkingStatusUpdates();
  finaliseStreamingBubble();
  chatStopRequested = false;
  setChatBusy(false);
  if (chatInputEl) chatInputEl.focus();
}

function onStreamError(errMsg) {
  stopThinkingStatusUpdates();

  if (chatStopRequested) {
    if (streamingBubble && !streamingRawContent) {
      streamingRawContent = translate('chat.responseInterrupted');
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
      streamingRawContent = translate('chat.errorUnknown', { error: String(errMsg || translate('common.unknown')) });
    }
    finaliseStreamingBubble();
  } else {
    addChatMessage('assistant', translate('chat.errorUnknown', { error: String(errMsg || translate('common.unknown')) }));
  }
  setChatBusy(false);
  if (chatInputEl) chatInputEl.focus();
}

function onStreamStopped() {
  stopThinkingStatusUpdates();
  if (streamingBubble && !streamingRawContent) {
    streamingRawContent = translate('chat.responseInterrupted');
  }
  finaliseStreamingBubble();
  chatStopRequested = false;
  setChatBusy(false);
  if (chatInputEl) chatInputEl.focus();
}

// Register Wails event listeners once the runtime is ready.
(function registerChatStreamEvents() {
  function doRegister() {
    if (window.runtime && window.runtime.EventsOn) {
      window.runtime.EventsOn('chat:token', onStreamToken);
      window.runtime.EventsOn('chat:thinking', onStreamThinking);
      window.runtime.EventsOn('chat:done', onStreamDone);
      window.runtime.EventsOn('chat:error', onStreamError);
      window.runtime.EventsOn('chat:stopped', onStreamStopped);
    }
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', doRegister);
  } else {
    // Runtime may not be injected yet - defer slightly.
    setTimeout(doRegister, 200);
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

function shouldSuggestChatActions(content) {
  var text = String(content || '').toLowerCase();
  if (!text) return false;
  return /confirme|confirmacao|pode prosseguir|posso prosseguir|deseja que eu|quer que eu|autoriza|aprova|aguardo.*confirmacao/.test(text);
}

function extractChatActionOptions(content) {
  var text = String(content || '');
  if (!text) return [];

  var lines = text.split(/\r?\n/);
  var options = [];
  var seen = new Set();

  function pushOption(raw) {
    var clean = String(raw || '')
      .replace(/^[-*]\s+/, '')
      .replace(/^\d+\.\s+/, '')
      .trim();
    if (!clean) return;

    var key = clean.toLowerCase();
    if (seen.has(key)) return;
    seen.add(key);

    var label = clean.length > 52 ? (clean.slice(0, 49) + '...') : clean;
    options.push({ label: label, value: clean });
  }

  for (var i = 0; i < lines.length; i += 1) {
    var line = String(lines[i] || '').trim();
    if (/^[-*]\s+/.test(line) || /^\d+\.\s+/.test(line)) {
      pushOption(line);
    }
  }

  // Keep UI concise even if the assistant listed many alternatives.
  return options.slice(0, 6);
}

function appendChatQuickActions(containerEl, actionOptions) {
  if (!containerEl || !chatMessagesEl) return;
  var actions = document.createElement('div');
  actions.className = 'chat-msg-actions';

  var options = actionOptions && actionOptions.length
    ? actionOptions
    : [
      { label: 'Confirmar', value: 'Confirmo. Pode prosseguir.' },
      { label: 'Cancelar', value: 'Cancelar. Nao execute nenhuma acao.' },
      { label: 'Sim', value: 'Sim, pode executar.' },
      { label: 'Nao', value: 'Nao, por enquanto nao.' },
    ];

  options.forEach(function (item) {
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn subtle btn-xs';
    btn.textContent = item.label;
    btn.addEventListener('click', function () {
      if (chatSending || !chatInputEl) return;
      chatInputEl.value = item.value;
      sendChatMessage();
    });
    actions.appendChild(btn);
  });

  containerEl.appendChild(actions);
}

function parseChatProgressLine(line) {
  var raw = String(line || '');
  if (!raw.startsWith('[chat] ')) return '';
  var text = raw.replace(/^\[chat\]\s*/, '');

  if (text.indexOf('mensagem recebida') >= 0) return 'Entendendo sua solicitacao...';
  if (text.indexOf('ferramentas disponiveis') >= 0) return 'Preparando ferramentas...';
  if (text.indexOf('rodada de ferramentas') >= 0) return 'Analisando e planejando a melhor acao...';
  if (text.indexOf('chamando ferramenta:') >= 0) {
    var name = text.split('chamando ferramenta:')[1] || '';
    name = name.trim();
    return name ? ('Executando: ' + name + '...') : 'Executando ferramenta...';
  }
  if (text.indexOf('executada com sucesso') >= 0) return 'Acao concluida com sucesso, preparando resposta...';
  if (text.indexOf('retornou erro') >= 0) return 'Houve um erro na acao. Ajustando resposta...';
  if (text.indexOf('resposta final') >= 0) return 'Finalizando resposta...';
  return '';
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

document.addEventListener('ui:suspend', handleChatUISuspend);

function startThinkingStatusUpdates(thinkingEl) {
  stopThinkingStatusUpdates();
  if (!thinkingEl) return;

  var busy = false;
  var lastStatus = '';
  chatThinkingPollId = setInterval(async function () {
    if (busy) return;
    busy = true;
    try {
      var lines = await appApi().GetLogs();
      var status = '';
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
  var escaped = escapeHtml(String(text || ''));
  var codeTokens = [];

  escaped = escaped.replace(/`([^`\n]+)`/g, function (_, code) {
    var token = '__CHAT_CODE_' + codeTokens.length + '__';
    codeTokens.push('<code>' + code + '</code>');
    return token;
  });

  escaped = escaped.replace(/\[([^\]]+)\]\(((?:https?:\/\/|(?:discovery|app):\/\/)[^\s)]+)\)/g, function (_, label, url) {
    var safeLabel = String(label || '').trim();
    if (/^(?:discovery|app):\/\//i.test(url)) {
      var parts = safeLabel.split('|').map(function (p) { return p.trim(); }).filter(Boolean);
      if (parts.length >= 2) {
        var title = parts[0];
        var subtitle = parts[1];
        var meta = parts.slice(2).join(' - ');
        return '<a href="#" class="chat-internal-link chat-internal-card" data-internal-url="' + escapeHtmlAttr(url) + '">' +
          '<span class="chat-internal-card-title">' + title + '</span>' +
          '<span class="chat-internal-card-subtitle">' + subtitle + '</span>' +
          (meta ? '<span class="chat-internal-card-meta">' + meta + '</span>' : '') +
        '</a>';
      }
      return '<a href="#" class="chat-internal-link" data-internal-url="' + escapeHtmlAttr(url) + '">' + safeLabel + '</a>';
    }
    return '<a href="' + url + '" target="_blank" rel="noopener noreferrer">' + safeLabel + '</a>';
  });

  escaped = escaped
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/__([^_]+)__/g, '<strong>$1</strong>')
    .replace(/\*([^*\n]+)\*/g, '<em>$1</em>')
    .replace(/_([^_\n]+)_/g, '<em>$1</em>');

  for (var i = 0; i < codeTokens.length; i += 1) {
    escaped = escaped.replace('__CHAT_CODE_' + i + '__', codeTokens[i]);
  }

  return escaped;
}

function parseInternalAppRoute(url) {
  try {
    var parsed = new URL(String(url || ''));
    var scheme = (parsed.protocol || '').replace(':', '').toLowerCase();
    if (scheme !== 'discovery' && scheme !== 'app') return null;

    var segments = [];
    if (parsed.hostname) segments.push(parsed.hostname.toLowerCase());
    if (parsed.pathname) {
      segments = segments.concat(parsed.pathname.split('/').filter(Boolean).map(function (s) { return s.toLowerCase(); }));
    }

    var ticketId = parsed.searchParams.get('ticketId') || parsed.searchParams.get('id') || '';
    if (!ticketId && segments[0] === 'support' && segments[1] === 'ticket' && segments[2]) {
      ticketId = segments[2];
    }

    var tabBySegment;
    switch (segments[0]) {
      case 'support':
      case 'tickets':
        tabBySegment = 'support';
        break;
      case 'store':
        tabBySegment = 'store';
        break;
      case 'updates':
        tabBySegment = 'updates';
        break;
      case 'inventory':
        tabBySegment = 'inventory';
        break;
      case 'logs':
        tabBySegment = 'logs';
        break;
      case 'chat':
        tabBySegment = 'chat';
        break;
      case 'knowledge':
        tabBySegment = 'knowledge';
        break;
      case 'debug':
        tabBySegment = 'debug';
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
    showToast(translate('chat.invalidInternalLink', { url: String(url || '') }), 'error');
    return;
  }

  setActiveTab(route.tab);

  if (route.tab === 'support') {
    await loadSupportTickets();
    if (route.ticketId) {
      try {
        var ticket = await appApi().GetSupportTicketDetails(route.ticketId);
        showTicketDetail(ticket);
      } catch (err) {
        showToast(translate('chat.openTicketError', { error: String(err) }), 'error');
      }
    }
  }
}

function bindInternalChatLinks(containerEl) {
  if (!containerEl) return;
  var links = containerEl.querySelectorAll('a.chat-internal-link[data-internal-url]');
  links.forEach(function (link) {
    if (link.dataset.boundInternalClick === '1') return;
    link.dataset.boundInternalClick = '1';
    link.addEventListener('click', function (e) {
      e.preventDefault();
      var internalURL = link.getAttribute('data-internal-url') || '';
      navigateInternalAppRoute(internalURL);
    });
  });
}

function renderAssistantMarkdown(content) {
  var lines = String(content || '').replace(/\r\n/g, '\n').split('\n');
  var html = [];
  var inCode = false;
  var codeLang = '';
  var codeLines = [];
  var inUl = false;
  var inOl = false;

  function closeLists() {
    if (inUl) {
      html.push('</ul>');
      inUl = false;
    }
    if (inOl) {
      html.push('</ol>');
      inOl = false;
    }
  }

  function flushCodeBlock() {
    var langClass = codeLang ? (' class="lang-' + escapeHtmlAttr(codeLang) + '"') : '';
    html.push('<pre class="chat-code"><code' + langClass + '>' + escapeHtml(codeLines.join('\n')) + '</code></pre>');
    inCode = false;
    codeLang = '';
    codeLines = [];
  }

  function isTableRow(s) {
    return /^\|([^|\r\n]+\|)+\s*$/.test(s.trim());
  }

  function isSeparatorRow(s) {
    return /^\|(\s*:?-{2,}:?\s*\|)+\s*$/.test(s.trim());
  }

  function parseTableCells(s) {
    return s.trim().replace(/^\|/, '').replace(/\|\s*$/, '').split('|').map(function (c) { return c.trim(); });
  }

  function parseTableAlign(s) {
    return parseTableCells(s).map(function (c) {
      if (/^:-+:$/.test(c)) return 'center';
      if (/-+:$/.test(c)) return 'right';
      return 'left';
    });
  }

  function renderTable(startIdx) {
    var headerCells = parseTableCells(lines[startIdx]);
    var aligns = parseTableAlign(lines[startIdx + 1]);
    var out = '<div class="chat-table-wrap"><table class="chat-table"><thead><tr>';
    for (var c = 0; c < headerCells.length; c += 1) {
      out += '<th style="text-align:' + (aligns[c] || 'left') + '">' + formatInlineChatMarkdown(headerCells[c]) + '</th>';
    }
    out += '</tr></thead><tbody>';
    var r = startIdx + 2;
    while (r < lines.length && isTableRow(lines[r])) {
      var cells = parseTableCells(lines[r]);
      out += '<tr>';
      for (var c2 = 0; c2 < headerCells.length; c2 += 1) {
        out += '<td style="text-align:' + (aligns[c2] || 'left') + '">' + formatInlineChatMarkdown(cells[c2] || '') + '</td>';
      }
      out += '</tr>';
      r += 1;
    }
    out += '</tbody></table></div>';
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
      codeLang = fence[1] || '';
      continue;
    }

    var line = raw.trim();
    if (!line) {
      closeLists();
      continue;
    }

    if (isTableRow(line) && (i + 1) < lines.length && isSeparatorRow(lines[i + 1].trim())) {
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
      html.push('<h' + level + '>' + formatInlineChatMarkdown(heading[2]) + '</h' + level + '>');
      continue;
    }

    if (/^>\s+/.test(line)) {
      closeLists();
      html.push('<blockquote>' + formatInlineChatMarkdown(line.replace(/^>\s+/, '')) + '</blockquote>');
      continue;
    }

    if (/^[-*]\s+/.test(line)) {
      if (inOl) {
        html.push('</ol>');
        inOl = false;
      }
      if (!inUl) {
        html.push('<ul>');
        inUl = true;
      }
      html.push('<li>' + formatInlineChatMarkdown(line.replace(/^[-*]\s+/, '')) + '</li>');
      continue;
    }

    if (/^\d+\.\s+/.test(line)) {
      if (inUl) {
        html.push('</ul>');
        inUl = false;
      }
      if (!inOl) {
        html.push('<ol>');
        inOl = true;
      }
      html.push('<li>' + formatInlineChatMarkdown(line.replace(/^\d+\.\s+/, '')) + '</li>');
      continue;
    }

    closeLists();
    html.push('<p>' + formatInlineChatMarkdown(line) + '</p>');
  }

  if (inCode) {
    flushCodeBlock();
  }
  closeLists();

  return html.join('');
}

function addChatMessage(role, content) {
  if (!chatMessagesEl) return;
  var div = document.createElement('div');
  div.className = 'chat-msg ' + role;

  if (role === 'assistant') {
    div.innerHTML = renderAssistantMarkdown(content);
    bindInternalChatLinks(div);
  } else {
    div.textContent = content;
  }

  if (role === 'assistant') {
    var dynamicActions = extractChatActionOptions(content);
    if (dynamicActions.length > 0) {
      appendChatQuickActions(div, dynamicActions);
    } else if (shouldSuggestChatActions(content)) {
      appendChatQuickActions(div, null);
    }
  }

  chatMessagesEl.appendChild(div);
  scheduleChatScrollToBottom();
  return div;
}

function removeChatThinking() {
  if (!chatMessagesEl) return;
  stopThinkingStatusUpdates();
  var thinking = chatMessagesEl.querySelector('.chat-msg.thinking');
  if (thinking) {
    thinking.remove();
    scheduleChatScrollToBottom();
  }
}

async function sendChatMessage() {
  if (chatSending || !chatInputEl) return;
  var text = chatInputEl.value.trim();
  if (!text) return;

  chatInputEl.value = '';
  addChatMessage('user', text);

  chatStopRequested = false;
  setChatBusy(true);

  // Create the streaming bubble immediately.
  streamingRawContent = '';
  streamingRafPending = false;
  streamingBubble = document.createElement('div');
  streamingBubble.className = 'chat-msg assistant streaming';

  var thinkingEl = document.createElement('div');
  thinkingEl.className = 'stream-thinking';
  thinkingEl.textContent = translate('chat.thinking');
  streamingBubble.appendChild(thinkingEl);

  var cursorEl = document.createElement('span');
  cursorEl.className = 'stream-cursor';
  streamingBubble.appendChild(cursorEl);

  if (chatMessagesEl) chatMessagesEl.appendChild(streamingBubble);
  scheduleChatScrollToBottom();

  try {
    // StartChatStream returns immediately; response arrives via events.
    appApi().StartChatStream(text).catch(function (err) {
      onStreamError(String(err));
    });
  } catch (err) {
    onStreamError(String(err));
  }
}

async function loadChatConfig() {
  try {
    var cfg = await appApi().GetChatConfig();
    if (chatEndpointEl) chatEndpointEl.value = cfg.endpoint || '';
    if (chatModelEl) chatModelEl.value = cfg.model || '';
    if (chatMaxTokensEl) {
      var maxTokens = Number(cfg.maxTokens || 0);
      chatMaxTokensEl.value = maxTokens > 0 ? String(maxTokens) : '';
    }
    if (chatSystemPromptEl) chatSystemPromptEl.value = cfg.systemPrompt || '';
    // Don't set API key - it's masked
  } catch (_) {}
}

async function saveChatConfig() {
  var endpoint = chatEndpointEl ? chatEndpointEl.value.trim() : '';
  var apiKey = chatApiKeyEl ? chatApiKeyEl.value.trim() : '';
  var model = chatModelEl ? chatModelEl.value.trim() : '';
  var maxTokensRaw = chatMaxTokensEl ? chatMaxTokensEl.value.trim() : '';
  var systemPrompt = chatSystemPromptEl ? chatSystemPromptEl.value.trim() : '';
  var maxTokens = 0;

  if (maxTokensRaw) {
    maxTokens = Number(maxTokensRaw);
    if (!Number.isFinite(maxTokens) || maxTokens < 0) {
      showFeedback(translate('chat.maxTokensValidation'), true);
      return;
    }
    maxTokens = Math.floor(maxTokens);
  }

  try {
    await appApi().SetChatConfig({ endpoint: endpoint, apiKey: apiKey, model: model, systemPrompt: systemPrompt, maxTokens: maxTokens });
    showFeedback(translate('chat.configSavedSuccess'));
    if (chatConfigPanel) chatConfigPanel.classList.add('hidden');
  } catch (err) {
    showFeedback(translate('chat.configSaveError', { error: String(err) }), true);
  }
}

async function testChatConfig() {
  var endpoint = chatEndpointEl ? chatEndpointEl.value.trim() : '';
  var apiKey = chatApiKeyEl ? chatApiKeyEl.value.trim() : '';
  var model = chatModelEl ? chatModelEl.value.trim() : '';
  var maxTokensRaw = chatMaxTokensEl ? chatMaxTokensEl.value.trim() : '';
  var systemPrompt = chatSystemPromptEl ? chatSystemPromptEl.value.trim() : '';
  var maxTokens = 0;

  if (maxTokensRaw) {
    maxTokens = Number(maxTokensRaw);
    if (!Number.isFinite(maxTokens) || maxTokens < 0) {
      showFeedback(translate('chat.maxTokensValidation'), true);
      return;
    }
    maxTokens = Math.floor(maxTokens);
  }

  if (chatTestConfigBtn) chatTestConfigBtn.disabled = true;
  try {
    showFeedback(translate('chat.configTesting'));
    var reply = await appApi().TestChatConfig({ endpoint: endpoint, apiKey: apiKey, model: model, systemPrompt: systemPrompt, maxTokens: maxTokens });
    var normalized = String(reply || '').trim();
    showFeedback(translate('chat.configTestSuccess', { suffix: normalized ? ': ' + normalized : '' }));
  } catch (err) {
    showFeedback(translate('chat.configTestFailure', { error: String(err) }), true);
  } finally {
    if (chatTestConfigBtn) chatTestConfigBtn.disabled = false;
  }
}

async function loadChatTools() {
  if (!chatToolsList) return;
  try {
    var tools = await appApi().GetAvailableTools();
    chatToolsList.innerHTML = (tools || []).map(function (t) {
      return '<span class="chat-tool-badge" title="' + escapeHtml(t.description) + '">' +
        escapeHtml(t.name) +
      '</span>';
    }).join('');
  } catch (_) {
    chatToolsList.innerHTML = '<span class="meta">' + escapeHtml(translate('chat.toolsLoadError')) + '</span>';
  }
}

async function loadChatDebugLogs() {
  if (!chatLogsOutput) return;
  try {
    var lines = await appApi().GetLogs();
    var chatLines = (lines || []).filter(function (line) {
      return String(line).startsWith('[chat]');
    });
    chatLogsOutput.textContent = chatLines.length ? chatLines.join('\n') : translate('chat.noLogsYet');
    chatLogsOutput.scrollTop = chatLogsOutput.scrollHeight;
  } catch (err) {
    chatLogsOutput.textContent = translate('chat.logsLoadError', { error: String(err) });
  }
}

async function loadChatMemories() {
  if (!chatMemoriesList) return;
  try {
    var notes = await appApi().GetLocalMemories();
    if (!notes || !notes.length) {
      chatMemoriesList.innerHTML = '<div class="meta">' + escapeHtml(translate('chat.noMemoryFound')) + '</div>';
      return;
    }

    var html = notes.map(function (n) {
      var created = n.createdAt ? formatDate(n.createdAt, '') : '';
      var updated = n.updatedAt ? formatDate(n.updatedAt, '') : '';
      return '<div class="chat-memory-item">' +
        '<div class="chat-memory-meta"><span>' + escapeHtml(created) + '</span>' +
        (updated && updated !== created ? ' <span>' + escapeHtml(translate('chat.updatedAt', { date: updated })) + '</span>' : '') +
        '</div>' +
        '<div class="chat-memory-content">' + escapeHtml(n.content) + '</div>' +
        '<div class="chat-memory-actions">' +
        '<button class="btn danger chat-memory-delete-btn" data-id="' + escapeHtml(String(n.id)) + '">' + escapeHtml(translate('action.delete')) + '</button>' +
        '</div>' +
      '</div>';
    }).join('');

    chatMemoriesList.innerHTML = html;

    // Attach delete handlers
    var deleteButtons = chatMemoriesList.querySelectorAll('.chat-memory-delete-btn');
    deleteButtons.forEach(function (btn) {
      btn.addEventListener('click', function () {
        var id = parseInt(btn.getAttribute('data-id'), 10);
        if (!Number.isFinite(id)) return;
        deleteChatMemory(id);
      });
    });
  } catch (err) {
    chatMemoriesList.innerHTML = '<div class="meta">' + escapeHtml(translate('chat.memoriesLoadError', { error: String(err) })) + '</div>';
  }
}

function openChatMemoriesModal() {
  if (!chatMemoriesModal) return;
  chatMemoriesModal.classList.remove('hidden');
  chatMemoriesModal.setAttribute('aria-hidden', 'false');
  loadChatMemories();
}

function closeChatMemoriesModal() {
  if (!chatMemoriesModal) return;
  chatMemoriesModal.classList.add('hidden');
  chatMemoriesModal.setAttribute('aria-hidden', 'true');
}

function deleteChatMemory(id) {
  if (!chatMemoriesList) return;
  appApi().DeleteLocalMemory(id).then(function () {
    loadChatMemories();
  }).catch(function (err) {
    showFeedback(translate('chat.memoryDeleteError', { error: String(err) }), true);
  });
}

function openChatLogsModal() {
  if (!chatLogsModal) return;
  chatLogsModal.classList.remove('hidden');
  chatLogsModal.setAttribute('aria-hidden', 'false');
  loadChatDebugLogs();
}

function closeChatLogsModal() {
  if (!chatLogsModal) return;
  chatLogsModal.classList.add('hidden');
  chatLogsModal.setAttribute('aria-hidden', 'true');
}

function initChat() {
  if (chatSendBtn) {
    chatSendBtn.addEventListener('click', sendChatMessage);
  }
  if (chatStopBtn) {
    chatStopBtn.addEventListener('click', requestStopChatStream);
  }
  if (chatInputEl) {
    chatInputEl.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        sendChatMessage();
      }
    });
  }
  if (chatConfigBtn && chatConfigPanel) {
    chatConfigBtn.addEventListener('click', function () {
      chatConfigPanel.classList.toggle('hidden');
      if (chatToolsPanel) chatToolsPanel.classList.add('hidden');
      loadChatConfig();
    });
  }
  if (chatToolsBtn && chatToolsPanel) {
    chatToolsBtn.addEventListener('click', function () {
      chatToolsPanel.classList.toggle('hidden');
      if (chatConfigPanel) chatConfigPanel.classList.add('hidden');
      loadChatTools();
    });
  }
  if (chatLogsBtn) {
    chatLogsBtn.addEventListener('click', openChatLogsModal);
  }
  if (chatLogsCloseBtn) {
    chatLogsCloseBtn.addEventListener('click', closeChatLogsModal);
  }
  if (chatLogsRefreshBtn) {
    chatLogsRefreshBtn.addEventListener('click', loadChatDebugLogs);
  }
  if (chatLogsModal) {
    chatLogsModal.addEventListener('click', function (e) {
      if (e.target === chatLogsModal) closeChatLogsModal();
    });
  }

  if (chatMemoriesBtn) {
    chatMemoriesBtn.classList.toggle('hidden', !isDebugRuntimeMode());
    chatMemoriesBtn.addEventListener('click', openChatMemoriesModal);
  }
  if (chatMemoriesCloseBtn) {
    chatMemoriesCloseBtn.addEventListener('click', closeChatMemoriesModal);
  }
  if (chatMemoriesRefreshBtn) {
    chatMemoriesRefreshBtn.addEventListener('click', loadChatMemories);
  }
  if (chatMemoriesModal) {
    chatMemoriesModal.addEventListener('click', function (e) {
      if (e.target === chatMemoriesModal) closeChatMemoriesModal();
    });
  }

  if (chatClearBtn) {
    chatClearBtn.addEventListener('click', async function () {
      try {
        await appApi().ClearChatHistory();
        if (chatMessagesEl) chatMessagesEl.innerHTML = '';
        showFeedback(translate('chat.cleared'));
      } catch (err) {
        showFeedback(translate('chat.clearError', { error: String(err) }), true);
      }
    });
  }
  if (chatSaveConfigBtn) {
    chatSaveConfigBtn.addEventListener('click', saveChatConfig);
  }
  if (chatTestConfigBtn) {
    chatTestConfigBtn.addEventListener('click', testChatConfig);
  }
}
