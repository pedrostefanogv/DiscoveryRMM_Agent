"use strict";

var currentTicketId = '';
var currentTicket = null;
var workflowStatesCache = null;
var workflowStatesCacheKey = '';
var workflowStatesLoadedAt = 0;
var workflowStatesById = {};
var WORKFLOW_STATES_CACHE_TTL_MS = 10 * 60 * 1000;
// Departamentos/perfis para o picker do formulario de abertura de chamado.
var ticketOptionsCache = { departments: [], workflowProfiles: [] };

// Campos do formulario de abertura fornecidos pelo SERVIDOR para o
// departamento escolhido. Nada e fixo aqui: o servidor define quais campos
// existem (e se estao visiveis) e quais opcoes cada um tem.
var ticketFormSchemaFields = [];
var ticketFormSchemaDepartmentId = '';
// Preenchimento pendente do modelo (categoria/prioridade) que chegou antes do
// schema do departamento: aplicado assim que o servidor responder.
var ticketFormSchemaPrefill = {};
// Sequencia das buscas de schema: resposta de um departamento antigo nao pode
// sobrescrever a selecao atual (usuario pode trocar rapido de departamento).
var ticketFormSchemaRequestSeq = 0;

// Requisicoes do formulario do departamento em transito (campos + schema).
// Enquanto nao terminam, o formulario esta incompleto: enviar nessa janela
// pulava a validacao dos campos obrigatorios (nao havia campo renderizado).
var ticketDepartmentFormLoads = {};
var ticketDepartmentFormLoadSeq = 0;

// Snapshot volatil da sessao: substituido a cada carga e NUNCA persistido.
var supportTicketsAll = [];

// Offline o agente opera em modo somente consulta: controles de escrita ficam
// desabilitados enquanto a listagem vier do cache.
var supportOfflineReadOnly = false;
// Offline de TRANSPORTE (sem comunicação com o servidor), derivado dos eventos
// de conectividade: bloqueia a escrita mesmo antes de a listagem voltar do
// cache (ex.: conexão cai com a página já aberta).
var supportServerDisconnected = false;
// Data do snapshot exibida no aviso (preenchida quando a listagem veio do cache).
var supportStaleCachedAt = '';
var SUPPORT_WRITE_CONTROLS = [
  'newTicketBtn', 'submitTicketBtn', 'submitCommentBtn', 'commentInput',
  'openCloseTicketBtn', 'closeTicketBtn', 'reopenTicketBtn',
  'submitRatingBtn', 'clearRatingPanelBtn', 'clearRatingBtn',
];

function applySupportOfflineMode() {
  var offline = !!supportOfflineReadOnly || !!supportServerDisconnected;
  SUPPORT_WRITE_CONTROLS.forEach(function (id) {
    var el = document.getElementById(id);
    if (el) el.disabled = offline;
  });
  // Aviso explícito no composer, além do botão desabilitado: deixa claro que
  // não é possível enviar comentário enquanto não houver comunicação.
  var hint = document.getElementById('commentOfflineHint');
  if (hint) hint.classList.toggle('hidden', !offline);
  // O botao de abrir chamado tem DUAS condicoes (online E departamento
  // escolhido): o dono do estado final e updateTicketOpenFieldsVisibility().
  updateTicketOpenFieldsVisibility();
}

function formatCacheAge(iso) {
  if (!iso) return '';
  try {
    if (typeof formatDate === 'function') {
      return formatDate(iso, '') || String(iso);
    }
  } catch (_) { /* usa o valor cru */ }
  return String(iso);
}

// Mostra/esconde o aviso e aplica o bloqueio de escrita.
function renderSupportCacheState(result) {
  var stale = !!(result && result.stale);
  supportOfflineReadOnly = stale;
  supportStaleCachedAt = stale ? (result && result.cachedAt) || '' : '';
  applySupportOfflineMode();
  updateSupportOfflineBanner();
}

// updateSupportOfflineBanner centraliza o aviso: aparece quando a listagem veio
// do cache OU quando não há comunicação com o servidor.
function updateSupportOfflineBanner() {
  var banner = document.getElementById('supportStaleBanner');
  if (!banner) return;
  var offline = !!supportOfflineReadOnly || !!supportServerDisconnected;
  if (!offline) {
    banner.classList.add('hidden');
    banner.textContent = '';
    return;
  }
  var msg;
  if (supportOfflineReadOnly) {
    // Listagem veio do snapshot: informa também a data do cache.
    msg = translate('support.staleCache');
    var when = formatCacheAge(supportStaleCachedAt);
    if (when) msg += ' ' + translate('cache.updatedAt', { time: when });
  } else {
    // Apenas transporte offline (lista ainda não recarregada do cache).
    msg = translate('support.serverOffline');
  }
  banner.textContent = msg;
  banner.classList.remove('hidden');
}

// initSupportConnectivity liga o bloqueio/aviso ao estado real de conexão
// (evento do backend, com a mesma histerese anti-flicker do indicador).
var supportConnectivityBound = false;

function initSupportConnectivity() {
  // Idempotente: initSupport pode ser reexecutado sem duplicar listeners.
  if (supportConnectivityBound) return;
  if (!(window.wails && typeof window.wails.on === 'function')) return;
  supportConnectivityBound = true;
  var handler = function (data) {
    // Evento sem "connected" booleano é IGNORADO (payload parcial/versão
    // antiga): assumir offline aqui colocava o suporte em somente-consulta
    // mesmo com o servidor no ar.
    if (!data || typeof data.connected !== 'boolean') return;
    var connected = !!data.connected;
    var shown = typeof window.__statusConnectedHysteresis === 'function'
      ? window.__statusConnectedHysteresis(connected)
      : connected;
    var wasDisconnected = supportServerDisconnected;
    supportServerDisconnected = !shown;
    applySupportOfflineMode();
    updateSupportOfflineBanner();
    // Reconectou: recarrega a lista para sair do modo somente consulta.
    if (!supportServerDisconnected && wasDisconnected && supportOfflineReadOnly) {
      loadSupportTickets({ keepView: true });
    }
  };
  window.wails.on('agent:connectivity', handler);
  window.wails.on('agent:status_snapshot', handler);
}
// Cache local id->chamado usado pelo clique no card. Chamados fechados E ja
// avaliados nao entram aqui: nao ha mais acao pendente neles (requisito de
// produto). Se um filtro explicito exibir um deles, o detalhe e buscado ao vivo.
var supportTicketsById = {};
var ticketFilters = { query: '', status: '__default', priority: 'all' };
// Evita que uma interacao com os filtros sobrescreva a mensagem de erro de
// carregamento por "nenhum chamado".
var supportTicketsLoadFailed = false;
// Filtros vivem apenas na sessão: ao iniciar o agent a lista sempre abre no
// padrão "Abertos e aguardando avaliação" (não há restauração de localStorage).

var closeTicketStarsWidget = null;
var ratingStarsWidget = null;

var priorityLabels = { 1: 'Baixa', 2: 'Media', 3: 'Alta', 4: 'Critica' };
var priorityClasses = { 1: 'p-baixa', 2: 'p-media', 3: 'p-alta', 4: 'p-critica' };

// ── Classificacao do chamado ───────────────────────────────────────────────

function resolveTicketState(ticket) {
  if (!ticket) return null;
  if (ticket.workflowState) return ticket.workflowState;
  var stateId = ticket.workflowStateId || '';
  return stateId ? (workflowStatesById[stateId] || null) : null;
}

function stateNameLooksFinal(name) {
  var normalized = String(name || '').toLowerCase();
  return normalized.indexOf('fechado') >= 0
    || normalized.indexOf('encerrado') >= 0
    || normalized.indexOf('closed') >= 0
    || normalized.indexOf('resolvido') >= 0;
}

function getTicketRating(ticket) {
  if (!ticket) return 0;
  var value = Number(ticket.rating);
  return Number.isFinite(value) && value > 0 ? value : 0;
}

function isTicketRated(ticket) {
  return getTicketRating(ticket) > 0;
}

function ticketHasFinalState(ticket) {
  if (!ticket) return false;
  if (ticket.closedAt) return true;
  var state = resolveTicketState(ticket);
  if (state && state.isFinal) return true;
  if (state && stateNameLooksFinal(state.name)) return true;
  // Ultimo recurso: sem estados carregados, uma avaliacao registrada implica
  // que o chamado passou por fechamento.
  return isTicketRated(ticket);
}

function isTicketClosed(ticket) {
  return ticketHasFinalState(ticket);
}

function isTicketAwaitingRating(ticket) {
  return isTicketClosed(ticket) && !isTicketRated(ticket);
}

function ticketStatusInfo(t) {
  var rootStyles = getComputedStyle(document.documentElement);
  var fallbackColor = rootStyles.getPropertyValue('--success').trim() || '#2d7a44';
  var closedColor = rootStyles.getPropertyValue('--muted').trim() || fallbackColor;
  var state = resolveTicketState(t);
  if (state) {
    return {
      name: (state.name) ? state.name : translate('support.defaultOpenStatus'),
      color: (state.color) ? state.color : fallbackColor,
      isFinal: ticketHasFinalState(t)
    };
  }
  if (ticketHasFinalState(t)) {
    return { name: translate('support.closedStatus'), color: closedColor, isFinal: true };
  }
  return { name: translate('support.defaultOpenStatus'), color: fallbackColor, isFinal: false };
}

function ticketLastActivityText(t) {
  if (t.closedAt) return translate('support.closedAt', { date: formatDate(t.closedAt, '') });
  if (t.updatedAt && t.updatedAt !== t.createdAt) return translate('support.updatedAt', { date: formatDate(t.updatedAt, '') });
  return '';
}

// ── Ordenacao e filtros ────────────────────────────────────────────────────

function ticketClosedTimestamp(t) {
  var raw = t && (t.closedAt || t.updatedAt || t.createdAt);
  var ts = raw ? Date.parse(raw) : NaN;
  return Number.isFinite(ts) ? ts : 0;
}

function ticketCreatedTimestamp(t) {
  var ts = t && t.createdAt ? Date.parse(t.createdAt) : NaN;
  return Number.isFinite(ts) ? ts : 0;
}

// Grupos: 0 = fechado aguardando avaliacao, 1 = aberto, 2 = fechado e avaliado.
function ticketSortGroup(t) {
  if (isTicketAwaitingRating(t)) return 0;
  if (!isTicketClosed(t)) return 1;
  return 2;
}

function compareTickets(a, b) {
  var groupA = ticketSortGroup(a);
  var groupB = ticketSortGroup(b);
  if (groupA !== groupB) return groupA - groupB;
  // Fechados aguardando avaliacao: mais antigos primeiro.
  if (groupA === 0) return ticketClosedTimestamp(a) - ticketClosedTimestamp(b);
  // Abertos: mais recentes primeiro.
  if (groupA === 1) return ticketCreatedTimestamp(b) - ticketCreatedTimestamp(a);
  // Fechados e avaliados: mais recentes primeiro.
  return ticketClosedTimestamp(b) - ticketClosedTimestamp(a);
}

function sortTickets(list) {
  return (list || []).slice().sort(function (a, b) {
    var cmp = compareTickets(a, b);
    if (cmp !== 0) return cmp;
    return String(a.id || '').localeCompare(String(b.id || ''));
  });
}

function normalizeSearchText(value) {
  var text = String(value == null ? '' : value).toLowerCase();
  try {
    if (typeof text.normalize === 'function') {
      text = text.normalize('NFD').replace(/[\u0300-\u036f]/g, '');
    }
  } catch (e) {
    // mantem o texto original (ambiente sem String.normalize)
  }
  return text;
}

// Termos da busca: cada palavra e um termo. Todos precisam estar presentes
// (AND), em QUALQUER ordem/posicao — "erro sistema" e "sistema erro" dao o
// mesmo resultado.
function ticketSearchTerms() {
  return normalizeSearchText(ticketFilters.query)
    .split(/\s+/)
    .filter(function (term) { return term.length > 0; });
}

function ticketMatchesQuery(ticket, terms) {
  if (!terms || !terms.length) return true;
  var state = resolveTicketState(ticket);
  var haystack = normalizeSearchText([
    ticket.title,
    ticket.description,
    ticket.category,
    ticket.id,
    ticket.id ? String(ticket.id).substring(0, 8) : '',
    state ? state.name : ''
  ].join(' '));
  return terms.every(function (term) { return haystack.indexOf(term) >= 0; });
}

// Normaliza preservando o COMPRIMENTO (ponto a ponto), para mapear os indices
// do texto original na hora de destacar os termos.
function foldSearchTextPreserveLength(text) {
  var raw = String(text == null ? '' : text);
  var out = '';
  for (var i = 0; i < raw.length; i++) {
    var ch = raw.charAt(i);
    var base = ch;
    try {
      if (typeof ch.normalize === 'function') {
        var decomposed = ch.normalize('NFD').replace(/[\u0300-\u036f]/g, '');
        if (decomposed.length) base = decomposed.charAt(0);
      }
    } catch (e) {
      // mantem o caractere original
    }
    out += base.toLowerCase();
  }
  return out;
}

// Destaca (com <mark>) os termos encontrados no texto, sem quebrar o escape.
function highlightSearchTerms(text, terms) {
  var raw = text == null ? '' : String(text);
  if (!raw || !terms || !terms.length) return escapeHtml(raw);

  var folded = foldSearchTextPreserveLength(raw);
  var ranges = [];
  terms.forEach(function (term) {
    if (!term) return;
    var from = 0;
    while (from + term.length <= folded.length) {
      var idx = folded.indexOf(term, from);
      if (idx < 0) break;
      ranges.push([idx, idx + term.length]);
      from = idx + Math.max(1, term.length);
    }
  });
  if (!ranges.length) return escapeHtml(raw);

  ranges.sort(function (a, b) { return a[0] - b[0]; });
  var merged = [];
  ranges.forEach(function (range) {
    var last = merged[merged.length - 1];
    if (last && range[0] <= last[1]) {
      if (range[1] > last[1]) last[1] = range[1];
    } else {
      merged.push([range[0], range[1]]);
    }
  });

  var html = '';
  var cursor = 0;
  merged.forEach(function (range) {
    html += escapeHtml(raw.slice(cursor, range[0]));
    html += '<mark class="search-hit">' + escapeHtml(raw.slice(range[0], range[1])) + '</mark>';
    cursor = range[1];
  });
  html += escapeHtml(raw.slice(cursor));
  return html;
}

// Trecho da descricao em volta do primeiro termo encontrado, para o card
// mostrar ONDE a busca casou quando o termo nao aparece no titulo/categoria.
function descriptionSnippet(description, terms) {
  var raw = description == null ? '' : String(description);
  if (!raw || !terms || !terms.length) return '';
  var folded = foldSearchTextPreserveLength(raw);
  var at = -1;
  var matchLength = 0;
  terms.forEach(function (term) {
    if (!term) return;
    var idx = folded.indexOf(term);
    if (idx >= 0 && (at < 0 || idx < at)) {
      at = idx;
      matchLength = term.length;
    }
  });
  if (at < 0) return '';

  var matchEnd = at + matchLength;
  var sliceStart = Math.max(0, at - 60);
  var sliceEnd = Math.min(raw.length, at + 90);
  var snippet = raw.slice(sliceStart, sliceEnd);

  // Corte em limite de palavra, mas nunca removendo o trecho que casou (senao
  // o destaque sumiria do snippet).
  if (sliceStart > 0) {
    var firstSpace = snippet.indexOf(' ');
    if (firstSpace > 0 && sliceStart + firstSpace + 1 <= at) {
      snippet = snippet.slice(firstSpace + 1);
      sliceStart = sliceStart + firstSpace + 1;
    }
  }
  if (sliceEnd < raw.length) {
    var lastSpace = snippet.lastIndexOf(' ');
    if (lastSpace > 0 && sliceStart + lastSpace >= matchEnd) {
      snippet = snippet.slice(0, lastSpace);
      sliceEnd = sliceStart + lastSpace;
    }
  }
  return (sliceStart > 0 ? '…' : '') + snippet.trim() + (sliceEnd < raw.length ? '…' : '');
}

function ticketMatchesStatusFilter(ticket, status) {
  if (!status || status === '__default') return !(isTicketClosed(ticket) && isTicketRated(ticket));
  if (status === '__awaiting') return isTicketAwaitingRating(ticket);
  if (status === '__open') return !isTicketClosed(ticket);
  if (status === '__closed') return isTicketClosed(ticket);
  if (status === '__all') return true;
  var stateId = ticket.workflowStateId || (ticket.workflowState ? ticket.workflowState.id : '');
  return stateId === status;
}

function filterTickets(list) {
  var terms = ticketSearchTerms();
  return (list || []).filter(function (t) {
    if (!ticketMatchesStatusFilter(t, ticketFilters.status)) return false;
    if (ticketFilters.priority !== 'all' && String(t.priority) !== String(ticketFilters.priority)) return false;
    return ticketMatchesQuery(t, terms);
  });
}

function buildStatusFilterOptions(states) {
  var options = [
    { value: '__default', label: translate('support.filterDefault') },
    { value: '__awaiting', label: translate('support.filterAwaiting') },
    { value: '__open', label: translate('support.filterOpenOnly') },
    { value: '__closed', label: translate('support.filterClosedOnly') },
    { value: '__all', label: translate('support.filterAll') }
  ];
  (states || []).forEach(function (s) {
    if (!s || !s.id) return;
    options.push({ value: s.id, label: s.name || s.id.substring(0, 8) });
  });
  return options;
}

function renderTicketFilterControls(states) {
  if (ticketStatusFilterEl) {
    var options = buildStatusFilterOptions(states);
    ticketStatusFilterEl.innerHTML = options.map(function (o) {
      return '<option value="' + escapeHtmlAttr(o.value) + '">' + escapeHtml(o.label) + '</option>';
    }).join('');
    var previousStatus = ticketFilters.status || '__default';
    var current = previousStatus;
    if (!options.some(function (o) { return o.value === current; })) current = '__default';
    ticketFilters.status = current;
    ticketStatusFilterEl.value = current;
    if (previousStatus !== current) updateTicketFiltersToggle();
  }
  if (ticketPriorityFilterEl) {
    if (!ticketPriorityFilterEl.options.length) {
      var priorityOptions = [{ value: 'all', label: translate('support.filterAllPriorities') }];
      [1, 2, 3, 4].forEach(function (p) {
        priorityOptions.push({ value: String(p), label: ticketPriorityLabel(p) });
      });
      ticketPriorityFilterEl.innerHTML = priorityOptions.map(function (o) {
        return '<option value="' + escapeHtmlAttr(o.value) + '">' + escapeHtml(o.label) + '</option>';
      }).join('');
    }
    if (['all', '1', '2', '3', '4'].indexOf(String(ticketFilters.priority)) < 0) {
      ticketFilters.priority = 'all';
    }
    ticketPriorityFilterEl.value = ticketFilters.priority || 'all';
  }
}

function clearTicketFilters() {
  ticketFilters.query = '';
  ticketFilters.status = '__default';
  ticketFilters.priority = 'all';
  if (ticketSearchInputEl) ticketSearchInputEl.value = '';
  if (ticketStatusFilterEl) ticketStatusFilterEl.value = '__default';
  if (ticketPriorityFilterEl) ticketPriorityFilterEl.value = 'all';
  renderSupportTicketList(supportTicketsAll);
  updateTicketFiltersToggle();
}

// Algum filtro fora do padrao? (mantem o icone destacado mesmo recolhido)
function hasActiveTicketFilters() {
  return !!ticketFilters.query
    || ticketFilters.priority !== 'all'
    || (!!ticketFilters.status && ticketFilters.status !== '__default');
}

function updateTicketFiltersToggle() {
  if (toggleTicketFiltersBtnEl) {
    toggleTicketFiltersBtnEl.classList.toggle('active', hasActiveTicketFilters());
  }
}

function setTicketFiltersPanelVisible(visible) {
  if (!ticketFiltersPanelEl) return;
  ticketFiltersPanelEl.classList.toggle('hidden', !visible);
  if (toggleTicketFiltersBtnEl) {
    toggleTicketFiltersBtnEl.setAttribute('aria-expanded', String(visible));
    toggleTicketFiltersBtnEl.classList.toggle('open', visible);
  }
}

function toggleTicketFiltersPanel() {
  if (!ticketFiltersPanelEl) return;
  setTicketFiltersPanelVisible(ticketFiltersPanelEl.classList.contains('hidden'));
}

// ── Modal de fechamento ────────────────────────────────────────────────────

function showCloseTicketModal() {
  if (closeTicketModalEl) closeTicketModalEl.classList.remove('hidden');
  resetRating();
}

function hideCloseTicketModal() {
  if (closeTicketModalEl) closeTicketModalEl.classList.add('hidden');
}

function showSupportList() {
  if (supportListViewEl) supportListViewEl.classList.remove("hidden");
  if (supportDetailViewEl) supportDetailViewEl.classList.add("hidden");
  if (supportNewTicketViewEl) supportNewTicketViewEl.classList.add("hidden");
  if (supportStatusBarEl) supportStatusBarEl.classList.remove("hidden");
}

function showSupportDetail() {
  if (supportListViewEl) supportListViewEl.classList.add("hidden");
  if (supportDetailViewEl) supportDetailViewEl.classList.remove("hidden");
  if (supportNewTicketViewEl) supportNewTicketViewEl.classList.add("hidden");
  if (supportStatusBarEl) supportStatusBarEl.classList.add("hidden");
}

function showNewTicketForm() {
  if (supportListViewEl) supportListViewEl.classList.add("hidden");
  if (supportDetailViewEl) supportDetailViewEl.classList.add("hidden");
  if (supportNewTicketViewEl) supportNewTicketViewEl.classList.remove("hidden");
  // O cabecalho unificado (busca/filtros/acoes) nao faz sentido no formulario.
  if (supportStatusBarEl) supportStatusBarEl.classList.add("hidden");
  hideTicketFormStatus();
  // Reabrir o formulario comeca limpo: o departamento e escolhido de novo e e
  // ele que libera os demais campos (modelo/campos do servidor/descricao).
  var departmentSelect = document.getElementById('ticketDepartment');
  if (departmentSelect) departmentSelect.value = '';
  clearTicketOpenFields();
  updateTicketOpenFieldsVisibility();
  // Campos do departamento sao recarregados a cada abertura do formulario
  // (evita cache velho quando um campo e adicionado no servidor).
  departmentFieldsCache = {};
  // Sem departamento selecionado o modelo fica desabilitado.
  renderTicketTemplateOptions();
  loadTicketOptions();
  loadTicketTemplates();
}

function escapeOptionLabel(value) {
  var el = document.createElement('div');
  el.textContent = value == null ? '' : String(value);
  return el.innerHTML;
}

// Carrega os departamentos do cliente do agente para o formulario. Sem
// departamento o servidor nao calcula SLA. O perfil de workflow deixou de ser
// informado na abertura (o servidor aplica o padrao do departamento).
function loadTicketOptions() {
  var api = appApi();
  if (!api || typeof api.GetTicketOptions !== 'function') return;
  api.GetTicketOptions().then(function (options) {
    ticketOptionsCache = options || { departments: [], workflowProfiles: [] };
    var deptSelect = document.getElementById('ticketDepartment');
    if (deptSelect) {
      var html = '<option value="">' + escapeHtml(translate('support.select')) + '</option>';
      (ticketOptionsCache.departments || []).forEach(function (d) {
        html += '<option value="' + escapeHtmlAttr(d.id) + '">' + escapeOptionLabel(d.name) + '</option>';
      });
      deptSelect.innerHTML = html;
    }
    // Reconstruir a lista zera a selecao: reaplica o gating e descarta um
    // schema que tenha ficado de um departamento anterior.
    updateTicketOpenFieldsVisibility();
    if (!selectedDepartmentId()) clearTicketFormSchema();
  }).catch(function (err) {
    console.warn('[support] falha ao carregar departamentos:', err);
  });
}

// ── Schema do formulario de abertura (fornecido pelo servidor) ─────────────
//
// O agent NAO tem lista fixa de campos/opcoes. Depois que o usuario escolhe o
// departamento, buscamos o schema do departamento no servidor e renderizamos
// exatamente o que vier: campos invisiveis (ou sem opcoes) nao aparecem. Assim,
// cada departamento pode ter campos e opcoes diferentes (modelos ativos e
// historico do proprio departamento).

function ticketSchemaFieldInputId(key) {
  return 'ticketSchemaField_' + String(key || '');
}

function ticketSchemaFieldIsVisible(field) {
  if (!field || !field.key) return false;
  if (field.visible === false) return false;
  var type = String(field.type || 'select').toLowerCase();
  if (type === 'select') {
    return Array.isArray(field.options) && field.options.length > 0;
  }
  return true;
}

function renderTicketFormSchema(schema) {
  var container = document.getElementById('ticketFormSchemaFields');
  var received = schema && Array.isArray(schema.fields) ? schema.fields : [];
  ticketFormSchemaFields = received.filter(ticketSchemaFieldIsVisible);
  if (!container) return;

  var html = '';
  ticketFormSchemaFields.forEach(function (field) {
    var id = ticketSchemaFieldInputId(field.key);
    var type = String(field.type || 'select').toLowerCase();
    var options = Array.isArray(field.options) ? field.options : [];
    var defaultValue = field.defaultValue == null ? '' : String(field.defaultValue);

    html += '<div class="form-field">';
    html += '<label for="' + escapeHtmlAttr(id) + '">' + escapeOptionLabel(field.label || field.key);
    if (field.required) html += ' *';
    html += '</label>';

    if (type === 'select') {
      html += '<select id="' + escapeHtmlAttr(id) + '">';
      html += '<option value="">' + escapeHtml(translate('support.select')) + '</option>';
      options.forEach(function (option) {
        var value = option && option.value != null ? String(option.value) : '';
        var label = option && option.label ? String(option.label) : value;
        html += '<option value="' + escapeHtmlAttr(value) + '"' +
          (value === defaultValue ? ' selected' : '') + '>' + escapeOptionLabel(label) + '</option>';
      });
      html += '</select>';
    } else if (type === 'textarea') {
      html += '<textarea id="' + escapeHtmlAttr(id) + '" rows="3"></textarea>';
    } else {
      html += '<input id="' + escapeHtmlAttr(id) + '" type="text"';
      if (defaultValue) html += ' value="' + escapeHtmlAttr(defaultValue) + '"';
      html += '/>';
    }
    html += '</div>';
  });

  container.innerHTML = html;
  container.classList.toggle('hidden', ticketFormSchemaFields.length === 0);
  container.classList.toggle('form-row-single', ticketFormSchemaFields.length === 1);

  // Modelo escolhido antes da resposta da API: aplica o preenchimento que
  // ficou na fila agora que os campos existem.
  Object.keys(ticketFormSchemaPrefill).forEach(function (key) {
    applyTicketFormSchemaValue(key, ticketFormSchemaPrefill[key]);
  });
}

function readTicketFormSchemaValue(key) {
  var el = document.getElementById(ticketSchemaFieldInputId(key));
  if (!el) return '';
  return String(el.value == null ? '' : el.value).trim();
}

// Aplica um valor no campo do schema, se ele ja estiver renderizado. Valor
// fora da lista do servidor (modelo com categoria propria) entra como opcao
// extra para nao ser perdido.
function applyTicketFormSchemaValue(key, value) {
  var el = document.getElementById(ticketSchemaFieldInputId(key));
  if (!el) return false;
  var next = String(value);
  if (el.tagName === 'SELECT') {
    var found = false;
    for (var i = 0; i < el.options.length; i++) {
      if (el.options[i].value === next) { found = true; break; }
    }
    if (!found) {
      var option = document.createElement('option');
      option.value = next;
      option.textContent = next;
      el.appendChild(option);
    }
  }
  el.value = next;
  return true;
}

// Preenchimento vindo do modelo (categoria/prioridade). Fica na fila mesmo que
// os campos ainda nao existam (schema do departamento em transito).
function setTicketFormSchemaValue(key, value) {
  if (!key || value == null || value === '') return;
  ticketFormSchemaPrefill[key] = String(value);
  applyTicketFormSchemaValue(key, value);
}

function collectTicketFormSchemaMissing() {
  var missing = [];
  ticketFormSchemaFields.forEach(function (field) {
    if (!field.required) return;
    if (readTicketFormSchemaValue(field.key) === '') missing.push(field.label || field.key);
  });
  return missing;
}

function clearTicketFormSchema() {
  ticketFormSchemaRequestSeq += 1;
  ticketFormSchemaDepartmentId = '';
  ticketFormSchemaPrefill = {};
  renderTicketFormSchema(null);
}

function clearTicketOpenFields() {
  if (ticketTemplateSelectEl) ticketTemplateSelectEl.value = '';
  lastAppliedTemplateId = '';
  clearTicketTemplateExtra();
  clearDepartmentFields();
  clearTicketFormSchema();
}

// Mostra o restante do formulario (modelo, titulo, campos do servidor,
// descricao e envio) somente com departamento escolhido. Sem departamento o
// servidor nao calcula SLA e nao ha schema para renderizar.
// Controle das requisicoes do formulario do departamento (campos + schema).
function beginTicketDepartmentFormLoad(departmentId) {
  var id = ++ticketDepartmentFormLoadSeq;
  ticketDepartmentFormLoads[id] = String(departmentId || '');
  updateTicketOpenFieldsVisibility();
  return id;
}

function endTicketDepartmentFormLoad(id) {
  delete ticketDepartmentFormLoads[id];
  updateTicketOpenFieldsVisibility();
}

// Um departamento com carga em transito nao pode liberar o envio: sem os
// campos renderizados a validacao de obrigatorios nao tem o que validar.
function ticketDepartmentFormIsLoading(departmentId) {
  var current = String(departmentId || '');
  for (var id in ticketDepartmentFormLoads) {
    if (ticketDepartmentFormLoads[id] === current) return true;
  }
  return false;
}

function updateTicketOpenFieldsVisibility() {
  var departmentId = selectedDepartmentId();
  var hasDepartment = !!departmentId;
  var section = document.getElementById('ticketOpenFields');
  if (section) section.classList.toggle('hidden', !hasDepartment);
  var btn = document.getElementById('submitTicketBtn');
  if (btn) {
    var offline = !!supportOfflineReadOnly || !!supportServerDisconnected;
    var loading = hasDepartment && ticketDepartmentFormIsLoading(departmentId);
    btn.disabled = !hasDepartment || offline || loading;
  }
}

function loadTicketDepartmentFormSchema(departmentId) {
  var api = appApi();
  if (!departmentId || !api || typeof api.GetTicketDepartmentFormSchema !== 'function') {
    clearTicketFormSchema();
    return;
  }
  // Mesmo departamento ja renderizado: nao repete o round-trip (reabrir o
  // formulario com o departamento ja escolhido).
  if (ticketFormSchemaDepartmentId === departmentId && ticketFormSchemaFields.length) return;
  ticketFormSchemaRequestSeq += 1;
  var seq = ticketFormSchemaRequestSeq;
  var loadId = beginTicketDepartmentFormLoad(departmentId);
  api.GetTicketDepartmentFormSchema(departmentId).then(function (schema) {
    // Resposta de um departamento ja trocado nao pode sobrescrever a tela.
    if (seq !== ticketFormSchemaRequestSeq || selectedDepartmentId() !== departmentId) return;
    ticketFormSchemaDepartmentId = departmentId;
    renderTicketFormSchema(schema);
  }).catch(function (err) {
    if (seq !== ticketFormSchemaRequestSeq) return;
    console.warn('[support] falha ao carregar o formulario do departamento:', err);
    clearTicketFormSchema();
  }).finally(function () {
    endTicketDepartmentFormLoad(loadId);
  });
}

// ── Modelos (templates) de abertura de chamado ─────────────────────────────

var ticketTemplatesCache = [];

function templatePriorityToInt(priority) {
  var map = {
    '1': 1, 'low': 1, 'baixa': 1,
    '2': 2, 'medium': 2, 'media': 2, 'média': 2,
    '3': 3, 'high': 3, 'alta': 3,
    '4': 4, 'critical': 4, 'critica': 4, 'crítica': 4
  };
  return map[String(priority || '').trim().toLowerCase()] || 0;
}

function loadTicketTemplates() {
  var api = appApi();
  if (!api || typeof api.GetTicketTemplates !== 'function' || !ticketTemplateSelectEl) return;
  api.GetTicketTemplates().then(function (templates) {
    ticketTemplatesCache = Array.isArray(templates) ? templates : [];
    renderTicketTemplateOptions();
  }).catch(function (err) {
    console.warn('[support] falha ao carregar modelos de chamado:', err);
    renderTicketTemplateOptions();
  });
}

// O departamento vem primeiro: o modelo so fica disponivel depois dele e a
// lista e filtrada pelo departamento (modelos sem departamento valem para
// qualquer um). Assim nao ha como escolher um modelo de outro departamento.
function renderTicketTemplateOptions() {
  if (!ticketTemplateSelectEl) return;
  var departmentId = selectedDepartmentId();
  if (!departmentId) {
    ticketTemplateSelectEl.innerHTML =
      '<option value="">' + escapeHtml(translate('support.templateSelectDepartment')) + '</option>';
    ticketTemplateSelectEl.value = '';
    ticketTemplateSelectEl.disabled = true;
    return;
  }
  var available = ticketTemplatesCache.filter(function (t) {
    return t && t.id && (!t.departmentId || t.departmentId === departmentId);
  });
  var previous = ticketTemplateSelectEl.value;
  var html = '<option value="">' + escapeHtml(translate('support.templateNone')) + '</option>';
  available.forEach(function (t) {
    // O texto humano e o Title; Name e apenas a chave identificadora.
    html += '<option value="' + escapeHtmlAttr(t.id) + '">' + escapeHtml(t.title || t.name || t.id) + '</option>';
  });
  ticketTemplateSelectEl.innerHTML = html;
  // Preserva a escolha do usuario: a lista pode ser reconstruida depois de uma
  // resposta assincrona (loadTicketTemplates) e nao pode descartar a selecao.
  var keepPrevious = available.some(function (t) { return t.id === previous; });
  ticketTemplateSelectEl.value = keepPrevious ? previous : '';
  ticketTemplateSelectEl.disabled = false;
}

function findTicketTemplate(id) {
  for (var i = 0; i < ticketTemplatesCache.length; i++) {
    if (ticketTemplatesCache[i] && ticketTemplatesCache[i].id === id) return ticketTemplatesCache[i];
  }
  return null;
}

function clearTicketTemplateExtra() {
  if (!ticketTemplateExtraEl) return;
  ticketTemplateExtraEl.innerHTML = '';
  ticketTemplateExtraEl.classList.add('hidden');
}

function templateInputId(prefix, key) {
  return 'tpl-' + prefix + '-' + String(key || '').replace(/[^a-zA-Z0-9_-]/g, '');
}

// ── Mascara leve (mesmos tokens do console: 9=digito, A=letra, *=alfanumerico)

var MASK_TOKEN_PATTERNS = { '9': /[0-9]/, 'A': /[A-Za-z]/, '*': /[A-Za-z0-9]/ };

function isMaskToken(ch) { return ch === '9' || ch === 'A' || ch === '*'; }

function matchesMaskToken(token, ch) {
  var pattern = MASK_TOKEN_PATTERNS[token];
  return pattern ? pattern.test(ch) : false;
}

// Formata o valor digitado segundo a mascara (largura fixa, ignorando excessos
// invalidos). A autoridade de validacao continua no servidor.
function applyFieldMask(mask, raw) {
  var trimmed = String(mask || '').trim();
  if (!trimmed) return raw == null ? '' : String(raw);
  var chars = String(raw == null ? '' : raw).split('');
  var out = '';
  var cursor = 0;
  for (var i = 0; i < trimmed.length; i++) {
    var token = trimmed.charAt(i);
    if (isMaskToken(token)) {
      while (cursor < chars.length && !matchesMaskToken(token, chars[cursor])) cursor++;
      if (cursor >= chars.length) break;
      out += chars[cursor];
      cursor++;
    } else {
      if (cursor >= chars.length) break;
      out += token;
    }
  }
  return out;
}

// Numeros mascarados (ex.: "R$ 9.999.999,99"): normaliza pt-BR/en-US para o
// valor que o Number() entende e formata o rascunho para exibicao.
function normalizeNumericDraft(draft) {
  var raw = String(draft == null ? '' : draft).trim();
  if (!raw) return '';
  var cleaned = raw.replace(/[^\d,.-]/g, '');
  if (!cleaned) return '';
  var lastComma = cleaned.lastIndexOf(',');
  var lastDot = cleaned.lastIndexOf('.');
  var negative = cleaned.charAt(0) === '-';
  var body = cleaned.replace(/-/g, '');
  var normalized;
  if (lastComma >= 0 && lastComma > lastDot) {
    normalized = body.replace(/\./g, '').replace(',', '.');
  } else {
    normalized = body.replace(/,/g, '');
  }
  return (negative ? '-' : '') + normalized;
}

function maskUsesCommaDecimals(mask) {
  return /,\s*9+\s*$/.test(String(mask || ''));
}

// Normaliza considerando tipo e locale da mascara: em Integer, ponto/virgula
// sao separadores de milhar; em Decimal com mascara pt-BR (sufixo ",99") o
// ponto e milhar e a virgula e decimal. Evita "1.234" virar 1,23.
function normalizeNumericDraftForMask(mask, draft, dataType) {
  var raw = String(draft == null ? '' : draft).trim();
  if (!raw) return '';
  if (dataType === 'Integer') {
    // Usa so a parte inteira (antes do separador decimal da mascara, se houver).
    var integerRaw = maskUsesCommaDecimals(mask) ? raw.split(',')[0] : raw;
    var digits = integerRaw.replace(/[^\d-]/g, '');
    return (digits === '' || digits === '-') ? '' : digits;
  }
  if (!maskUsesCommaDecimals(mask)) return normalizeNumericDraft(raw);

  var cleaned = raw.replace(/[^\d,.-]/g, '');
  if (!cleaned) return '';
  var negative = cleaned.charAt(0) === '-';
  var body = cleaned.replace(/-/g, '');
  var commaAt = body.lastIndexOf(',');
  var integerText;
  var decimalText = '';
  if (commaAt >= 0) {
    integerText = body.slice(0, commaAt).replace(/\./g, '');
    decimalText = body.slice(commaAt + 1).replace(/\D/g, '');
  } else {
    integerText = body.replace(/\./g, '');
  }
  var normalized = integerText + (decimalText ? '.' + decimalText : '');
  if (!normalized) return '';
  return (negative ? '-' : '') + normalized;
}

function maskNumericDraft(mask, draft, dataType) {
  var trimmedMask = String(mask || '').trim();
  var normalized = normalizeNumericDraftForMask(trimmedMask, draft, dataType);
  if (!normalized) return '';
  if (!trimmedMask) return normalized;

  var prefixMatch = /^[^9A*]*/.exec(trimmedMask);
  var prefix = prefixMatch ? prefixMatch[0] : '';
  var decimalSuffix = /,9+\s*$/.exec(trimmedMask);
  var decimals = decimalSuffix ? decimalSuffix[0].replace(/[^9]/g, '').length : 0;
  var negative = normalized.charAt(0) === '-';
  var parts = normalized.replace('-', '').split('.');
  var integerPart = parts[0] || '';
  var decimalPart = parts[1] || '';
  var grouped = integerPart.replace(/\B(?=(\d{3})+(?!\d))/g, '.');
  var decimalText = decimals > 0 ? ',' + decimalPart.padEnd(decimals, '0').slice(0, decimals) : '';
  return (negative ? '-' : '') + prefix + grouped + decimalText;
}

// Orientação humana para regex/máscara (port do fieldFormatHints do console):
// o usuário final nunca deve ver a regex crua.

var KNOWN_FORMAT_EXAMPLES = [
  { test: /@/, example: 'nome@empresa.com' },
  { test: /\[A-Za-z0-9\]\{12\}|\\d\{14\}/, example: 'XX.XXX.XXX/XXXX-00' },
  { test: /\\d\{11\}/, example: '000.000.000-00' },
  { test: /\\d\{8\}|\\d\{5\}-\?\\d\{3\}/, example: '00000-000' },
  { test: /\(\?\\d\{2\}|\\d\{4,5\}/, example: '(00) 00000-0000' },
  { test: /https\?:/, example: 'https://exemplo.com' },
  { test: /\\d\{4\}.*\\d\{2\}|\\d{1,2}\/\\d{1,2}/, example: 'dd/mm/aaaa' }
];

function fieldMaskPlaceholder(mask) {
  var trimmed = String(mask || '').trim();
  if (!trimmed) return '';
  return trimmed.replace(/9/g, '0').replace(/A/g, 'A').replace(/\*/g, 'X');
}

function exampleFromRegex(regex) {
  if (!regex) return null;
  for (var i = 0; i < KNOWN_FORMAT_EXAMPLES.length; i++) {
    if (KNOWN_FORMAT_EXAMPLES[i].test.test(regex)) return KNOWN_FORMAT_EXAMPLES[i].example;
  }
  return null;
}

// Linha de orientação do campo: helpText/description + exemplo (máscara ou
// regex conhecida). Sem regex crua.
function describeFieldFormat(source) {
  var help = ((source && (source.helpText || source.description)) || '').trim();
  var maskExample = fieldMaskPlaceholder(source && source.inputMask);
  var regexExample = maskExample ? null : exampleFromRegex(source && source.validationRegex);
  var parts = [];
  if (help) parts.push(help);
  if (maskExample) parts.push('ex.: ' + maskExample);
  else if (regexExample) parts.push('ex.: ' + regexExample);
  return parts.length > 0 ? parts.join(' · ') : '';
}

// Mensagem de erro com orientação: "Label: informe um valor como X."
function describeFieldFormatError(label, source) {
  var maskExample = fieldMaskPlaceholder(source && source.inputMask);
  var example = maskExample || exampleFromRegex(source && source.validationRegex);
  if (example) return translate('support.formatLike', { label: label || '', example: example });
  return translate('support.formatMismatch', { label: label || '' });
}

function attachInputMasks(containerEl) {
  if (!containerEl) return;
  containerEl.querySelectorAll('input[data-input-mask]').forEach(function (el) {
    var mask = el.getAttribute('data-input-mask');
    if (!mask) return;
    el.addEventListener('input', function () {
      var next = applyFieldMask(mask, el.value);
      if (el.value !== next) el.value = next;
    });
  });

  // Numericos: formata ao sair do campo (digitar continua livre) e o valor e
  // normalizado no envio.
  containerEl.querySelectorAll('input[data-number-mask]').forEach(function (el) {
    var mask = el.getAttribute('data-number-mask');
    if (!mask) return;
    var type = el.getAttribute('data-number-type') || 'Decimal';
    var format = function () {
      var next = maskNumericDraft(mask, el.value, type);
      if (next && el.value !== next) el.value = next;
    };
    el.addEventListener('blur', format);
    el.addEventListener('change', format);
  });
}

// Min/max dos campos numéricos no cliente: o type="text" mascarado perde os
// limites nativos, então validamos aqui (o servidor continua validando).
function numericBoundsFailures(items, prefix) {
  var failures = [];
  (items || []).forEach(function (item) {
    if (!item || (item.minValue == null && item.maxValue == null)) return;
    var dataType = String(item.dataType || '');
    if (dataType !== 'Integer' && dataType !== 'Decimal') return;
    var key = prefix === 'field' ? item.definitionId : item.key;
    if (!key) return;
    var value = readTemplateControlValue(templateInputId(prefix, key), dataType);
    if (value === undefined) return;
    var num = Number(value);
    if (!Number.isFinite(num)) return;
    if (item.minValue != null && num < Number(item.minValue)) {
      failures.push(translate('support.minValueHint', { label: item.label || key, min: Number(item.minValue) }));
      return;
    }
    if (item.maxValue != null && num > Number(item.maxValue)) {
      failures.push(translate('support.maxValueHint', { label: item.label || key, max: Number(item.maxValue) }));
    }
  });
  return failures;
}

// validationRegex do servidor aplicada no cliente (semântica de substring,
// igual ao Regex.IsMatch). Regex incompatível com JS é ignorada com aviso e a
// mensagem de erro mostra o formato esperado.
function regexValidationFailures(items, prefix) {
  var failures = [];
  (items || []).forEach(function (item) {
    if (!item || !item.validationRegex) return;
    // O servidor aplica validationRegex apenas em Text (ValidateTextInternal).
    if (String(item.dataType || 'Text') !== 'Text') return;
    var key = prefix === 'field' ? item.definitionId : item.key;
    if (!key) return;
    var value = readTemplateControlValue(templateInputId(prefix, key), item.dataType);
    if (value === undefined) return;
    var text = Array.isArray(value) ? value.join(', ') : String(value);
    var regex = null;
    try {
      regex = new RegExp(item.validationRegex);
    } catch (e) {
      console.warn('[support] validationRegex incompativel com JS (ignorada):', item.validationRegex);
      return;
    }
    if (!regex.test(text)) {
      failures.push(describeFieldFormatError(item.label || item.name || key, item));
    }
  });
  return failures;
}

// Controles por CustomFieldDataType da API (Text, Integer, Decimal, Boolean,
// Date, DateTime, Dropdown, ListBox).
function buildTemplateFieldControl(item, idAttr) {
  var dataType = String((item && item.dataType) || 'Text');
  var required = !!(item && item.isRequired);
  var options = (item && Array.isArray(item.options)) ? item.options : [];

  // Limites do campo viram atributos HTML (o servidor continua validando).
  var bounds = '';
  if (item && item.minValue !== null && item.minValue !== undefined) bounds += ' min="' + Number(item.minValue) + '"';
  if (item && item.maxValue !== null && item.maxValue !== undefined) bounds += ' max="' + Number(item.maxValue) + '"';
  var lengths = '';
  if (item && item.minLength !== null && item.minLength !== undefined) lengths += ' minlength="' + Number(item.minLength) + '"';
  if (item && item.maxLength !== null && item.maxLength !== undefined) lengths += ' maxlength="' + Number(item.maxLength) + '"';
  var maskAttr = (item && item.inputMask)
    ? ' data-input-mask="' + escapeHtmlAttr(item.inputMask) + '"'
    : '';

  if (dataType === 'Dropdown' && options.length) {
    var selectHtml = '<select id="' + idAttr + '"' + (required ? ' required' : '') + '><option value="">' + escapeHtml(translate('support.select')) + '</option>';
    options.forEach(function (o) {
      selectHtml += '<option value="' + escapeHtmlAttr(o) + '">' + escapeHtml(o) + '</option>';
    });
    return selectHtml + '</select>';
  }
  if (dataType === 'ListBox') {
    var listHtml = '<select id="' + idAttr + '" multiple size="' + Math.min(5, Math.max(2, options.length || 2)) + '"' + (required ? ' required' : '') + '>';
    options.forEach(function (o) {
      listHtml += '<option value="' + escapeHtmlAttr(o) + '">' + escapeHtml(o) + '</option>';
    });
    return listHtml + '</select>';
  }
  if (dataType === 'Boolean') {
    return '<select id="' + idAttr + '"' + (required ? ' required' : '') + '>' +
      '<option value="">' + escapeHtml(translate('support.select')) + '</option>' +
      '<option value="true">' + escapeHtml(translate('support.yes')) + '</option>' +
      '<option value="false">' + escapeHtml(translate('support.no')) + '</option>' +
      '</select>';
  }
  var numberMask = (item && item.inputMask)
    ? ' data-number-mask="' + escapeHtmlAttr(item.inputMask) + '"'
    : '';
  if (dataType === 'Integer') {
    return numberMask
      ? '<input id="' + idAttr + '" type="text" inputmode="numeric"' + numberMask + ' data-number-type="Integer"' + (required ? ' required' : '') + ' />'
      : '<input id="' + idAttr + '" type="number" step="1"' + bounds + (required ? ' required' : '') + ' />';
  }
  if (dataType === 'Decimal') {
    return numberMask
      ? '<input id="' + idAttr + '" type="text" inputmode="decimal"' + numberMask + ' data-number-type="Decimal"' + (required ? ' required' : '') + ' />'
      : '<input id="' + idAttr + '" type="number" step="0.01"' + bounds + (required ? ' required' : '') + ' />';
  }
  if (dataType === 'Date') return '<input id="' + idAttr + '" type="date"' + (required ? ' required' : '') + ' />';
  if (dataType === 'DateTime') return '<input id="' + idAttr + '" type="datetime-local"' + (required ? ' required' : '') + ' />';
  return '<input id="' + idAttr + '" type="text"' + lengths + maskAttr + (required ? ' required' : '') + ' />';
}

// Campos em si: cada item sai como .form-field puro — sem caixa, sem recuo e
// sem título — para ficar alinhado aos demais campos do formulário. O modelo em
// uso (quando houver) já aparece no select "Modelo de chamado" acima, então
// repetir "Modelo: X" aqui só duplicava a informação.
function renderTemplateFields(prefix, items) {
  var html = '';
  (Array.isArray(items) ? items : []).forEach(function (item) {
    if (!item) return;
    var key = prefix === 'field' ? item.definitionId : item.key;
    if (!key) return;
    var idAttr = templateInputId(prefix, key);
    var label = item.label || item.name || key;
    var formatHint = describeFieldFormat({
      inputMask: item.inputMask,
      validationRegex: item.validationRegex,
      helpText: item.helpText,
      description: item.description
    });
    html += '<div class="form-field">' +
      '<label for="' + idAttr + '">' + escapeHtml(label) + (item.isRequired ? ' *' : '') + '</label>' +
      buildTemplateFieldControl(item, idAttr) +
      (formatHint ? '<div class="meta">' + escapeHtml(formatHint) + '</div>' : '') +
      '</div>';
  });
  return html;
}

function renderTicketTemplateExtra(template) {
  if (!ticketTemplateExtraEl) return;
  var html = template ? renderTemplateFields('q', template.questions) : '';
  if (!html) {
    clearTicketTemplateExtra();
    return;
  }
  ticketTemplateExtraEl.innerHTML = html;
  ticketTemplateExtraEl.classList.remove('hidden');
  attachInputMasks(ticketTemplateExtraEl);
}

function setSelectValueIfPresent(select, value) {
  if (!select || !value) return;
  for (var i = 0; i < select.options.length; i++) {
    if (select.options[i].value === value) {
      select.value = value;
      return;
    }
  }
}

// ── Campos personalizados do departamento ──────────────────────────────────
// Valem para TODO chamado do departamento (com ou sem template), por isso sao
// carregados do endpoint do departamento — nao apenas dos campos do template.

var currentDepartmentFields = [];

function clearDepartmentFields() {
  currentDepartmentFields = [];
  if (!ticketDepartmentFieldsEl) return;
  ticketDepartmentFieldsEl.innerHTML = '';
  ticketDepartmentFieldsEl.classList.add('hidden');
}

function parseTemplateDefaults(defaultsJson) {
  if (!defaultsJson) return {};
  try {
    var parsed = typeof defaultsJson === 'string' ? JSON.parse(defaultsJson) : defaultsJson;
    return (parsed && typeof parsed === 'object') ? parsed : {};
  } catch (e) {
    return {};
  }
}

function setTemplateControlValue(idAttr, dataType, value) {
  var el = document.getElementById(idAttr);
  if (!el || value === null || value === undefined) return;
  if (dataType === 'ListBox') {
    var wanted = Array.isArray(value) ? value.map(String) : [String(value)];
    for (var i = 0; i < el.options.length; i++) {
      el.options[i].selected = wanted.indexOf(el.options[i].value) >= 0;
    }
    return;
  }
  if (dataType === 'Boolean') {
    el.value = (value === true || value === 'true') ? 'true' : 'false';
    return;
  }
  var numberMask = el.getAttribute ? el.getAttribute('data-number-mask') : '';
  if (numberMask) {
    el.value = maskNumericDraft(numberMask, String(value), dataType);
    return;
  }
  el.value = String(value);
  // Defaults/valores restaurados tambem precisam respeitar a mascara.
  var mask = el.getAttribute ? el.getAttribute('data-input-mask') : '';
  if (mask) el.value = applyFieldMask(mask, el.value);
}

// Le os valores atuais dos campos antes de um re-render (para nao apagar o que
// o usuario digitou quando ele troca o modelo).
function readDepartmentFieldValuesSnapshot() {
  var snapshot = {};
  currentDepartmentFields.forEach(function (field) {
    if (!field || !field.definitionId) return;
    var value = readTemplateControlValue(templateInputId('field', field.definitionId), field.dataType);
    if (value !== undefined) snapshot[field.definitionId] = value;
  });
  return snapshot;
}

function renderDepartmentFields(fields, defaultsJson) {
  var previousValues = readDepartmentFieldValuesSnapshot();
  currentDepartmentFields = Array.isArray(fields) ? fields : [];
  if (!ticketDepartmentFieldsEl || !currentDepartmentFields.length) {
    clearDepartmentFields();
    return;
  }
  // Sem caixa/título: os campos do departamento aparecem como os demais.
  ticketDepartmentFieldsEl.innerHTML = renderTemplateFields('field', currentDepartmentFields);
  ticketDepartmentFieldsEl.classList.remove('hidden');
  attachInputMasks(ticketDepartmentFieldsEl);

  // Restaura o que ja havia sido digitado...
  currentDepartmentFields.forEach(function (field) {
    if (!field || !field.definitionId) return;
    if (Object.prototype.hasOwnProperty.call(previousValues, field.definitionId)) {
      setTemplateControlValue(templateInputId('field', field.definitionId), field.dataType, previousValues[field.definitionId]);
    }
  });

  // ...e aplica os defaults do modelo somente nos campos ainda vazios.
  var defaults = parseTemplateDefaults(defaultsJson);
  currentDepartmentFields.forEach(function (field) {
    if (!field || !field.definitionId) return;
    if (Object.prototype.hasOwnProperty.call(previousValues, field.definitionId)) return;
    if (Object.prototype.hasOwnProperty.call(defaults, field.definitionId)) {
      setTemplateControlValue(templateInputId('field', field.definitionId), field.dataType, defaults[field.definitionId]);
    }
  });
}

function selectedTemplateDefaultsJson() {
  var template = findTicketTemplate(ticketTemplateSelectEl ? ticketTemplateSelectEl.value : '');
  return template ? template.customFieldDefaultsJson : '';
}

function selectedDepartmentId() {
  var select = document.getElementById('ticketDepartment');
  return select ? select.value : '';
}

var departmentFieldsCache = {};
// TTL curto: campo criado no servidor aparece sem precisar reiniciar o app.
var DEPARTMENT_FIELDS_CACHE_TTL_MS = 2 * 60 * 1000;
// Perfil (scheme|server|agentId) e ultimo modelo aplicado: usados para
// invalidar caches quando o agente troca de servidor e para nao sobrescrever
// texto digitado pelo usuario.
var lastSupportProfileKey = '';
var lastAppliedTemplateId = '';

function loadDepartmentFields(departmentId) {
  var api = appApi();
  if (!departmentId || !api || typeof api.GetTicketDepartmentFields !== 'function') {
    clearDepartmentFields();
    return;
  }
  var requested = departmentId;
  var cached = departmentFieldsCache[requested];
  if (cached && cached.fields && (Date.now() - Number(cached.at || 0)) <= DEPARTMENT_FIELDS_CACHE_TTL_MS) {
    renderDepartmentFields(cached.fields, selectedTemplateDefaultsJson());
    return;
  }
  var loadId = beginTicketDepartmentFormLoad(requested);
  api.GetTicketDepartmentFields(departmentId).then(function (fields) {
    var list = Array.isArray(fields) ? fields : [];
    departmentFieldsCache[requested] = { fields: list, at: Date.now() };
    // Ignora resposta tardia de um departamento que nao esta mais selecionado.
    if (selectedDepartmentId() !== requested) return;
    renderDepartmentFields(list, selectedTemplateDefaultsJson());
  }).catch(function (err) {
    console.warn('[support] falha ao carregar campos do departamento:', err);
    clearDepartmentFields();
  }).finally(function () {
    endTicketDepartmentFormLoad(loadId);
  });
}

function collectDepartmentFieldValues() {
  var result = { values: {}, missing: [] };
  currentDepartmentFields.forEach(function (field) {
    if (!field || !field.definitionId) return;
    var value = readTemplateControlValue(templateInputId('field', field.definitionId), field.dataType);
    if (value === undefined) {
      if (field.isRequired) result.missing.push(field.label || field.name || field.definitionId);
      return;
    }
    result.values[field.definitionId] = value;
  });
  return result;
}

// Preenche o campo apenas se o usuario nao tiver mexido nele: vazio ou ainda
// com o valor do template aplicado anteriormente.
function prefillIfUntouched(el, nextValue, previousValue) {
  if (!el || !nextValue) return;
  var current = String(el.value || '').trim();
  if (current === '' || (previousValue != null && current === String(previousValue))) {
    el.value = nextValue;
  }
}

function applyTicketTemplate(templateId) {
  var template = findTicketTemplate(templateId);
  var previousTemplate = findTicketTemplate(lastAppliedTemplateId);
  if (!template) {
    clearTicketTemplateExtra();
    lastAppliedTemplateId = '';
    // Sem modelo, os campos do departamento continuam valendo.
    loadDepartmentFields(selectedDepartmentId());
    return;
  }
  // Prefill sem sobrescrever o que o usuario ja digitou.
  prefillIfUntouched(
    document.getElementById('ticketTitle'),
    template.title,
    previousTemplate ? previousTemplate.title : null);
  prefillIfUntouched(
    document.getElementById('ticketDescription'),
    template.description,
    previousTemplate ? previousTemplate.description : null);
  // Pre-preenche os campos definidos pelo servidor (o modelo traz categoria e
  // prioridade). O valor entra mesmo que nao esteja na lista do departamento.
  setTicketFormSchemaValue('category', template.category);
  var priority = templatePriorityToInt(template.priority);
  if (priority) setTicketFormSchemaValue('priority', String(priority));
  // O departamento NAO e alterado pelo modelo: ele foi escolhido antes e o
  // modelo ja foi filtrado por ele.
  renderTicketTemplateExtra(template);
  // Os campos sao do departamento (nao do template): recarrega para o
  // departamento efetivo, aplicando os defaults do modelo.
  loadDepartmentFields(selectedDepartmentId());
  lastAppliedTemplateId = template.id;
}

function hasNumberMask(el) {
  return !!(el && el.getAttribute && el.getAttribute('data-number-mask'));
}

function readTemplateControlValue(idAttr, dataType) {
  var el = document.getElementById(idAttr);
  if (!el) return undefined;
  if (dataType === 'ListBox') {
    var selected = [];
    for (var i = 0; i < el.options.length; i++) {
      if (el.options[i].selected) selected.push(el.options[i].value);
    }
    return selected.length ? selected : undefined;
  }
  var raw = el.value;
  if (raw === '' || raw === null || raw === undefined) return undefined;
  if (dataType === 'Integer') {
    var intRaw = hasNumberMask(el)
      ? normalizeNumericDraftForMask(el.getAttribute('data-number-mask'), raw, dataType)
      : raw;
    var intValue = parseInt(intRaw, 10);
    return Number.isFinite(intValue) ? intValue : undefined;
  }
  if (dataType === 'Decimal') {
    var decRaw = hasNumberMask(el)
      ? normalizeNumericDraftForMask(el.getAttribute('data-number-mask'), raw, dataType)
      : raw;
    var decValue = parseFloat(decRaw);
    return Number.isFinite(decValue) ? decValue : undefined;
  }
  if (dataType === 'Boolean') return raw === 'true';
  return raw;
}

function collectTicketTemplateExtra(template) {
  var result = { templateAnswers: {}, missing: [] };
  if (!template) return result;
  (template.questions || []).forEach(function (q) {
    if (!q || !q.key) return;
    var value = readTemplateControlValue(templateInputId('q', q.key), q.dataType);
    if (value === undefined) {
      if (q.isRequired) result.missing.push(q.label || q.key);
      return;
    }
    result.templateAnswers[q.key] = value;
  });
  return result;
}

function ticketPriorityLabel(p) { return priorityLabels[p] || 'N/A'; }
function ticketPriorityClass(p) { return priorityClasses[p] || 'p-media'; }

function renderStars(rating) {
  if (rating === null || rating === undefined || rating === '') return '';
  var value = Number(rating);
  if (!Number.isFinite(value) || value <= 0) return '';
  var full = Math.min(5, Math.floor(value));
  return '★★★★★'.slice(0, full) + '☆☆☆☆☆'.slice(0, 5 - full) + ' (' + value + '/5)';
}

// ── Estados de workflow (nome/cor/isFinal) ─────────────────────────────────

function buildWorkflowProfileKey(cfg) {
  if (!cfg) return 'default';
  var scheme = String(cfg.apiScheme || '').trim().toLowerCase();
  var server = String(cfg.apiServer || '').trim().toLowerCase();
  var agentId = String(cfg.agentId || '').trim().toLowerCase();
  return [scheme, server, agentId].join('|');
}

function workflowStatesStorageKey(profileKey) {
  return 'discovery.support.workflow-states.v1.' + profileKey;
}

function readWorkflowStatesLocal(profileKey) {
  try {
    if (!window.localStorage) return null;
    var raw = window.localStorage.getItem(workflowStatesStorageKey(profileKey));
    if (!raw) return null;

    var payload = JSON.parse(raw);
    if (!payload || !Array.isArray(payload.states) || !payload.expiresAt) {
      window.localStorage.removeItem(workflowStatesStorageKey(profileKey));
      return null;
    }

    if (Date.now() > Number(payload.expiresAt)) {
      window.localStorage.removeItem(workflowStatesStorageKey(profileKey));
      return null;
    }

    return payload.states;
  } catch (e) {
    return null;
  }
}

function writeWorkflowStatesLocal(profileKey, states) {
  try {
    if (!window.localStorage) return;
    var payload = {
      states: Array.isArray(states) ? states : [],
      expiresAt: Date.now() + WORKFLOW_STATES_CACHE_TTL_MS,
    };
    window.localStorage.setItem(workflowStatesStorageKey(profileKey), JSON.stringify(payload));
  } catch (e) {
    // ignore storage quota/privacy errors
  }
}

function indexWorkflowStates(states) {
  workflowStatesById = {};
  (states || []).forEach(function (s) {
    if (s && s.id) workflowStatesById[s.id] = s;
  });
}

// ensureWorkflowStates resolve a lista de estados uma unica vez por perfil,
// reaproveitando cache em memoria + localStorage. E usada tanto pelo seletor de
// estado final do modal quanto pela classificacao/filtro da listagem.
async function ensureWorkflowStates(force) {
  var profileKey = 'default';
  try {
    var cfg = await appApi().GetDebugConfig();
    profileKey = buildWorkflowProfileKey(cfg);
  } catch (e) {
    profileKey = 'default';
  }

  if (profileKey !== lastSupportProfileKey) {
    // Servidor/agente mudou: caches de campos/modelos do perfil antigo nao valem.
    lastSupportProfileKey = profileKey;
    departmentFieldsCache = {};
    ticketTemplatesCache = [];
    lastAppliedTemplateId = '';
  }

  // Um resultado vazio tambem e cacheavel: sem isso um cliente sem estados
  // configurados refaz a chamada a cada carga da lista/detalhe.
  if (!force && workflowStatesCacheKey === profileKey && workflowStatesCache !== null
    && (Date.now() - workflowStatesLoadedAt) < WORKFLOW_STATES_CACHE_TTL_MS) {
    indexWorkflowStates(workflowStatesCache);
    return workflowStatesCache;
  }

  var localStates = readWorkflowStatesLocal(profileKey);
  if (!force && localStates !== null) {
    workflowStatesCache = localStates;
    workflowStatesCacheKey = profileKey;
    workflowStatesLoadedAt = Date.now();
    indexWorkflowStates(workflowStatesCache);
    return workflowStatesCache;
  }

  try {
    var states = await appApi().GetTicketWorkflowStates();
    workflowStatesCache = Array.isArray(states) ? states : [];
    workflowStatesCacheKey = profileKey;
    workflowStatesLoadedAt = Date.now();
    writeWorkflowStatesLocal(profileKey, workflowStatesCache);
    indexWorkflowStates(workflowStatesCache);
    return workflowStatesCache;
  } catch (err) {
    console.warn('[support] falha ao carregar estados de workflow:', err);
    return workflowStatesCache && workflowStatesCacheKey === profileKey ? workflowStatesCache : [];
  }
}

function currentTicketStateId(ticket) {
  if (!ticket) return '';
  return ticket.workflowStateId || (ticket.workflowState ? ticket.workflowState.id : '');
}

function populateWorkflowStateOptions(states, currentWorkflowStateId) {
  if (!closeTicketWorkflowStateSelectEl) return;

  var options = ['<option value="">' + escapeHtml(translate('support.closeWithDefaultState')) + '</option>'];
  var finalStates = (states || []).filter(function (s) { return !!s && s.isFinal; });
  var available = finalStates.length ? finalStates : (states || []);

  available.forEach(function (s) {
    var label = s.name || (s.id ? ('Estado ' + s.id.substring(0, 8)) : 'Estado');
    if (s.isFinal) label += ' (Final)';
    if (s.displayOrder || s.displayOrder === 0) label += ' - Ordem ' + s.displayOrder;
    options.push('<option value="' + escapeHtmlAttr(s.id) + '">' + escapeHtml(label) + '</option>');
  });

  closeTicketWorkflowStateSelectEl.innerHTML = options.join('');
  // Sem estados reais (ex.: endpoint ainda indisponivel) desabilita o seletor
  // em vez de oferecer um estado final que o backend ignoraria.
  closeTicketWorkflowStateSelectEl.disabled = available.length === 0;

  if (currentWorkflowStateId && available.some(function (s) { return s.id === currentWorkflowStateId; })) {
    closeTicketWorkflowStateSelectEl.value = currentWorkflowStateId;
  } else {
    closeTicketWorkflowStateSelectEl.value = '';
  }
}

async function loadWorkflowStatesForClose(ticket) {
  if (!closeTicketWorkflowStateSelectEl) return;
  var ticketId = ticket ? ticket.id : '';
  var states = await ensureWorkflowStates(false);
  // O detalhe pode ter mudado enquanto os estados carregavam.
  if (ticketId && currentTicketId !== ticketId) return;
  if (states && states.length) {
    populateWorkflowStateOptions(states, currentTicketStateId(ticket));
    return;
  }
  closeTicketWorkflowStateSelectEl.innerHTML =
    '<option value="">' + escapeHtml(translate('support.closeWithDefaultState')) + '</option>';
  closeTicketWorkflowStateSelectEl.value = '';
  closeTicketWorkflowStateSelectEl.disabled = true;
}

function showTicketFormStatus(msg, isError) {
  if (!ticketFormStatusEl) return;
  ticketFormStatusEl.textContent = msg;
  ticketFormStatusEl.className = 'form-status' + (isError ? ' form-status-error' : ' form-status-ok');
  ticketFormStatusEl.classList.remove('hidden');
}
function hideTicketFormStatus() {
  if (ticketFormStatusEl) ticketFormStatusEl.classList.add('hidden');
}

// ── Widget de estrelas reutilizavel (modal de fechamento + avaliacao) ──────

function attachStarsWidget(containerEl, clearBtnEl) {
  var widget = { value: 0, onChange: null };

  function paint() {
    if (clearBtnEl) clearBtnEl.classList.toggle('hidden', widget.value === 0);
    if (!containerEl) return;
    containerEl.querySelectorAll('.star-btn').forEach(function (s) {
      var v = parseInt(s.getAttribute('data-value'), 10);
      s.classList.toggle('active', v <= widget.value);
      s.setAttribute('aria-pressed', String(v === widget.value));
    });
  }

  widget.set = function (value) {
    var parsed = parseInt(value, 10);
    widget.value = Number.isFinite(parsed) && parsed > 0 ? Math.min(5, parsed) : 0;
    paint();
    if (typeof widget.onChange === 'function') widget.onChange(widget.value);
  };
  widget.reset = function () { widget.set(0); };

  if (containerEl) {
    containerEl.querySelectorAll('.star-btn').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var v = parseInt(btn.getAttribute('data-value'), 10);
        widget.set(v === widget.value ? 0 : v);
      });
    });
  }
  paint();
  return widget;
}

function setStars(value) {
  if (closeTicketStarsWidget) closeTicketStarsWidget.set(value);
}

function resetRating() {
  setStars(0);
}

// ── Inicializacao ──────────────────────────────────────────────────────────

function initSupport() {
  if (!supportFormEl) return;

  // Bind independente do resto do init: se algo abaixo falhar, as listas
  // continuam repassando a roda para a pagina.
  bindSupportScrollChaining();
  // Bloqueio/aviso dirigidos pelo estado real de conexão (mesmo quando a
  // listagem ainda não foi recarregada).
  initSupportConnectivity();

  closeTicketStarsWidget = attachStarsWidget(closeTicketStarsEl, clearRatingBtnEl);
  ratingStarsWidget = attachStarsWidget(ratingStarsEl, clearRatingPanelBtnEl);

  supportFormEl.addEventListener('submit', async function (e) {
    e.preventDefault();
    var title = document.getElementById('ticketTitle') ? document.getElementById('ticketTitle').value.trim() : '';
    // Categoria/prioridade (e qualquer campo futuro) vem do schema do
    // servidor para o departamento escolhido. Campo que o servidor nao mandou
    // nao existe no formulario e nao vai no payload.
    var category = readTicketFormSchemaValue('category');
    var priority = parseInt(readTicketFormSchemaValue('priority') || '0', 10);
    var departmentId = document.getElementById('ticketDepartment') ? document.getElementById('ticketDepartment').value : '';
    var description = document.getElementById('ticketDescription') ? document.getElementById('ticketDescription').value.trim() : '';
    var templateId = ticketTemplateSelectEl ? ticketTemplateSelectEl.value : '';
    var template = findTicketTemplate(templateId);

    if (!title || !description) {
      showToast(translate('support.fillTitleDescription'), 'error');
      return;
    }
    // Departamento e obrigatorio: define responsavel (auto-atribuicao) e SLA.
    if (!departmentId) {
      showToast(translate('support.selectDepartment'), 'error');
      return;
    }
    // Defesa em profundidade: o botao fica desabilitado durante a carga, mas
    // enviar antes da resposta do servidor pulava a validacao dos campos
    // obrigatorios do departamento (nenhum campo estava renderizado ainda).
    if (ticketDepartmentFormIsLoading(departmentId)) {
      showToast(translate('support.departmentFormLoading'), 'info');
      return;
    }

    // Obrigatorios: perguntas do modelo + campos personalizados do departamento.
    var templateExtra = collectTicketTemplateExtra(template);
    var departmentExtra = collectDepartmentFieldValues();
    var missingFields = templateExtra.missing
      .concat(departmentExtra.missing)
      .concat(collectTicketFormSchemaMissing());
    if (missingFields.length) {
      showToast(translate('support.fieldRequired', { fields: missingFields.join(', ') }), 'error');
      return;
    }

    // Regras dos campos (min/max, validationRegex) no cliente; o servidor
    // continua validando.
    var validationMessages = []
      .concat(numericBoundsFailures(currentDepartmentFields, 'field'))
      .concat(numericBoundsFailures(template ? template.questions : [], 'q'))
      .concat(regexValidationFailures(currentDepartmentFields, 'field'))
      .concat(regexValidationFailures(template ? template.questions : [], 'q'));
    if (validationMessages.length) {
      showToast(validationMessages.join(' '), 'error');
      return;
    }

    var btn = document.getElementById('submitTicketBtn');
    if (btn) { btn.disabled = true; btn.textContent = translate('support.sending'); }
    showTicketFormStatus(translate('support.submittingTicket'), false);

    try {
      var payload = { title: title, description: description, departmentId: departmentId };
      if (category) payload.category = category;
      if (Number.isFinite(priority) && priority > 0) payload.priority = priority;
      if (Object.keys(departmentExtra.values).length) payload.customFieldValues = departmentExtra.values;
      if (templateId) {
        payload.templateId = templateId;
        if (Object.keys(templateExtra.templateAnswers).length) payload.templateAnswers = templateExtra.templateAnswers;
      }
      await appApi().CreateSupportTicket(payload);
      showToast(translate('support.ticketCreatedSuccess'), 'success');
      supportFormEl.reset();
      clearTicketOpenFields();
      hideTicketFormStatus();
      showSupportList();
      loadSupportTickets();
    } catch (err) {
      showTicketFormStatus(translate('support.ticketCreateError', { error: String(err) }), true);
      showToast(translate('support.ticketCreateError', { error: String(err) }), 'error');
    } finally {
      if (btn) btn.textContent = translate('action.sendTicket');
      // O botao volta ao estado calculado (online E departamento escolhido).
      updateTicketOpenFieldsVisibility();
    }
  });

  if (ticketTemplateSelectEl) {
    ticketTemplateSelectEl.addEventListener('change', function () {
      applyTicketTemplate(ticketTemplateSelectEl.value);
    });
  }
  var ticketDepartmentSelect = document.getElementById('ticketDepartment');
  if (ticketDepartmentSelect) {
    // Departamento primeiro: ao trocar, reseta o modelo e recarrega os campos
    // personalizados e os modelos permitidos para o novo departamento.
    ticketDepartmentSelect.addEventListener('change', function () {
      // Trocar o departamento invalida tudo que era dele: modelo, campos
      // personalizados e o schema (campos/opcoes) fornecido pelo servidor.
      clearTicketOpenFields();
      updateTicketOpenFieldsVisibility();
      renderTicketTemplateOptions();
      loadTicketDepartmentFormSchema(ticketDepartmentSelect.value);
      loadDepartmentFields(ticketDepartmentSelect.value);
    });
  }

  if (refreshTicketsBtnEl) {
    refreshTicketsBtnEl.addEventListener('click', function () { loadSupportTickets(); });
  }
  if (newTicketBtnEl) {
    newTicketBtnEl.addEventListener('click', function () { showNewTicketForm(); });
  }
  if (backFromNewBtnEl) {
    backFromNewBtnEl.addEventListener('click', function () { showSupportList(); });
  }
  if (backToListBtnEl) {
    backToListBtnEl.addEventListener('click', function () { closeTicketDetail(); });
  }
  if (submitCommentBtnEl) {
    submitCommentBtnEl.addEventListener('click', submitTicketComment);
  }
  if (commentInputEl) {
    // Ctrl+Enter envia o comentario (Enter livre para quebras de linha)
    commentInputEl.addEventListener('keydown', function (e) {
      if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
        e.preventDefault();
        submitTicketComment();
      }
    });
  }
  if (ticketSearchInputEl) {
    ticketSearchInputEl.addEventListener('input', debounce(function () {
      ticketFilters.query = ticketSearchInputEl.value;
      updateTicketFiltersToggle();
      renderSupportTicketList(supportTicketsAll);
    }, 200));
  }
  if (ticketStatusFilterEl) {
    ticketStatusFilterEl.addEventListener('change', function () {
      ticketFilters.status = ticketStatusFilterEl.value || '__default';
      updateTicketFiltersToggle();
      renderSupportTicketList(supportTicketsAll);
    });
  }
  if (ticketPriorityFilterEl) {
    ticketPriorityFilterEl.addEventListener('change', function () {
      ticketFilters.priority = ticketPriorityFilterEl.value || 'all';
      updateTicketFiltersToggle();
      renderSupportTicketList(supportTicketsAll);
    });
  }
  if (clearTicketFiltersBtnEl) {
    clearTicketFiltersBtnEl.addEventListener('click', clearTicketFilters);
  }
  if (toggleTicketFiltersBtnEl) {
    toggleTicketFiltersBtnEl.addEventListener('click', toggleTicketFiltersPanel);
  }
  updateTicketFiltersToggle();

  if (reopenTicketBtnEl) {
    reopenTicketBtnEl.addEventListener('click', reopenTicket);
  }
  if (submitRatingBtnEl) {
    submitRatingBtnEl.addEventListener('click', submitTicketRating);
  }
  if (clearRatingPanelBtnEl) {
    clearRatingPanelBtnEl.addEventListener('click', function () {
      if (ratingStarsWidget) ratingStarsWidget.reset();
    });
  }

  if (closeTicketBtnEl) {
    closeTicketBtnEl.addEventListener('click', async function () {
      if (!currentTicketId) return;

      var rating = closeTicketStarsWidget && closeTicketStarsWidget.value > 0 ? closeTicketStarsWidget.value : null;
      var comment = closeTicketCommentEl ? closeTicketCommentEl.value.trim() : '';
      var workflowStateId = closeTicketWorkflowStateSelectEl ? closeTicketWorkflowStateSelectEl.value.trim() : '';

      closeTicketBtnEl.disabled = true;
      closeTicketBtnEl.textContent = translate('support.closing');

      try {
        var payload = {};
        if (comment) payload.comment = comment;
        if (workflowStateId) payload.workflowStateId = workflowStateId;
        if (rating !== null) payload.rating = rating;
        var ticket = await appApi().CloseSupportTicket(currentTicketId, payload);
        showToast(translate('support.ticketClosedSuccess'), 'success');
        if (ticket && ticket.id) {
          currentTicket = ticket;
          renderTicketDetail(ticket);
        }
        hideCloseTicketModal();
        resetRating();
        if (closeTicketCommentEl) closeTicketCommentEl.value = '';
        if (closeTicketWorkflowStateSelectEl) closeTicketWorkflowStateSelectEl.value = '';
        // Mantem o detalhe aberto: se o chamado ficou sem nota, o painel de
        // avaliacao aparece imediatamente.
        await loadSupportTickets({ keepView: true });
      } catch (err) {
        showToast(translate('support.ticketCloseError', { error: String(err) }), 'error');
      } finally {
        closeTicketBtnEl.disabled = false;
        closeTicketBtnEl.textContent = translate('action.closeTicket');
      }
    });
  }

  if (openCloseTicketBtnEl) {
    openCloseTicketBtnEl.addEventListener('click', function () {
      if (!currentTicketId) return;
      showCloseTicketModal();
    });
  }

  if (closeTicketModalCancelBtnEl) {
    closeTicketModalCancelBtnEl.addEventListener('click', function () {
      hideCloseTicketModal();
    });
  }

  if (closeTicketModalCancelBottomBtnEl) {
    closeTicketModalCancelBottomBtnEl.addEventListener('click', function () {
      hideCloseTicketModal();
    });
  }

  if (closeTicketModalEl) {
    closeTicketModalEl.addEventListener('click', function (e) {
      if (e.target === closeTicketModalEl) {
        hideCloseTicketModal();
      }
    });
  }
}

// ── Listagem ───────────────────────────────────────────────────────────────

async function loadSupportTickets(opts) {
  if (!supportTicketsListEl) return;
  var options = opts || {};
  if (!options.keepView) showSupportList();

  supportTicketsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('common.loading')) + '</div>';
  if (ticketsResultCountEl) ticketsResultCountEl.textContent = '';

  var staleBanner = document.getElementById('supportStaleBanner');

  try {
    var api = appApi();
    var tickets;
    // Binding com status: permite avisar que a lista veio do cache offline.
    if (typeof api.GetSupportTicketsWithStatus === 'function') {
      var result = await api.GetSupportTicketsWithStatus();
      // Tolera tanto o envelope {tickets,stale} quanto um array cru (binário
      // antigo/build sem bindings regenerados).
      tickets = result && Array.isArray(result.tickets)
        ? result.tickets
        : (Array.isArray(result) ? result : []);
      renderSupportCacheState(result);
    } else {
      tickets = await api.GetSupportTickets();
      renderSupportCacheState(null);
    }
    supportTicketsAll = Array.isArray(tickets) ? tickets : [];
    supportTicketsLoadFailed = false;
    var states = [];
    try { states = await ensureWorkflowStates(false); } catch (e) { states = []; }
    renderTicketFilterControls(states);
    renderSupportTicketList(supportTicketsAll);
  } catch (err) {
    supportTicketsAll = [];
    supportTicketsById = {};
    supportTicketsLoadFailed = true;
    // Sem lista nem cache não há como saber o estado: assume somente consulta.
    supportOfflineReadOnly = true;
    applySupportOfflineMode();
    if (staleBanner) staleBanner.classList.add('hidden');
    supportTicketsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('support.ticketListLoadError', { error: String(err) })) + '</div>';
  }
}

function renderSupportTicketList(tickets) {
  if (!supportTicketsListEl) return;
  if (supportTicketsLoadFailed) return;

  var visible = sortTickets(filterTickets(tickets));
  var searchTerms = ticketSearchTerms();
  if (ticketsResultCountEl) {
    ticketsResultCountEl.textContent = visible.length
      ? translate('support.resultsCount', { count: visible.length })
      : '';
  }

  if (!visible.length) {
    supportTicketsById = {};
    var emptyKey = (tickets && tickets.length) ? 'support.noTicketsFiltered' : 'support.noTicketsPrompt';
    supportTicketsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate(emptyKey)) + '</div>';
    return;
  }

  // Cache local por id: apenas o que pode gerar acao. Fechado+avaliado e
  // buscado ao vivo quando um filtro explicito o exibe.
  var cache = {};
  supportTicketsListEl.innerHTML = visible.map(function (t) {
    if (!(isTicketClosed(t) && isTicketRated(t))) {
      cache[t.id] = t;
    }
    var status = ticketStatusInfo(t);
    var priLabel = ticketPriorityLabel(t.priority);
    var priClass = ticketPriorityClass(t.priority);
    var cat = t.category || '';
    var date = formatDate(t.createdAt, '');
    var lastActivity = ticketLastActivityText(t);
    var ratingText = renderStars(t.rating);
    var awaitingBadge = isTicketAwaitingRating(t)
      ? '<span class="ticket-awaiting-rating-badge">' + escapeHtml(translate('support.awaitingRatingBadge')) + '</span>'
      : '';
    var snippet = '';
    if (searchTerms.length) {
      var visibleText = foldSearchTextPreserveLength((t.title || '') + ' ' + (t.category || '') + ' ' + String(t.id || ''));
      var descriptionText = foldSearchTextPreserveLength(t.description || '');
      var matchedOnlyInDescription = searchTerms.some(function (term) {
        return descriptionText.indexOf(term) >= 0 && visibleText.indexOf(term) < 0;
      });
      if (matchedOnlyInDescription) snippet = descriptionSnippet(t.description, searchTerms);
    }
    return '<button class="support-ticket-card" data-id="' + escapeHtml(t.id) + '">' +
      '<div class="ticket-subject">' + highlightSearchTerms(t.title || translate('support.untitledTicket'), searchTerms) + '</div>' +
      '<div class="ticket-header">' +
        '<span class="ticket-id-badge">#' + escapeHtml(String(t.id).substring(0, 8)) + '</span>' +
        '<span class="ticket-status-badge"' + safeStatusBadgeStyle(status.color) + '>' + escapeHtml(status.name) + '</span>' +
        '<span class="ticket-priority-badge ' + priClass + '">' + escapeHtml(priLabel) + '</span>' +
        awaitingBadge +
      '</div>' +
      '<div class="ticket-meta">' +
        (cat ? '<span>' + highlightSearchTerms(cat, searchTerms) + '</span>' : '') +
        (date ? '<span>' + escapeHtml(translate('support.openedAtShort', { date: date })) + '</span>' : '') +
        (lastActivity ? '<span>' + escapeHtml(lastActivity) + '</span>' : '') +
        (ratingText ? '<span>' + escapeHtml(ratingText) + '</span>' : '') +
      '</div>' +
      (snippet ? '<div class="ticket-snippet">' + highlightSearchTerms(snippet, searchTerms) + '</div>' : '') +
    '</button>';
  }).join('');
  supportTicketsById = cache;

  supportTicketsListEl.querySelectorAll('.support-ticket-card').forEach(function (card) {
    card.addEventListener('click', function () {
      openTicketFromCard(card.getAttribute('data-id'));
    });
  });
}

async function openTicketFromCard(ticketId) {
  try {
    if (!ticketId) throw new Error('ticketId vazio');
    var ticket = supportTicketsById[ticketId];
    if (!ticket) {
      // Fora do cache local (ex.: fechado+avaliado visto por filtro explicito).
      ticket = await appApi().GetSupportTicketDetails(ticketId);
    }
    if (!ticket) throw new Error('ticket não encontrado');
    showTicketDetail(ticket);
  } catch (e) {
    console.error('support: falha ao abrir ticket do card:', e);
    if (typeof showFeedback === 'function') {
      showFeedback(translate('support.ticketLoadError', { error: String(e) }), true);
    }
  }
}

// ── Detalhe ────────────────────────────────────────────────────────────────

function showTicketDetail(t) {
  if (!t) return;
  currentTicketId = t.id;
  currentTicket = t;
  showSupportDetail();

  // Troca de chamado: limpa a avaliacao em rascunho do detalhe anterior.
  if (ratingStarsWidget) ratingStarsWidget.reset();
  if (ratingFeedbackEl) ratingFeedbackEl.value = '';

  renderTicketDetail(t);
  loadWorkflowStatesForClose(t);
  loadTicketComments(t.id);
  loadTicketFields(t.id);
  ensureWorkflowStates(false).then(function () {
    if (currentTicketId === t.id && currentTicket) renderTicketDetail(currentTicket);
  }).catch(function () { /* mantem o render atual */ });

  appApi().GetSupportTicketDetails(t.id)
    .then(function (fresh) {
      if (!fresh || currentTicketId !== t.id) return;
      currentTicket = fresh;
      renderTicketDetail(fresh);
      loadWorkflowStatesForClose(fresh);
    })
    .catch(function () { /* mantem dados ja exibidos */ });
}

function renderTicketDetail(t) {
  if (!t) return;
  var status = ticketStatusInfo(t);
  var priLabel = ticketPriorityLabel(t.priority);
  var priClass = ticketPriorityClass(t.priority);
  var date = formatDate(t.createdAt, '');
  var cat = t.category || '';
  var ratedAt = formatDate(t.ratedAt, '');
  var ratedBy = t.ratedBy || '';
  var closed = isTicketClosed(t);
  var rating = getTicketRating(t);
  var lastActivity = ticketLastActivityText(t);

  if (ticketDetailIdEl) ticketDetailIdEl.textContent = '#' + String(t.id || '').substring(0, 8);
  if (ticketDetailStatusEl) {
    ticketDetailStatusEl.textContent = status.name;
    // B14: cor do servidor validada antes de aplicar no style.
    var _statusColor = safeCssColor(status.color);
    if (_statusColor) {
      ticketDetailStatusEl.style.background = 'color-mix(in srgb, ' + _statusColor + ' 14%, transparent)';
      ticketDetailStatusEl.style.color = _statusColor;
    }
  }
  if (ticketDetailPriorityEl) {
    ticketDetailPriorityEl.textContent = priLabel;
    ticketDetailPriorityEl.className = 'ticket-priority-badge ' + priClass;
  }
  if (ticketDetailTitleEl) ticketDetailTitleEl.textContent = t.title || translate('support.untitledTicket');
  if (ticketDetailMetaEl) {
    ticketDetailMetaEl.innerHTML =
      (cat ? '<span>' + escapeHtml(cat) + '</span>' : '') +
      (t.templateName ? '<span>' + escapeHtml(translate('support.templateLabel', { name: t.templateName })) + '</span>' : '') +
      (date ? '<span>' + escapeHtml(translate('support.ticketOpenedAt', { date: date })) + '</span>' : '') +
      (lastActivity ? '<span>' + escapeHtml(lastActivity) + '</span>' : '') +
      (rating > 0 ? '<span>' + escapeHtml(translate('support.ratingDisplay', { rating: renderStars(rating) })) + '</span>' : '') +
      (ratedAt ? '<span>' + escapeHtml(translate('support.ratedAt', { date: ratedAt })) + '</span>' : '') +
      (ratedBy ? '<span>' + escapeHtml(translate('support.ratedBy', { name: ratedBy })) + '</span>' : '');
  }
  if (ticketDetailDescEl) ticketDetailDescEl.textContent = t.description || '';

  // Fechado: sem botao de fechar e sem compositor de comentario; oferece
  // reabertura e a avaliacao (CSAT) quando ainda nao avaliado.
  if (openCloseTicketBtnEl) openCloseTicketBtnEl.classList.toggle('hidden', closed);
  if (commentComposeEl) commentComposeEl.classList.toggle('hidden', closed);
  if (ticketClosedNoticeEl) ticketClosedNoticeEl.classList.toggle('hidden', !closed);
  if (closed && ticketClosedNoticeTextEl) {
    ticketClosedNoticeTextEl.textContent = t.closedAt
      ? translate('support.closedNotice', { date: formatDate(t.closedAt, '') })
      : translate('support.closedNoticeNoDate');
  }

  var showRatingPanel = closed && rating === 0;
  if (ticketRatingPanelEl) ticketRatingPanelEl.classList.toggle('hidden', !showRatingPanel);
  if (showRatingPanel && ratingStarsWidget) ratingStarsWidget.reset();
  if (ticketRatingSummaryEl) ticketRatingSummaryEl.classList.toggle('hidden', rating === 0);
  if (rating > 0 && ticketRatingSummaryBodyEl) {
    var parts = ['<div class="rating-summary-stars">' + escapeHtml(renderStars(rating)) + '</div>'];
    if (t.ratingFeedback) parts.push('<div class="rating-summary-feedback">' + escapeHtml(t.ratingFeedback) + '</div>');
    if (ratedAt) parts.push('<div class="meta">' + escapeHtml(translate('support.ratedAt', { date: ratedAt })) + '</div>');
    if (ratedBy) parts.push('<div class="meta">' + escapeHtml(translate('support.ratedBy', { name: ratedBy })) + '</div>');
    ticketRatingSummaryBodyEl.innerHTML = parts.join('');
  }
}

function closeTicketDetail() {
  currentTicketId = '';
  currentTicket = null;
  showSupportList();
  hideCloseTicketModal();
  resetRating();
  if (ratingStarsWidget) ratingStarsWidget.reset();
  if (ratingFeedbackEl) ratingFeedbackEl.value = '';
  if (closeTicketWorkflowStateSelectEl) closeTicketWorkflowStateSelectEl.value = '';
  if (closeTicketCommentEl) closeTicketCommentEl.value = '';
}

// ── Acoes do detalhe ───────────────────────────────────────────────────────

// Rebusca o detalhe exibido. Usado quando o servidor rejeita uma acao porque o
// chamado mudou de estado fora da UI (ex.: tecnico encerrou enquanto o
// compositor estava aberto).
async function refreshCurrentTicketDetail() {
  if (!currentTicketId) return;
  try {
    var fresh = await appApi().GetSupportTicketDetails(currentTicketId);
    if (fresh && currentTicketId === fresh.id) {
      currentTicket = fresh;
      renderTicketDetail(fresh);
    }
  } catch (e) {
    // mantem os dados ja exibidos
  }
}

async function submitTicketComment() {
  if (!currentTicketId || !commentInputEl || !submitCommentBtnEl) return;
  if (isTicketClosed(currentTicket)) {
    showToast(translate('support.commentsBlocked'), 'error');
    return;
  }
  var content = commentInputEl.value.trim();
  if (!content) { showToast(translate('support.enterComment'), 'error'); return; }
  submitCommentBtnEl.disabled = true;
  try {
    await appApi().AddTicketComment(currentTicketId, '', content);
    commentInputEl.value = '';
    showToast(translate('support.commentSent'), 'success');
    await loadTicketComments(currentTicketId);
  } catch (err) {
    showToast(translate('support.commentSendError', { error: String(err) }), 'error');
    // O estado local pode estar defasado (chamado encerrado no servidor):
    // sincroniza para esconder o compositor e oferecer a reabertura.
    await refreshCurrentTicketDetail();
  } finally {
    submitCommentBtnEl.disabled = false;
  }
}

async function reopenTicket() {
  if (!currentTicketId || !reopenTicketBtnEl) return;
  if (!window.confirm(translate('support.reopenConfirm'))) return;

  var previousText = reopenTicketBtnEl.textContent;
  reopenTicketBtnEl.disabled = true;
  reopenTicketBtnEl.textContent = translate('support.reopening');
  try {
    var ticket = await appApi().ReopenSupportTicket(currentTicketId);
    showToast(translate('support.reopenSuccess'), 'success');
    if (ticket && ticket.id) {
      currentTicket = ticket;
      renderTicketDetail(ticket);
    }
    await loadTicketComments(currentTicketId);
    await loadSupportTickets({ keepView: true });
    if (currentTicketId) renderTicketDetail(currentTicket);
  } catch (err) {
    showToast(translate('support.reopenError', { error: String(err) }), 'error');
  } finally {
    reopenTicketBtnEl.disabled = false;
    reopenTicketBtnEl.textContent = previousText || translate('support.reopenTicket');
  }
}

async function submitTicketRating() {
  if (!currentTicketId || !submitRatingBtnEl) return;
  var rating = ratingStarsWidget ? ratingStarsWidget.value : 0;
  if (rating < 1) {
    showToast(translate('support.selectRating'), 'error');
    return;
  }
  var feedback = ratingFeedbackEl ? ratingFeedbackEl.value.trim() : '';

  submitRatingBtnEl.disabled = true;
  submitRatingBtnEl.textContent = translate('support.ratingSending');
  try {
    var ticket = await appApi().RateSupportTicket(currentTicketId, rating, feedback);
    showToast(translate('support.ratingSuccess'), 'success');
    if (ticket && ticket.id) {
      currentTicket = ticket;
      renderTicketDetail(ticket);
    }
    if (ratingFeedbackEl) ratingFeedbackEl.value = '';
    // O chamado avaliado sai da listagem padrao; recarrega sem trocar de tela.
    await loadSupportTickets({ keepView: true });
  } catch (err) {
    showToast(translate('support.ratingError', { error: String(err) }), 'error');
    await refreshCurrentTicketDetail();
  } finally {
    submitRatingBtnEl.disabled = false;
    submitRatingBtnEl.textContent = translate('support.submitRating');
  }
}

// ── Campos personalizados do chamado (somente leitura) ────────────────────

function formatFieldValue(rawJson) {
  if (rawJson === null || rawJson === undefined || rawJson === '') return '';
  var value = rawJson;
  try {
    value = JSON.parse(rawJson);
  } catch (e) {
    // mantem o valor cru quando nao for JSON valido
  }
  if (value === null || value === undefined) return '';
  if (typeof value === 'boolean') return value ? translate('support.yes') : translate('support.no');
  if (Array.isArray(value)) return value.map(function (v) { return String(v); }).join(', ');
  return String(value);
}

function ticketDataRow(label, value) {
  var text = (value === null || value === undefined || value === '') ? '—' : String(value);
  return '<div class="ticket-field-row">' +
    '<span class="ticket-field-label">' + escapeHtml(label || '') + '</span>' +
    '<span class="ticket-field-value">' + escapeHtml(text) + '</span>' +
  '</div>';
}

async function loadTicketFields(ticketId) {
  if (!ticketCustomFieldsEl) return;
  ticketCustomFieldsEl.classList.add('hidden');
  ticketCustomFieldsEl.innerHTML = '';
  if (!ticketId) return;
  try {
    var api = appApi();
    // Uma falha (ex.: /answers indisponivel) nao pode esconder a outra secao.
    var fieldsPromise = typeof api.GetTicketFields === 'function'
      ? api.GetTicketFields(ticketId).catch(function () { return []; })
      : Promise.resolve([]);
    var answersPromise = typeof api.GetTicketAnswers === 'function'
      ? api.GetTicketAnswers(ticketId).catch(function () { return []; })
      : Promise.resolve([]);
    var results = await Promise.all([fieldsPromise, answersPromise]);
    if (currentTicketId !== ticketId) return;

    var fields = Array.isArray(results[0]) ? results[0] : [];
    var answers = Array.isArray(results[1]) ? results[1] : [];
    if (!fields.length && !answers.length) return;

    var parts = ['<h4>' + escapeHtml(translate('support.ticketFields')) + '</h4>'];
    if (fields.length) {
      parts.push('<div class="ticket-fields-group-title">' + escapeHtml(translate('support.templateFields')) + '</div>');
      parts.push(fields.map(function (f) {
        return ticketDataRow(f.label || f.name, formatFieldValue(f.valueJson));
      }).join(''));
    }
    if (answers.length) {
      parts.push('<div class="ticket-fields-group-title">' + escapeHtml(translate('support.ticketAnswers')) + '</div>');
      parts.push(answers.map(function (a) {
        return ticketDataRow(a.questionLabel || a.questionKey, a.valueText);
      }).join(''));
    }
    ticketCustomFieldsEl.innerHTML = parts.join('');
    ticketCustomFieldsEl.classList.remove('hidden');
  } catch (err) {
    console.warn('[support] falha ao carregar dados do chamado:', err);
  }
}

// ── Conversa do chamado (layout estilo chat) ───────────────────────────────
// Comentarios criados pelo usuario local/agent ficam a direita; os do suporte
// remoto (tecnico/portal) a esquerda — mesma linguagem visual do chat do app.

function isOwnTicketComment(comment) {
  return String((comment && comment.author) || '').trim().toLowerCase() === 'agent';
}

function formatChatDay(value) {
  var d = value instanceof Date ? value : new Date(value);
  if (isNaN(d.getTime())) return '';
  try {
    return d.toLocaleDateString(getAppLocaleTag(getAppLocale()));
  } catch (e) {
    return d.toLocaleDateString();
  }
}

function formatChatTime(value) {
  var d = value instanceof Date ? value : new Date(value);
  if (isNaN(d.getTime())) return '';
  try {
    return d.toLocaleTimeString(getAppLocaleTag(getAppLocale()), { hour: '2-digit', minute: '2-digit' });
  } catch (e) {
    return d.toLocaleTimeString();
  }
}

// Data + hora no proprio balao (o separador de dia centralizado foi removido:
// a data agora fica ao lado do nome de quem enviou, junto da hora).
function formatChatDayTime(value) {
  var day = formatChatDay(value);
  var time = formatChatTime(value);
  if (day && time) return day + ' ' + time;
  return day || time;
}

function ticketCommentHtml(c) {
  var own = isOwnTicketComment(c);
  var author = own ? translate('support.you') : (c.author || translate('support.supportTeam'));
  var when = formatChatDayTime(c.createdAt);
  return '<div class="ticket-msg ' + (own ? 'own' : 'other') + '">' +
    '<div class="ticket-bubble">' + escapeHtml(c.content) + '</div>' +
    '<div class="ticket-msg-meta">' +
      '<span class="ticket-msg-author">' + escapeHtml(author) + '</span>' +
      (when ? '<span class="ticket-msg-time">' + escapeHtml(when) + '</span>' : '') +
    '</div>' +
  '</div>';
}

async function loadTicketComments(ticketId) {
  if (!commentsListEl) return;
  commentsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('support.loadingComments')) + '</div>';
  try {
    var comments = await appApi().GetTicketComments(ticketId);
    // Ignora resposta tardia de um chamado que nao esta mais aberto (troca rapida).
    if (currentTicketId !== ticketId) return;
    if (!comments || !comments.length) {
      commentsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('support.noComments')) + '</div>';
      return;
    }
    // Cada balao carrega a propria data/hora no meta (autor + data + hora);
    // nao ha mais separador de dia centralizado entre as mensagens.
    var html = '';
    comments.forEach(function (c) {
      html += ticketCommentHtml(c);
    });
    commentsListEl.innerHTML = html;
    // Rola para o comentario mais recente
    commentsListEl.scrollTop = commentsListEl.scrollHeight;
  } catch (err) {
    if (currentTicketId !== ticketId) return;
    // Sem snapshot local, falha de rede vira mensagem amigável em vez do erro cru.
    var commentMsg = /conectar|deadline|timeout|connection|network|inacess/i.test(String(err))
      ? translate('support.commentsOffline')
      : translate('support.commentLoadError', { error: String(err) });
    commentsListEl.innerHTML = '<div class="meta">' + escapeHtml(commentMsg) + '</div>';
  }
}

// ── Rolagem: repasse da roda do mouse para a pagina ───────────────────────
// Listas com rolagem propria (.comments-list e .support-tickets-list) tem
// max-height. Ao chegar ao fim (topo/base) o WebView2 nao repassa a roda do
// mouse para a pagina, deixando a janela "travada". Repassamos o delta
// manualmente para o ancestral rolavel mais proximo — inclusive quando a lista
// nem tem overflow. Nao usamos overscroll-behavior: contain de proposito: se
// este JS nao rodar, o encadeamento nativo continua sendo a rede de seguranca.

// Elementos ja instrumentados (WeakSet: os elementos vem do DOM).
var scrollChainingBound = new WeakSet();

// Valor original de scroll-behavior por elemento, enquanto uma rajada de roda
// estiver em andamento. Uma variavel local por evento NAO serve: dois eventos
// no mesmo frame fariam o segundo capturar "auto" (ja aplicado pelo primeiro) e
// restaurar "auto" no fim, desligando o smooth da pagina permanentemente.
var smoothScrollSuspensions = new WeakMap();

// deltaMode: 0 = pixels, 1 = linhas, 2 = paginas.
function normalizeWheelDelta(event) {
  if (!event) return 0;
  if (event.deltaMode === 1) return event.deltaY * 16;
  if (event.deltaMode === 2) return event.deltaY * 100;
  return event.deltaY;
}

function requestFrame(callback) {
  if (typeof window !== 'undefined' && typeof window.requestAnimationFrame === 'function') {
    window.requestAnimationFrame(callback);
    return;
  }
  setTimeout(callback, 0);
}

function suspendSmoothScroll(node) {
  if (!smoothScrollSuspensions.has(node)) {
    smoothScrollSuspensions.set(node, node.style.scrollBehavior);
  }
  node.style.scrollBehavior = 'auto';
}

function restoreSmoothScroll(node) {
  if (!smoothScrollSuspensions.has(node)) return;
  node.style.scrollBehavior = smoothScrollSuspensions.get(node);
  smoothScrollSuspensions.delete(node);
}

function scrollNearestScrollableAncestor(el, deltaY) {
  var node = el ? el.parentElement : null;
  while (node) {
    var style = window.getComputedStyle(node);
    var overflowY = style.overflowY;
    if ((overflowY === 'auto' || overflowY === 'scroll') && node.scrollHeight > node.clientHeight) {
      var before = node.scrollTop;
      // Durante a rajada de roda o scroll-behavior: smooth reinicia a animacao
      // a cada evento e a rolagem parece travada; rola instantaneo e restaura.
      suspendSmoothScroll(node);
      node.scrollTop = before + deltaY;
      var moved = node.scrollTop !== before;
      var target = node;
      requestFrame(function () { restoreSmoothScroll(target); });
      if (moved) return true;
    }
    node = node.parentElement;
  }
  return false;
}

// A lista ainda consegue rolar na direcao do delta?
function canScrollInside(el, deltaY) {
  if (el.scrollHeight <= el.clientHeight) return false;
  var atTop = el.scrollTop <= 0;
  var atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 1;
  return deltaY < 0 ? !atTop : !atBottom;
}

// Repassa a roda para a pagina. Retorna true quando interceptou o evento.
function forwardWheelToPage(el, event) {
  // Eventos nao-cancelaveis (fling do trackpad) nao aceitam preventDefault;
  // nesse caso deixamos o encadeamento nativo cuidar da rolagem.
  if (!el || !event || event.cancelable === false) return false;
  var delta = normalizeWheelDelta(event);
  if (!delta) return false;
  if (canScrollInside(el, delta)) return false; // rola dentro da lista
  event.preventDefault();
  scrollNearestScrollableAncestor(el, delta);
  return true;
}

function bindScrollChaining(el) {
  if (!el || scrollChainingBound.has(el)) return;
  scrollChainingBound.add(el);
  el.addEventListener('wheel', function (event) {
    forwardWheelToPage(el, event);
  }, { passive: false });
}

// Historico do chamado e lista de chamados: mesma estrutura (scroller aninhado
// dentro do .page-content) e mesmo sintoma.
function bindSupportScrollChaining() {
  bindScrollChaining(commentsListEl);
  bindScrollChaining(supportTicketsListEl);
}
