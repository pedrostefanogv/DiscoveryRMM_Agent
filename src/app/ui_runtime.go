package app

import (
	"strings"

	"discovery/internal/watchdog"
)

const (
	uiRuntimeAwaitingBootstrapReason = "aguardando bootstrap da UI"
	uiRuntimeHiddenReason            = "janela oculta"
)

type uiRuntimeNativeProbe struct {
	Supported   bool
	WindowFound bool
	Visible     bool
	Hung        bool
	Title       string
}

// SetUIRuntimeSuspended tells the watchdog that the desktop UI is intentionally
// not expected to emit heartbeats, for example while hidden in the tray.
func (a *App) SetUIRuntimeSuspended(suspended bool, reason string) {
	if a == nil || a.watchdogSvc == nil {
		return
	}
	if suspended {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			reason = uiRuntimeHiddenReason
		}
		a.watchdogSvc.Suspend(watchdog.ComponentUI, reason)
		return
	}
	a.watchdogSvc.Resume(watchdog.ComponentUI)
}

// ReportUIRuntimeState records a UI liveness pulse coming from the frontend.
// On Windows, a cheap native probe cross-checks the main window before the
// heartbeat is accepted.
func (a *App) ReportUIRuntimeState(visible, focused bool, source string) {
	if a == nil || a.watchdogSvc == nil {
		return
	}

	source = strings.TrimSpace(source)
	if !visible {
		reason := uiRuntimeHiddenReason
		if source != "" {
			reason += ": " + source
		}
		a.SetUIRuntimeSuspended(true, reason)
		return
	}

	// A visible window should be monitored again even if it was previously
	// hidden in the tray. A subsequent heartbeat confirms full liveness.
	a.SetUIRuntimeSuspended(false, "")

	probe := probeUIRuntimeNative()
	if probe.Supported {
		if !probe.WindowFound || probe.Hung {
			return
		}
	}

	_ = focused
	a.watchdogSvc.Heartbeat(watchdog.ComponentUI)
}
