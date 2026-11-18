//go:build !windows

package platform

import "errors"

// VerifyAuthenticode retorna erro em plataformas não-Windows.
func VerifyAuthenticode(path string) error {
	return errors.New("verificacao Authenticode disponivel apenas no Windows")
}
