// Package native implements native (zero-subprocess) inventory collectors
// for Windows. It replaces the osquery dependency as the primary source of
// inventory data, using Win32 APIs, WMI via COM, and registry reads.
//
// The collectors are designed to be used as the primary strategy in the
// inventory Provider, with osquery kept only as an optional fallback.
package native

import (
	"context"

	"discovery/app/core/models"
)

// Collector is the interface implemented by native inventory collectors.
// Each method returns the corresponding slice of models, or an error if the
// collection could not be performed.
type Collector interface {
	// CollectSystemInfo returns hostname, OS and basic hardware identity.
	CollectSystemInfo(ctx context.Context) (models.HardwareInfo, models.OperatingSystem, error)
	// CollectDisks returns logical volumes and physical disks.
	CollectDisks(ctx context.Context) ([]models.DiskInfo, []models.DiskInfo, error)
	// CollectDiskMediaTypes returns a map of drive letter -> media type (SSD/HDD).
	CollectDiskMediaTypes(ctx context.Context) map[string]string
	// CollectNetworks returns network interfaces.
	CollectNetworks(ctx context.Context) ([]models.NetworkInfo, error)
	// CollectNetworkConnections returns listening ports and open sockets.
	CollectNetworkConnections(ctx context.Context) ([]models.ListeningPortInfo, []models.OpenSocketInfo, error)
	// CollectHardware returns motherboard, BIOS, GPU, memory and CPU details.
	CollectHardware(ctx context.Context) (models.HardwareInfo, []models.MemoryModule, []models.GPUInfo, []models.CPUInfo, []models.CPUFeature, error)
	// CollectSoftware returns installed software.
	CollectSoftware(ctx context.Context) ([]models.SoftwareItem, error)
	// CollectStartupItems returns startup items.
	CollectStartupItems(ctx context.Context) ([]models.StartupItem, error)
	// CollectLoggedInUsers returns logged-in users.
	CollectLoggedInUsers(ctx context.Context) ([]models.LoggedInUser, error)
	// CollectBitLocker returns BitLocker volume status.
	CollectBitLocker(ctx context.Context) ([]models.BitLockerInfo, error)
	// CollectBattery returns battery info.
	CollectBattery(ctx context.Context) ([]models.BatteryInfo, error)
}

// New returns a native Collector for the current platform.
// On non-Windows platforms it returns a no-op collector that reports
// ErrUnsupported, since the agent currently targets Windows only.
func New() Collector {
	return newPlatformCollector()
}
