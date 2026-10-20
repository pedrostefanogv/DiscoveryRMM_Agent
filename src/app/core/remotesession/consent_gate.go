package remotesession

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

// ── Gate de consentimento ───────────────────────────────────────────────────

func (m *Manager) cancelPendingConsent() bool {
	m.consentMu.Lock()
	defer m.consentMu.Unlock()
	if m.consentCancel == nil {
		return false
	}
	m.consentCancel()
	m.consentCancel = nil
	m.consentSessionID = ""
	return true
}

// runConsentGate executa o prompt (ou aplica a ação de timeout) e reporta a
// decisão ao servidor. Fail-closed: sem prompt/report, a sessão NÃO inicia.
func (m *Manager) runConsentGate(ctx context.Context, sessionID string, payload map[string]any) (bool, string) {
	cfg := ParseConsentConfig(payload)
	if cfg == nil {
		return true, ""
	}
	// Fail-closed em modo desconhecido: um servidor que manda um modo que este
	// agent não conhece NÃO pode ganhar acesso sem prompt (antes cai fora do
	// gate com return true e a sessão iniciava sem autorização).
	switch cfg.Mode {
	case "prompt":
		// segue para o prompt abaixo
	case "off", "notify":
		return true, ""
	default:
		log.Printf("[remote-consent] modo de consentimento desconhecido %q sessionId=%s — recusando (fail-closed)\n", cfg.Mode, sessionID)
		return false, "consentimento: modo desconhecido (" + cfg.Mode + ")"
	}

	// Cancela prompt anterior (novo start substitui a espera anterior).
	if m.cancelPendingConsent() {
		log.Printf("[remote-consent] prompt anterior cancelado (superseded) sessionId=%s\n", sessionID)
	}

	promptCtx, cancel := cfg.WithTimeoutContext(ctx)
	defer cancel()

	m.consentMu.Lock()
	m.consentCancel = cancel
	m.consentSessionID = sessionID
	m.consentMu.Unlock()
	defer func() {
		m.consentMu.Lock()
		// Só limpa se ainda for o prompt DESTA sessão: um start novo pode ter
		// substituído o cancel (não pode apagar o cancel do prompt mais novo).
		if m.consentSessionID == sessionID {
			m.consentCancel = nil
			m.consentSessionID = ""
		}
		m.consentMu.Unlock()
	}()

	decision := ConsentDecision{}
	if m.interactiveUser != nil && strings.TrimSpace(m.interactiveUser()) == "" {
		decision = cfg.TimeoutDecision("no-interactive-user")
		decision.Outcome = "no-user-denied"
		if decision.Accepted {
			decision.Outcome = "no-user-granted"
		}
	} else if m.consentPrompt == nil {
		decision = ConsentDecision{
			Accepted:      false,
			Auto:          true,
			Outcome:       "prompt-unavailable",
			Reason:        "prompt unavailable",
			PromptBackend: "none",
			DecidedAt:     time.Now().UTC(),
		}
	} else {
		decision = m.consentPrompt(promptCtx, cfg.PromptRequest(sessionID))
		// Deadline vencido = aplica a ação de timeout configurada. Cancelamento
		// explícito (stop do servidor/worker) vira recusa auditável.
		switch {
		case errors.Is(promptCtx.Err(), context.DeadlineExceeded):
			decision = cfg.TimeoutDecision("timeout")
		case promptCtx.Err() != nil && !decision.Accepted:
			decision = ConsentDecision{
				Accepted:      false,
				Auto:          true,
				Outcome:       "stopped-by-server",
				Reason:        "stopped_by_server",
				PromptBackend: decision.PromptBackend,
				DecidedAt:     time.Now().UTC(),
			}
		}
	}

	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = time.Now().UTC()
	}
	if decision.Outcome == "" {
		if decision.Accepted {
			decision.Outcome = "granted"
		} else {
			decision.Outcome = "denied"
		}
	}
	if decision.Reason == "" {
		decision.Reason = decision.Outcome
	}

	if m.consentReporter == nil {
		log.Printf("[remote-consent] reporter não configurado sessionId=%s\n", sessionID)
		return false, "consentimento indisponível: reporter não configurado"
	}

	reportCtx, reportCancel := context.WithTimeout(context.Background(), 20*time.Second)
	err := m.consentReporter(reportCtx, sessionID, decision)
	reportCancel()
	if err != nil {
		log.Printf("[remote-consent] falha ao reportar decisão sessionId=%s: %v\n", sessionID, err)
		// Sem decisão registrada no servidor não há credenciais para o viewer:
		// fail-closed mesmo quando o usuário autorizou.
		return false, "falha ao reportar consentimento ao servidor"
	}

	if !decision.Accepted {
		reason := strings.TrimSpace(decision.Reason)
		if reason == "" {
			reason = "acesso remoto não autorizado"
		}
		return false, "acesso remoto negado: " + reason
	}

	log.Printf("[remote-consent] autorizado sessionId=%s backend=%s auto=%t\n",
		sessionID, decision.PromptBackend, decision.Auto)
	return true, ""
}
