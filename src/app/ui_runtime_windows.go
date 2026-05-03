//go:build windows

package app

// probeUIRuntimeNative is a no-op (watchdog system removed).
func probeUIRuntimeNative() uiRuntimeNativeProbe {
	return uiRuntimeNativeProbe{}
}
