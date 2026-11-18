//go:build windows

package platform

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const dpapiPrefixV1 = "dpapi:v1:"

var dpapiEntropyV1 = []byte("discovery-rmm-agent-dpapi-v1")

// IsProtectedSecret indica se o valor já está encapsulado no formato DPAPI v1.
func IsProtectedSecret(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), dpapiPrefixV1)
}

// ProtectSecret cifra um segredo usando Windows DPAPI no escopo da máquina
// (CRYPTPROTECT_LOCAL_MACHINE | CRYPTPROTECT_UI_FORBIDDEN) para que tanto
// discovery-service.exe (SYSTEM) quanto ferramentas administrativas locais
// possam decifrar quando autorizadas pela ACL do arquivo.
func ProtectSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	raw := []byte(plaintext)
	inBlob := windows.DataBlob{
		Size: uint32(len(raw)),
		Data: &raw[0],
	}
	entropyBlob := windows.DataBlob{
		Size: uint32(len(dpapiEntropyV1)),
		Data: &dpapiEntropyV1[0],
	}
	var outBlob windows.DataBlob
	flags := uint32(windows.CRYPTPROTECT_LOCAL_MACHINE | windows.CRYPTPROTECT_UI_FORBIDDEN)
	if err := windows.CryptProtectData(&inBlob, nil, &entropyBlob, 0, nil, flags, &outBlob); err != nil {
		return "", fmt.Errorf("dpapi CryptProtectData falhou: %w", err)
	}
	defer func() {
		if outBlob.Data != nil {
			_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
		}
	}()
	cipherBytes := make([]byte, int(outBlob.Size))
	copy(cipherBytes, unsafe.Slice(outBlob.Data, int(outBlob.Size)))
	return dpapiPrefixV1 + base64.StdEncoding.EncodeToString(cipherBytes), nil
}

// UnprotectSecret decifra um segredo protegido por ProtectSecret.
// Falha fechado se o prefixo esperado estiver ausente ou o payload estiver corrompido.
func UnprotectSecret(protected string) (string, error) {
	trimmed := strings.TrimSpace(protected)
	if trimmed == "" {
		return "", nil
	}
	if !strings.HasPrefix(trimmed, dpapiPrefixV1) {
		return "", errors.New("segredo DPAPI invalido: prefixo dpapi:v1 ausente")
	}
	encoded := strings.TrimPrefix(trimmed, dpapiPrefixV1)
	if encoded == "" {
		return "", errors.New("segredo DPAPI invalido: payload vazio")
	}
	cipherBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("segredo DPAPI invalido (base64): %w", err)
	}
	if len(cipherBytes) == 0 {
		return "", errors.New("segredo DPAPI invalido: ciphertext vazio")
	}
	inBlob := windows.DataBlob{
		Size: uint32(len(cipherBytes)),
		Data: &cipherBytes[0],
	}
	entropyBlob := windows.DataBlob{
		Size: uint32(len(dpapiEntropyV1)),
		Data: &dpapiEntropyV1[0],
	}
	var outBlob windows.DataBlob
	if err := windows.CryptUnprotectData(&inBlob, nil, &entropyBlob, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &outBlob); err != nil {
		return "", fmt.Errorf("dpapi CryptUnprotectData falhou: %w", err)
	}
	defer func() {
		if outBlob.Data != nil {
			_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
		}
	}()
	plainBytes := make([]byte, int(outBlob.Size))
	copy(plainBytes, unsafe.Slice(outBlob.Data, int(outBlob.Size)))
	return string(plainBytes), nil
}
