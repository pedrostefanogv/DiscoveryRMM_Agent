//go:build windows

package native

import (
	"context"
	"strings"

	"discovery/app/core/models"
)

const wmiNamespace = `root\cimv2`

// collectHardwareNative collects motherboard, BIOS, GPU, memory and CPU
// details via WMI (COM), without any PowerShell subprocess.
func collectHardwareNative(ctx context.Context) (models.HardwareInfo, []models.MemoryModule, []models.GPUInfo, []models.CPUInfo, []models.CPUFeature, error) {
	hw := models.HardwareInfo{}

	// Motherboard (Win32_BaseBoard).
	if rows, err := wmiQuery(wmiNamespace, "SELECT Manufacturer, Product, SerialNumber FROM Win32_BaseBoard"); err == nil && len(rows) > 0 {
		hw.MotherboardManufacturer = wmiString(rows[0], "Manufacturer")
		hw.MotherboardModel = wmiString(rows[0], "Product")
		hw.MotherboardSerial = wmiString(rows[0], "SerialNumber")
	}

	// BIOS (Win32_BIOS).
	if rows, err := wmiQuery(wmiNamespace, "SELECT Manufacturer, SMBIOSBIOSVersion, ReleaseDate, SerialNumber FROM Win32_BIOS"); err == nil && len(rows) > 0 {
		hw.BIOSVendor = wmiString(rows[0], "Manufacturer")
		hw.BIOSVersion = wmiString(rows[0], "SMBIOSBIOSVersion")
		hw.BIOSSerial = wmiString(rows[0], "SerialNumber")
		hw.BIOSReleaseDate = wmiBIOSDate(wmiString(rows[0], "ReleaseDate"))
	}

	// System manufacturer/model/serial (Win32_ComputerSystem / Win32_SystemEnclosure).
	if rows, err := wmiQuery(wmiNamespace, "SELECT Manufacturer, Model FROM Win32_ComputerSystem"); err == nil && len(rows) > 0 {
		if hw.Manufacturer == "" {
			hw.Manufacturer = wmiString(rows[0], "Manufacturer")
		}
		if hw.Model == "" {
			hw.Model = wmiString(rows[0], "Model")
		}
	}
	if rows, err := wmiQuery(wmiNamespace, "SELECT SerialNumber FROM Win32_SystemEnclosure"); err == nil && len(rows) > 0 {
		if hw.SerialNumber == "" {
			hw.SerialNumber = wmiString(rows[0], "SerialNumber")
		}
	}

	// Memory modules (Win32_PhysicalMemory).
	memoryModules := collectMemoryModulesWMI()

	// GPU (Win32_VideoController).
	gpus := collectGPUsWMI()

	// CPU details (Win32_Processor).
	cpus := collectCPUsWMI()

	// CPU features via cpuid (native).
	features := collectCPUFeaturesNative()

	return hw, memoryModules, gpus, cpus, features, nil
}

func collectMemoryModulesWMI() []models.MemoryModule {
	rows, err := wmiQuery(wmiNamespace, "SELECT Tag, BankLabel, DeviceLocator, Capacity, Speed, ConfiguredClockSpeed, Manufacturer, PartNumber, SerialNumber, SMBIOSMemoryType, MemoryType, FormFactor, DataWidth, TotalWidth FROM Win32_PhysicalMemory")
	if err != nil {
		return nil
	}

	items := make([]models.MemoryModule, 0, len(rows))
	for _, row := range rows {
		capacityBytes := wmiFloat(row, "Capacity")
		if capacityBytes <= 0 {
			continue
		}
		sizeGB := round2(capacityBytes / (1024.0 * 1024.0 * 1024.0))
		sizeMB := int(round2(capacityBytes / (1024.0 * 1024.0)))

		items = append(items, models.MemoryModule{
			Handle:            wmiString(row, "Tag"),
			Bank:              wmiString(row, "BankLabel"),
			Slot:              wmiString(row, "DeviceLocator"),
			SizeMB:            sizeMB,
			SizeGB:            sizeGB,
			SpeedMHz:          wmiInt(row, "ConfiguredClockSpeed"),
			MaxSpeedMTs:       wmiInt(row, "Speed"),
			Manufacturer:      wmiString(row, "Manufacturer"),
			PartNumber:        wmiString(row, "PartNumber"),
			Serial:            wmiString(row, "SerialNumber"),
			Type:              wmiString(row, "SMBIOSMemoryType"),
			MemoryTypeDetails: wmiString(row, "MemoryType"),
			FormFactor:        wmiString(row, "FormFactor"),
			DataWidth:         wmiInt(row, "DataWidth"),
			TotalWidth:        wmiInt(row, "TotalWidth"),
		})
	}
	return items
}

func collectGPUsWMI() []models.GPUInfo {
	rows, err := wmiQuery(wmiNamespace, "SELECT Name, AdapterCompatibility, DriverVersion, AdapterRAM, Status FROM Win32_VideoController")
	if err != nil {
		return nil
	}

	items := make([]models.GPUInfo, 0, len(rows))
	for _, row := range rows {
		name := wmiString(row, "Name")
		if name == "" {
			continue
		}
		vramBytes := wmiFloat(row, "AdapterRAM")
		vramGB := 0.0
		if vramBytes > 0 {
			vramGB = round2(vramBytes / (1024.0 * 1024.0 * 1024.0))
		}
		items = append(items, models.GPUInfo{
			Name:          name,
			Manufacturer:  wmiString(row, "AdapterCompatibility"),
			DriverVersion: wmiString(row, "DriverVersion"),
			VRAMGB:        vramGB,
			Status:        wmiString(row, "Status"),
		})
	}
	return items
}

func collectCPUsWMI() []models.CPUInfo {
	rows, err := wmiQuery(wmiNamespace, "SELECT DeviceID, Name, Manufacturer, NumberOfCores, NumberOfLogicalProcessors, CurrentClockSpeed, MaxClockSpeed, SocketDesignation, AddressWidth, LoadPercentage FROM Win32_Processor")
	if err != nil {
		return nil
	}

	items := make([]models.CPUInfo, 0, len(rows))
	for _, row := range rows {
		items = append(items, models.CPUInfo{
			DeviceID:          wmiString(row, "DeviceID"),
			Model:             wmiString(row, "Name"),
			Manufacturer:      wmiString(row, "Manufacturer"),
			NumberOfCores:     wmiInt(row, "NumberOfCores"),
			LogicalProcessors: wmiInt(row, "NumberOfLogicalProcessors"),
			CurrentClockSpeed: wmiInt(row, "CurrentClockSpeed"),
			MaxClockSpeed:     wmiInt(row, "MaxClockSpeed"),
			SocketDesignation: wmiString(row, "SocketDesignation"),
			AddressWidth:      wmiInt(row, "AddressWidth"),
			LoadPercentage:    wmiInt(row, "LoadPercentage"),
		})
	}
	return items
}

// wmiBIOSDate converts a WMI CIM_DATETIME (e.g. "20260502210000.000000+000")
// into a simplified "YYYY-MM-DD" string.
func wmiBIOSDate(v string) string {
	v = strings.TrimSpace(v)
	if len(v) < 8 {
		return ""
	}
	return v[0:4] + "-" + v[4:6] + "-" + v[6:8]
}
