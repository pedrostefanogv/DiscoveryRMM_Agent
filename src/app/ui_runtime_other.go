//go:build !windows

package app

import "discovery/app/uiruntime"

func probeUIRuntimeNative() uiRuntimeNativeProbe {
	return uiruntime.ProbeNative()
}
