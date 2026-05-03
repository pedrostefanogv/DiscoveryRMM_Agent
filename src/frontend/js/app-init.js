"use strict";

function initAppBindings() {
  cardsEl.addEventListener('click', function (event) {
    var target = event.target;
    if (!(target instanceof HTMLButtonElement)) return;

    // Detail modal button
    if (target.dataset.detailId) {
      var pkg = (state.allPackages || []).find(function (p) { return p.id === target.dataset.detailId; });
      if (pkg && typeof openAppDetailModal === 'function') openAppDetailModal(pkg);
      return;
    }

    var action = target.dataset.action;
    var id = target.dataset.id;
    if (!action || !id) return;

    runAction(action, id);
  });

  // App detail modal close handlers
  (function wireAppDetailModal() {
    var closeBtn = document.getElementById('appDetailCloseBtn');
    var actionBtn = document.getElementById('appDetailActionBtn');
    var modal = document.getElementById('appDetailModal');

    if (closeBtn) closeBtn.addEventListener('click', function () {
      if (typeof closeAppDetailModal === 'function') closeAppDetailModal();
    });
    if (modal) modal.addEventListener('click', function (e) {
      if (e.target === modal && typeof closeAppDetailModal === 'function') closeAppDetailModal();
    });
    if (actionBtn) actionBtn.addEventListener('click', function () {
      var act = actionBtn.dataset.action;
      var id = actionBtn.dataset.id;
      if (act && id) runAction(act, id);
      if (typeof closeAppDetailModal === 'function') closeAppDetailModal();
    });
  })();

  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && typeof closeAppDetailModal === 'function') closeAppDetailModal();
  });

  if (searchEl) {
    searchEl.addEventListener('input', debounce(applyFilter, 300));
  }
  if (reloadBtn) {
    reloadBtn.addEventListener('click', loadCatalog);
  }
  if (upgradeAllBtn) {
    upgradeAllBtn.addEventListener('click', runUpgradeAll);
  }
  if (installedBtn) {
    installedBtn.addEventListener('click', listInstalled);
  }
  if (tabStatusBtn) {
    tabStatusBtn.addEventListener('click', function () {
      setActiveTab('status');
      loadStatusOverview();
    });
  }
  tabStoreBtn.addEventListener('click', function () {
    setActiveTab('store');
    if (typeof handleStoreTabActivated === 'function') {
      handleStoreTabActivated();
    }
  });
  tabUpdatesBtn.addEventListener('click', function () { setActiveTab('updates'); });
  tabInventoryBtn.addEventListener('click', function () {
    setActiveTab('inventory');
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

  if (tabAutomationBtn) {
    tabAutomationBtn.addEventListener('click', function () {
      setActiveTab('automation');
      loadAutomationState();
    });
  }

  if (tabDebugBtn) {
    tabDebugBtn.addEventListener('click', function () {
      setActiveTab('debug');
      loadDebugConfig();
    });
  }

  if (tabPSADTBtn) {
    tabPSADTBtn.addEventListener('click', function () {
      setActiveTab('psadt');
      if (typeof loadPSADTDebugState === 'function') {
        loadPSADTDebugState();
      }
    });
  }

  if (tabP2PBtn) {
    tabP2PBtn.addEventListener('click', function () {
      setActiveTab('p2p');
      if (typeof loadP2PView === 'function') {
        loadP2PView();
      }
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
  if (logsOriginFilterEl) {
    logsOriginFilterEl.addEventListener('change', renderLogsOutput);
  }
  if (logsSearchInputEl) {
    logsSearchInputEl.addEventListener('input', renderLogsOutput);
  }
  if (clearLogsBtn) {
    clearLogsBtn.addEventListener('click', clearLogs);
  }
  if (copyLogsBtn) {
    copyLogsBtn.addEventListener('click', copyLogs);
  }
  if (exportLogsBtn) {
    exportLogsBtn.addEventListener('click', exportLogs);
  }

  // Auto-scroll toggle: pausa scroll quando usuario faz scroll manual
  if (logsOutputEl) {
    logsOutputEl.addEventListener('scroll', function () {
      var atBottom = logsOutputEl.scrollHeight - logsOutputEl.scrollTop - logsOutputEl.clientHeight < 40;
      logsOutputEl.dataset.pinned = atBottom ? 'true' : 'false';
    });
  }

  if (sidebarToggleBtn && sidebarEl) {
    sidebarToggleBtn.addEventListener('click', function () {
      sidebarEl.classList.toggle('collapsed');
      if (typeof syncWindowChromeSidebarWidth === 'function') {
        syncWindowChromeSidebarWidth();
      }
    });
  }

  if (refreshInventoryBtn) {
    refreshInventoryBtn.addEventListener('click', function () { loadInventory(true); });
  }
  if (installOsqueryBtn) {
    installOsqueryBtn.addEventListener('click', installOsquery);
  }
  if (exportInventoryBtn) {
    exportInventoryBtn.addEventListener('click', exportInventory);
  }
  if (exportInventoryPdfBtn) {
    exportInventoryPdfBtn.addEventListener('click', exportInventoryPdf);
  }

  softwareSearchInputEl.addEventListener('input', debounce(applySoftwareFilter, 300));
  softwarePrevBtn.addEventListener('click', function () {
    softwarePage -= 1;
    renderSoftwareTable();
  });
  softwareNextBtn.addEventListener('click', function () {
    softwarePage += 1;
    renderSoftwareTable();
  });
  if (refreshSoftwareBtn) {
    refreshSoftwareBtn.addEventListener('click', refreshSoftware);
  }

  startupSearchInputEl.addEventListener('input', debounce(applyStartupFilter, 300));
  startupPrevBtn.addEventListener('click', function () {
    startupPage -= 1;
    renderStartupTable();
  });
  startupNextBtn.addEventListener('click', function () {
    startupPage += 1;
    renderStartupTable();
  });
  if (refreshStartupBtn) {
    refreshStartupBtn.addEventListener('click', refreshStartupItems);
  }

  // Network Connections listeners
  connectionsSearchInputEl.addEventListener('input', debounce(applyConnectionsFilter, 300));
  connectionsTabListening.addEventListener('click', function () {
    switchConnectionsTab('listening');
  });
  connectionsTabOpen.addEventListener('click', function () {
    switchConnectionsTab('open');
  });
  if (refreshConnectionsBtn) {
    refreshConnectionsBtn.addEventListener('click', refreshNetworkConnections);
  }
  if (refreshListeningPortsBtn) {
    refreshListeningPortsBtn.addEventListener('click', refreshListeningPorts);
  }
  connectionsPrevBtn.addEventListener('click', function () {
    connectionsPage -= 1;
    renderConnectionsTable();
  });
  connectionsNextBtn.addEventListener('click', function () {
    connectionsPage += 1;
    renderConnectionsTable();
  });

  // Connections table header sort listeners
  var listeningHeaders = listeningPortsTableEl.querySelectorAll('th.sortable');
  listeningHeaders.forEach(function (th) {
    th.addEventListener('click', function () {
      toggleConnectionsSort(this.dataset.sortKey);
    });
  });
  var openHeaders = openSocketsTableEl.querySelectorAll('th.sortable');
  openHeaders.forEach(function (th) {
    th.addEventListener('click', function () {
      toggleConnectionsSort(this.dataset.sortKey);
    });
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

  var startupThead = document.querySelector('.startup-table thead');
  if (startupThead) {
    startupThead.addEventListener('click', function (e) {
      var th = e.target.closest('th.sortable');
      if (th && th.dataset.sortKey) {
        toggleStartupSort(th.dataset.sortKey);
      }
    });
  }
}

async function bootstrapApp() {
  if (typeof initApplicationLocale === 'function') {
    await initApplicationLocale();
  }
  initAppBindings();
  updateSortIndicators();
  initTheme();
  if (typeof syncWindowChromeSidebarWidth === 'function') {
    syncWindowChromeSidebarWidth();
  }
  setRuntimeFlags({ debugMode: false });
  try {
    var flags = await appApi().GetRuntimeFlags();
    setRuntimeFlags(flags || { debugMode: false });
  } catch (_) {
    setRuntimeFlags({ debugMode: false });
  }

  if (typeof syncProvisioningOverlayFromRuntime === 'function') {
    await syncProvisioningOverlayFromRuntime();
  }

  if (isDebugRuntimeMode()) {
    setActiveTab('logs');
    loadLogs();
  } else {
    setActiveTab('status');
    loadStatusOverview();
  }

  // Hide tabs based on agent configuration feature flags.
  try {
    var cfg = await appApi().GetAgentConfiguration();
    if (cfg) {
      hideTabIfNeeded(tabStoreBtn, storeViewEl, cfg.appStoreEnabled);
      hideTabIfNeeded(tabChatBtn, chatViewEl, cfg.chatAIEnabled);
      hideTabIfNeeded(tabSupportBtn, supportViewEl, cfg.supportEnabled);
      hideTabIfNeeded(tabKnowledgeBtn, knowledgeViewEl, cfg.knowledgeBaseEnabled);

      // Ensure the active tab is visible (fallback to status)
      var active = document.querySelector('.sidebar-link.active');
      if (active && active.classList.contains('hidden')) {
        setActiveTab('status');
      }
    }
  } catch (_) {
    // ignore; leave tabs as-is
  }

  loadCatalog();
  loadSidebarUser();
  initChat();
  initSupport();
  initKnowledge();
  initAutomation();
  initDebug();
  if (typeof initP2PPage === 'function') {
    initP2PPage();
  }

  if (window.runtime && window.runtime.EventsOn && typeof handleNotificationEvent === 'function') {
    window.runtime.EventsOn('notification:new', handleNotificationEvent);
  }
  if (typeof startUIRuntimeMonitor === 'function') {
    startUIRuntimeMonitor('bootstrap');
  }
}

function hideTabIfNeeded(tabBtn, viewEl, flag) {
  if (!tabBtn) return;
  if (flag === false) {
    tabBtn.classList.add('hidden');
    if (viewEl) viewEl.classList.add('hidden');
  }
}

bootstrapApp();
