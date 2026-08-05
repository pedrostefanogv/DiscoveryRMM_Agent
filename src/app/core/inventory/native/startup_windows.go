//go:build windows

package native

import (
	"context"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"discovery/app/core/models"
)

const (
	runKeyCurrentUser      = `Software\Microsoft\Windows\CurrentVersion\Run`
	runKeyLocalMachine     = `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`
	runOnceKeyLocalMachine = `SOFTWARE\Microsoft\Windows\CurrentVersion\RunOnce`
)

// collectStartupItemsNative reads startup items from the registry Run/RunOnce
// keys (HKLM and HKCU) and the startup folder.
func collectStartupItemsNative(ctx context.Context) ([]models.StartupItem, error) {
	var items []models.StartupItem

	// HKLM Run
	items = append(items, readRunKey(registry.LOCAL_MACHINE, runKeyLocalMachine, "HKLM Run")...)
	// HKLM RunOnce
	items = append(items, readRunKey(registry.LOCAL_MACHINE, runOnceKeyLocalMachine, "HKLM RunOnce")...)
	// HKCU Run
	items = append(items, readRunKey(registry.CURRENT_USER, runKeyCurrentUser, "HKCU Run")...)

	return items, nil
}

func readRunKey(root registry.Key, path, source string) []models.StartupItem {
	var items []models.StartupItem

	key, err := registry.OpenKey(root, path, windows.KEY_READ)
	if err != nil {
		return items
	}
	defer key.Close()

	// Enumerate value names.
	names, err := key.ReadValueNames(-1)
	if err != nil {
		return items
	}

	for _, name := range names {
		val, _, err := key.GetStringValue(name)
		if err != nil {
			continue
		}
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		items = append(items, models.StartupItem{
			Name:   name,
			Path:   val,
			Type:   "registry",
			Source: source,
			Status: "enabled",
		})
	}

	return items
}

// collectLoggedInUsersNative returns logged-in users via the WTS API.
func collectLoggedInUsersNative(ctx context.Context) ([]models.LoggedInUser, error) {
	return collectLoggedInUsersWTS()
}
