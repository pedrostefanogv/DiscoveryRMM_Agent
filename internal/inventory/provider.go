package inventory

import (
	"context"
	"fmt"
	"log"
	"time"

	"winget-store/internal/ctxutil"
	"winget-store/internal/models"
)

// Provider orchestrates inventory collection using osquery (preferred)
// with a PowerShell fallback.
type Provider struct {
	timeout time.Duration
}

// NewProvider creates a Provider with the given per-collection timeout.
func NewProvider(timeout time.Duration) *Provider {
	return &Provider{timeout: timeout}
}

// Collect gathers a full inventory report. It tries osquery first; if
// osquery is unavailable or fails, it falls back to PowerShell/CIM.
func (p *Provider) Collect(ctx context.Context) (models.InventoryReport, error) {
	if report, err := p.collectWithOsquery(ctx); err == nil {
		return report, nil
	}

	report, err := p.collectWithPowerShell(ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}
	return report, nil
}

// collectWithOsquery runs all osquery queries in parallel under a single
// shared timeout context, then assembles the report.
func (p *Provider) collectWithOsquery(ctx context.Context) (models.InventoryReport, error) {
	bin, err := FindOsqueryBinary()
	if err != nil {
		return models.InventoryReport{}, err
	}

	// Create one timeout context shared by all parallel queries.
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
	}

	results := runParallelQueries(runCtx, bin, queries)

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
		Battery:       mapBatteryRows(get("battery")),
		BitLocker:     mapBitLockerRows(get("bitlocker_info")),
		CPUInfo:       mapCPUInfoRows(get("cpu_info")),
		CPUFeatures:   mapCPUFeatures(get("cpuid")),
		MemoryModules: mapMemoryModules(get("memory_devices")),
		Monitors:      []models.MonitorInfo{},
		GPUs:          mapGPURows(get("video_info")),
		Volumes:       mapLogicalDrives(get("logical_drives")),
		PhysicalDisks: mapPhysicalDisks(get("disk_info")),
		Networks:      mapNetworkRows(get("interface_details"), get("interface_addresses"), get("routes")),
		Software:      mapPrograms(results["programs"].rows, "osquery/programs"),
		StartupItems:  mapStartupItems(get("startup_items")),
		Autoexec:      mapAutoexecItems(get("autoexec")),
		LoggedInUsers: mapLoggedInUsers(get("logged_in_users")),
	}

	report.Hardware.MemoryModulesCount = len(report.MemoryModules)

	if details, derr := collectWindowsHardwareDetails(runCtx); derr == nil {
		if report.Hardware.MotherboardSerial == "" {
			report.Hardware.MotherboardSerial = details.MotherboardSerial
		}
		if report.Hardware.MotherboardManufacturer == "" {
			report.Hardware.MotherboardManufacturer = details.MotherboardManufacturer
		}
		if report.Hardware.MotherboardModel == "" {
			report.Hardware.MotherboardModel = details.MotherboardModel
		}
		if report.Hardware.BIOSVendor == "" {
			report.Hardware.BIOSVendor = details.BIOSVendor
		}
		if report.Hardware.BIOSVersion == "" {
			report.Hardware.BIOSVersion = details.BIOSVersion
		}
		if report.Hardware.BIOSReleaseDate == "" {
			report.Hardware.BIOSReleaseDate = details.BIOSReleaseDate
		}
		if report.Hardware.BIOSSerial == "" {
			report.Hardware.BIOSSerial = details.BIOSSerial
		}
		if len(report.MemoryModules) == 0 {
			report.MemoryModules = details.MemoryModules
			report.Hardware.MemoryModulesCount = len(report.MemoryModules)
		}
		if len(report.GPUs) == 0 {
			report.GPUs = details.GPUs
		}
		report.Monitors = details.Monitors
	}
	sanitizeHardwareFields(&report)

	report.Disks = report.Volumes
	if len(report.Disks) == 0 {
		report.Disks = report.PhysicalDisks
	}

	return report, nil
}

// collectWithPowerShell delegates to the PowerShell fallback with a timeout context.
func (p *Provider) collectWithPowerShell(ctx context.Context) (models.InventoryReport, error) {
	runCtx, cancel := ctxutil.WithTimeout(ctx, p.timeout)
	defer cancel()

	report, err := collectWithPowerShell(runCtx)
	if err != nil {
		return models.InventoryReport{}, err
	}

	log.Printf("[inventory] coleta via powershell concluida: %d softwares, %d volumes",
		len(report.Software), len(report.Volumes))

	return report, nil
}
