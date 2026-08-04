package app

import "discovery/app/services/hardwareid"

// HardwareIdentityInfo agrega as identidades de hardware da máquina usadas
// para identificação persistente do agente (sobrevive a formatação).
type HardwareIdentityInfo = hardwareid.Info

// GetHardwareIdentity retorna as identidades de hardware da máquina (TPM EK e
// UUID SMBIOS). É exposto ao frontend via Wails e ao debug HTTP via /api/.
func (a *App) GetHardwareIdentity() HardwareIdentityInfo {
	if a == nil || a.hardwareIDSvc == nil {
		return hardwareid.Info{}
	}
	return a.hardwareIDSvc.Get()
}

// RefreshHardwareIdentity limpa o cache e re-coleta a identidade de hardware.
// Útil quando o usuário quer forçar uma nova leitura (ex.: após habilitar o TPM
// na BIOS). Exposto ao frontend via Wails e ao debug HTTP via /api/.
func (a *App) RefreshHardwareIdentity() HardwareIdentityInfo {
	if a == nil || a.hardwareIDSvc == nil {
		return hardwareid.Info{}
	}
	return a.hardwareIDSvc.Refresh()
}
