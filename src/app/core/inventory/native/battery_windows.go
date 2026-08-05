//go:build windows

package native

import (
	"context"
	"unsafe"

	"discovery/app/core/models"
)

// systemPowerStatus mirrors the SYSTEM_POWER_STATUS structure.
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var procGetSystemPowerStatus = modkernel32.NewProc("GetSystemPowerStatus")

// collectBatteryNative returns battery info via GetSystemPowerStatus and WMI
// (Win32_Battery).
func collectBatteryNative(ctx context.Context) ([]models.BatteryInfo, error) {
	var items []models.BatteryInfo

	// WMI Win32_Battery provides detailed battery info.
	rows, err := wmiQuery(wmiNamespace, "SELECT Name, Manufacturer, DeviceID, EstimatedChargeRemaining, BatteryStatus, DesignCapacity, FullChargeCapacity, Chemistry, SerialNumber FROM Win32_Battery")
	if err == nil && len(rows) > 0 {
		for _, row := range rows {
			items = append(items, models.BatteryInfo{
				Manufacturer:        wmiString(row, "Manufacturer"),
				Model:               wmiString(row, "Name"),
				SerialNumber:        wmiString(row, "SerialNumber"),
				PercentRemaining:    wmiInt(row, "EstimatedChargeRemaining"),
				DesignedCapacityMAh: wmiInt(row, "DesignCapacity"),
				MaxCapacityMAh:      wmiInt(row, "FullChargeCapacity"),
				Chemistry:           wmiString(row, "Chemistry"),
				State:               batteryStatusString(wmiInt(row, "BatteryStatus")),
			})
		}
	}

	// If WMI returned nothing, fall back to GetSystemPowerStatus.
	if len(items) == 0 {
		var sps systemPowerStatus
		r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&sps)))
		if r != 0 && sps.BatteryLifePercent <= 100 {
			items = append(items, models.BatteryInfo{
				PercentRemaining: int(sps.BatteryLifePercent),
				Charging:         sps.ACLineStatus == 0 && sps.BatteryFlag == 8,
				Charged:          sps.BatteryFlag == 128,
			})
		}
	}

	return items, nil
}

func batteryStatusString(status int) string {
	switch status {
	case 1:
		return "Discharging"
	case 2:
		return "On AC"
	case 3:
		return "Fully Charged"
	case 4:
		return "Low"
	case 5:
		return "Critical"
	case 6:
		return "Charging"
	case 7:
		return "Charging and High"
	case 8:
		return "Charging and Low"
	case 9:
		return "Charging and Critical"
	case 10:
		return "Undefined"
	case 11:
		return "Partially Charged"
	default:
		return ""
	}
}
