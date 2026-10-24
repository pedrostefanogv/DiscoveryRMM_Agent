//go:build !windows

package app

import "discovery/app/core/automation"

func (a *App) nativeAutomationWelcome(req automation.AutomationNotificationRequest, timeoutSeconds int, appName string, closeProcs []string, allowDefer bool, message string) (automation.AutomationNotificationResponse, bool) {
	return automation.AutomationNotificationResponse{}, false
}
