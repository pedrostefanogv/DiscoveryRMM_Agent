//go:build windows

package native

import (
	"strings"

	"discovery/app/core/models"
)

// collectPhysicalDisksWMI enumerates physical disks via Win32_DiskDrive.
func collectPhysicalDisksWMI() []models.DiskInfo {
	rows, err := wmiQuery(wmiNamespace, "SELECT DeviceID, Model, Manufacturer, SerialNumber, Size, MediaType, InterfaceType, Partitions FROM Win32_DiskDrive")
	if err != nil {
		return nil
	}

	items := make([]models.DiskInfo, 0, len(rows))
	for _, row := range rows {
		sizeBytes := wmiFloat(row, "Size")
		if sizeBytes <= 0 {
			continue
		}
		sizeGB := round2(sizeBytes / (1024.0 * 1024.0 * 1024.0))
		device := wmiString(row, "DeviceID")
		if device == "" {
			device = wmiString(row, "Name")
		}

		items = append(items, models.DiskInfo{
			Device:       device,
			Label:        wmiString(row, "Model"),
			Type:         wmiString(row, "InterfaceType"),
			MediaType:    wmiString(row, "MediaType"),
			SizeGB:       sizeGB,
			FreeGB:       -1,
			FreeKnown:    false,
			Manufacturer: wmiString(row, "Manufacturer"),
			Model:        wmiString(row, "Model"),
			Serial:       wmiString(row, "SerialNumber"),
			Partitions:   wmiInt(row, "Partitions"),
			Description:  wmiString(row, "Model"),
		})
	}
	return items
}

// collectDiskMediaTypesWMI returns a map of drive letter -> media type
// (SSD/HDD) by joining Win32_DiskDrive with Win32_LogicalDiskToPartition
// and Win32_LogicalDisk.
func collectDiskMediaTypesWMI() map[string]string {
	// Map physical disk index -> media type.
	diskRows, err := wmiQuery(wmiNamespace, "SELECT Index, MediaType FROM Win32_DiskDrive")
	if err != nil {
		return nil
	}
	mediaByIndex := make(map[string]string, len(diskRows))
	for _, row := range diskRows {
		idx := wmiString(row, "Index")
		mt := wmiString(row, "MediaType")
		if idx != "" && mt != "" {
			mediaByIndex[idx] = mt
		}
	}

	// Map drive letter -> disk index via Win32_LogicalDiskToPartition.
	partRows, err := wmiQuery(wmiNamespace, "SELECT Antecedent, Dependent FROM Win32_LogicalDiskToPartition")
	if err != nil {
		return nil
	}

	// Parse "Win32_DiskDrive.DeviceID=\"\\\\.\\PHYSICALDRIVE0\"" style refs.
	letterToIndex := make(map[string]string)
	for _, row := range partRows {
		antecedent := wmiString(row, "Antecedent")
		dependent := wmiString(row, "Dependent")
		diskIdx := extractDiskIndex(antecedent)
		letter := extractDriveLetter(dependent)
		if diskIdx != "" && letter != "" {
			letterToIndex[letter] = diskIdx
		}
	}

	result := make(map[string]string)
	for letter, idx := range letterToIndex {
		if mt, ok := mediaByIndex[idx]; ok {
			result[letter] = mt
		}
	}
	return result
}

// extractDiskIndex parses a WMI reference like
// "\\HOST\root\cimv2:Win32_DiskDrive.DeviceID=\"\\\\.\\PHYSICALDRIVE0\""
// and returns the numeric index (e.g. "0").
func extractDiskIndex(ref string) string {
	lower := strings.ToLower(ref)
	idx := strings.LastIndex(lower, "physicaldrive")
	if idx < 0 {
		return ""
	}
	rest := ref[idx+len("physicaldrive"):]
	rest = strings.Trim(rest, `"\\ .`)
	// Keep only digits.
	var digits []rune
	for _, r := range rest {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		} else {
			break
		}
	}
	return string(digits)
}

// extractDriveLetter parses a WMI reference like
// "\\HOST\root\cimv2:Win32_LogicalDisk.DeviceID=\"C:\"" and returns "C:".
func extractDriveLetter(ref string) string {
	lower := strings.ToLower(ref)
	idx := strings.LastIndex(lower, "win32_logicaldisk")
	if idx < 0 {
		return ""
	}
	rest := ref[idx+len("win32_logicaldisk"):]
	eq := strings.Index(rest, "=")
	if eq < 0 {
		return ""
	}
	val := strings.Trim(rest[eq+1:], `"`)
	val = strings.TrimSpace(val)
	if len(val) >= 2 && val[1] == ':' {
		return strings.ToUpper(val[0:2])
	}
	return ""
}
