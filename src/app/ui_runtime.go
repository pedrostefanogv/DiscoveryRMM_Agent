package app

import (
	"discovery/app/uiruntime"
)

// uiRuntimeNativeProbe retained for compilation of platform stubs.
type uiRuntimeNativeProbe = uiruntime.NativeProbe

// SetUIRuntimeSuspended is a no-op (watchdog system removed).
func (a *App) SetUIRuntimeSuspended(suspended bool, reason string) {
}

// ReportUIRuntimeState is a no-op (watchdog system removed).
func (a *App) ReportUIRuntimeState(visible, focused bool, source string) {
}
