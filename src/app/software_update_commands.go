package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"discovery/app/agentcommands"
)

// IsSoftwareUpdateCommandType verifica se o cmdType é de atualização de
// software instalado (comando remoto do detalhe do agente no dashboard).
func IsSoftwareUpdateCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "softwareupdate", "software_update", "software-update":
		return true
	default:
		return false
	}
}

// handleSoftwareUpdateCommand processa o comando remoto de atualização de um
// app instalado. Payload esperado:
//
//	{"packageId":"...","installationType":"winget|chocolatey","source":"..."}
//
// A aprovação/confirmação é responsabilidade do servidor; aqui executamos o
// fluxo nativo de pacotes (router com P2P + switches silenciosos do catálogo)
// via InventorySvc.UpgradeFromSource.
func (a *App) handleSoftwareUpdateCommand(_ context.Context, payload any) (bool, int, string, string) {
	payloadJSON, err := agentcommands.NormalizePayloadJSON(payload)
	if err != nil {
		return true, 2, "", "payload softwareupdate inválido: " + err.Error()
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

	a.Logs.Append(fmt.Sprintf("[agent] softwareupdate: type=%s packageId=%s", installationType, packageID))

	out, updateErr := a.InventorySvc.UpgradeFromSource(installationType, packageID)
	if updateErr != nil {
		a.Logs.Append("[agent] softwareupdate falhou: " + updateErr.Error())
		return true, 1, out, updateErr.Error()
	}

	result := map[string]any{
		"success":          true,
		"packageId":        packageID,
		"installationType": installationType,
	}
	body, _ := json.Marshal(result)
	return true, 0, string(body), ""
}
