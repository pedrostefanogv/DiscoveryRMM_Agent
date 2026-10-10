//go:build !windows

package app

import "fmt"

// readUninstallerFromRegistry é o stub não-Windows: o descomissionamento
// remoto depende do uninstaller NSIS e do registro do Windows.
func readUninstallerFromRegistry() string { return "" }

func launchAgentUninstallerDetached(uninstallerPath string) (uint32, error) {
	return 0, fmt.Errorf("descomissionamento remoto indisponível fora do Windows")
}
