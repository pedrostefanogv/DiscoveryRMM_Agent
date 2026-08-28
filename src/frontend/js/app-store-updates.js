"use strict";

var storeCatalogDirty = false;
var lastStoreUpdateToastAt = 0;

function handleStoreTabActivated() {
  if (!storeCatalogDirty) return;
  storeCatalogDirty = false;
  loadCatalog();
}

function onStoreCatalogUpdated(data) {
  storeCatalogDirty = true;

  if (activeTab === 'store' && !window.__discoveryUISuspended && !document.hidden) {
    storeCatalogDirty = false;
    loadCatalog();
    showToast(translate('store.synced', { variant: data && data.variant ? ' (' + data.variant + ')' : '' }), 'info');
    lastStoreUpdateToastAt = Date.now();
    return;
  }

  // Throttle: evita toast repetido em menos de 5 minutos
  var now = Date.now();
  if (now - lastStoreUpdateToastAt < 5 * 60 * 1000) {
    return;
  }

  showToast(translate('store.newDataSync'), 'info');
  lastStoreUpdateToastAt = now;
}

(function registerStoreSyncEvents() {
  function doRegister() {
    if (window.wails && typeof window.wails.on === 'function') {
      window.wails.on('store:catalog-updated', onStoreCatalogUpdated);
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', doRegister);
  } else {
    setTimeout(doRegister, 200);
  }
})();

// ---------------------------------------------------------------------------
// Strip basic Markdown syntax for clean card descriptions
// ---------------------------------------------------------------------------
function stripMarkdown(text) {
  if (!text) return '';
  return String(text)
    .replace(/^#{1,6}\s+/gm, '')
    .replace(/\*\*(.+?)\*\*/g, '$1')
    .replace(/__([^_]+)__/g, '$1')
    .replace(/\*([^*]+)\*/g, '$1')
    .replace(/_([^_]+)_/g, '$1')
    .replace(/!\[([^\]]*)\]\([^)]+\)/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
    .replace(/`([^`]+)`/g, '$1')
    .replace(/^[-*+]\s+/gm, '')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

function getStoreSearchTerms() {
  var query = searchEl ? String(searchEl.value || '') : '';
  if (!query.trim()) return [];

  var parts = query
    .toLowerCase()
    .split(/\s+/)
    .map(function (part) { return part.trim(); })
    .filter(Boolean);

  var unique = [];
  var seen = Object.create(null);
  parts.forEach(function (part) {
    if (seen[part]) return;
    seen[part] = true;
    unique.push(part);
  });

  return unique;
}

function packageMatchesSearchTerms(pkg, terms) {
  if (!terms || !terms.length) return true;

  var searchable = [
    pkg && pkg.name,
    pkg && pkg.id,
    pkg && pkg.publisher,
    pkg && pkg.category,
    stripMarkdown(pkg && pkg.description),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();

  return terms.every(function (term) {
    return searchable.includes(term);
  });
}

function highlightStoreText(value, terms) {
  var text = String(value || '');
  if (!terms || !terms.length || !text) {
    return escapeHtml(text);
  }

  var lower = text.toLowerCase();
  var ranges = [];

  terms.forEach(function (term) {
    if (!term) return;
    var fromIndex = 0;
    while (fromIndex < lower.length) {
      var foundAt = lower.indexOf(term, fromIndex);
      if (foundAt === -1) break;
      ranges.push({ start: foundAt, end: foundAt + term.length });
      fromIndex = foundAt + Math.max(1, term.length);
    }
  });

  if (!ranges.length) {
    return escapeHtml(text);
  }

  ranges.sort(function (a, b) {
    if (a.start !== b.start) return a.start - b.start;
    return b.end - a.end;
  });

  var merged = [];
  ranges.forEach(function (range) {
    if (!merged.length) {
      merged.push({ start: range.start, end: range.end });
      return;
    }
    var last = merged[merged.length - 1];
    if (range.start <= last.end) {
      last.end = Math.max(last.end, range.end);
      return;
    }
    merged.push({ start: range.start, end: range.end });
  });

  var html = '';
  var cursor = 0;
  merged.forEach(function (range) {
    if (cursor < range.start) {
      html += escapeHtml(text.slice(cursor, range.start));
    }
    html += '<mark class="store-hit">' + escapeHtml(text.slice(range.start, range.end)) + '</mark>';
    cursor = range.end;
  });

  if (cursor < text.length) {
    html += escapeHtml(text.slice(cursor));
  }

  return html;
}

// ---------------------------------------------------------------------------
// Catalog card rendering with pagination
// ---------------------------------------------------------------------------
function renderCards() {
  if (!state.filtered.length) {
    cardsEl.classList.remove('cards-compact');
    cardsEl.innerHTML = '<div class="card"><h3>' + escapeHtml(translate('store.noPackagesFound')) + '</h3><p class="meta">' + escapeHtml(translate('store.adjustSearchFilter')) + '</p></div>';
    updateCatalogPagination();
    return;
  }

  var pg = getPaginationState(state.filtered, catalogPage, catalogPageSize);
  catalogPage = pg.validPage;

  var start = pg.start;
  var end = start + catalogPageSize;
  var pageItems = state.filtered.slice(start, end);
  var searchTerms = getStoreSearchTerms();

  // Keep card widths balanced when only a few results are shown.
  cardsEl.classList.toggle('cards-compact', pageItems.length > 0 && pageItems.length <= 3);

  cardsEl.innerHTML = pageItems.map(function (pkg) {
    var description = stripMarkdown(pkg.description || translate('store.noDescription'));
    var shortDescription = description.slice(0, 280);
    var publisher = String(pkg.publisher || translate('common.unknown'));
    var version = String(pkg.version || translate('common.notAvailable'));
    var packageID = String(pkg.id || '');
    var packageLabel = translate('store.packageId', { id: pkg.id });
    var nameLabel = pkg.name || pkg.id;
    var publisherVersionLabel = publisher + ' | ' + version;
    var iconImgHtml = '';
    if (pkg.icon) {
      iconImgHtml = '<img src="' + escapeHtmlAttr(pkg.icon) + '" alt="' + escapeHtmlAttr(pkg.name || pkg.id) + '" class="app-icon" />';
    }

    var action = getContextAction(pkg.id);
    var actionClass = action.action === 'install' ? 'btn primary' : 'btn danger';
    var actionButton = '<button class="' + actionClass + '" data-action="' + escapeHtmlAttr(action.action) + '" data-id="' + escapeHtmlAttr(pkg.id) + '">' + escapeHtml(action.label) + '</button>';
    var detailButton = '<button class="btn subtle store-detail-btn" data-detail-id="' + escapeHtmlAttr(pkg.id) + '" title="' + escapeHtmlAttr(translate('store.viewDetails')) + '" aria-label="' + escapeHtmlAttr(translate('store.viewDetailsOf', { name: pkg.name || pkg.id })) + '">ⓘ</button>';

    return '<article class="card store-card" data-detail-id="' + escapeHtmlAttr(packageID) + '">' +
      '<div class="store-card-top">' +
        '<div class="app-icon-container store-card-icon-slot">' + iconImgHtml + '</div>' +
        detailButton +
      '</div>' +
      '<h3>' + highlightStoreText(nameLabel, searchTerms) + '</h3>' +
      '<div class="meta">' + highlightStoreText(publisherVersionLabel, searchTerms) + '</div>' +
      '<p class="desc">' + highlightStoreText(shortDescription, searchTerms) + '</p>' +
      '<div class="card-actions">' +
        '<span class="card-info">' + escapeHtml(packageID) + ' &middot; v' + escapeHtml(version) + '</span>' +
        actionButton +
      '</div>' +
    '</article>';
  }).join('');

  updateCatalogPagination();
}

function updateCatalogPagination() {
  var pg = getPaginationState(state.filtered, catalogPage, catalogPageSize);
  var singlePage = pg.totalPages <= 1;

  // Esconde todo o footer de paginação quando ha apenas uma pagina:
  // nao faz sentido mostrar botões Anterior/Proxima sem outra pagina.
  if (catalogPaginationEl) catalogPaginationEl.classList.toggle('hidden', singlePage);

  if (!singlePage) {
    if (catalogPageInfoEl) catalogPageInfoEl.textContent = translate('pagination.page', { page: catalogPage, total: pg.totalPages });
    if (catalogPrevBtn) catalogPrevBtn.disabled = catalogPage <= 1;
    if (catalogNextBtn) catalogNextBtn.disabled = catalogPage >= pg.totalPages;
  }

  updateHomeBtn();
}

function applyFilter() {
  var terms = getStoreSearchTerms();
  catalogPage = 1;

  state.filtered = state.allPackages.filter(function (pkg) {
    return packageMatchesSearchTerms(pkg, terms);
  });
  updateHomeBtn();
  renderCards();
}

function updateHomeBtn() {
  if (!homeBtn) return;
  var hasSearch = searchEl ? searchEl.value.trim().length > 0 : false;
  homeBtn.disabled = catalogPage <= 1 && !hasSearch;
}

function goHome() {
  if (searchEl) searchEl.value = '';
  catalogPage = 1;
  state.filtered = state.allPackages;
  updateHomeBtn();
  renderCards();
}

async function loadCatalog() {
  if (reloadBtn) reloadBtn.classList.add('loading');
  try {
    showFeedback(translate('store.catalogLoading'));
    var api = appApi();
    var catalog = await api.GetCatalog();
    state.allPackages = catalog.packages || [];
    await loadPackageActions(api);
    state.filtered = state.allPackages;
    catalogPage = 1;
    infoEl.textContent = translate('store.appsAllowed', { count: (catalog.count || state.allPackages.length) });
    applyFilter();
    showFeedback(translate('store.catalogLoaded'));
  } catch (error) {
    showFeedback(String(error), true);
    infoEl.textContent = translate('store.catalogLoadFailure');
  } finally {
    if (reloadBtn) reloadBtn.classList.remove('loading');
  }
}

async function loadPackageActions(api) {
  state.packageActions = {};
  try {
    var actions = await (api || appApi()).GetPackageActionsJSON();
    if (actions && typeof actions === 'object') {
      state.packageActions = actions;
    }
  } catch (_) {
    // best effort only
  }
}

function getContextAction(packageId) {
  var key = String(packageId || '').toLowerCase();
  var action = state.packageActions[key];
  if (action === 'upgrade' || action === 'uninstall') return { action: 'uninstall', label: translate('action.remove') };
  return { action: 'install', label: translate('action.install') };
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

async function runAction(action, id, displayID) {
  if (!id) return;
  if (!VALID_ACTIONS.has(action)) return;
  try {
    var itemLabel = displayID || id;
    showFeedback(action + ' ' + itemLabel + '...');
    var output = '';

    if (action === 'install') output = await appApi().Install(id);
    else if (action === 'uninstall') output = await appApi().Uninstall(id);
    else if (action === 'upgrade') output = await appApi().Upgrade(id);

    showFeedback(action + ' concluido para ' + itemLabel);
    if (installedOutputEl) {
      installedOutputEl.textContent = output || translate('common.noOutput');
    }
  } catch (error) {
    showFeedback(String(error), true);
  }
}

async function runUpgradeAll() {
  try {
    showFeedback('Atualizando todos os apps...');
    var output = await appApi().UpgradeAll();
    showFeedback('Atualizacao geral concluida.');
    if (installedOutputEl) {
      installedOutputEl.textContent = output || translate('common.noOutput');
    }
  } catch (error) {
    showFeedback(String(error), true);
  }
}

async function listInstalled() {
  try {
    showFeedback('Consultando apps instalados...');
    var output = await appApi().ListInstalled();
    if (installedOutputEl) {
      installedOutputEl.textContent = output || translate('common.noOutput');
    }
    showFeedback('Lista de instalados atualizada.');
  } catch (error) {
    showFeedback(String(error), true);
  }
}

// ---------------------------------------------------------------------------
// Updates tab
// ---------------------------------------------------------------------------

async function checkPendingUpdates() {
  try {
    updatesProgressEl.classList.remove('hidden');
    updatesInfoEl.textContent = translate('common.loading');
    checkUpdatesBtn.disabled = true;
    pendingUpdates = (await appApi().GetPendingUpdates()) || [];
    updatesInfoEl.textContent = translate('updates.availableCount', { count: pendingUpdates.length });
    renderUpdatesTable();
    if (pendingUpdates.length > 0) {
      showToast(translate('updates.foundCount', { count: pendingUpdates.length }), 'success');
    } else {
      showToast(translate('updates.nonePending'), 'info');
    }
  } catch (error) {
    showFeedback(String(error), true);
    updatesInfoEl.textContent = translate('updates.checkError');
  } finally {
    updatesProgressEl.classList.add('hidden');
    checkUpdatesBtn.disabled = false;
  }
}

function normalizeUpdateSource(source) {
  var normalized = String(source || '').trim().toLowerCase();
  if (normalized === 'choco' || normalized === 'chocolatey') return 'chocolatey';
  if (normalized === 'winget') return 'winget';
  return normalized || 'winget';
}

function buildUpdateUpgradeTarget(item) {
  var id = String(item && item.id ? item.id : '').trim();
  if (!id) return '';
  return normalizeUpdateSource(item && item.source) + '::' + id;
}

function renderUpdatesTable() {
  if (!pendingUpdates.length) {
    updatesTableBodyEl.innerHTML = '<tr><td colspan="6" class="meta">' + escapeHtml(translate('updates.nonePending')) + '</td></tr>';
    upgradeSelectedBtn.disabled = true;
    if (updateSelectAllEl) updateSelectAllEl.checked = false;
    return;
  }
  updatesTableBodyEl.innerHTML = pendingUpdates.map(function (u, i) {
    var target = buildUpdateUpgradeTarget(u);
    var packageLabel = String(u.id || '').trim();
    var source = normalizeUpdateSource(u.source);
    return '<tr>' +
      '<td class="update-check-col"><input type="checkbox" class="update-check" data-idx="' + i + '" data-id="' + escapeHtmlAttr(target) + '" data-package-label="' + escapeHtmlAttr(packageLabel) + '" /></td>' +
      '<td>' + escapeHtml(u.name || '-') + '</td>' +
      '<td>' + escapeHtml(u.currentVersion || '-') + '</td>' +
      '<td>' + escapeHtml(u.availableVersion || '-') + '</td>' +
      '<td>' + escapeHtml(source) + '</td>' +
      '<td><button class="btn primary" data-action="upgrade" data-id="' + escapeHtmlAttr(target) + '" data-package-label="' + escapeHtmlAttr(packageLabel) + '">' + escapeHtml(translate('updates.upgrade')) + '</button></td>' +
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
  var items = Array.from(checked).map(function (cb) {
    return {
      target: cb.dataset.id,
      label: cb.dataset.packageLabel || cb.dataset.id,
    };
  });
  upgradeSelectedBtn.disabled = true;
  for (var i = 0; i < items.length; i++) {
    try {
      showToast(translate('updates.upgradingItem', { id: items[i].label }), 'info');
      await appApi().Upgrade(items[i].target);
      showToast(translate('updates.upgradeSuccess', { id: items[i].label }), 'success');
    } catch (error) {
      showToast(translate('updates.upgradeError', { id: items[i].label, error: String(error) }), 'error');
    }
  }
  showToast(translate('updates.batchComplete'), 'success');
  checkPendingUpdates();
}

// ---------------------------------------------------------------------------
// App detail modal
// ---------------------------------------------------------------------------

var _appDetailModal = null;
function getAppDetailModal() {
  if (!_appDetailModal) _appDetailModal = document.getElementById('appDetailModal');
  return _appDetailModal;
}

function openAppDetailModal(pkg) {
  var modal = getAppDetailModal();
  if (!modal) return;

  var titleEl = document.getElementById('appDetailModalTitle');
  var metaEl  = document.getElementById('appDetailMeta');
  var iconEl  = document.getElementById('appDetailIcon');
  var descEl  = document.getElementById('appDetailDescription');
  var actionBtn = document.getElementById('appDetailActionBtn');

  if (titleEl) titleEl.textContent = pkg.name || pkg.id;
  if (metaEl) metaEl.textContent = translate('store.appMeta', { publisher: (pkg.publisher || translate('common.unknown')), version: (pkg.version || translate('common.notAvailable')), id: pkg.id });
  if (iconEl) iconEl.innerHTML = pkg.icon
    ? '<img src="' + escapeHtmlAttr(pkg.icon) + '" alt="" class="app-icon" style="width:64px;height:64px;" />'
    : '';
  if (descEl) descEl.innerHTML = typeof renderMarkdown === 'function'
    ? renderMarkdown(pkg.description || translate('store.noDescription'))
    : escapeHtml(pkg.description || translate('store.noDescription'));

  if (actionBtn) {
    var action = getContextAction(pkg.id);
    actionBtn.textContent = action.label;
    actionBtn.className = action.action === 'install' ? 'btn primary' : 'btn danger';
    actionBtn.dataset.action = action.action;
    actionBtn.dataset.id = pkg.id;
  }

  modal.classList.remove('hidden');
  modal.setAttribute('aria-hidden', 'false');
}

function closeAppDetailModal() {
  var modal = getAppDetailModal();
  if (!modal) return;
  modal.classList.add('hidden');
  modal.setAttribute('aria-hidden', 'true');
}
