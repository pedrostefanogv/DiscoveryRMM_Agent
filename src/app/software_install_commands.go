package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"discovery/app/agentcommands"
)

// IsSoftwareInstallCommandType verifica se o cmdType é de instalação de
// software do catálogo (comando remoto do dashboard).
func IsSoftwareInstallCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "softwareinstall", "software_install", "software-install":
		return true
	default:
		return false
	}
}

// handleSoftwareInstallCommand processa o comando remoto de instalação de um
// app do catálogo. Payload esperado:
//
//	{"packageId":"...","installationType":"winget|chocolatey|custom","source":"..."}
//
// A aprovação/confirmação é responsabilidade do servidor; aqui executamos o
// fluxo nativo de pacotes (router com P2P + switches silenciosos do catálogo)
// via InventorySvc.InstallFromSource.
func (a *App) handleSoftwareInstallCommand(_ context.Context, payload any) (bool, int, string, string) {
	payloadJSON, err := agentcommands.NormalizePayloadJSON(payload)
	if err != nil {
		return true, 2, "", "payload softwareinstall inválido: " + err.Error()
	}

	packageID := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "packageId"))
	if packageID == "" {
		return true, 2, "", "campo 'packageId' é obrigatório"
	}

	installationType := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "installationType"))
	if installationType == "" {
		installationType = strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "source"))
	}

	if a == nil || a.InventorySvc == nil {
		return true, 1, "", "serviço de inventário indisponível"
	}

	a.Logs.Append(fmt.Sprintf("[agent] softwareinstall: type=%s packageId=%s", installationType, packageID))

	out, installErr := a.InventorySvc.InstallFromSource(installationType, packageID)
	if installErr != nil {
		a.Logs.Append("[agent] softwareinstall falhou: " + installErr.Error())
		return true, 1, "", installErr.Error()
	}

	if trimmed := strings.TrimSpace(out); trimmed != "" {
		a.Logs.Append("[agent] softwareinstall: " + trimmed)
	}

	result := map[string]any{
		"success":          true,
		"packageId":        packageID,
		"installationType": installationType,
	}
	body, _ := json.Marshal(result)
	return true, 0, string(body), ""
}
