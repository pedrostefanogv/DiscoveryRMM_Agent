//go:build !windows

package native

import (
	"context"
	"errors"

	"discovery/app/core/models"
)

// ErrUnsupported is returned by collectors on platforms without native support.
var ErrUnsupported = errors.New("native inventory collector not supported on this platform")

// unsupportedCollector is a no-op collector for non-Windows platforms.
type unsupportedCollector struct{}

func newPlatformCollector() Collector { return unsupportedCollector{} }

func (unsupportedCollector) CollectSystemInfo(context.Context) (models.HardwareInfo, models.OperatingSystem, error) {
	return models.HardwareInfo{}, models.OperatingSystem{}, ErrUnsupported
}
func (unsupportedCollector) CollectDisks(context.Context) ([]models.DiskInfo, []models.DiskInfo, error) {
	return nil, nil, ErrUnsupported
}
func (unsupportedCollector) CollectDiskMediaTypes(context.Context) map[string]string {
	return nil
}
func (unsupportedCollector) CollectSmartHealth(context.Context) map[string]SmartHealth {
	return nil
}
func (unsupportedCollector) CollectNetworks(context.Context) ([]models.NetworkInfo, error) {
	return nil, ErrUnsupported
}
func (unsupportedCollector) CollectNetworkConnections(context.Context) ([]models.ListeningPortInfo, []models.OpenSocketInfo, error) {
	return nil, nil, ErrUnsupported
}
func (unsupportedCollector) CollectHardware(context.Context) (models.HardwareInfo, []models.MemoryModule, []models.GPUInfo, []models.CPUInfo, []models.CPUFeature, error) {
	return models.HardwareInfo{}, nil, nil, nil, nil, ErrUnsupported
}
func (unsupportedCollector) CollectSoftware(context.Context) ([]models.SoftwareItem, error) {
	return nil, ErrUnsupported
}
func (unsupportedCollector) CollectStartupItems(context.Context) ([]models.StartupItem, error) {
	return nil, ErrUnsupported
}
func (unsupportedCollector) CollectLoggedInUsers(context.Context) ([]models.LoggedInUser, error) {
	return nil, ErrUnsupported
}
func (unsupportedCollector) CollectBitLocker(context.Context) ([]models.BitLockerInfo, error) {
	return nil, ErrUnsupported
}
func (unsupportedCollector) CollectBattery(context.Context) ([]models.BatteryInfo, error) {
	return nil, ErrUnsupported
}
