package agentconfig

import "strings"

// Offline queue mode constants.
const (
	OfflineQueueModeLoggingOnly     = "logging_only"
	OfflineQueueModeEnqueueOnly     = "enqueue_only"
	OfflineQueueModeEnqueueAndDrain = "enqueue_and_drain"
)

// Consolidation window mode constants.
const (
	ConsolidationModeRealtime = "realtime"
	ConsolidationMode1Min     = "1min"
	ConsolidationMode5Min     = "5min"
)

// NormalizeOfflineQueueMode normaliza um modo de fila offline.
func NormalizeOfflineQueueMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "", "enqueue_and_drain", "active", "enabled", "full":
		return OfflineQueueModeEnqueueAndDrain
	case "logging", "logging_only", "disabled":
		return OfflineQueueModeLoggingOnly
	case "enqueue", "enqueue_only", "buffer_only":
		return OfflineQueueModeEnqueueOnly
	default:
		return OfflineQueueModeEnqueueAndDrain
	}
}

// NormalizeConsolidationWindowMode normaliza um modo de janela de consolidação.
func NormalizeConsolidationWindowMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "1min", "batch_1min", "batch-1min":
		return ConsolidationMode1Min
	case "5min", "batch_5min", "batch-5min":
		return ConsolidationMode5Min
	case "", "realtime", "real_time", "real-time":
		return ConsolidationModeRealtime
	default:
		return ConsolidationModeRealtime
	}
}
