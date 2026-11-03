//go:build !windows

package automation

// Coletores de sensores em plataformas não-Windows.
//
// O agente de produção é Windows-only: as APIs de coleta (SCM, Toolhelp,
// GetTickCount64, ICMP raw, iphlpapi, PDH, WMI) não existem fora dele. O stub
// existe para que o pacote continue compilando em `go vet`/CI em Linux, e devolve
// erro EXPLÍCITO (o servidor marca Unknown + RunError) em vez de medida
// inventada — inclusive para os 7 tipos da fase 5 (Bandwidth, DiskIo,
// EventLogRate, SmartStatus, CertificateExpiry, DefenderStatus, BitLocker).

import (
	"context"
	"fmt"
)

// collectSensor devolve erro de "não implementado" em plataformas não-Windows.
func collectSensor(_ context.Context, cfg *SensorConfig) []sensorCollection {
	sensorType := "desconhecido"
	if cfg != nil && cfg.Policy.SensorType != "" {
		sensorType = string(cfg.Policy.SensorType)
	}
	return []sensorCollection{{Err: fmt.Sprintf("coleta do sensor %s nao implementada nesta plataforma", sensorType)}}
}
