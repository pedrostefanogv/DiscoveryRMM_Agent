package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"discovery/app/core/notifyui"
)

// ── Dispatch NATIVO do console de debug (psadt_visual) ──
//
// Mapeia os tipos do console de debug para os componentes nativos
// (core/notifyui), produzindo NativeToolResult.

func executeNativeVisualNotification(req NativeVisualNotificationRequest, result *NativeToolResult) NativeToolResult {
	req.NotifType = strings.TrimSpace(strings.ToLower(req.NotifType))
	if req.NotifType == "" {
		req.NotifType = "balloon_info"
	}

	switch req.NotifType {
	case "balloon_info", "balloon_warning", "balloon_error":
		return nativeBalloonNotification(req, result)
	case "progress":
		return nativeProgressNotification(req, result)
	case "dialog", "dialog_box":
		return nativeDialogNotification(req, result)
	case "prompt_ok", "prompt_yesno", "prompt_continue", "countdown", "welcome", "restart_prompt":
		return nativeCardNotification(req, result)
	default:
		result.Success = false
		result.ExitCode = 1
		result.Error = "tipo de notificacao desconhecido: " + req.NotifType
		return *result
	}
}

func nativeBalloonNotification(req NativeVisualNotificationRequest, result *NativeToolResult) NativeToolResult {
	// Nota: em modo worker (sessão do usuário) o diretório do asset é RX para o
	// usuário — agentToastIconPath() devolve "" e o toast usa o ícone default
	// do Windows. O fluxo do servidor (toast job) resolve o ícone NO SERVIÇO.
	err := notifyui.ShowToast(notifyui.ToastRequest{
		AppID:   notifyui.DefaultToastAppID,
		IconPath: agentToastIconPath(),
		Title:   strings.TrimSpace(req.Title),
		Message: strings.TrimSpace(req.Message),
	})
	if err != nil {
		result.Success = false
		result.ExitCode = 1
		result.Error = err.Error()
		return *result
	}
	result.Success = true
	result.ExitCode = 0
	result.Output = "Toast exibido com sucesso"
	return *result
}

func nativeProgressNotification(req NativeVisualNotificationRequest, result *NativeToolResult) NativeToolResult {
	duration := req.DurationSeconds
	if duration <= 0 || duration > 60 {
		duration = 5
	}
	preq := notifyui.ProgressRequest{
		Title:               strings.TrimSpace(req.Title),
		Subtitle:            nativeSubtitle(req.Subtitle, req.AppName),
		StatusMessage:       strings.TrimSpace(req.Message),
		StatusMessageDetail: strings.TrimSpace(req.Subtitle),
		Percent:             -1,
		DurationSeconds:     duration,
	}
	pctx, cancel := context.WithTimeout(context.Background(), time.Duration(duration+15)*time.Second)
	defer cancel()
	pres, ok := notifyui.ShowProgress(pctx, preq)
	if !ok || pres.Outcome == notifyui.ProgressError {
		result.Success = false
		result.ExitCode = 1
		result.Error = pres.Reason
		if result.Error == "" {
			result.Error = "janela de progresso indisponivel"
		}
		return *result
	}
	result.Success = true
	result.ExitCode = 0
	result.Output = fmt.Sprintf("Progresso exibido por %d segundos", duration)
	return *result
}

func nativeDialogNotification(req NativeVisualNotificationRequest, result *NativeToolResult) NativeToolResult {
	label, err := notifyui.ShowMessageBox(strings.TrimSpace(req.Title), strings.TrimSpace(req.Message), req.DialogButtons)
	if err != nil {
		result.Success = false
		result.ExitCode = 1
		result.Error = err.Error()
		return *result
	}
	result.Success = true
	result.ExitCode = 0
	result.Result = label
	result.Output = "DialogBox exibido: botao=" + label
	return *result
}

// nativeCardNotification cobre prompt_ok/yesno/continue, countdown, welcome
// e restart_prompt via cartão nativo.
func nativeCardNotification(req NativeVisualNotificationRequest, result *NativeToolResult) NativeToolResult {
	cardReq := notifyui.CardRequest{
		Title:    strings.TrimSpace(req.Title),
		Subtitle: nativeSubtitle(req.Subtitle, req.AppName),
		Message:  strings.TrimSpace(req.Message),
	}

	trimmed := func(s string) string { return strings.TrimSpace(s) }
	switch req.NotifType {
	case "prompt_ok":
		cardReq.Buttons = []notifyui.Button{{ID: "ok", Label: orDefault(trimmed(req.PromptRightText), "OK"), Kind: "primary"}}
		cardReq.CountdownSeconds = clampPromptTimeout(req.PromptTimeout, false)
	case "prompt_yesno":
		cardReq.Buttons = []notifyui.Button{
			{ID: "left", Label: orDefault(trimmed(req.PromptLeftText), "Sim")},
			{ID: "right", Label: orDefault(trimmed(req.PromptRightText), "Nao"), Kind: "primary"},
		}
		cardReq.DefaultButtonID = "right"
		cardReq.CountdownSeconds = clampPromptTimeout(req.PromptTimeout, false)
	case "prompt_continue":
		cardReq.Buttons = []notifyui.Button{
			{ID: "left", Label: orDefault(trimmed(req.PromptLeftText), "Continuar")},
			{ID: "right", Label: orDefault(trimmed(req.PromptRightText), "Adiar"), Kind: "primary"},
		}
		cardReq.DefaultButtonID = "left"
		cardReq.CountdownSeconds = clampPromptTimeout(req.PromptTimeout, false)
	case "countdown":
		okText := orDefault(trimmed(req.ButtonText), "OK")
		closeText := orDefault(trimmed(req.ButtonCloseText), "Fechar")
		cardReq.Buttons = []notifyui.Button{
			{ID: "close", Label: closeText},
			{ID: "ok", Label: okText, Kind: "primary"},
		}
		if req.CountdownNoDefer {
			cardReq.Buttons = []notifyui.Button{{ID: "ok", Label: okText, Kind: "primary"}}
		}
		cardReq.DefaultButtonID = "ok"
		cardReq.TimeoutButtonID = "ok"
		cardReq.CloseButtonID = nativeCloseButton(req.CountdownNoDefer)
		cardReq.CountdownSeconds = clampPromptTimeout(req.PromptTimeout, false)
		cardReq.CountdownLabel = orDefault(trimmed(req.CountdownLabel), "Esta janela fechara automaticamente em:")
		cardReq.Hint = "Enter confirma · Esc fecha"
	case "welcome":
		closeCountdown := req.CloseProcessesCountdown
		if closeCountdown <= 0 || closeCountdown > 3600 {
			closeCountdown = 60
		}
		cardReq.Buttons = []notifyui.Button{
			{ID: "defer", Label: "Adiar"},
			{ID: "install", Label: "Instalar", Kind: "primary"},
		}
		if !req.AllowDefer {
			cardReq.Buttons = []notifyui.Button{{ID: "install", Label: "Instalar", Kind: "primary"}}
		}
		cardReq.DefaultButtonID = "install"
		cardReq.TimeoutButtonID = "install"
		// Esc/X: com AllowDefer fecha = adiar; sem adiamento prossegue
		// (os IDs precisam EXISTIR entre os botões — Normalize rejeita os outros).
		cardReq.CloseButtonID = nativeCloseButton(!req.AllowDefer)
		if req.AllowDefer {
			cardReq.CloseButtonID = "defer"
		}
		cardReq.CountdownSeconds = closeCountdown
		cardReq.CountdownLabel = "A instalacao continuara automaticamente em:"
		cardReq.Hint = "Enter confirma · Esc adia"
	case "restart_prompt":
		countdown := req.RestartCountdownSeconds
		if countdown <= 0 || countdown > 3600 {
			countdown = 60
		}
		cardReq.Buttons = []notifyui.Button{{ID: "restart", Label: "Reiniciar agora", Kind: "primary"}}
		cardReq.DefaultButtonID = "restart"
		cardReq.TimeoutButtonID = "restart"
		cardReq.CloseButtonID = "restart"
		cardReq.CountdownSeconds = countdown
		cardReq.CountdownLabel = "O computador sera reiniciado em:"
		cardReq.Hint = "Enter confirma · Esc fecha"
	}

	// Debug: sem contador → guarda de 3300s (mesmo limite legado).
	guard := cardReq.CountdownSeconds
	if guard <= 0 {
		guard = maxPsadtDialogTimeoutSeconds
	}
	cardCtx, cancel := context.WithTimeout(context.Background(), time.Duration(guard+30)*time.Second)
	defer cancel()

	cardRes, ok := notifyui.ShowCard(cardCtx, cardReq)
	if !ok {
		result.Success = false
		result.ExitCode = 1
		result.Error = cardRes.Reason
		if result.Error == "" {
			result.Error = "cartao nativo indisponivel"
		}
		return *result
	}

	result.Success = true
	result.ExitCode = 0
	result.Result = cardButtonText(cardRes, cardReq.Buttons)
	switch cardRes.Outcome {
	case notifyui.OutcomeClicked, notifyui.OutcomeTimeout, notifyui.OutcomeClosed:
		result.Output = "Notificacao exibida: desfecho=" + cardRes.Outcome + " botao=" + result.Result
	default:
		result.Output = "Notificacao exibida: desfecho=" + cardRes.Outcome
	}
	// Marcadores legados preservados para consumidores do console.
	if req.NotifType == "welcome" && (cardRes.Outcome == notifyui.OutcomeTimeout ||
		(cardRes.Outcome == notifyui.OutcomeClicked && cardRes.ButtonID == "install")) {
		result.Output += "\nInstallationWelcome concluido"
	}
	if req.NotifType == "countdown" && (cardRes.Outcome == notifyui.OutcomeTimeout ||
		(cardRes.Outcome == notifyui.OutcomeClicked && cardRes.ButtonID == "ok")) {
		result.Output += "\nCOUNTDOWN-PROCEED"
	}
	return *result
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// nativeSubtitle devolve subtítulo sempre não-vazio (cartão nativo).
func nativeSubtitle(subtitle, appName string) string {
	if s := strings.TrimSpace(subtitle); s != "" {
		return s
	}
	if s := strings.TrimSpace(appName); s != "" {
		return s
	}
	return "Discovery Agent"
}

// clampPromptTimeout: NoWait → 0 (sem contador); sem timeout → 120s;
// teto 3300s (guarda legada).
func clampPromptTimeout(timeout int, noWait bool) int {
	if noWait {
		return 0
	}
	if timeout <= 0 {
		return 120
	}
	if timeout > maxPsadtDialogTimeoutSeconds {
		return maxPsadtDialogTimeoutSeconds
	}
	return timeout
}

// nativeCloseButton define o desfecho do Esc/X quando não há adiamento:
// sem opção de adiar, fechar prossegue (mesma semântica do PSADT).
func nativeCloseButton(noDefer bool) string {
	if noDefer {
		return "ok"
	}
	return "close"
}

func cardButtonText(res notifyui.CardResult, buttons []notifyui.Button) string {
	for _, b := range buttons {
		if strings.EqualFold(b.ID, res.ButtonID) {
			return b.Label
		}
	}
	return res.ButtonID
}