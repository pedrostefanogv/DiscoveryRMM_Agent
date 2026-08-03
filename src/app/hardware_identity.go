package app

// HardwareIdentityInfo agrega as identidades de hardware da máquina usadas
// para identificação persistente do agente (sobrevive a formatação).
//
// TPMEK é a chave Endorsement Key (EK) do TPM 2.0 — imutável e única por chip.
// SMBIOSUUID é o UUID do SMBIOS (System Information) — persistente na placa-mãe.
// Ambos são apresentados quando disponíveis; o agente pode usar um como
// Hardware ID e o outro como fallback.
type HardwareIdentityInfo struct {
	// TPMEK é o hash SHA-256 da chave pública da Endorsement Key (EK) do TPM 2.0.
	// Vazio quando não há TPM disponível/acessível.
	TPMEK string `json:"tpmEk"`
	// TPMEKAvailable indica se a EK foi lida com sucesso do TPM.
	TPMEKAvailable bool `json:"tpmEkAvailable"`
	// TPMEKError descreve a falha ao ler a EK (se houver).
	TPMEKError string `json:"tpmEkError,omitempty"`
	// TPMEKAlg indica o algoritmo da EK (ex.: "RSA", "ECC").
	TPMEKAlg string `json:"tpmEkAlg,omitempty"`

	// SMBIOSUUID é o UUID do SMBIOS da máquina (Win32_ComputerSystemProduct.UUID).
	// Vazio quando não foi possível obter.
	SMBIOSUUID string `json:"smbiosUuid"`
	// SMBIOSUUIDAvailable indica se o UUID foi obtido com sucesso.
	SMBIOSUUIDAvailable bool `json:"smbiosUuidAvailable"`
	// SMBIOSUUIDError descreve a falha ao obter o UUID (se houver).
	SMBIOSUUIDError string `json:"smbiosUuidError,omitempty"`
}

// GetHardwareIdentity retorna as identidades de hardware da máquina (TPM EK e
// UUID SMBIOS). É exposto ao frontend via Wails e ao debug HTTP via /api/.
//
// O resultado é cacheado no App: o hardware não muda em runtime, e a coleta
// (TPM + WMI) é relativamente lenta. A primeira chamada coleta e cacheia; as
// chamadas seguintes retornam do cache imediatamente.
func (a *App) GetHardwareIdentity() HardwareIdentityInfo {
	if a == nil {
		return collectHardwareIdentity()
	}

	a.hardwareIdentityMu.RLock()
	if a.hardwareIdentityDone {
		info := a.hardwareIdentityCache
		a.hardwareIdentityMu.RUnlock()
		return info
	}
	a.hardwareIdentityMu.RUnlock()

	// Coleta fora do lock (pode demorar) e cacheia.
	info := collectHardwareIdentity()

	a.hardwareIdentityMu.Lock()
	if !a.hardwareIdentityDone {
		a.hardwareIdentityCache = info
		a.hardwareIdentityDone = true
	}
	a.hardwareIdentityMu.Unlock()

	a.logs.append("[hardware-id] TPM EK disponível=" + boolToStr(info.TPMEKAvailable) +
		" SMBIOS UUID disponível=" + boolToStr(info.SMBIOSUUIDAvailable))
	return info
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// RefreshHardwareIdentity limpa o cache e re-coleta a identidade de hardware.
// Útil quando o usuário quer forçar uma nova leitura (ex.: após habilitar o TPM
// na BIOS). Exposto ao frontend via Wails e ao debug HTTP via /api/.
func (a *App) RefreshHardwareIdentity() HardwareIdentityInfo {
	if a != nil {
		a.hardwareIdentityMu.Lock()
		a.hardwareIdentityDone = false
		a.hardwareIdentityCache = HardwareIdentityInfo{}
		a.hardwareIdentityMu.Unlock()
	}
	return a.GetHardwareIdentity()
}
