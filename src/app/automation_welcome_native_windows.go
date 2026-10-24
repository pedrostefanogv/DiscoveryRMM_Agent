//go:build windows

package app

import (
	"context"
	"time"

	"discovery/app/core/automation"
	"discovery/app/core/notifyui"
)

// nativeAutomationWelcome exibe o prompt de confirmação nativo (substituto do
// Show-ADTInstallationWelcome) para automations "notificar usuario":
//
//	approved — usuario clicou em Instalar OU o contador expirou (ação padrão
//	           continuar, mesma semântica do PSADT);
//	deferred — usuario clicou em Adiar (ou fechou por Esc/X com adiamento
//	           permitido).
//
// handled=false indica indisponibilidade → chamador cai no caminho PSADT/toast.
func (a *App) nativeAutomationWelcome(req automation.AutomationNotificationRequest, timeoutSeconds int, appName string, closeProcs []string, allowDefer bool, message string) (automation.AutomationNotificationResponse, bool) {
	buttons := []notifyui.Button{}
	if allowDefer {
		buttons = append(buttons, notifyui.Button{ID: "defer", Label: "Adiar"})
	}
	buttons = append(buttons, notifyui.Button{ID: "install", Label: "Instalar", Kind: "primary"})

	cardReq := notifyui.CardRequest{
		Title:           "Instalando " + appName,
		Subtitle:        "Instalação do Aplicativo",
		Message:         message,
		Buttons:         buttons,
		DefaultButtonID: "install",
		// Fim do contador = prosseguir (mesma semântica do Welcome do PSADT).
		TimeoutButtonID:  "install",
		CloseButtonID:    allowDeferID(allowDefer),
		CountdownSeconds: timeoutSeconds,
		CountdownLabel:   "A instalacao continuara automaticamente em:",
		Hint:             "Enter confirma · Esc adia",
	}
	cardCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds+30)*time.Second)
	defer cancel()

	// O Welcome roda no SERVIÇO (sessão 0): sem o worker de UI o cartão seria
	// invisível para o usuário (mesma lacuna já corrigida nos psadt-alerts).
	result, ok := runCardAsUser(cardCtx, cardReq, time.Duration(timeoutSeconds+30)*time.Second)
	if !ok || result.Outcome == notifyui.OutcomeError || result.Outcome == notifyui.OutcomeCanceled {
		reason := result.Reason
		if reason == "" {
			reason = result.Outcome
		}
		if a != nil {
			a.Logs.Append("[automation] cartão nativo Welcome indisponível (" + reason + ") — usando caminho PSADT/toast")
		}
		return automation.AutomationNotificationResponse{}, false
	}

	switch {
	case result.Outcome == notifyui.OutcomeTimeout ||
		(result.Outcome == notifyui.OutcomeClicked && result.ButtonID == "install"):
		return automation.AutomationNotificationResponse{
			Accepted:    true,
			Result:      "approved",
			AgentAction: "native_welcome_continue",
			Message:     "usuario confirmou a execucao no prompt nativo",
		}, true
	default: // Adiar ou fechou
		return automation.AutomationNotificationResponse{
			Accepted:    true,
			Result:      "deferred",
			AgentAction: "native_welcome_defer",
			Message:     "usuario adiou a execucao no prompt nativo",
		}, true
	}
}

// allowDeferID define o desfecho do Esc/X: com adiamento permitido fecha =
// adiar; sem adiamento o Welcome do PSADT força a continuação.
func allowDeferID(allowDefer bool) string {
	if allowDefer {
		return "defer"
	}
	return "install"
}
