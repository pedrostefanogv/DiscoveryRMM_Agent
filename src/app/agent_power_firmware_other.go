//go:build !windows

package app

// probeFirmwareBootSupport: o reinício direto no firmware é um recurso do
// Windows (shutdown.exe /fw). Fora do Windows o verbo é sempre indisponível.
func probeFirmwareBootSupport() (bool, string) {
	return false, "reinício no firmware suportado apenas no Windows"
}
