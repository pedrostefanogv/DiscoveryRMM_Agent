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
	IntegrityLow      IntegrityLevel = "Low"
	IntegrityMedium   IntegrityLevel = "Medium"
	IntegrityHigh     IntegrityLevel = "High"
	IntegritySystem   IntegrityLevel = "System"
	IntegrityUnknown  IntegrityLevel = "Unknown"
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
	// Lê o token de integridade do processo.
	label, err := getProcessIntegrityLabel()
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

// getProcessIntegrityLabel lê o TOKEN_MANDATORY_LABEL (TOKEN_INTEGRITY_LEVEL)
// do token do processo corrente.
func getProcessIntegrityLabel() (*windows.Tokenmandatorylabel, error) {
	token := windows.GetCurrentProcessToken()
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