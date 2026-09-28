package app

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"discovery/app/core/automation"
)

// tryDispatchAutomationPsadtWelcome tenta exibir o prompt Welcome nativo do
// PSAppDeployToolkit (Show-ADTInstallationWelcome) quando uma automation task
// marcada como "notificar usuario" (RequiresApproval) vai executar.
//
// Regras:
//   - Só intercepta confirmacao de INICIO (install_start + require_confirmation
//   - layout welcome). Resultados continuam no caminho normal.
//   - Exige PSADT habilitado e Windows. Caso contrário retorna handled=false e o
//     chamador segue no toast require_confirmation (fallback).
//   - Usuário adiou -> Result "deferred" (a automation agenda nova tentativa).
//   - Contador expirou / usuário continuou -> Result "approved" (executa).
//   - Falha ao exibir o Welcome -> handled=false (fallback para o toast).
func (a *App) tryDispatchAutomationPsadtWelcome(req automation.AutomationNotificationRequest) (automation.AutomationNotificationResponse, bool) {
	if a == nil {
		return automation.AutomationNotificationResponse{}, false
	}
	if req.Mode != "require_confirmation" || req.EventType != "install_start" {
		return automation.AutomationNotificationResponse{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(req.Layout), "welcome") {
		return automation.AutomationNotificationResponse{}, false
	}
	if runtime.GOOS != "windows" {
		return automation.AutomationNotificationResponse{}, false
	}
	psadtCfg := a.GetAgentConfiguration().PSADT
	if psadtCfg.Enabled == nil || !*psadtCfg.Enabled {
		return automation.AutomationNotificationResponse{}, false
	}

	timeoutSeconds := automationMetadataInt(req.Metadata, "promptTimeoutSeconds", 60)
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	if timeoutSeconds > 3600 {
		timeoutSeconds = 3600
	}

	// Identidade: o dialogo deve dizer O QUE esta sendo instalado. O PSADT monta
	// o titulo a partir do AppName da sessao (sem Vendor/Version — o agent nao
	// deve aparecer como "Discovery Discovery Agent 1.0").
	appName := strings.TrimSpace(automationMetadataString(req.Metadata, "taskName"))
	if appName == "" {
		appName = strings.TrimSpace(automationMetadataString(req.Metadata, "packageId"))
	}
	if appName == "" {
		appName = "aplicativo"
	}
	closeProcs := automationMetadataStrings(req.Metadata, "closeProcesses")
	message := fmt.Sprintf("Instalando %s no seu computador.", appName)
	if len(closeProcs) > 0 {
		message += fmt.Sprintf(" Os programas abertos (%s) serao fechados automaticamente em %d segundos.", strings.Join(closeProcs, ", "), timeoutSeconds)
	} else {
		message += fmt.Sprintf(" A instalacao continuara automaticamente em %d segundos.", timeoutSeconds)
	}
	message += " Selecione Instalar para prosseguir agora ou Adiar para fazer depois."

	welcomeReq := PSADTVisualNotificationRequest{
		NotifType:               "welcome",
		Title:                   "Instalando " + appName,
		Message:                 message,
		Subtitle:                "Instalação do Aplicativo",
		AppName:                 appName,
		AppVendor:               "",
		AppVersion:              "",
		AllowDefer:              automationMetadataBool(req.Metadata, "allowDefer", true),
		DeferTimes:              automationMetadataInt(req.Metadata, "deferTimes", 0),
		CloseProcesses:          strings.Join(closeProcs, ","),
		CloseProcessesCountdown: timeoutSeconds,
	}

	a.Logs.Append(fmt.Sprintf("[automation] exigindo confirmacao do usuario via PSADT Welcome (task=%s timeout=%ds procs=%q defer=%t)",
		strings.TrimSpace(req.NotificationID), timeoutSeconds, welcomeReq.CloseProcesses, welcomeReq.AllowDefer))

	result := a.ExecutePSADTVisualNotification(welcomeReq)

	// Marcador emitido pelo script quando o Welcome terminou e o usuario
	// continuou (ou o contador expirou). Chegar até aqui = prosseguir.
	if strings.Contains(result.Output, "InstallationWelcome concluido") {
		return automation.AutomationNotificationResponse{
			Accepted:    true,
			Result:      "approved",
			AgentAction: "psadt_welcome_continue",
			Message:     "usuario confirmou a execucao no Welcome do PSADT",
		}, true
	}

	// Without the marker, distinguish a real PSADT failure (fallback to toast)
	// from the user deferring (which terminates the session before the marker).
	if psadtWelcomeFailed(result.Output, result.Error, result.ExitCode) {
		a.Logs.Append("[automation] PSADT Welcome indisponivel - usando notificacao padrao como fallback")
		return automation.AutomationNotificationResponse{}, false
	}

	return automation.AutomationNotificationResponse{
		Accepted:    true,
		Result:      "deferred",
		AgentAction: "psadt_welcome_defer",
		Message:     "usuario adiou a execucao no Welcome do PSADT",
	}, true
}

// psadtWelcomeFailed identifica falhas de infraestrutura do PSADT (modulo
// ausente, sessao nao abriu, staging invalido) que devem cair no fallback de
// toast em vez de serem tratadas como adiamento do usuario.
func psadtWelcomeFailed(output, errText string, exitCode int) bool {
	combined := strings.ToLower(output + " " + errText)
	for _, marker := range []string{
		"falha ao importar psappdeploytoolkit",
		"falha ao abrir sessao psadt",
		"falha ao aplicar branding customizado",
		"psadtwelcome",
		"is not recognized",
		"ambiguousparameterset",
		"parameter set cannot be resolved",
	} {
		if strings.Contains(combined, marker) {
			return true
		}
	}
	// ExitCode 1..3 são os exits explícitos do header (import/sessao/staging).
	// Em Windows, defer do Welcome encerra o processo pelo PSADT com códigos
	// reservados (>=60000) ou 0 — nunca 1..3.
	return exitCode >= 1 && exitCode <= 3
}

func automationMetadataBool(metadata map[string]any, key string, fallback bool) bool {
	if metadata == nil {
		return fallback
	}
	value, ok := metadata[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		if err == nil {
			return parsed
		}
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	}
	return fallback
}

func automationMetadataInt(metadata map[string]any, key string, fallback int) int {
	if metadata == nil {
		return fallback
	}
	value, ok := metadata[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return fallback
}

// automationMetadataString extrai um valor string simples do metadata (com
// tolerância a números/floats vindos de JSON).
func automationMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	if number, ok := value.(float64); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func automationMetadataStrings(metadata map[string]any, key string) []string {
	if metadata == nil {
		return nil
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return nil
	}
	const maxCloseProcesses = 20
	appendName := func(out []string, seen map[string]struct{}, raw string) []string {
		name := strings.TrimSpace(raw)
		if name == "" || len(out) >= maxCloseProcesses {
			return out
		}
		lower := strings.ToLower(name)
		if _, exists := seen[lower]; exists {
			return out
		}
		seen[lower] = struct{}{}
		return append(out, name)
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)
	switch typed := value.(type) {
	case []string:
		for _, item := range typed {
			out = appendName(out, seen, item)
		}
	case []any:
		for _, item := range typed {
			out = appendName(out, seen, fmt.Sprint(item))
		}
	case string:
		for _, item := range strings.Split(typed, ",") {
			out = appendName(out, seen, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
