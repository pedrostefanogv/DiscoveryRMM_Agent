//go:build !windows

package app

// registerNotificationAppIcon é um stub não-Windows: o AppUserModelId é um
// conceito do Windows.
func registerNotificationAppIcon(appID, iconURL string) error { return nil }
