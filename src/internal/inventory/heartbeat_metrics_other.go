//go:build !windows

package inventory

import "discovery/internal/agentconn"

func collectWindowsMemoryNative() (float64, float64, float64, bool) {
	return -1, -1, -1, false
}

func collectWindowsCPUPercentNative() (float64, bool) {
	return -1, false
}

func collectDiskSpaceNative() (float64, float64, float64, bool) {
	return -1, -1, -1, false
}

func collectUptimeSeconds() int64 {
	return -1
}

func collectProcessCountNative() int {
	return -1
}

func collectHeartbeatDiskIOWindowsNative(_ *agentconn.AgentHeartbeatMetrics) {}
