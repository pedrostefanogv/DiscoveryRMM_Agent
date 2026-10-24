package notifyui

import (
	"strings"
)

// ProgressRequest descreve uma janela de progresso nativa (substituto do
// Show-ADTInstallationProgress).
type ProgressRequest struct {
	// Title é o título da janela (default "Discovery Agent").
	Title string
	// Subtitle aparece sob o título (default "Discovery Agent").
	Subtitle string
	// StatusMessage é o texto principal do progresso.
	StatusMessage string
	// StatusMessageDetail é o texto de apoio (default " ").
	StatusMessageDetail string
	// Percent é o percentual concluído (0..100). Fora da faixa = indeterminado
	// (barra animada).
	Percent int
	// DurationSeconds fecha a janela automaticamente após esse tempo (>0).
	// 0 = fecha apenas pelo contexto.
	DurationSeconds int
}

// ProgressResult é o desfecho da janela de progresso.
type ProgressResult struct {
	// Outcome: "completed" (tempo esgotado), "canceled" (contexto) ou
	// "error" (janela não pôde ser criada; Reason tem o detalhe).
	Outcome string
	Reason  string
}

// Progress outcomes conhecidos.
const (
	ProgressCompleted = "completed"
	ProgressCanceled  = "canceled"
	ProgressError     = "error"
)

// NormalizeProgressRequest valida e preenche defaults (puro, testável).
func NormalizeProgressRequest(req ProgressRequest) ProgressRequest {
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		req.Title = "Discovery Agent"
	}
	req.Subtitle = strings.TrimSpace(req.Subtitle)
	if req.Subtitle == "" {
		req.Subtitle = "Discovery Agent"
	}
	req.StatusMessage = strings.TrimSpace(req.StatusMessage)
	if req.StatusMessage == "" {
		req.StatusMessage = "Trabalhando..."
	}
	if strings.TrimSpace(req.StatusMessageDetail) == "" {
		req.StatusMessageDetail = " "
	}
	if req.Percent < 0 || req.Percent > 100 {
		req.Percent = -1 // indeterminado
	}
	if req.DurationSeconds < 0 {
		req.DurationSeconds = 0
	}
	return req
}

// IsIndeterminate informa se a barra deve animar sem percentual.
func (r ProgressRequest) IsIndeterminate() bool { return r.Percent < 0 }
