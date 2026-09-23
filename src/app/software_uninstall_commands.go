package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"discovery/app/agentcommands"
	"discovery/app/core/processutil"
)

// IsSoftwareUninstallCommandType verifica se o cmdType é de desinstalação de
// software instalado (comando remoto do detalhe do agente no dashboard).
func IsSoftwareUninstallCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "softwareuninstall", "software_uninstall", "software-uninstall":
		return true
	default:
		return false
	}
}

// handleSoftwareUninstallCommand processa o comando remoto de desinstalação.
// Payload esperado:
//
//	{"name":"...","packageId":"...","installationType":"winget|chocolatey",
//	 "installId":"...","serial":"...","installSource":"..."}
//
// Estratégias, em ordem:
//  1. Gerenciador de pacotes (winget/choco) com o Id reconhecido;
//  2. MSI pelo ProductCode (installId) → msiexec /x;
//  3. UninstallString do registro (installSource/serial) → cmd /C.
func (a *App) handleSoftwareUninstallCommand(ctx context.Context, payload any) (bool, int, string, string) {
	payloadJSON, err := agentcommands.NormalizePayloadJSON(payload)
	if err != nil {
		return true, 2, "", "payload softwareuninstall inválido: " + err.Error()
	}

	name := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "name"))
	packageID := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "packageId"))
	installationType := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "installationType"))
	installID := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "installId"))
	serial := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "serial"))
	installSource := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "installSource"))

	if a == nil || a.InventorySvc == nil {
		return true, 1, "", "serviço de inventário indisponível"
	}

	a.Logs.Append(fmt.Sprintf("[agent] softwareuninstall: packageId=%q name=%q", packageID, name))

	// 1) Gerenciador de pacotes (winget/chocolatey).
	if packageID != "" {
		out, uninstallErr := a.InventorySvc.UninstallFromSource(installationType, packageID)
		if uninstallErr == nil {
			return softwareUninstallResult(installationType, out)
		}
		a.Logs.Append("[agent] softwareuninstall: gerenciador falhou: " + uninstallErr.Error())
	}

	// 2) MSI pelo ProductCode.
	if isMsiProductCode(installID) {
		out, msiErr := runUninstallCommand(ctx, fmt.Sprintf("msiexec /x %s /qn /norestart", installID))
		if msiErr == nil {
			return softwareUninstallResult("msi", out)
		}
		a.Logs.Append("[agent] softwareuninstall: msiexec falhou: " + msiErr.Error())
	}

	// 3) UninstallString do registro. "serial" costuma carregar o UninstallString
	// quando não há ProductCode (aceita .exe); "installSource" costuma ser o
	// InstallLocation e só é executado se indicar um desinstalador.
	for _, candidate := range []struct {
		value  string
		strict bool
	}{
		{serial, false},
		{installSource, true},
	} {
		if strings.TrimSpace(candidate.value) == "" {
			continue
		}
		if candidate.strict {
			if !looksLikeUninstallTarget(candidate.value) {
				continue
			}
		} else if !looksLikeUninstallCommand(candidate.value) {
			continue
		}
		out, runErr := runUninstallCommand(ctx, strings.TrimSpace(candidate.value))
		if runErr == nil {
			return softwareUninstallResult("registry", out)
		}
		a.Logs.Append("[agent] softwareuninstall: UninstallString falhou: " + runErr.Error())
	}

	return true, 1, "", "não foi possível identificar o comando de desinstalação para este aplicativo"
}

func softwareUninstallResult(method, output string) (bool, int, string, string) {
	body, _ := json.Marshal(map[string]any{
		"success": true,
		"method":  method,
		"output":  strings.TrimSpace(output),
	})
	return true, 0, string(body), ""
}

// isMsiProductCode valida o formato {XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX}.
func isMsiProductCode(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) != 38 {
		return false
	}
	if trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return false
	}
	for i, r := range trimmed[1 : len(trimmed)-1] {
		switch {
		case r == '-':
			if i != 8 && i != 13 && i != 18 && i != 23 {
				return false
			}
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
		default:
			return false
		}
	}
	return true
}

// looksLikeUninstallCommand evita executar um InstallLocation (diretório) como
// se fosse comando; só aceita algo que pareça um desinstalador.
func looksLikeUninstallCommand(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "uninstall") ||
		strings.Contains(normalized, "msiexec") ||
		strings.Contains(normalized, ".exe")
}

// looksLikeUninstallTarget é estrito: usado no InstallLocation (que costuma ser
// um diretório), evitando executar um .exe qualquer da pasta do produto.
func looksLikeUninstallTarget(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized != "" && (strings.Contains(normalized, "uninstall") ||
		strings.Contains(normalized, "unins") ||
		strings.Contains(normalized, "msiexec"))
}

func runUninstallCommand(parent context.Context, command string) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "cmd", "/C", command)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("comando de desinstalação falhou: %w", err)
	}
	return text, nil
}
