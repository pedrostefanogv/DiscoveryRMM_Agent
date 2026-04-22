//go:build !windows

package app

func probeUIRuntimeNative() uiRuntimeNativeProbe {
	return uiRuntimeNativeProbe{}
}
