//go:build windows

package inventory

import (
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"discovery/internal/agentconn"
)

// ─── PDH (Performance Data Helper) Disk I/O ──────────────────────────
//
// Coleta métricas de leitura/escrita de disco (%) e latência via pdh.dll,
// eliminando completamente o subprocesso PowerShell/WMI.
//
// Contadores usados (versão em inglês, funcionam em qualquer Windows):
//   \PhysicalDisk(_Total)\% Disk Read Time       → disk read %
//   \PhysicalDisk(_Total)\% Disk Write Time      → disk write %
//   \PhysicalDisk(_Total)\% Disk Time            → disk busy %
//   \PhysicalDisk(_Total)\Avg. Disk sec/Transfer → latency (segundos)
//
// O PDH requer duas amostras (PdhCollectQueryData) para calcular %.
// A primeira chamada após init serve como baseline; chamadas subsequentes
// retornam os percentuais válidos do intervalo — casando perfeitamente
// com o modelo de heartbeat periódico (15–60s).

const (
	pdhFmtDouble = 0x00000200
	pdhCstatusOk = 0x00000000
	pdhMoreData  = 0x800007D1
)

var (
	modpdh = syscall.NewLazyDLL("pdh.dll")

	procPdhOpenQueryW               = modpdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW       = modpdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = modpdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterValue = modpdh.NewProc("PdhGetFormattedCounterValue")
	procPdhCloseQuery               = modpdh.NewProc("PdhCloseQuery")
)

// pdhFmtCounterValue mirrors the Windows PDH_FMT_COUNTERVALUE struct (16 bytes on x64).
// Layout: CStatus (uint32, 4B) + padding (4B) + DoubleValue (float64, 8B).
type pdhFmtCounterValue struct {
	CStatus     uint32
	_           uint32 // alignment
	DoubleValue float64
}

// ─── Global PDH state (inicializado uma vez e reutilizado) ──────────

var (
	diskIOPdhMu      sync.Mutex
	diskIOPdhQuery   uintptr // HQUERY
	diskIOCtrRead    uintptr // HCOUNTER
	diskIOCtrWrite   uintptr // HCOUNTER
	diskIOCtrBusy    uintptr // HCOUNTER
	diskIOCtrLatency uintptr // HCOUNTER
	diskIOPdhReady   bool    // query aberta e counters adicionados
	diskIOPdhSeeded  bool    // primeira amostra coletada
	diskIOPdhInitErr error   // erro de inicialização (evita retry)
)

// diskIOCounters contém os paths dos contadores PDH para disco.
var diskIOCounterPaths = []string{
	`\PhysicalDisk(_Total)\% Disk Read Time`,
	`\PhysicalDisk(_Total)\% Disk Write Time`,
	`\PhysicalDisk(_Total)\% Disk Time`,
	`\PhysicalDisk(_Total)\Avg. Disk sec/Transfer`,
}

// ensureDiskIOPdhInit inicializa a query PDH e adiciona os contadores.
// Thread-safe (mutex). Retorna true se inicializado com sucesso.
func ensureDiskIOPdhInit() bool {
	diskIOPdhMu.Lock()
	defer diskIOPdhMu.Unlock()

	if diskIOPdhReady {
		return true
	}
	if diskIOPdhInitErr != nil {
		return false
	}

	// PdhOpenQueryW(NULL, 0, &hQuery)
	var hQuery uintptr
	ret, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&hQuery)))
	if ret != 0 {
		diskIOPdhInitErr = fmt.Errorf("pdh: PdhOpenQueryW failed: 0x%X", ret)
		return false
	}

	counters := []*uintptr{&diskIOCtrRead, &diskIOCtrWrite, &diskIOCtrBusy, &diskIOCtrLatency}
	for i, path := range diskIOCounterPaths {
		pathPtr, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			// cleanup
			procPdhCloseQuery.Call(hQuery)
			diskIOPdhInitErr = fmt.Errorf("pdh: UTF16 encoding failed for %q: %w", path, err)
			return false
		}
		var hCtr uintptr
		ret, _, _ := procPdhAddEnglishCounterW.Call(
			hQuery,
			uintptr(unsafe.Pointer(pathPtr)),
			0,
			uintptr(unsafe.Pointer(&hCtr)),
		)
		if ret != 0 {
			procPdhCloseQuery.Call(hQuery)
			diskIOPdhInitErr = fmt.Errorf("pdh: PdhAddEnglishCounterW failed for %q: 0x%X", path, ret)
			return false
		}
		*counters[i] = hCtr
	}

	diskIOPdhQuery = hQuery
	diskIOPdhReady = true
	return true
}

// ─── Coletor principal ───────────────────────────────────────────────

// collectDiskIONativePercent retorna métricas de I/O de disco via PDH.
// Retorna (diskBusyPercent, readPercent, writePercent, latencyMs, ok).
//
// A primeira chamada apenas semeia a baseline (retorna false).
// Chamadas subsequentes retornam os percentuais do intervalo entre
// a chamada anterior e a atual.
func collectDiskIONativePercent() (float64, float64, float64, float64, bool) {
	if !ensureDiskIOPdhInit() {
		return -1, -1, -1, -1, false
	}

	diskIOPdhMu.Lock()
	defer diskIOPdhMu.Unlock()

	// Coleta a amostra atual
	ret, _, _ := procPdhCollectQueryData.Call(diskIOPdhQuery)
	if ret != 0 && ret != pdhMoreData {
		return -1, -1, -1, -1, false
	}

	// Primeira chamada: apenas semeia baseline, sem dados ainda
	if !diskIOPdhSeeded {
		diskIOPdhSeeded = true
		return -1, -1, -1, -1, false
	}

	// Obtém valores formatados dos contadores
	readPercent := getPdhCounterDouble(diskIOCtrRead)
	writePercent := getPdhCounterDouble(diskIOCtrWrite)
	busyPercent := getPdhCounterDouble(diskIOCtrBusy)
	latencySec := getPdhCounterDouble(diskIOCtrLatency)

	// Converte latência de segundos para ms
	var latencyMs float64 = -1
	if latencySec >= 0 {
		latencyMs = latencySec * 1000.0
	}

	// Valida e normaliza
	if readPercent < 0 || writePercent < 0 || busyPercent < 0 {
		return -1, -1, -1, -1, false
	}

	return busyPercent, readPercent, writePercent, latencyMs, true
}

// getPdhCounterDouble retorna o valor formatado (double) de um contador PDH.
// Retorna -1 em caso de erro.
func getPdhCounterDouble(hCounter uintptr) float64 {
	if hCounter == 0 {
		return -1
	}

	var dwType uint32
	var value pdhFmtCounterValue

	ret, _, _ := procPdhGetFormattedCounterValue.Call(
		hCounter,
		uintptr(pdhFmtDouble),
		uintptr(unsafe.Pointer(&dwType)),
		uintptr(unsafe.Pointer(&value)),
	)
	if ret != 0 {
		return -1
	}
	if value.CStatus != pdhCstatusOk {
		return -1
	}

	result := value.DoubleValue
	// Valores negativos ou NaN são inválidos
	if result < 0 {
		return 0
	}
	// PDH pode retornar valores > 100 para % (soma de read+write pode exceder 100
	// em discos com múltiplos spindles/controladoras). Limitamos a 100.
	if result > 100 {
		result = 100
	}
	return result
}

// ─── Substituição do cache de 30s ───────────────────────────────────
//
// Agora que o coletor PDH é nativo (zero subprocessos), não precisamos
// mais de cache. Coletamos a cada heartbeat sem custo adicional.
//
// A única ressalva é que a primeira chamada semeia baseline e retorna
// sem dados — tratamos isso devolvendo os valores anteriores ou -1.

var (
	diskIONativeLastAt     time.Time
	diskIONativeLastDisk   float64 = -1
	diskIONativeLastRead   float64 = -1
	diskIONativeLastWrite  float64 = -1
	diskIONativeLastRespMs float64 = -1
)

// collectHeartbeatDiskIOWindowsNative coleta métricas de disco I/O via PDH nativo.
// Substitui collectHeartbeatDiskIOWindowsCached (PowerShell + cache 30s).
func collectHeartbeatDiskIOWindowsNative(metrics *agentconn.AgentHeartbeatMetrics) {
	if metrics == nil {
		return
	}

	now := time.Now()
	diskBusy, readPct, writePct, respMs, ok := collectDiskIONativePercent()

	diskIOPdhMu.Lock()
	if ok {
		diskIONativeLastAt = now
		diskIONativeLastDisk = diskBusy
		diskIONativeLastRead = readPct
		diskIONativeLastWrite = writePct
		diskIONativeLastRespMs = respMs
		diskIOPdhMu.Unlock()

		metrics.DiskReadPercent = normalizeHeartbeatPercent(readPct)
		metrics.DiskWritePercent = normalizeHeartbeatPercent(writePct)
		metrics.DiskResponseMs = normalizeHeartbeatLatencyMs(respMs)
		if metrics.DiskPercent < 0 && diskBusy >= 0 {
			metrics.DiskPercent = normalizeHeartbeatPercent(diskBusy)
		}
		return
	}

	// Fallback: PDH ainda semeando baseline — usa último valor conhecido
	// se estiver dentro da janela de validade (2x heartbeat típico = 120s).
	diskIONativeLastAtVal := diskIONativeLastAt
	lastRead := diskIONativeLastRead
	lastWrite := diskIONativeLastWrite
	lastResp := diskIONativeLastRespMs
	lastDisk := diskIONativeLastDisk
	diskIOPdhMu.Unlock()

	if time.Since(diskIONativeLastAtVal) < 120*time.Second {
		metrics.DiskReadPercent = lastRead
		metrics.DiskWritePercent = lastWrite
		metrics.DiskResponseMs = lastResp
		if metrics.DiskPercent < 0 && lastDisk >= 0 {
			metrics.DiskPercent = lastDisk
		}
	}
}
