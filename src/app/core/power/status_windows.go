//go:build windows

package power

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// systemPowerStatus espelha SYSTEM_POWER_STATUS (winbase.h).
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// Read lê o estado de energia atual via GetSystemPowerStatus.
//
// ok=false quando a chamada falhou — nesse caso NADA pode ser afirmado sobre a
// máquina e todos os ponteiros de Status são nil.
func Read() (Status, bool) {
	var ps systemPowerStatus
	ret, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&ps)))
	if ret == 0 {
		return Status{}, false
	}
	return MapRaw(RawStatus{
		ACLineStatus:        ps.ACLineStatus,
		BatteryFlag:         ps.BatteryFlag,
		BatteryLifePercent:  ps.BatteryLifePercent,
		BatteryLifeTime:     ps.BatteryLifeTime,
		BatteryFullLifeTime: ps.BatteryFullLifeTime,
	}), true
}
