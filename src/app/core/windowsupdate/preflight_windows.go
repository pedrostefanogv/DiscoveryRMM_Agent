//go:build windows

package windowsupdate

import (
	"golang.org/x/sys/windows"

	"discovery/app/core/power"
)

// preflightDiskFloorBytes e o piso de espaco livre exigido antes de instalar.
//
// Nao temos (nesta camada) o tamanho exato de cada update: o piso cobre o
// maior cumulative update tipico com folga. Quando o tamanho e conhecido
// (MaxDownloadSize), o chamador pode exigir mais.
const preflightDiskFloorBytes = int64(2) << 30 // 2 GiB

// onBattery informa se a maquina esta sem energia AC. Em erro (ou leitura
// incerta) assume AC — nunca bloqueia update por um sinal incerto.
//
// A leitura em si vive em core/power para ser a MESMA usada pelo inventário,
// pelo heartbeat e pelo adiamento de deploy.
func onBattery() bool {
	return power.OnBattery()
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
