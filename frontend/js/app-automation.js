"use strict";

function setAutomationStatus(message, type) {
  if (!automationStatusEl) return;
  automationStatusEl.textContent = message || '';
  automationStatusEl.className = 'debug-status' + (type ? ' ' + type : '');
}

function formatAutomationDate(value) {
  if (!value) return 'N/A';
  if (typeof formatDateTime === 'function') return formatDateTime(value);
  return String(value).replace('T', ' ').substring(0, 16);
}

function renderAutomationSummary(state) {
  if (!automationSummaryEl) return;

  var connectionText = !state.available ? 'Aguardando configuracao' : (state.connected ? 'Sincronizado' : 'Sem comunicacao');
  var upToDateText = state.upToDate ? 'Sem alteracoes' : 'Policy nova ou local';
  var cacheText = state.loadedFromCache ? 'Sim' : 'Nao';
  var facts = [
    { label: 'Conexao', value: connectionText },
    { label: 'PolicyFingerprint', value: state.policyFingerprint || 'N/A' },
    { label: 'Tasks', value: String(state.taskCount || 0) },
    { label: 'Callbacks pendentes', value: String(state.pendingCallbacks || 0) },
    { label: 'UpToDate', value: upToDateText },
    { label: 'Ultimo sync', value: formatAutomationDate(state.lastSyncAt) },
    { label: 'Ultima tentativa', value: formatAutomationDate(state.lastAttemptAt) },
    { label: 'Cache local', value: cacheText },
    { label: 'Correlation ID', value: state.correlationId || 'N/A' },
  ];

  automationSummaryEl.innerHTML = facts.map(function (fact) {
    return '<div class="fact"><span class="fact-label">' + escapeHtml(fact.label) + '</span><span class="fact-value">' + escapeHtml(fact.value) + '</span></div>';
  }).join('');
}

function renderAutomationNotes(state) {
  if (!automationNotesEl) return;

  var notes = [];
  if (state.generatedAt) notes.push('Policy gerada em ' + formatAutomationDate(state.generatedAt));
  if (state.includeScriptContent) notes.push('Sync manual com conteudo de script habilitado');
  if (state.lastError) notes.push('Ultimo erro: ' + state.lastError);
  if (!notes.length) notes.push('Sem alertas no momento.');
  automationNotesEl.innerHTML = notes.map(function (line) {
    return '<div>' + escapeHtml(line) + '</div>';
  }).join('');
}

function triggerChips(task) {
  var items = [];
  if (task.triggerImmediate) items.push('<span class="automation-chip">Immediate</span>');
  if (task.triggerRecurring) items.push('<span class="automation-chip">Recurring</span>');
  if (task.triggerOnUserLogin) items.push('<span class="automation-chip">UserLogin</span>');
  if (task.triggerOnAgentCheckIn) items.push('<span class="automation-chip">AgentCheckIn</span>');
  if (task.requiresApproval) items.push('<span class="automation-chip warn">RequiresApproval</span>');
  return items.join(' ');
}

function renderAutomationTasks(tasks) {
  if (!automationTasksEl) return;

  if (!tasks || !tasks.length) {
    automationTasksEl.innerHTML = '<div class="meta">Nenhuma tarefa resolvida para o agent.</div>';
    return;
  }

  automationTasksEl.innerHTML = tasks.map(function (task) {
    var meta = [];
    meta.push(task.actionLabel || task.actionType || 'Acao');
    meta.push(task.scopeLabel || task.scopeType || 'Escopo');
    if (task.installationLabel) meta.push(task.installationLabel);
    if (task.scriptTypeLabel) meta.push(task.scriptTypeLabel);

    var details = [];
    if (task.packageId) details.push('<div><strong>PackageId:</strong> ' + escapeHtml(task.packageId) + '</div>');
    if (task.scriptName) details.push('<div><strong>Script:</strong> ' + escapeHtml(task.scriptName) + (task.scriptVersion ? ' v' + escapeHtml(task.scriptVersion) : '') + '</div>');
    if (task.scheduleCron) details.push('<div><strong>Cron:</strong> ' + escapeHtml(task.scheduleCron) + '</div>');
    if (task.commandPayload) details.push('<div><strong>Payload:</strong> ' + escapeHtml(task.commandPayload) + '</div>');
    if (task.includeTags && task.includeTags.length) details.push('<div><strong>IncludeTags:</strong> ' + escapeHtml(task.includeTags.join(', ')) + '</div>');
    if (task.excludeTags && task.excludeTags.length) details.push('<div><strong>ExcludeTags:</strong> ' + escapeHtml(task.excludeTags.join(', ')) + '</div>');
    if (task.lastUpdatedAt) details.push('<div><strong>Atualizado em:</strong> ' + escapeHtml(formatAutomationDate(task.lastUpdatedAt)) + '</div>');

    return '' +
      '<article class="automation-task-card">' +
      '  <div class="automation-task-top">' +
      '    <div>' +
      '      <h4>' + escapeHtml(task.name || 'Tarefa sem nome') + '</h4>' +
      '      <div class="automation-task-meta">' + escapeHtml(meta.join(' • ')) + '</div>' +
      '    </div>' +
      '    <span class="automation-task-id">' + escapeHtml(task.taskId || '') + '</span>' +
      '  </div>' +
      (task.description ? '<p class="automation-task-desc">' + escapeHtml(task.description) + '</p>' : '') +
      '  <div class="automation-chip-row">' + triggerChips(task) + '</div>' +
      '  <div class="automation-task-details">' + details.join('') + '</div>' +
      '</article>';
  }).join('');
}

function automationExecutionBadgeClass(execution) {
  var status = String(execution && execution.status || '').toLowerCase();
  if (status === 'failed') return 'error';
  if (status === 'completed') return 'success';
  if (status === 'acknowledged') return 'warn';
  return '';
}

function automationExecutionOutputId(execution, index) {
  var raw = execution && execution.executionId ? String(execution.executionId) : String(index || 0);
  return 'automation-output-' + raw.replace(/[^a-zA-Z0-9_-]/g, '-');
}

function toggleAutomationExecutionOutput(button) {
  if (!button) return;
  var outputId = button.getAttribute('data-output-id');
  if (!outputId) return;

  var panel = document.getElementById(outputId);
  if (!panel) return;

  var willExpand = panel.classList.contains('hidden');
  panel.classList.toggle('hidden', !willExpand);
  button.setAttribute('aria-expanded', willExpand ? 'true' : 'false');

  var label = button.querySelector('.automation-log-toggle-label');
  if (label) {
    label.textContent = willExpand ? 'Ocultar logs' : 'Ver logs';
  }

  var icon = button.querySelector('.automation-log-toggle-icon');
  if (icon) {
    icon.textContent = willExpand ? '▾' : '▸';
  }
}

function renderAutomationExecutions(executions, pendingCallbacks) {
  if (!automationExecutionsEl) return;

  if (!executions || !executions.length) {
    automationExecutionsEl.innerHTML = '<div class="meta">Nenhuma execucao registrada.</div>';
    return;
  }

  automationExecutionsEl.innerHTML = executions.map(function (execution, index) {
    var details = [];
    if (execution.summaryLine) details.push('<div><strong>Resumo:</strong> ' + escapeHtml(execution.summaryLine) + '</div>');
    if (execution.packageId) details.push('<div><strong>PackageId:</strong> ' + escapeHtml(execution.packageId) + '</div>');
    if (execution.scriptId) details.push('<div><strong>ScriptId:</strong> ' + escapeHtml(execution.scriptId) + '</div>');
    if (execution.errorMessage) details.push('<div><strong>Erro:</strong> ' + escapeHtml(execution.errorMessage) + '</div>');
    if (execution.exitCodeSet) details.push('<div><strong>ExitCode:</strong> ' + escapeHtml(String(execution.exitCode)) + '</div>');
    if (execution.correlationId) details.push('<div><strong>Correlation ID:</strong> ' + escapeHtml(execution.correlationId) + '</div>');

    var outputSection = '';
    if (execution.output) {
      var outputId = automationExecutionOutputId(execution, index);
      outputSection = '' +
        '<div class="automation-output-wrap">' +
        '  <button type="button" class="btn subtle automation-log-toggle" data-output-id="' + escapeHtmlAttr(outputId) + '" aria-expanded="false">' +
        '    <span class="automation-log-toggle-icon" aria-hidden="true">▸</span>' +
        '    <span class="automation-log-toggle-label">Ver logs</span>' +
        '  </button>' +
        '  <div id="' + escapeHtmlAttr(outputId) + '" class="automation-output-panel hidden">' +
        '    <pre class="automation-execution-output">' + escapeHtml(execution.output) + '</pre>' +
        '  </div>' +
        '</div>';
    }

    var chips = [];
    if (execution.triggerLabel) chips.push('<span class="automation-chip">' + escapeHtml(execution.triggerLabel) + '</span>');
    if (execution.sourceLabel) chips.push('<span class="automation-chip">' + escapeHtml(execution.sourceLabel) + '</span>');
    if (execution.installationLabel) chips.push('<span class="automation-chip">' + escapeHtml(execution.installationLabel) + '</span>');
    if (execution.hasPendingCallback || pendingCallbacks > 0 && execution.commandId) chips.push('<span class="automation-chip warn">Callback pendente</span>');

    return '' +
      '<article class="automation-task-card automation-execution-card">' +
      '  <div class="automation-task-top">' +
      '    <div>' +
      '      <h4>' + escapeHtml(execution.taskName || execution.taskId || 'Execucao') + '</h4>' +
      '      <div class="automation-task-meta">' + escapeHtml(execution.actionLabel || execution.actionType || 'Acao') + ' • ' + escapeHtml(execution.statusLabel || execution.status || 'Status') + '</div>' +
      '    </div>' +
      '    <span class="automation-execution-badge ' + automationExecutionBadgeClass(execution) + '">' + escapeHtml(execution.statusLabel || execution.status || 'Status') + '</span>' +
      '  </div>' +
      '  <div class="automation-chip-row">' + chips.join(' ') + '</div>' +
      '  <div class="automation-task-details">' +
      '    <div><strong>Inicio:</strong> ' + escapeHtml(formatAutomationDate(execution.startedAt)) + '</div>' +
      '    <div><strong>Fim:</strong> ' + escapeHtml(execution.finishedAt ? formatAutomationDate(execution.finishedAt) : 'Em andamento') + '</div>' +
      (execution.durationLabel ? '<div><strong>Duracao:</strong> ' + escapeHtml(execution.durationLabel) + '</div>' : '') +
      (execution.commandId ? '<div><strong>CommandId:</strong> ' + escapeHtml(execution.commandId) + '</div>' : '') +
      details.join('') +
      '  </div>' + outputSection +
      '</article>';
  }).join('');
}

function renderAutomationState(state) {
  renderAutomationSummary(state || {});
  renderAutomationNotes(state || {});
  renderAutomationTasks(state && state.tasks ? state.tasks : []);
  renderAutomationExecutions(state && state.recentExecutions ? state.recentExecutions : [], (state && state.pendingCallbacks) || 0);
  if (automationTaskCountEl) {
    automationTaskCountEl.textContent = String((state && state.taskCount) || 0) + ' tarefas';
  }
  if (automationPendingCallbacksEl) {
    automationPendingCallbacksEl.textContent = String((state && state.pendingCallbacks) || 0) + ' callbacks pendentes';
  }
  if (automationExecutionCountEl) {
    automationExecutionCountEl.textContent = String((state && state.recentExecutions && state.recentExecutions.length) || 0) + ' execucoes';
  }
  if (automationIncludeScriptContentEl) {
    automationIncludeScriptContentEl.checked = !!(state && state.includeScriptContent);
  }
}

async function loadAutomationState() {
  if (!automationSummaryEl) return;
  setAutomationStatus('Carregando estado da automacao...', '');
  try {
    var state = await appApi().GetAutomationState();
    renderAutomationState(state || {});
    if (!state || !state.available) {
      setAutomationStatus('Automacao aguardando configuracao de servidor/token.', '');
    } else if (state.lastError) {
      setAutomationStatus('Automacao com ultimo erro registrado.', 'error');
    } else if (state.connected) {
      setAutomationStatus('Policy carregada com sucesso.', 'success');
    } else {
      setAutomationStatus('Policy local carregada sem comunicacao atual com o backend.', '');
    }
  } catch (err) {
    setAutomationStatus('Falha ao carregar automacao: ' + String(err), 'error');
  }
}

async function refreshAutomationPolicy() {
  var includeScriptContent = !!(automationIncludeScriptContentEl && automationIncludeScriptContentEl.checked);
  setAutomationStatus('Sincronizando policy...', '');
  if (automationRefreshBtn) automationRefreshBtn.disabled = true;
  try {
    var state = await appApi().RefreshAutomationPolicy(includeScriptContent);
    renderAutomationState(state || {});
    if (state && state.lastError) {
      setAutomationStatus('Policy sincronizada com alerta.', 'error');
    } else {
      setAutomationStatus('Policy sincronizada com sucesso.', 'success');
    }
  } catch (err) {
    setAutomationStatus('Falha no policy sync: ' + String(err), 'error');
  } finally {
    if (automationRefreshBtn) automationRefreshBtn.disabled = false;
  }
}

function initAutomation() {
  if (automationRefreshBtn) {
    automationRefreshBtn.addEventListener('click', refreshAutomationPolicy);
  }
  if (automationExecutionsEl) {
    automationExecutionsEl.addEventListener('click', function (event) {
      var button = event.target && event.target.closest ? event.target.closest('.automation-log-toggle') : null;
      if (!button) return;
      toggleAutomationExecutionOutput(button);
    });
  }
}