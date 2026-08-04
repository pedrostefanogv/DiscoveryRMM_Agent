package hardwareid

import (
	"sync"
)

// Info agrega as identidades de hardware da máquina usadas
// para identificação persistente do agente (sobrevive a formatação).
//
// TPMEK é a chave Endorsement Key (EK) do TPM 2.0 — imutável e única por chip.
// SMBIOSUUID é o UUID do SMBIOS (System Information) — persistente na placa-mãe.
// Ambos são apresentados quando disponíveis; o agente pode usar um como
// Hardware ID e o outro como fallback.
type Info struct {
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

// Deps são as dependências injetadas no Service.
type Deps struct {
	// Logf appends a log line.
	Logf func(string)
}

// Service encapsula a coleta e cache de identidade de hardware.
type Service struct {
	logf func(string)

	mu    sync.RWMutex
	cache Info
	done  bool
}

// New cria um HardwareIDService.
func New(deps Deps) *Service {
	logf := deps.Logf
	if logf == nil {
		logf = func(string) {}
	}
	return &Service{logf: logf}
}

// Get retorna as identidades de hardware da máquina (TPM EK e UUID SMBIOS).
// O resultado é cacheado: o hardware não muda em runtime, e a coleta
// (TPM + WMI) é relativamente lenta. A primeira chamada coleta e cacheia; as
// chamadas seguintes retornam do cache imediatamente.
func (s *Service) Get() Info {
	s.mu.RLock()
	if s.done {
		info := s.cache
		s.mu.RUnlock()
		return info
	}
	s.mu.RUnlock()

	// Coleta fora do lock (pode demorar) e cacheia.
	info := collect()

	s.mu.Lock()
	if !s.done {
		s.cache = info
		s.done = true
	}
	s.mu.Unlock()

	s.logf("[hardware-id] TPM EK disponível=" + boolToStr(info.TPMEKAvailable) +
		" SMBIOS UUID disponível=" + boolToStr(info.SMBIOSUUIDAvailable))
	return info
}

// Refresh limpa o cache e re-coleta a identidade de hardware.
// Útil quando o usuário quer forçar uma nova leitura (ex.: após habilitar o TPM
// na BIOS).
func (s *Service) Refresh() Info {
	s.mu.Lock()
	s.done = false
	s.cache = Info{}
	s.mu.Unlock()
	return s.Get()
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
