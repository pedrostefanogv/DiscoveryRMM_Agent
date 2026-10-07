package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"discovery/app/core/processutil"
)

// Defaults do power_action chamado pela IA. O atraso minimo garante tempo
// habil para o usuario CANCELAR a acao (medida de seguranca do produto).
const (
	powerActionMCPDefaultDelaySeconds = 30
	powerActionMCPMinDelaySeconds     = 10
	powerActionMCPMaxDelaySeconds     = 300
)

// RunPowerAction implementa AppBridge: restart/shutdown/lock pedidos pela IA.
//
// restart/shutdown abrem o aviso nativo do PSADT com CONTAGEM regressiva e
// botao CANCELAR (o usuario pode cancelar por seguranca). Se o usuario
// cancelar, NADA e executado. Se o PSADT nao estiver disponivel, cai para
// shutdown.exe /t <delay> /c <msg> — que tambem mostra aviso nativo e pode ser
// cancelado com "shutdown /a".
func (a *App) RunPowerAction(ctx context.Context, action string, delaySeconds int, message string) (json.RawMessage, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "restart", "shutdown", "lock":
	default:
		return nil, fmt.Errorf("acao de energia invalida: %q (use restart, shutdown ou lock)", action)
	}

	if action == "lock" {
		if err := lockWorkstationSession(ctx); err != nil {
			return nil, err
		}
		a.logPowerAction(action, "sessao bloqueada")
		return marshalPowerResult(map[string]any{"ok": true, "action": "lock", "outcome": "executed"}), nil
	}

	delay := delaySeconds
	if delay <= 0 {
		delay = powerActionMCPDefaultDelaySeconds
	}
	if delay < powerActionMCPMinDelaySeconds {
		delay = powerActionMCPMinDelaySeconds
	}
	if delay > powerActionMCPMaxDelaySeconds {
		delay = powerActionMCPMaxDelaySeconds
	}

	body := strings.TrimSpace(message)

	// 1) PSADT: aviso Fluent com contador e cancelamento.
	switch a.showPSADTCancellablePowerCountdown(action, delay, body) {
	case "proceed":
		code, out, errText := a.executeSystemPowerAction(ctx, action, 0, false, "")
		if code != 0 || strings.TrimSpace(errText) != "" {
			return nil, fmt.Errorf("falha ao executar %s apos confirmacao: %s %s", action, strings.TrimSpace(out), strings.TrimSpace(errText))
		}
		a.logPowerAction(action, "executado apos confirmacao no aviso")
		return marshalPowerResult(map[string]any{
			"ok": true, "action": action, "outcome": "executed", "delaySeconds": delay,
		}), nil
	case "cancel":
		a.logPowerAction(action, "CANCELADO pelo usuario no aviso do PSADT")
		return marshalPowerResult(map[string]any{
			"ok": false, "action": action, "outcome": "cancelled",
			"message": "O usuario CANCELOU a acao. Nada foi executado; nao repita sem pedir de novo.",
		}), nil
	}

	// 2) Fallback: aviso nativo do Windows + cancelavel via "shutdown /a".
	code, out, errText := a.executeSystemPowerAction(ctx, action, delay, false, body)
	if code != 0 || strings.TrimSpace(errText) != "" {
		return nil, fmt.Errorf("falha ao agendar %s: %s %s", action, strings.TrimSpace(out), strings.TrimSpace(errText))
	}
	a.logPowerAction(action, fmt.Sprintf("agendado com aviso nativo (delay=%ds)", delay))
	return marshalPowerResult(map[string]any{
		"ok": true, "action": action, "outcome": "scheduled", "delaySeconds": delay,
		"cancelHint": fmt.Sprintf("o usuario tem %ds para cancelar com 'shutdown /a'", delay),
	}), nil
}

// showPSADTCancellablePowerCountdown exibe o aviso do PSADT com contador e
// botao Cancelar para restart E shutdown (o RestartPrompt do PSADT nao tem
// cancelamento). Retorna:
//
//	"proceed"  — usuario confirmou ou o contador terminou;
//	"cancel"   — usuario cancelou (a acao NAO deve ser executada);
//	"fallback" — PSADT indisponivel/erro (usar o caminho nativo).
func (a *App) showPSADTCancellablePowerCountdown(action string, delaySeconds int, message string) string {
	if runtime.GOOS != "windows" {
		return "fallback"
	}
	if delaySeconds <= 0 {
		delaySeconds = powerActionMCPDefaultDelaySeconds
	}

	title := "Reinicializacao Necessaria"
	okText := "Reiniciar agora"
	countdownLabel := "O computador sera reiniciado em:"
	defaultMsg := fmt.Sprintf("O computador sera reiniciado automaticamente em %d segundos.", delaySeconds)
	if action == "shutdown" {
		title = "Desligamento Necessario"
		okText = "Desligar agora"
		countdownLabel = "O computador sera desligado em:"
		defaultMsg = fmt.Sprintf("O computador sera desligado automaticamente em %d segundos.", delaySeconds)
	}

	body := strings.TrimSpace(message)
	if body == "" {
		body = defaultMsg
	}

	req := PSADTVisualNotificationRequest{
		NotifType:        "countdown",
		Title:            title,
		Message:          body,
		Subtitle:         "Discovery Agent",
		AppName:          "Discovery Agent",
		ButtonText:       okText,
		ButtonCloseText:  "Cancelar",
		CountdownLabel:   countdownLabel,
		PromptTimeout:    delaySeconds,
		CountdownNoDefer: false,
	}

	a.logPowerAction(action, fmt.Sprintf("exibindo aviso PSADT com contador (delay=%ds, cancelavel)", delaySeconds))
	result := a.ExecutePSADTVisualNotification(req)

	if strings.Contains(result.Output, "COUNTDOWN-PROCEED") {
		return "proceed"
	}
	if psadtWelcomeFailed(result.Output, result.Error, result.ExitCode) {
		a.logPowerAction(action, fmt.Sprintf("aviso PSADT indisponivel (exit=%d) - usando aviso nativo", result.ExitCode))
		return "fallback"
	}
	// Sem o marcador de conclusao e sem erro de infraestrutura: o PSADT
	// encerrou porque o usuario cancelou/adiu.
	return "cancel"
}

// lockWorkstationSession bloqueia a sessao (sem aviso: o bloqueio e reversivel
// e nao perde trabalho). Usa CommandContext para o botao Parar/cancelamento do
// stream nao ficar preso num processo que nao responde.
func lockWorkstationSession(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "rundll32.exe", "user32.dll,LockWorkStation")
	processutil.HideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("falha ao bloquear a sessao: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *App) logPowerAction(action, detail string) {
	if a == nil {
		return
	}
	a.Logs.Append(fmt.Sprintf("[power][mcp] %s: %s", action, detail))
}

func marshalPowerResult(payload map[string]any) json.RawMessage {
	raw, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{"error":"falha ao serializar resultado"}`)
	}
	return raw
}
