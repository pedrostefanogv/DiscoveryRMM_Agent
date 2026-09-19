//go:build windows

package platform

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IntegrityLevel representa o nível de integridade do processo (IL).
// Níveis < High não podem injetar input (SendInput) em janelas de integridade
// maior por causa do UIPI (User Interface Privilege Isolation). Elevated (High)
// é necessário para controle remoto completo e gerenciamento de serviços.
type IntegrityLevel string

const (
	IntegrityUntrusted IntegrityLevel = "Untrusted"
	IntegrityLow       IntegrityLevel = "Low"
	IntegrityMedium    IntegrityLevel = "Medium"
	IntegrityHigh      IntegrityLevel = "High"
	IntegritySystem    IntegrityLevel = "System"
	IntegrityUnknown   IntegrityLevel = "Unknown"
)

// Related well-known integrity RIDs (SID SECURITY_MANDATORY_LABEL).
const (
	_ = 0x0000 // Untrusted   0x0000
	_ = 0x1000 // Low
	_ = 0x2000 // Medium
	_ = 0x3000 // High
	_ = 0x4000 // System
)

// IsRunningElevated retorna true se o processo está elevado (High IL / UAC).
// Usa TOKEN_ELEVATION — decisivo para saber se o agente consegue injetar
// input em janelas elevadas (UIPI) e acessar o SCM (serviços).
func IsRunningElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// ProcessIntegrityLevel retorna o nível de integridade do processo.
func ProcessIntegrityLevel() IntegrityLevel {
	return TokenIntegrityLevel(windows.GetCurrentProcessToken())
}

// TokenIntegrityLevel retorna o nível de integridade de um token qualquer
// (processo corrente, token duplicado do worker de remote session etc.).
//
// POR QUÊ existe: a checagem de UIPI do controle remoto precisa saber a
// integridade do TOKEN que vai injetar input — não a do processo que o criou.
// O spawn do worker pode entregar um token de usuário (Medium) mesmo quando
// o serviço é SYSTEM (fallback), e só a leitura do token em si revela isso.
func TokenIntegrityLevel(tok windows.Token) IntegrityLevel {
	label, err := getTokenIntegrityLabel(tok)
	if err != nil {
		return IntegrityUnknown
	}
	sid := label.Label.Sid
	if sid == nil {
		return IntegrityUnknown
	}
	// Para SID de integridade há apenas 1 sub-authority: o RID (nível).
	var level uint32
	if sid.SubAuthorityCount() > 0 {
		level = sid.SubAuthority(0)
	}
	switch {
	case level >= 0x4000:
		return IntegritySystem
	case level >= 0x3000:
		return IntegrityHigh
	case level >= 0x2000:
		return IntegrityMedium
	case level >= 0x1000:
		return IntegrityLow
	case level > 0:
		return IntegrityUntrusted
	default:
		return IntegrityUnknown
	}
}

// IntegritySupportsUipiInjection reporta se um processo rodando com o nível
// de integridade informado pode injetar input (SendInput) em QUALQUER janela
// da sessão, incluindo elevadas: Gerenciador de Tarefas (manifest autoElevate
// — roda SEMPRE High) e a própria UI do agente (manifest requireAdministrator
// — High). UIPI descarta o SendInput de um injetor cuja integridade é menor
// que a da janela em primeiro plano: é a causa do sintoma "o controle remoto
// morre ao abrir o Gerenciador de Tarefas e volta ao fechá-lo".
func IntegritySupportsUipiInjection(level IntegrityLevel) bool {
	return level == IntegrityHigh || level == IntegritySystem
}

// getTokenIntegrityLabel lê o TOKEN_MANDATORY_LABEL (TOKEN_INTEGRITY_LEVEL)
// de um token qualquer (o corrente ou um duplicado do worker).
func getTokenIntegrityLabel(tok windows.Token) (*windows.Tokenmandatorylabel, error) {
	token := tok
	var size uint32
	_ = windows.GetTokenInformation(token, windows.TokenIntegrityLevel, nil, 0, &size)
	if size == 0 {
		return nil, fmt.Errorf("tamanho do label de integridade desconhecido")
	}
	buf := make([]byte, size)
	var returned uint32
	if err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, &buf[0], size, &returned); err != nil {
		return nil, err
	}
	return (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buf[0])), nil
}

// ElevationReport descreve o contexto de privilégio do processo, útil para
// o diagnóstico do controle remoto (UIPI) e do gerenciamento de serviços.
func ElevationReport() string {
	return fmt.Sprintf("elevated=%t integrity=%s", IsRunningElevated(), ProcessIntegrityLevel())
}
