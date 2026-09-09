//go:build !windows

package app

import "discovery/app/services/notifications"

// dispatchNativeToastWhenHeadless é um stub não-Windows (toasts do Windows não
// se aplicam). O caminho headless padrão do notificationSvc permanece.
func dispatchNativeToastWhenHeadless(req notifications.DispatchRequest) {}

// setToastActivationHook é um stub não-Windows.
func setToastActivationHook(fn func(notificationID, result string)) {}
