//go:build windows

package windowsupdate

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// preflightDiskFloorBytes e o piso de espaco livre exigido antes de instalar.
//
// Nao temos (nesta camada) o tamanho exato de cada update: o piso cobre o
// maior cumulative update tipico com folga. Quando o tamanho e conhecido
// (MaxDownloadSize), o chamador pode exigir mais.
const preflightDiskFloorBytes = int64(2) << 30 // 2 GiB

// systemPowerStatus espelha SYSTEM_POWER_STATUS (winbase.h).
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var (
	kernel32Power            = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemPowerStatus = kernel32Power.NewProc("GetSystemPowerStatus")
)

// onBattery informa se a maquina esta sem energia AC. Em erro assume AC
// (nao bloqueia por um sinal incerto).
func onBattery() bool {
	var status systemPowerStatus
	ret, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&status)))
	if ret == 0 {
		return false
	}
	// ACLineStatus: 0 = offline (bateria), 1 = online, 255 = desconhecido.
	return status.ACLineStatus == 0
}

// freeDiskBytes devolve o espaco livre na unidade do sistema (C:).
func freeDiskBytes() int64 {
	root, err := windows.UTF16PtrFromString(`C:\`)
	if err != nil {
		return 0
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(root, &freeToCaller, &total, &totalFree); err != nil {
		return 0
	}
	return int64(freeToCaller)
}

// gatherPreflightInput coleta os sinais para o preflight.
func gatherPreflightInput(requiredDiskBytes int64, blockOnBattery bool) PreflightInput {
	if requiredDiskBytes <= 0 {
		requiredDiskBytes = preflightDiskFloorBytes
	}
	return PreflightInput{
		ServiceState:      windowsUpdateServiceState(),
		FreeDiskBytes:     freeDiskBytes(),
		RequiredDiskBytes: requiredDiskBytes,
		OnBattery:         onBattery(),
		BlockOnBattery:    blockOnBattery,
	}
}
