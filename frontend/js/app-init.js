"use strict";

function initAppBindings() {
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
    tabChatBtn.addEventListener('click', function () {
      setActiveTab('chat');
      loadChatConfig();
    });
  }

  if (tabSupportBtn) {
    tabSupportBtn.addEventListener('click', function () {
      setActiveTab('support');
      loadSupportTickets();
    });
  }

  if (tabKnowledgeBtn) {
    tabKnowledgeBtn.addEventListener('click', function () {
      setActiveTab('knowledge');
      loadKnowledgeBase();
    });
  }

  if (tabDebugBtn) {
    tabDebugBtn.addEventListener('click', function () {
      setActiveTab('debug');
      loadDebugConfig();
    });
  }

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

  if (themeToggleBtn) {
    themeToggleBtn.addEventListener('click', toggleTheme);
  }

  if (checkUpdatesBtn) {
    checkUpdatesBtn.addEventListener('click', checkPendingUpdates);
  }
  if (upgradeSelectedBtn) {
    upgradeSelectedBtn.addEventListener('click', upgradeSelected);
  }

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
        var all = document.querySelectorAll('.update-check');
        var checked = document.querySelectorAll('.update-check:checked');
        if (updateSelectAllEl) {
          updateSelectAllEl.checked = all.length > 0 && all.length === checked.length;
        }
      }
    });

    updatesTableBodyEl.addEventListener('click', function (e) {
      var btn = e.target;
      if (btn instanceof HTMLButtonElement && btn.dataset.action === 'upgrade' && btn.dataset.id) {
        runAction('upgrade', btn.dataset.id);
      }
    });
  }

  if (refreshLogsBtn) {
    refreshLogsBtn.addEventListener('click', loadLogs);
  }
  if (clearLogsBtn) {
    clearLogsBtn.addEventListener('click', clearLogs);
  }

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
        // API not ready; ignore.
      }
    });
  }

  var thead = document.querySelector('.software-table thead');
  if (thead) {
    thead.addEventListener('click', function (e) {
      var th = e.target.closest('th.sortable');
      if (th && th.dataset.sortKey) {
        toggleSort(th.dataset.sortKey);
      }
    });
  }
}

function bootstrapApp() {
  initAppBindings();
  updateSortIndicators();
  initTheme();
  setActiveTab('store');
  loadCatalog();
  loadSidebarUser();
  initChat();
  initSupport();
  initKnowledge();
  initDebug();
}

bootstrapApp();
