//go:build windows

package native

import (
	"unsafe"
)

// memoryStatusEx mirrors the MEMORYSTATUSEX structure.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")

// getMemoryStatus returns total GB, used GB and percent via GlobalMemoryStatusEx.
func getMemoryStatus() (totalGB, usedGB, percent float64, ok bool) {
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 || ms.TotalPhys == 0 {
		return 0, 0, 0, false
	}
	totalGB = float64(ms.TotalPhys) / (1024.0 * 1024.0 * 1024.0)
	usedGB = float64(ms.TotalPhys-ms.AvailPhys) / (1024.0 * 1024.0 * 1024.0)
	percent = float64(ms.MemoryLoad)
	return totalGB, usedGB, percent, true
}
