//go:build windows

package platform

// Auto-configuração do ciclo de vida do serviço Windows do Discovery Agent.
//
// O binário do serviço roda como LocalSystem, então pode ajustar a própria
// configuração no SCM. Isso é feito a cada start (best-effort) para sobreviver
// a upgrades e instalações manuais sem depender do NSIS:
//
//   - SERVICE_CONFIG_PRESHUTDOWN_INFO: define quanto o SCM espera o serviço
//     terminar no shutdown (permite segurar o encerramento e manter o acesso
//     remoto vivo durante o reinício/desligamento nativo do Windows).
//   - start= auto: reafirma o start o mais cedo possível no boot.

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// servicePreshutdownInfo espelha SERVICE_PRESHUTDOWN_INFO (winsvc.h). A x/sys
// expõe o info level e a chamada ChangeServiceConfig2, mas não a struct.
type servicePreshutdownInfo struct {
	dwPreshutdownTimeout uint32
}

// PreshutdownTimeoutFor calcula o timeout (ms) do PRESHUTDOWN a partir do grace
// em segundos, com margem de 5s para o serviço reportar STOPPED e limitado ao
// teto clássico de 180s.
func PreshutdownTimeoutFor(graceSeconds int) uint32 {
	if graceSeconds <= 0 {
		return 0
	}
	ms := graceSeconds*1000 + 5000
	if ms > 180000 {
		ms = 180000
	}
	return uint32(ms)
}

// ConfigureServiceLifecycle ajusta preshutdown + start=auto do serviço. Um
// preshutdownTimeoutMs == 0 apenas reafirma start=auto (grace desligado).
func ConfigureServiceLifecycle(name string, preshutdownTimeoutMs uint32) error {
	mgr, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return fmt.Errorf("OpenSCManager: %w", err)
	}
	defer windows.CloseServiceHandle(mgr)

	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return fmt.Errorf("nome do serviço inválido: %w", err)
	}
	h, err := windows.OpenService(mgr, namePtr, windows.SERVICE_CHANGE_CONFIG|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return fmt.Errorf("OpenService(%s): %w", name, err)
	}
	defer windows.CloseServiceHandle(h)

	if preshutdownTimeoutMs > 0 {
		info := servicePreshutdownInfo{dwPreshutdownTimeout: preshutdownTimeoutMs}
		if err := windows.ChangeServiceConfig2(h, windows.SERVICE_CONFIG_PRESHUTDOWN_INFO, (*byte)(unsafe.Pointer(&info))); err != nil {
			return fmt.Errorf("ChangeServiceConfig2(PRESHUTDOWN=%dms): %w", preshutdownTimeoutMs, err)
		}
	}

	// Reafirma start= auto sem tocar nos demais campos da configuração.
	if err := windows.ChangeServiceConfig(h, windows.SERVICE_NO_CHANGE, windows.SERVICE_AUTO_START, windows.SERVICE_NO_CHANGE, nil, nil, nil, nil, nil, nil, nil); err != nil {
		return fmt.Errorf("ChangeServiceConfig(start=auto): %w", err)
	}
	return nil
}
