package app

// uiRuntimeNativeProbe retained for compilation of platform stubs.
type uiRuntimeNativeProbe struct {
	Supported   bool
	WindowFound bool
	Visible     bool
	Hung        bool
	Title       string
}

// SetUIRuntimeSuspended is a no-op (watchdog system removed).
func (a *App) SetUIRuntimeSuspended(suspended bool, reason string) {
}

// ReportUIRuntimeState is a no-op (watchdog system removed).
func (a *App) ReportUIRuntimeState(visible, focused bool, source string) {
}
