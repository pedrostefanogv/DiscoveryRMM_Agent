package inventory

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"discovery/internal/ctxutil"
	"discovery/internal/models"
)

// Provider orchestrates inventory collection using osquery.
type Provider struct {
	timeout          time.Duration
	progressMu       sync.RWMutex
	progressCallback func()
}

// NewProvider creates a Provider with the given per-collection timeout.
func NewProvider(timeout time.Duration) *Provider {
	return &Provider{timeout: timeout}
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
//  2. osqueryi launched in socket mode (single init + Thrift calls per query).
func (p *Provider) runQueries(ctx context.Context, binary string, queries []osqueryQuery) map[string]osqueryResult {
	// 1. Try a running osqueryd daemon socket.
	if socketPath := findOsquerydSocket(); socketPath != "" {
		log.Printf("[inventory] usando socket osqueryd em %s", socketPath)
		results := runQueriesViaSocket(ctx, socketPath, queries, p.emitProgressHeartbeat)
		if allRequiredSucceeded(results, queries) {
			return results
		}
		log.Printf("[inventory] socket osqueryd falhou; tentando modo socket do osqueryi")
	}

	// 2. Start osqueryi in socket mode for a single-connection, multi-query session.
	if proc, err := startOsqueryiSocket(ctx, binary); err == nil {
		defer proc.stop()
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
		log.Printf("[inventory] socket osqueryd falhou; tentando modo socket do osqueryi")
	}

	if proc, err := startOsqueryiSocket(ctx, binary); err == nil {
		defer proc.stop()
		log.Printf("[inventory] usando osqueryi em modo socket em %s", proc.socketPath)
		results := runQueriesViaSocket(ctx, proc.socketPath, queries, p.emitProgressHeartbeat)
		if allQueriesSucceeded(results, queries) {
			return results
		}
		log.Printf("[inventory] modo socket do osqueryi falhou")
	}

	return failedQueryResults(queries, fmt.Errorf("falha na execucao via socket (osqueryd/osqueryi)"))
}

// Collect gathers a full inventory report using osquery-only execution.
func (p *Provider) Collect(ctx context.Context) (models.InventoryReport, error) {
	p.emitProgressHeartbeat()
	report, err := p.collectWithOsquery(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}
	p.emitProgressHeartbeat()
	return report, nil
}

// CollectNetworkConnections gathers only listening ports and open sockets.
func (p *Provider) CollectNetworkConnections(ctx context.Context) (models.NetworkConnectionsReport, error) {
	p.emitProgressHeartbeat()
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
		{name: "system_info", sql: "SELECT hostname, hardware_vendor, hardware_model, cpu_brand, cpu_physical_cores, cpu_logical_cores, physical_memory FROM system_info LIMIT 1", required: true},
		{name: "os_version", sql: "SELECT name, version, build, arch FROM os_version LIMIT 1", required: true},
		{name: "baseboard_info", sql: "SELECT manufacturer, model, serial FROM baseboard_info LIMIT 1"},
		{name: "memory_devices", sql: "SELECT handle, array_handle, form_factor, total_width, data_width, size, set, device_locator, bank_locator, memory_type, memory_type_details, max_speed, configured_clock_speed, manufacturer, serial_number, asset_tag, part_number, min_voltage, max_voltage, configured_voltage FROM memory_devices WHERE size > 0"},
		{name: "bios_info", sql: "SELECT vendor, version, date, revision FROM bios_info LIMIT 1"},
		{name: "video_info", sql: "SELECT model, vendor, driver, vram FROM video_info"},
		{name: "battery", sql: "SELECT manufacturer, model, serial_number, cycle_count, state, charging, charged, designed_capacity, max_capacity, current_capacity, percent_remaining, amperage, voltage, minutes_until_empty, minutes_to_full_charge, chemistry, health, condition, manufacture_date FROM battery"},
		{name: "bitlocker_info", sql: "SELECT device_id, drive_letter, persistent_volume_id, conversion_status, protection_status, encryption_method, version, percentage_encrypted, lock_status FROM bitlocker_info"},
		{name: "cpu_info", sql: "SELECT device_id, model, manufacturer, processor_type, cpu_status, number_of_cores, logical_processors, address_width, current_clock_speed, max_clock_speed, socket_designation, availability, load_percentage, number_of_efficiency_cores, number_of_performance_cores FROM cpu_info"},
		{name: "cpuid", sql: "SELECT feature, value, output_register, output_bit, input_eax FROM cpuid"},
		{name: "programs", sql: "SELECT name, version, publisher, identifying_number, uninstall_string FROM programs WHERE name <> ''", required: true},
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

	report := models.InventoryReport{
		CollectedAt: time.Now().Format(time.RFC3339),
		Source:      "osquery",
		Hardware: models.HardwareInfo{
			Hostname:                getString(system, "hostname"),
			Manufacturer:            getString(system, "hardware_vendor"),
			Model:                   getString(system, "hardware_model"),
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
		Software:       mapPrograms(results["programs"].rows, "osquery/programs"),
		StartupItems:   mapStartupItems(get("startup_items")),
		Autoexec:       mapAutoexecItems(get("autoexec")),
		LoggedInUsers:  mapLoggedInUsers(get("logged_in_users")),
	}

	report.Hardware.MemoryModulesCount = len(report.MemoryModules)
	sanitizeHardwareFields(&report)

	report.Disks = report.Volumes
	if len(report.Disks) == 0 {
		report.Disks = report.PhysicalDisks
	}
	p.emitProgressHeartbeat()

	return report, nil
}

// CollectSoftware collects only installed software.
func (p *Provider) CollectSoftware(ctx context.Context) ([]models.SoftwareItem, error) {
	p.emitProgressHeartbeat()

	bin, err := FindOsqueryBinary()
	if err != nil {
		return []models.SoftwareItem{}, err
	}

	runCtx, cancel := ctxutil.WithTimeout(ctx, p.timeout)
	defer cancel()

	queries := []osqueryQuery{
		{name: "programs", sql: "SELECT name, version, publisher, identifying_number, uninstall_string FROM programs WHERE name <> ''", required: true},
	}

	results := p.runQueries(runCtx, bin, queries)
	r := results["programs"]
	if r.err != nil {
		return []models.SoftwareItem{}, fmt.Errorf("falha ao consultar programs: %w", r.err)
	}

	p.emitProgressHeartbeat()
	return mapPrograms(r.rows, "osquery/programs"), nil
}

// CollectStartupItems collects only startup items.
func (p *Provider) CollectStartupItems(ctx context.Context) ([]models.StartupItem, error) {
	p.emitProgressHeartbeat()

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
