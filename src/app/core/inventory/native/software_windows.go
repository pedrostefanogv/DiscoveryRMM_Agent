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
	uninstallKey64 = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	uninstallKey32 = `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`
)

// collectSoftwareNative reads installed software from the registry
// (both 64-bit and 32-bit views), without any subprocess.
func collectSoftwareNative(ctx context.Context) ([]models.SoftwareItem, error) {
	_ = ctx
	var items []models.SoftwareItem

	items = append(items, readUninstallKey(registry.LOCAL_MACHINE, uninstallKey64, windows.KEY_READ|windows.KEY_WOW64_64KEY, "registry")...)
	items = append(items, readUninstallKey(registry.LOCAL_MACHINE, uninstallKey32, windows.KEY_READ|windows.KEY_WOW64_32KEY, "registry")...)

	return items, nil
}

func readUninstallKey(root registry.Key, path string, access uint32, source string) []models.SoftwareItem {
	var items []models.SoftwareItem

	key, err := registry.OpenKey(root, path, access)
	if err != nil {
		return items
	}
	defer key.Close()

	subKeys, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return items
	}

	for _, sub := range subKeys {
		subKey, err := registry.OpenKey(key, sub, access)
		if err != nil {
			continue
		}

		displayName, _, _ := subKey.GetStringValue("DisplayName")
		if strings.TrimSpace(displayName) == "" {
			subKey.Close()
			continue
		}

		version, _, _ := subKey.GetStringValue("DisplayVersion")
		publisher, _, _ := subKey.GetStringValue("Publisher")
		installID, _, _ := subKey.GetStringValue("IdentifyingNumber")
		uninstallString, _, _ := subKey.GetStringValue("UninstallString")
		installDate, _, _ := subKey.GetStringValue("InstallDate")
		installLocation, _, _ := subKey.GetStringValue("InstallLocation")

		items = append(items, models.SoftwareItem{
			Name:          displayName,
			Version:       version,
			Publisher:     publisher,
			InstallID:     installID,
			Serial:        firstNonEmptyStr(installID, uninstallString),
			Source:        source,
			InstallDate:   installDate,
			InstallSource: firstNonEmptyStr(installLocation, uninstallString),
		})
		subKey.Close()
	}

	return items
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
