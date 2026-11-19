//go:build windows

package platform

import (
	"encoding/binary"
	"fmt"
	"runtime"
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
	level, ok := getTokenIntegrityRID(tok)
	if !ok {
		return IntegrityUnknown
	}
	return integrityLevelFromRID(level)
}

func integrityLevelFromRID(level uint32) IntegrityLevel {
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

// getTokenIntegrityRID lê o TOKEN_MANDATORY_LABEL (TOKEN_INTEGRITY_LEVEL)
// e decodifica o RID da SID diretamente dos bytes do buffer devolvido pelo
// kernel, evitando getSidSubAuthorityCount/getSidSubAuthority em x/sys/windows
// (que reconvertem uintptr de volta para unsafe.Pointer e disparam checkptr
// sob `go test -race`).
func getTokenIntegrityRID(tok windows.Token) (uint32, bool) {
	var size uint32
	_ = windows.GetTokenInformation(tok, windows.TokenIntegrityLevel, nil, 0, &size)
	minHeader := uint32(unsafe.Sizeof(windows.Tokenmandatorylabel{}))
	if size < minHeader+8 {
		return 0, false
	}
	words := make([]uint64, (size+7)/8)
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), len(words)*8)
	var returned uint32
	if err := windows.GetTokenInformation(tok, windows.TokenIntegrityLevel, &raw[0], size, &returned); err != nil {
		return 0, false
	}
	if returned < minHeader || int(returned) > len(raw) {
		return 0, false
	}
	label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&words[0]))
	if label.Label.Sid == nil {
		return 0, false
	}
	baseAddr := uintptr(unsafe.Pointer(&raw[0]))
	sidAddr := uintptr(unsafe.Pointer(label.Label.Sid))
	if sidAddr < baseAddr || sidAddr >= baseAddr+uintptr(returned) {
		return 0, false
	}
	sidBytes := make([]byte, int(uintptr(returned)-(sidAddr-baseAddr)))
	copy(sidBytes, raw[int(sidAddr-baseAddr):int(returned)])
	runtime.KeepAlive(words)
	return parseIntegrityRIDFromSIDBytes(sidBytes)
}

func parseIntegrityRIDFromSIDBytes(sidBytes []byte) (uint32, bool) {
	if len(sidBytes) < 8 || sidBytes[0] != 1 {
		return 0, false
	}
	subCount := int(sidBytes[1])
	if subCount < 1 || len(sidBytes) < 8+subCount*4 {
		return 0, false
	}
	offset := 8 + (subCount-1)*4
	return binary.LittleEndian.Uint32(sidBytes[offset : offset+4]), true
}

// ElevationReport descreve o contexto de privilégio do processo, útil para
// o diagnóstico do controle remoto (UIPI) e do gerenciamento de serviços.
func ElevationReport() string {
	return fmt.Sprintf("elevated=%t integrity=%s", IsRunningElevated(), ProcessIntegrityLevel())
}
