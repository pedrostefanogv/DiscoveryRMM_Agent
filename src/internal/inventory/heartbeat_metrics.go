package inventory

import (
	"context"
	"fmt"
	"math"
	"time"

	"discovery/internal/agentconn"
)

// heartbeatMetricsSQL é uma única query osquery que coleta todas as métricas
// necessárias para o heartbeat (CPU, memória, disco, hostname, uptime,
// processos) em uma única chamada via socket Thrift. Isso elimina o
// overhead de múltiplas queries e minimiza o tempo de execução.
//
// A query usa subqueries que podem retornar NULL (ex: disco C: ausente)
// para evitar que a query inteira quebre com CROSS JOIN vazio.
// Os valores NULL são tratados como fallback no mapHeartbeatRow.
const heartbeatMetricsSQL = `
SELECT
  si.hostname,
  COALESCE(
    (SELECT ROUND(AVG(CAST(c.load_percentage AS REAL)), 1)
     FROM cpu_info c WHERE c.load_percentage IS NOT NULL),
    NULL
  ) AS cpu_percent,
  ROUND(mem.memory_total / 1073741824.0, 2) AS memory_total_gb,
  ROUND((mem.memory_total - mem.memory_free) / 1073741824.0, 2) AS memory_used_gb,
  CASE WHEN mem.memory_total > 0
    THEN ROUND((mem.memory_total - mem.memory_free) * CAST(100.0 AS REAL) / mem.memory_total, 1)
    ELSE NULL
  END AS memory_percent,
  ROUND(d.disk_total_gb, 2)      AS disk_total_gb,
  ROUND(d.disk_used_gb, 2)       AS disk_used_gb,
  ROUND(d.disk_percent, 1)       AS disk_percent,
  CAST(up.total_seconds AS INTEGER) AS uptime_seconds,
  CAST((SELECT COUNT(*) FROM processes) AS INTEGER) AS process_count
FROM system_info si
CROSS JOIN memory_info mem
CROSS JOIN (SELECT total_seconds FROM uptime LIMIT 1) up
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
// Retorna um AgentHeartbeatMetrics preenchido ou nil em caso de erro,
// permitindo que o caller use fallback (PowerShell WMI).
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
			return mapHeartbeatRow(r.rows[0])
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
