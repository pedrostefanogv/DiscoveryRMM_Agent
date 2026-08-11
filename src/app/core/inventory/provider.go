package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"discovery/app/core/ctxutil"
	"discovery/app/core/inventory/native"
	"discovery/app/core/models"
	"discovery/app/core/processutil"
)

// Provider orchestrates inventory collection. It uses native collectors as
// the primary strategy (zero subprocess) and falls back to osquery when the
// native collector is unavailable or fails.
type Provider struct {
	timeout          time.Duration
	progressMu       sync.RWMutex
	progressCallback func()
	native           native.Collector
}

// NewProvider creates a Provider with the given per-collection timeout.
func NewProvider(timeout time.Duration) *Provider {
	return &Provider{
		timeout: timeout,
		native:  native.New(),
	}
}

// SetProgressCallback registers a hook called during long-running collection steps.
func (p *Provider) SetProgressCallback(cb func()) {
	p.progressMu.Lock()
	p.progressCallback = cb
	p.progressMu.Unlock()
}

func (p *Provider) emitProgressHeartbeat() {
	p.progressMu.RLock()
	cb := p.progressCallback
	p.progressMu.RUnlock()
	if cb != nil {
		cb()
	}
}

// runQueries selects the best available query strategy and executes all queries.
//
// Priority:
//  1. Running osqueryd socket (fastest – reuses an existing daemon connection).
//  2. Keep-alive pool (osqueryi cached from previous calls within TTL).
//  3. osqueryi launched in socket mode (single init + Thrift calls per query).
func (p *Provider) runQueries(ctx context.Context, binary string, queries []osqueryQuery) map[string]osqueryResult {
	// 1. Try a running osqueryd daemon socket.
	if socketPath := findOsquerydSocket(); socketPath != "" {
		log.Printf("[inventory] usando socket osqueryd em %s", socketPath)
		results := runQueriesViaSocket(ctx, socketPath, queries, p.emitProgressHeartbeat)
		if allRequiredSucceeded(results, queries) {
			return results
		}
		log.Printf("[inventory] socket osqueryd falhou; tentando pool osqueryi")
	}

	// 2. Try the keep-alive pool (reuses osqueryi from previous calls).
	if socketPath := acquireOsqueryiSocket(); socketPath != "" {
		log.Printf("[inventory] usando osqueryi do pool em %s", socketPath)
		results := runQueriesViaSocket(ctx, socketPath, queries, p.emitProgressHeartbeat)
		if allRequiredSucceeded(results, queries) {
			return results
		}
		log.Printf("[inventory] pool osqueryi falhou; tentando novo osqueryi")
	}

	// 3. Start osqueryi in socket mode and store in the keep-alive pool.
	if proc, err := startOsqueryiSocket(ctx, binary); err == nil {
		storeOsqueryiSocket(proc)
		log.Printf("[inventory] usando osqueryi em modo socket em %s", proc.socketPath)
		results := runQueriesViaSocket(ctx, proc.socketPath, queries, p.emitProgressHeartbeat)
		if allRequiredSucceeded(results, queries) {
			return results
		}
		log.Printf("[inventory] modo socket do osqueryi falhou")
	}

	return failedQueryResults(queries, fmt.Errorf("falha na execucao via socket (osqueryd/osqueryi)"))
}

func failedQueryResults(queries []osqueryQuery, err error) map[string]osqueryResult {
	out := make(map[string]osqueryResult, len(queries))
	for _, q := range queries {
		out[q.name] = osqueryResult{name: q.name, err: err}
	}
	return out
}

// allRequiredSucceeded returns true when every required query in the results
// map completed without error and returned at least one row.
func allRequiredSucceeded(results map[string]osqueryResult, queries []osqueryQuery) bool {
	for _, q := range queries {
		if !q.required {
			continue
		}
		r := results[q.name]
		if r.err != nil || len(r.rows) == 0 {
			return false
		}
	}
	return true
}

func allQueriesSucceeded(results map[string]osqueryResult, queries []osqueryQuery) bool {
	for _, q := range queries {
		r := results[q.name]
		if r.err != nil {
			return false
		}
	}
	return true
}

func (p *Provider) runQueriesAllowEmpty(ctx context.Context, binary string, queries []osqueryQuery) map[string]osqueryResult {
	if socketPath := findOsquerydSocket(); socketPath != "" {
		log.Printf("[inventory] usando socket osqueryd em %s", socketPath)
		results := runQueriesViaSocket(ctx, socketPath, queries, p.emitProgressHeartbeat)
		if allQueriesSucceeded(results, queries) {
			return results
		}
		log.Printf("[inventory] socket osqueryd falhou; tentando pool osqueryi")
	}

	// Try the keep-alive pool first.
	if socketPath := acquireOsqueryiSocket(); socketPath != "" {
		log.Printf("[inventory] usando osqueryi do pool em %s", socketPath)
		results := runQueriesViaSocket(ctx, socketPath, queries, p.emitProgressHeartbeat)
		if allQueriesSucceeded(results, queries) {
			return results
		}
		log.Printf("[inventory] pool osqueryi falhou; tentando novo osqueryi")
	}

	if proc, err := startOsqueryiSocket(ctx, binary); err == nil {
		storeOsqueryiSocket(proc)
		log.Printf("[inventory] usando osqueryi em modo socket em %s", proc.socketPath)
		results := runQueriesViaSocket(ctx, proc.socketPath, queries, p.emitProgressHeartbeat)
		if allQueriesSucceeded(results, queries) {
			return results
		}
		log.Printf("[inventory] modo socket do osqueryi falhou")
	}

	return failedQueryResults(queries, fmt.Errorf("falha na execucao via socket (osqueryd/osqueryi)"))
}

// Collect gathers a full inventory report. It prefers the native collector
// (zero subprocess) and falls back to osquery when native is unavailable.
func (p *Provider) Collect(ctx context.Context) (models.InventoryReport, error) {
	p.emitProgressHeartbeat()

	if report, err := p.collectWithNative(ctx); err == nil {
		p.emitProgressHeartbeat()
		return report, nil
	}

	report, err := p.collectWithOsquery(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}
	p.emitProgressHeartbeat()
	return report, nil
}

// collectWithNative assembles a full inventory report using native collectors.
func (p *Provider) collectWithNative(ctx context.Context) (models.InventoryReport, error) {
	if p.native == nil {
		return models.InventoryReport{}, fmt.Errorf("coletor nativo indisponivel")
	}

	hw, osInfo, err := p.native.CollectSystemInfo(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}

	// Hardware details (motherboard, BIOS, GPU, memory, CPU).
	hwDetail, memoryModules, gpus, cpus, cpuFeatures, err := p.native.CollectHardware(ctx)
	if err == nil {
		hw = mergeHardwareInfo(hw, hwDetail)
	}

	volumes, physicalDisks, err := p.native.CollectDisks(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}

	networks, err := p.native.CollectNetworks(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}

	listeningPorts, openSockets, err := p.native.CollectNetworkConnections(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}

	software, err := p.native.CollectSoftware(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}

	startupItems, err := p.native.CollectStartupItems(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}

	loggedInUsers, err := p.native.CollectLoggedInUsers(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}

	bitLocker, err := p.native.CollectBitLocker(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}

	battery, err := p.native.CollectBattery(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}

	// Media types for volumes/disks.
	mediaTypes := p.native.CollectDiskMediaTypes(ctx)
	for i := range volumes {
		dl := normalizeDriveKey(volumes[i].Device)
		if mt, ok := mediaTypes[dl]; ok {
			volumes[i].MediaType = mt
		}
	}
	for i := range physicalDisks {
		dl := normalizeDriveKey(physicalDisks[i].Device)
		if mt, ok := mediaTypes[dl]; ok {
			physicalDisks[i].MediaType = mt
		}
	}

	// SMART/health data for volumes (keyed by drive letter).
	smart := p.native.CollectSmartHealth(ctx)
	for i := range volumes {
		dl := normalizeDriveKey(volumes[i].Device)
		sh, ok := smart[dl]
		if !ok {
			continue
		}
		volumes[i].SmartStatus = mapSmartStatus(sh.HealthStatus)
		if sh.TemperatureC > 0 {
			t := sh.TemperatureC
			volumes[i].TemperatureC = &t
		}
		if sh.PowerOnHours > 0 {
			p := sh.PowerOnHours
			volumes[i].PowerOnHours = &p
		}
		// Erros acumulados (leitura + escrita) são o sinal mais relevante de
		// degradação. Wear (desgaste SSD) não é mapeado para ReallocatedSectors
		// para não misturar semânticas distintas.
		if sh.ReadErrorsTotal > 0 || sh.WriteErrorsTotal > 0 {
			r := sh.ReadErrorsTotal + sh.WriteErrorsTotal
			volumes[i].ReallocatedSectors = &r
		}
	}

	hw.MemoryModulesCount = len(memoryModules)

	report := models.InventoryReport{
		CollectedAt:    time.Now().Format(time.RFC3339),
		Source:         "native",
		Hardware:       hw,
		OS:             osInfo,
		LoggedInUsers:  loggedInUsers,
		Battery:        battery,
		BitLocker:      bitLocker,
		CPUInfo:        cpus,
		CPUFeatures:    cpuFeatures,
		MemoryModules:  memoryModules,
		GPUs:           gpus,
		Volumes:        volumes,
		PhysicalDisks:  physicalDisks,
		Disks:          volumes,
		Networks:       networks,
		ListeningPorts: listeningPorts,
		OpenSockets:    openSockets,
		Software:       software,
		StartupItems:   startupItems,
	}

	if len(report.Disks) == 0 {
		report.Disks = report.PhysicalDisks
	}

	sanitizeHardwareFields(&report)
	return report, nil
}

// mergeHardwareInfo merges basic hardware info with detailed hardware info,
// preferring non-empty values from the detail.
func mergeHardwareInfo(base, detail models.HardwareInfo) models.HardwareInfo {
	if base.Manufacturer == "" {
		base.Manufacturer = detail.Manufacturer
	}
	if base.Model == "" {
		base.Model = detail.Model
	}
	if base.SerialNumber == "" {
		base.SerialNumber = detail.SerialNumber
	}
	if base.MotherboardManufacturer == "" {
		base.MotherboardManufacturer = detail.MotherboardManufacturer
	}
	if base.MotherboardModel == "" {
		base.MotherboardModel = detail.MotherboardModel
	}
	if base.MotherboardSerial == "" {
		base.MotherboardSerial = detail.MotherboardSerial
	}
	if base.BIOSVendor == "" {
		base.BIOSVendor = detail.BIOSVendor
	}
	if base.BIOSVersion == "" {
		base.BIOSVersion = detail.BIOSVersion
	}
	if base.BIOSReleaseDate == "" {
		base.BIOSReleaseDate = detail.BIOSReleaseDate
	}
	if base.BIOSSerial == "" {
		base.BIOSSerial = detail.BIOSSerial
	}
	// Núcleos/threads: o detalhe WMI (Win32_Processor) é autoritativo e
	// corrige o fallback do registro/systeminfo (Windows moderno não expõe
	// ProcessorCoreCount/ProcessorLogicalCount — bug "0C / 0T" no card).
	// Só sobrescreve quando há um valor real do WMI (nunca um placeholder).
	if detail.Cores > 0 {
		base.Cores = detail.Cores
	}
	if detail.LogicalCores > 0 {
		base.LogicalCores = detail.LogicalCores
	}
	return base
}

// CollectNetworkConnections gathers only listening ports and open sockets.
func (p *Provider) CollectNetworkConnections(ctx context.Context) (models.NetworkConnectionsReport, error) {
	p.emitProgressHeartbeat()

	if p.native != nil {
		if listening, open, err := p.native.CollectNetworkConnections(ctx); err == nil {
			p.emitProgressHeartbeat()
			return models.NetworkConnectionsReport{
				CollectedAt:    time.Now().Format(time.RFC3339),
				Source:         "native",
				ListeningPorts: listening,
				OpenSockets:    open,
			}, nil
		}
	}

	report, err := p.collectNetworkConnectionsWithOsquery(ctx)
	if err != nil {
		return models.NetworkConnectionsReport{}, err
	}
	p.emitProgressHeartbeat()
	return report, nil
}

func (p *Provider) collectNetworkConnectionsWithOsquery(ctx context.Context) (models.NetworkConnectionsReport, error) {
	bin, err := FindOsqueryBinary()
	if err != nil {
		return models.NetworkConnectionsReport{}, err
	}

	runCtx, cancel := ctxutil.WithTimeout(ctx, p.timeout)
	defer cancel()

	queries := []osqueryQuery{
		{name: "listening_ports", sql: "SELECT p.name AS process_name, p.pid AS pid, p.path AS process_path, l.protocol, l.address, l.port FROM listening_ports l JOIN processes p USING (pid) WHERE l.port != 0"},
		{name: "open_sockets", sql: "SELECT p.name AS process_name, p.pid AS pid, p.path AS process_path, s.local_address, s.local_port, s.remote_address, s.remote_port, s.protocol, s.family FROM process_open_sockets s JOIN processes p USING (pid) WHERE s.remote_port != 0"},
	}

	results := p.runQueriesAllowEmpty(runCtx, bin, queries)
	for _, q := range queries {
		r := results[q.name]
		if r.err != nil {
			return models.NetworkConnectionsReport{}, fmt.Errorf("falha ao consultar %s: %w", q.name, r.err)
		}
	}

	get := func(name string) []map[string]any {
		r := results[name]
		if r.err != nil || len(r.rows) == 0 {
			return []map[string]any{}
		}
		return r.rows
	}

	p.emitProgressHeartbeat()

	return models.NetworkConnectionsReport{
		CollectedAt:    time.Now().Format(time.RFC3339),
		Source:         "osquery",
		ListeningPorts: mapListeningPorts(get("listening_ports")),
		OpenSockets:    mapOpenSockets(get("open_sockets")),
	}, nil
}

func softwareInventoryQueries(programsRequired bool) []osqueryQuery {
	return []osqueryQuery{
		{
			name:     "programs",
			sql:      "SELECT name, version, publisher, identifying_number AS install_id, uninstall_string, install_date, install_source FROM programs WHERE name <> ''",
			required: programsRequired,
		},
		{
			name: "chocolatey_packages",
			sql:  "SELECT name, version, author AS publisher, path AS install_id, '' AS uninstall_string, '' AS install_date, '' AS install_source FROM chocolatey_packages WHERE name <> ''",
		},
		{
			name: "npm_packages",
			sql:  "SELECT name, version, author AS publisher, path AS install_id, '' AS uninstall_string, '' AS install_date, path AS install_source FROM npm_packages WHERE name <> ''",
		},
		{
			name: "python_packages",
			sql:  "SELECT name, version, summary AS publisher, directory AS install_id, '' AS uninstall_string, '' AS install_date, directory AS install_source FROM python_packages WHERE name <> ''",
		},
	}
}

func buildSoftwareInventoryFromResults(results map[string]osqueryResult) []models.SoftwareItem {
	rowsFor := func(name string) []map[string]any {
		r := results[name]
		if r.err != nil || len(r.rows) == 0 {
			return nil
		}
		return r.rows
	}

	return mergeSoftwareInventories(
		mapPrograms(rowsFor("programs"), "osquery/programs"),
		mapPrograms(rowsFor("chocolatey_packages"), "osquery/chocolatey_packages"),
		mapPrograms(rowsFor("npm_packages"), "osquery/npm_packages"),
		mapPrograms(rowsFor("python_packages"), "osquery/python_packages"),
	)
}

// collectWithOsquery runs all osquery queries and assembles the report.
//
// Query execution strategy (tried in order):
//  1. Running osqueryd socket – connect via osquery-go Thrift client; single
//     connection handles all queries without per-query subprocess overhead.
//  2. osqueryi in socket mode – launch osqueryi once with --extensions_socket,
//     wait for it to be ready, then query via the same Thrift client.
func (p *Provider) collectWithOsquery(ctx context.Context) (models.InventoryReport, error) {
	bin, err := FindOsqueryBinary()
	if err != nil {
		return models.InventoryReport{}, err
	}

	// Create one timeout context shared by all queries.
	runCtx, cancel := ctxutil.WithTimeout(ctx, p.timeout)
	defer cancel()

	queries := []osqueryQuery{
		{name: "system_info", sql: "SELECT hostname, hardware_vendor, hardware_model, hardware_serial, cpu_brand, cpu_physical_cores, cpu_logical_cores, physical_memory FROM system_info LIMIT 1", required: true},
		{name: "os_version", sql: "SELECT name, version, build, arch FROM os_version LIMIT 1", required: true},
		{name: "baseboard_info", sql: "SELECT manufacturer, model, serial FROM baseboard_info LIMIT 1"},
		{name: "memory_devices", sql: "SELECT handle, array_handle, form_factor, total_width, data_width, size, set, device_locator, bank_locator, memory_type, memory_type_details, max_speed, configured_clock_speed, manufacturer, serial_number, asset_tag, part_number, min_voltage, max_voltage, configured_voltage FROM memory_devices WHERE size > 0"},
		{name: "bios_info", sql: "SELECT vendor, version, date, revision, serial FROM bios_info LIMIT 1"},
		{name: "video_info", sql: "SELECT model, vendor, driver, vram FROM video_info"},
		{name: "battery", sql: "SELECT manufacturer, model, serial_number, cycle_count, state, charging, charged, designed_capacity, max_capacity, current_capacity, percent_remaining, amperage, voltage, minutes_until_empty, minutes_to_full_charge, chemistry, health, condition, manufacture_date FROM battery"},
		{name: "bitlocker_info", sql: "SELECT device_id, drive_letter, persistent_volume_id, conversion_status, protection_status, encryption_method, version, percentage_encrypted, lock_status FROM bitlocker_info"},
		{name: "cpu_info", sql: "SELECT device_id, model, manufacturer, processor_type, cpu_status, number_of_cores, logical_processors, address_width, current_clock_speed, max_clock_speed, socket_designation, availability, load_percentage, number_of_efficiency_cores, number_of_performance_cores FROM cpu_info"},
		{name: "cpuid", sql: "SELECT feature, value, output_register, output_bit, input_eax FROM cpuid"},
		{name: "programs", sql: "SELECT name, version, publisher, identifying_number AS install_id, uninstall_string, install_date, install_source FROM programs WHERE name <> ''", required: true},
		{name: "chocolatey_packages", sql: "SELECT name, version, author AS publisher, path AS install_id, '' AS uninstall_string, '' AS install_date, '' AS install_source FROM chocolatey_packages WHERE name <> ''"},
		{name: "npm_packages", sql: "SELECT name, version, author AS publisher, path AS install_id, '' AS uninstall_string, '' AS install_date, path AS install_source FROM npm_packages WHERE name <> ''"},
		{name: "python_packages", sql: "SELECT name, version, summary AS publisher, directory AS install_id, '' AS uninstall_string, '' AS install_date, directory AS install_source FROM python_packages WHERE name <> ''"},
		{name: "startup_items", sql: "SELECT name, path, args, type, source, status, username FROM startup_items"},
		{name: "autoexec", sql: "SELECT path, name, source FROM autoexec"},
		{name: "logged_in_users", sql: "SELECT user, type, tty, host, pid, sid, registry_hive, time FROM logged_in_users"},
		{name: "disk_info", sql: "SELECT partitions, disk_index, type, id, pnp_device_id, disk_size, manufacturer, hardware_model, name, serial, description FROM disk_info"},
		{name: "logical_drives", sql: "SELECT device_id, type, description, free_space, size, file_system, boot_partition FROM logical_drives WHERE size <> '-1'"},
		{name: "interface_details", sql: "SELECT interface, mac, type, mtu, link_speed, friendly_name, description, manufacturer, connection_status, enabled, physical_adapter, dhcp_enabled, dns_server_search_order FROM interface_details"},
		{name: "interface_addresses", sql: "SELECT interface, address, mask FROM interface_addresses WHERE address <> ''"},
		{name: "routes", sql: "SELECT interface, gateway, destination FROM routes WHERE destination IN ('0.0.0.0', '::')"},
		{name: "listening_ports", sql: "SELECT p.name AS process_name, p.pid AS pid, p.path AS process_path, l.protocol, l.address, l.port FROM listening_ports l JOIN processes p USING (pid) WHERE l.port != 0"},
		{name: "open_sockets", sql: "SELECT p.name AS process_name, p.pid AS pid, p.path AS process_path, s.local_address, s.local_port, s.remote_address, s.remote_port, s.protocol, s.family FROM process_open_sockets s JOIN processes p USING (pid) WHERE s.remote_port != 0"},
	}

	results := p.runQueries(runCtx, bin, queries)

	// Check required queries.
	for _, q := range queries {
		if !q.required {
			continue
		}
		r := results[q.name]
		if r.err != nil {
			return models.InventoryReport{}, fmt.Errorf("falha ao consultar %s: %w", q.name, r.err)
		}
		if len(r.rows) == 0 {
			return models.InventoryReport{}, fmt.Errorf("falha ao consultar %s: resultado vazio", q.name)
		}
	}

	// Convenience accessors (non-required queries default to empty slices).
	get := func(name string) []map[string]any {
		r := results[name]
		if r.err != nil || len(r.rows) == 0 {
			return []map[string]any{}
		}
		return r.rows
	}

	system := results["system_info"].rows[0]
	osInfo := results["os_version"].rows[0]

	memoryBytes := parseFloat(getString(system, "physical_memory"))
	memoryGB := memoryBytes / bytesPerGB
	// osquery system_info.physical_memory retorna 0 em VMs com NUMA ativado.
	// Fallback: API nativa GlobalMemoryStatusEx (kernel32.dll) — mesma usada no heartbeat.
	if memoryBytes <= 0 {
		if totalGB, _, _, ok := collectWindowsMemoryNative(); ok && totalGB > 0 {
			memoryGB = totalGB
		}
	}

	report := models.InventoryReport{
		CollectedAt: time.Now().Format(time.RFC3339),
		Source:      "osquery",
		Hardware: models.HardwareInfo{
			Hostname:                getString(system, "hostname"),
			Manufacturer:            getString(system, "hardware_vendor"),
			Model:                   getString(system, "hardware_model"),
			SerialNumber:            getString(system, "hardware_serial"),
			CPU:                     getString(system, "cpu_brand"),
			LogicalCores:            parseInt(getString(system, "cpu_logical_cores")),
			Cores:                   parseInt(getString(system, "cpu_physical_cores")),
			MemoryGB:                round2(memoryGB),
			MotherboardManufacturer: getString(firstRow(get("baseboard_info")), "manufacturer"),
			MotherboardModel:        getString(firstRow(get("baseboard_info")), "model"),
			MotherboardSerial:       getString(firstRow(get("baseboard_info")), "serial"),
			BIOSVendor:              getString(firstRow(get("bios_info")), "vendor"),
			BIOSVersion:             getString(firstRow(get("bios_info")), "version"),
			BIOSReleaseDate:         firstNonEmpty(getString(firstRow(get("bios_info")), "date"), getString(firstRow(get("bios_info")), "revision")),
			BIOSSerial:              getString(firstRow(get("bios_info")), "serial"),
		},
		OS: models.OperatingSystem{
			Name:         getString(osInfo, "name"),
			Version:      getString(osInfo, "version"),
			Build:        getString(osInfo, "build"),
			Architecture: getString(osInfo, "arch"),
		},
		Battery:        mapBatteryRows(get("battery")),
		BitLocker:      mapBitLockerRows(get("bitlocker_info")),
		CPUInfo:        mapCPUInfoRows(get("cpu_info")),
		CPUFeatures:    mapCPUFeatures(get("cpuid")),
		MemoryModules:  mapMemoryModules(get("memory_devices")),
		Monitors:       []models.MonitorInfo{},
		GPUs:           mapGPURows(get("video_info")),
		Volumes:        mapLogicalDrives(get("logical_drives")),
		PhysicalDisks:  mapPhysicalDisks(get("disk_info")),
		Networks:       mapNetworkRows(get("interface_details"), get("interface_addresses"), get("routes")),
		ListeningPorts: mapListeningPorts(get("listening_ports")),
		OpenSockets:    mapOpenSockets(get("open_sockets")),
		Software:       buildSoftwareInventoryFromResults(results),
		StartupItems:   mapStartupItems(get("startup_items")),
		Autoexec:       mapAutoexecItems(get("autoexec")),
		LoggedInUsers:  mapLoggedInUsers(get("logged_in_users")),
	}

	report.Hardware.MemoryModulesCount = len(report.MemoryModules)
	sanitizeHardwareFields(&report)

	// Fallback WMI/CIM: em VMs (QEMU/KVM/Proxmox) o DMI/SMBIOS não é
	// exposto ao osquery. Preenche campos vazios (serial, motherboard, BIOS)
	// usando consultas WMI diretas.
	enrichHardwareFromWMI(&report.Hardware)

	diskMediaTypes := collectPhysicalDiskMediaTypes()
	for i := range report.Volumes {
		dl := strings.ToUpper(strings.TrimSpace(report.Volumes[i].Device))
		if mt, ok := diskMediaTypes[dl]; ok {
			report.Volumes[i].MediaType = mt
		}
	}
	for i := range report.PhysicalDisks {
		dl := strings.ToUpper(strings.TrimSpace(report.PhysicalDisks[i].Device))
		if mt, ok := diskMediaTypes[dl]; ok {
			report.PhysicalDisks[i].MediaType = mt
		}
	}

	report.Disks = report.Volumes
	if len(report.Disks) == 0 {
		report.Disks = report.PhysicalDisks
	}
	p.emitProgressHeartbeat()

	return report, nil
}

// mapSmartStatus normalizes the Windows HealthStatus string into a compact
// status used by the API/frontend.
func mapSmartStatus(health string) string {
	switch strings.ToLower(strings.TrimSpace(health)) {
	case "healthy":
		return "OK"
	case "warning":
		return "Atenção"
	case "unhealthy":
		return "Falha prevista"
	default:
		return "Indisponível"
	}
}

// normalizeDriveKey normalizes a volume device string (e.g. "C:\" or "C:")
// into a canonical drive-letter key ("C:") used to look up media types and
// SMART health maps.
func normalizeDriveKey(device string) string {
	key := strings.ToUpper(strings.TrimSpace(device))
	key = strings.TrimRight(key, `\`)
	if len(key) >= 2 && key[1] == ':' {
		return key[:2]
	}
	return key
}

// collectPhysicalDiskMediaTypes queries Get-PhysicalDisk via PowerShell to
// determine whether each logical drive resides on an SSD or HDD. Returns a
// map of drive letter (e.g. "C:") to media type ("SSD" / "HDD" / "UnSpecified").
func collectPhysicalDiskMediaTypes() map[string]string {
	if runtime.GOOS != "windows" {
		return nil
	}

	script := `$ErrorActionPreference = 'Stop'
@(Get-PhysicalDisk | ForEach-Object {
    $disk = $_
    Get-Disk -Number $_.DeviceId | Get-Partition |
        Where-Object { $_.DriveLetter } |
        ForEach-Object {
            [PSCustomObject]@{
                DriveLetter = $_.DriveLetter + ':'
                MediaType   = $disk.MediaType
            }
        }
}) | ConvertTo-Json -Depth 2 -Compress`

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[inventory] falha ao consultar Get-PhysicalDisk: %v", err)
		return nil
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" {
		return nil
	}

	type diskPartition struct {
		DriveLetter string `json:"DriveLetter"`
		MediaType   string `json:"MediaType"`
	}

	var partitions []diskPartition
	if err := json.Unmarshal([]byte(trimmed), &partitions); err != nil {
		log.Printf("[inventory] falha ao parsear saida do Get-PhysicalDisk: %v", err)
		return nil
	}

	m := make(map[string]string, len(partitions))
	for _, p := range partitions {
		dl := strings.ToUpper(strings.TrimSpace(p.DriveLetter))
		mt := strings.TrimSpace(p.MediaType)
		if dl != "" && mt != "" {
			m[dl] = mt
		}
	}

	return m
}

// CollectSoftware collects only installed software.
func (p *Provider) CollectSoftware(ctx context.Context) ([]models.SoftwareItem, error) {
	p.emitProgressHeartbeat()

	if p.native != nil {
		if items, err := p.native.CollectSoftware(ctx); err == nil {
			p.emitProgressHeartbeat()
			return items, nil
		}
	}

	bin, err := FindOsqueryBinary()
	if err != nil {
		return []models.SoftwareItem{}, err
	}

	runCtx, cancel := ctxutil.WithTimeout(ctx, p.timeout)
	defer cancel()

	queries := softwareInventoryQueries(true)

	results := p.runQueries(runCtx, bin, queries)
	r := results["programs"]
	if r.err != nil {
		return []models.SoftwareItem{}, fmt.Errorf("falha ao consultar programs: %w", r.err)
	}

	p.emitProgressHeartbeat()
	return buildSoftwareInventoryFromResults(results), nil
}

// CollectStartupItems collects only startup items.
func (p *Provider) CollectStartupItems(ctx context.Context) ([]models.StartupItem, error) {
	p.emitProgressHeartbeat()

	if p.native != nil {
		if items, err := p.native.CollectStartupItems(ctx); err == nil {
			p.emitProgressHeartbeat()
			return items, nil
		}
	}

	bin, err := FindOsqueryBinary()
	if err != nil {
		return []models.StartupItem{}, err
	}

	runCtx, cancel := ctxutil.WithTimeout(ctx, p.timeout)
	defer cancel()

	queries := []osqueryQuery{
		{name: "startup_items", sql: "SELECT name, path, args, type, source, status, username FROM startup_items"},
	}

	results := p.runQueriesAllowEmpty(runCtx, bin, queries)
	r := results["startup_items"]
	if r.err != nil {
		return []models.StartupItem{}, fmt.Errorf("falha ao consultar startup_items: %w", r.err)
	}

	p.emitProgressHeartbeat()
	return mapStartupItems(r.rows), nil
}

// CollectListeningPorts collects only listening ports.
func (p *Provider) CollectListeningPorts(ctx context.Context) ([]models.ListeningPortInfo, error) {
	p.emitProgressHeartbeat()

	if p.native != nil {
		if listening, _, err := p.native.CollectNetworkConnections(ctx); err == nil {
			p.emitProgressHeartbeat()
			return listening, nil
		}
	}

	bin, err := FindOsqueryBinary()
	if err != nil {
		return []models.ListeningPortInfo{}, err
	}

	runCtx, cancel := ctxutil.WithTimeout(ctx, p.timeout)
	defer cancel()

	queries := []osqueryQuery{
		{name: "listening_ports", sql: "SELECT p.name AS process_name, p.pid AS pid, p.path AS process_path, l.protocol, l.address, l.port FROM listening_ports l JOIN processes p USING (pid) WHERE l.port != 0"},
	}

	results := p.runQueriesAllowEmpty(runCtx, bin, queries)
	r := results["listening_ports"]
	if r.err != nil {
		return []models.ListeningPortInfo{}, fmt.Errorf("falha ao consultar listening_ports: %w", r.err)
	}

	p.emitProgressHeartbeat()
	return mapListeningPorts(r.rows), nil
}
