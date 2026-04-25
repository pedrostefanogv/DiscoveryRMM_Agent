"use strict";

function setAutomationStatus(message, type) {
  if (!automationStatusEl) return;
  automationStatusEl.textContent = message || '';
  automationStatusEl.className = 'debug-status' + (type ? ' ' + type : '');
}

// formatAutomationDate mantida como alias para compatibilidade; use formatDate diretamente.
function formatAutomationDate(value) { return formatDate(value, 'N/A'); }

function renderAutomationSummary(state) {
  if (!automationSummaryEl) return;

  var connectionText = !state.available ? translate('automation.awaitingConfig') : (state.connected ? translate('automation.synchronized') : translate('automation.noCommunication'));
  var upToDateText = state.upToDate ? translate('automation.noChanges') : translate('automation.newOrLocalPolicy');
  var cacheText = state.loadedFromCache ? translate('common.yes') : translate('common.no');
  var facts = [
    { label: translate('automation.connection'), value: connectionText },
    { label: 'PolicyFingerprint', value: state.policyFingerprint || 'N/A' },
    { label: translate('automation.tasks'), value: String(state.taskCount || 0) },
    { label: translate('automation.pendingCallbacksLabel'), value: String(state.pendingCallbacks || 0) },
    { label: translate('automation.upToDate'), value: upToDateText },
    { label: translate('automation.lastSync'), value: formatAutomationDate(state.lastSyncAt) },
    { label: translate('automation.lastAttempt'), value: formatAutomationDate(state.lastAttemptAt) },
    { label: translate('automation.localCache'), value: cacheText },
    { label: 'Correlation ID', value: state.correlationId || 'N/A' },
  ];

  automationSummaryEl.innerHTML = facts.map(function (fact) {
    return '<div class="fact"><span class="fact-label">' + escapeHtml(fact.label) + '</span><span class="fact-value">' + escapeHtml(fact.value) + '</span></div>';
  }).join('');
}

function renderAutomationNotes(state) {
  if (!automationNotesEl) return;

  var notes = [];
  if (state.generatedAt) notes.push(translate('automation.generatedAt', { date: formatAutomationDate(state.generatedAt) }));
  if (state.includeScriptContent) notes.push(translate('automation.manualSyncWithScript'));
  if (state.lastError) notes.push(translate('automation.lastErrorLine', { error: state.lastError }));
  if (!notes.length) notes.push(translate('automation.noAlerts'));
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
    automationTasksEl.innerHTML = '<div class="meta">' + escapeHtml(translate('automation.noneResolvedTasks')) + '</div>';
    return;
  }

  automationTasksEl.innerHTML = tasks.map(function (task) {
    var meta = [];
    meta.push(task.actionLabel || task.actionType || translate('automation.action'));
    meta.push(task.scopeLabel || task.scopeType || translate('automation.scope'));
    if (task.installationLabel) meta.push(task.installationLabel);
    if (task.scriptTypeLabel) meta.push(task.scriptTypeLabel);

    var details = [];
    if (task.packageId) details.push('<div><strong>PackageId:</strong> ' + escapeHtml(task.packageId) + '</div>');
    if (task.scriptName) details.push('<div><strong>Script:</strong> ' + escapeHtml(task.scriptName) + (task.scriptVersion ? ' v' + escapeHtml(task.scriptVersion) : '') + '</div>');
    if (task.scheduleCron) details.push('<div><strong>Cron:</strong> ' + escapeHtml(task.scheduleCron) + '</div>');
    if (task.commandPayload) details.push('<div><strong>Payload:</strong> ' + escapeHtml(task.commandPayload) + '</div>');
    if (task.includeTags && task.includeTags.length) details.push('<div><strong>IncludeTags:</strong> ' + escapeHtml(task.includeTags.join(', ')) + '</div>');
    if (task.excludeTags && task.excludeTags.length) details.push('<div><strong>ExcludeTags:</strong> ' + escapeHtml(task.excludeTags.join(', ')) + '</div>');
    if (task.lastUpdatedAt) details.push('<div><strong>' + escapeHtml(translate('automation.updatedAt')) + ':</strong> ' + escapeHtml(formatAutomationDate(task.lastUpdatedAt)) + '</div>');

    return '' +
      '<article class="automation-task-card">' +
      '  <div class="automation-task-top">' +
      '    <div>' +
      '      <h4>' + escapeHtml(task.name || translate('automation.untitledTask')) + '</h4>' +
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
    label.textContent = willExpand ? translate('automation.hideLogs') : translate('automation.viewLogs');
  }

  var icon = button.querySelector('.automation-log-toggle-icon');
  if (icon) {
    icon.textContent = willExpand ? '▾' : '▸';
  }
}

function renderAutomationExecutions(executions, pendingCallbacks) {
  if (!automationExecutionsEl) return;

  if (!executions || !executions.length) {
    automationExecutionsEl.innerHTML = '<div class="meta">' + escapeHtml(translate('automation.noExecutions')) + '</div>';
    return;
  }

  automationExecutionsEl.innerHTML = executions.map(function (execution, index) {
    var details = [];
    if (execution.summaryLine) details.push('<div><strong>' + escapeHtml(translate('automation.summary')) + ':</strong> ' + escapeHtml(execution.summaryLine) + '</div>');
    if (execution.packageId) details.push('<div><strong>PackageId:</strong> ' + escapeHtml(execution.packageId) + '</div>');
    if (execution.scriptId) details.push('<div><strong>ScriptId:</strong> ' + escapeHtml(execution.scriptId) + '</div>');
    if (execution.errorMessage) details.push('<div><strong>' + escapeHtml(translate('automation.errorLabel')) + ':</strong> ' + escapeHtml(execution.errorMessage) + '</div>');
    if (execution.exitCodeSet) details.push('<div><strong>ExitCode:</strong> ' + escapeHtml(String(execution.exitCode)) + '</div>');
    if (execution.correlationId) details.push('<div><strong>Correlation ID:</strong> ' + escapeHtml(execution.correlationId) + '</div>');

    var outputSection = '';
    if (execution.output) {
      var outputId = automationExecutionOutputId(execution, index);
      outputSection = '' +
        '<div class="automation-output-wrap">' +
        '  <button type="button" class="btn subtle automation-log-toggle" data-output-id="' + escapeHtmlAttr(outputId) + '" aria-expanded="false">' +
        '    <span class="automation-log-toggle-icon" aria-hidden="true">▸</span>' +
        '    <span class="automation-log-toggle-label">' + escapeHtml(translate('automation.viewLogs')) + '</span>' +
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
    if (execution.hasPendingCallback || pendingCallbacks > 0 && execution.commandId) chips.push('<span class="automation-chip warn">' + escapeHtml(translate('automation.pendingCallbackChip')) + '</span>');

    return '' +
      '<article class="automation-task-card automation-execution-card">' +
      '  <div class="automation-task-top">' +
      '    <div>' +
      '      <h4>' + escapeHtml(execution.taskName || execution.taskId || translate('automation.executionTitle')) + '</h4>' +
      '      <div class="automation-task-meta">' + escapeHtml(execution.actionLabel || execution.actionType || translate('automation.action')) + ' • ' + escapeHtml(execution.statusLabel || execution.status || translate('field.status')) + '</div>' +
      '    </div>' +
      '    <span class="automation-execution-badge ' + automationExecutionBadgeClass(execution) + '">' + escapeHtml(execution.statusLabel || execution.status || translate('field.status')) + '</span>' +
      '  </div>' +
      '  <div class="automation-chip-row">' + chips.join(' ') + '</div>' +
      '  <div class="automation-task-details">' +
      '    <div><strong>' + escapeHtml(translate('automation.start')) + ':</strong> ' + escapeHtml(formatAutomationDate(execution.startedAt)) + '</div>' +
      '    <div><strong>' + escapeHtml(translate('automation.end')) + ':</strong> ' + escapeHtml(execution.finishedAt ? formatAutomationDate(execution.finishedAt) : translate('automation.inProgress')) + '</div>' +
      (execution.durationLabel ? '<div><strong>' + escapeHtml(translate('automation.duration')) + ':</strong> ' + escapeHtml(execution.durationLabel) + '</div>' : '') +
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
    automationTaskCountEl.textContent = translate('automation.zeroTasks', { count: String((state && state.taskCount) || 0) });
  }
  if (automationPendingCallbacksEl) {
    automationPendingCallbacksEl.textContent = translate('automation.pendingCallbacks', { count: String((state && state.pendingCallbacks) || 0) });
  }
  if (automationExecutionCountEl) {
    automationExecutionCountEl.textContent = translate('automation.zeroExecutions', { count: String((state && state.recentExecutions && state.recentExecutions.length) || 0) });
  }
  if (automationIncludeScriptContentEl) {
    automationIncludeScriptContentEl.checked = !!(state && state.includeScriptContent);
  }
}

async function loadAutomationState() {
  if (!automationSummaryEl) return;
  setAutomationStatus(translate('automation.loadingState'), '');
  try {
    var state = await appApi().GetAutomationState();
    renderAutomationState(state || {});
    if (!state || !state.available) {
      setAutomationStatus(translate('automation.waitingServerConfig'), '');
    } else if (state.lastError) {
      setAutomationStatus(translate('automation.lastErrorState'), 'error');
    } else if (state.connected) {
      setAutomationStatus(translate('automation.policyLoadedSuccess'), 'success');
    } else {
      setAutomationStatus(translate('automation.policyLoadedLocal'), '');
    }
  } catch (err) {
    setAutomationStatus(translate('automation.loadFailed', { error: String(err) }), 'error');
  }
}

async function refreshAutomationPolicy() {
  var includeScriptContent = !!(automationIncludeScriptContentEl && automationIncludeScriptContentEl.checked);
  setAutomationStatus(translate('automation.syncingPolicy'), '');
  if (automationRefreshBtn) automationRefreshBtn.disabled = true;
  try {
    var state = await appApi().RefreshAutomationPolicy(includeScriptContent);
    renderAutomationState(state || {});
    if (state && state.lastError) {
      setAutomationStatus(translate('automation.syncWarning'), 'error');
    } else {
      setAutomationStatus(translate('automation.syncSuccess'), 'success');
    }
  } catch (err) {
    setAutomationStatus(translate('automation.syncFailed', { error: String(err) }), 'error');
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