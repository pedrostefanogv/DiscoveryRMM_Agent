//go:build !windows

package uiruntime

// ProbeNative is a no-op (watchdog system removed).
func ProbeNative() NativeProbe {
	return NativeProbe{}
}
