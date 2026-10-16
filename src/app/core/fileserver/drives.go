package fileserver

import (
	"sort"
	"syscall"
	"unsafe"
)

// DriveInfo descreve um volume lógico (unidade) visível no host remoto.
// É usado pelo viewer para oferecer a troca de disco (C:, D:, ...) sem exigir
// que o operador digite o caminho na mão.
type DriveInfo struct {
	Name       string `json:"name"`                 // "C:"
	Root       string `json:"root"`                 // "C:\\"
	Label      string `json:"label,omitempty"`      // rótulo do volume
	FileSystem string `json:"fileSystem,omitempty"` // NTFS, FAT32, ...
	Type       string `json:"type"`                 // fixed|removable|network|cdrom|ramdisk|unknown
	TotalBytes int64  `json:"totalBytes,omitempty"`
	FreeBytes  int64  `json:"freeBytes,omitempty"`
	// Ready indica que o volume respondeu a GetDiskFreeSpaceEx (mídia presente).
	// Unidades ótimas vazias (CD/DVD) aparecem com ready=false.
	Ready bool `json:"ready"`
}

const (
	drvTypeRemovable = 2
	drvTypeFixed     = 3
	drvTypeRemote    = 4
	drvTypeCDROM     = 5
	drvTypeRAMDisk   = 6
)

var (
	modKernel32Drives               = syscall.NewLazyDLL("kernel32.dll")
	procGetLogicalDriveStringsW     = modKernel32Drives.NewProc("GetLogicalDriveStringsW")
	procGetDriveTypeWDrives         = modKernel32Drives.NewProc("GetDriveTypeW")
	procGetDiskFreeSpaceExWDrives   = modKernel32Drives.NewProc("GetDiskFreeSpaceExW")
	procGetVolumeInformationWDrives = modKernel32Drives.NewProc("GetVolumeInformationW")
)

// driveTypeName mapeia DRIVE_* do Windows para um rótulo estável usado no JSON.
func driveTypeName(t uintptr) string {
	switch int(t) {
	case drvTypeRemovable:
		return "removable"
	case drvTypeFixed:
		return "fixed"
	case drvTypeRemote:
		return "network"
	case drvTypeCDROM:
		return "cdrom"
	case drvTypeRAMDisk:
		return "ramdisk"
	default:
		return "unknown"
	}
}

// logicalDriveRoots enumera as raízes lógicas ("C:\", "D:\", ...).
func logicalDriveRoots() []string {
	buf := make([]uint16, 512)
	n, _, _ := procGetLogicalDriveStringsW.Call(uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	if n == 0 {
		return nil
	}
	if int(n) > len(buf) {
		buf = make([]uint16, n+1)
		n, _, _ = procGetLogicalDriveStringsW.Call(uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
		if n == 0 {
			return nil
		}
	}

	var roots []string
	start := 0
	for i := 0; i < int(n) && i < len(buf); i++ {
		if buf[i] == 0 {
			if i == start {
				break
			}
			roots = append(roots, syscall.UTF16ToString(buf[start:i]))
			start = i + 1
		}
	}
	return roots
}

// ListDrives enumera as unidades lógicas do Windows com tipo, rótulo e espaço
// livre. Retorna slice vazio quando a enumeração falha (nunca nil para o
// viewer tratar como "sem unidades" em vez de "sem resposta").
func ListDrives() []DriveInfo {
	roots := logicalDriveRoots()
	if len(roots) == 0 {
		return []DriveInfo{}
	}

	drives := make([]DriveInfo, 0, len(roots))
	for _, root := range roots {
		rootPtr, err := syscall.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		driveType, _, _ := procGetDriveTypeWDrives.Call(uintptr(unsafe.Pointer(rootPtr)))

		var freeAvailable, totalBytes, totalFree int64
		fr, _, _ := procGetDiskFreeSpaceExWDrives.Call(
			uintptr(unsafe.Pointer(rootPtr)),
			uintptr(unsafe.Pointer(&freeAvailable)),
			uintptr(unsafe.Pointer(&totalBytes)),
			uintptr(unsafe.Pointer(&totalFree)),
		)
		// ready = consulta de espaço respondeu (mídia presente). Não exige
		// totalBytes > 0: unidades de rede podem reportar 0 em casos raros e
		// ainda assim serem navegáveis.
		ready := fr != 0

		label := ""
		fileSystem := ""
		if ready {
			labelBuf := make([]uint16, 256)
			fsBuf := make([]uint16, 64)
			vi, _, _ := procGetVolumeInformationWDrives.Call(
				uintptr(unsafe.Pointer(rootPtr)),
				uintptr(unsafe.Pointer(&labelBuf[0])),
				uintptr(len(labelBuf)),
				0, 0, 0,
				uintptr(unsafe.Pointer(&fsBuf[0])),
				uintptr(len(fsBuf)),
			)
			if vi != 0 {
				label = syscall.UTF16ToString(labelBuf)
				fileSystem = syscall.UTF16ToString(fsBuf)
			}
		}

		name := root
		if len(name) >= 2 && (name[len(name)-1] == '\\' || name[len(name)-1] == '/') {
			name = name[:len(name)-1]
		}

		info := DriveInfo{
			Name:       name,
			Root:       root,
			Label:      label,
			FileSystem: fileSystem,
			Type:       driveTypeName(driveType),
			Ready:      ready,
		}
		if ready {
			info.TotalBytes = totalBytes
			info.FreeBytes = freeAvailable
		}
		drives = append(drives, info)
	}

	sort.SliceStable(drives, func(i, j int) bool { return drives[i].Name < drives[j].Name })
	return drives
}
