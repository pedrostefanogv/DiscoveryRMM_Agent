//go:build windows

package platform

import (
	"sync"
	"syscall"
	"unsafe"
)

var (
	modkernel32CPU        = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemTimesCPU = modkernel32CPU.NewProc("GetSystemTimes")
)

// CPUSampler mede uso de CPU via GetSystemTimes com estado próprio,
// independente do heartbeat. Adequado para throttling em hot loops
// (chamado a cada 1-2s durante build de manifest).
type CPUSampler struct {
	mu          sync.Mutex
	initialized bool
	lastIdle    uint64
	lastKernel  uint64
	lastUser    uint64
}

// NewCPUSampler cria um sampler com estado isolado.
func NewCPUSampler() *CPUSampler {
	return &CPUSampler{}
}

// Sample retorna o percentual de CPU (0-100) desde a última chamada.
// Retorna -1 se ainda não houver baseline (primeira chamada) ou erro.
func (s *CPUSampler) Sample() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	var idle, kernel, user syscall.Filetime
	r, _, _ := procGetSystemTimesCPU.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r == 0 {
		return -1
	}

	idleVal := uint64(idle.HighDateTime)<<32 | uint64(idle.LowDateTime)
	kernelVal := uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)
	userVal := uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)

	if !s.initialized || idleVal < s.lastIdle {
		s.lastIdle = idleVal
		s.lastKernel = kernelVal
		s.lastUser = userVal
		s.initialized = true
		return -1
	}

	deltaIdle := idleVal - s.lastIdle
	deltaTotal := (kernelVal + userVal) - (s.lastKernel + s.lastUser)

	s.lastIdle = idleVal
	s.lastKernel = kernelVal
	s.lastUser = userVal

	if deltaTotal == 0 {
		return -1
	}

	percent := float64(deltaTotal-deltaIdle) * 100.0 / float64(deltaTotal)
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return percent
}
