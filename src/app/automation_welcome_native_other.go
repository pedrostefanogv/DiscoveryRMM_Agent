//go:build !windows

package app

import (
	"discovery/app/core/automation"
	"discovery/app/core/notifyui"
)

func (a *App) nativeAutomationWelcome(req automation.AutomationNotificationRequest, timeoutSeconds int, appName string, closeProcs []string, runningProcs []notifyui.ProcessItem, allowDefer bool, message string) (automation.AutomationNotificationResponse, bool) {
	return automation.AutomationNotificationResponse{}, false
}
