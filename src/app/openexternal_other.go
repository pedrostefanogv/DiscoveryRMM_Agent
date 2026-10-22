//go:build !windows

package app

import "fmt"

func init() {
	// Fora do Windows não há FOLDERID: o resolver cai no fallback por variáveis
	// de ambiente.
	knownFolderPath = func(string) (string, bool) { return "", false }
}

// A abertura de pastas/aplicativos é específica do Windows (Explorer/App Paths).
// Nos demais alvos a tool responde com erro claro em vez de fingir sucesso.
func launchExternalTarget(path string) error {
	return fmt.Errorf("abrir pastas/aplicativos so e suportado no Windows")
}

func lookupRegisteredAppPath(name string) (string, bool) { return "", false }

// revealLocalFile: fora do Windows a tool de pastas já responde com erro
// claro; o "abrir pasta" do chat segue o mesmo princípio (sem fingir sucesso).
func revealLocalFile(path string) error {
	return fmt.Errorf("revelar arquivo na pasta só é suportado no Windows")
}
