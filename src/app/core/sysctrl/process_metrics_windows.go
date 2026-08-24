//go:build windows

package sysctrl

import (
	"encoding/binary"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ─────────────────────────────────────────────────────────────────────
// Métricas por processo/serviço (CPU, RAM, Disco I/O, conexões de rede)
// coletadas via APIs nativas do Windows (zero subprocessos).
//
// CPU% e taxas de disco exigem duas amostras: a primeira chama semeia a
// baseline e retorna 0; chamadas subsequentes (auto-refresh do viewer)
// retornam os valores reais do intervalo.
// ─────────────────────────────────────────────────────────────────────

var (
	modpsapi    = syscall.NewLazyDLL("psapi.dll")
	modiphlpapi = syscall.NewLazyDLL("iphlpapi.dll")

	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
	procGetProcessTimes      = modkernel32.NewProc("GetProcessTimes")
	procGetProcessIoCounters = modkernel32.NewProc("GetProcessIoCounters")
	procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = modkernel32.NewProc("GetSystemTimes")

	procGetExtendedTcpTable = modiphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = modiphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	tcpTableOwnerPidAll = 5
	udpTableOwnerPid    = 1
	afInet              = 2
	afInet6             = 23
)

// processQueryAccess são os direitos usados para abrir o processo e ler
// métricas. PROCESS_QUERY_INFORMATION|PROCESS_VM_READ dá acesso total a
// WorkingSet/IO/Times; se negado (processos protegidos), cai para
// PROCESS_QUERY_LIMITED_INFORMATION (que ainda preenche o essencial).
func openProc(pid uint32) (windows.Handle, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err == nil {
		return h, nil
	}
	return windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
}

// ─── FILETIME helper ────────────────────────────────────────────────

func filetimeUint64(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

// ─── Memória por processo (WorkingSet) ───────────────────────────────

// processMemoryCounters espelha PROCESS_MEMORY_COUNTERS (psapi).
type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func processMemoryBytes(h windows.Handle) uint64 {
	var pmc processMemoryCounters
	pmc.cb = uint32(unsafe.Sizeof(pmc))
	r, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&pmc)),
		uintptr(pmc.cb),
	)
	if r == 0 {
		return 0
	}
	return uint64(pmc.WorkingSetSize)
}

// ─── Disco I/O por processo (GetProcessIoCounters) ───────────────────

// ioCounters espelha IO_COUNTERS.
type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

// processIoBytes retorna (bytes lidos, bytes gravados) acumulados.
func processIoBytes(h windows.Handle) (uint64, uint64) {
	var c ioCounters
	r, _, _ := procGetProcessIoCounters.Call(uintptr(h), uintptr(unsafe.Pointer(&c)))
	if r == 0 {
		return 0, 0
	}
	return c.ReadTransferCount, c.WriteTransferCount
}

// ─── CPU e I/O por processo (janela deslizante) ──────────────────────
//
// CPU% e taxas de I/O (bytes/s) exigem duas amostras: a primeira chamada
// semeia a baseline e retorna 0. Para evitar vazamento de memória,
// baselines de PIDs não amostrados há mais de 60s são podadas.

type procSample struct {
	kernel  uint64
	user    uint64
	ioRead  uint64
	ioWrite uint64
	wall    time.Time
}

var (
	procMu  sync.Mutex
	procMap = map[uint32]procSample{}
)

// processTimes lê os tempos de kernel/user de um processo (GetProcessTimes).
func processTimes(h windows.Handle) (kernel, user uint64, ok bool) {
	var creation, exit, k, u windows.Filetime
	r, _, _ := procGetProcessTimes.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&k)),
		uintptr(unsafe.Pointer(&u)),
	)
	if r == 0 {
		return 0, 0, false
	}
	return filetimeUint64(k), filetimeUint64(u), true
}

// sampleProcRates calcula CPU% e taxas de I/O (bytes/s) desde a última
// amostra do PID. A primeira chamada apenas semeia a baseline (retorna 0).
// Em sistemas multicore a CPU% pode exceder 100% (soma de núcleos).
func sampleProcRates(pid uint32, kernel, user, ioRead, ioWrite uint64) (cpuPct, ioReadBps, ioWriteBps float64) {
	now := time.Now()

	procMu.Lock()
	defer procMu.Unlock()

	prev, ok := procMap[pid]
	procMap[pid] = procSample{kernel: kernel, user: user, ioRead: ioRead, ioWrite: ioWrite, wall: now}
	if !ok {
		return 0, 0, 0
	}

	secs := now.Sub(prev.wall).Seconds()
	if secs <= 0 {
		return 0, 0, 0
	}

	deltaProc := (kernel + user) - (prev.kernel + prev.user)
	if deltaProc > 0 {
		// deltaProc é em ticks de 100ns → segundos: deltaProc / 1e7.
		cpuPct = float64(deltaProc) / 1e7 / secs * 100.0
	}
	if ioRead >= prev.ioRead {
		ioReadBps = float64(ioRead-prev.ioRead) / secs
	}
	if ioWrite >= prev.ioWrite {
		ioWriteBps = float64(ioWrite-prev.ioWrite) / secs
	}
	return cpuPct, ioReadBps, ioWriteBps
}

// pruneProcMap remove baselines de PIDs não amostrados há mais de maxAge,
// evitando crescimento indefinido do mapa (processos que terminam).
func pruneProcMap(now time.Time, maxAge time.Duration) {
	procMu.Lock()
	defer procMu.Unlock()
	for pid, s := range procMap {
		if now.Sub(s.wall) > maxAge {
			delete(procMap, pid)
		}
	}
}

// ─── CPU total do sistema (GetSystemTimes, janela deslizante) ────────

var (
	sysCPUMu     sync.Mutex
	sysIdle      uint64
	sysKernel    uint64
	sysUser      uint64
	sysCPUSeeded bool
)

func systemCPUPercent() (float64, bool) {
	var idle, kernel, user windows.Filetime
	r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r == 0 {
		return 0, false
	}
	i := filetimeUint64(idle)
	k := filetimeUint64(kernel)
	u := filetimeUint64(user)

	sysCPUMu.Lock()
	defer sysCPUMu.Unlock()

	if !sysCPUSeeded || i < sysIdle {
		sysIdle, sysKernel, sysUser, sysCPUSeeded = i, k, u, true
		return 0, false
	}
	dIdle := i - sysIdle
	dTotal := (k + u) - (sysKernel + sysUser)
	sysIdle, sysKernel, sysUser = i, k, u
	if dTotal == 0 {
		return 0, false
	}
	pct := float64(dTotal-dIdle) * 100.0 / float64(dTotal)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct, true
}

// ─── Memória total do sistema (GlobalMemoryStatusEx) ─────────────────

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

func systemMemory() (total uint64, used uint64, pct float64, ok bool) {
	var ms memoryStatusEx
	ms.cbSize = uint32(unsafe.Sizeof(ms))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 || ms.ullTotalPhys == 0 {
		return 0, 0, 0, false
	}
	used = ms.ullTotalPhys - ms.ullAvailPhys
	pct = float64(used) * 100.0 / float64(ms.ullTotalPhys)
	return ms.ullTotalPhys, used, pct, true
}

// ─── Conexões de rede por processo (GetExtendedTcpTable/UdpTable) ────

type tcpRowOwnerPid struct {
	State      uint32
	LocalAddr  [4]byte
	LocalPort  uint32
	RemoteAddr [4]byte
	RemotePort uint32
	OwningPid  uint32
}

type tcp6RowOwnerPid struct {
	LocalAddr   [16]byte
	LocalScope  uint32
	LocalPort   uint32
	RemoteAddr  [16]byte
	RemoteScope uint32
	RemotePort  uint32
	State       uint32
	OwningPid   uint32
}

type udpRowOwnerPid struct {
	LocalAddr [4]byte
	LocalPort uint32
	OwningPid uint32
}

type udp6RowOwnerPid struct {
	LocalAddr  [16]byte
	LocalScope uint32
	LocalPort  uint32
	OwningPid  uint32
}

// connCountsByPid retorna o número de conexões TCP+UDP (IPv4+IPv6) por PID.
// Uma única enumeração para toda a lista (evita varrer as tabelas por PID).
func connCountsByPid() map[uint32]uint32 {
	counts := make(map[uint32]uint32)
	add := func(pids []uint32) {
		for _, pid := range pids {
			if pid != 0 {
				counts[pid]++
			}
		}
	}
	add(tcpRowPids(afInet))
	add(tcpRowPids(afInet6))
	add(udpRowPids(afInet))
	add(udpRowPids(afInet6))
	return counts
}

func tcpRowPids(family int) []uint32 {
	size := uint32(0)
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), uintptr(tcpTableOwnerPidAll), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(family),
		uintptr(tcpTableOwnerPidAll),
		0,
	)
	if r != 0 {
		return nil
	}

	num := binary.LittleEndian.Uint32(buf[0:4])
	pids := make([]uint32, 0, num)
	rowSize := int(unsafe.Sizeof(tcpRowOwnerPid{}))
	if family == afInet6 {
		rowSize = int(unsafe.Sizeof(tcp6RowOwnerPid{}))
	}
	offset := 4
	for i := uint32(0); i < num; i++ {
		if offset+rowSize > len(buf) {
			break
		}
		var pid uint32
		if family == afInet6 {
			row := (*tcp6RowOwnerPid)(unsafe.Pointer(&buf[offset]))
			pid = row.OwningPid
		} else {
			row := (*tcpRowOwnerPid)(unsafe.Pointer(&buf[offset]))
			pid = row.OwningPid
		}
		pids = append(pids, pid)
		offset += rowSize
	}
	return pids
}

func udpRowPids(family int) []uint32 {
	size := uint32(0)
	procGetExtendedUdpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), uintptr(udpTableOwnerPid), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedUdpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(family),
		uintptr(udpTableOwnerPid),
		0,
	)
	if r != 0 {
		return nil
	}

	num := binary.LittleEndian.Uint32(buf[0:4])
	pids := make([]uint32, 0, num)
	rowSize := int(unsafe.Sizeof(udpRowOwnerPid{}))
	if family == afInet6 {
		rowSize = int(unsafe.Sizeof(udp6RowOwnerPid{}))
	}
	offset := 4
	for i := uint32(0); i < num; i++ {
		if offset+rowSize > len(buf) {
			break
		}
		var pid uint32
		if family == afInet6 {
			row := (*udp6RowOwnerPid)(unsafe.Pointer(&buf[offset]))
			pid = row.OwningPid
		} else {
			row := (*udpRowOwnerPid)(unsafe.Pointer(&buf[offset]))
			pid = row.OwningPid
		}
		pids = append(pids, pid)
		offset += rowSize
	}
	return pids
}

// ─── Orquestração ────────────────────────────────────────────────────

// processMetrics coleta CPU%, RAM (working set em bytes) e taxas de I/O
// (bytes/s) para um único PID. Processos inacessíveis retornam zeros.
func processMetrics(pid uint32) (cpuPercent float64, memoryBytes uint64, ioReadBps float64, ioWriteBps float64) {
	if pid == 0 {
		return 0, 0, 0, 0
	}
	h, err := openProc(pid)
	if err != nil {
		return 0, 0, 0, 0
	}
	defer windows.CloseHandle(h)

	memoryBytes = processMemoryBytes(h)
	ioRead, ioWrite := processIoBytes(h)

	if kernel, user, ok := processTimes(h); ok {
		cpuPercent, ioReadBps, ioWriteBps = sampleProcRates(pid, kernel, user, ioRead, ioWrite)
	}
	return cpuPercent, memoryBytes, ioReadBps, ioWriteBps
}

// enrichProcessMetrics preenche as métricas de todos os processos.
func enrichProcessMetrics(procs []ProcessInfo) []ProcessInfo {
	conns := connCountsByPid()
	now := time.Now()
	for i := range procs {
		procs[i].CpuPercent, procs[i].MemoryBytes, procs[i].IoReadBps, procs[i].IoWriteBps = processMetrics(procs[i].PID)
		procs[i].Connections = conns[procs[i].PID]
	}
	pruneProcMap(now, 60*time.Second)
	return procs
}

// enrichServiceMetrics preenche as métricas dos serviços em execução
// (aqueles com PID associado). Serviços parados ficam com campos zerados.
func enrichServiceMetrics(svcs []ServiceInfo) []ServiceInfo {
	conns := connCountsByPid()
	now := time.Now()
	for i := range svcs {
		if svcs[i].PID == 0 {
			continue
		}
		svcs[i].CpuPercent, svcs[i].MemoryBytes, svcs[i].IoReadBps, svcs[i].IoWriteBps = processMetrics(svcs[i].PID)
		svcs[i].Connections = conns[svcs[i].PID]
	}
	pruneProcMap(now, 60*time.Second)
	return svcs
}

// SystemInfo resume o uso do host (RAM + CPU totais).
type SystemInfo struct {
	TotalMemoryBytes uint64  `json:"totalMemoryBytes"`
	UsedMemoryBytes  uint64  `json:"usedMemoryBytes"`
	MemoryPercent    float64 `json:"memoryPercent"`
	CpuPercent       float64 `json:"cpuPercent"`
}

// GetSystemInfo retorna o resumo de RAM e CPU do host.
func GetSystemInfo() SystemInfo {
	si := SystemInfo{}
	if t, u, p, ok := systemMemory(); ok {
		si.TotalMemoryBytes = t
		si.UsedMemoryBytes = u
		si.MemoryPercent = p
	}
	if c, ok := systemCPUPercent(); ok {
		si.CpuPercent = c
	}
	return si
}