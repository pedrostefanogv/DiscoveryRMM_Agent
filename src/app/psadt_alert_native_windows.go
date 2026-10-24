//go:build windows

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"discovery/app/core/notifyui"
	"discovery/app/core/platform"
)

// ── Backend NATIVO dos psadt-alerts (comando tipo 9) ──
//
// Mesmo contrato dos caminhos PSADT (exit/body/err), sem depender do módulo
// PSAppDeployToolkit:
//
//   - toast  → toast do Windows Runtime (go-toast) com o ícone GLOBAL do
//     agente (appiconPSADT.png embedado — um único asset para todos os
//     clientes/agentes do servidor);
//   - modal  → cartão nativo (core/notifyui), mesmo visual polido do prompt
//     de consentimento remoto (tema do Windows, countdown visível).
//
// Qualquer falha devolve handled=false para o chamador cair no caminho PSADT.

// psadtActionValue devolve o value da ação (identificador estável do botão).
func psadtActionValue(action PsadtAlertAction) string {
	if v := strings.TrimSpace(action.Value); v != "" {
		return v
	}
	return strings.TrimSpace(action.Label)
}

// agentToastIconPath extrai o ícone global do agente (embedado) para um
// arquivo e devolve o caminho absoluto — o Windows Runtime exige um path.
var (
	agentToastIconOnce sync.Once
	agentToastIconPathResult string
)

func agentToastIconPath() string {
	agentToastIconOnce.Do(func() {
		if len(psadtAgentIconPNG) == 0 {
			return
		}
		tmpBase, err := platform.EnsureTempDir()
		if err != nil {
			return
		}
		dir := filepath.Join(tmpBase, "notifyui")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		dest := filepath.Join(dir, "discovery-agent-icon.png")
		if err := os.WriteFile(dest, psadtAgentIconPNG, 0o644); err != nil {
			return
		}
		agentToastIconPathResult = dest
	})
	return agentToastIconPathResult
}

// nativeToastAlert exibe o toast nativo. Retorna handled=true quando o toast
// foi entregue (ou decididamente não exibível por política própria).
func (a *App) nativeToastAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string, bool) {
	req := notifyui.ToastRequest{
		AppID:   notifyui.DefaultToastAppID,
		IconPath: agentToastIconPath(),
		Title:   strings.TrimSpace(p.Title),
		Message: strings.TrimSpace(p.Message),
	}
	if req.Title == "" {
		req.Title = "Discovery Agent"
	}
	if err := runToastAsUser(ctx, req); err != nil {
		if a != nil {
			a.Logs.Append("[agent] psadt-alert [WARN] toast nativo falhou (" + err.Error() + ") — caindo no PSADT")
		}
		return 1, "", "", false
	}
	body, _ := json.Marshal(map[string]string{"action": "shown", "mode": "native-toast"})
	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert [OK] type=toast alertId=%s action=shown (native-toast)", p.AlertID))
	}
	return 0, string(body), "", true
}

// nativeModalAlert exibe o prompt modal nativo com até 3 botões e countdown
// opcional. Retorna handled=true quando o cartão produziu um desfecho
// estruturado (clicado/timeout/fechado).
func (a *App) nativeModalAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string, bool) {
	timeout := p.TimeoutSeconds
	if p.WaitForUser || timeout <= 0 {
		// Mesmo limite do caminho PSADT (UI.DefaultTimeout): guarda de
		// segurança para um prompt "sem timeout" não ficar eterno.
		timeout = maxPsadtDialogTimeoutSeconds
	}
	if timeout > maxPsadtDialogTimeoutSeconds {
		timeout = maxPsadtDialogTimeoutSeconds
	}

	// Botões: ações customizadas do alerta (ordem esquerda→direita, a última
	// é a ação principal). Sem ações, um único OK.
	buttons := make([]notifyui.Button, 0, 3)
	switch {
	case len(p.Actions) >= 3:
		p.Actions = p.Actions[:3]
		for _, action := range p.Actions {
			buttons = append(buttons, notifyui.Button{ID: psadtActionValue(action), Label: psadtActionLabel(action)})
		}
	case len(p.Actions) == 2:
		for _, action := range p.Actions {
			buttons = append(buttons, notifyui.Button{ID: psadtActionValue(action), Label: psadtActionLabel(action)})
		}
	case len(p.Actions) == 1:
		// Ação única do servidor vira O botão do prompt (mesmo comportamento
		// do PSADT, que colocava o label em PromptRightText).
		buttons = []notifyui.Button{{ID: psadtActionValue(p.Actions[0]), Label: psadtActionLabel(p.Actions[0])}}
	default:
		buttons = []notifyui.Button{{ID: "ok", Label: "OK"}}
	}
	buttons[len(buttons)-1].Kind = "primary"
	defaultID := buttons[len(buttons)-1].ID

	// waitForUser (ou timeout<=0) significa "esperar o usuário" — sem contador.
	hasCountdown := !p.WaitForUser && p.TimeoutSeconds > 0

	req := notifyui.CardRequest{
		Title:    strings.TrimSpace(p.Title),
		Subtitle: defaultPSADTSubtitleForNative(strings.TrimSpace(p.Subtitle)),
		Message:  strings.TrimSpace(p.Message),
		Buttons:  buttons,
		// Enter confirma a ação principal (último botão), como no PSADT.
		DefaultButtonID: defaultID,
		CountdownSeconds: func() int {
			if hasCountdown {
				return timeout
			}
			return 0
		}(),
		CountdownLabel: "Esta janela fechara automaticamente em:",
	}
	// Sem contador o cartão fica bloqueante: guarda de contexto de qualquer forma.
	cardCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout+30)*time.Second)
	defer cancel()

	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert iniciando type=modal (cartão nativo) alertId=%s timeout=%ds", p.AlertID, timeout))
	}

	result, ok := runCardAsUser(cardCtx, req, time.Duration(timeout+30)*time.Second)
	if !ok || result.Outcome == notifyui.OutcomeError || result.Outcome == notifyui.OutcomeCanceled {
		reason := result.Reason
		if reason == "" {
			reason = result.Outcome
		}
		if a != nil {
			a.Logs.Append("[agent] psadt-alert [WARN] cartão nativo indisponível (" + reason + ") — caindo no PSADT")
		}
		return 1, "", "", false
	}

	action := mapNativePromptResult(result, p)
	body, _ := json.Marshal(map[string]string{
		"action": action,
		"mode":   "native-card",
		"result": result.Outcome,
	})
	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert [OK] type=modal alertId=%s action=%s (native-card)", p.AlertID, action))
	}
	return 0, string(body), "", true
}

// nativeProgressAlert exibe a janela nativa de progresso (substituto do
// Show-ADTInstallationProgress) e devolve "shown" no mesmo contrato do PSADT.
func (a *App) nativeProgressAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string, bool) {
	duration := p.TimeoutSeconds
	if duration <= 0 {
		duration = 30
	}
	if duration > 300 {
		duration = 300
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = "Discovery Agent"
	}
	statusText := strings.TrimSpace(p.StatusText)
	if statusText == "" {
		statusText = title
	}
	req := notifyui.ProgressRequest{
		Title:               title,
		Subtitle:            defaultPSADTSubtitleForNative(strings.TrimSpace(p.Subtitle)),
		StatusMessage:       statusText,
		StatusMessageDetail: strings.TrimSpace(p.Subtitle),
		Percent:             p.ProgressPercent,
		DurationSeconds:     duration,
	}
	progressCtx, cancel := context.WithTimeout(context.Background(), time.Duration(duration+15)*time.Second)
	defer cancel()

	result, ok := runProgressAsUser(progressCtx, req)
	if !ok || result.Outcome == notifyui.ProgressError {
		reason := result.Reason
		if reason == "" {
			reason = result.Outcome
		}
		if a != nil {
			a.Logs.Append("[agent] psadt-alert [WARN] progresso nativo indisponível (" + reason + ") — caindo no PSADT")
		}
		return 1, "", "", false
	}
	body, _ := json.Marshal(map[string]string{"action": "shown", "mode": "native-progress"})
	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert [OK] type=update-progress alertId=%s action=shown (native-progress)", p.AlertID))
	}
	return 0, string(body), "", true
}

// mapNativePromptResult converte o desfecho do cartão nativo para o "action"
// do contrato psadt-alert (mesmas regras do mapPSADTPromptResult).
func mapNativePromptResult(result notifyui.CardResult, p PsadtAlertPayload) string {
	defaultAction := strings.TrimSpace(p.DefaultAction)
	switch result.Outcome {
	case notifyui.OutcomeClicked:
		clicked := strings.TrimSpace(result.ButtonID)
		for _, action := range p.Actions {
			if strings.EqualFold(clicked, psadtActionValue(action)) || strings.EqualFold(clicked, psadtActionLabel(action)) {
				return strings.TrimSpace(psadtActionValue(action))
			}
		}
		if clicked != "" {
			return clicked
		}
		if defaultAction != "" {
			return defaultAction
		}
		return "ok"
	case notifyui.OutcomeTimeout:
		if defaultAction != "" {
			return defaultAction
		}
		return "timeout"
	default: // closed (Esc/X)
		if defaultAction != "" {
			return defaultAction
		}
		return "ok"
	}
}

// defaultPSADTSubtitleForNative devolve um subtítulo sempre não-vazio (o
// cartão nativo não reserva linha em branco como o PSADT).
func defaultPSADTSubtitleForNative(subtitle string) string {
	if strings.TrimSpace(subtitle) == "" {
		return "Discovery Agent"
	}
	return subtitle
}

// nativePowerCountdown exibe o aviso nativo de restart/shutdown com contador e
// botão Cancelar. Retorna:
//
//	"proceed", true  — usuario confirmou ou o contador terminou;
//	"cancel",  true  — usuario cancelou (ação NÃO deve ser executada);
//	"",        false — indisponível (chamador cai no PSADT/nativo fallback).
func (a *App) nativePowerCountdown(ctx context.Context, action string, delaySeconds int, message string) (string, bool) {
	if delaySeconds <= 0 {
		delaySeconds = 60
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

	req := notifyui.CardRequest{
		Title:            title,
		Subtitle:         "Discovery Agent",
		Message:          body,
		Buttons: []notifyui.Button{
			{ID: "cancel", Label: "Cancelar"},
			{ID: "proceed", Label: okText, Kind: "primary"},
		},
		// Fim da contagem = prosseguir (mesma semântica do PSADT hoje).
		DefaultButtonID:  "proceed",
		TimeoutButtonID:  "proceed",
		CloseButtonID:    "cancel",
		CountdownSeconds: delaySeconds,
		CountdownLabel:   countdownLabel,
		Hint:             "Enter confirma · Esc cancela",
	}
	cardCtx, cancel := context.WithTimeout(ctx, time.Duration(delaySeconds+30)*time.Second)
	defer cancel()

	result, ok := runCardAsUser(cardCtx, req, time.Duration(delaySeconds+30)*time.Second)
	if !ok || result.Outcome == notifyui.OutcomeError {
		if a != nil {
			a.Logs.Append("[agent] power [WARN] cartão nativo indisponível (" + result.Reason + ") — usando fallback")
		}
		return "", false
	}
	switch {
	case result.Outcome == notifyui.OutcomeClicked && result.ButtonID == "proceed":
		return "proceed", true
	case result.Outcome == notifyui.OutcomeTimeout:
		return "proceed", true
	default:
		// Clique em Cancelar, Esc/X ou cancelamento do servidor: nada executa.
		return "cancel", true
	}
}
