"use strict";

var storeCatalogDirty = false;

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
    return;
  }

  showToast(translate('store.newDataSync'), 'info');
}

(function registerStoreSyncEvents() {
  function doRegister() {
    if (window.runtime && window.runtime.EventsOn) {
      window.runtime.EventsOn('store:catalog-updated', onStoreCatalogUpdated);
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', doRegister);
  } else {
    setTimeout(doRegister, 200);
  }
})();

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

  // Keep card widths balanced when only a few results are shown.
  cardsEl.classList.toggle('cards-compact', pageItems.length > 0 && pageItems.length <= 3);

  cardsEl.innerHTML = pageItems.map(function (pkg) {
    var description = pkg.description || translate('store.noDescription');
    var publisher = pkg.publisher || translate('common.unknown');
    var version = pkg.version || translate('common.notAvailable');
    var iconHtml = '';
    if (pkg.icon) {
      iconHtml = '<div class="app-icon-container"><img src="' + escapeHtmlAttr(pkg.icon) + '" alt="' + escapeHtmlAttr(pkg.name || pkg.id) + '" class="app-icon" /></div>';
    }

    var action = getContextAction(pkg.id);
    var actionClass = action.action === 'install' ? 'btn primary' : 'btn danger';
    var actionButton = '<button class="' + actionClass + '" data-action="' + escapeHtmlAttr(action.action) + '" data-id="' + escapeHtmlAttr(pkg.id) + '">' + escapeHtml(action.label) + '</button>';
    var detailButton = '<button class="btn subtle store-detail-btn" data-detail-id="' + escapeHtmlAttr(pkg.id) + '" title="' + escapeHtmlAttr(translate('store.viewDetails')) + '" aria-label="' + escapeHtmlAttr(translate('store.viewDetailsOf', { name: pkg.name || pkg.id })) + '">ⓘ</button>';

    return '<article class="card store-card">' +
      iconHtml +
      '<h3>' + escapeHtml(pkg.name || pkg.id) + '</h3>' +
      '<div class="meta">' + escapeHtml(publisher) + ' | ' + escapeHtml(version) + '</div>' +
      '<div class="meta">' + escapeHtml(translate('store.packageId', { id: pkg.id })) + '</div>' +
      '<p class="desc">' + escapeHtml(description).slice(0, 180) + '</p>' +
      '<div class="card-actions">' +
        actionButton +
        detailButton +
      '</div>' +
    '</article>';
  }).join('');

  updateCatalogPagination();
}

function updateCatalogPagination() {
  var pg = getPaginationState(state.filtered, catalogPage, catalogPageSize);
  if (catalogPageInfoEl) catalogPageInfoEl.textContent = translate('pagination.page', { page: catalogPage, total: pg.totalPages });
  if (catalogPrevBtn) catalogPrevBtn.disabled = catalogPage <= 1;
  if (catalogNextBtn) catalogNextBtn.disabled = catalogPage >= pg.totalPages;
}

function applyFilter() {
  var q = searchEl ? searchEl.value.trim().toLowerCase() : '';
  catalogPage = 1;

  state.filtered = state.allPackages.filter(function (pkg) {
    if (!q) return true;
    return [pkg.name, pkg.id, pkg.publisher, pkg.category]
      .filter(Boolean)
      .some(function (v) { return String(v).toLowerCase().includes(q); });
  });
  renderCards();
}

async function loadCatalog() {
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
