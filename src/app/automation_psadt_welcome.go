package app

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"discovery/app/core/automation"
	"discovery/app/core/notifyui"
)

// tryDispatchAutomationPsadtWelcome exibe o prompt de confirmação NATIVO do
// agente (cartão winui com contador, substituto do Welcome do PSADT) quando
// uma automation task marcada como "notificar usuario" vai executar.
//
// Regras:
//   - Só intercepta confirmacao de INICIO (install_start + require_confirmation
//   - layout welcome). Resultados continuam no caminho normal.
//   - Usuário adiou -> Result "deferred" (a automation agenda nova tentativa).
//   - Contador expirou / usuário continuou -> Result "approved" (executa).
//   - Prompt indisponível -> handled=false (fallback para o toast).
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
	// Um ÚNICO snapshot de processos alimenta a mensagem e a lista do cartão
	// (dois snapshots podiam divergir: mensagem citava processo já fechado).
	runningProcs := detectRunningCloseProcesses(closeProcs)
	message := welcomeMessage(appName, timeoutSeconds, closeProcs, runningProcs)
		message += fmt.Sprintf(" Os programas abertos (%s) serao fechados automaticamente em %d segundos.", strings.Join(closeProcs, ", "), timeoutSeconds)
	// Prompt NATIVO do agente (cartão winui com contador). Indisponível →
	// handled=false e o chamador cai no toast require_confirmation.
	allowDefer := automationMetadataBool(req.Metadata, "allowDefer", true)
	if resp, handled := a.nativeAutomationWelcome(req, timeoutSeconds, appName, closeProcs, runningProcs, allowDefer, message); handled {
		a.Logs.Append(fmt.Sprintf("[automation] confirmacao via prompt nativo (task=%s timeout=%ds resultado=%s)",
			strings.TrimSpace(req.NotificationID), timeoutSeconds, resp.Result))
		return resp, true
	}

	a.Logs.Append("[automation] prompt nativo indisponivel - usando notificacao padrao como fallback")
	return automation.AutomationNotificationResponse{}, false
}

// automationMetadataBool extrai um boolean do metadata (tolerante a JSON).
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
// welcomeMessage monta o corpo do Welcome nativo: lista em execução vira
// "programas abaixo" (caixa no cartão); sem processos rodando, mantém o
// texto inline com os nomes configurados.
func welcomeMessage(appName string, timeoutSeconds int, closeProcs []string, runningProcs []notifyui.ProcessItem) string {
	message := fmt.Sprintf("Instalando %s no seu computador.", appName)
	switch {
	case len(runningProcs) > 0:
		// Lista renderizada como caixa de processos no cartão (estilo PSADT).
		message += fmt.Sprintf(" Os programas abaixo serao fechados automaticamente em %d segundos.", timeoutSeconds)
	case len(closeProcs) > 0:
		message += fmt.Sprintf(" Os programas abertos (%s) serao fechados automaticamente em %d segundos.", strings.Join(closeProcs, ", "), timeoutSeconds)
	default:
		message += fmt.Sprintf(" A instalacao continuara automaticamente em %d segundos.", timeoutSeconds)
	}
	return message + " Selecione Instalar para prosseguir agora ou Adiar para fazer depois."
}
