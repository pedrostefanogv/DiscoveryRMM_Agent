//go:build windows

package app

import "discovery/app/uiruntime"

// probeUIRuntimeNative is a no-op (watchdog system removed).
func probeUIRuntimeNative() uiRuntimeNativeProbe {
	return uiruntime.ProbeNative()
}
