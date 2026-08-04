//go:build windows

package inventory

import (
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGetTickCount64       = modkernel32.NewProc("GetTickCount64")
	procGetDiskFreeSpaceExW  = modkernel32.NewProc("GetDiskFreeSpaceExW")
	procCreateToolhelp32Snap = modkernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW      = modkernel32.NewProc("Process32FirstW")
	procProcess32NextW       = modkernel32.NewProc("Process32NextW")
	procGetSystemTimes       = modkernel32.NewProc("GetSystemTimes")
)

const (
	th32csSnapprocess = 0x00000002
	maxPath           = 260
)

type processEntry32W struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [maxPath]uint16
}

// ─── Uptime ──────────────────────────────────────────────────────────

// collectUptimeSeconds returns system uptime in seconds via GetTickCount64.
func collectUptimeSeconds() int64 {
	r, _, _ := procGetTickCount64.Call()
	if r == 0 {
		return 0
	}
	return int64(r) / 1000
}

// ─── Disk Space ──────────────────────────────────────────────────────

// collectDiskSpaceNative returns disk C: total GB, used GB, and usage
// percent via GetDiskFreeSpaceExW.
func collectDiskSpaceNative() (float64, float64, float64, bool) {
	path, err := syscall.UTF16PtrFromString("C:\\")
	if err != nil {
		return -1, -1, -1, false
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes int64
	r, _, _ := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(path)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r == 0 || totalBytes <= 0 {
		return -1, -1, -1, false
	}

	totalGB := float64(totalBytes) / (1024.0 * 1024.0 * 1024.0)
	freeGB := float64(freeBytesAvailable) / (1024.0 * 1024.0 * 1024.0)
	usedGB := totalGB - freeGB
	if usedGB < 0 {
		usedGB = 0
	}
	percent := (usedGB * 100.0) / totalGB

	return normalizeHeartbeatGigabytes(totalGB), normalizeHeartbeatGigabytes(usedGB), normalizeHeartbeatPercent(percent), true
}

// ─── Process Count ───────────────────────────────────────────────────

// collectProcessCountNative returns the number of running processes via
// CreateToolhelp32Snapshot.
func collectProcessCountNative() int {
	handle, _, _ := procCreateToolhelp32Snap.Call(uintptr(th32csSnapprocess), 0)
	if handle == uintptr(windows.InvalidHandle) {
		return -1
	}
	defer windows.CloseHandle(windows.Handle(handle))

	var pe processEntry32W
	pe.dwSize = uint32(unsafe.Sizeof(pe))

	count := 0
	r, _, _ := procProcess32FirstW.Call(handle, uintptr(unsafe.Pointer(&pe)))
	for r != 0 {
		count++
		pe = processEntry32W{dwSize: uint32(unsafe.Sizeof(pe))}
		r, _, _ = procProcess32NextW.Call(handle, uintptr(unsafe.Pointer(&pe)))
	}
	return count
}

// ─── CPU Percent (Sliding Window via GetSystemTimes) ─────────────────

var (
	cpuSlidingMu   sync.Mutex
	lastIdleTime   uint64
	lastKernelTime uint64
	lastUserTime   uint64
	cpuInitialized bool
)

// collectWindowsCPUPercentNative returns CPU usage percent using a
// sliding-window delta of GetSystemTimes calls. The first invocation
// after startup returns (-1, false) to seed the baseline; subsequent
// calls return the actual CPU usage over the interval since the last
// call. This is ideal for periodic heartbeats (30–60s).
func collectWindowsCPUPercentNative() (float64, bool) {
	cpuSlidingMu.Lock()
	defer cpuSlidingMu.Unlock()

	var idle, kernel, user windows.Filetime
	r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r == 0 {
		return -1, false
	}

	idleVal := uint64(idle.HighDateTime)<<32 | uint64(idle.LowDateTime)
	kernelVal := uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)
	userVal := uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)

	if !cpuInitialized || idleVal < lastIdleTime {
		// First call or counter wrapped: seed baseline
		lastIdleTime = idleVal
		lastKernelTime = kernelVal
		lastUserTime = userVal
		cpuInitialized = true
		return -1, false
	}

	deltaIdle := idleVal - lastIdleTime
	deltaTotal := (kernelVal + userVal) - (lastKernelTime + lastUserTime)

	lastIdleTime = idleVal
	lastKernelTime = kernelVal
	lastUserTime = userVal

	if deltaTotal == 0 {
		return -1, false
	}

	percent := float64(deltaTotal-deltaIdle) * 100.0 / float64(deltaTotal)
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return normalizeHeartbeatPercent(percent), true
}
