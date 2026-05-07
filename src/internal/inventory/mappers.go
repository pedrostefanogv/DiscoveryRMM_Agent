package inventory

import (
	"sort"
	"strings"

	"discovery/internal/models"
)

// -----------------------------------------------------------------------
// Software
// -----------------------------------------------------------------------

func mapPrograms(rows []map[string]any, source string) []models.SoftwareItem {
	if len(rows) == 0 {
		return nil
	}
	items := make([]models.SoftwareItem, 0, len(rows))
	for _, row := range rows {
		name := getString(row, "name")
		if strings.TrimSpace(name) == "" {
			continue
		}
		installID := firstNonEmpty(
			getString(row, "install_id"),
			getString(row, "identifying_number"),
			getString(row, "package_id"),
			getString(row, "path"),
			getString(row, "directory"),
			getString(row, "uninstall_string"),
		)
		installSource := firstNonEmpty(
			getString(row, "install_source"),
			getString(row, "path"),
			getString(row, "directory"),
		)
		items = append(items, models.SoftwareItem{
			Name:          name,
			Version:       getString(row, "version"),
			Publisher:     firstNonEmpty(getString(row, "publisher"), getString(row, "summary"), getString(row, "author")),
			InstallID:     installID,
			Serial:        firstNonEmpty(getString(row, "serial"), getString(row, "identifying_number"), installID, getString(row, "guid"), installSource, getString(row, "uninstall_string")),
			Source:        source,
			InstallDate:   getString(row, "install_date"),
			InstallSource: installSource,
		})
	}
	return items
}

func mergeSoftwareInventories(groups ...[]models.SoftwareItem) []models.SoftwareItem {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	if total == 0 {
		return nil
	}

	items := make([]models.SoftwareItem, 0, total)
	byKey := make(map[string]int, total)

	for _, group := range groups {
		for _, item := range group {
			key := softwareIdentityKey(item)
			if key == "" {
				continue
			}
			if idx, ok := byKey[key]; ok {
				items[idx] = mergeSoftwareItemValues(items[idx], item)
				continue
			}
			byKey[key] = len(items)
			items = append(items, item)
		}
	}

	if len(items) == 0 {
		return nil
	}

	sort.Slice(items, func(i, j int) bool {
		return softwareSortKey(items[i]) < softwareSortKey(items[j])
	})

	return items
}

func softwareIdentityKey(item models.SoftwareItem) string {
	name := strings.ToLower(strings.TrimSpace(item.Name))
	if name == "" {
		return ""
	}
	return name + "|" +
		strings.TrimSpace(item.Version) + "|" +
		strings.ToLower(strings.TrimSpace(item.Publisher)) + "|" +
		strings.TrimSpace(item.InstallID) + "|" +
		strings.ToLower(strings.TrimSpace(item.Source))
}

func softwareSortKey(item models.SoftwareItem) string {
	return strings.ToLower(strings.TrimSpace(item.Name)) + "|" +
		strings.TrimSpace(item.Version) + "|" +
		strings.ToLower(strings.TrimSpace(item.Publisher)) + "|" +
		strings.ToLower(strings.TrimSpace(item.Source)) + "|" +
		strings.TrimSpace(item.InstallID)
}

func mergeSoftwareItemValues(current, incoming models.SoftwareItem) models.SoftwareItem {
	if strings.TrimSpace(current.Version) == "" {
		current.Version = incoming.Version
	}
	if strings.TrimSpace(current.Publisher) == "" {
		current.Publisher = incoming.Publisher
	}
	if strings.TrimSpace(current.InstallID) == "" {
		current.InstallID = incoming.InstallID
	}
	if strings.TrimSpace(current.Serial) == "" {
		current.Serial = incoming.Serial
	}
	if strings.TrimSpace(current.Source) == "" {
		current.Source = incoming.Source
	}
	if strings.TrimSpace(current.InstallDate) == "" {
		current.InstallDate = incoming.InstallDate
	}
	if strings.TrimSpace(current.InstallSource) == "" {
		current.InstallSource = incoming.InstallSource
	}
	return current
}

// -----------------------------------------------------------------------
// Memory
// -----------------------------------------------------------------------

func mapMemoryModules(rows []map[string]any) []models.MemoryModule {
	items := make([]models.MemoryModule, 0, len(rows))
	for _, row := range rows {
		sizeRaw := parseFloat(getString(row, "size"))
		if sizeRaw <= 0 {
			continue
		}
		sizeGB := round2(sizeRaw / bytesPerGB)
		sizeMB := int(round2(sizeRaw / (1024 * 1024)))
		if sizeRaw > 0 && sizeRaw < memorySizeAmbiguityThreshold {
			// Some providers return memory size in MB.
			sizeMB = int(sizeRaw)
			sizeGB = round2(sizeRaw / 1024)
		}
		items = append(items, models.MemoryModule{
			Handle:              getString(row, "handle"),
			ArrayHandle:         getString(row, "array_handle"),
			FormFactor:          getString(row, "form_factor"),
			TotalWidth:          parseInt(getString(row, "total_width")),
			DataWidth:           parseInt(getString(row, "data_width")),
			SizeMB:              sizeMB,
			Set:                 parseInt(getString(row, "set")),
			Slot:                firstNonEmpty(getString(row, "device_locator"), getString(row, "slot")),
			Bank:                firstNonEmpty(getString(row, "bank_locator"), getString(row, "bank")),
			MemoryTypeDetails:   getString(row, "memory_type_details"),
			MaxSpeedMTs:         parseInt(getString(row, "max_speed")),
			Manufacturer:        getString(row, "manufacturer"),
			PartNumber:          getString(row, "part_number"),
			Serial:              firstNonEmpty(getString(row, "serial_number"), getString(row, "serial")),
			AssetTag:            getString(row, "asset_tag"),
			SizeGB:              sizeGB,
			SpeedMHz:            parseInt(firstNonEmpty(getString(row, "configured_clock_speed"), getString(row, "speed"))),
			Type:                firstNonEmpty(getString(row, "memory_type"), getString(row, "type")),
			MinVoltageMV:        parseInt(getString(row, "min_voltage")),
			MaxVoltageMV:        parseInt(getString(row, "max_voltage")),
			ConfiguredVoltageMV: parseInt(getString(row, "configured_voltage")),
		})
	}
	return items
}

// -----------------------------------------------------------------------
// Battery
// -----------------------------------------------------------------------

func mapBatteryRows(rows []map[string]any) []models.BatteryInfo {
	items := make([]models.BatteryInfo, 0, len(rows))
	for _, row := range rows {
		items = append(items, models.BatteryInfo{
			Manufacturer:         getString(row, "manufacturer"),
			Model:                getString(row, "model"),
			SerialNumber:         getString(row, "serial_number"),
			CycleCount:           parseInt(getString(row, "cycle_count")),
			State:                getString(row, "state"),
			Charging:             parseBoolLoose(getString(row, "charging")),
			Charged:              parseBoolLoose(getString(row, "charged")),
			DesignedCapacityMAh:  parseInt(getString(row, "designed_capacity")),
			MaxCapacityMAh:       parseInt(getString(row, "max_capacity")),
			CurrentCapacityMAh:   parseInt(getString(row, "current_capacity")),
			PercentRemaining:     parseInt(getString(row, "percent_remaining")),
			AmperageMA:           parseInt(getString(row, "amperage")),
			VoltageMV:            parseInt(getString(row, "voltage")),
			MinutesUntilEmpty:    parseInt(getString(row, "minutes_until_empty")),
			MinutesToFullCharge:  parseInt(getString(row, "minutes_to_full_charge")),
			Chemistry:            getString(row, "chemistry"),
			Health:               getString(row, "health"),
			Condition:            getString(row, "condition"),
			ManufactureDateEpoch: int64(parseInt(getString(row, "manufacture_date"))),
		})
	}
	return items
}

// -----------------------------------------------------------------------
// BitLocker
// -----------------------------------------------------------------------

func mapBitLockerRows(rows []map[string]any) []models.BitLockerInfo {
	items := make([]models.BitLockerInfo, 0, len(rows))
	for _, row := range rows {
		items = append(items, models.BitLockerInfo{
			DeviceID:            getString(row, "device_id"),
			DriveLetter:         getString(row, "drive_letter"),
			PersistentVolumeID:  getString(row, "persistent_volume_id"),
			ConversionStatus:    parseInt(getString(row, "conversion_status")),
			ProtectionStatus:    parseInt(getString(row, "protection_status")),
			EncryptionMethod:    getString(row, "encryption_method"),
			Version:             parseInt(getString(row, "version")),
			PercentageEncrypted: parseInt(getString(row, "percentage_encrypted")),
			LockStatus:          parseInt(getString(row, "lock_status")),
		})
	}
	return items
}

// -----------------------------------------------------------------------
// CPU
// -----------------------------------------------------------------------

func mapCPUInfoRows(rows []map[string]any) []models.CPUInfo {
	items := make([]models.CPUInfo, 0, len(rows))
	for _, row := range rows {
		items = append(items, models.CPUInfo{
			DeviceID:                 getString(row, "device_id"),
			Model:                    getString(row, "model"),
			Manufacturer:             getString(row, "manufacturer"),
			ProcessorType:            getString(row, "processor_type"),
			CPUStatus:                parseInt(getString(row, "cpu_status")),
			NumberOfCores:            parseInt(getString(row, "number_of_cores")),
			LogicalProcessors:        parseInt(getString(row, "logical_processors")),
			AddressWidth:             parseInt(getString(row, "address_width")),
			CurrentClockSpeed:        parseInt(getString(row, "current_clock_speed")),
			MaxClockSpeed:            parseInt(getString(row, "max_clock_speed")),
			SocketDesignation:        getString(row, "socket_designation"),
			Availability:             getString(row, "availability"),
			LoadPercentage:           parseInt(getString(row, "load_percentage")),
			NumberOfEfficiencyCores:  parseInt(getString(row, "number_of_efficiency_cores")),
			NumberOfPerformanceCores: parseInt(getString(row, "number_of_performance_cores")),
		})
	}
	return items
}

func mapCPUFeatures(rows []map[string]any) []models.CPUFeature {
	items := make([]models.CPUFeature, 0, len(rows))
	for _, row := range rows {
		items = append(items, models.CPUFeature{
			Feature:        getString(row, "feature"),
			Value:          getString(row, "value"),
			OutputRegister: getString(row, "output_register"),
			OutputBit:      parseInt(getString(row, "output_bit")),
			InputEAX:       getString(row, "input_eax"),
		})
	}
	return items
}

// -----------------------------------------------------------------------
// GPU
// -----------------------------------------------------------------------

func mapGPURows(rows []map[string]any) []models.GPUInfo {
	items := make([]models.GPUInfo, 0, len(rows))
	for _, row := range rows {
		name := firstNonEmpty(getString(row, "model"), getString(row, "name"))
		if strings.TrimSpace(name) == "" {
			continue
		}
		vramBytes := parseFloat(getString(row, "vram"))
		vramGB := 0.0
		if vramBytes > 0 {
			vramGB = round2(vramBytes / bytesPerGB)
		}
		items = append(items, models.GPUInfo{
			Name:          name,
			Manufacturer:  firstNonEmpty(getString(row, "vendor"), getString(row, "manufacturer")),
			DriverVersion: firstNonEmpty(getString(row, "driver"), getString(row, "driverVersion")),
			VRAMGB:        vramGB,
			Status:        getString(row, "status"),
		})
	}
	return items
}

// -----------------------------------------------------------------------
// Startup / Autoexec
// -----------------------------------------------------------------------

func mapStartupItems(rows []map[string]any) []models.StartupItem {
	items := make([]models.StartupItem, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(getString(row, "name"))
		path := strings.TrimSpace(getString(row, "path"))
		if name == "" && path == "" {
			continue
		}
		items = append(items, models.StartupItem{
			Name:     name,
			Path:     path,
			Args:     getString(row, "args"),
			Type:     getString(row, "type"),
			Source:   getString(row, "source"),
			Status:   getString(row, "status"),
			Username: getString(row, "username"),
		})
	}
	return items
}

func mapAutoexecItems(rows []map[string]any) []models.AutoexecItem {
	items := make([]models.AutoexecItem, 0, len(rows))
	for _, row := range rows {
		path := strings.TrimSpace(getString(row, "path"))
		name := strings.TrimSpace(getString(row, "name"))
		if path == "" && name == "" {
			continue
		}
		items = append(items, models.AutoexecItem{
			Path:   path,
			Name:   name,
			Source: getString(row, "source"),
		})
	}
	return items
}

// -----------------------------------------------------------------------
// Disks
// -----------------------------------------------------------------------

func mapLogicalDrives(logicalDriveRows []map[string]any) []models.DiskInfo {
	items := make([]models.DiskInfo, 0, len(logicalDriveRows))
	for _, row := range logicalDriveRows {
		sizeBytes := parseFloat(getString(row, "size"))
		freeBytes := parseFloat(getString(row, "free_space"))
		if sizeBytes <= 0 {
			continue
		}
		sizeGB := sizeBytes / bytesPerGB
		freeKnown := freeBytes >= 0
		freeGB := -1.0
		if freeKnown {
			freeGB = freeBytes / bytesPerGB
		}

		items = append(items, models.DiskInfo{
			Device:        getString(row, "device_id"),
			Label:         getString(row, "description"),
			FileSystem:    getString(row, "file_system"),
			Type:          firstNonEmpty(getString(row, "type"), "Unknown"),
			SizeGB:        round2(sizeGB),
			FreeGB:        round2(freeGB),
			FreeKnown:     freeKnown,
			BootPartition: parseBoolLoose(getString(row, "boot_partition")),
			Partitions:    0,
			Description:   getString(row, "description"),
		})
	}
	return items
}

func mapPhysicalDisks(diskInfoRows []map[string]any) []models.DiskInfo {
	items := make([]models.DiskInfo, 0, len(diskInfoRows))
	for _, row := range diskInfoRows {
		sizeGB := parseFloat(getString(row, "disk_size")) / bytesPerGB
		device := strings.TrimSpace(getString(row, "id"))
		if device == "" {
			idx := strings.TrimSpace(getString(row, "disk_index"))
			if idx != "" {
				device = "Disk " + idx
			}
		}

		items = append(items, models.DiskInfo{
			Device:        device,
			Label:         getString(row, "name"),
			FileSystem:    "",
			Type:          getString(row, "type"),
			SizeGB:        round2(sizeGB),
			FreeGB:        -1,
			FreeKnown:     false,
			BootPartition: false,
			Manufacturer:  getString(row, "manufacturer"),
			Model:         getString(row, "hardware_model"),
			Serial:        getString(row, "serial"),
			Partitions:    parseInt(getString(row, "partitions")),
			Description:   getString(row, "description"),
		})
	}
	return items
}

// -----------------------------------------------------------------------
// Networks
// -----------------------------------------------------------------------

func mapNetworkRows(rows []map[string]any, addressRows []map[string]any, routeRows []map[string]any) []models.NetworkInfo {
	ipv4ByInterface := map[string]string{}
	ipv6ByInterface := map[string]string{}
	gatewayByInterface := map[string]string{}

	for _, row := range addressRows {
		iface := strings.TrimSpace(getString(row, "interface"))
		addr := strings.TrimSpace(getString(row, "address"))
		if iface == "" || addr == "" {
			continue
		}
		if strings.Contains(addr, ":") {
			ipv6ByInterface[iface] = appendCSV(ipv6ByInterface[iface], addr)
		} else {
			ipv4ByInterface[iface] = appendCSV(ipv4ByInterface[iface], addr)
		}
	}

	for _, row := range routeRows {
		iface := strings.TrimSpace(getString(row, "interface"))
		gw := strings.TrimSpace(getString(row, "gateway"))
		if iface == "" || gw == "" {
			continue
		}
		gatewayByInterface[iface] = appendCSV(gatewayByInterface[iface], gw)
	}

	items := make([]models.NetworkInfo, 0, len(rows))
	for _, row := range rows {
		iface := getString(row, "interface")
		friendly := getString(row, "friendly_name")
		if friendly == "" {
			friendly = getString(row, "friendlyName")
		}
		if iface == "" && friendly == "" {
			continue
		}

		items = append(items, models.NetworkInfo{
			Interface:        iface,
			FriendlyName:     friendly,
			MAC:              getString(row, "mac"),
			IPv4:             firstNonEmpty(getString(row, "ipv4"), ipv4ByInterface[iface]),
			IPv6:             firstNonEmpty(getString(row, "ipv6"), ipv6ByInterface[iface]),
			Gateway:          firstNonEmpty(getString(row, "gateway"), gatewayByInterface[iface]),
			Type:             getString(row, "type"),
			MTU:              parseInt(getString(row, "mtu")),
			LinkSpeedMbps:    parseInt(firstNonEmpty(getString(row, "link_speed"), getString(row, "linkSpeedMbps"))),
			ConnectionStatus: firstNonEmpty(getString(row, "connection_status"), getString(row, "connectionStatus")),
			Enabled:          parseBoolLoose(getString(row, "enabled")),
			PhysicalAdapter:  parseBoolLoose(firstNonEmpty(getString(row, "physical_adapter"), getString(row, "physicalAdapter"))),
			DHCPEnabled:      parseBoolLoose(firstNonEmpty(getString(row, "dhcp_enabled"), getString(row, "dhcpEnabled"))),
			DNSServers:       firstNonEmpty(getString(row, "dns_server_search_order"), getString(row, "dnsServers")),
			Description:      getString(row, "description"),
			Manufacturer:     getString(row, "manufacturer"),
		})
	}
	return items
}

func mapListeningPorts(rows []map[string]any) []models.ListeningPortInfo {
	if len(rows) == 0 {
		return nil
	}

	const maxListeningPorts = 200
	items := make([]models.ListeningPortInfo, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))

	for _, row := range rows {
		port := parseInt(getString(row, "port"))
		if port <= 0 {
			continue
		}

		protocol := strings.TrimSpace(getString(row, "protocol"))
		address := strings.TrimSpace(getString(row, "address"))
		pid := parseInt(getString(row, "pid"))
		key := protocol + "|" + address + "|" + getString(row, "port") + "|" + getString(row, "pid")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		items = append(items, models.ListeningPortInfo{
			ProcessName: strings.TrimSpace(getString(row, "process_name")),
			ProcessID:   pid,
			ProcessPath: strings.TrimSpace(getString(row, "process_path")),
			Protocol:    protocol,
			Address:     address,
			Port:        port,
		})

		if len(items) >= maxListeningPorts {
			break
		}
	}

	return items
}

func mapOpenSockets(rows []map[string]any) []models.OpenSocketInfo {
	if len(rows) == 0 {
		return nil
	}

	const maxOpenSockets = 500
	items := make([]models.OpenSocketInfo, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))

	for _, row := range rows {
		localPort := parseInt(getString(row, "local_port"))
		remotePort := parseInt(getString(row, "remote_port"))
		if localPort <= 0 && remotePort <= 0 {
			continue
		}

		pid := parseInt(getString(row, "pid"))
		localAddress := strings.TrimSpace(getString(row, "local_address"))
		remoteAddress := strings.TrimSpace(getString(row, "remote_address"))
		protocol := strings.TrimSpace(getString(row, "protocol"))
		family := strings.TrimSpace(getString(row, "family"))
		key := protocol + "|" + family + "|" + localAddress + "|" + getString(row, "local_port") + "|" + remoteAddress + "|" + getString(row, "remote_port") + "|" + getString(row, "pid")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		items = append(items, models.OpenSocketInfo{
			ProcessName:   strings.TrimSpace(getString(row, "process_name")),
			ProcessID:     pid,
			ProcessPath:   strings.TrimSpace(getString(row, "process_path")),
			LocalAddress:  localAddress,
			LocalPort:     localPort,
			RemoteAddress: remoteAddress,
			RemotePort:    remotePort,
			Protocol:      protocol,
			Family:        family,
		})

		if len(items) >= maxOpenSockets {
			break
		}
	}

	return items
}

// -----------------------------------------------------------------------
// Logged-in Users
// -----------------------------------------------------------------------

func mapLoggedInUsers(rows []map[string]any) []models.LoggedInUser {
	items := make([]models.LoggedInUser, 0, len(rows))
	for _, row := range rows {
		user := getString(row, "user")
		if user == "" {
			continue
		}
		items = append(items, models.LoggedInUser{
			User:     user,
			Type:     getString(row, "type"),
			TTY:      getString(row, "tty"),
			Host:     getString(row, "host"),
			PID:      parseInt(getString(row, "pid")),
			SID:      getString(row, "sid"),
			Registry: getString(row, "registry_hive"),
			Time:     int64(parseFloat(getString(row, "time"))),
		})
	}
	return items
}
