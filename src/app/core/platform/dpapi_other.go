//go:build !windows

package platform

import (
	"errors"
	"strings"
)

const dpapiPrefixV1 = "dpapi:v1:"

// IsProtectedSecret indica se o valor está encapsulado no formato DPAPI v1.
func IsProtectedSecret(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), dpapiPrefixV1)
}

// ProtectSecret retorna erro fora do Windows (DPAPI é específico do Windows).
func ProtectSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return "", errors.New("DPAPI disponivel apenas no Windows")
}

// UnprotectSecret retorna erro fora do Windows (DPAPI é específico do Windows).
func UnprotectSecret(protected string) (string, error) {
	if strings.TrimSpace(protected) == "" {
		return "", nil
	}
	return "", errors.New("DPAPI disponivel apenas no Windows")
}
