//go:build windows

// Package sysctrl fornece administração de processos e serviços Windows
// para uso em sessões remotas (aba "Processos" do acesso remoto).
package sysctrl

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreateToolhelp32Snapshot = modkernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = modkernel32.NewProc("Process32FirstW")
	procProcess32NextW           = modkernel32.NewProc("Process32NextW")
	procOpenProcess              = modkernel32.NewProc("OpenProcess")
	procTerminateProcess         = modkernel32.NewProc("TerminateProcess")
)

const (
	th32csSnapprocess   = 0x00000002
	processTerminate    = 0x0001
	processQueryLimited = 0x1000
	maxPath             = 260
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

// ProcessInfo representa um processo em execução.
type ProcessInfo struct {
	PID           uint32  `json:"pid"`
	ParentPID     uint32  `json:"parentPid"`
	Name          string  `json:"name"`
	Threads       uint32  `json:"threads"`
	PriorityBasis int32   `json:"priorityBase"`
	// Métricas de consumo (coletadas em process_metrics_windows.go).
	CpuPercent  float64 `json:"cpuPercent"`
	MemoryBytes uint64  `json:"memoryBytes"`
	IoReadBps   float64 `json:"ioReadBps"` // taxa de leitura (bytes/s)
	IoWriteBps  float64 `json:"ioWriteBps"`
	Connections uint32  `json:"connections"`
}

// ListProcesses lista todos os processos em execução via CreateToolhelp32Snapshot.
func ListProcesses() ([]ProcessInfo, error) {
	handle, _, e := procCreateToolhelp32Snapshot.Call(uintptr(th32csSnapprocess), 0)
	if handle == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", e)
	}
	defer windows.CloseHandle(windows.Handle(handle))

	var pe processEntry32W
	pe.dwSize = uint32(unsafe.Sizeof(pe))

	procs := make([]ProcessInfo, 0, 256)
	r, _, _ := procProcess32FirstW.Call(handle, uintptr(unsafe.Pointer(&pe)))
	for r != 0 {
		procs = append(procs, ProcessInfo{
			PID:           pe.th32ProcessID,
			ParentPID:     pe.th32ParentProcessID,
			Name:          syscall.UTF16ToString(pe.szExeFile[:]),
			Threads:       pe.cntThreads,
			PriorityBasis: pe.pcPriClassBase,
		})
		pe = processEntry32W{dwSize: uint32(unsafe.Sizeof(pe))}
		r, _, _ = procProcess32NextW.Call(handle, uintptr(unsafe.Pointer(&pe)))
	}
	// Enriquecimento com métricas (CPU/RAM/disco/rede). Processos
	// protegidos ficam com valores zerados — não interrompe a listagem.
	return enrichProcessMetrics(procs), nil
}

// KillProcess encerra um processo pelo PID via TerminateProcess.
func KillProcess(pid uint32) error {
	// Segurança: impede encerrar o processo do próprio agente (evita
	// "tiro no pé" — encerrar o agent cortaria o acesso remoto).
	if pid == uint32(os.Getpid()) {
		return fmt.Errorf("recusa: não é possível encerrar o processo do agente (PID %d)", pid)
	}

	h, _, e := procOpenProcess.Call(
		uintptr(processTerminate),
		0,
		uintptr(pid),
	)
	if h == 0 {
		return fmt.Errorf("OpenProcess(%d): %w", pid, e)
	}
	defer windows.CloseHandle(windows.Handle(h))

	r, _, e := procTerminateProcess.Call(h, 1)
	if r == 0 {
		// ERROR_ACCESS_DENIED e o caso comum para processos protegidos do sistema.
		return fmt.Errorf("TerminateProcess(%d): %w", pid, e)
	}
	return nil
}
