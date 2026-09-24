//go:build windows

package winget

import (
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// fromAppPathsRegistry lê o caminho real do winget da chave "App Paths" do
// registro — gravada pelo instalador do DesktopAppInstaller:
//
//	HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\winget.exe
//	HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\winget.exe
//
// A chave por MÁQUINA é a mais valiosa: existe independentemente de qual
// usuário instalou o pacote e é legível por qualquer conta (inclusive SYSTEM),
// ao contrário do alias em %LOCALAPPDATA%\Microsoft\WindowsApps.
func fromAppPathsRegistry() string {
	const subKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\winget.exe`

	// HKLM (máquina) primeiro — funciona para qualquer conta.
	if p := readAppPath(registry.LOCAL_MACHINE, subKey,
		windows.KEY_READ|windows.KEY_WOW64_64KEY); p != "" {
		return p
	}
	if p := readAppPath(registry.LOCAL_MACHINE, subKey,
		windows.KEY_READ|windows.KEY_WOW64_32KEY); p != "" {
		return p
	}

	// HKCU cobre a instalação por usuário (o instalador grava na conta que
	// executou o winget).
	if p := readAppPath(registry.CURRENT_USER, subKey, windows.KEY_READ); p != "" {
		return p
	}
	return ""
}

// readAppPath abre a chave e devolve o valor padrão ("(Default)") validado.
func readAppPath(root registry.Key, subKey string, access uint32) string {
	key, err := registry.OpenKey(root, subKey, access)
	if err != nil {
		return ""
	}
	defer key.Close()

	value, _, err := key.GetStringValue("")
	if err != nil {
		return ""
	}

	value = strings.TrimSpace(value)
	if !usable(value) {
		return ""
	}
	return value
}
