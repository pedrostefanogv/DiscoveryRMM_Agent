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
      // Config de debug atualizada (salva na página de Debug ou aplicada pelo
      // serviço via IPC): a loja pode voltar a funcionar — marca o catálogo
      // como dirty para recarregar ao ativar a aba (e imediatamente se já
      // estiver nela). Corrige o erro "configuração de servidor API
      // incompleta" persistir até fechar e abrir o agent/ui.
      window.wails.on('debug:config_updated', function () {
        storeCatalogDirty = true;
        if (typeof setActiveTab === 'function' && activeTab === 'store' && !window.__discoveryUISuspended && !document.hidden) {
          loadCatalog();
        }
      });
      // Entrega tardia do updates:scan (revisão 4 — bug B8): quando o scan
      // winget/choco no serviço passa do timeout do RPC (10s), o resultado
      // chega por este evento em vez da resposta do GetPendingUpdates.
      window.wails.on('updates:list', function (data) {
        try {
          var items = (data && data.updates) || [];
          if (!Array.isArray(items) || !items.length) return;
          pendingUpdates = items;
          updatesInfoEl.textContent = translate('updates.availableCount', { count: pendingUpdates.length });
          renderUpdatesTable();
          showToast(translate('updates.foundCount', { count: pendingUpdates.length }), 'success');
        } catch (e) { /* best-effort: entrega tardia não deve quebrar a página */ }
      });
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
    pkg && pkg.installationType,
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
// Origem do app (Winget | Chocolatey | Custom) — badge nos cards e no modal
// ---------------------------------------------------------------------------

var STORE_ORIGIN_ICONS = {
    winget: '<svg viewBox="0 -960 960 960" width="11" height="11" fill="none" aria-hidden="true" focusable="false"><path d="M440-183v-274L200-596v274l240 139Zm80 0 240-139v-274L520-457v274Zm-80 92L160-252q-19-11-29.5-29T120-321v-318q0-22 10.5-40t29.5-29l280-161q19-11 40-11t40 11l280 161q19 11 29.5 29t10.5 40v318q0 22-10.5 40T800-252L520-91q-19 11-40 11t-40-11Zm200-528 77-44-237-137-78 45 238 136Zm-160 93 78-45-237-137-78 45 237 137Z" fill="currentColor" /></svg>',
    chocolatey: '<svg viewBox="0 -960 960 960" width="11" height="11" fill="none" aria-hidden="true" focusable="false"><path d="m620-275 198-198q11-11 28-11t28 11q11 11 11 28t-11 28L648-191q-12 12-28 12t-28-12L478-305q-11-11-11-28t11-28q11-11 28-11t28 11l86 86ZM200-120q-33 0-56.5-23.5T120-200v-560q0-33 23.5-56.5T200-840h167q11-35 43-57.5t70-22.5q40 0 71.5 22.5T594-840h166q33 0 56.5 23.5T840-760v160q0 17-11.5 28.5T800-560q-17 0-28.5-11.5T760-600v-160h-80v80q0 17-11.5 28.5T640-640H320q-17 0-28.5-11.5T280-680v-80h-80v560h200q17 0 28.5 11.5T440-160q0 17-11.5 28.5T400-120H200Zm280-640q17 0 28.5-11.5T520-800q0-17-11.5-28.5T480-840q-17 0-28.5 11.5T440-800q0 17 11.5 28.5T480-760Z" fill="currentColor" /></svg>',
    custom: '<svg viewBox="0 -960 960 960" width="11" height="11" fill="none" aria-hidden="true" focusable="false"><path d="M360-360q-100 0-170-70t-70-170q0-20 3-40t11-38q5-10 12.5-15t16.5-7q9-2 18.5.5T199-689l105 105 72-72-105-105q-8-8-10.5-17.5T260-797q2-9 7-16.5t15-12.5q18-8 38-11t40-3q100 0 170 70t70 170q0 23-4 43.5T584-516l202 200q29 29 29 71t-29 71q-29 29-71 29t-71-30L444-376q-20 8-40.5 12t-43.5 4Zm0-80q26 0 52-8t47-25l243 243q5 5 13.5 4.5T729-231q5-5 5-13.5t-5-13.5L486-500q18-20 26-46.5t8-53.5q0-60-38.5-104.5T386-758l74 74q12 12 12 28t-12 28L332-500q-12 12-28 12t-28-12l-74-74q9 57 53.5 95.5T360-440Zm109-51Z" fill="currentColor" /></svg>'
  };

function storeOriginKey(pkg) {
  var raw = String((pkg && pkg.installationType) || '').trim().toLowerCase();
  if (raw === 'winget') return 'winget';
  if (raw === 'chocolatey' || raw === 'choco') return 'chocolatey';
  if (raw === 'custom') return 'custom';
  // Fallback legado: catálogos antigos usavam category=installationType.
  var category = String((pkg && pkg.category) || '').trim().toLowerCase();
  if (category === 'winget') return 'winget';
  if (category === 'chocolatey' || category === 'choco') return 'chocolatey';
  if (category === 'custom') return 'custom';
  return '';
}

function storeOriginBadgeHtml(pkg) {
  var key = storeOriginKey(pkg);
  if (!key) return '';
  return '<span class="store-origin-badge store-origin-' + key + '" data-origin="' + key + '" title="' +
    escapeHtmlAttr(translate('store.originTooltip.' + key)) + '">' +
    STORE_ORIGIN_ICONS[key] +
    escapeHtml(translate('store.origin.' + key)) +
    '</span>';
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

    var action = getContextAction(pkg.id, pkg);
    var actionClass = action.action === 'uninstall' ? 'btn danger' : action.action === 'installed' ? 'btn subtle' : 'btn primary';
    // Botões só com ícone: o rótulo vira title/aria-label (acessibilidade).
    var actionButton = '<button class="' + actionClass + '" data-action="' + escapeHtmlAttr(action.action) + '" data-id="' + escapeHtmlAttr(pkg.id) + '" title="' + escapeHtmlAttr(action.label) + '" aria-label="' + escapeHtmlAttr(action.label) + '"' + (action.action === 'installed' ? ' disabled' : '') + '>' + storeActionIcon(action.action) + '</button>';
    // App instalado com update pendente: além de "Atualizar", oferece "Remover".
    if (action.action === 'upgrade') {
      var removeLabel = translate('action.remove');
      actionButton += '<button class="btn danger" data-action="uninstall" data-id="' + escapeHtmlAttr(pkg.id) + '" title="' + escapeHtmlAttr(removeLabel) + '" aria-label="' + escapeHtmlAttr(removeLabel) + '">' + storeActionIcon('uninstall') + '</button>';
    }
    var originBadge = storeOriginBadgeHtml(pkg);
    var installedBadge = storeInstalledBadgeHtml(pkg);

    return '<article class="card store-card" data-detail-id="' + escapeHtmlAttr(packageID) + '">' +
      '<div class="store-card-top">' +
        '<div class="app-icon-container store-card-icon-slot">' + iconImgHtml + '</div>' +
        '<div class="store-card-top-right">' +
          originBadge +
        '</div>' +
      '</div>' +
      '<h3>' + highlightStoreText(nameLabel, searchTerms) + '</h3>' +
      '<div class="meta">' + highlightStoreText(publisherVersionLabel, searchTerms) + '</div>' +
      '<p class="desc">' + highlightStoreText(shortDescription, searchTerms) + '</p>' +
      '<div class="card-actions">' +
        '<span class="card-info">' + escapeHtml(packageID) + ' &middot; v' + escapeHtml(version) + '</span>' +
        installedBadge +
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

// loadCatalog(forceRefresh) carrega o catálogo da loja. Com forceRefresh=true
// (botão "atualizar") faz um fetch novo no servidor — necessário para um pacote
// custom recém-cadastrado/aprovado aparecer sem esperar o próximo sync de
// background. Sem o parâmetro, usa o cache de política do serviço.
async function loadCatalog(forceRefresh) {
  if (reloadBtn) reloadBtn.classList.add('loading');
  try {
    showFeedback(translate(forceRefresh ? 'store.catalogRefreshing' : 'store.catalogLoading'));
    var api = appApi();
    var catalog = forceRefresh && typeof api.RefreshCatalog === 'function'
      ? await api.RefreshCatalog()
      : await api.GetCatalog();
    state.allPackages = catalog.packages || [];
    await loadPackageActions(api);
    state.filtered = state.allPackages;
    catalogPage = 1;
    infoEl.textContent = translate('store.appsAllowed', { count: (catalog.count || state.allPackages.length) });
    applyFilter();
    showFeedback(translate('store.catalogLoaded'));
  } catch (error) {
    // Falha ao carregar: marca dirty para que reativar a aba da loja tente de
    // novo (antes o erro ficava travado na tela até fechar e abrir o agente).
    storeCatalogDirty = true;
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

// storeActionIcon devolve o SVG (16px) da ação do card. Os botões da Loja são
// somente ícone — o texto saiu para dar lugar ao rótulo acessível (title/aria).
function storeActionIcon(action) {
  if (action === 'installed') return '<svg viewBox="0 -960 960 960" width="16" height="16" fill="none" aria-hidden="true" focusable="false"><path d="M480-80q-83 0-156-31.5T197-197q-54-54-85.5-127T80-480q0-83 31.5-156T197-763q54-54 127-85.5T480-880q83 0 156 31.5T763-763q54 54 85.5 127T880-480q0 83-31.5 156T763-197q-54 54-127 85.5T480-80Zm-32-206 260-260-57-57-203 203-94-94-57 57 151 151Z" fill="currentColor" /></svg>';
  if (action === 'upgrade') return '<svg viewBox="0 -960 960 960" width="16" height="16" fill="none" aria-hidden="true" focusable="false"><path d="M440-488v128q0 17 11.5 28.5T480-320q17 0 28.5-11.5T520-360v-128l36 36q11 11 28 11t28-11q11-11 11-28t-11-28L508-612q-12-12-28-12t-28 12L348-508q-11 11-11 28t11 28q11 11 28 11t28-11l36-36Zm40 408q-83 0-156-31.5T197-197q-54-54-85.5-127T80-480q0-83 31.5-156T197-763q54-54 127-85.5T480-880q83 0 156 31.5T763-763q54 54 85.5 127T880-480q0 83-31.5 156T763-197q-54 54-127 85.5T480-80Zm0-80q134 0 227-93t93-227q0-134-93-227t-227-93q-134 0-227 93t-93 227q0 134 93 227t227 93Zm0-320Z" fill="currentColor" /></svg>';
  if (action === 'uninstall') return '<svg viewBox="0 -960 960 960" width="16" height="16" fill="none" aria-hidden="true" focusable="false"><path d="M280-120q-33 0-56.5-23.5T200-200v-520q-17 0-28.5-11.5T160-760q0-17 11.5-28.5T200-800h160q0-17 11.5-28.5T400-840h160q17 0 28.5 11.5T600-800h160q17 0 28.5 11.5T800-760q0 17-11.5 28.5T760-720v520q0 33-23.5 56.5T680-120H280Zm400-600H280v520h400v-520ZM400-280q17 0 28.5-11.5T440-320v-280q0-17-11.5-28.5T400-640q-17 0-28.5 11.5T360-600v280q0 17 11.5 28.5T400-280Zm160 0q17 0 28.5-11.5T600-320v-280q0-17-11.5-28.5T560-640q-17 0-28.5 11.5T520-600v280q0 17 11.5 28.5T560-280ZM280-720v520-520Z" fill="currentColor" /></svg>';
  // install
  return '<svg viewBox="0 -960 960 960" width="16" height="16" fill="none" aria-hidden="true" focusable="false"><path d="M480-337q-8 0-15-2.5t-13-8.5L308-492q-12-12-11.5-28t11.5-28q12-12 28.5-12.5T365-549l75 75v-286q0-17 11.5-28.5T480-800q17 0 28.5 11.5T520-760v286l75-75q12-12 28.5-11.5T652-548q11 12 11.5 28T652-492L508-348q-6 6-13 8.5t-15 2.5ZM240-160q-33 0-56.5-23.5T160-240v-80q0-17 11.5-28.5T200-360q17 0 28.5 11.5T240-320v80h480v-80q0-17 11.5-28.5T760-360q17 0 28.5 11.5T800-320v80q0 33-23.5 56.5T720-160H240Z" fill="currentColor" /></svg>';
}

// ---------------------------------------------------------------------------
// Estado "já instalado" de pacotes custom (detecção configurada no servidor)
// ---------------------------------------------------------------------------

// packageIsInstalled: o catálogo traz `installed` (runtime) para itens custom
// cuja detecção por inventário encontrou o app neste computador.
function packageIsInstalled(pkg) {
  return !!(pkg && pkg.installed);
}

// storeInstalledBadgeHtml mostra o badge "Já instalado" nos cards e no modal.
function storeInstalledBadgeHtml(pkg) {
  if (!packageIsInstalled(pkg)) return '';
  var label = translate('store.installedBadge');
  return '<span class="store-installed-badge" data-installed="true" title="' +
    escapeHtmlAttr(translate('store.installedBadgeTooltip')) + '">' +
    escapeHtml(label) +
    '</span>';
}

// storeInstallActionLabel troca o rótulo do botão para "Reinstalar" quando o
// app já está instalado. A ação continua sendo `install` — reinstalar é decisão
// do usuário, nada é pulado automaticamente.
function storeInstallActionLabel(pkg) {
  return packageIsInstalled(pkg) ? translate('action.reinstall') : translate('action.install');
}

function getContextAction(packageId, pkg) {
  var key = String(packageId || '').toLowerCase();
  var action = state.packageActions[key];
  // upgrade tem precedência sobre uninstall no mapa do agente (updates
  // pendentes sobrescrevem a ação de um app instalado). Quando há update,
  // o card mostra "Atualizar" em vez de "Remover".
  if (action === 'upgrade') return { action: 'upgrade', label: translate('action.update') };
  if (action === 'uninstall') return { action: 'uninstall', label: translate('action.remove') };
  // App custom já instalado (detecção do servidor) sem comando de desinstalação
  // disponível: não mostra "Instalar/Reinstalar" de novo — mostra "Já instalado".
  if (packageIsInstalled(pkg)) return { action: 'installed', label: translate('store.installedAction') };
  return { action: 'install', label: storeInstallActionLabel(pkg) };
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

    // Recarrega o contexto de pacotes (instalado/update) para o card refletir o
    // novo estado — ex.: após atualizar, deixa de mostrar "Atualizar".
    try {
      await loadPackageActions(appApi());
      renderCards();
    } catch (_) {
      // best effort: falha ao atualizar o contexto não invalida a ação.
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
  var detailInstalledBadgeEl = document.getElementById('appDetailInstalledBadge');
  if (detailInstalledBadgeEl) detailInstalledBadgeEl.innerHTML = storeInstalledBadgeHtml(pkg);
  var originBadgeEl = document.getElementById('appDetailOriginBadge');
  if (originBadgeEl) originBadgeEl.innerHTML = storeOriginBadgeHtml(pkg);
  if (metaEl) metaEl.textContent = translate('store.appMeta', { publisher: (pkg.publisher || translate('common.unknown')), version: (pkg.version || translate('common.notAvailable')), id: pkg.id });
  if (iconEl) iconEl.innerHTML = pkg.icon
    ? '<img src="' + escapeHtmlAttr(pkg.icon) + '" alt="" class="app-icon" style="width:64px;height:64px;" />'
    : '';
  if (descEl) descEl.innerHTML = typeof renderMarkdown === 'function'
    ? renderMarkdown(pkg.description || translate('store.noDescription'))
    : escapeHtml(pkg.description || translate('store.noDescription'));

  if (actionBtn) {
    var action = getContextAction(pkg.id, pkg);
    actionBtn.textContent = action.label;
    actionBtn.className = action.action === 'uninstall' ? 'btn danger' : action.action === 'installed' ? 'btn subtle' : 'btn primary';
    actionBtn.dataset.action = action.action;
    actionBtn.dataset.id = pkg.id;
    // Estado "Já instalado" sem comando de desinstalação: botão desabilitado.
    actionBtn.disabled = action.action === 'installed';
    actionBtn.title = action.action === 'installed' ? translate('store.installedActionTitle') : '';
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
