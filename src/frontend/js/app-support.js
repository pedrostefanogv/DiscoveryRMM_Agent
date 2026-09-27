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

// Snapshot volatil da sessao: substituido a cada carga e NUNCA persistido.
var supportTicketsAll = [];
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
  // Reabrir o formulario comeca limpo (modelo/campos do departamento).
  if (ticketTemplateSelectEl) ticketTemplateSelectEl.value = '';
  clearTicketTemplateExtra();
  clearDepartmentFields();
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
  }).catch(function (err) {
    console.warn('[support] falha ao carregar departamentos:', err);
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
  if (dataType === 'Integer') return '<input id="' + idAttr + '" type="number" step="1"' + bounds + (required ? ' required' : '') + ' />';
  if (dataType === 'Decimal') return '<input id="' + idAttr + '" type="number" step="0.01"' + bounds + (required ? ' required' : '') + ' />';
  if (dataType === 'Date') return '<input id="' + idAttr + '" type="date"' + (required ? ' required' : '') + ' />';
  if (dataType === 'DateTime') return '<input id="' + idAttr + '" type="datetime-local"' + (required ? ' required' : '') + ' />';
  return '<input id="' + idAttr + '" type="text"' + lengths + (required ? ' required' : '') + ' />';
}

function renderTemplateGroup(titleKey, prefix, items) {
  var list = Array.isArray(items) ? items : [];
  if (!list.length) return '';
  var html = '<div class="template-group"><div class="template-group-title">' + escapeHtml(translate(titleKey)) + '</div>';
  list.forEach(function (item) {
    if (!item) return;
    var key = prefix === 'field' ? item.definitionId : item.key;
    if (!key) return;
    var idAttr = templateInputId(prefix, key);
    var label = item.label || item.name || key;
    html += '<div class="form-field">' +
      '<label for="' + idAttr + '">' + escapeHtml(label) + (item.isRequired ? ' *' : '') + '</label>' +
      buildTemplateFieldControl(item, idAttr) +
      ((item.helpText || item.description)
        ? '<div class="meta">' + escapeHtml(item.helpText || item.description) + '</div>'
        : '') +
      '</div>';
  });
  return html + '</div>';
}

function renderTicketTemplateExtra(template) {
  if (!ticketTemplateExtraEl) return;
  var html = template ? renderTemplateGroup('support.templateQuestions', 'q', template.questions) : '';
  if (!html) {
    clearTicketTemplateExtra();
    return;
  }
  ticketTemplateExtraEl.innerHTML = html;
  ticketTemplateExtraEl.classList.remove('hidden');
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
  el.value = String(value);
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
  ticketDepartmentFieldsEl.innerHTML = renderTemplateGroup('support.templateFields', 'field', currentDepartmentFields);
  ticketDepartmentFieldsEl.classList.remove('hidden');

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

function loadDepartmentFields(departmentId) {
  var api = appApi();
  if (!departmentId || !api || typeof api.GetTicketDepartmentFields !== 'function') {
    clearDepartmentFields();
    return;
  }
  var requested = departmentId;
  if (Object.prototype.hasOwnProperty.call(departmentFieldsCache, requested)) {
    renderDepartmentFields(departmentFieldsCache[requested], selectedTemplateDefaultsJson());
    return;
  }
  api.GetTicketDepartmentFields(departmentId).then(function (fields) {
    departmentFieldsCache[requested] = Array.isArray(fields) ? fields : [];
    // Ignora resposta tardia de um departamento que nao esta mais selecionado.
    if (selectedDepartmentId() !== requested) return;
    renderDepartmentFields(departmentFieldsCache[requested], selectedTemplateDefaultsJson());
  }).catch(function (err) {
    console.warn('[support] falha ao carregar campos do departamento:', err);
    clearDepartmentFields();
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

function applyTicketTemplate(templateId) {
  var template = findTicketTemplate(templateId);
  if (!template) {
    clearTicketTemplateExtra();
    // Sem modelo, os campos do departamento continuam valendo.
    loadDepartmentFields(selectedDepartmentId());
    return;
  }
  // Prefill a partir do modelo; o usuario ainda pode editar antes de enviar.
  var titleEl = document.getElementById('ticketTitle');
  if (titleEl && template.title) titleEl.value = template.title;
  var descriptionEl = document.getElementById('ticketDescription');
  if (descriptionEl && template.description) descriptionEl.value = template.description;
  setSelectValueIfPresent(document.getElementById('ticketCategory'), template.category);
  var priority = templatePriorityToInt(template.priority);
  if (priority) setSelectValueIfPresent(document.getElementById('ticketPriority'), String(priority));
  // O departamento NAO e alterado pelo modelo: ele foi escolhido antes e o
  // modelo ja foi filtrado por ele.
  renderTicketTemplateExtra(template);
  // Os campos sao do departamento (nao do template): recarrega para o
  // departamento efetivo, aplicando os defaults do modelo.
  loadDepartmentFields(selectedDepartmentId());
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
    var intValue = parseInt(raw, 10);
    return Number.isFinite(intValue) ? intValue : undefined;
  }
  if (dataType === 'Decimal') {
    var decValue = parseFloat(raw);
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
  var states = await ensureWorkflowStates(false);
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

  closeTicketStarsWidget = attachStarsWidget(closeTicketStarsEl, clearRatingBtnEl);
  ratingStarsWidget = attachStarsWidget(ratingStarsEl, clearRatingPanelBtnEl);

  supportFormEl.addEventListener('submit', async function (e) {
    e.preventDefault();
    var title = document.getElementById('ticketTitle') ? document.getElementById('ticketTitle').value.trim() : '';
    var category = document.getElementById('ticketCategory') ? document.getElementById('ticketCategory').value : '';
    var priority = parseInt(document.getElementById('ticketPriority') ? document.getElementById('ticketPriority').value : '2', 10);
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

    // Obrigatorios: perguntas do modelo + campos personalizados do departamento.
    var templateExtra = collectTicketTemplateExtra(template);
    var departmentExtra = collectDepartmentFieldValues();
    var missingFields = templateExtra.missing.concat(departmentExtra.missing);
    if (missingFields.length) {
      showToast(translate('support.fieldRequired', { fields: missingFields.join(', ') }), 'error');
      return;
    }

    var btn = document.getElementById('submitTicketBtn');
    if (btn) { btn.disabled = true; btn.textContent = translate('support.sending'); }
    showTicketFormStatus(translate('support.submittingTicket'), false);

    try {
      var payload = { title: title, description: description, priority: priority, category: category, departmentId: departmentId };
      if (Object.keys(departmentExtra.values).length) payload.customFieldValues = departmentExtra.values;
      if (templateId) {
        payload.templateId = templateId;
        if (Object.keys(templateExtra.templateAnswers).length) payload.templateAnswers = templateExtra.templateAnswers;
      }
      await appApi().CreateSupportTicket(payload);
      showToast(translate('support.ticketCreatedSuccess'), 'success');
      supportFormEl.reset();
      if (ticketTemplateSelectEl) ticketTemplateSelectEl.value = '';
      clearTicketTemplateExtra();
      clearDepartmentFields();
      hideTicketFormStatus();
      showSupportList();
      loadSupportTickets();
    } catch (err) {
      showTicketFormStatus(translate('support.ticketCreateError', { error: String(err) }), true);
      showToast(translate('support.ticketCreateError', { error: String(err) }), 'error');
    } finally {
      if (btn) { btn.disabled = false; btn.textContent = translate('action.sendTicket'); }
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
      if (ticketTemplateSelectEl) ticketTemplateSelectEl.value = '';
      clearTicketTemplateExtra();
      renderTicketTemplateOptions();
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

  try {
    var tickets = await appApi().GetSupportTickets();
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

async function loadTicketFields(ticketId) {
  if (!ticketCustomFieldsEl) return;
  ticketCustomFieldsEl.classList.add('hidden');
  ticketCustomFieldsEl.innerHTML = '';
  if (!ticketId) return;
  try {
    var fields = await appApi().GetTicketFields(ticketId);
    if (!fields || !fields.length || currentTicketId !== ticketId) return;
    var rows = fields.map(function (f) {
      var label = f.label || f.name || '';
      var value = formatFieldValue(f.valueJson);
      return '<div class="ticket-field-row">' +
        '<span class="ticket-field-label">' + escapeHtml(label) + '</span>' +
        '<span class="ticket-field-value">' + escapeHtml(value || '—') + '</span>' +
      '</div>';
    }).join('');
    ticketCustomFieldsEl.innerHTML =
      '<h4>' + escapeHtml(translate('support.ticketFields')) + '</h4>' + rows;
    ticketCustomFieldsEl.classList.remove('hidden');
  } catch (err) {
    console.warn('[support] falha ao carregar campos do chamado:', err);
  }
}

async function loadTicketComments(ticketId) {
  if (!commentsListEl) return;
  commentsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('support.loadingComments')) + '</div>';
  try {
    var comments = await appApi().GetTicketComments(ticketId);
    if (!comments || !comments.length) {
      commentsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('support.noComments')) + '</div>';
      return;
    }
    commentsListEl.innerHTML = comments.map(function (c) {
      var date = c.createdAt ? formatDate(c.createdAt, '') : '';
      return '<div class="comment-card' + (c.isInternal ? ' comment-internal' : '') + '">' +
        '<div class="comment-header">' +
          '<span class="comment-author">' + escapeHtml(c.author || translate('support.user')) + '</span>' +
          (date ? '<span class="comment-date">' + escapeHtml(date) + '</span>' : '') +
          (c.isInternal ? '<span class="comment-internal-badge">' + escapeHtml(translate('support.internal')) + '</span>' : '') +
        '</div>' +
        '<div class="comment-content">' + escapeHtml(c.content) + '</div>' +
      '</div>';
    }).join('');
    // Rola para o comentario mais recente
    commentsListEl.scrollTop = commentsListEl.scrollHeight;
  } catch (err) {
    commentsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('support.commentLoadError', { error: String(err) })) + '</div>';
  }
}
