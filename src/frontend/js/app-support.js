"use strict";

var currentTicketId = '';
var currentTicket = null;
var workflowStatesCache = null;
var workflowStatesCacheKey = '';
var WORKFLOW_STATES_CACHE_TTL_MS = 10 * 60 * 1000;

var priorityLabels = { 1: 'Baixa', 2: 'Media', 3: 'Alta', 4: 'Critica' };
var priorityClasses = { 1: 'p-baixa', 2: 'p-media', 3: 'p-alta', 4: 'p-critica' };

function ticketPriorityLabel(p) { return priorityLabels[p] || 'N/A'; }
function ticketPriorityClass(p) { return priorityClasses[p] || 'p-media'; }

// formatDateTime mantida para compatibilidade com chamadas existentes.
function formatDateTime(v) { return formatDate(v, ''); }

function renderStars(rating) {
  if (rating === null || rating === undefined || rating === '') return 'Sem avaliacao';
  var value = Number(rating);
  if (!Number.isFinite(value) || value < 0) return 'Sem avaliacao';
  var stars = '*****'.slice(0, Math.min(5, Math.floor(value)));
  var empty = '-----'.slice(0, 5 - Math.min(5, Math.floor(value)));
  return stars + empty + ' (' + value + '/5)';
}

function workflowMetaText(state) {
  if (!state) return '';
  var flags = [];
  if (state.isInitial) flags.push('Inicial');
  if (state.isFinal) flags.push('Final');
  if (state.displayOrder || state.displayOrder === 0) flags.push('Ordem ' + state.displayOrder);
  return flags.join(' - ');
}

function ticketHasFinalState(ticket) {
  if (!ticket || !ticket.workflowState) return false;
  var ws = ticket.workflowState;
  if (ws.isFinal) return true;
  var name = String(ws.name || '').toLowerCase();
  return name.indexOf('fechado') >= 0 || name.indexOf('closed') >= 0 || name.indexOf('resolvido') >= 0;
}

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

function populateWorkflowStateOptions(states, currentWorkflowStateId) {
  if (!closeTicketWorkflowStateSelectEl) return;

  var options = ['<option value="">Fechar com estado padrao</option>'];
  var finalStates = (states || []).filter(function (s) { return !!s && s.isFinal; });
  var available = finalStates.length ? finalStates : (states || []);

  available.forEach(function (s) {
    var label = s.name || (s.id ? ('Estado ' + s.id.substring(0, 8)) : 'Estado');
    if (s.isFinal) label += ' (Final)';
    if (s.displayOrder || s.displayOrder === 0) label += ' - Ordem ' + s.displayOrder;
    options.push('<option value="' + escapeHtmlAttr(s.id) + '">' + escapeHtml(label) + '</option>');
  });

  options.push('<option value="__manual__">Informar GUID manualmente</option>');
  closeTicketWorkflowStateSelectEl.innerHTML = options.join('');

  if (currentWorkflowStateId && available.some(function (s) { return s.id === currentWorkflowStateId; })) {
    closeTicketWorkflowStateSelectEl.value = currentWorkflowStateId;
  } else {
    closeTicketWorkflowStateSelectEl.value = '';
  }

  if (closeTicketWorkflowStateIdEl) {
    closeTicketWorkflowStateIdEl.classList.add('hidden');
    closeTicketWorkflowStateIdEl.value = '';
  }
}

async function loadWorkflowStatesForClose(ticket) {
  if (!closeTicketWorkflowStateSelectEl) return;

  var profileKey = 'default';
  try {
    var cfg = await appApi().GetDebugConfig();
    profileKey = buildWorkflowProfileKey(cfg);
  } catch (e) {
    profileKey = 'default';
  }

  if (workflowStatesCacheKey === profileKey && workflowStatesCache && workflowStatesCache.length) {
    populateWorkflowStateOptions(workflowStatesCache, ticket && ticket.workflowState ? ticket.workflowState.id : '');
    return;
  }

  var localStates = readWorkflowStatesLocal(profileKey);
  if (localStates && localStates.length) {
    workflowStatesCache = localStates;
    workflowStatesCacheKey = profileKey;
    populateWorkflowStateOptions(workflowStatesCache, ticket && ticket.workflowState ? ticket.workflowState.id : '');
    return;
  }

  try {
    var states = await appApi().GetTicketWorkflowStates();
    workflowStatesCache = Array.isArray(states) ? states : [];
    workflowStatesCacheKey = profileKey;
    writeWorkflowStatesLocal(profileKey, workflowStatesCache);
    populateWorkflowStateOptions(workflowStatesCache, ticket && ticket.workflowState ? ticket.workflowState.id : '');
  } catch (err) {
    closeTicketWorkflowStateSelectEl.innerHTML =
      '<option value="">Fechar com estado padrao</option>' +
      '<option value="__manual__">Informar GUID manualmente</option>';
    closeTicketWorkflowStateSelectEl.value = '';
  }
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

function openNewTicketModal() {
  if (supportCreateOverlayEl) {
    supportCreateOverlayEl.classList.remove('hidden');
    supportCreateOverlayEl.setAttribute('aria-hidden', 'false');
  }
  if (supportCreateFormEl) {
    supportCreateFormEl.classList.remove('hidden');
  }
  hideTicketFormStatus();
}

function closeNewTicketModal() {
  if (supportCreateOverlayEl) {
    supportCreateOverlayEl.classList.add('hidden');
    supportCreateOverlayEl.setAttribute('aria-hidden', 'true');
  }
  if (supportCreateFormEl) {
    supportCreateFormEl.classList.add('hidden');
  }
}

function initSupport() {
  if (!supportFormEl) return;

  supportFormEl.addEventListener('submit', async function (e) {
    e.preventDefault();
    var title = document.getElementById('ticketTitle') ? document.getElementById('ticketTitle').value.trim() : '';
    var category = document.getElementById('ticketCategory') ? document.getElementById('ticketCategory').value : '';
    var priority = parseInt(document.getElementById('ticketPriority') ? document.getElementById('ticketPriority').value : '2', 10);
    var description = document.getElementById('ticketDescription') ? document.getElementById('ticketDescription').value.trim() : '';

    if (!title || !description) {
      showToast(translate('support.fillTitleDescription'), 'error');
      return;
    }

    var btn = document.getElementById('submitTicketBtn');
    if (btn) { btn.disabled = true; btn.textContent = translate('support.sending'); }
    showTicketFormStatus(translate('support.submittingTicket'), false);

    try {
      var ticket = await appApi().CreateSupportTicket({ title: title, description: description, priority: priority, category: category });
      showToast(translate('support.ticketCreatedSuccess'), 'success');
      supportFormEl.reset();
      hideTicketFormStatus();
      closeNewTicketModal();
      loadSupportTickets();
    } catch (err) {
      showTicketFormStatus(translate('support.ticketCreateError', { error: String(err) }), true);
      showToast(translate('support.ticketCreateError', { error: String(err) }), 'error');
    } finally {
      if (btn) { btn.disabled = false; btn.textContent = translate('action.sendTicket'); }
    }
  });

  if (refreshTicketsBtnEl) {
    refreshTicketsBtnEl.addEventListener('click', function () { loadSupportTickets(); });
  }
  if (newTicketBtnEl) {
    newTicketBtnEl.addEventListener('click', function () { openNewTicketModal(); });
  }
  if (closeNewTicketBtnEl) {
    closeNewTicketBtnEl.addEventListener('click', function () { closeNewTicketModal(); });
  }
  if (supportCreateOverlayEl) {
    supportCreateOverlayEl.addEventListener('click', function () { closeNewTicketModal(); });
  }
  if (backToFormBtnEl) {
    backToFormBtnEl.addEventListener('click', function () { closeTicketDetail(); });
  }
  if (submitCommentBtnEl) {
    submitCommentBtnEl.addEventListener('click', async function () {
      if (!currentTicketId || !commentInputEl) return;
      var content = commentInputEl.value.trim();
      if (!content) { showToast(translate('support.enterComment'), 'error'); return; }
      submitCommentBtnEl.disabled = true;
      try {
        await appApi().AddTicketComment(currentTicketId, '', content);
        commentInputEl.value = '';
        showToast(translate('support.commentSent'), 'success');
        loadTicketComments(currentTicketId);
      } catch (err) {
        showToast(translate('support.commentSendError', { error: String(err) }), 'error');
      } finally {
        submitCommentBtnEl.disabled = false;
      }
    });
  }

  if (closeTicketBtnEl) {
    closeTicketBtnEl.addEventListener('click', async function () {
      if (!currentTicketId) return;

      var rating = null;
      if (closeTicketRatingEl && closeTicketRatingEl.value !== '') {
        rating = parseInt(closeTicketRatingEl.value, 10);
        if (Number.isNaN(rating) || rating < 0 || rating > 5) {
          showToast(translate('support.invalidRating'), 'error');
          return;
        }
      }

      var comment = closeTicketCommentEl ? closeTicketCommentEl.value.trim() : '';
      var workflowStateId = '';
      if (closeTicketWorkflowStateSelectEl && closeTicketWorkflowStateSelectEl.value === '__manual__') {
        workflowStateId = closeTicketWorkflowStateIdEl ? closeTicketWorkflowStateIdEl.value.trim() : '';
      } else if (closeTicketWorkflowStateSelectEl) {
        workflowStateId = closeTicketWorkflowStateSelectEl.value.trim();
      } else if (closeTicketWorkflowStateIdEl) {
        workflowStateId = closeTicketWorkflowStateIdEl.value.trim();
      }

      closeTicketBtnEl.disabled = true;
      closeTicketBtnEl.textContent = translate('support.closing');

      try {
        var payload = { comment: comment, workflowStateId: workflowStateId };
        if (rating !== null) payload.rating = rating;
        var ticket = await appApi().CloseSupportTicket(currentTicketId, payload);
        showToast(translate('support.ticketClosedSuccess'), 'success');
        currentTicket = ticket;
        renderTicketDetail(ticket);
        if (closeTicketCommentEl) closeTicketCommentEl.value = '';
        if (closeTicketRatingEl) closeTicketRatingEl.value = '';
        if (closeTicketWorkflowStateIdEl) closeTicketWorkflowStateIdEl.value = '';
        if (closeTicketWorkflowStateSelectEl) closeTicketWorkflowStateSelectEl.value = '';
        await loadSupportTickets();
      } catch (err) {
        showToast(translate('support.ticketCloseError', { error: String(err) }), 'error');
      } finally {
        closeTicketBtnEl.disabled = false;
        closeTicketBtnEl.textContent = translate('action.closeTicket');
      }
    });
  }

  if (closeTicketWorkflowStateSelectEl && closeTicketWorkflowStateIdEl) {
    closeTicketWorkflowStateSelectEl.addEventListener('change', function () {
      var manual = closeTicketWorkflowStateSelectEl.value === '__manual__';
      closeTicketWorkflowStateIdEl.classList.toggle('hidden', !manual);
      if (!manual) closeTicketWorkflowStateIdEl.value = '';
    });
  }
}

async function loadSupportTickets() {
  if (!supportTicketsListEl) return;

  closeNewTicketModal();

  // show loading
  if (ticketsLoadingEl) ticketsLoadingEl.classList.remove('hidden');
  supportTicketsListEl.innerHTML = '';

  // Resolve agent context for the banner
  try {
    var agent = await appApi().GetAgentInfo();
    if (agentContextBannerEl && agentContextTextEl) {
      var computerName = (agent && agent.hostname) ? String(agent.hostname).trim() : '';
      if (!computerName) {
        computerName = translate('status.localComputer');
      }
      agentContextTextEl.textContent = translate('support.agentLabel', { name: computerName });
      agentContextBannerEl.classList.remove('hidden');
    }
    if (agentContextErrorEl) agentContextErrorEl.classList.add('hidden');
  } catch (err) {
    if (agentContextErrorEl && agentContextErrorTextEl) {
      agentContextErrorTextEl.textContent = String(err);
      agentContextErrorEl.classList.remove('hidden');
    }
    if (agentContextBannerEl) agentContextBannerEl.classList.add('hidden');
    if (ticketsLoadingEl) ticketsLoadingEl.classList.add('hidden');
    supportTicketsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('support.serverNotConfigured')) + '</div>';
    if (supportSidePanelEl) supportSidePanelEl.classList.add('hidden');
    if (supportTicketDetailEl) supportTicketDetailEl.classList.add('hidden');
    return;
  }

  try {
    var tickets = await appApi().GetSupportTickets();
    if (ticketsLoadingEl) ticketsLoadingEl.classList.add('hidden');
    if (!tickets || !tickets.length) {
      supportTicketsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('support.noTicketsPrompt')) + '</div>';
      if (supportSidePanelEl) supportSidePanelEl.classList.add('hidden');
      if (supportTicketDetailEl) supportTicketDetailEl.classList.add('hidden');
      return;
    }
    if (supportSidePanelEl) supportSidePanelEl.classList.add('hidden');
    if (supportTicketDetailEl) supportTicketDetailEl.classList.add('hidden');
    supportTicketsListEl.innerHTML = tickets.map(function (t) {
      var statusName = (t.workflowState && t.workflowState.name) ? t.workflowState.name : translate('support.defaultOpenStatus');
      var statusColor = (t.workflowState && t.workflowState.color) ? t.workflowState.color : '#0b6e4f';
      var statusMeta = workflowMetaText(t.workflowState);
      var priLabel = ticketPriorityLabel(t.priority);
      var priClass = ticketPriorityClass(t.priority);
      var cat = t.category || '';
      var date = formatDateTime(t.createdAt);
      var ratingText = renderStars(t.rating);
      return '<button class="support-ticket-card" data-id="' + escapeHtml(t.id) + '" data-ticket=\'' + escapeAttr(t) + '\'>' +
        '<div class="ticket-header">' +
          '<span class="ticket-id-badge">#' + escapeHtml(t.id.substring(0, 8)) + '</span>' +
          '<span class="ticket-status-badge" style="background:' + escapeHtml(statusColor) + '20;color:' + escapeHtml(statusColor) + '">' + escapeHtml(statusName) + '</span>' +
          '<span class="ticket-priority-badge ' + priClass + '">' + escapeHtml(priLabel) + '</span>' +
        '</div>' +
        '<div class="ticket-subject">' + escapeHtml(t.title) + '</div>' +
        '<div class="ticket-meta">' +
          (cat ? '<span>' + escapeHtml(cat) + '</span>' : '') +
          (date ? '<span>' + escapeHtml(date) + '</span>' : '') +
          (statusMeta ? '<span>' + escapeHtml(statusMeta) + '</span>' : '') +
          '<span>' + escapeHtml(ratingText) + '</span>' +
        '</div>' +
      '</button>';
    }).join('');

    // attach click handlers
    supportTicketsListEl.querySelectorAll('.support-ticket-card').forEach(function (card) {
      card.addEventListener('click', function () {
        try {
          var t = JSON.parse(card.getAttribute('data-ticket').replace(/&apos;/g, "'"));
          showTicketDetail(t);
        } catch (e) { /* ignore */ }
      });
    });
  } catch (err) {
    if (ticketsLoadingEl) ticketsLoadingEl.classList.add('hidden');
    supportTicketsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('support.ticketListLoadError', { error: String(err) })) + '</div>';
  }
}

function escapeAttr(obj) {
  return JSON.stringify(obj).replace(/'/g, '&apos;').replace(/"/g, '&quot;');
}

function showTicketDetail(t) {
  currentTicketId = t.id;
  currentTicket = t;
  if (supportSidePanelEl) supportSidePanelEl.classList.remove('hidden');
  if (supportTicketDetailEl) supportTicketDetailEl.classList.remove('hidden');

  renderTicketDetail(t);
  loadWorkflowStatesForClose(t);
  loadTicketComments(t.id);

  appApi().GetSupportTicketDetails(t.id)
    .then(function (fresh) {
      if (!fresh || currentTicketId !== t.id) return;
      currentTicket = fresh;
      renderTicketDetail(fresh);
      loadWorkflowStatesForClose(fresh);
      loadTicketComments(t.id);
    })
    .catch(function () { /* mantem dados ja exibidos */ });
}

function renderTicketDetail(t) {
  var statusName = (t.workflowState && t.workflowState.name) ? t.workflowState.name : translate('support.defaultOpenStatus');
  var statusColor = (t.workflowState && t.workflowState.color) ? t.workflowState.color : '#0b6e4f';
  var statusMeta = workflowMetaText(t.workflowState);
  var priLabel = ticketPriorityLabel(t.priority);
  var priClass = ticketPriorityClass(t.priority);
  var date = formatDateTime(t.createdAt);
  var cat = t.category || '';
  var ratedAt = formatDateTime(t.ratedAt);
  var ratedBy = t.ratedBy || '';
  var isFinal = ticketHasFinalState(t);

  if (ticketDetailIdEl) ticketDetailIdEl.textContent = '#' + t.id.substring(0, 8);
  if (ticketDetailStatusEl) {
    ticketDetailStatusEl.textContent = statusName;
    ticketDetailStatusEl.style.background = statusColor + '20';
    ticketDetailStatusEl.style.color = statusColor;
  }
  if (ticketDetailPriorityEl) {
    ticketDetailPriorityEl.textContent = priLabel;
    ticketDetailPriorityEl.className = 'ticket-priority-badge ' + priClass;
  }
  if (ticketDetailTitleEl) ticketDetailTitleEl.textContent = t.title || '';
  if (ticketDetailMetaEl) {
    ticketDetailMetaEl.innerHTML =
      (cat ? '<span>' + escapeHtml(cat) + '</span>' : '') +
      (date ? '<span>' + escapeHtml(translate('support.ticketOpenedAt', { date: date })) + '</span>' : '') +
      (statusMeta ? '<span>' + escapeHtml(statusMeta) + '</span>' : '') +
      '<span>' + escapeHtml(translate('support.ratingDisplay', { rating: renderStars(t.rating) })) + '</span>' +
      (ratedAt ? '<span>' + escapeHtml(translate('support.ratedAt', { date: ratedAt })) + '</span>' : '') +
      (ratedBy ? '<span>' + escapeHtml(translate('support.ratedBy', { name: ratedBy })) + '</span>' : '');
  }
  if (ticketDetailDescEl) ticketDetailDescEl.textContent = t.description || '';

  if (ticketClosePanelEl) {
    ticketClosePanelEl.classList.toggle('hidden', isFinal);
  }
}

function closeTicketDetail() {
  currentTicketId = '';
  currentTicket = null;
  if (supportTicketDetailEl) supportTicketDetailEl.classList.add('hidden');
  if (supportSidePanelEl) supportSidePanelEl.classList.add('hidden');
  if (closeTicketWorkflowStateSelectEl) closeTicketWorkflowStateSelectEl.value = '';
  if (closeTicketWorkflowStateIdEl) {
    closeTicketWorkflowStateIdEl.value = '';
    closeTicketWorkflowStateIdEl.classList.add('hidden');
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
  } catch (err) {
    commentsListEl.innerHTML = '<div class="meta">' + escapeHtml(translate('support.commentLoadError', { error: String(err) })) + '</div>';
  }
}
