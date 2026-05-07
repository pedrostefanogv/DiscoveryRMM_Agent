package inventory

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"discovery/internal/agentconn"
	"discovery/internal/processutil"
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
  ROUND(CAST(si.physical_memory AS REAL) / 1073741824.0, 2) AS memory_total_gb,
  ROUND(CAST(COALESCE(mem_agg.resident_bytes, 0) AS REAL) / 1073741824.0, 2) AS memory_used_gb,
  CASE WHEN si.physical_memory > 0
    THEN ROUND(COALESCE(mem_agg.resident_bytes, 0) * 100.0 / CAST(si.physical_memory AS REAL), 1)
    ELSE NULL
  END AS memory_percent,
  ROUND(d.disk_total_gb, 2)      AS disk_total_gb,
  ROUND(d.disk_used_gb, 2)       AS disk_used_gb,
  ROUND(d.disk_percent, 1)       AS disk_percent,
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
    MAX(size - free_space) / 1073741824.0 AS disk_used_gb,
    CASE WHEN MAX(size) > 0
      THEN MAX(size - free_space) * 100.0 / MAX(size)
      ELSE NULL
    END                               AS disk_percent
  FROM logical_drives
  WHERE device_id = 'C:' AND size > 0
) d
LIMIT 1
`

// CollectHeartbeatMetrics coleta métricas do sistema para o heartbeat
// usando uma única query osquery otimizada. A estratégia de execução segue
// a mesma prioridade do Provider: (1) socket osqueryd, (2) osqueryi socket.
//
// Retorna um AgentHeartbeatMetrics preenchido ou nil em caso de erro.
// Quando cpu_percent vier ausente/invalidado no osquery, aplica fallback
// local via PowerShell/WMI para evitar omissao de CPU no heartbeat.
func CollectHeartbeatMetrics(ctx context.Context) *agentconn.AgentHeartbeatMetrics {
	bin, err := FindOsqueryBinary()
	if err != nil {
		return nil
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
					applyHeartbeatCPUFallback(runCtx, m)
					return m
				}
			}
		}
	}

	// 2. Fallback: osqueryi transient socket
	proc, err := startOsqueryiSocket(runCtx, bin)
	if err != nil {
		return nil
	}
	defer proc.stop()

	results = runQueriesViaSocket(runCtx, proc.socketPath, queries, nil)
	if results != nil {
		if r, ok := results["heartbeat_metrics"]; ok && r.err == nil && len(r.rows) > 0 {
			m := mapHeartbeatRow(r.rows[0])
			applyHeartbeatCPUFallback(runCtx, m)
			return m
		}
	}

	return nil
}

// mapHeartbeatRow converte uma linha raw do osquery em AgentHeartbeatMetrics.
// Usa parseFloat64 para lidar com a conversão de string para número.
func mapHeartbeatRow(row map[string]any) *agentconn.AgentHeartbeatMetrics {
	if row == nil {
		return nil
	}
	return &agentconn.AgentHeartbeatMetrics{
		Hostname:      getString(row, "hostname"),
		CpuPercent:    parseHeartbeatFloat(row, "cpu_percent", -1),
		MemoryPercent: parseHeartbeatFloat(row, "memory_percent", -1),
		MemoryTotalGb: parseHeartbeatFloat(row, "memory_total_gb", 0),
		MemoryUsedGb:  parseHeartbeatFloat(row, "memory_used_gb", 0),
		DiskPercent:   parseHeartbeatFloat(row, "disk_percent", -1),
		DiskTotalGb:   parseHeartbeatFloat(row, "disk_total_gb", 0),
		DiskUsedGb:    parseHeartbeatFloat(row, "disk_used_gb", 0),
		UptimeSeconds: int64(parseHeartbeatFloat(row, "uptime_seconds", 0)),
		ProcessCount:  int(parseHeartbeatFloat(row, "process_count", 0)),
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

func applyHeartbeatCPUFallback(ctx context.Context, metrics *agentconn.AgentHeartbeatMetrics) {
	if metrics == nil || metrics.CpuPercent >= 0 {
		return
	}
	if cpuPercent, ok := collectWindowsCPUPercentFunc(ctx); ok {
		metrics.CpuPercent = cpuPercent
	}
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
