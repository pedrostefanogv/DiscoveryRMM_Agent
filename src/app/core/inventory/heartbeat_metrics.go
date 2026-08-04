package inventory

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"discovery/app/core/agentconn"
	"discovery/app/core/processutil"
)

// heartbeatMetricsSQL é uma única query osquery que coleta todas as métricas
// necessárias para o heartbeat (CPU, memória, disco, hostname, uptime,
// processos) em uma única chamada via socket Thrift. Isso elimina o
// overhead de múltiplas queries e minimiza o tempo de execução.
//
// A query é compatível com Windows, onde as tabelas system_info.physical_memory
// e processes.resident_size substituem a tabela memory_info (inexistente no
// osqueryi padrão sem a extension inc/win). No Windows a coluna load_percentage
// de cpu_info não existe; nesse caso cpu_percent vem como NULL e é tratado
// no fallback do caller.
//
// Valores NULL são tratados como fallback no mapHeartbeatRow.
const heartbeatMetricsSQL = `
SELECT
  si.hostname,
  NULL AS cpu_percent,
  CASE WHEN si.physical_memory > 0
    THEN ROUND(CAST(si.physical_memory AS REAL) / 1073741824.0, 2)
  END AS memory_total_gb,
  ROUND(CAST(COALESCE(mem_agg.resident_bytes, 0) AS REAL) / 1073741824.0, 2) AS memory_used_gb,
  CASE WHEN si.physical_memory > 0
    THEN ROUND(COALESCE(mem_agg.resident_bytes, 0) * 100.0 / CAST(si.physical_memory AS REAL), 1)
    ELSE NULL
  END AS memory_percent,
  ROUND(d.disk_total_gb, 2)      AS disk_total_gb,
  ROUND(d.disk_used_gb, 2)       AS disk_used_gb,
	ROUND(p.disk_percent, 1)       AS disk_percent,
	ROUND(p.disk_read_percent, 1)  AS disk_read_percent,
	ROUND(p.disk_write_percent, 1) AS disk_write_percent,
	NULL                           AS disk_response_ms,
  CAST(up.total_seconds AS INTEGER) AS uptime_seconds,
  CAST((SELECT COUNT(*) FROM processes) AS INTEGER) AS process_count
FROM system_info si
CROSS JOIN (SELECT total_seconds FROM uptime LIMIT 1) up
LEFT JOIN (
  SELECT SUM(COALESCE(resident_size, 0)) AS resident_bytes FROM processes
) mem_agg
LEFT JOIN (
  SELECT
    MAX(size) / 1073741824.0          AS disk_total_gb,
    MAX(size - free_space) / 1073741824.0 AS disk_used_gb
  FROM logical_drives
  WHERE device_id = 'C:' AND size > 0
) d
LEFT JOIN (
	SELECT
		MAX(CAST(percent_disk_time AS REAL))       AS disk_percent,
		MAX(CAST(percent_disk_read_time AS REAL))  AS disk_read_percent,
		MAX(CAST(percent_disk_write_time AS REAL)) AS disk_write_percent
	FROM physical_disk_performance
	WHERE name LIKE '% C:'
) p
LIMIT 1
`

// CollectHeartbeatMetrics coleta métricas do sistema para o heartbeat
// usando APIs nativas do Windows sempre que disponível, com osquery como
// fallback para plataformas não-Windows onde as APIs nativas não existem.
//
// Windows: todas as métricas (CPU, memória, disco, uptime, processos) são
// coletadas via syscalls nativos (kernel32.dll), eliminando subprocessos.
// Isso reduz o consumo de CPU de ~3 subprocessos/heartbeat para zero.
//
// Não-Windows: mantém a estratégia original de socket osqueryd/osqueryi,
// já que Linux/macOS possuem leitura eficiente de /proc e sysfs.
func CollectHeartbeatMetrics(ctx context.Context) *agentconn.AgentHeartbeatMetrics {
	metrics := &agentconn.AgentHeartbeatMetrics{
		CpuPercent:       -1,
		MemoryPercent:    -1,
		DiskPercent:      -1,
		DiskReadPercent:  -1,
		DiskWritePercent: -1,
		DiskResponseMs:   -1,
	}

	if runtime.GOOS == "windows" {
		collectHeartbeatMetricsWindows(ctx, metrics)
	} else {
		collectHeartbeatMetricsOsquery(ctx, metrics)
	}

	if !hasAnyHeartbeatMetric(metrics) {
		return nil
	}

	return metrics
}

// collectHeartbeatMetricsWindows coleta TODAS as métricas via APIs nativas
// do Windows (kernel32.dll), sem subprocessos. Zero osquery, zero PowerShell.
func collectHeartbeatMetricsWindows(ctx context.Context, metrics *agentconn.AgentHeartbeatMetrics) {
	// CPU: sliding-window GetSystemTimes (instantâneo, sem subprocesso)
	if cpuPercent, ok := collectWindowsCPUPercentNativeFunc(); ok {
		metrics.CpuPercent = cpuPercent
	} else {
		// Fallback: PowerShell/WMI (apenas quando GetSystemTimes falha — raro)
		if cpuPercent, ok := collectWindowsCPUPercentFunc(ctx); ok {
			metrics.CpuPercent = cpuPercent
		}
	}

	// Memória: GlobalMemoryStatusEx (API nativa, sempre confiável)
	if totalGB, usedGB, percent, ok := collectWindowsMemoryNativeFunc(); ok && totalGB > 0 {
		metrics.MemoryTotalGb = totalGB
		metrics.MemoryUsedGb = usedGB
		metrics.MemoryPercent = percent
	} else if totalGB, usedGB, percent, ok := collectHeartbeatMemoryMetricsFunc(ctx); ok && totalGB > 0 {
		metrics.MemoryTotalGb = totalGB
		metrics.MemoryUsedGb = usedGB
		metrics.MemoryPercent = percent
	}

	// Disco (espaço): GetDiskFreeSpaceExW (API nativa)
	if totalGB, usedGB, percent, ok := collectDiskSpaceNativeFunc(); ok {
		metrics.DiskTotalGb = totalGB
		metrics.DiskUsedGb = usedGB
		metrics.DiskPercent = percent
	}

	// Disco I/O: PDH nativo (pdh.dll) — zero subprocessos, sem cache
	collectHeartbeatDiskIOWindowsNative(metrics)

	// Uptime: GetTickCount64 (API nativa)
	if uptime := collectUptimeSecondsFunc(); uptime > 0 {
		metrics.UptimeSeconds = uptime
	}

	// Processos: CreateToolhelp32Snapshot (API nativa)
	if count := collectProcessCountNativeFunc(); count >= 0 {
		metrics.ProcessCount = count
	}

	// Temperatura da CPU: PDH nativo (pdh.dll) — zero subprocessos
	collectHeartbeatCPUTemperatureWindowsNative(metrics)
}

// collectHeartbeatMetricsOsquery é o fallback para plataformas não-Windows
// usando o mecanismo original de socket osquery.
func collectHeartbeatMetricsOsquery(ctx context.Context, metrics *agentconn.AgentHeartbeatMetrics) {
	bin, err := findOsqueryBinaryFunc()
	if err != nil {
		return
	}

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	queries := []osqueryQuery{
		{name: "heartbeat_metrics", sql: heartbeatMetricsSQL, required: true},
	}

	var results map[string]osqueryResult

	// 1. Tentar socket osqueryd (daemon rodando)
	if socketPath := findOsquerydSocket(); socketPath != "" {
		results = runQueriesViaSocket(runCtx, socketPath, queries, nil)
		if results != nil {
			if r, ok := results["heartbeat_metrics"]; ok && r.err == nil && len(r.rows) > 0 {
				if m := mapHeartbeatRow(r.rows[0]); m != nil {
					*metrics = *m
				}
			}
		}
		return
	}

	// 2. Try keep-alive pool (reuses osqueryi from previous calls).
	if socketPath := acquireOsqueryiSocket(); socketPath != "" {
		results = runQueriesViaSocket(runCtx, socketPath, queries, nil)
		if results != nil {
			if r, ok := results["heartbeat_metrics"]; ok && r.err == nil && len(r.rows) > 0 {
				if m := mapHeartbeatRow(r.rows[0]); m != nil {
					*metrics = *m
				}
			}
		}
		return
	}

	// 3. Fallback: osqueryi transient socket (stored in pool).
	proc, startErr := startOsqueryiSocket(runCtx, bin)
	if startErr != nil {
		return
	}
	storeOsqueryiSocket(proc)

	results = runQueriesViaSocket(runCtx, proc.socketPath, queries, nil)
	if results != nil {
		if r, ok := results["heartbeat_metrics"]; ok && r.err == nil && len(r.rows) > 0 {
			if m := mapHeartbeatRow(r.rows[0]); m != nil {
				*metrics = *m
			}
		}
	}

	// Aplicar fallbacks para preencher lacunas do osquery
	applyHeartbeatCPUFallback(ctx, metrics)
	applyHeartbeatDiskIOFallback(ctx, metrics)
	applyHeartbeatMemoryFallback(ctx, metrics)
}

func hasAnyHeartbeatMetric(metrics *agentconn.AgentHeartbeatMetrics) bool {
	if metrics == nil {
		return false
	}

	return strings.TrimSpace(metrics.Hostname) != "" ||
		metrics.CpuPercent >= 0 ||
		metrics.MemoryPercent >= 0 ||
		metrics.MemoryTotalGb > 0 ||
		metrics.MemoryUsedGb > 0 ||
		metrics.DiskPercent >= 0 ||
		metrics.DiskTotalGb > 0 ||
		metrics.DiskUsedGb > 0 ||
		metrics.DiskReadPercent >= 0 ||
		metrics.DiskWritePercent >= 0 ||
		metrics.DiskResponseMs >= 0 ||
		metrics.UptimeSeconds > 0 ||
		metrics.ProcessCount > 0 ||
		metrics.CpuTemperatureCelsius >= 0
}

// mapHeartbeatRow converte uma linha raw do osquery em AgentHeartbeatMetrics.
// Usa parseFloat64 para lidar com a conversão de string para número.
func mapHeartbeatRow(row map[string]any) *agentconn.AgentHeartbeatMetrics {
	if row == nil {
		return nil
	}
	return &agentconn.AgentHeartbeatMetrics{
		Hostname:         getString(row, "hostname"),
		CpuPercent:       parseHeartbeatFloat(row, "cpu_percent", -1),
		MemoryPercent:    parseHeartbeatFloat(row, "memory_percent", -1),
		MemoryTotalGb:    parseHeartbeatFloat(row, "memory_total_gb", 0),
		MemoryUsedGb:     parseHeartbeatFloat(row, "memory_used_gb", 0),
		DiskPercent:      parseHeartbeatFloat(row, "disk_percent", -1),
		DiskTotalGb:      parseHeartbeatFloat(row, "disk_total_gb", 0),
		DiskUsedGb:       parseHeartbeatFloat(row, "disk_used_gb", 0),
		DiskReadPercent:  parseHeartbeatFloat(row, "disk_read_percent", -1),
		DiskWritePercent: parseHeartbeatFloat(row, "disk_write_percent", -1),
		DiskResponseMs:   parseHeartbeatFloat(row, "disk_response_ms", -1),
		UptimeSeconds:    int64(parseHeartbeatFloat(row, "uptime_seconds", 0)),
		ProcessCount:     int(parseHeartbeatFloat(row, "process_count", 0)),
	}
}

// parseHeartbeatFloat extrai um valor float64 de uma row do osquery.
// O osquery retorna tudo como string no JSON, então precisa de conversão.
func parseHeartbeatFloat(row map[string]any, key string, def float64) float64 {
	v, ok := row[key]
	if !ok || v == nil {
		return def
	}
	switch val := v.(type) {
	case float64:
		return val
	case string:
		if val == "" {
			return def
		}
		var f float64
		if _, err := fmt.Sscanf(val, "%f", &f); err == nil {
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return def
			}
			return f
		}
		return def
	case int64:
		return float64(val)
	case int:
		return float64(val)
	default:
		return def
	}
}

var collectWindowsCPUPercentFunc = CollectWindowsCPUPercent
var collectWindowsDiskIOMetricsFunc = CollectWindowsDiskIOMetrics
var collectHeartbeatMemoryMetricsFunc = collectHeartbeatMemoryMetrics
var findOsqueryBinaryFunc = FindOsqueryBinary

// Native Windows collectors — mockable for testing.
var (
	collectWindowsCPUPercentNativeFunc = collectWindowsCPUPercentNative
	collectWindowsMemoryNativeFunc     = collectWindowsMemoryNative
	collectDiskSpaceNativeFunc         = collectDiskSpaceNative
	collectUptimeSecondsFunc           = collectUptimeSeconds
	collectProcessCountNativeFunc      = collectProcessCountNative
)

func applyHeartbeatCPUFallback(ctx context.Context, metrics *agentconn.AgentHeartbeatMetrics) {
	if metrics == nil || metrics.CpuPercent >= 0 {
		return
	}
	if cpuPercent, ok := collectWindowsCPUPercentFunc(ctx); ok {
		metrics.CpuPercent = cpuPercent
	}
}

func applyHeartbeatDiskIOFallback(ctx context.Context, metrics *agentconn.AgentHeartbeatMetrics) {
	if metrics == nil {
		return
	}

	diskPercent := metrics.DiskPercent
	readPercent := metrics.DiskReadPercent
	writePercent := metrics.DiskWritePercent
	responseMs := metrics.DiskResponseMs

	diskMissing := diskPercent < 0
	readMissing := readPercent < 0
	writeMissing := writePercent < 0
	responseMissing := responseMs < 0
	if diskMissing || readMissing || writeMissing || responseMissing {
		collectedDiskPercent, collectedRead, collectedWrite, collectedResponseMs, ok := collectWindowsDiskIOMetricsFunc(ctx)
		if ok {
			if diskMissing {
				diskPercent = collectedDiskPercent
			}
			if readMissing {
				readPercent = collectedRead
			}
			if writeMissing {
				writePercent = collectedWrite
			}
			if responseMissing && collectedResponseMs >= 0 {
				responseMs = collectedResponseMs
			}
		}
	}

	if diskPercent >= 0 {
		metrics.DiskPercent = normalizeHeartbeatPercent(diskPercent)
	} else {
		metrics.DiskPercent = -1
	}

	if readPercent >= 0 {
		metrics.DiskReadPercent = normalizeHeartbeatPercent(readPercent)
	} else {
		metrics.DiskReadPercent = -1
	}

	if writePercent >= 0 {
		metrics.DiskWritePercent = normalizeHeartbeatPercent(writePercent)
	} else {
		metrics.DiskWritePercent = -1
	}

	if responseMs >= 0 {
		metrics.DiskResponseMs = normalizeHeartbeatLatencyMs(responseMs)
	} else {
		metrics.DiskResponseMs = -1
	}
}

func applyHeartbeatMemoryFallback(ctx context.Context, metrics *agentconn.AgentHeartbeatMetrics) {
	if metrics == nil {
		return
	}

	// Se já temos total e usado, podemos derivar percent sem nenhum collector.
	if metrics.MemoryTotalGb > 0 && metrics.MemoryUsedGb >= 0 && metrics.MemoryPercent < 0 {
		metrics.MemoryPercent = normalizeHeartbeatPercent((metrics.MemoryUsedGb * 100.0) / metrics.MemoryTotalGb)
		return
	}

	// On Windows, always collect memory via native API (GlobalMemoryStatusEx).
	if runtime.GOOS == "windows" {
		if totalGB, usedGB, percent, ok := collectWindowsMemoryNativeFunc(); ok && totalGB > 0 {
			metrics.MemoryTotalGb = totalGB
			metrics.MemoryUsedGb = usedGB
			metrics.MemoryPercent = percent
			return
		}

		// Second fallback: high-level collector
		if totalGB, usedGB, percent, ok := collectHeartbeatMemoryMetricsFunc(ctx); ok && totalGB > 0 {
			metrics.MemoryTotalGb = totalGB
			metrics.MemoryUsedGb = usedGB
			metrics.MemoryPercent = percent
			return
		}
	}

	// Non-Windows or all Windows fallbacks failed: calculate from osquery data
	if metrics.MemoryPercent < 0 && metrics.MemoryTotalGb > 0 && metrics.MemoryUsedGb >= 0 {
		metrics.MemoryPercent = normalizeHeartbeatPercent((metrics.MemoryUsedGb * 100.0) / metrics.MemoryTotalGb)
		return
	}

	if metrics.MemoryTotalGb > 0 {
		metrics.MemoryTotalGb = normalizeHeartbeatGigabytes(metrics.MemoryTotalGb)
	} else {
		metrics.MemoryTotalGb = 0
	}

	if metrics.MemoryUsedGb > 0 {
		metrics.MemoryUsedGb = normalizeHeartbeatGigabytes(metrics.MemoryUsedGb)
	} else if metrics.MemoryUsedGb < 0 {
		metrics.MemoryUsedGb = 0
	}

	if metrics.MemoryTotalGb > 0 && metrics.MemoryUsedGb > metrics.MemoryTotalGb {
		metrics.MemoryUsedGb = metrics.MemoryTotalGb
	}

	if metrics.MemoryPercent < 0 && metrics.MemoryTotalGb > 0 && metrics.MemoryUsedGb >= 0 {
		metrics.MemoryPercent = normalizeHeartbeatPercent((metrics.MemoryUsedGb * 100.0) / metrics.MemoryTotalGb)
	}
	if metrics.MemoryPercent >= 0 {
		metrics.MemoryPercent = normalizeHeartbeatPercent(metrics.MemoryPercent)
	} else {
		metrics.MemoryPercent = -1
	}
}

func normalizeHeartbeatGigabytes(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return math.Round(value*100) / 100
}

func collectHeartbeatMemoryMetrics(ctx context.Context) (float64, float64, float64, bool) {
	switch runtime.GOOS {
	case "windows":
		return CollectWindowsMemoryMetrics(ctx)
	case "linux":
		return CollectLinuxMemoryMetrics()
	default:
		return -1, -1, -1, false
	}
}

// CollectWindowsMemoryMetrics coleta memoria total/usada (%) usando
// GlobalMemoryStatusEx (API nativa Windows). Funciona em qualquer
// contexto de execucao (SYSTEM, servico, usuario logado).
// Retorna (totalGb, usedGb, percent, true) quando os valores sao validos.
func CollectWindowsMemoryMetrics(ctx context.Context) (float64, float64, float64, bool) {
	if runtime.GOOS != "windows" {
		return -1, -1, -1, false
	}

	// Tentativa 1: API nativa GlobalMemoryStatusEx
	if totalGB, usedGB, percent, ok := collectWindowsMemoryNativeFunc(); ok {
		return totalGB, usedGB, percent, true
	}

	// Tentativa 2: Fallback para PowerShell/WMI (cobertura adicional)
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	script := `$ErrorActionPreference = 'Stop'
$os = Get-CimInstance Win32_OperatingSystem | Select-Object -First 1
if ($null -eq $os -or [double]$os.TotalVisibleMemorySize -le 0) {
	''
} else {
	$totalKb = [double]$os.TotalVisibleMemorySize
	$freeKb = [double]$os.FreePhysicalMemory
	$usedKb = [math]::Max($totalKb - $freeKb, 0)
	$totalGb = [math]::Round($totalKb / 1048576.0, 2)
	$usedGb = [math]::Round($usedKb / 1048576.0, 2)
	$percent = [math]::Round(($usedKb * 100.0) / $totalKb, 1)
	$ci = [System.Globalization.CultureInfo]::InvariantCulture
	"$($totalGb.ToString($ci)),$($usedGb.ToString($ci)),$($percent.ToString($ci))"
}`

	cmd := exec.CommandContext(runCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return -1, -1, -1, false
	}

	raw := strings.TrimSpace(string(output))
	if raw == "" || strings.EqualFold(raw, "null") {
		return -1, -1, -1, false
	}

	parts := strings.Split(raw, ",")
	if len(parts) != 3 {
		return -1, -1, -1, false
	}

	totalGB, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil || math.IsNaN(totalGB) || math.IsInf(totalGB, 0) || totalGB <= 0 {
		return -1, -1, -1, false
	}

	usedGB, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil || math.IsNaN(usedGB) || math.IsInf(usedGB, 0) || usedGB < 0 {
		return -1, -1, -1, false
	}

	percent, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err != nil || math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 {
		return -1, -1, -1, false
	}

	if usedGB > totalGB {
		usedGB = totalGB
	}

	return normalizeHeartbeatGigabytes(totalGB), normalizeHeartbeatGigabytes(usedGB), normalizeHeartbeatPercent(percent), true
}

// CollectLinuxMemoryMetrics coleta memoria total/usada (%) a partir de /proc/meminfo.
// Retorna (totalGb, usedGb, percent, true) quando os valores sao validos.
func CollectLinuxMemoryMetrics() (float64, float64, float64, bool) {
	if runtime.GOOS != "linux" {
		return -1, -1, -1, false
	}

	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return -1, -1, -1, false
	}

	var totalKB float64
	var availableKB float64
	var freeKB float64
	var buffersKB float64
	var cachedKB float64

	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		value, parseErr := strconv.ParseFloat(strings.TrimSpace(fields[1]), 64)
		if parseErr != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			continue
		}

		switch strings.TrimSuffix(strings.TrimSpace(fields[0]), ":") {
		case "MemTotal":
			totalKB = value
		case "MemAvailable":
			availableKB = value
		case "MemFree":
			freeKB = value
		case "Buffers":
			buffersKB = value
		case "Cached":
			cachedKB = value
		}
	}

	if totalKB <= 0 {
		return -1, -1, -1, false
	}

	if availableKB <= 0 {
		availableKB = freeKB + buffersKB + cachedKB
	}
	if availableKB < 0 {
		availableKB = 0
	}
	if availableKB > totalKB {
		availableKB = totalKB
	}

	usedKB := totalKB - availableKB
	totalGB := normalizeHeartbeatGigabytes(totalKB / 1048576.0)
	usedGB := normalizeHeartbeatGigabytes(usedKB / 1048576.0)
	percent := normalizeHeartbeatPercent((usedKB * 100.0) / totalKB)

	return totalGB, usedGB, percent, true
}

func normalizeHeartbeatPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return -1
	}
	return math.Round(value*10) / 10
}

func normalizeHeartbeatLatencyMs(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return -1
	}
	return math.Round(value*1000) / 1000
}

// CollectWindowsDiskIOMetrics coleta utilizacao de leitura/escrita (%) e
// tempo medio de resposta (ms) do disco C via CIM.
// Retorna (diskPercent, readPercent, writePercent, responseMs, true) quando os percentuais forem validos.
// responseMs pode retornar -1 quando indisponivel no host.
func CollectWindowsDiskIOMetrics(ctx context.Context) (float64, float64, float64, float64, bool) {
	if runtime.GOOS != "windows" {
		return -1, -1, -1, -1, false
	}

	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	script := `$ErrorActionPreference = 'Stop'
$sample = Get-CimInstance -ClassName Win32_PerfFormattedData_PerfDisk_PhysicalDisk -Filter "Name LIKE '% C:'" | Select-Object -First 1
if ($null -eq $sample) {
	''
} else {
	$busy = [double]$sample.PercentDiskTime
	$read = [double]$sample.PercentDiskReadTime
	$write = [double]$sample.PercentDiskWriteTime
	$responseMs = -1
	if ($sample.PSObject.Properties['AvgDisksecPerTransfer']) {
		$responseSec = [double]$sample.AvgDisksecPerTransfer
		$responseMs = [math]::Round($responseSec * 1000.0, 3)
	}
	if ($null -eq $busy -or $null -eq $read -or $null -eq $write) {
		''
	} else {
		$ci = [System.Globalization.CultureInfo]::InvariantCulture
		$busyText = ([math]::Round([double]$busy, 1)).ToString($ci)
		$readText = ([math]::Round([double]$read, 1)).ToString($ci)
		$writeText = ([math]::Round([double]$write, 1)).ToString($ci)
		$responseText = ([double]$responseMs).ToString($ci)
		"$busyText,$readText,$writeText,$responseText"
	}
}
`

	cmd := exec.CommandContext(runCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return -1, -1, -1, -1, false
	}

	raw := strings.TrimSpace(string(output))
	if raw == "" || strings.EqualFold(raw, "null") {
		return -1, -1, -1, -1, false
	}

	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return -1, -1, -1, -1, false
	}

	diskPercent, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil || math.IsNaN(diskPercent) || math.IsInf(diskPercent, 0) || diskPercent < 0 {
		return -1, -1, -1, -1, false
	}
	readPercent, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil || math.IsNaN(readPercent) || math.IsInf(readPercent, 0) || readPercent < 0 {
		return -1, -1, -1, -1, false
	}
	writePercent, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err != nil || math.IsNaN(writePercent) || math.IsInf(writePercent, 0) || writePercent < 0 {
		return -1, -1, -1, -1, false
	}
	responseMs := -1.0
	if parsedResponse, err := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64); err == nil {
		responseMs = normalizeHeartbeatLatencyMs(parsedResponse)
	}

	return normalizeHeartbeatPercent(diskPercent), normalizeHeartbeatPercent(readPercent), normalizeHeartbeatPercent(writePercent), responseMs, true
}

// CollectWindowsCPUPercent coleta o uso medio de CPU via Win32_Processor.
// Retorna (valor, true) quando a coleta for valida; caso contrario (-1, false).
func CollectWindowsCPUPercent(ctx context.Context) (float64, bool) {
	if runtime.GOOS != "windows" {
		return -1, false
	}

	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	script := `$ErrorActionPreference = 'Stop'
$cpu = Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average
if ($null -eq $cpu -or $null -eq $cpu.Average) {
  ''
} else {
  [math]::Round([double]$cpu.Average, 1).ToString([System.Globalization.CultureInfo]::InvariantCulture)
}`

	cmd := exec.CommandContext(runCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return -1, false
	}

	raw := strings.TrimSpace(string(output))
	if raw == "" || strings.EqualFold(raw, "null") {
		return -1, false
	}

	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return -1, false
	}
	if v > 100 {
		v = 100
	}
	return v, true
}
