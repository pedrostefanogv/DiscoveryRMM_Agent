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

    inventoryInfoEl.textContent = 'Coletado em ' + (report.collectedAt || '-');
    renderFacts(hardwareOutputEl, report.hardware);
    renderFacts(osOutputEl, report.os);
    renderLoggedUsers(report.loggedInUsers || []);
    renderVolumes(report.volumes || report.disks || []);
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
