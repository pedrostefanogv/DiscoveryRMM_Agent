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
    showToast('Loja atualizada por sync' + (data && data.variant ? ' (' + data.variant + ')' : ''), 'info');
    return;
  }

  showToast('Novos dados da loja recebidos via sync', 'info');
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

    var action = getContextAction(pkg.id);
    var actionClass = action.action === 'install' ? 'btn primary' : 'btn danger';
    var actionButton = '<button class="' + actionClass + '" data-action="' + escapeHtmlAttr(action.action) + '" data-id="' + escapeHtmlAttr(pkg.id) + '">' + escapeHtml(action.label) + '</button>';

    return '<article class="card store-card">' +
      iconHtml +
      '<h3>' + escapeHtml(pkg.name || pkg.id) + '</h3>' +
      '<div class="meta">' + escapeHtml(publisher) + ' | ' + escapeHtml(version) + '</div>' +
      '<div class="meta">ID: ' + escapeHtml(pkg.id) + '</div>' +
      '<p class="desc">' + escapeHtml(description).slice(0, 180) + '</p>' +
      '<div class="card-actions">' +
        actionButton +
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
    showFeedback('Carregando catalogo...');
    var api = appApi();
    var catalog = await api.GetCatalog();
    state.allPackages = catalog.packages || [];
    await loadPackageActions(api);
    state.filtered = state.allPackages;
    catalogPage = 1;
    infoEl.textContent = 'Apps permitidos: ' + (catalog.count || state.allPackages.length) + ' | Com icone: ' + (catalog.packagesWithIcon || 0);
    applyFilter();
    showFeedback('Catalogo carregado.');
  } catch (error) {
    showFeedback(String(error), true);
    infoEl.textContent = 'Falha ao carregar apps permitidos';
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
  if (action === 'upgrade' || action === 'uninstall') return { action: 'uninstall', label: 'Remover' };
  return { action: 'install', label: 'Instalar' };
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
    if (installedOutputEl) {
      installedOutputEl.textContent = output || '(sem saida)';
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
      installedOutputEl.textContent = output || '(sem saida)';
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
      installedOutputEl.textContent = output || '(sem saida)';
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
    updatesTableBodyEl.innerHTML = '<tr><td colspan="5" class="meta">Nenhuma atualizacao pendente.</td></tr>';
    upgradeSelectedBtn.disabled = true;
    if (updateSelectAllEl) updateSelectAllEl.checked = false;
    return;
  }
  updatesTableBodyEl.innerHTML = pendingUpdates.map(function (u, i) {
    return '<tr>' +
      '<td class="update-check-col"><input type="checkbox" class="update-check" data-idx="' + i + '" data-id="' + escapeHtmlAttr(u.id) + '" /></td>' +
      '<td>' + escapeHtml(u.name || '-') + '</td>' +
      '<td>' + escapeHtml(u.currentVersion || '-') + '</td>' +
      '<td>' + escapeHtml(u.availableVersion || '-') + '</td>' +
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
