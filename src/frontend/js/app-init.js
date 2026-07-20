"use strict";

function initColorModeSync() {
  syncColorMode();
  if (typeof MutationObserver !== "undefined") {
    var observer = new MutationObserver(function (mutations) {
      for (var i = 0; i < mutations.length; i += 1) {
        if (mutations[i].attributeName === "data-theme") {
          syncColorMode();
          break;
        }
      }
    });
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
  }
}

function initAppBindings() {
  cardsEl.addEventListener("click", function (event) {
    var target = event.target;
    if (!(target instanceof Element)) return;

    var clickedButton = target.closest("button");
    if (clickedButton instanceof HTMLButtonElement) {
      // Detail modal button
      if (clickedButton.dataset.detailId) {
        var detailPkg = (state.allPackages || []).find(function (p) {
          return p.id === clickedButton.dataset.detailId;
        });
        if (detailPkg && typeof openAppDetailModal === "function")
          openAppDetailModal(detailPkg);
        return;
      }

      var action = clickedButton.dataset.action;
      var id = clickedButton.dataset.id;
      if (!action || !id) return;

      runAction(action, id);
      return;
    }

    var cardEl = target.closest("article.store-card[data-detail-id]");
    if (!cardEl) return;

    var cardPkg = (state.allPackages || []).find(function (p) {
      return p.id === cardEl.dataset.detailId;
    });
    if (cardPkg && typeof openAppDetailModal === "function") {
      openAppDetailModal(cardPkg);
    }
  });

  // App detail modal close handlers
  (function wireAppDetailModal() {
    var closeBtn = document.getElementById("appDetailCloseBtn");
    var actionBtn = document.getElementById("appDetailActionBtn");
    var modal = document.getElementById("appDetailModal");

    if (closeBtn)
      closeBtn.addEventListener("click", function () {
        if (typeof closeAppDetailModal === "function") closeAppDetailModal();
      });
    if (modal)
      modal.addEventListener("click", function (e) {
        if (e.target === modal && typeof closeAppDetailModal === "function")
          closeAppDetailModal();
      });
    if (actionBtn)
      actionBtn.addEventListener("click", function () {
        var act = actionBtn.dataset.action;
        var id = actionBtn.dataset.id;
        if (act && id) runAction(act, id);
        if (typeof closeAppDetailModal === "function") closeAppDetailModal();
      });
  })();

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && typeof closeAppDetailModal === "function")
      closeAppDetailModal();
  });

  if (searchEl) {
    searchEl.addEventListener("input", debounce(applyFilter, 300));
  }
  if (reloadBtn) {
    reloadBtn.addEventListener("click", loadCatalog);
  }
  if (homeBtn) {
    homeBtn.addEventListener("click", function () {
      goHome();
    });
  }
  if (upgradeAllBtn) {
    upgradeAllBtn.addEventListener("click", runUpgradeAll);
  }
  if (installedBtn) {
    installedBtn.addEventListener("click", listInstalled);
  }
  if (tabStatusBtn) {
    tabStatusBtn.addEventListener("click", function () {
      setActiveTab("status");
      loadStatusOverview();
    });
  }
  if (tabStoreBtn) {
    tabStoreBtn.addEventListener("click", function () {
      setActiveTab("store");
      if (typeof handleStoreTabActivated === "function") {
        handleStoreTabActivated();
      }
    });
  }
  if (tabUpdatesBtn) {
    tabUpdatesBtn.addEventListener("click", function () {
      setActiveTab("updates");
    });
  }
  if (tabInventoryBtn) {
    tabInventoryBtn.addEventListener("click", function () {
      setActiveTab("inventory");
      if (!inventoryLoadedOnce) {
        loadInventory();
      }
    });
  }
  if (tabLogsBtn) {
    tabLogsBtn.addEventListener("click", function () {
      setActiveTab("logs");
    });
  }

  if (tabChatBtn) {
    tabChatBtn.addEventListener("click", function () {
      setActiveTab("chat");
      loadChatConfig();
    });
  }

  if (tabSupportBtn) {
    tabSupportBtn.addEventListener("click", function () {
      setActiveTab("support");
      loadSupportTickets();
    });
  }

  if (tabKnowledgeBtn) {
    tabKnowledgeBtn.addEventListener("click", function () {
      setActiveTab("knowledge");
      loadKnowledgeBase();
    });
  }

  if (tabAutomationBtn) {
    tabAutomationBtn.addEventListener("click", function () {
      setActiveTab("automation");
      loadAutomationState();
    });
  }

  if (tabDebugBtn) {
    tabDebugBtn.addEventListener("click", function () {
      setActiveTab("debug");
      loadDebugConfig();
    });
  }

  if (tabPSADTBtn) {
    tabPSADTBtn.addEventListener("click", function () {
      setActiveTab("psadt");
      if (typeof loadPSADTDebugState === "function") {
        loadPSADTDebugState();
      }
    });
  }

  if (tabP2PBtn) {
    tabP2PBtn.addEventListener("click", function () {
      setActiveTab("p2p");
      if (typeof loadP2PView === "function") {
        loadP2PView();
      }
    });
  }

  if (tabZeroTouchConfigBtn) {
    tabZeroTouchConfigBtn.addEventListener("click", function () {
      setActiveTab("zeroTouchConfig");
      if (typeof refreshZeroTouchConfig === "function") {
        refreshZeroTouchConfig();
      }
    });
  }

  if (categorySearchEl) {
    categorySearchEl.addEventListener(
      "input",
      debounce(function () {
        renderCategoryList(categorySearchEl.value);
      }, 200),
    );
  }

  if (categoryListEl) {
    categoryListEl.addEventListener("click", function (e) {
      var li = e.target.closest("li");
      if (!li || li.dataset.cat === undefined) return;
      state.selectedCategory = li.dataset.cat;
      renderCategoryList(categorySearchEl ? categorySearchEl.value : "");
      applyFilter();
    });
  }

  if (themeToggleBtn) {
    themeToggleBtn.addEventListener("click", toggleTheme);
  }

  if (checkUpdatesBtn) {
    checkUpdatesBtn.addEventListener("click", checkPendingUpdates);
  }
  if (upgradeSelectedBtn) {
    upgradeSelectedBtn.addEventListener("click", upgradeSelected);
  }

  if (updateSelectAllEl) {
    updateSelectAllEl.addEventListener("change", function () {
      var cbs = document.querySelectorAll(".update-check");
      cbs.forEach(function (cb) {
        cb.checked = updateSelectAllEl.checked;
      });
      updateUpgradeSelectedState();
    });
  }

  if (updatesTableBodyEl) {
    updatesTableBodyEl.addEventListener("change", function (e) {
      if (e.target.classList.contains("update-check")) {
        updateUpgradeSelectedState();
        var all = document.querySelectorAll(".update-check");
        var checked = document.querySelectorAll(".update-check:checked");
        if (updateSelectAllEl) {
          updateSelectAllEl.checked =
            all.length > 0 && all.length === checked.length;
        }
      }
    });

    updatesTableBodyEl.addEventListener("click", function (e) {
      var btn = e.target;
      if (
        btn instanceof HTMLButtonElement &&
        btn.dataset.action === "upgrade" &&
        btn.dataset.id
      ) {
        runAction(
          "upgrade",
          btn.dataset.id,
          btn.dataset.packageLabel || btn.dataset.id,
        );
      }
    });
  }

  if (refreshLogsBtn) {
    refreshLogsBtn.addEventListener("click", loadLogs);
  }
  if (logsOriginFilterEl) {
    logsOriginFilterEl.addEventListener("change", renderLogsOutput);
  }
  if (logsSearchInputEl) {
    logsSearchInputEl.addEventListener("input", renderLogsOutput);
  }
  if (clearLogsBtn) {
    clearLogsBtn.addEventListener("click", clearLogs);
  }
  if (copyLogsBtn) {
    copyLogsBtn.addEventListener("click", copyLogs);
  }
  if (exportLogsBtn) {
    exportLogsBtn.addEventListener("click", exportLogs);
  }

  // Auto-scroll toggle: pausa scroll quando usuario faz scroll manual
  if (logsOutputEl) {
    logsOutputEl.addEventListener("scroll", function () {
      var atBottom =
        logsOutputEl.scrollHeight -
          logsOutputEl.scrollTop -
          logsOutputEl.clientHeight <
        40;
      logsOutputEl.dataset.pinned = atBottom ? "true" : "false";
    });
  }

  if (sidebarToggleBtn && sidebarEl) {
    sidebarToggleBtn.addEventListener("click", function () {
      sidebarEl.classList.toggle("collapsed");
      if (typeof syncWindowChromeSidebarWidth === "function") {
        syncWindowChromeSidebarWidth();
      }
    });
  }

  if (refreshInventoryBtn) {
    refreshInventoryBtn.addEventListener("click", function () {
      loadInventory(true);
    });
  }
  if (installOsqueryBtn) {
    installOsqueryBtn.addEventListener("click", installOsquery);
  }
  if (exportInventoryBtn) {
    exportInventoryBtn.addEventListener("click", exportInventory);
  }
  if (exportInventoryPdfBtn) {
    exportInventoryPdfBtn.addEventListener("click", exportInventoryPdf);
  }

  if (softwareSearchInputEl) {
    softwareSearchInputEl.addEventListener(
      "input",
      debounce(applySoftwareFilter, 300),
    );
  }
  if (softwarePrevBtn) {
    softwarePrevBtn.addEventListener("click", function () {
      softwarePage -= 1;
      renderSoftwareTable();
    });
  }
  if (softwareNextBtn) {
    softwareNextBtn.addEventListener("click", function () {
      softwarePage += 1;
      renderSoftwareTable();
    });
  }
  if (refreshSoftwareBtn) {
    refreshSoftwareBtn.addEventListener("click", refreshSoftware);
  }

  if (startupSearchInputEl) {
    startupSearchInputEl.addEventListener(
      "input",
      debounce(applyStartupFilter, 300),
    );
  }
  if (startupPrevBtn) {
    startupPrevBtn.addEventListener("click", function () {
      startupPage -= 1;
      renderStartupTable();
    });
  }
  if (startupNextBtn) {
    startupNextBtn.addEventListener("click", function () {
      startupPage += 1;
      renderStartupTable();
    });
  }
  if (refreshStartupBtn) {
    refreshStartupBtn.addEventListener("click", refreshStartupItems);
  }

  // Network Connections listeners
  if (connectionsSearchInputEl) {
    connectionsSearchInputEl.addEventListener(
      "input",
      debounce(applyConnectionsFilter, 300),
    );
  }
  if (connectionsTabListening) {
    connectionsTabListening.addEventListener("click", function () {
      switchConnectionsTab("listening");
    });
  }
  if (connectionsTabOpen) {
    connectionsTabOpen.addEventListener("click", function () {
      switchConnectionsTab("open");
    });
  }
  if (refreshConnectionsBtn) {
    refreshConnectionsBtn.addEventListener("click", refreshNetworkConnections);
  }
  if (refreshListeningPortsBtn) {
    refreshListeningPortsBtn.addEventListener("click", refreshListeningPorts);
  }
  if (connectionsPrevBtn) {
    connectionsPrevBtn.addEventListener("click", function () {
      connectionsPage -= 1;
      renderConnectionsTable();
    });
  }
  if (connectionsNextBtn) {
    connectionsNextBtn.addEventListener("click", function () {
      connectionsPage += 1;
      renderConnectionsTable();
    });
  }

  // Connections table header sort listeners
  if (listeningPortsTableEl) {
    var listeningHeaders =
      listeningPortsTableEl.querySelectorAll("th.sortable");
    listeningHeaders.forEach(function (th) {
      th.addEventListener("click", function () {
        toggleConnectionsSort(this.dataset.sortKey);
      });
    });
  }
  if (openSocketsTableEl) {
    var openHeaders = openSocketsTableEl.querySelectorAll("th.sortable");
    openHeaders.forEach(function (th) {
      th.addEventListener("click", function () {
        toggleConnectionsSort(this.dataset.sortKey);
      });
    });
  }

  if (catalogPrevBtn) {
    catalogPrevBtn.addEventListener("click", function () {
      catalogPage -= 1;
      renderCards();
    });
  }

  if (catalogNextBtn) {
    catalogNextBtn.addEventListener("click", function () {
      catalogPage += 1;
      renderCards();
    });
  }

  if (redactToggleEl) {
    redactToggleEl.addEventListener("change", function () {
      try {
        appApi().SetExportRedaction(redactToggleEl.checked);
      } catch (_) {
        // API not ready; ignore.
      }
    });
  }

  var thead = document.querySelector(".software-table thead");
  if (thead) {
    thead.addEventListener("click", function (e) {
      var th = e.target.closest("th.sortable");
      if (th && th.dataset.sortKey) {
        toggleSort(th.dataset.sortKey);
      }
    });
  }

  var startupThead = document.querySelector(".startup-table thead");
  if (startupThead) {
    startupThead.addEventListener("click", function (e) {
      var th = e.target.closest("th.sortable");
      if (th && th.dataset.sortKey) {
        toggleStartupSort(th.dataset.sortKey);
      }
    });
  }
}

async function bootstrapApp() {
  if (typeof initApplicationLocale === "function") {
    await initApplicationLocale();
  }
  initAppBindings();
  updateSortIndicators();
  initTheme();
  if (typeof syncWindowChromeSidebarWidth === "function") {
    syncWindowChromeSidebarWidth();
  }
  setRuntimeFlags({ debugMode: false });
  try {
    var flags = await appApi().GetRuntimeFlags();
    setRuntimeFlags(flags || { debugMode: false });
  } catch (_) {
    setRuntimeFlags({ debugMode: false });
  }

  if (typeof syncProvisioningOverlayFromRuntime === "function") {
    await syncProvisioningOverlayFromRuntime();
  }

  if (isDebugRuntimeMode()) {
    setActiveTab("logs");
    loadLogs();
  } else {
    setActiveTab("status");
    loadStatusOverview();
  }

  // Hide tabs based on agent configuration feature flags.
  try {
    var cfg = await appApi().GetAgentConfiguration();
    if (cfg) {
      hideTabIfNeeded(tabStoreBtn, storeViewEl, cfg.appStoreEnabled);
      hideTabIfNeeded(tabChatBtn, chatViewEl, cfg.chatAIEnabled);
      hideTabIfNeeded(tabSupportBtn, supportViewEl, cfg.supportEnabled);
      hideTabIfNeeded(
        tabKnowledgeBtn,
        knowledgeViewEl,
        cfg.knowledgeBaseEnabled,
      );

      // Ensure the active tab is visible (fallback to status)
      var active = document.querySelector(".sidebar-link.active");
      if (active && active.classList.contains("hidden")) {
        setActiveTab("status");
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
  if (typeof initP2PPage === "function") {
    initP2PPage();
  }

  if (
    window.runtime &&
    window.runtime.EventsOn &&
    typeof handleNotificationEvent === "function"
  ) {
    window.runtime.EventsOn("notification:new", handleNotificationEvent);
  }
  if (typeof startUIRuntimeMonitor === "function") {
    startUIRuntimeMonitor("bootstrap");
  }
}

function hideTabIfNeeded(tabBtn, viewEl, flag) {
  if (!tabBtn) return;
  if (flag === false) {
    tabBtn.classList.add("hidden");
    if (viewEl) viewEl.classList.add("hidden");
  }
}

bootstrapApp();
