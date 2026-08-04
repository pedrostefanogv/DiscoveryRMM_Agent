//go:build windows

package inventory

import (
	"syscall"
	"unsafe"
)

// memoryStatusEx mirrors the Windows MEMORYSTATUSEX struct.
type memoryStatusEx struct {
	cbSize                  uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// collectWindowsMemoryNative reads memory metrics from GlobalMemoryStatusEx.
func collectWindowsMemoryNative() (float64, float64, float64, bool) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	globalMemoryStatusEx := kernel32.NewProc("GlobalMemoryStatusEx")

	var ms memoryStatusEx
	ms.cbSize = uint32(unsafe.Sizeof(ms))

	r, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 {
		return -1, -1, -1, false
	}

	if ms.ullTotalPhys == 0 {
		return -1, -1, -1, false
	}

	totalBytes := float64(ms.ullTotalPhys)
	freeBytes := float64(ms.ullAvailPhys)
	usedBytes := totalBytes - freeBytes
	if usedBytes < 0 {
		usedBytes = 0
	}

	totalGB := totalBytes / (1024.0 * 1024.0 * 1024.0)
	usedGB := usedBytes / (1024.0 * 1024.0 * 1024.0)
	percent := (usedBytes * 100.0) / totalBytes

	return normalizeHeartbeatGigabytes(totalGB), normalizeHeartbeatGigabytes(usedGB), normalizeHeartbeatPercent(percent), true
}
