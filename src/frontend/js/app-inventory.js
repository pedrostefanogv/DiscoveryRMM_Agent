"use strict";

// ---------------------------------------------------------------------------
// Inventory section renderers
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

function renderPrinters(items) {
  renderCardList(printerOutputEl, items, 'Nenhuma impressora encontrada.', function (p) {
    var summary = [
      'Driver: ' + escapeHtml(p.driverName || '-'),
      'Porta: ' + escapeHtml(p.portName || '-'),
      'Status: ' + escapeHtml(p.status || '-'),
      'Padrao: ' + (p.defaultPrinter ? 'sim' : 'nao')
    ].join(' | ');

    return '<div class="network-card">' +
      '<strong>' + escapeHtml(p.name || 'Impressora') + '</strong>' +
      '<span class="meta printer-card-summary">' + summary + '</span>' +
      '<details class="printer-card-details">' +
        '<summary>Detalhes</summary>' +
        '<span class="meta">Compartilhada: ' + (p.shared ? 'sim' : 'nao') + ' | Share: ' + escapeHtml(p.shareName || '-') + '</span>' +
        '<span class="meta">Rede: ' + (p.networkPrinter ? 'sim' : 'nao') + ' | Local: ' + (p.localPrinter ? 'sim' : 'nao') + '</span>' +
        '<span class="meta">Trabalhos: ' + escapeHtml(p.jobCount != null ? p.jobCount : '-') + '</span>' +
        '<span class="meta">KeepPrintedJobs: ' + (p.keepPrintedJobs ? 'sim' : 'nao') + ' | Published: ' + (p.published ? 'sim' : 'nao') + '</span>' +
      '</details>' +
    '</div>';
  });
}

function renderStartupItems(items) {
  inventoryStartupItems = items || [];
  inventoryStartupItemsFiltered = inventoryStartupItems;
  startupPage = 1;
  sortStartupItems();
  updateStartupSortIndicators();
  renderStartupTable();
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
      try { timeStr = new Date(u.time * 1000).toLocaleString(); } catch (e) { timeStr = String(u.time); }
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

function setInventoryLoading(isLoading) {
  if (inventoryProgressEl) {
    inventoryProgressEl.classList.toggle('hidden', !isLoading);
  }
  var buttons = [refreshInventoryBtn, exportInventoryBtn, exportInventoryPdfBtn];
  buttons.forEach(function (btn) {
    if (!btn) return;
    btn.disabled = isLoading;
    btn.setAttribute('aria-busy', String(isLoading));
  });
}

function setInventoryInitialLoadingState(isLoading, message, isError) {
  if (!inventoryInitialLoadingEl || !inventoryContentEl) return;

  var infoMessage = message || 'Coletando dados de inventario...';
  if (inventoryInitialLoadingTextEl) {
    inventoryInitialLoadingTextEl.textContent = infoMessage;
  }

  inventoryInitialLoadingEl.classList.toggle('error', !!isError);
  inventoryInitialLoadingEl.classList.toggle('hidden', !isLoading);
  inventoryContentEl.classList.toggle('hidden', !!isLoading);

  if (inventoryViewEl) {
    inventoryViewEl.setAttribute('aria-busy', String(!!isLoading));
  }
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

function renderStartupTable() {
  if (!inventoryStartupItemsFiltered.length) {
    startupTableBodyEl.innerHTML = '<tr><td colspan="5">Nenhum startup item encontrado.</td></tr>';
    startupCountEl.textContent = 'Total visivel: 0';
    startupPageInfoEl.textContent = 'Pagina 1 de 1';
    startupPrevBtn.disabled = true;
    startupNextBtn.disabled = true;
    return;
  }

  var pg = getPaginationState(inventoryStartupItemsFiltered, startupPage, startupPageSize);
  startupPage = pg.validPage;
  var start = pg.start;
  var end = start + startupPageSize;
  var pageItems = inventoryStartupItemsFiltered.slice(start, end);

  startupTableBodyEl.innerHTML = pageItems.map(function (item) {
    return '<tr>' +
      '<td>' +
        '<details>' +
          '<summary>' + escapeHtml(item.name || '-') + '</summary>' +
          '<div>' +
            '<span><strong>Path:</strong> ' + escapeHtml(item.path || '-') + '</span>' +
            '<span><strong>Args:</strong> ' + escapeHtml(item.args || '-') + '</span>' +
          '</div>' +
        '</details>' +
      '</td>' +
      '<td>' + escapeHtml(item.type || '-') + '</td>' +
      '<td>' + escapeHtml(item.source || '-') + '</td>' +
      '<td>' + escapeHtml(item.status || '-') + '</td>' +
      '<td>' + escapeHtml(item.username || '-') + '</td>' +
    '</tr>';
  }).join('');

  startupCountEl.textContent = 'Total visivel: ' + inventoryStartupItemsFiltered.length + ' | Total inventario: ' + inventoryStartupItems.length;
  startupPageInfoEl.textContent = 'Pagina ' + startupPage + ' de ' + pg.totalPages;
  startupPrevBtn.disabled = startupPage <= 1;
  startupNextBtn.disabled = startupPage >= pg.totalPages;
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

function applyStartupFilter() {
  var q = startupSearchInputEl.value.trim().toLowerCase();
  if (!q) {
    inventoryStartupItemsFiltered = inventoryStartupItems;
  } else {
    inventoryStartupItemsFiltered = inventoryStartupItems.filter(function (item) {
      return (item.name && String(item.name).toLowerCase().includes(q)) ||
             (item.path && String(item.path).toLowerCase().includes(q)) ||
             (item.args && String(item.args).toLowerCase().includes(q)) ||
             (item.type && String(item.type).toLowerCase().includes(q)) ||
             (item.source && String(item.source).toLowerCase().includes(q)) ||
             (item.status && String(item.status).toLowerCase().includes(q)) ||
             (item.username && String(item.username).toLowerCase().includes(q));
    });
  }
  startupPage = 1;
  sortStartupItems();
  renderStartupTable();
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

function sortStartupItems() {
  var key = startupSortKey;
  var dir = startupSortDirection === 'asc' ? 1 : -1;

  inventoryStartupItemsFiltered.sort(function (a, b) {
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

function updateStartupSortIndicators() {
  document.querySelectorAll('.startup-table th.sortable').forEach(function (th) {
    th.classList.remove('asc', 'desc');
    var key = th.dataset.sortKey;
    if (key === startupSortKey) {
      th.classList.add(startupSortDirection);
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

function toggleStartupSort(key) {
  if (startupSortKey === key) {
    startupSortDirection = startupSortDirection === 'asc' ? 'desc' : 'asc';
  } else {
    startupSortKey = key;
    startupSortDirection = 'asc';
  }

  sortStartupItems();
  startupPage = 1;
  updateStartupSortIndicators();
  renderStartupTable();
}

// -----------------------------------------------------------------------
// Network Connections Rendering
// -----------------------------------------------------------------------

function syncConnectionsTabUI() {
  connectionsTabListening.classList.toggle('active', connectionsType === 'listening');
  connectionsTabOpen.classList.toggle('active', connectionsType === 'open');
  listeningPortsTableEl.classList.toggle('hidden', connectionsType !== 'listening');
  openSocketsTableEl.classList.toggle('hidden', connectionsType !== 'open');
}

function setConnectionsLoading(isLoading) {
  connectionsRefreshInFlight = isLoading;
  if (!refreshConnectionsBtn) return;
  refreshConnectionsBtn.disabled = isLoading;
  refreshConnectionsBtn.setAttribute('aria-busy', String(isLoading));
  refreshConnectionsBtn.classList.toggle('is-loading', isLoading);
}

function setConnectionsRefreshStatus(message, isError) {
  if (!connectionsRefreshStatusEl) return;
  connectionsRefreshStatusEl.textContent = message || '';
  connectionsRefreshStatusEl.classList.remove('error');
  if (isError) {
    connectionsRefreshStatusEl.classList.add('error');
  }
}

function renderNetworkConnections(report, options) {
  var preserveState = !!(options && options.preserveState);

  // Populate connections data with type discriminator
  connectionsData.listening = (report.listeningPorts || []).map(function (p) {
    p.type = 'listening';
    return p;
  });
  connectionsData.open = (report.openSockets || []).map(function (s) {
    s.type = 'open';
    return s;
  });

  if (!preserveState) {
    connectionsType = 'listening';
    connectionsSortKey = 'processName';
    connectionsSortDirection = 'asc';
    connectionsPage = 1;
    if (connectionsSearchInputEl) {
      connectionsSearchInputEl.value = '';
    }
  }

  syncConnectionsTabUI();
  if (report && report.collectedAt) {
    setConnectionsRefreshStatus('Atualizado em ' + report.collectedAt, false);
  }
  applyConnectionsFilter();
}

function switchConnectionsTab(type) {
  connectionsType = type;
  
  syncConnectionsTabUI();
  
  // Reset search and pagination
  connectionsSearchInputEl.value = '';
  connectionsPage = 1;
  connectionsSortKey = 'processName';
  connectionsSortDirection = 'asc';
  
  applyConnectionsFilter();
}

async function refreshNetworkConnections() {
  if (connectionsRefreshInFlight) {
    return;
  }

  try {
    setConnectionsLoading(true);
    setConnectionsRefreshStatus('Atualizando...', false);
    showFeedback('Atualizando conexões de rede...');

    var report = await appApi().RefreshNetworkConnections();
    renderNetworkConnections(report, { preserveState: true });

    showFeedback('Conexões de rede atualizadas.');
  } catch (error) {
    setConnectionsRefreshStatus('Falha ao atualizar conexões', true);
    showFeedback(String(error), true);
  } finally {
    setConnectionsLoading(false);
  }
}

async function refreshSoftware() {
  if (!refreshSoftwareBtn) return;

  try {
    refreshSoftwareBtn.disabled = true;
    refreshSoftwareBtn.setAttribute('aria-busy', 'true');
    refreshSoftwareBtn.classList.add('is-loading');
    showFeedback('Atualizando softwares instalados...');

    var software = await appApi().RefreshSoftware();
    if (typeof renderSoftware === 'function') {
      renderSoftware(software);
    }

    showFeedback('Softwares instalados atualizados.');
  } catch (error) {
    showFeedback('Falha ao atualizar softwares: ' + String(error), true);
  } finally {
    refreshSoftwareBtn.disabled = false;
    refreshSoftwareBtn.setAttribute('aria-busy', 'false');
    refreshSoftwareBtn.classList.remove('is-loading');
  }
}

async function refreshStartupItems() {
  if (!refreshStartupBtn) return;

  try {
    refreshStartupBtn.disabled = true;
    refreshStartupBtn.setAttribute('aria-busy', 'true');
    refreshStartupBtn.classList.add('is-loading');
    showFeedback('Atualizando itens de inicializacao...');

    var startupItems = await appApi().RefreshStartupItems();
    if (typeof renderStartupItems === 'function') {
      renderStartupItems(startupItems);
    }

    showFeedback('Itens de inicializacao atualizados.');
  } catch (error) {
    showFeedback('Falha ao atualizar itens de inicializacao: ' + String(error), true);
  } finally {
    refreshStartupBtn.disabled = false;
    refreshStartupBtn.setAttribute('aria-busy', 'false');
    refreshStartupBtn.classList.remove('is-loading');
  }
}

async function refreshListeningPorts() {
  if (!refreshListeningPortsBtn) return;

  try {
    refreshListeningPortsBtn.disabled = true;
    refreshListeningPortsBtn.setAttribute('aria-busy', 'true');
    refreshListeningPortsBtn.classList.add('is-loading');
    showFeedback('Atualizando portas em escuta...');

    var ports = await appApi().RefreshListeningPorts();
    setConnectionsLoading(true);
    setConnectionsRefreshStatus('Atualizando...', false);
    
    if (connectionsData && typeof Array.isArray(connectionsData.listening)) {
      connectionsData.listening = ports || [];
      if (connectionsType === 'listening') {
        applyConnectionsFilter();
      }
    }

    showFeedback('Portas em escuta atualizadas.');
  } catch (error) {
    setConnectionsRefreshStatus('Falha ao atualizar portas', true);
    showFeedback('Falha ao atualizar portas: ' + String(error), true);
  } finally {
    refreshListeningPortsBtn.disabled = false;
    refreshListeningPortsBtn.setAttribute('aria-busy', 'false');
    refreshListeningPortsBtn.classList.remove('is-loading');
    setConnectionsLoading(false);
  }
}

function applyConnectionsFilter() {
  var q = connectionsSearchInputEl.value.trim().toLowerCase();
  var source = connectionsData[connectionsType] || [];
  
  if (!q) {
    connectionsFiltered = source;
  } else {
    connectionsFiltered = source.filter(function (item) {
      var processName = item.processName ? String(item.processName).toLowerCase() : '';
      var processPath = item.processPath ? String(item.processPath).toLowerCase() : '';
      var protocol = item.protocol ? String(item.protocol).toLowerCase() : '';
      var address = item.address ? String(item.address).toLowerCase() : '';
      var port = item.port != null ? String(item.port).toLowerCase() : '';
      var localAddress = item.localAddress ? String(item.localAddress).toLowerCase() : '';
      var localPort = item.localPort != null ? String(item.localPort).toLowerCase() : '';
      var remoteAddress = item.remoteAddress ? String(item.remoteAddress).toLowerCase() : '';
      var remotePort = item.remotePort != null ? String(item.remotePort).toLowerCase() : '';
      
      return processName.includes(q) ||
             processPath.includes(q) ||
             protocol.includes(q) ||
             address.includes(q) ||
             port.includes(q) ||
             localAddress.includes(q) ||
             localPort.includes(q) ||
             remoteAddress.includes(q) ||
             remotePort.includes(q);
    });
  }
  
  // Sort and render
  sortConnections();
  connectionsPage = 1;
  renderConnectionsTable();
}

function sortConnections() {
  var key = connectionsSortKey;
  var dir = connectionsSortDirection === 'asc' ? 1 : -1;

  connectionsFiltered.sort(function (a, b) {
    var av, bv;
    
    if (key === 'processName') {
      av = String(a.processName || '').toLowerCase();
      bv = String(b.processName || '').toLowerCase();
    } else if (key === 'protocol') {
      av = String(a.protocol || '').toLowerCase();
      bv = String(b.protocol || '').toLowerCase();
    } else if (key === 'address' || key === 'localAddress' || key === 'remoteAddress') {
      av = String(a[key] || '').toLowerCase();
      bv = String(b[key] || '').toLowerCase();
    } else if (key === 'port' || key === 'localPort' || key === 'remotePort') {
      av = parseInt(a[key]) || 0;
      bv = parseInt(b[key]) || 0;
      if (av < bv) return -1 * dir;
      if (av > bv) return 1 * dir;
      return 0;
    } else {
      av = String(a[key] || '').toLowerCase();
      bv = String(b[key] || '').toLowerCase();
    }
    
    if (av < bv) return -1 * dir;
    if (av > bv) return 1 * dir;
    return 0;
  });
}

function updateConnectionsSortIndicators() {
  var tableEl = connectionsType === 'listening' ? listeningPortsTableEl : openSocketsTableEl;
  if (!tableEl) return;
  
  tableEl.querySelectorAll('th.sortable').forEach(function (th) {
    th.classList.remove('asc', 'desc');
    var key = th.dataset.sortKey;
    if (key === connectionsSortKey) {
      th.classList.add(connectionsSortDirection);
    }
  });
}

function renderConnectionsTable() {
  if (!connectionsFiltered.length) {
    var emptyMsg = '<tr><td colspan="4">Nenhuma ' + (connectionsType === 'listening' ? 'porta' : 'conexão') + ' encontrada.</td></tr>';
    listeningPortsTableBodyEl.innerHTML = connectionsType === 'listening' ? emptyMsg : '';
    openSocketsTableBodyEl.innerHTML = connectionsType === 'open' ? emptyMsg : '';
    connectionsCountEl.textContent = 'Total: 0';
    connectionsPageInfoEl.textContent = 'Pagina 1 de 1';
    connectionsPrevBtn.disabled = true;
    connectionsNextBtn.disabled = true;
    return;
  }

  var pg = getPaginationState(connectionsFiltered, connectionsPage, connectionsPageSize);
  connectionsPage = pg.validPage;
  var start = pg.start;
  var end = start + connectionsPageSize;
  var pageItems = connectionsFiltered.slice(start, end);

  if (connectionsType === 'listening') {
    listeningPortsTableBodyEl.innerHTML = pageItems.map(buildListeningPortRow).join('');
  } else {
    openSocketsTableBodyEl.innerHTML = pageItems.map(buildOpenSocketRow).join('');
  }

  connectionsCountEl.textContent = 'Total visivel: ' + connectionsFiltered.length + ' | Total inventario: ' + (connectionsData[connectionsType] || []).length;
  connectionsPageInfoEl.textContent = 'Pagina ' + connectionsPage + ' de ' + pg.totalPages;
  connectionsPrevBtn.disabled = connectionsPage <= 1;
  connectionsNextBtn.disabled = connectionsPage >= pg.totalPages;
  
  updateConnectionsSortIndicators();
}

function buildListeningPortRow(item) {
  var processPath = item.processPath || '-';
  var processId = item.processId || '-';
  
  return '<tr>' +
    '<td>' +
      '<details>' +
        '<summary>' + escapeHtml(item.processName || '-') + '</summary>' +
        '<div>' +
          '<span><strong>ID:</strong> ' + escapeHtml(String(processId)) + '</span>' +
          '<span><strong>Path:</strong> ' + escapeHtml(processPath) + '</span>' +
        '</div>' +
      '</details>' +
    '</td>' +
    '<td>' + escapeHtml(item.protocol || '-') + '</td>' +
    '<td>' + escapeHtml(item.address || '-') + '</td>' +
    '<td>' + escapeHtml(item.port != null ? String(item.port) : '-') + '</td>' +
    '</tr>';
}

function buildOpenSocketRow(item) {
  var processPath = item.processPath || '-';
  var processId = item.processId || '-';
  var family = item.family || '-';
  
  var localEndpoint = (item.localAddress || '-') + ':' + (item.localPort != null ? item.localPort : '-');
  var remoteEndpoint = (item.remoteAddress || '-') + ':' + (item.remotePort != null ? item.remotePort : '-');
  
  return '<tr>' +
    '<td>' +
      '<details>' +
        '<summary>' + escapeHtml(item.processName || '-') + '</summary>' +
        '<div>' +
          '<span><strong>ID:</strong> ' + escapeHtml(String(processId)) + '</span>' +
          '<span><strong>Path:</strong> ' + escapeHtml(processPath) + '</span>' +
          '<span><strong>Family:</strong> ' + escapeHtml(family) + '</span>' +
        '</div>' +
      '</details>' +
    '</td>' +
    '<td>' + escapeHtml(item.protocol || '-') + '</td>' +
    '<td>' + escapeHtml(localEndpoint) + '</td>' +
    '<td>' + escapeHtml(remoteEndpoint) + '</td>' +
    '</tr>';
}

function toggleConnectionsSort(key) {
  if (connectionsSortKey === key) {
    connectionsSortDirection = connectionsSortDirection === 'asc' ? 'desc' : 'asc';
  } else {
    connectionsSortKey = key;
    connectionsSortDirection = 'asc';
  }

  sortConnections();
  connectionsPage = 1;
  updateConnectionsSortIndicators();
  renderConnectionsTable();
}

async function loadInventory(forceRefresh) {
  var isInitialLoad = !inventoryLoadedOnce;
  var keepInitialState = false;

  try {
    setInventoryLoading(true);
    inventoryInfoEl.textContent = 'Coletando inventario...';
    showFeedback('Coletando inventario...');

    if (isInitialLoad) {
      setInventoryInitialLoadingState(true, 'Buscando cache local e preparando os dados para exibicao.', false);
    }

    var report = forceRefresh ? await appApi().RefreshInventory() : await appApi().GetInventory();

    inventoryInfoEl.textContent = 'Coletado em ' + (report.collectedAt || '-');
    renderFacts(hardwareOutputEl, report.hardware);
    renderFacts(osOutputEl, report.os);
    renderLoggedUsers(report.loggedInUsers || []);
    renderVolumes(report.volumes || report.disks || []);
    renderNetworks(report.networks || []);
    renderPrinters(report.printers || []);
    renderMemoryModules(report.memoryModules || []);
    renderMonitors(report.monitors || []);
    renderGPUs(report.gpus || []);
    renderBattery(report.battery || []);
    renderBitLocker(report.bitLocker || []);
    renderCPUInfo(report.cpuInfo || []);
    renderStartupItems(report.startupItems || []);
    renderAutoexec(report.autoexec || []);
    renderNetworkConnections(report);

    inventorySoftware = report.software || [];
    inventorySoftwareFiltered = inventorySoftware;
    sortSoftware();
    softwarePage = 1;
    updateSortIndicators();
    renderSoftwareTable();
    inventoryLoadedOnce = true;

    showFeedback('Inventario atualizado.');
    loadSidebarUser();

    if (isInitialLoad) {
      setInventoryInitialLoadingState(false, '', false);
    }
  } catch (error) {
    showFeedback(String(error), true);
    inventoryInfoEl.textContent = 'Falha ao coletar inventario';

    if (isInitialLoad) {
      keepInitialState = true;
      setInventoryInitialLoadingState(true, 'Falha ao coletar os dados iniciais. Clique em Coletar Inventario para tentar novamente.', true);
    }
  } finally {
    setInventoryLoading(false);

    if (isInitialLoad && !keepInitialState) {
      setInventoryInitialLoadingState(false, '', false);
    }
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
    // non-critical
  }
}

async function exportInventory() {
  if (!exportInventoryBtn) return;
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
  if (!exportInventoryPdfBtn) return;
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
  if (!osqueryStatusEl || !installOsqueryBtn) return;
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
  if (!installOsqueryBtn) return;
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
