// Package notifyui fornece as janelas nativas de notificação do agente
// (backend "native" das notificações visuais, substituto do PSADT):
//
//   - ShowCard: cartão topmost no canto inferior direito, desenhado com GDI/GDI+
//     e o tema do Windows do usuário (claro/escuro + accent), com botões
//     (1..3), contagem regressiva opcional e resultado estruturado — o mesmo
//     padrão visual e de código do prompt de consentimento remoto;
//   - ShowToast: toast do Windows via Windows Runtime (go-toast), com o AppID
//     global do agente.
//
// O pacote NÃO conhece o PSADT: os chamadores (app) mapeiam os payloads dos
// comandos do servidor para os tipos daqui.
package notifyui

import (
	"fmt"
	"strings"
	"time"
)

// Button descreve um botão do cartão.
type Button struct {
	// ID é o identificador devolvido em CardResult.ButtonID quando clicado.
	ID string
	// Label é o texto exibido.
	Label string
	// Kind seleciona o estilo: "primary" (preenchido) ou "outline".
	// Vazio = "outline".
	Kind string
}

// IsPrimary informa se o botão deve ser desenhado em destaque.
func (b Button) IsPrimary() bool {
	return strings.EqualFold(strings.TrimSpace(b.Kind), "primary")
}

// CardRequest descreve um cartão de notificação nativo.
type CardRequest struct {
	// Title é o título do cartão (obrigatório; default "Discovery Agent").
	Title string
	// Subtitle aparece sob o título (default "Discovery Agent").
	Subtitle string
	// Message é o corpo da mensagem (quebrado/reticenciado como no consent).
	Message string
	// Buttons lista os botões (1..3, em ordem da esquerda para a direita).
	Buttons []Button
	// DefaultButtonID é o botão ativado por Enter/Space e com foco inicial.
	// Vazio = último botão.
	DefaultButtonID string
	// TimeoutButtonID é o botão considerado quando a contagem termina.
	// Vazio = o resultado vem com Outcome "timeout" e ButtonID "".
	TimeoutButtonID string
	// CloseButtonID é o botão considerado no Esc/X.
	// Vazio = o resultado vem com Outcome "closed" e ButtonID "".
	CloseButtonID string
	// CountdownSeconds exibe a contagem regressiva (>0) e encerra o cartão
	// quando chega a zero. 0 = sem contador (espera o usuário para sempre —
	// o chamador deve proteger com contexto/timeout).
	CountdownSeconds int
	// CountdownLabel é o texto acima do relógio
	// (default "O computador sera reiniciado em:" não — default genérico
	// "Tempo restante:").
	CountdownLabel string
	// Hint é a dica de teclado exibida sob os botões. Vazio usa o padrão
	// "Enter confirma · Esc fecha".
	Hint string
	// Icon é o motivo da notificação exibido à direita do cabeçalho:
	// "info" | "warning" | "error" | "question". Vazio vira "info".
	Icon string
}

// CardResult é o desfecho do cartão.
//
//   - Outcome "clicked": o usuário clicou em um botão (ButtonID preenchido);
//   - Outcome "timeout": a contagem terminou (ButtonID = TimeoutButtonID);
//   - Outcome "closed": Esc/X (ButtonID = CloseButtonID);
//   - Outcome "canceled": o contexto foi cancelado pelo servidor;
//   - Outcome "error": a janela não pôde ser criada.
type CardResult struct {
	ButtonID string
	Outcome  string
	// Reason traz detalhe para logs em caso de erro.
	Reason string
}

// Outcomes conhecidos.
const (
	OutcomeClicked  = "clicked"
	OutcomeTimeout  = "timeout"
	OutcomeClosed   = "closed"
	OutcomeCanceled = "canceled"
	OutcomeError    = "error"
)

// NormalizeCardRequest valida e preenche os defaults do pedido (puro, testável).
func NormalizeCardRequest(req CardRequest) CardRequest {
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		req.Title = "Discovery Agent"
	}
	req.Subtitle = strings.TrimSpace(req.Subtitle)
	if req.Subtitle == "" {
		req.Subtitle = "Discovery Agent"
	}
	if req.Message == "" {
		req.Message = " "
	}
	req.CountdownLabel = strings.TrimSpace(req.CountdownLabel)
	if req.CountdownLabel == "" && req.CountdownSeconds > 0 {
		req.CountdownLabel = "Tempo restante:"
	}
	req.Hint = strings.TrimSpace(req.Hint)
	if req.Hint == "" {
		req.Hint = "Enter confirma · Esc fecha"
	}
	req.Icon = normalizeIconKind(req.Icon)

	// Botões: mantém no máximo 3 e garante que haja pelo menos um OK.
	buttons := make([]Button, 0, 3)
	for _, b := range req.Buttons {
		b.ID = strings.TrimSpace(b.ID)
		b.Label = strings.TrimSpace(b.Label)
		if b.ID == "" || b.Label == "" {
			continue
		}
		buttons = append(buttons, b)
		if len(buttons) == 3 {
			break
		}
	}
	if len(buttons) == 0 {
		buttons = []Button{{ID: "ok", Label: "OK", Kind: "primary"}}
	}
	req.Buttons = buttons

	// Default: último botão (padrão de dialogos: a ação à direita é a principal).
	req.DefaultButtonID = strings.TrimSpace(req.DefaultButtonID)
	if req.DefaultButtonID == "" {
		req.DefaultButtonID = buttons[len(buttons)-1].ID
	}
	// O default precisa existir entre os botões; senão cai no último.
	if !hasButtonID(buttons, req.DefaultButtonID) {
		req.DefaultButtonID = buttons[len(buttons)-1].ID
	}
	req.TimeoutButtonID = strings.TrimSpace(req.TimeoutButtonID)
	req.CloseButtonID = strings.TrimSpace(req.CloseButtonID)
	if req.CountdownSeconds < 0 {
		req.CountdownSeconds = 0
	}
	if req.CountdownSeconds > 0 {
		// Sem o botão de timeout o cartão encerraria com resultado vazio.
		if req.TimeoutButtonID == "" || !hasButtonID(buttons, req.TimeoutButtonID) {
			req.TimeoutButtonID = req.DefaultButtonID
		}
	}
	return req
}

func hasButtonID(buttons []Button, id string) bool {
	for _, b := range buttons {
		if strings.EqualFold(b.ID, id) {
			return true
		}
	}
	return false
}

// CountdownClock formata o relógio da contagem (mm:ss; >= 1h também mm:ss).
func CountdownClock(remaining time.Duration) string {
	if remaining < 0 {
		remaining = 0
	}
	total := int(remaining / time.Second)
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}

// ── DialogBox (substituto padronizado da MessageBox Win32) ──
//
// O console de debug usava a MessageBox nativa do Windows (ícone/título do
// SO, fora do padrão visual). ShowDialog exibe o MESMO cartão custom das
// notificações (logo do agente, tema do Windows) e devolve o texto do botão
// clicado — mesmo contrato do MessageBoxButtonText.

// dialogButtonSets mapeia o conjunto de botões legado para os botões do
// cartão (ordem esquerda→direita; o ÚLTIMO é a ação principal). Os rótulos
// preservam o texto exato do MessageBoxButtonText (contrato do resultado).
var dialogButtonSets = map[string][]Button{
	"ok":                {{"ok", "OK", "primary"}},
	"okcancel":          {{"cancel", "Cancel", ""}, {"ok", "OK", "primary"}},
	"abortretryignore":  {{"ignore", "Ignore", ""}, {"retry", "Retry", ""}, {"abort", "Abort", "primary"}},
	"yesnocancel":       {{"cancel", "Cancel", ""}, {"no", "No", ""}, {"yes", "Yes", "primary"}},
	"yesno":             {{"no", "No", ""}, {"yes", "Yes", "primary"}},
	"retrycancel":       {{"cancel", "Cancel", ""}, {"retry", "Retry", "primary"}},
	"canceltrycontinue": {{"cancel", "Cancel", ""}, {"tryagain", "Try Again", ""}, {"continue", "Continue", "primary"}},
}

// NormalizeDialogButtons devolve o conjunto de botões do cartão para o kind
// legado (""/desconhecido = OK).
func NormalizeDialogButtons(buttons string) []Button {
	key := strings.ToLower(strings.TrimSpace(buttons))
	set, ok := dialogButtonSets[key]
	if !ok {
		set = dialogButtonSets["ok"]
	}
	out := make([]Button, len(set))
	copy(out, set)
	return out
}

// DialogCloseLabel devolve o rótulo usado quando o usuário fecha o cartão
// (Esc/X): "Cancel" quando o conjunto tem cancelar; senão o rótulo padrão.
func DialogCloseLabel(buttons []Button) string {
	for _, b := range buttons {
		if strings.EqualFold(b.ID, "cancel") {
			return b.Label
		}
	}
	return buttons[len(buttons)-1].Label
}
