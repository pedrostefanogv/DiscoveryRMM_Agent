"use strict";

// ---------------------------------------------------------------------------
// Debounce utility — prevents excessive calls on rapid events (e.g. typing)
// ---------------------------------------------------------------------------
function debounce(fn, delayMs) {
  var timeoutId;
  return function () {
    var ctx = this;
    var args = arguments;
    clearTimeout(timeoutId);
    timeoutId = setTimeout(function () { fn.apply(ctx, args); }, delayMs);
  };
}

// ---------------------------------------------------------------------------
// Pagination utility — avoids duplicating logic across catalog & software
// ---------------------------------------------------------------------------
function getPaginationState(items, currentPage, pageSize) {
  var totalPages = Math.max(1, Math.ceil(items.length / pageSize));
  var validPage = Math.max(1, Math.min(currentPage, totalPages));
  var start = (validPage - 1) * pageSize;
  return { totalPages: totalPages, validPage: validPage, start: start };
}

const VALID_ACTIONS = new Set(['install', 'uninstall', 'upgrade']);

const state = {
  allPackages: [],
  filtered: [],
  selectedCategory: '',
  categoryNames: [],
  categoryCounts: {},
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
const tabStoreBtn = document.getElementById('tabStore');
const tabUpdatesBtn = document.getElementById('tabUpdates');
const tabInventoryBtn = document.getElementById('tabInventory');
const tabLogsBtn = document.getElementById('tabLogs');
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
const supportFormEl = document.getElementById('supportForm');
const supportTicketsListEl = document.getElementById('supportTicketsList');
const chatMessagesEl = document.getElementById('chatMessages');
const chatInputEl = document.getElementById('chatInput');
const chatSendBtn = document.getElementById('chatSendBtn');
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

let inventorySoftware = [];
let inventorySoftwareFiltered = [];
let softwareSortKey = 'name';
let softwareSortDirection = 'asc';
let softwarePage = 1;
const softwarePageSize = 30;
let inventoryLoadedOnce = false;
let pendingUpdates = [];
let logsAutoRefreshId = null;

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
// Generic card-list renderer — replaces 13 nearly-identical render functions.
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

// ---------------------------------------------------------------------------
// Catalog card rendering with pagination
// ---------------------------------------------------------------------------
function renderCards() {
  if (!state.filtered.length) {
    cardsEl.innerHTML = '<div class="card"><h3>Nenhum pacote encontrado</h3><p class="meta">Ajuste o filtro de busca.</p></div>';
    updateCatalogPagination();
    return;
  }

  var pg = getPaginationState(state.filtered, catalogPage, catalogPageSize);
  catalogPage = pg.validPage;

  var start = pg.start;
  var end = start + catalogPageSize;
  var pageItems = state.filtered.slice(start, end);

  cardsEl.innerHTML = pageItems.map(function (pkg) {
    var description = pkg.description || 'Sem descricao';
    var publisher = pkg.publisher || 'Desconhecido';
    var version = pkg.version || 'N/A';
    var iconHtml = '';
    if (pkg.icon) {
      iconHtml = '<div class="app-icon-container"><img src="' + escapeHtmlAttr(pkg.icon) + '" alt="' + escapeHtmlAttr(pkg.name || pkg.id) + '" class="app-icon" /></div>';
    }

    return '<article class="card">' +
      iconHtml +
      '<h3>' + escapeHtml(pkg.name || pkg.id) + '</h3>' +
      '<div class="meta">' + escapeHtml(publisher) + ' \u2022 ' + escapeHtml(version) + '</div>' +
      '<div class="meta">ID: ' + escapeHtml(pkg.id) + '</div>' +
      '<p class="desc">' + escapeHtml(description).slice(0, 180) + '</p>' +
      '<div class="card-actions">' +
        '<button class="btn primary" data-action="install" data-id="' + escapeHtmlAttr(pkg.id) + '">Instalar</button>' +
        '<button class="btn danger" data-action="uninstall" data-id="' + escapeHtmlAttr(pkg.id) + '">Remover</button>' +
        '<button class="btn" data-action="upgrade" data-id="' + escapeHtmlAttr(pkg.id) + '">Atualizar</button>' +
      '</div>' +
    '</article>';
  }).join('');

  updateCatalogPagination();
}

function updateCatalogPagination() {
  var pg = getPaginationState(state.filtered, catalogPage, catalogPageSize);
  if (catalogPageInfoEl) catalogPageInfoEl.textContent = 'Pagina ' + catalogPage + ' de ' + pg.totalPages;
  if (catalogPrevBtn) catalogPrevBtn.disabled = catalogPage <= 1;
  if (catalogNextBtn) catalogNextBtn.disabled = catalogPage >= pg.totalPages;
}

function applyFilter() {
  var q = searchEl.value.trim().toLowerCase();
  var cat = state.selectedCategory;
  catalogPage = 1;

  state.filtered = state.allPackages.filter(function (pkg) {
    if (cat && String(pkg.category || '').toLowerCase() !== cat.toLowerCase()) return false;
    if (!q) return true;
    return [pkg.name, pkg.id, pkg.publisher, pkg.category]
      .filter(Boolean)
      .some(function (v) { return String(v).toLowerCase().includes(q); });
  });
  renderCards();
}

async function loadCatalog() {
  try {
    showFeedback('Carregando catalogo...');
    var catalog = await appApi().GetCatalog();
    state.allPackages = catalog.packages || [];
    state.filtered = state.allPackages;
    catalogPage = 1;
    infoEl.textContent = 'Pacotes: ' + (catalog.count || state.allPackages.length) + ' | Com icone: ' + (catalog.packagesWithIcon || 0);
    populateCategories();
    applyFilter();
    showFeedback('Catalogo carregado.');
  } catch (error) {
    showFeedback(String(error), true);
    infoEl.textContent = 'Falha ao carregar catalogo';
  }
}

function populateCategories() {
  if (!categoryListEl) return;
  var catCount = {};
  state.allPackages.forEach(function (pkg) {
    var c = (pkg.category || '').trim();
    if (c) catCount[c] = (catCount[c] || 0) + 1;
  });
  state.categoryNames = Object.keys(catCount).sort();
  state.categoryCounts = catCount;
  renderCategoryList('');
}

function renderCategoryList(query) {
  if (!categoryListEl) return;
  var q = (query || '').toLowerCase();
  var items = state.categoryNames || [];
  if (q) items = items.filter(function (c) { return c.toLowerCase().includes(q); });
  var html = '<li class="' + (state.selectedCategory === '' ? 'active' : '') + '" data-cat="">Todas <span class="category-count">(' + state.allPackages.length + ')</span></li>';
  html += items.map(function (c) {
    var count = state.categoryCounts[c] || 0;
    var cls = state.selectedCategory === c ? 'active' : '';
    return '<li class="' + cls + '" data-cat="' + escapeHtmlAttr(c) + '">' + escapeHtml(c) + ' <span class="category-count">(' + count + ')</span></li>';
  }).join('');
  categoryListEl.innerHTML = html;
}

async function runAction(action, id) {
  if (!id) return;
  if (!VALID_ACTIONS.has(action)) return;
  try {
    showFeedback(action + ' ' + id + '...');
    var output = '';

    if (action === 'install') output = await appApi().Install(id);
    else if (action === 'uninstall') output = await appApi().Uninstall(id);
    else if (action === 'upgrade') output = await appApi().Upgrade(id);

    showFeedback(action + ' concluido para ' + id);
    installedOutputEl.textContent = output || '(sem saida)';
  } catch (error) {
    showFeedback(String(error), true);
  }
}

async function runUpgradeAll() {
  try {
    showFeedback('Atualizando todos os apps...');
    var output = await appApi().UpgradeAll();
    showFeedback('Atualizacao geral concluida.');
    installedOutputEl.textContent = output || '(sem saida)';
  } catch (error) {
    showFeedback(String(error), true);
  }
}

async function listInstalled() {
  try {
    showFeedback('Consultando apps instalados...');
    var output = await appApi().ListInstalled();
    installedOutputEl.textContent = output || '(sem saida)';
    showFeedback('Lista de instalados atualizada.');
  } catch (error) {
    showFeedback(String(error), true);
  }
}

function setActiveTab(tab) {
  var views = {
    store: storeViewEl,
    updates: updatesViewEl,
    inventory: inventoryViewEl,
    logs: logsViewEl,
    chat: chatViewEl,
    support: supportViewEl,
  };
  var tabs = {
    store: tabStoreBtn,
    updates: tabUpdatesBtn,
    inventory: tabInventoryBtn,
    logs: tabLogsBtn,
    chat: tabChatBtn,
    support: tabSupportBtn,
  };

  Object.keys(views).forEach(function (key) {
    if (views[key]) views[key].classList.toggle('hidden', key !== tab);
    if (tabs[key]) {
      tabs[key].classList.toggle('active', key === tab);
      tabs[key].setAttribute('aria-selected', String(key === tab));
    }
  });

  if (storeActionsEl) storeActionsEl.classList.toggle('hidden', tab !== 'store');

  // Stop logs auto-refresh when leaving logs tab
  if (tab !== 'logs' && logsAutoRefreshId) {
    clearInterval(logsAutoRefreshId);
    logsAutoRefreshId = null;
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
// Inventory section renderers — using generic renderCardList
// ---------------------------------------------------------------------------

function renderFacts(target, data) {
  var entries = Object.entries(data || {});
  target.innerHTML = entries.map(function (entry) {
    return '<div class="fact">' +
      '<div class="k">' + escapeHtml(entry[0]) + '</div>' +
      '<div class="v">' + escapeHtml(entry[1] != null ? entry[1] : '-') + '</div>' +
    '</div>';
  }).join('');
}

function renderVolumes(volumes) {
  renderCardList(volumeOutputEl, volumes, 'Nenhum volume encontrado.', function (d) {
    return '<div class="disk-card">' +
      '<strong>' + escapeHtml(d.device || '-') + (d.label ? ' - ' + escapeHtml(d.label) : '') + '</strong>' +
      '<span class="meta">Tipo: ' + escapeHtml(d.type || '-') + ' | FS: ' + escapeHtml(d.fileSystem || '-') + '</span>' +
      '<span class="meta">Particao de boot: ' + (d.bootPartition ? 'sim' : 'nao') + '</span>' +
      '<span class="meta">Fabricante: ' + escapeHtml(d.manufacturer || '-') + '</span>' +
      '<span class="meta">Modelo: ' + escapeHtml(d.model || '-') + '</span>' +
      '<span class="meta">Serial: ' + escapeHtml(d.serial || '-') + '</span>' +
      '<span class="meta">Particoes: ' + escapeHtml(d.partitions != null ? d.partitions : '-') + '</span>' +
      '<span class="meta">Tamanho: ' + escapeHtml(d.sizeGB != null ? d.sizeGB : '-') + ' GB</span>' +
      '<span class="meta">Livre: ' + (d.freeKnown ? escapeHtml(d.freeGB != null ? d.freeGB : '-') + ' GB' : 'indisponivel') + '</span>' +
      '<span class="meta">Ocupado: ' + renderDiskOccupiedGB(d) + '</span>' +
      renderDiskUsageBar(d) +
      '<span class="meta">' + renderDiskUsageLabel(d) + '</span>' +
      '<span class="meta">Descricao: ' + escapeHtml(d.description || '-') + '</span>' +
    '</div>';
  });
}

function renderPhysicalDisks(disks) {
  renderCardList(physicalDiskOutputEl, disks, 'Nenhum disco fisico encontrado.', function (d) {
    return '<div class="disk-card">' +
      '<strong>' + escapeHtml(d.device || '-') + (d.label ? ' - ' + escapeHtml(d.label) : '') + '</strong>' +
      '<span class="meta">Tipo: ' + escapeHtml(d.type || '-') + '</span>' +
      '<span class="meta">Fabricante: ' + escapeHtml(d.manufacturer || '-') + '</span>' +
      '<span class="meta">Modelo: ' + escapeHtml(d.model || '-') + '</span>' +
      '<span class="meta">Serial: ' + escapeHtml(d.serial || '-') + '</span>' +
      '<span class="meta">Particoes: ' + escapeHtml(d.partitions != null ? d.partitions : '-') + '</span>' +
      '<span class="meta">Tamanho: ' + escapeHtml(d.sizeGB != null ? d.sizeGB : '-') + ' GB</span>' +
      '<span class="meta">Descricao: ' + escapeHtml(d.description || '-') + '</span>' +
    '</div>';
  });
}

function renderNetworks(networks) {
  renderCardList(networkOutputEl, networks, 'Nenhuma interface encontrada.', function (n) {
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(n.interface || '-') + (n.friendlyName ? ' - ' + escapeHtml(n.friendlyName) : '') + '</strong>' +
      '<span class="meta">MAC: ' + escapeHtml(n.mac || '-') + '</span>' +
      '<span class="meta">IPv4: ' + escapeHtml(n.ipv4 || '-') + '</span>' +
      '<span class="meta">IPv6: ' + escapeHtml(n.ipv6 || '-') + '</span>' +
      '<span class="meta">Gateway: ' + escapeHtml(n.gateway || '-') + '</span>' +
      '<span class="meta">Tipo: ' + escapeHtml(n.type || '-') + ' | MTU: ' + escapeHtml(n.mtu != null ? n.mtu : '-') + '</span>' +
      '<span class="meta">Velocidade: ' + escapeHtml(n.linkSpeedMbps != null ? n.linkSpeedMbps : '-') + ' Mb/s</span>' +
      '<span class="meta">Status: ' + escapeHtml(n.connectionStatus || '-') + ' | Habilitada: ' + (n.enabled ? 'sim' : 'nao') + '</span>' +
      '<span class="meta">Fisica: ' + (n.physicalAdapter ? 'sim' : 'nao') + ' | DHCP: ' + (n.dhcpEnabled ? 'sim' : 'nao') + '</span>' +
      '<span class="meta">DNS: ' + escapeHtml(n.dnsServers || '-') + '</span>' +
      '<span class="meta">Fabricante: ' + escapeHtml(n.manufacturer || '-') + '</span>' +
      '<span class="meta">Descricao: ' + escapeHtml(n.description || '-') + '</span>' +
    '</div>';
  }, { maxItems: 100 });
}

function renderStartupItems(items) {
  renderCardList(startupOutputEl, items, 'Nenhum startup item encontrado.', function (s) {
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(s.name || '-') + '</strong>' +
      '<span class="meta">Path: ' + escapeHtml(s.path || '-') + '</span>' +
      '<span class="meta">Args: ' + escapeHtml(s.args || '-') + '</span>' +
      '<span class="meta">Tipo: ' + escapeHtml(s.type || '-') + ' | Source: ' + escapeHtml(s.source || '-') + '</span>' +
      '<span class="meta">Status: ' + escapeHtml(s.status || '-') + ' | Usuario: ' + escapeHtml(s.username || '-') + '</span>' +
    '</div>';
  }, { maxItems: 200 });
}

function renderAutoexec(items) {
  renderCardList(autoexecOutputEl, items, 'Nenhum autoexec encontrado.', function (a) {
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(a.name || '-') + '</strong>' +
      '<span class="meta">Path: ' + escapeHtml(a.path || '-') + '</span>' +
      '<span class="meta">Source: ' + escapeHtml(a.source || '-') + '</span>' +
    '</div>';
  });
}

function renderLoggedUsers(items) {
  renderCardList(loggedUsersOutputEl, items, 'Nenhum usuario logado encontrado.', function (u) {
    var timeStr = '-';
    if (u.time && u.time > 0) {
      try { timeStr = new Date(u.time * 1000).toLocaleString(); } catch(e) { timeStr = String(u.time); }
    }
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(u.user || '-') + '</strong>' +
      '<span class="meta">Tipo: ' + escapeHtml(u.type || '-') + '</span>' +
      (u.tty ? '<span class="meta">TTY: ' + escapeHtml(u.tty) + '</span>' : '') +
      (u.host ? '<span class="meta">Host: ' + escapeHtml(u.host) + '</span>' : '') +
      (u.pid ? '<span class="meta">PID: ' + escapeHtml(String(u.pid)) + '</span>' : '') +
      (u.sid ? '<span class="meta">SID: ' + escapeHtml(u.sid) + '</span>' : '') +
      (u.registry ? '<span class="meta">Registry: ' + escapeHtml(u.registry) + '</span>' : '') +
      '<span class="meta">Logon: ' + escapeHtml(timeStr) + '</span>' +
    '</div>';
  });
}

function renderMemoryModules(items) {
  renderCardList(memoryOutputEl, items, 'Nenhum modulo de memoria encontrado.', function (m) {
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(m.slot || m.bank || 'Modulo') + '</strong>' +
      '<span class="meta">Handle: ' + escapeHtml(m.handle || '-') + ' | Array: ' + escapeHtml(m.arrayHandle || '-') + '</span>' +
      '<span class="meta">Form factor: ' + escapeHtml(m.formFactor || '-') + ' | Set: ' + escapeHtml(m.set != null ? m.set : '-') + '</span>' +
      '<span class="meta">Largura total/dados: ' + escapeHtml(m.totalWidth != null ? m.totalWidth : '-') + ' / ' + escapeHtml(m.dataWidth != null ? m.dataWidth : '-') + ' bits</span>' +
      '<span class="meta">Banco: ' + escapeHtml(m.bank || '-') + '</span>' +
      '<span class="meta">Fabricante: ' + escapeHtml(m.manufacturer || '-') + '</span>' +
      '<span class="meta">Part Number: ' + escapeHtml(m.partNumber || '-') + '</span>' +
      '<span class="meta">Asset Tag: ' + escapeHtml(m.assetTag || '-') + '</span>' +
      '<span class="meta">Serial: ' + escapeHtml(m.serial || '-') + '</span>' +
      '<span class="meta">Tamanho: ' + escapeHtml(m.sizeGB != null ? m.sizeGB : '-') + ' GB (' + escapeHtml(m.sizeMB != null ? m.sizeMB : '-') + ' MB)</span>' +
      '<span class="meta">Velocidade config/max: ' + escapeHtml(m.speedMHz != null ? m.speedMHz : '-') + ' / ' + escapeHtml(m.maxSpeedMTs != null ? m.maxSpeedMTs : '-') + ' MT/s</span>' +
      '<span class="meta">Tipo: ' + escapeHtml(m.type || '-') + ' | Detalhe: ' + escapeHtml(m.memoryTypeDetails || '-') + '</span>' +
      '<span class="meta">Voltagem min/max/config: ' + escapeHtml(m.minVoltageMV != null ? m.minVoltageMV : '-') + ' / ' + escapeHtml(m.maxVoltageMV != null ? m.maxVoltageMV : '-') + ' / ' + escapeHtml(m.configuredVoltageMV != null ? m.configuredVoltageMV : '-') + ' mV</span>' +
    '</div>';
  });
}

function renderMonitors(items) {
  renderCardList(monitorOutputEl, items, 'Nenhum monitor detectado.', function (m) {
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(m.name || 'Monitor') + '</strong>' +
      '<span class="meta">Fabricante: ' + escapeHtml(m.manufacturer || '-') + '</span>' +
      '<span class="meta">Serial: ' + escapeHtml(m.serial || '-') + '</span>' +
      '<span class="meta">Resolucao: ' + escapeHtml(m.resolution || '-') + '</span>' +
      '<span class="meta">Status: ' + escapeHtml(m.status || '-') + '</span>' +
    '</div>';
  });
}

function renderGPUs(items) {
  renderCardList(gpuOutputEl, items, 'Nenhuma GPU detectada.', function (g) {
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(g.name || 'GPU') + '</strong>' +
      '<span class="meta">Fabricante: ' + escapeHtml(g.manufacturer || '-') + '</span>' +
      '<span class="meta">Driver: ' + escapeHtml(g.driverVersion || '-') + '</span>' +
      '<span class="meta">VRAM: ' + escapeHtml(g.vramGB != null ? g.vramGB : '-') + ' GB</span>' +
      '<span class="meta">Status: ' + escapeHtml(g.status || '-') + '</span>' +
    '</div>';
  });
}

function renderBattery(items) {
  renderCardList(batteryOutputEl, items, 'Nenhuma bateria detectada.', function (b) {
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(b.model || 'Bateria') + '</strong>' +
      '<span class="meta">Fabricante: ' + escapeHtml(b.manufacturer || '-') + '</span>' +
      '<span class="meta">Serial: ' + escapeHtml(b.serialNumber || '-') + '</span>' +
      '<span class="meta">Estado: ' + escapeHtml(b.state || '-') + ' | Charging: ' + (b.charging ? 'sim' : 'nao') + ' | Charged: ' + (b.charged ? 'sim' : 'nao') + '</span>' +
      '<span class="meta">Ciclos: ' + escapeHtml(b.cycleCount != null ? b.cycleCount : '-') + ' | Restante: ' + escapeHtml(b.percentRemaining != null ? b.percentRemaining : '-') + '%</span>' +
      '<span class="meta">Capacidade mAh (design/max/atual): ' + escapeHtml(b.designedCapacityMAh != null ? b.designedCapacityMAh : '-') + ' / ' + escapeHtml(b.maxCapacityMAh != null ? b.maxCapacityMAh : '-') + ' / ' + escapeHtml(b.currentCapacityMAh != null ? b.currentCapacityMAh : '-') + '</span>' +
      '<span class="meta">Corrente/Voltagem: ' + escapeHtml(b.amperageMA != null ? b.amperageMA : '-') + ' mA / ' + escapeHtml(b.voltageMV != null ? b.voltageMV : '-') + ' mV</span>' +
      '<span class="meta">Tempo: vazio ' + escapeHtml(b.minutesUntilEmpty != null ? b.minutesUntilEmpty : '-') + ' min | carga total ' + escapeHtml(b.minutesToFullCharge != null ? b.minutesToFullCharge : '-') + ' min</span>' +
      '<span class="meta">Quimica: ' + escapeHtml(b.chemistry || '-') + ' | Saude: ' + escapeHtml(b.health || '-') + ' | Condicao: ' + escapeHtml(b.condition || '-') + '</span>' +
    '</div>';
  });
}

function renderBitLocker(items) {
  renderCardList(bitlockerOutputEl, items, 'Nenhum volume BitLocker encontrado.', function (b) {
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(b.driveLetter || b.deviceId || 'Volume') + '</strong>' +
      '<span class="meta">Device ID: ' + escapeHtml(b.deviceId || '-') + '</span>' +
      '<span class="meta">Persistent Volume ID: ' + escapeHtml(b.persistentVolumeId || '-') + '</span>' +
      '<span class="meta">Metodo: ' + escapeHtml(b.encryptionMethod || '-') + ' | Versao: ' + escapeHtml(b.version != null ? b.version : '-') + '</span>' +
      '<span class="meta">Criptografado: ' + escapeHtml(b.percentageEncrypted != null ? b.percentageEncrypted : '-') + '%</span>' +
      '<span class="meta">Protection/Conversion/Lock: ' + escapeHtml(b.protectionStatus != null ? b.protectionStatus : '-') + ' / ' + escapeHtml(b.conversionStatus != null ? b.conversionStatus : '-') + ' / ' + escapeHtml(b.lockStatus != null ? b.lockStatus : '-') + '</span>' +
    '</div>';
  });
}

function renderCPUInfo(items) {
  renderCardList(cpuInfoOutputEl, items, 'Nenhum dado de CPU encontrado.', function (c) {
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(c.model || 'CPU') + '</strong>' +
      '<span class="meta">Fabricante: ' + escapeHtml(c.manufacturer || '-') + ' | Socket: ' + escapeHtml(c.socketDesignation || '-') + '</span>' +
      '<span class="meta">Cores/logicos: ' + escapeHtml(c.numberOfCores != null ? c.numberOfCores : '-') + ' / ' + escapeHtml(c.logicalProcessors != null ? c.logicalProcessors : '-') + '</span>' +
      '<span class="meta">Clock atual/max: ' + escapeHtml(c.currentClockSpeed != null ? c.currentClockSpeed : '-') + ' / ' + escapeHtml(c.maxClockSpeed != null ? c.maxClockSpeed : '-') + ' MHz</span>' +
      '<span class="meta">Carga: ' + escapeHtml(c.loadPercentage != null ? c.loadPercentage : '-') + '% | Status: ' + escapeHtml(c.cpuStatus != null ? c.cpuStatus : '-') + ' | Disponibilidade: ' + escapeHtml(c.availability || '-') + '</span>' +
      '<span class="meta">Address width: ' + escapeHtml(c.addressWidth != null ? c.addressWidth : '-') + ' | Tipo processador: ' + escapeHtml(c.processorType || '-') + '</span>' +
      '<span class="meta">Efficiency/Performance cores: ' + escapeHtml(c.numberOfEfficiencyCores != null ? c.numberOfEfficiencyCores : '-') + ' / ' + escapeHtml(c.numberOfPerformanceCores != null ? c.numberOfPerformanceCores : '-') + '</span>' +
    '</div>';
  });
}

function renderCPUFeatures(items) {
  renderCardList(cpuidOutputEl, items, 'Nenhuma feature CPUID encontrada.', function (f) {
    return '<div class="network-card">' +
      '<strong>' + escapeHtml(f.feature || '-') + '</strong>' +
      '<span class="meta">Valor: ' + escapeHtml(f.value || '-') + '</span>' +
      '<span class="meta">Register/Bit: ' + escapeHtml(f.outputRegister || '-') + ' / ' + escapeHtml(f.outputBit != null ? f.outputBit : '-') + '</span>' +
      '<span class="meta">Input EAX: ' + escapeHtml(f.inputEAX || '-') + '</span>' +
    '</div>';
  }, { maxItems: 200 });
}

function setInventoryLoading(isLoading) {
  inventoryProgressEl.classList.toggle('hidden', !isLoading);
  var buttons = [refreshInventoryBtn, exportInventoryBtn, exportInventoryPdfBtn];
  buttons.forEach(function (btn) {
    btn.disabled = isLoading;
    btn.setAttribute('aria-busy', String(isLoading));
  });
}

function renderSoftwareTable() {
  if (!inventorySoftwareFiltered.length) {
    softwareTableBodyEl.innerHTML = '<tr><td colspan="6">Nenhum software encontrado.</td></tr>';
    softwareCountEl.textContent = 'Total visivel: 0';
    softwarePageInfoEl.textContent = 'Pagina 1 de 1';
    softwarePrevBtn.disabled = true;
    softwareNextBtn.disabled = true;
    return;
  }

  var pg = getPaginationState(inventorySoftwareFiltered, softwarePage, softwarePageSize);
  softwarePage = pg.validPage;
  var start = pg.start;
  var end = start + softwarePageSize;
  var pageItems = inventorySoftwareFiltered.slice(start, end);

  softwareTableBodyEl.innerHTML = pageItems.map(function (s) {
    return '<tr>' +
      '<td>' + escapeHtml(s.name || '-') + '</td>' +
      '<td>' + escapeHtml(s.version || '-') + '</td>' +
      '<td>' + escapeHtml(s.publisher || '-') + '</td>' +
      '<td>' + escapeHtml(s.installId || '-') + '</td>' +
      '<td>' + escapeHtml(s.serial || '-') + '</td>' +
      '<td>' + escapeHtml(s.source || '-') + '</td>' +
    '</tr>';
  }).join('');

  softwareCountEl.textContent = 'Total visivel: ' + inventorySoftwareFiltered.length + ' | Total inventario: ' + inventorySoftware.length;
  softwarePageInfoEl.textContent = 'Pagina ' + softwarePage + ' de ' + pg.totalPages;
  softwarePrevBtn.disabled = softwarePage <= 1;
  softwareNextBtn.disabled = softwarePage >= pg.totalPages;
}

function applySoftwareFilter() {
  var q = softwareSearchInputEl.value.trim().toLowerCase();
  if (!q) {
    inventorySoftwareFiltered = inventorySoftware;
  } else {
    inventorySoftwareFiltered = inventorySoftware.filter(function (s) {
      return (s.name && String(s.name).toLowerCase().includes(q)) ||
             (s.version && String(s.version).toLowerCase().includes(q)) ||
             (s.publisher && String(s.publisher).toLowerCase().includes(q)) ||
             (s.installId && String(s.installId).toLowerCase().includes(q)) ||
             (s.serial && String(s.serial).toLowerCase().includes(q)) ||
             (s.source && String(s.source).toLowerCase().includes(q));
    });
  }
  softwarePage = 1;
  sortSoftware();
  renderSoftwareTable();
}

function sortSoftware() {
  var key = softwareSortKey;
  var dir = softwareSortDirection === 'asc' ? 1 : -1;

  inventorySoftwareFiltered.sort(function (a, b) {
    var av = String(a[key] || '').toLowerCase();
    var bv = String(b[key] || '').toLowerCase();
    if (av < bv) return -1 * dir;
    if (av > bv) return 1 * dir;
    return 0;
  });
}

function updateSortIndicators() {
  document.querySelectorAll('.software-table th.sortable').forEach(function (th) {
    th.classList.remove('asc', 'desc');
    var key = th.dataset.sortKey;
    if (key === softwareSortKey) {
      th.classList.add(softwareSortDirection);
    }
  });
}

function toggleSort(key) {
  if (softwareSortKey === key) {
    softwareSortDirection = softwareSortDirection === 'asc' ? 'desc' : 'asc';
  } else {
    softwareSortKey = key;
    softwareSortDirection = 'asc';
  }

  sortSoftware();
  softwarePage = 1;
  updateSortIndicators();
  renderSoftwareTable();
}

async function loadInventory(forceRefresh) {
  try {
    setInventoryLoading(true);
    inventoryInfoEl.textContent = 'Coletando inventario...';
    showFeedback('Coletando inventario...');
    var report = forceRefresh ? await appApi().RefreshInventory() : await appApi().GetInventory();

    inventoryInfoEl.textContent = 'Coletado em ' + (report.collectedAt || '-') + ' via ' + (report.source || '-');
    renderFacts(hardwareOutputEl, report.hardware);
    renderFacts(osOutputEl, report.os);
    renderLoggedUsers(report.loggedInUsers || []);
    renderVolumes(report.volumes || report.disks || []);
    renderPhysicalDisks(report.physicalDisks || []);
    renderNetworks(report.networks || []);
    renderMemoryModules(report.memoryModules || []);
    renderMonitors(report.monitors || []);
    renderGPUs(report.gpus || []);
    renderBattery(report.battery || []);
    renderBitLocker(report.bitLocker || []);
    renderCPUInfo(report.cpuInfo || []);
    renderCPUFeatures(report.cpuFeatures || []);
    renderStartupItems(report.startupItems || []);
    renderAutoexec(report.autoexec || []);

    inventorySoftware = report.software || [];
    inventorySoftwareFiltered = inventorySoftware;
    sortSoftware();
    softwarePage = 1;
    updateSortIndicators();
    renderSoftwareTable();
    inventoryLoadedOnce = true;

    showFeedback('Inventario atualizado.');
    loadSidebarUser();
  } catch (error) {
    showFeedback(String(error), true);
    inventoryInfoEl.textContent = 'Falha ao coletar inventario';
  } finally {
    setInventoryLoading(false);
  }
}

async function loadSidebarUser() {
  var el = document.getElementById('sidebarUser');
  var nameEl = document.getElementById('sidebarUserName');
  var typeEl = document.getElementById('sidebarUserType');
  if (!el || !nameEl || !typeEl) return;

  try {
    var report = await appApi().GetInventory();
    var users = report.loggedInUsers || [];
    if (users.length === 0) return;

    // Pick the first interactive/console user, fallback to first entry
    var user = users.find(function (u) {
      return u.type === 'interactive' || u.type === 'console' || u.type === 'cached_interactive';
    }) || users[0];

    nameEl.textContent = user.user || '-';
    var typeMap = {
      'interactive': 'Sessao local',
      'console': 'Console',
      'remote_interactive': 'Acesso remoto',
      'cached_interactive': 'Sessao em cache',
      'remote': 'Remoto'
    };
    typeEl.textContent = typeMap[user.type] || user.type || '';
    el.classList.remove('hidden');
  } catch (e) {
    // Silently ignore — sidebar user is non-critical
  }
}

async function exportInventory() {
  try {
    showFeedback('Exportando inventario em Markdown...');
    setExportStatus('Exportacao Markdown em andamento...');
    exportInventoryBtn.disabled = true;
    var path = await appApi().ExportInventoryMarkdown();
    path = String(path || '').trim();
    if (!path) {
      showFeedback('Nenhum arquivo retornado do servidor', true);
      setExportStatus('Falha: nenhum caminho retornado', true);
      return;
    }
    showFeedback('Markdown exportado: ' + path);
    setExportStatus('Markdown criado com sucesso em: ' + path);
  } catch (error) {
    showFeedback(String(error), true);
    setExportStatus('Falha ao exportar Markdown: ' + String(error), true);
  } finally {
    exportInventoryBtn.disabled = false;
  }
}

async function exportInventoryPdf() {
  try {
    showFeedback('Exportando inventario em PDF...');
    setExportStatus('Exportacao PDF em andamento...');
    exportInventoryPdfBtn.disabled = true;
    var path = await appApi().ExportInventoryPDF();
    path = String(path || '').trim();
    if (!path) {
      showFeedback('Nenhum arquivo retornado do servidor', true);
      setExportStatus('Falha: nenhum caminho retornado', true);
      return;
    }
    showFeedback('PDF exportado: ' + path);
    setExportStatus('PDF criado com sucesso em: ' + path);
  } catch (error) {
    showFeedback(String(error), true);
    setExportStatus('Falha ao exportar PDF: ' + String(error), true);
  } finally {
    exportInventoryPdfBtn.disabled = false;
  }
}

async function refreshOsqueryStatus() {
  try {
    var status = await appApi().GetOsqueryStatus();
    if (status.installed) {
      osqueryStatusEl.textContent = 'osquery: instalado (' + (status.path || 'path desconhecido') + ')';
      installOsqueryBtn.classList.add('hidden');
      return;
    }

    osqueryStatusEl.textContent = 'osquery: nao detectado (pacote: ' + (status.suggestedPackageID || 'osquery.osquery') + ')';
    installOsqueryBtn.classList.remove('hidden');
  } catch (error) {
    osqueryStatusEl.textContent = 'osquery: erro ao verificar (' + String(error) + ')';
    installOsqueryBtn.classList.remove('hidden');
  }
}

async function installOsquery() {
  try {
    showFeedback('Instalando osquery via winget...');
    installOsqueryBtn.disabled = true;
    var output = await appApi().InstallOsquery();
    installedOutputEl.textContent = output || '(sem saida)';
    showFeedback('Instalacao do osquery concluida.');
    await refreshOsqueryStatus();
  } catch (error) {
    showFeedback(String(error), true);
  } finally {
    installOsqueryBtn.disabled = false;
  }
}

function escapeHtml(value) {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#039;');
}

function escapeHtmlAttr(value) {
  return escapeHtml(value).replaceAll('`', '');
}

function getDiskUsagePercent(disk) {
  if (!disk.freeKnown) return null;
  var size = Number(disk.sizeGB || 0);
  var free = Number(disk.freeGB || 0);
  if (!Number.isFinite(size) || size <= 0) return 0;
  var used = Math.max(0, size - free);
  var pct = Math.round((used / size) * 100);
  return Math.min(100, Math.max(0, pct));
}

function renderDiskUsageBar(disk) {
  var usage = getDiskUsagePercent(disk);
  if (usage === null) return '';
  return '<div class="disk-bar"><span style="width: ' + escapeHtmlAttr(String(usage)) + '%"></span></div>';
}

function renderDiskUsageLabel(disk) {
  var usage = getDiskUsagePercent(disk);
  if (usage === null) return 'Uso: indisponivel';
  var freePct = 100 - usage;
  return 'Uso: ' + usage + '% | Livre: ' + freePct + '%';
}

function renderDiskOccupiedGB(disk) {
  if (!disk.freeKnown) return 'indisponivel';
  var size = Number(disk.sizeGB || 0);
  var free = Number(disk.freeGB || 0);
  if (!Number.isFinite(size) || !Number.isFinite(free) || size <= 0) return 'indisponivel';
  var occupied = Math.max(0, size - free);
  return occupied.toFixed(2) + ' GB';
}

// ---------------------------------------------------------------------------
// Updates tab
// ---------------------------------------------------------------------------

async function checkPendingUpdates() {
  try {
    updatesProgressEl.classList.remove('hidden');
    updatesInfoEl.textContent = 'Verificando...';
    checkUpdatesBtn.disabled = true;
    pendingUpdates = (await appApi().GetPendingUpdates()) || [];
    updatesInfoEl.textContent = pendingUpdates.length + ' atualizacao(oes) disponivel(is)';
    renderUpdatesTable();
    if (pendingUpdates.length > 0) {
      showToast(pendingUpdates.length + ' atualizacao(oes) encontrada(s)', 'success');
    } else {
      showToast('Nenhuma atualizacao pendente', 'info');
    }
  } catch (error) {
    showFeedback(String(error), true);
    updatesInfoEl.textContent = 'Erro ao verificar atualizacoes';
  } finally {
    updatesProgressEl.classList.add('hidden');
    checkUpdatesBtn.disabled = false;
  }
}

function renderUpdatesTable() {
  if (!pendingUpdates.length) {
    updatesTableBodyEl.innerHTML = '<tr><td colspan="7" class="meta">Nenhuma atualizacao pendente.</td></tr>';
    upgradeSelectedBtn.disabled = true;
    if (updateSelectAllEl) updateSelectAllEl.checked = false;
    return;
  }
  updatesTableBodyEl.innerHTML = pendingUpdates.map(function (u, i) {
    return '<tr>' +
      '<td class="update-check-col"><input type="checkbox" class="update-check" data-idx="' + i + '" data-id="' + escapeHtmlAttr(u.id) + '" /></td>' +
      '<td>' + escapeHtml(u.name || '-') + '</td>' +
      '<td>' + escapeHtml(u.id || '-') + '</td>' +
      '<td>' + escapeHtml(u.currentVersion || '-') + '</td>' +
      '<td>' + escapeHtml(u.availableVersion || '-') + '</td>' +
      '<td>' + escapeHtml(u.source || '-') + '</td>' +
      '<td><button class="btn primary" data-action="upgrade" data-id="' + escapeHtmlAttr(u.id) + '">Atualizar</button></td>' +
    '</tr>';
  }).join('');
  updateUpgradeSelectedState();
}

function updateUpgradeSelectedState() {
  var checked = document.querySelectorAll('.update-check:checked');
  upgradeSelectedBtn.disabled = checked.length === 0;
}

async function upgradeSelected() {
  var checked = document.querySelectorAll('.update-check:checked');
  if (!checked.length) return;
  var ids = Array.from(checked).map(function (cb) { return cb.dataset.id; });
  upgradeSelectedBtn.disabled = true;
  for (var i = 0; i < ids.length; i++) {
    try {
      showToast('Atualizando ' + ids[i] + '...', 'info');
      await appApi().Upgrade(ids[i]);
      showToast(ids[i] + ' atualizado com sucesso', 'success');
    } catch (error) {
      showToast('Erro ao atualizar ' + ids[i] + ': ' + String(error), 'error');
    }
  }
  showToast('Atualizacao em lote concluida', 'success');
  checkPendingUpdates();
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

// ---------------------------------------------------------------------------
// Event listeners
// ---------------------------------------------------------------------------

cardsEl.addEventListener('click', function (event) {
  var target = event.target;
  if (!(target instanceof HTMLButtonElement)) return;

  var action = target.dataset.action;
  var id = target.dataset.id;
  if (!action || !id) return;

  runAction(action, id);
});

searchEl.addEventListener('input', debounce(applyFilter, 300));
reloadBtn.addEventListener('click', loadCatalog);
upgradeAllBtn.addEventListener('click', runUpgradeAll);
installedBtn.addEventListener('click', listInstalled);
tabStoreBtn.addEventListener('click', function () { setActiveTab('store'); });
tabUpdatesBtn.addEventListener('click', function () { setActiveTab('updates'); });
tabInventoryBtn.addEventListener('click', function () {
  setActiveTab('inventory');
  refreshOsqueryStatus();
  if (!inventoryLoadedOnce) {
    loadInventory();
  }
});
tabLogsBtn.addEventListener('click', function () { setActiveTab('logs'); });
if (tabChatBtn) {
  tabChatBtn.addEventListener('click', function () { setActiveTab('chat'); loadChatConfig(); });
}
if (tabSupportBtn) {
  tabSupportBtn.addEventListener('click', function () { setActiveTab('support'); loadSupportTickets(); });
}

// Category filter (searchable list)
if (categorySearchEl) {
  categorySearchEl.addEventListener('input', debounce(function () {
    renderCategoryList(categorySearchEl.value);
  }, 200));
}
if (categoryListEl) {
  categoryListEl.addEventListener('click', function (e) {
    var li = e.target.closest('li');
    if (!li || li.dataset.cat === undefined) return;
    state.selectedCategory = li.dataset.cat;
    renderCategoryList(categorySearchEl ? categorySearchEl.value : '');
    applyFilter();
  });
}

// Theme toggle
if (themeToggleBtn) {
  themeToggleBtn.addEventListener('click', toggleTheme);
}

// Updates tab
if (checkUpdatesBtn) checkUpdatesBtn.addEventListener('click', checkPendingUpdates);
if (upgradeSelectedBtn) upgradeSelectedBtn.addEventListener('click', upgradeSelected);

if (updateSelectAllEl) {
  updateSelectAllEl.addEventListener('change', function () {
    var cbs = document.querySelectorAll('.update-check');
    cbs.forEach(function (cb) { cb.checked = updateSelectAllEl.checked; });
    updateUpgradeSelectedState();
  });
}

if (updatesTableBodyEl) {
  updatesTableBodyEl.addEventListener('change', function (e) {
    if (e.target.classList.contains('update-check')) {
      updateUpgradeSelectedState();
      // Sync select-all state
      var all = document.querySelectorAll('.update-check');
      var checked = document.querySelectorAll('.update-check:checked');
      if (updateSelectAllEl) updateSelectAllEl.checked = all.length > 0 && all.length === checked.length;
    }
  });
  // Upgrade individual from updates table
  updatesTableBodyEl.addEventListener('click', function (e) {
    var btn = e.target;
    if (btn instanceof HTMLButtonElement && btn.dataset.action === 'upgrade' && btn.dataset.id) {
      runAction('upgrade', btn.dataset.id);
    }
  });
}

// Logs tab
if (refreshLogsBtn) refreshLogsBtn.addEventListener('click', loadLogs);
if (clearLogsBtn) clearLogsBtn.addEventListener('click', clearLogs);

// Sidebar toggle
if (sidebarToggleBtn && sidebarEl) {
  sidebarToggleBtn.addEventListener('click', function () {
    sidebarEl.classList.toggle('collapsed');
  });
}
refreshInventoryBtn.addEventListener('click', function () { loadInventory(true); });
installOsqueryBtn.addEventListener('click', installOsquery);
exportInventoryBtn.addEventListener('click', exportInventory);
exportInventoryPdfBtn.addEventListener('click', exportInventoryPdf);
softwareSearchInputEl.addEventListener('input', debounce(applySoftwareFilter, 300));
softwarePrevBtn.addEventListener('click', function () {
  softwarePage -= 1;
  renderSoftwareTable();
});
softwareNextBtn.addEventListener('click', function () {
  softwarePage += 1;
  renderSoftwareTable();
});

if (catalogPrevBtn) {
  catalogPrevBtn.addEventListener('click', function () {
    catalogPage -= 1;
    renderCards();
  });
}
if (catalogNextBtn) {
  catalogNextBtn.addEventListener('click', function () {
    catalogPage += 1;
    renderCards();
  });
}

if (redactToggleEl) {
  redactToggleEl.addEventListener('change', function () {
    try {
      appApi().SetExportRedaction(redactToggleEl.checked);
    } catch (_) {
      // API not ready — ignore
    }
  });
}

// Event delegation for sortable table headers — survives DOM rebuilds
(function () {
  var thead = document.querySelector('.software-table thead');
  if (thead) {
    thead.addEventListener('click', function (e) {
      var th = e.target.closest('th.sortable');
      if (th && th.dataset.sortKey) {
        toggleSort(th.dataset.sortKey);
      }
    });
  }
})();

updateSortIndicators();

// =========================================================================
// CHAT AI
// =========================================================================

var chatSending = false;

function addChatMessage(role, content) {
  if (!chatMessagesEl) return;
  var div = document.createElement('div');
  div.className = 'chat-msg ' + role;
  div.textContent = content;
  chatMessagesEl.appendChild(div);
  chatMessagesEl.scrollTop = chatMessagesEl.scrollHeight;
  return div;
}

function removeChatThinking() {
  if (!chatMessagesEl) return;
  var thinking = chatMessagesEl.querySelector('.chat-msg.thinking');
  if (thinking) thinking.remove();
}

async function sendChatMessage() {
  if (chatSending || !chatInputEl) return;
  var text = chatInputEl.value.trim();
  if (!text) return;

  chatInputEl.value = '';
  addChatMessage('user', text);

  chatSending = true;
  if (chatSendBtn) chatSendBtn.disabled = true;
  addChatMessage('thinking', 'Pensando...');

  try {
    var reply = await appApi().SendChatMessage(text);
    removeChatThinking();
    addChatMessage('assistant', reply || '(sem resposta)');
  } catch (err) {
    removeChatThinking();
    addChatMessage('assistant', 'Erro: ' + String(err));
  } finally {
    chatSending = false;
    if (chatSendBtn) chatSendBtn.disabled = false;
    if (chatInputEl) chatInputEl.focus();
  }
}

async function loadChatConfig() {
  try {
    var cfg = await appApi().GetChatConfig();
    if (chatEndpointEl && cfg.endpoint) chatEndpointEl.value = cfg.endpoint;
    if (chatModelEl && cfg.model) chatModelEl.value = cfg.model;
    // Don't set API key — it's masked
  } catch (_) {}
}

async function saveChatConfig() {
  var endpoint = chatEndpointEl ? chatEndpointEl.value.trim() : '';
  var apiKey = chatApiKeyEl ? chatApiKeyEl.value.trim() : '';
  var model = chatModelEl ? chatModelEl.value.trim() : '';

  if (!endpoint || !apiKey || !model) {
    showFeedback('Preencha todos os campos de configuracao', true);
    return;
  }

  try {
    await appApi().SetChatConfig({ endpoint: endpoint, apiKey: apiKey, model: model });
    showFeedback('Configuracao de IA salva com sucesso');
    if (chatConfigPanel) chatConfigPanel.classList.add('hidden');
  } catch (err) {
    showFeedback('Erro ao salvar configuracao: ' + String(err), true);
  }
}

async function testChatConfig() {
  var endpoint = chatEndpointEl ? chatEndpointEl.value.trim() : '';
  var apiKey = chatApiKeyEl ? chatApiKeyEl.value.trim() : '';
  var model = chatModelEl ? chatModelEl.value.trim() : '';

  if (!endpoint || !apiKey || !model) {
    showFeedback('Preencha todos os campos antes de testar', true);
    return;
  }

  if (chatTestConfigBtn) chatTestConfigBtn.disabled = true;
  try {
    showFeedback('Testando configuracao de IA...');
    var reply = await appApi().TestChatConfig({ endpoint: endpoint, apiKey: apiKey, model: model });
    var normalized = String(reply || '').trim();
    showFeedback('Teste concluido com sucesso' + (normalized ? ': ' + normalized : ''));
  } catch (err) {
    showFeedback('Falha no teste da configuracao: ' + String(err), true);
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
    chatToolsList.innerHTML = '<span class="meta">Erro ao carregar ferramentas</span>';
  }
}

async function loadChatDebugLogs() {
  if (!chatLogsOutput) return;
  try {
    var lines = await appApi().GetLogs();
    var chatLines = (lines || []).filter(function (line) {
      return String(line).startsWith('[chat]');
    });
    chatLogsOutput.textContent = chatLines.length ? chatLines.join('\n') : '(sem logs de chat ainda)';
    chatLogsOutput.scrollTop = chatLogsOutput.scrollHeight;
  } catch (err) {
    chatLogsOutput.textContent = 'Erro ao carregar logs: ' + String(err);
  }
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
  if (chatClearBtn) {
    chatClearBtn.addEventListener('click', async function () {
      try {
        await appApi().ClearChatHistory();
        if (chatMessagesEl) chatMessagesEl.innerHTML = '';
        showFeedback('Chat limpo');
      } catch (err) {
        showFeedback('Erro: ' + String(err), true);
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

initTheme();
setActiveTab('store');
loadCatalog();
loadSidebarUser();
initChat();
initSupport();

// =========================================================================
// SUPPORT TICKETS
// =========================================================================

function initSupport() {
  if (!supportFormEl) return;
  supportFormEl.addEventListener('submit', async function (e) {
    e.preventDefault();
    var subject = document.getElementById('ticketSubject').value.trim();
    var category = document.getElementById('ticketCategory').value;
    var priority = document.getElementById('ticketPriority').value;
    var description = document.getElementById('ticketDescription').value.trim();

    if (!subject || !category || !description) {
      showToast('Preencha todos os campos obrigatorios', 'error');
      return;
    }

    try {
      var ticket = await appApi().CreateSupportTicket({
        subject: subject,
        category: category,
        priority: priority,
        description: description,
      });
      showToast('Chamado ' + ticket.id + ' criado com sucesso!', 'success');
      supportFormEl.reset();
      loadSupportTickets();
    } catch (err) {
      showToast('Erro ao criar chamado: ' + String(err), 'error');
    }
  });
}

async function loadSupportTickets() {
  if (!supportTicketsListEl) return;
  try {
    var tickets = await appApi().GetSupportTickets();
    if (!tickets || !tickets.length) {
      supportTicketsListEl.innerHTML = '<div class="meta">Nenhum chamado aberto.</div>';
      return;
    }
    supportTicketsListEl.innerHTML = tickets.map(function (t) {
      var priorityClass = 'priority-' + (t.priority || 'media').toLowerCase();
      return '<div class="support-ticket-card">' +
        '<div class="ticket-header">' +
          '<span class="ticket-id">' + escapeHtml(t.id) + '</span>' +
          '<span class="ticket-status badge-open">' + escapeHtml(t.status) + '</span>' +
          '<span class="ticket-priority ' + priorityClass + '">' + escapeHtml(t.priority) + '</span>' +
        '</div>' +
        '<div class="ticket-subject">' + escapeHtml(t.subject) + '</div>' +
        '<div class="ticket-meta">' +
          '<span>Categoria: ' + escapeHtml(t.category) + '</span>' +
          '<span>Aberto em: ' + escapeHtml(t.createdAt) + '</span>' +
        '</div>' +
        '<div class="ticket-desc">' + escapeHtml(t.description) + '</div>' +
      '</div>';
    }).join('');
  } catch (err) {
    supportTicketsListEl.innerHTML = '<div class="meta">Erro ao carregar chamados.</div>';
  }
}
