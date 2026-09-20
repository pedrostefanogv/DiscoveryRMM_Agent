//go:build windows

package platform

import (
	"sync"
	"unsafe"
)

// ─── Samplers de "poder da máquina" para a semente/escada do modo AUTO ────
// CPU via GetSystemTimes (janela deslizante com estado próprio) e memória
// via GlobalMemoryStatusEx. Ambos nativos (kernel32), sem subprocessos.

var (
	hostSampleMu            sync.Mutex
	hostCPUSampler          = NewCPUSampler()
	procGlobalMemoryStatusExHost = modkernel32CPU.NewProc("GlobalMemoryStatusEx")
)

type memoryStatusExHost struct {
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

// SampleCPUPercent retorna o uso de CPU total (0-100) desde a última chamada.
// Retorna -1 na primeira chamada (sem baseline) ou em caso de erro.
func SampleCPUPercent() float64 {
	hostSampleMu.Lock()
	defer hostSampleMu.Unlock()
	return hostCPUSampler.Sample()
}

// SampleMemoryPercent retorna o percentual de memória física em uso (0-100).
// Retorna -1 em caso de erro.
func SampleMemoryPercent() float64 {
	var ms memoryStatusExHost
	ms.cbSize = uint32(unsafe.Sizeof(ms))
	r, _, _ := procGlobalMemoryStatusExHost.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 || ms.ullTotalPhys == 0 {
		return -1
	}
	return float64(ms.ullTotalPhys-ms.ullAvailPhys) * 100.0 / float64(ms.ullTotalPhys)
}
