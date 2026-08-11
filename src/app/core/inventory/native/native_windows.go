//go:build windows

package native

import (
	"context"

	"discovery/app/core/models"
)

// windowsCollector implements Collector using native Windows APIs.
type windowsCollector struct{}

func newPlatformCollector() Collector { return windowsCollector{} }

// CollectSystemInfo returns hostname, OS and basic hardware identity using
// native Win32 APIs (GetComputerNameExW, RtlGetVersion, GetNativeSystemInfo).
func (windowsCollector) CollectSystemInfo(ctx context.Context) (models.HardwareInfo, models.OperatingSystem, error) {
	return collectSystemInfoNative(ctx)
}

// CollectDisks returns logical volumes and physical disks using native APIs.
func (windowsCollector) CollectDisks(ctx context.Context) ([]models.DiskInfo, []models.DiskInfo, error) {
	return collectDisksNative(ctx)
}

// CollectDiskMediaTypes returns a map of drive letter -> media type.
func (windowsCollector) CollectDiskMediaTypes(ctx context.Context) map[string]string {
	return collectDiskMediaTypesWMI()
}

// CollectSmartHealth returns a map of drive letter -> SMART/health data.
func (windowsCollector) CollectSmartHealth(ctx context.Context) map[string]SmartHealth {
	return collectSmartHealth()
}

// CollectNetworks returns network interfaces using GetAdaptersAddresses.
func (windowsCollector) CollectNetworks(ctx context.Context) ([]models.NetworkInfo, error) {
	return collectNetworksNative(ctx)
}

// CollectNetworkConnections returns listening ports and open sockets using
// GetExtendedTcpTable/GetExtendedUdpTable.
func (windowsCollector) CollectNetworkConnections(ctx context.Context) ([]models.ListeningPortInfo, []models.OpenSocketInfo, error) {
	return collectNetworkConnectionsNative(ctx)
}

// CollectHardware returns motherboard, BIOS, GPU, memory and CPU details via
// WMI (COM) and native APIs.
func (windowsCollector) CollectHardware(ctx context.Context) (models.HardwareInfo, []models.MemoryModule, []models.GPUInfo, []models.CPUInfo, []models.CPUFeature, error) {
	return collectHardwareNative(ctx)
}

// CollectSoftware returns installed software from the registry.
func (windowsCollector) CollectSoftware(ctx context.Context) ([]models.SoftwareItem, error) {
	return collectSoftwareNative(ctx)
}

// CollectStartupItems returns startup items from the registry and startup folder.
func (windowsCollector) CollectStartupItems(ctx context.Context) ([]models.StartupItem, error) {
	return collectStartupItemsNative(ctx)
}

// CollectLoggedInUsers returns logged-in users via WTS API.
func (windowsCollector) CollectLoggedInUsers(ctx context.Context) ([]models.LoggedInUser, error) {
	return collectLoggedInUsersNative(ctx)
}

// CollectBitLocker returns BitLocker volume status via WMI.
func (windowsCollector) CollectBitLocker(ctx context.Context) ([]models.BitLockerInfo, error) {
	return collectBitLockerNative(ctx)
}

// CollectBattery returns battery info via GetSystemPowerStatus and WMI.
func (windowsCollector) CollectBattery(ctx context.Context) ([]models.BatteryInfo, error) {
	return collectBatteryNative(ctx)
}
