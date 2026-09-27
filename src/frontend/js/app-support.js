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
// Filtros persistidos por perfil (scheme|server|agentId) no localStorage, para
// sobreviverem ao fechamento do app.
var ticketFiltersRestored = false;
var ticketFiltersProfileKey = '';
var TICKET_FILTERS_STORAGE_VERSION = 'v1';

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

function ticketMatchesQuery(ticket, query) {
  if (!query) return true;
  var state = resolveTicketState(ticket);
  var haystack = [
    ticket.title,
    ticket.category,
    ticket.id,
    ticket.id ? String(ticket.id).substring(0, 8) : '',
    state ? state.name : ''
  ].join(' ');
  return normalizeSearchText(haystack).indexOf(query) >= 0;
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
  var query = normalizeSearchText(ticketFilters.query);
  return (list || []).filter(function (t) {
    if (!ticketMatchesStatusFilter(t, ticketFilters.status)) return false;
    if (ticketFilters.priority !== 'all' && String(t.priority) !== String(ticketFilters.priority)) return false;
    return ticketMatchesQuery(t, query);
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
    // Filtro salvo apontando para um estado que não existe mais: corrige e
    // regrava para não repetir a validação no próximo carregamento.
    if (previousStatus !== current) persistTicketFilters();
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

function ticketFiltersStorageKey(profileKey) {
  return 'discovery.support.ticket-filters.' + TICKET_FILTERS_STORAGE_VERSION + '.' + (profileKey || 'default');
}

// Sanitiza o que vem do storage: status valido e validado depois contra a lista
// de estados; prioridade precisa ser uma das opcoes conhecidas.
function sanitizeTicketFilters(raw) {
  var out = { query: '', status: '__default', priority: 'all' };
  if (!raw || typeof raw !== 'object') return out;
  if (typeof raw.query === 'string') out.query = raw.query.slice(0, 200);
  if (typeof raw.status === 'string' && raw.status) out.status = raw.status;
  if (['all', '1', '2', '3', '4'].indexOf(String(raw.priority)) >= 0) out.priority = String(raw.priority);
  return out;
}

function readTicketFiltersLocal(profileKey) {
  try {
    if (!window.localStorage) return null;
    var raw = window.localStorage.getItem(ticketFiltersStorageKey(profileKey));
    if (!raw) return null;
    return sanitizeTicketFilters(JSON.parse(raw));
  } catch (e) {
    return null;
  }
}

var persistTicketFiltersDebounced = debounce(function () {
  try {
    if (!window.localStorage) return;
    window.localStorage.setItem(
      ticketFiltersStorageKey(ticketFiltersProfileKey || 'default'),
      JSON.stringify({
        query: String(ticketFilters.query || '').slice(0, 200),
        status: String(ticketFilters.status || '__default'),
        priority: String(ticketFilters.priority || 'all'),
      })
    );
  } catch (e) {
    // storage indisponivel/quota: filtros seguem apenas em memoria
  }
}, 400);

function persistTicketFilters() {
  persistTicketFiltersDebounced();
}

// Restaura os filtros uma vez por perfil. Ao trocar de servidor/agente, os
// filtros do perfil anterior sao substituidos pelos do novo.
function restoreTicketFilters() {
  var profileKey = workflowStatesCacheKey || 'default';
  if (ticketFiltersRestored && ticketFiltersProfileKey === profileKey) return;
  ticketFiltersRestored = true;
  ticketFiltersProfileKey = profileKey;
  var next = readTicketFiltersLocal(profileKey) || { query: '', status: '__default', priority: 'all' };
  ticketFilters.query = next.query;
  ticketFilters.status = next.status;
  ticketFilters.priority = next.priority;
  if (ticketSearchInputEl) ticketSearchInputEl.value = ticketFilters.query;
}

function clearTicketFilters() {
  ticketFilters.query = '';
  ticketFilters.status = '__default';
  ticketFilters.priority = 'all';
  if (ticketSearchInputEl) ticketSearchInputEl.value = '';
  if (ticketStatusFilterEl) ticketStatusFilterEl.value = '__default';
  if (ticketPriorityFilterEl) ticketPriorityFilterEl.value = 'all';
  persistTicketFilters();
  renderSupportTicketList(supportTicketsAll);
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
  hideTicketFormStatus();
  loadTicketOptions();
}

function escapeOptionLabel(value) {
  var el = document.createElement('div');
  el.textContent = value == null ? '' : String(value);
  return el.innerHTML;
}

function renderTicketProfileOptions(departmentId) {
  var select = document.getElementById('ticketWorkflowProfile');
  if (!select) return;
  var profiles = (ticketOptionsCache.workflowProfiles || []).filter(function (p) {
    return !departmentId || p.departmentId === departmentId;
  });
  var html = '<option value="">Padrao do departamento</option>';
  profiles.forEach(function (p) {
    html += '<option value="' + p.id + '">' + escapeOptionLabel(p.name) + '</option>';
  });
  select.innerHTML = html;
}

// Carrega departamentos/perfis do cliente do agente para o formulario. Sem
// departamento o servidor nao calcula SLA; sem perfil, usa o padrao do setor.
function loadTicketOptions() {
  var api = appApi();
  if (!api || typeof api.GetTicketOptions !== 'function') return;
  api.GetTicketOptions().then(function (options) {
    ticketOptionsCache = options || { departments: [], workflowProfiles: [] };
    var deptSelect = document.getElementById('ticketDepartment');
    if (deptSelect) {
      var html = '<option value="">Selecione...</option>';
      (ticketOptionsCache.departments || []).forEach(function (d) {
        html += '<option value="' + d.id + '">' + escapeOptionLabel(d.name) + '</option>';
      });
      deptSelect.innerHTML = html;
    }
    renderTicketProfileOptions('');
  }).catch(function (err) {
    console.warn('[support] falha ao carregar departamentos/perfis:', err);
  });
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
    var workflowProfileId = document.getElementById('ticketWorkflowProfile') ? document.getElementById('ticketWorkflowProfile').value : '';
    var description = document.getElementById('ticketDescription') ? document.getElementById('ticketDescription').value.trim() : '';

    if (!title || !description) {
      showToast(translate('support.fillTitleDescription'), 'error');
      return;
    }
    // Departamento e obrigatorio: define responsavel (auto-atribuicao) e SLA.
    if (!departmentId) {
      showToast(translate('support.selectDepartment'), 'error');
      return;
    }

    var btn = document.getElementById('submitTicketBtn');
    if (btn) { btn.disabled = true; btn.textContent = translate('support.sending'); }
    showTicketFormStatus(translate('support.submittingTicket'), false);

    try {
      var payload = { title: title, description: description, priority: priority, category: category, departmentId: departmentId };
      if (workflowProfileId) payload.workflowProfileId = workflowProfileId;
      await appApi().CreateSupportTicket(payload);
      showToast(translate('support.ticketCreatedSuccess'), 'success');
      supportFormEl.reset();
      renderTicketProfileOptions('');
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

  var ticketDepartmentSelect = document.getElementById('ticketDepartment');
  if (ticketDepartmentSelect) {
    ticketDepartmentSelect.addEventListener('change', function () {
      renderTicketProfileOptions(ticketDepartmentSelect.value);
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
      persistTicketFilters();
      renderSupportTicketList(supportTicketsAll);
    }, 200));
  }
  if (ticketStatusFilterEl) {
    ticketStatusFilterEl.addEventListener('change', function () {
      ticketFilters.status = ticketStatusFilterEl.value || '__default';
      persistTicketFilters();
      renderSupportTicketList(supportTicketsAll);
    });
  }
  if (ticketPriorityFilterEl) {
    ticketPriorityFilterEl.addEventListener('change', function () {
      ticketFilters.priority = ticketPriorityFilterEl.value || 'all';
      persistTicketFilters();
      renderSupportTicketList(supportTicketsAll);
    });
  }
  if (clearTicketFiltersBtnEl) {
    clearTicketFiltersBtnEl.addEventListener('click', clearTicketFilters);
  }

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
    restoreTicketFilters();
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
      '<div class="ticket-subject">' + escapeHtml(t.title || translate('support.untitledTicket')) + '</div>' +
      '<div class="ticket-header">' +
        '<span class="ticket-id-badge">#' + escapeHtml(String(t.id).substring(0, 8)) + '</span>' +
        '<span class="ticket-status-badge"' + safeStatusBadgeStyle(status.color) + '>' + escapeHtml(status.name) + '</span>' +
        '<span class="ticket-priority-badge ' + priClass + '">' + escapeHtml(priLabel) + '</span>' +
        awaitingBadge +
      '</div>' +
      '<div class="ticket-meta">' +
        (cat ? '<span>' + escapeHtml(cat) + '</span>' : '') +
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
      ticketDetailStatusEl.style.background = _statusColor + '20';
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
