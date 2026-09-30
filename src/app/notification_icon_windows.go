//go:build windows

package app

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// registerNotificationAppIcon corrige os metadados do AppUserModelId usado pelo
// toast nativo (ícone da aplicação).
//
// Por que isso é necessário: o wintoast grava "IconUri" com o caminho cru do
// arquivo (ex.: C:Users...TempDiscovery{guid}.png) e o Windows espera um
// URI (file:///...). Além disso o helper de escrita do wintoast ignora valores
// já existentes — um IconUri antigo (temp já apagado) nunca é corrigido. Aqui
// gravamos o URI válido e o DisplayName, preservando CustomActivator (usado pela
// ativação do toast).
func registerNotificationAppIcon(appID, iconURL string) error {
	appID = strings.TrimSpace(appID)
	iconURL = strings.TrimSpace(iconURL)
	if appID == "" || iconURL == "" {
		return nil
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `SOFTWAREClassesAppUserModelId`+appID, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	if err := key.SetStringValue("IconUri", iconURL); err != nil {
		return err
	}
	return key.SetStringValue("DisplayName", "Discovery")
}
