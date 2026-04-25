"use strict";

const VALID_ACTIONS = new Set(['install', 'uninstall', 'upgrade']);

const state = {
  allPackages: [],
  filtered: [],
  selectedCategory: '',
  categoryNames: [],
  categoryCounts: {},
  packageActions: {},
};

const catalogPageSize = 48;
let catalogPage = 1;

const cardsEl = document.getElementById('cards');
const searchEl = document.getElementById('searchInput');
const infoEl = document.getElementById('catalogInfo');
const pageTitleEl = document.getElementById('pageTitle');
const feedbackEl = document.getElementById('feedback');
const installedOutputEl = document.getElementById('installedOutput');
const reloadBtn = document.getElementById('reloadBtn');
const upgradeAllBtn = document.getElementById('upgradeAllBtn');
const installedBtn = document.getElementById('installedBtn');
const tabStatusBtn = document.getElementById('tabStatus');
const tabStoreBtn = document.getElementById('tabStore');
const tabUpdatesBtn = document.getElementById('tabUpdates');
const tabInventoryBtn = document.getElementById('tabInventory');
const tabLogsBtn = document.getElementById('tabLogs');
const statusViewEl = document.getElementById('statusView');
const storeViewEl = document.getElementById('storeView');
const updatesViewEl = document.getElementById('updatesView');
const inventoryViewEl = document.getElementById('inventoryView');
const logsViewEl = document.getElementById('logsView');
const refreshInventoryBtn = document.getElementById('refreshInventoryBtn');
const inventoryInfoEl = document.getElementById('inventoryInfo');
const inventoryProgressEl = document.getElementById('inventoryProgress');
const inventoryInitialLoadingEl = document.getElementById('inventoryInitialLoading');
const inventoryInitialLoadingTextEl = document.getElementById('inventoryInitialLoadingText');
const inventoryContentEl = document.getElementById('inventoryContent');
const osqueryStatusEl = document.getElementById('osqueryStatus');
const exportStatusEl = document.getElementById('exportStatus');
const installOsqueryBtn = document.getElementById('installOsqueryBtn');
const exportInventoryBtn = document.getElementById('exportInventoryBtn');
const exportInventoryPdfBtn = document.getElementById('exportInventoryPdfBtn');
const redactToggleEl = document.getElementById('redactToggle');
const hardwareOutputEl = document.getElementById('hardwareOutput');
const osOutputEl = document.getElementById('osOutput');
const loggedUsersOutputEl = document.getElementById('loggedUsersOutput');
const volumeOutputEl = document.getElementById('volumeOutput');
const physicalDiskOutputEl = document.getElementById('physicalDiskOutput');
const networkOutputEl = document.getElementById('networkOutput');
const printerOutputEl = document.getElementById('printerOutput');
const memoryOutputEl = document.getElementById('memoryOutput');
const monitorOutputEl = document.getElementById('monitorOutput');
const gpuOutputEl = document.getElementById('gpuOutput');
const batteryOutputEl = document.getElementById('batteryOutput');
const bitlockerOutputEl = document.getElementById('bitlockerOutput');
const cpuInfoOutputEl = document.getElementById('cpuInfoOutput');
const startupTableBodyEl = document.getElementById('startupTableBody');
const startupSearchInputEl = document.getElementById('startupSearchInput');
const startupCountEl = document.getElementById('startupCount');
const refreshStartupBtn = document.getElementById('refreshStartupBtn');
const startupPrevBtn = document.getElementById('startupPrevBtn');
const startupNextBtn = document.getElementById('startupNextBtn');
const startupPageInfoEl = document.getElementById('startupPageInfo');
const autoexecOutputEl = document.getElementById('autoexecOutput');
const softwareSearchInputEl = document.getElementById('softwareSearchInput');
const softwareTableBodyEl = document.getElementById('softwareTableBody');
const softwareCountEl = document.getElementById('softwareCount');
const chatViewEl = document.getElementById('chatView');
const tabChatBtn = document.getElementById('tabChat');
const supportViewEl = document.getElementById('supportView');
const tabSupportBtn = document.getElementById('tabSupport');
const knowledgeViewEl = document.getElementById('knowledgeView');
const tabKnowledgeBtn = document.getElementById('tabKnowledge');
const automationViewEl = document.getElementById('automationView');
const tabAutomationBtn = document.getElementById('tabAutomation');
const supportFormEl = document.getElementById('supportForm');
const supportTicketsListEl = document.getElementById('supportTicketsList');
// Support - extended refs
const agentContextBannerEl = document.getElementById('agentContextBanner');
const agentContextTextEl = document.getElementById('agentContextText');
const agentContextErrorEl = document.getElementById('agentContextError');
const agentContextErrorTextEl = document.getElementById('agentContextErrorText');
const ticketsLoadingEl = document.getElementById('ticketsLoading');
const refreshTicketsBtnEl = document.getElementById('refreshTicketsBtn');
const newTicketBtnEl = document.getElementById('newTicketBtn');
const closeNewTicketBtnEl = document.getElementById('closeNewTicketBtn');
const supportCreateOverlayEl = document.getElementById('supportCreateOverlay');
const supportSidePanelEl = document.getElementById('supportSidePanel');
const supportCreateFormEl = document.getElementById('supportCreateForm');
const supportTicketDetailEl = document.getElementById('supportTicketDetail');
const backToFormBtnEl = document.getElementById('backToFormBtn');
const ticketDetailIdEl = document.getElementById('ticketDetailId');
const ticketDetailStatusEl = document.getElementById('ticketDetailStatus');
const ticketDetailPriorityEl = document.getElementById('ticketDetailPriority');
const ticketDetailTitleEl = document.getElementById('ticketDetailTitle');
const ticketDetailMetaEl = document.getElementById('ticketDetailMeta');
const ticketDetailDescEl = document.getElementById('ticketDetailDesc');
const ticketClosePanelEl = document.getElementById('ticketClosePanel');
const closeTicketRatingEl = document.getElementById('closeTicketRating');
const closeTicketWorkflowStateSelectEl = document.getElementById('closeTicketWorkflowStateSelect');
const closeTicketWorkflowStateIdEl = document.getElementById('closeTicketWorkflowStateId');
const closeTicketCommentEl = document.getElementById('closeTicketComment');
const closeTicketBtnEl = document.getElementById('closeTicketBtn');
const commentsListEl = document.getElementById('commentsList');
const commentInputEl = document.getElementById('commentInput');
const submitCommentBtnEl = document.getElementById('submitCommentBtn');
const ticketFormStatusEl = document.getElementById('ticketFormStatus');
const kbSearchInputEl = document.getElementById('kbSearchInput');
const kbArticlesListEl = document.getElementById('kbArticlesList');
const kbArticleDetailEl = document.getElementById('kbArticleDetail');
const kbDetailTitleEl = document.getElementById('kbDetailTitle');
const kbDetailMetaEl = document.getElementById('kbDetailMeta');
const kbDetailContentEl = document.getElementById('kbDetailContent');
const kbOpenFullBtn = document.getElementById('kbOpenFullBtn');
const kbReaderModal = document.getElementById('kbReaderModal');
const kbReaderTitleEl = document.getElementById('kbReaderTitle');
const kbReaderMetaEl = document.getElementById('kbReaderMeta');
const kbReaderContentEl = document.getElementById('kbReaderContent');
const kbReaderCloseBtn = document.getElementById('kbReaderCloseBtn');
const chatMessagesEl = document.getElementById('chatMessages');
const chatInputEl = document.getElementById('chatInput');
const chatSendBtn = document.getElementById('chatSendBtn');
const chatStopBtn = document.getElementById('chatStopBtn');
const chatConfigBtn = document.getElementById('chatConfigBtn');
const chatToolsBtn = document.getElementById('chatToolsBtn');
const chatLogsBtn = document.getElementById('chatLogsBtn');
const chatClearBtn = document.getElementById('chatClearBtn');
const chatMemoriesBtn = document.getElementById('chatMemoriesBtn');
const chatMemoriesModal = document.getElementById('chatMemoriesModal');
const chatMemoriesList = document.getElementById('chatMemoriesList');
const chatMemoriesRefreshBtn = document.getElementById('chatMemoriesRefreshBtn');
const chatMemoriesCloseBtn = document.getElementById('chatMemoriesCloseBtn');
const chatConfigPanel = document.getElementById('chatConfigPanel');
const chatToolsPanel = document.getElementById('chatToolsPanel');
const chatToolsList = document.getElementById('chatToolsList');
const chatTestConfigBtn = document.getElementById('chatTestConfigBtn');
const chatSaveConfigBtn = document.getElementById('chatSaveConfigBtn');
const chatEndpointEl = document.getElementById('chatEndpoint');
const chatApiKeyEl = document.getElementById('chatApiKey');
const chatModelEl = document.getElementById('chatModel');
const chatMaxTokensEl = document.getElementById('chatMaxTokens');
const chatSystemPromptEl = document.getElementById('chatSystemPrompt');
const chatLogsModal = document.getElementById('chatLogsModal');
const chatLogsOutput = document.getElementById('chatLogsOutput');
const chatLogsCloseBtn = document.getElementById('chatLogsCloseBtn');
const chatLogsRefreshBtn = document.getElementById('chatLogsRefreshBtn');
const softwarePrevBtn = document.getElementById('softwarePrevBtn');
const softwareNextBtn = document.getElementById('softwareNextBtn');
const softwarePageInfoEl = document.getElementById('softwarePageInfo');
const refreshSoftwareBtn = document.getElementById('refreshSoftwareBtn');
const connectionsTabListening = document.getElementById('connectionsTabListening');
const connectionsTabOpen = document.getElementById('connectionsTabOpen');
const refreshConnectionsBtn = document.getElementById('refreshConnectionsBtn');
const refreshListeningPortsBtn = document.getElementById('refreshListeningPortsBtn');
const connectionsRefreshStatusEl = document.getElementById('connectionsRefreshStatus');
const connectionsSearchInputEl = document.getElementById('connectionsSearchInput');
const connectionsPrevBtn = document.getElementById('connectionsPrevBtn');
const connectionsNextBtn = document.getElementById('connectionsNextBtn');
const connectionsPageInfoEl = document.getElementById('connectionsPageInfo');
const connectionsCountEl = document.getElementById('connectionsCount');
const listeningPortsTableBodyEl = document.getElementById('listeningPortsTableBody');
const openSocketsTableBodyEl = document.getElementById('openSocketsTableBody');
const listeningPortsTableEl = document.getElementById('listeningPortsTable');
const openSocketsTableEl = document.getElementById('openSocketsTable');
const catalogPrevBtn = document.getElementById('catalogPrevBtn');
const catalogNextBtn = document.getElementById('catalogNextBtn');
const catalogPageInfoEl = document.getElementById('catalogPageInfo');
const sidebarEl = document.getElementById('sidebar');
const sidebarToggleBtn = document.getElementById('sidebarToggle');
const categorySearchEl = document.getElementById('categorySearch');
const categoryListEl = document.getElementById('categoryList');
const toastContainerEl = document.getElementById('toastContainer');
const themeToggleBtn = document.getElementById('themeToggle');
const checkUpdatesBtn = document.getElementById('checkUpdatesBtn');
const upgradeSelectedBtn = document.getElementById('upgradeSelectedBtn');
const updatesTableBodyEl = document.getElementById('updatesTableBody');
const updatesInfoEl = document.getElementById('updatesInfo');
const updatesProgressEl = document.getElementById('updatesProgress');
const updateSelectAllEl = document.getElementById('updateSelectAll');
const logsOutputEl = document.getElementById('logsOutput');
const logsOriginFilterEl = document.getElementById('logsOriginFilter');
const refreshLogsBtn = document.getElementById('refreshLogsBtn');
const clearLogsBtn = document.getElementById('clearLogsBtn');
const tabDebugBtn = document.getElementById('tabDebug');
const tabPSADTBtn = document.getElementById('tabPSADT');
const tabP2PBtn = document.getElementById('tabP2P');
const debugViewEl = document.getElementById('debugView');
const psadtViewEl = document.getElementById('psadtView');
const p2pViewEl = document.getElementById('p2pView');
const apiSchemeEl = document.getElementById('apiScheme');
const apiServerEl = document.getElementById('apiServer');
const natsServerEl = document.getElementById('natsServer');
const debugAuthTokenEl = document.getElementById('debugAuthToken');
const debugAgentIDEl = document.getElementById('debugAgentID');
const automationP2PWingetInstallEnabledEl = document.getElementById('automationP2PWingetInstallEnabled');
const debugSaveBtn = document.getElementById('debugSaveBtn');
const debugTestBtn = document.getElementById('debugTestBtn');
const debugStatusEl = document.getElementById('debugStatus');
const debugResponseWrapEl = document.getElementById('debugResponseWrap');
const debugResponseLabelEl = document.getElementById('debugResponseLabel');
const debugResponseEl = document.getElementById('debugResponse');
const agentStatusDotEl = document.getElementById('agentStatusDot');
const agentStatusLabelEl = document.getElementById('agentStatusLabel');
const agentStatusDetailEl = document.getElementById('agentStatusDetail');
const agentStatusRefreshBtn = document.getElementById('agentStatusRefreshBtn');
const watchdogHealthContainer = document.getElementById('watchdogHealthContainer');
const watchdogRefreshBtn = document.getElementById('watchdogRefreshBtn');
const automationRefreshBtn = document.getElementById('automationRefreshBtn');
const automationIncludeScriptContentEl = document.getElementById('automationIncludeScriptContent');
const automationStatusEl = document.getElementById('automationStatus');
const automationSummaryEl = document.getElementById('automationSummary');
const automationNotesEl = document.getElementById('automationNotes');
const automationTasksEl = document.getElementById('automationTasks');
const automationTaskCountEl = document.getElementById('automationTaskCount');
const automationPendingCallbacksEl = document.getElementById('automationPendingCallbacks');
const automationExecutionsEl = document.getElementById('automationExecutions');
const automationExecutionCountEl = document.getElementById('automationExecutionCount');

let agentStatusPollId = null;
let watchdogPollId = null;

const WATCHDOG_TOAST_DEDUPE_MS = 60 * 1000;
const WATCHDOG_TOAST_MAX_PER_MINUTE = 3;
const PROVISIONING_CHECK_INTERVAL_MS = 10 * 1000;
const watchdogToastState = {
  lastToastAtByComponentStatus: {},
  windowStartMs: 0,
  sentInCurrentWindow: 0,
};

const DEFAULT_NOTIFICATION_THEME = {
  surface: '#1f1a14',
  text: '#f8f4ea',
  accent: '#f4a259',
  success: '#0b6e4f',
  warning: '#8a4e12',
  danger: '#9a031e',
};

const notificationUXState = {
  refs: null,
  modalTimerId: null,
  modalDeadlineAt: 0,
  rebootTimerId: null,
  rebootDeadlineAt: 0,
  activeProgressGroup: '',
  progressByGroup: {},
};

const provisioningOverlayState = {
  refs: null,
  pollId: null,
};

let inventorySoftware = [];
let inventorySoftwareFiltered = [];
let softwareSortKey = 'name';
let softwareSortDirection = 'asc';
let softwarePage = 1;
const softwarePageSize = 30;
let inventoryStartupItems = [];
let inventoryStartupItemsFiltered = [];
let startupSortKey = 'name';
let startupSortDirection = 'asc';
let startupPage = 1;
const startupPageSize = 25;
let connectionsData = { listening: [], open: [] };
let connectionsFiltered = [];
let connectionsType = 'listening';
let connectionsSortKey = 'processName';
let connectionsSortDirection = 'asc';
let connectionsPage = 1;
const connectionsPageSize = 50;
let connectionsRefreshInFlight = false;
let inventoryLoadedOnce = false;
let pendingUpdates = [];
let logsAutoRefreshId = null;
let logsLastLines = [];
let knowledgeArticles = [];
let selectedKnowledgeArticleID = '';
let activeTab = 'status';
let runtimeFlags = {
  debugMode: false,
};

function isAppWindowVisible() {
  return typeof document === 'undefined' || !document.hidden;
}

function setUISuspended(suspended) {
  window.__discoveryUISuspended = !!suspended;
  document.dispatchEvent(new CustomEvent(suspended ? 'ui:suspend' : 'ui:resume'));
}

function isDebugRuntimeMode() {
  return !!runtimeFlags.debugMode;
}

function isRuntimeTabAllowed(tab) {
  if (isDebugRuntimeMode()) {
    return true;
  }
  // In normal mode we hide tabs/views that are only relevant for debugging.
  return tab !== 'logs' && tab !== 'debug' && tab !== 'psadt' && tab !== 'automation' && tab !== 'p2p' && tab !== 'inventory';
}

function applyRuntimeTabVisibility() {
  var hiddenInNormal = !isDebugRuntimeMode();

  if (tabInventoryBtn) tabInventoryBtn.classList.toggle('hidden', hiddenInNormal);
  if (tabLogsBtn) tabLogsBtn.classList.toggle('hidden', hiddenInNormal);
  if (tabDebugBtn) tabDebugBtn.classList.toggle('hidden', hiddenInNormal);
  if (tabPSADTBtn) tabPSADTBtn.classList.toggle('hidden', hiddenInNormal);
  if (tabAutomationBtn) tabAutomationBtn.classList.toggle('hidden', hiddenInNormal);
  if (chatMemoriesBtn) chatMemoriesBtn.classList.toggle('hidden', hiddenInNormal);
  const openP2PDebugStatusBtnEl = document.getElementById('openP2PDebugStatusBtn');
  if (openP2PDebugStatusBtnEl) openP2PDebugStatusBtnEl.classList.toggle('hidden', hiddenInNormal);
  if (tabP2PBtn) tabP2PBtn.classList.toggle('hidden', hiddenInNormal);

  if (hiddenInNormal) {
    if (inventoryViewEl) inventoryViewEl.classList.add('hidden');
    if (logsViewEl) logsViewEl.classList.add('hidden');
    if (debugViewEl) debugViewEl.classList.add('hidden');
    if (psadtViewEl) psadtViewEl.classList.add('hidden');
    if (automationViewEl) automationViewEl.classList.add('hidden');
    if (p2pViewEl) p2pViewEl.classList.add('hidden');
  }

  if (!isRuntimeTabAllowed(activeTab)) {
    setActiveTab('store');
  }
}

function setRuntimeFlags(flags) {
  runtimeFlags = {
    debugMode: !!(flags && flags.debugMode),
  };
  applyRuntimeTabVisibility();
}

function appApi() {
  if (!window.go || !window.go.app || !window.go.app.App) {
    throw new Error('API do Wails indisponivel. Rode pelo wails dev/build.');
  }
  return window.go.app.App;
}

function showFeedback(message, isError) {
  if (feedbackEl) {
    feedbackEl.textContent = message;
    feedbackEl.style.color = isError ? '#9a031e' : '#665a4c';
  }
  showToast(message, isError ? 'error' : 'info');
}

function showToast(message, type) {
  if (!toastContainerEl) return;
  var toast = document.createElement('div');
  toast.className = 'toast ' + (type || 'info');
  toast.textContent = message;
  toastContainerEl.appendChild(toast);
  setTimeout(function () {
    toast.classList.add('removing');
    toast.addEventListener('animationend', function () { toast.remove(); });
  }, 3500);
}

function computeProvisioningState(debugCfg) {
  var cfg = debugCfg || {};
  var scheme = String(cfg.apiScheme || cfg.scheme || '').trim().toLowerCase();
  var server = String(cfg.apiServer || cfg.server || '').trim();
  var authToken = String(cfg.authToken || '').trim();
  var agentId = String(cfg.agentId || '').trim();
  var missing = [];

  if (!scheme || !server) missing.push('URL do servidor');
  if (!agentId) missing.push('Agent ID');
  if (!authToken) missing.push('Chave/token de comunicacao');

  return {
    isProvisioned: missing.length === 0,
    scheme: scheme,
    server: server,
    agentId: agentId,
    tokenPresent: authToken !== '',
    missing: missing,
  };
}

function ensureProvisioningOverlay() {
  if (provisioningOverlayState.refs) return provisioningOverlayState.refs;

  var provisioningAriaLabel = translate('provisioning.ariaLabel');
  var provisioningBadgeLabel = translate('provisioning.badge');
  var overlay = document.createElement('div');
  overlay.id = 'provisioningOverlay';
  overlay.className = 'provisioning-overlay hidden';
  overlay.setAttribute('aria-hidden', 'true');
  overlay.innerHTML = '' +
    '<section class="provisioning-panel" role="status" aria-live="polite" aria-label="' + escapeHtmlAttr(provisioningAriaLabel) + '">' +
      '<div class="provisioning-badge">' + escapeHtml(provisioningBadgeLabel) + '</div>' +
      '<div class="provisioning-hero">' +
        '<div class="provisioning-icon" aria-hidden="true">' +
          '<svg viewBox="0 0 64 64" fill="none" xmlns="http://www.w3.org/2000/svg">' +
            '<circle cx="32" cy="32" r="10" stroke="currentColor" stroke-width="4"/>' +
            '<path d="M32 8V16M32 48V56M8 32H16M48 32H56M15.5 15.5L21.2 21.2M42.8 42.8L48.5 48.5M48.5 15.5L42.8 21.2M21.2 42.8L15.5 48.5" stroke="currentColor" stroke-width="4" stroke-linecap="round"/>' +
          '</svg>' +
        '</div>' +
        '<div class="provisioning-copy">' +
          '<h2>' + translate('provisioning.title') + '</h2>' +
          '<p>' + translate('provisioning.message') + '</p>' +
        '</div>' +
      '</div>' +
      '<div class="provisioning-footnote" id="provisioningFootnote">' + translate('provisioning.footnote') + '</div>' +
      '<div class="provisioning-actions">' +
        '<button type="button" class="btn primary" id="provisioningRefreshBtn">' + translate('provisioning.refresh') + '</button>' +
      '</div>' +
    '</section>';
  document.body.appendChild(overlay);

  var refs = {
    overlay: overlay,
    footnote: document.getElementById('provisioningFootnote'),
    refreshBtn: document.getElementById('provisioningRefreshBtn'),
  };

  refs.refreshBtn.addEventListener('click', function () {
    syncProvisioningOverlayFromRuntime();
  });

  provisioningOverlayState.refs = refs;
  return refs;
}

function stopProvisioningOverlayPolling() {
  if (!provisioningOverlayState.pollId) return;
  clearInterval(provisioningOverlayState.pollId);
  provisioningOverlayState.pollId = null;
}

function startProvisioningOverlayPolling() {
  if (provisioningOverlayState.pollId) return;
  provisioningOverlayState.pollId = setInterval(function () {
    if (document.hidden) return;
    syncProvisioningOverlayFromRuntime();
  }, PROVISIONING_CHECK_INTERVAL_MS);
}

function syncProvisioningOverlayFromConfig(debugCfg) {
  var refs = ensureProvisioningOverlay();
  var status = computeProvisioningState(debugCfg);

  if (status.isProvisioned) {
    refs.overlay.classList.add('hidden');
    refs.overlay.setAttribute('aria-hidden', 'true');
    refs.footnote.textContent = translate('provisioning.completed');
    stopProvisioningOverlayPolling();
  } else {
    refs.overlay.classList.remove('hidden');
    refs.overlay.setAttribute('aria-hidden', 'false');
    refs.footnote.textContent = translate('provisioning.footnote');
    startProvisioningOverlayPolling();
  }

  return status;
}

function syncProvisioningOverlayFromRuntime() {
  try {
    return appApi().GetDebugConfig().then(function (cfg) {
      return syncProvisioningOverlayFromConfig(cfg);
    }).catch(function () {
      return null;
    });
  } catch (_) {
    return Promise.resolve(null);
  }
}

function sanitizeThemeColor(input, fallback) {
  var value = String(input || '').trim();
  if (!value) return fallback;
  if (/^#[0-9a-fA-F]{3,8}$/.test(value)) return value;
  if (/^rgb\(/i.test(value) || /^rgba\(/i.test(value) || /^hsl\(/i.test(value) || /^hsla\(/i.test(value)) return value;
  return fallback;
}

function normalizeNotificationModeValue(modeRaw) {
  var value = String(modeRaw || '').toLowerCase();
  if (value === 'silent' || value === 'silencioso') return 'silent';
  if (value === 'require_confirmation' || value === 'confirm') return 'require_confirmation';
  return 'notify_only';
}

function normalizeNotificationSeverityValue(severityRaw) {
  var value = String(severityRaw || '').toLowerCase();
  if (value === 'informativo' || value === 'info' || value === 'baixo' || value === 'low') return 'low';
  if (value === 'alerta' || value === 'warn' || value === 'warning' || value === 'medio' || value === 'médio' || value === 'medium') return 'medium';
  if (value === 'erro' || value === 'error' || value === 'alto' || value === 'high') return 'high';
  if (value === 'critico' || value === 'crítico' || value === 'critical') return 'critical';
  return 'medium';
}

function getDocumentLocale() {
  if (typeof getAppLocale === 'function') {
    return getAppLocale();
  }
  var root = document && document.documentElement;
  var lang = root && root.lang ? String(root.lang).toLowerCase() : 'pt-br';
  return lang || 'pt-br';
}

function buildNotificationMicrocopy(eventType, severity) {
  var locale = getDocumentLocale();
  var isPT = locale.indexOf('pt') === 0;
  var severityLabel = {
    low: isPT ? 'Informacao' : 'Info',
    medium: isPT ? 'Atencao' : 'Attention',
    high: isPT ? 'Erro' : 'Error',
    critical: isPT ? 'Critico' : 'Critical',
  }[severity] || (isPT ? 'Atencao' : 'Attention');

  var eventLabelMap = isPT
    ? {
      install_start: 'Instalacao iniciada',
      install_end: 'Instalacao concluida',
      install_failed: 'Instalacao com falha',
      reboot_required: 'Reinicio necessario',
      restart_required: 'Reinicio necessario',
    }
    : {
      install_start: 'Installation started',
      install_end: 'Installation completed',
      install_failed: 'Installation failed',
      reboot_required: 'Restart required',
      restart_required: 'Restart required',
    };

  return {
    severityLabel: severityLabel,
    eventLabel: eventLabelMap[eventType] || (isPT ? 'Notificacao de instalacao' : 'Installation notification'),
    approveLabel: isPT ? 'Aprovar' : 'Approve',
    deferLabel: isPT ? 'Adiar' : 'Defer',
    denyLabel: isPT ? 'Negar' : 'Deny',
    closeLabel: isPT ? 'Fechar' : 'Close',
    detailsLabel: isPT ? 'Ver detalhes' : 'Details',
    restartNowLabel: isPT ? 'Reiniciar agora' : 'Restart now',
    restartLaterLabel: isPT ? 'Reiniciar depois' : 'Restart later',
    rebootHint: isPT ? 'Salve seu trabalho. Reinicie para concluir a instalacao.' : 'Save your work. Restart to complete installation.',
  };
}

function resolveNotificationTheme(metadata, severity) {
  var styleOverride = metadata && metadata.styleOverride && typeof metadata.styleOverride === 'object' ? metadata.styleOverride : {};
  var branding = metadata && metadata.branding && typeof metadata.branding === 'object' ? metadata.branding : {};
  var tenantTheme = branding.theme && typeof branding.theme === 'object' ? branding.theme : {};

  var base = {
    surface: sanitizeThemeColor(tenantTheme.surface, DEFAULT_NOTIFICATION_THEME.surface),
    text: sanitizeThemeColor(tenantTheme.text, DEFAULT_NOTIFICATION_THEME.text),
    accent: sanitizeThemeColor(tenantTheme.accent, DEFAULT_NOTIFICATION_THEME.accent),
    success: sanitizeThemeColor(tenantTheme.success, DEFAULT_NOTIFICATION_THEME.success),
    warning: sanitizeThemeColor(tenantTheme.warning, DEFAULT_NOTIFICATION_THEME.warning),
    danger: sanitizeThemeColor(tenantTheme.danger, DEFAULT_NOTIFICATION_THEME.danger),
  };

  if (styleOverride && typeof styleOverride === 'object') {
    base.surface = sanitizeThemeColor(styleOverride.background, base.surface);
    base.text = sanitizeThemeColor(styleOverride.text, base.text);
  }

  if (severity === 'high' || severity === 'critical') {
    base.accent = base.danger;
  } else if (severity === 'medium') {
    base.accent = base.warning;
  } else if (severity === 'low') {
    base.accent = base.success;
  }

  base.companyName = String(branding.companyName || '').trim();
  return base;
}

function ensureNotificationUX() {
  if (notificationUXState.refs) return notificationUXState.refs;

  var hub = document.createElement('div');
  hub.className = 'ntf-hub';
  hub.innerHTML = '' +
    '<div class="ntf-banner-stack" id="ntfBannerStack"></div>' +
    '<div class="ntf-progress-card hidden" id="ntfProgressCard">' +
      '<div class="ntf-progress-head">' +
        '<strong id="ntfProgressTitle">Progresso da instalacao</strong>' +
        '<span id="ntfProgressMeta" class="meta"></span>' +
      '</div>' +
      '<div class="ntf-progress-bar-wrap"><div id="ntfProgressBar" class="ntf-progress-bar"></div></div>' +
      '<div id="ntfProgressDetail" class="ntf-progress-detail"></div>' +
    '</div>';
  document.body.appendChild(hub);

  var modalOverlay = document.createElement('div');
  modalOverlay.className = 'ntf-modal-overlay hidden';
  modalOverlay.setAttribute('aria-hidden', 'true');
  modalOverlay.id = 'ntfModalOverlay';
  modalOverlay.innerHTML = '' +
    '<div class="ntf-modal-card" role="dialog" aria-modal="true" aria-label="Notificacao">' +
      '<div class="ntf-modal-brand" id="ntfModalBrand"></div>' +
      '<h3 id="ntfModalTitle">Notificacao</h3>' +
      '<p id="ntfModalMessage"></p>' +
      '<div class="ntf-modal-meta" id="ntfModalMeta"></div>' +
      '<div class="ntf-modal-countdown hidden" id="ntfModalCountdown"></div>' +
      '<div class="ntf-modal-actions" id="ntfModalActions"></div>' +
    '</div>';
  document.body.appendChild(modalOverlay);

  var rebootOverlay = document.createElement('div');
  rebootOverlay.className = 'ntf-modal-overlay hidden';
  rebootOverlay.setAttribute('aria-hidden', 'true');
  rebootOverlay.id = 'ntfRebootOverlay';
  rebootOverlay.innerHTML = '' +
    '<div class="ntf-modal-card ntf-reboot-card" role="dialog" aria-modal="true" aria-label="Reinicio necessario">' +
      '<div class="ntf-reboot-pulse"></div>' +
      '<h3 id="ntfRebootTitle">Reinicio necessario</h3>' +
      '<p id="ntfRebootMessage">Reinicie o computador para concluir a instalacao.</p>' +
      '<div class="ntf-modal-countdown" id="ntfRebootCountdown"></div>' +
      '<div class="ntf-modal-actions">' +
        '<button type="button" class="btn btn-primary" id="ntfRebootNowBtn">Reiniciar agora</button>' +
        '<button type="button" class="btn" id="ntfRebootLaterBtn">Reiniciar depois</button>' +
      '</div>' +
    '</div>';
  document.body.appendChild(rebootOverlay);

  var refs = {
    hub: hub,
    bannerStack: document.getElementById('ntfBannerStack'),
    progressCard: document.getElementById('ntfProgressCard'),
    progressTitle: document.getElementById('ntfProgressTitle'),
    progressMeta: document.getElementById('ntfProgressMeta'),
    progressBar: document.getElementById('ntfProgressBar'),
    progressDetail: document.getElementById('ntfProgressDetail'),
    modalOverlay: modalOverlay,
    modalBrand: document.getElementById('ntfModalBrand'),
    modalTitle: document.getElementById('ntfModalTitle'),
    modalMessage: document.getElementById('ntfModalMessage'),
    modalMeta: document.getElementById('ntfModalMeta'),
    modalActions: document.getElementById('ntfModalActions'),
    modalCountdown: document.getElementById('ntfModalCountdown'),
    rebootOverlay: rebootOverlay,
    rebootTitle: document.getElementById('ntfRebootTitle'),
    rebootMessage: document.getElementById('ntfRebootMessage'),
    rebootCountdown: document.getElementById('ntfRebootCountdown'),
    rebootNowBtn: document.getElementById('ntfRebootNowBtn'),
    rebootLaterBtn: document.getElementById('ntfRebootLaterBtn'),
  };

  refs.modalOverlay.addEventListener('click', function (e) {
    if (e.target === refs.modalOverlay) closeNotificationModal();
  });

  refs.rebootLaterBtn.addEventListener('click', function () {
    closeRebootModal();
  });

  refs.rebootNowBtn.addEventListener('click', function () {
    showToast('Reinicio solicitado. Execute o restart conforme policy do endpoint.', 'warning');
    closeRebootModal();
  });

  notificationUXState.refs = refs;
  return refs;
}

function setThemeVars(targetEl, theme) {
  if (!targetEl || !theme) return;
  targetEl.style.setProperty('--ntf-surface', theme.surface);
  targetEl.style.setProperty('--ntf-text', theme.text);
  targetEl.style.setProperty('--ntf-accent', theme.accent);
}

function closeNotificationModal() {
  var refs = ensureNotificationUX();
  refs.modalOverlay.classList.add('hidden');
  refs.modalOverlay.setAttribute('aria-hidden', 'true');
  if (notificationUXState.modalTimerId) {
    clearInterval(notificationUXState.modalTimerId);
    notificationUXState.modalTimerId = null;
  }
  notificationUXState.modalDeadlineAt = 0;
}

function openNotificationModal(options) {
  var refs = ensureNotificationUX();
  var opt = options || {};
  var theme = opt.theme || DEFAULT_NOTIFICATION_THEME;

  refs.modalTitle.textContent = String(opt.title || 'Notificacao');
  refs.modalMessage.textContent = String(opt.message || '');
  refs.modalMeta.textContent = String(opt.metaText || '');
  refs.modalBrand.textContent = String(opt.brandText || '');
  refs.modalActions.innerHTML = '';
  setThemeVars(refs.modalOverlay, theme);

  var actions = Array.isArray(opt.actions) ? opt.actions : [];
  for (var i = 0; i < actions.length; i += 1) {
    (function (action, idx) {
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'btn' + (idx === 0 ? ' btn-primary' : '');
      btn.textContent = String(action.label || 'OK');
      btn.addEventListener('click', function () {
        if (typeof action.onClick === 'function') action.onClick();
      });
      refs.modalActions.appendChild(btn);
    })(actions[i], i);
  }

  if (opt.timeoutSeconds > 0) {
    refs.modalCountdown.classList.remove('hidden');
    notificationUXState.modalDeadlineAt = Date.now() + (opt.timeoutSeconds * 1000);
    refs.modalCountdown.textContent = 'Expira em ' + String(opt.timeoutSeconds) + 's';
    if (notificationUXState.modalTimerId) {
      clearInterval(notificationUXState.modalTimerId);
      notificationUXState.modalTimerId = null;
    }
    notificationUXState.modalTimerId = setInterval(function () {
      var remaining = Math.max(0, Math.floor((notificationUXState.modalDeadlineAt - Date.now()) / 1000));
      refs.modalCountdown.textContent = 'Expira em ' + String(remaining) + 's';
      if (remaining <= 0) {
        clearInterval(notificationUXState.modalTimerId);
        notificationUXState.modalTimerId = null;
        closeNotificationModal();
      }
    }, 1000);
  } else {
    refs.modalCountdown.classList.add('hidden');
    refs.modalCountdown.textContent = '';
  }

  refs.modalOverlay.classList.remove('hidden');
  refs.modalOverlay.setAttribute('aria-hidden', 'false');
}

function closeRebootModal() {
  var refs = ensureNotificationUX();
  refs.rebootOverlay.classList.add('hidden');
  refs.rebootOverlay.setAttribute('aria-hidden', 'true');
  if (notificationUXState.rebootTimerId) {
    clearInterval(notificationUXState.rebootTimerId);
    notificationUXState.rebootTimerId = null;
  }
  notificationUXState.rebootDeadlineAt = 0;
}

function openRebootModal(options) {
  var refs = ensureNotificationUX();
  var opt = options || {};
  var countdownSeconds = Math.max(0, Number(opt.countdownSeconds || 0));

  refs.rebootTitle.textContent = String(opt.title || 'Reinicio necessario');
  refs.rebootMessage.textContent = String(opt.message || 'Reinicie o computador para concluir a instalacao.');
  setThemeVars(refs.rebootOverlay, opt.theme || DEFAULT_NOTIFICATION_THEME);

  if (countdownSeconds > 0) {
    notificationUXState.rebootDeadlineAt = Date.now() + (countdownSeconds * 1000);
    refs.rebootCountdown.textContent = 'Reinicio recomendado em ' + String(countdownSeconds) + 's';
    if (notificationUXState.rebootTimerId) {
      clearInterval(notificationUXState.rebootTimerId);
      notificationUXState.rebootTimerId = null;
    }
    notificationUXState.rebootTimerId = setInterval(function () {
      var remaining = Math.max(0, Math.floor((notificationUXState.rebootDeadlineAt - Date.now()) / 1000));
      refs.rebootCountdown.textContent = 'Reinicio recomendado em ' + String(remaining) + 's';
      if (remaining <= 0) {
        clearInterval(notificationUXState.rebootTimerId);
        notificationUXState.rebootTimerId = null;
      }
    }, 1000);
  } else {
    refs.rebootCountdown.textContent = '';
  }

  refs.rebootOverlay.classList.remove('hidden');
  refs.rebootOverlay.setAttribute('aria-hidden', 'false');
}

function showNotificationBanner(notification, microcopy, theme) {
  var refs = ensureNotificationUX();
  if (!refs.bannerStack) return;

  var banner = document.createElement('div');
  banner.className = 'ntf-banner';
  setThemeVars(banner, theme);

  var title = document.createElement('strong');
  title.textContent = String(notification.title || microcopy.eventLabel || 'Notificacao');
  banner.appendChild(title);

  var text = document.createElement('span');
  text.className = 'meta';
  text.textContent = String(notification.message || '');
  banner.appendChild(text);

  var closeBtn = document.createElement('button');
  closeBtn.type = 'button';
  closeBtn.className = 'btn btn-xs';
  closeBtn.textContent = microcopy.closeLabel;
  closeBtn.addEventListener('click', function () {
    banner.remove();
  });
  banner.appendChild(closeBtn);

  refs.bannerStack.prepend(banner);
  setTimeout(function () {
    if (banner && banner.parentElement) banner.remove();
  }, 12000);
}

function normalizeNotificationActionType(actionType) {
  var value = String(actionType || '').trim().toLowerCase();
  if (!value) return '';
  if (value === 'approve' || value === 'approved' || value === 'confirm' || value === 'accept') return 'approved';
  if (value === 'defer' || value === 'deferred' || value === 'postpone' || value === 'snooze') return 'deferred';
  if (value === 'deny' || value === 'denied' || value === 'cancel' || value === 'reject') return 'denied';
  if (value === 'open_logs') return 'open_logs';
  if (value === 'open_details') return 'open_details';
  if (value === 'restart_now' || value === 'reboot_now') return 'restart_now';
  return value;
}

function handleNotificationUtilityAction(actionType, notification) {
  if (actionType === 'open_logs') {
    setActiveTab('logs');
    loadLogs();
    return true;
  }
  if (actionType === 'open_details') {
    setActiveTab('automation');
    return true;
  }
  if (actionType === 'restart_now') {
    openRebootModal({
      title: 'Reinicio necessario',
      message: String(notification && notification.message ? notification.message : 'Reinicie o computador para concluir a instalacao.'),
      countdownSeconds: Number(notification && notification.timeoutSeconds ? notification.timeoutSeconds : 0),
      theme: resolveNotificationTheme(notification && notification.metadata ? notification.metadata : {}, 'high'),
    });
    return true;
  }
  return false;
}

function applyProgressFromNotification(notification) {
  var eventType = String(notification.eventType || '').toLowerCase();
  if (eventType.indexOf('install_') !== 0 && eventType !== 'reboot_required' && eventType !== 'restart_required') return;

  var metadata = notification.metadata && typeof notification.metadata === 'object' ? notification.metadata : {};
  var groupId = String(metadata.correlationId || metadata.executionId || 'default').trim();
  if (!groupId) groupId = 'default';

  var group = notificationUXState.progressByGroup[groupId];
  if (!group) {
    group = {
      totalTasks: Number(metadata.totalTasks || 0),
      tasks: {},
      title: 'Progresso da instalacao',
      updatedAt: Date.now(),
      failed: false,
      phase: 'precheck',
    };
    notificationUXState.progressByGroup[groupId] = group;
  }

  var taskId = String(metadata.taskId || notification.id || ('task-' + Date.now())).trim();
  if (!group.tasks[taskId]) {
    group.tasks[taskId] = {
      id: taskId,
      name: String(metadata.taskName || metadata.packageId || taskId),
      status: 'queued',
    };
  }

  var rawStatus = String(metadata.status || '').toLowerCase();
  var inferredPhase = '';
  if (rawStatus === 'pending' || rawStatus === 'queued' || rawStatus === 'scheduled') inferredPhase = 'precheck';
  if (rawStatus === 'waiting_user' || rawStatus === 'awaiting_confirmation') inferredPhase = 'fechamento_de_apps';
  if (rawStatus === 'running' || rawStatus === 'in_progress') inferredPhase = 'instalacao';
  if (rawStatus === 'completed' || rawStatus === 'done' || rawStatus === 'success') inferredPhase = 'pos_instalacao';
  if (rawStatus === 'failed' || rawStatus === 'error') inferredPhase = 'validacao';

  if (eventType === 'install_start') {
    group.tasks[taskId].status = 'running';
    group.phase = metadata.phase ? String(metadata.phase) : (inferredPhase || 'instalacao');
  } else if (eventType === 'install_end') {
    group.tasks[taskId].status = 'done';
    group.phase = metadata.phase ? String(metadata.phase) : (inferredPhase || 'pos_instalacao');
  } else if (eventType === 'install_failed') {
    group.tasks[taskId].status = 'failed';
    group.failed = true;
    group.phase = metadata.phase ? String(metadata.phase) : (inferredPhase || 'validacao');
  } else if (eventType === 'reboot_required' || eventType === 'restart_required') {
    group.phase = 'reinicio';
  }

  if (Number(metadata.totalTasks || 0) > 0) {
    group.totalTasks = Number(metadata.totalTasks);
  }
  group.updatedAt = Date.now();
  group.title = metadata.batchName ? String(metadata.batchName) : group.title;
  notificationUXState.activeProgressGroup = groupId;

  renderInstallProgressPanel();
}

function renderInstallProgressPanel() {
  var refs = ensureNotificationUX();
  var groupId = notificationUXState.activeProgressGroup;
  if (!groupId || !notificationUXState.progressByGroup[groupId]) {
    refs.progressCard.classList.add('hidden');
    return;
  }

  var group = notificationUXState.progressByGroup[groupId];
  var tasks = Object.keys(group.tasks).map(function (k) { return group.tasks[k]; });
  if (!tasks.length) {
    refs.progressCard.classList.add('hidden');
    return;
  }

  var doneCount = tasks.filter(function (t) { return t.status === 'done'; }).length;
  var failCount = tasks.filter(function (t) { return t.status === 'failed'; }).length;
  var runningCount = tasks.filter(function (t) { return t.status === 'running'; }).length;
  var totalCount = group.totalTasks > 0 ? group.totalTasks : tasks.length;
  var settledCount = doneCount + failCount;
  var percentage = Math.round((Math.min(settledCount, totalCount) / Math.max(totalCount, 1)) * 100);

  refs.progressTitle.textContent = String(group.title || 'Progresso da instalacao');
  refs.progressMeta.textContent = settledCount + '/' + totalCount + ' apps';
  refs.progressBar.style.width = String(Math.max(6, percentage)) + '%';
  refs.progressDetail.textContent = 'Fase: ' + String(group.phase || 'instalacao') + ' | Em andamento: ' + String(runningCount) + ' | Falhas: ' + String(failCount);
  refs.progressCard.classList.remove('hidden');

  if (settledCount >= totalCount && runningCount === 0) {
    setTimeout(function () {
      if (notificationUXState.activeProgressGroup === groupId) {
        refs.progressCard.classList.add('hidden');
      }
    }, 6000);
  }
}

function shouldOpenRebootPrompt(notification, metadata) {
  var eventType = String(notification.eventType || '').toLowerCase();
  if (eventType === 'reboot_required' || eventType === 'restart_required') return true;
  var exitCode = Number(metadata && metadata.exitCode);
  if (exitCode === 1641 || exitCode === 3010) return true;
  var message = String(notification.message || '').toLowerCase();
  return message.indexOf('reinici') >= 0 || message.indexOf('reboot') >= 0 || message.indexOf('restart') >= 0;
}

function handleNotificationEvent(payload) {
  var notification = payload || {};
  var id = String(notification.id || '').trim();
  var mode = normalizeNotificationModeValue(notification.mode || 'notify_only');
  var severity = normalizeNotificationSeverityValue(notification.severity || 'medium');
  var title = String(notification.title || 'Notificacao');
  var message = String(notification.message || '');
  var layout = String(notification.layout || 'toast').toLowerCase();
  var metadata = notification.metadata && typeof notification.metadata === 'object' ? notification.metadata : {};
  var eventType = String(notification.eventType || '').toLowerCase();
  var actions = Array.isArray(metadata.actions) ? metadata.actions : [];
  var microcopy = buildNotificationMicrocopy(eventType, severity);
  var theme = resolveNotificationTheme(metadata, severity);
  var brandText = theme.companyName ? (theme.companyName + ' | ' + microcopy.severityLabel) : microcopy.severityLabel;

  notification.timeoutSeconds = Number(notification.timeoutSeconds || 0);

  if (mode === 'silent') {
    return;
  }

  applyProgressFromNotification(notification);

  var toastType = severity === 'high' || severity === 'critical' ? 'error' : (severity === 'medium' ? 'warning' : 'info');
  showToast(microcopy.eventLabel + ' - ' + title + (message ? ': ' + message : ''), toastType);

  if (layout === 'banner' && mode !== 'require_confirmation') {
    showNotificationBanner(notification, microcopy, theme);
  }

  if (shouldOpenRebootPrompt(notification, metadata)) {
    openRebootModal({
      title: title || 'Reinicio necessario',
      message: message || microcopy.rebootHint,
      countdownSeconds: Number(metadata.countdownSeconds || metadata.restartCountdownSeconds || notification.timeoutSeconds || 0),
      theme: theme,
    });
  }

  if (layout === 'modal' && mode !== 'require_confirmation') {
    openNotificationModal({
      title: title,
      message: message,
      metaText: microcopy.eventLabel,
      brandText: brandText,
      timeoutSeconds: notification.timeoutSeconds,
      theme: theme,
      actions: [{
        label: microcopy.closeLabel,
        onClick: function () {
          closeNotificationModal();
        },
      }],
    });
  }

  if (mode !== 'require_confirmation' || !id) {
    return;
  }

  var decisionActions = [];
  if (actions.length > 0) {
    for (var i = 0; i < actions.length; i += 1) {
      (function (action) {
        var actionType = normalizeNotificationActionType(action && action.actionType);
        var label = String(action && action.label ? action.label : microcopy.detailsLabel);
        decisionActions.push({
          label: label,
          onClick: function () {
            if (handleNotificationUtilityAction(actionType, notification)) {
              closeNotificationModal();
              return;
            }
            if (actionType === 'approved' || actionType === 'deferred' || actionType === 'denied') {
              appApi().RespondToNotification(id, actionType).catch(function (error) {
                showToast('Falha ao enviar confirmacao: ' + String(error), 'error');
              });
              closeNotificationModal();
            }
          },
        });
      })(actions[i]);
    }
  }

  if (!decisionActions.length) {
    decisionActions = [
      {
        label: microcopy.approveLabel,
        onClick: function () {
          appApi().RespondToNotification(id, 'approved').catch(function (error) {
            showToast('Falha ao enviar confirmacao: ' + String(error), 'error');
          });
          closeNotificationModal();
        },
      },
      {
        label: microcopy.deferLabel,
        onClick: function () {
          appApi().RespondToNotification(id, 'deferred').catch(function (error) {
            showToast('Falha ao enviar confirmacao: ' + String(error), 'error');
          });
          closeNotificationModal();
        },
      },
      {
        label: microcopy.denyLabel,
        onClick: function () {
          appApi().RespondToNotification(id, 'denied').catch(function (error) {
            showToast('Falha ao enviar confirmacao: ' + String(error), 'error');
          });
          closeNotificationModal();
        },
      },
    ];
  }

  openNotificationModal({
    title: title,
    message: message,
    metaText: microcopy.eventLabel,
    brandText: brandText,
    timeoutSeconds: notification.timeoutSeconds,
    theme: theme,
    actions: decisionActions,
  });
}

