//go:build windows

package native

import (
	"context"
	"syscall"
	"unsafe"

	"discovery/app/core/models"
)

var (
	procGetLogicalDriveStringsW = modkernel32.NewProc("GetLogicalDriveStringsW")
	procGetDriveTypeW           = modkernel32.NewProc("GetDriveTypeW")
	procGetDiskFreeSpaceExW     = modkernel32.NewProc("GetDiskFreeSpaceExW")
	procGetVolumeInformationW   = modkernel32.NewProc("GetVolumeInformationW")
)

const (
	driveTypeFixed     = 3
	driveTypeRemovable = 2
	driveTypeRemote    = 4
	driveTypeCDROM     = 5
	driveTypeRAMDisk   = 6
)

// collectDisksNative returns logical volumes and physical disks.
// Logical volumes are enumerated via GetLogicalDriveStringsW + GetDiskFreeSpaceExW.
// Physical disks are enumerated via WMI (Win32_DiskDrive) in a separate helper.
func collectDisksNative(ctx context.Context) ([]models.DiskInfo, []models.DiskInfo, error) {
	_ = ctx
	volumes := collectLogicalVolumes()
	physical := collectPhysicalDisks()
	return volumes, physical, nil
}

func collectLogicalVolumes() []models.DiskInfo {
	var items []models.DiskInfo

	// Enumerate drive letters.
	buf := make([]uint16, 512)
	size := uint32(len(buf))
	r, _, _ := procGetLogicalDriveStringsW.Call(
		uintptr(size),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if r == 0 {
		return items
	}

	// The buffer is a list of null-terminated strings, ending with a double null.
	drives := []string{}
	start := 0
	for i := 0; i < len(buf); i++ {
		if buf[i] == 0 {
			if i == start {
				break
			}
			drives = append(drives, syscall.UTF16ToString(buf[start:i]))
			start = i + 1
		}
	}

	for _, drive := range drives {
		root := drive // e.g. "C:\"
		driveType, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(root))))
		if int(driveType) != driveTypeFixed {
			continue
		}

		var freeBytesAvailable, totalBytes, totalFreeBytes int64
		fr, _, _ := procGetDiskFreeSpaceExW.Call(
			uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(root))),
			uintptr(unsafe.Pointer(&freeBytesAvailable)),
			uintptr(unsafe.Pointer(&totalBytes)),
			uintptr(unsafe.Pointer(&totalFreeBytes)),
		)
		if fr == 0 || totalBytes <= 0 {
			continue
		}

		sizeGB := float64(totalBytes) / (1024.0 * 1024.0 * 1024.0)
		freeGB := float64(freeBytesAvailable) / (1024.0 * 1024.0 * 1024.0)

		// Volume label and file system.
		label := make([]uint16, 256)
		fsName := make([]uint16, 64)
		vi, _, _ := procGetVolumeInformationW.Call(
			uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(root))),
			uintptr(unsafe.Pointer(&label[0])),
			uintptr(len(label)),
			0, 0, 0,
			uintptr(unsafe.Pointer(&fsName[0])),
			uintptr(len(fsName)),
		)
		labelStr := ""
		fsStr := ""
		if vi != 0 {
			labelStr = syscall.UTF16ToString(label)
			fsStr = syscall.UTF16ToString(fsName)
		}

		items = append(items, models.DiskInfo{
			Device:        root,
			Label:         labelStr,
			FileSystem:    fsStr,
			Type:          "Fixed",
			SizeGB:        round2(sizeGB),
			FreeGB:        round2(freeGB),
			FreeKnown:     true,
			BootPartition: false,
			Description:   labelStr,
		})
	}

	return items
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// collectPhysicalDisks enumerates physical disks via WMI (Win32_DiskDrive).
func collectPhysicalDisks() []models.DiskInfo {
	return collectPhysicalDisksWMI()
}

// getDiskMediaTypes returns a map of drive letter -> media type (SSD/HDD)
// using native WMI (Win32_MediaType via Win32_DiskDrive + partition mapping).
func getDiskMediaTypes() map[string]string {
	return collectDiskMediaTypesWMI()
}
