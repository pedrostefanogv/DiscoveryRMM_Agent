package p2p

import (
	"encoding/json"
	"fmt"
	"time"

	"discovery/app/p2pmeta"
)

const (
	TelemetryRetryBase       = 30 * time.Second
	TelemetryRetryMax        = 5 * time.Minute
	TelemetryDrainLimit      = 20
	TelemetryDedupWindow     = 5 * time.Minute
	TelemetryMaxPayloadBytes = 1 << 20
)

// TelemetryRetryBackoff calcula o backoff exponencial (limitado) para reenvio.
func TelemetryRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	backoff := TelemetryRetryBase
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= TelemetryRetryMax {
			backoff = TelemetryRetryMax
			break
		}
	}
	return backoff
}

// MarshalTelemetryPayload serializa o payload, rejeitando acima do limite.
func MarshalTelemetryPayload(payload p2pmeta.TelemetryPayload) ([]byte, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(payloadJSON) > TelemetryMaxPayloadBytes {
		return nil, fmt.Errorf("payload de telemetria excede limite de %d bytes", TelemetryMaxPayloadBytes)
	}
	return payloadJSON, nil
}
