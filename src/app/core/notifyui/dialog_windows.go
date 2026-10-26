//go:build windows

package notifyui

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ShowDialog exibe o DialogBox padronizado (cartão custom, mesmo visual das
// notificações: logo do agente, tema do Windows, glifo do motivo) e devolve
// o texto do botão clicado — contrato equivalente ao da antiga MessageBox
// Win32 usada pelo console de debug. Esc/X devolve o rótulo do botão de
// cancelar (ou o padrão, quando o conjunto não tem cancelar).
func ShowDialog(title, text, buttons string) (string, error) {
	buttonsList := NormalizeDialogButtons(buttons)
	defaultID := buttonsList[len(buttonsList)-1].ID
	closeLabel := DialogCloseLabel(buttonsList)
	closeID := defaultID
	for _, b := range buttonsList {
		if b.ID == "cancel" {
			closeID = "cancel"
			break
		}
	}

	req := CardRequest{
		Title:           title,
		Subtitle:        "Discovery Agent",
		Message:         text,
		Icon:            IconQuestion,
		Buttons:         buttonsList,
		DefaultButtonID: defaultID,
		// Sem contador: o DialogBox do PSADT esperava indefinidamente. A
		// guarda de 3300s é o mesmo limite do PSADT (UI.DefaultTimeout).
		CloseButtonID: closeID,
	}
	guardCtx, cancel := context.WithTimeout(context.Background(), maxDialogGuardSeconds)
	defer cancel()

	result, ok := ShowCard(guardCtx, req)
	if !ok {
		return "", fmt.Errorf("cartao de dialogo indisponivel: %s", result.Reason)
	}
	return dialogResultLabel(result, buttonsList, closeLabel)
}

// maxDialogGuardSeconds limita o diálogo "sem timeout" (guarda legada do PSADT).
const maxDialogGuardSeconds = 3300 * time.Second

// dialogResultLabel converte o desfecho do cartão para o contrato do DialogBox.
func dialogResultLabel(result CardResult, buttons []Button, closeLabel string) (string, error) {
	switch result.Outcome {
	case OutcomeClicked, OutcomeTimeout:
		for _, b := range buttons {
			if strings.EqualFold(b.ID, result.ButtonID) {
				return b.Label, nil
			}
		}
		return closeLabel, nil
	case OutcomeClosed:
		return closeLabel, nil
	case OutcomeCanceled:
		return "", fmt.Errorf("dialogo cancelado")
	default:
		return "", fmt.Errorf("cartao de dialogo indisponivel: %s", result.Reason)
	}
}
