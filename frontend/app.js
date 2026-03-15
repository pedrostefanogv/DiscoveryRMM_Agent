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
const memoryOutputEl = document.getElementById('memoryOutput');
const monitorOutputEl = document.getElementById('monitorOutput');
const gpuOutputEl = document.getElementById('gpuOutput');
const batteryOutputEl = document.getElementById('batteryOutput');
const bitlockerOutputEl = document.getElementById('bitlockerOutput');
const cpuInfoOutputEl = document.getElementById('cpuInfoOutput');
const cpuidOutputEl = document.getElementById('cpuidOutput');
const startupOutputEl = document.getElementById('startupOutput');
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
const catalogPrevBtn = document.getElementById('catalogPrevBtn');
const catalogNextBtn = document.getElementById('catalogNextBtn');
const catalogPageInfoEl = document.getElementById('catalogPageInfo');
const sidebarEl = document.getElementById('sidebar');
const sidebarToggleBtn = document.getElementById('sidebarToggle');
const storeActionsEl = document.getElementById('storeActions');
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
const refreshLogsBtn = document.getElementById('refreshLogsBtn');
const clearLogsBtn = document.getElementById('clearLogsBtn');
const tabDebugBtn = document.getElementById('tabDebug');
const debugViewEl = document.getElementById('debugView');
const apiSchemeEl = document.getElementById('apiScheme');
const apiServerEl = document.getElementById('apiServer');
const natsServerEl = document.getElementById('natsServer');
const debugAuthTokenEl = document.getElementById('debugAuthToken');
const debugAgentIDEl = document.getElementById('debugAgentID');
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
const watchdogToastState = {
  lastToastAtByComponentStatus: {},
  windowStartMs: 0,
  sentInCurrentWindow: 0,
};

let inventorySoftware = [];
let inventorySoftwareFiltered = [];
let softwareSortKey = 'name';
let softwareSortDirection = 'asc';
let softwarePage = 1;
const softwarePageSize = 30;
let inventoryLoadedOnce = false;
let pendingUpdates = [];
let logsAutoRefreshId = null;
let knowledgeArticles = [];
let selectedKnowledgeArticleID = '';
let activeTab = 'status';
let runtimeFlags = {
  debugMode: false,
};

function isDebugRuntimeMode() {
  return !!runtimeFlags.debugMode;
}

function isRuntimeTabAllowed(tab) {
  if (isDebugRuntimeMode()) {
    return true;
  }
  return tab !== 'logs' && tab !== 'debug' && tab !== 'automation';
}

function applyRuntimeTabVisibility() {
  var hiddenInNormal = !isDebugRuntimeMode();

  if (tabLogsBtn) tabLogsBtn.classList.toggle('hidden', hiddenInNormal);
  if (tabDebugBtn) tabDebugBtn.classList.toggle('hidden', hiddenInNormal);
  if (tabAutomationBtn) tabAutomationBtn.classList.toggle('hidden', hiddenInNormal);

  if (hiddenInNormal) {
    if (logsViewEl) logsViewEl.classList.add('hidden');
    if (debugViewEl) debugViewEl.classList.add('hidden');
    if (automationViewEl) automationViewEl.classList.add('hidden');
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
  if (!window.go || !window.go.main || !window.go.main.App) {
    throw new Error('API do Wails indisponivel. Rode pelo wails dev/build.');
  }
  return window.go.main.App;
}

function showFeedback(message, isError) {
  feedbackEl.textContent = message;
  feedbackEl.style.color = isError ? '#9a031e' : '#665a4c';
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

function setExportStatus(message, isError) {
  exportStatusEl.textContent = message;
  exportStatusEl.classList.remove('success', 'error');
  exportStatusEl.classList.add(isError ? 'error' : 'success');
}

// ---------------------------------------------------------------------------
// Generic card-list renderer - replaces 13 nearly-identical render functions.
// targetEl:     DOM element to render into
// items:        array of data objects
// emptyMessage: string shown when items is empty/null
// cardRenderer: function(item) => HTML string for one card
// opts:         optional { maxItems: number } to cap rendering
// ---------------------------------------------------------------------------
function renderCardList(targetEl, items, emptyMessage, cardRenderer, opts) {
  if (!items || !items.length) {
    targetEl.innerHTML = '<div class="meta">' + escapeHtml(emptyMessage) + '</div>';
    return;
  }
  var list = items;
  if (opts && opts.maxItems && list.length > opts.maxItems) {
    list = list.slice(0, opts.maxItems);
  }
  targetEl.innerHTML = list.map(cardRenderer).join('');
}

function setActiveTab(tab) {
  if (!isRuntimeTabAllowed(tab)) {
    tab = 'store';
  }

  activeTab = tab;
  var views = {
    status: statusViewEl,
    store: storeViewEl,
    updates: updatesViewEl,
    inventory: inventoryViewEl,
    logs: logsViewEl,
    chat: chatViewEl,
    support: supportViewEl,
    knowledge: knowledgeViewEl,
    automation: automationViewEl,
    debug: debugViewEl,
  };
  var tabs = {
    status: tabStatusBtn,
    store: tabStoreBtn,
    updates: tabUpdatesBtn,
    inventory: tabInventoryBtn,
    logs: tabLogsBtn,
    chat: tabChatBtn,
    support: tabSupportBtn,
    knowledge: tabKnowledgeBtn,
    automation: tabAutomationBtn,
    debug: tabDebugBtn,
  };

  Object.keys(views).forEach(function (key) {
    if (views[key]) views[key].classList.toggle('hidden', key !== tab);
    if (tabs[key]) {
      tabs[key].classList.toggle('active', key === tab);
      tabs[key].setAttribute('aria-selected', String(key === tab));
    }
  });

  if (storeActionsEl) storeActionsEl.classList.toggle('hidden', tab !== 'store');

  if (tab === 'chat') {
    scheduleChatScrollToBottom();
  }

  // Stop logs auto-refresh when leaving logs tab
  if (tab !== 'logs' && logsAutoRefreshId) {
    clearInterval(logsAutoRefreshId);
    logsAutoRefreshId = null;
  }

  if (tab !== 'status' && typeof stopStatusPoll === 'function') {
    stopStatusPoll();
  }

  // Stop agent status poll when leaving debug tab
  if (tab !== 'debug') {
    stopAgentStatusPoll();
    stopWatchdogPoll();
  }

  if (tab === 'status' && typeof startStatusPoll === 'function') {
    startStatusPoll();
  }

  // Start logs auto-refresh when entering logs tab
  if (tab === 'logs') {
    loadLogs();
    if (!logsAutoRefreshId) {
      logsAutoRefreshId = setInterval(loadLogs, 3000);
    }
  }
}

// ---------------------------------------------------------------------------
// Logs tab
// ---------------------------------------------------------------------------

async function loadLogs() {
  try {
    var lines = await appApi().GetLogs();
    logsOutputEl.textContent = (lines || []).join('\n') || '(sem logs)';
    logsOutputEl.scrollTop = logsOutputEl.scrollHeight;
  } catch (_) {
    // silent - auto-refresh shouldn't spam errors
  }
}

async function clearLogs() {
  try {
    await appApi().ClearLogs();
    logsOutputEl.textContent = '(logs limpos)';
    showToast('Logs limpos', 'info');
  } catch (error) {
    showToast('Erro ao limpar logs: ' + String(error), 'error');
  }
}

// ---------------------------------------------------------------------------
// Theme toggle
// ---------------------------------------------------------------------------

function initTheme() {
  var saved = localStorage.getItem('discovery-theme');
  if (saved === 'dark') {
    document.documentElement.setAttribute('data-theme', 'dark');
    updateThemeIcon(true);
  }
}

function toggleTheme() {
  var isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  if (isDark) {
    document.documentElement.removeAttribute('data-theme');
    localStorage.setItem('discovery-theme', 'light');
  } else {
    document.documentElement.setAttribute('data-theme', 'dark');
    localStorage.setItem('discovery-theme', 'dark');
  }
  updateThemeIcon(!isDark);
}

function updateThemeIcon(isDark) {
  var themeIcon = document.getElementById('themeIcon');
  var label = themeToggleBtn ? themeToggleBtn.querySelector('span') : null;
  if (themeIcon) {
    if (isDark) {
      themeIcon.innerHTML = '<circle cx="12" cy="12" r="5"/><line x1="12" y1="1" x2="12" y2="3"/><line x1="12" y1="21" x2="12" y2="23"/><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"/><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"/><line x1="1" y1="12" x2="3" y2="12"/><line x1="21" y1="12" x2="23" y2="12"/><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"/><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"/>';
    } else {
      themeIcon.innerHTML = '<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/>';
    }
  }
  if (label) label.textContent = isDark ? 'Tema Claro' : 'Tema Escuro';
}
