package remotesession

import (
	"context"
	"strings"
	"time"
)

// ── Consentimento do usuário do desktop (somente screen/all) ──

// ConsentConfig é o bloco "consent" enviado no start pelo servidor.
type ConsentConfig struct {
	Required      bool
	Mode          string // off | notify | prompt
	DeadlineUTC   time.Time
	Timeout       int
	TimeoutAction string // approve | deny
	Title         string
	Message       string
	Operator      string
	PolicySource  string
}

// BannerConfig é o bloco "banner" enviado no start pelo servidor.
type BannerConfig struct {
	Enabled         bool
	Text            string
	Operator        string
	AllowDisconnect bool
}

// ConsentDecision é o resultado do prompt (manual ou timeout).
type ConsentDecision struct {
	Accepted      bool
	Auto          bool
	Outcome       string
	Reason        string
	PromptBackend string
	DecidedAt     time.Time
}

// ConsentRequest é o contrato entre o manager e o renderer de prompt.
type ConsentRequest struct {
	SessionID     string
	Title         string
	Message       string
	Operator      string
	Timeout       time.Duration
	TimeoutAction string
}

// SessionBanner é o banner local exibido durante a sessão de tela.
type SessionBanner interface {
	Close()
}

// BannerOptions descreve o banner a criar.
type BannerOptions struct {
	SessionID       string
	Operator        string
	Text            string
	AllowDisconnect bool
	MonitorIndex    int
	OnDisconnect    func()
}

// ParseConsentConfig lê o bloco consent do payload do start. Retorna nil quando
// o bloco não existe (agente antigo/servidor antigo).
func ParseConsentConfig(payload map[string]any) *ConsentConfig {
	raw, ok := payload["consent"].(map[string]any)
	if !ok || raw == nil {
		return nil
	}

	cfg := &ConsentConfig{
		Required:      toBool(raw["required"]),
		Mode:          strings.ToLower(strings.TrimSpace(toString(raw["mode"]))),
		Timeout:       toInt(raw["timeoutSeconds"], 60),
		TimeoutAction: strings.ToLower(strings.TrimSpace(toString(raw["timeoutAction"]))),
		Title:         strings.TrimSpace(toString(raw["title"])),
		Message:       strings.TrimSpace(toString(raw["message"])),
		Operator:      strings.TrimSpace(toString(raw["operator"])),
		PolicySource:  strings.TrimSpace(toString(raw["policySource"])),
	}
	if cfg.Timeout < 10 {
		cfg.Timeout = 10
	}
	if cfg.Timeout > 600 {
		cfg.Timeout = 600
	}
	if cfg.TimeoutAction == "" {
		cfg.TimeoutAction = "deny"
	}
	if cfg.Mode == "" {
		if cfg.Required {
			cfg.Mode = "prompt"
		} else {
			cfg.Mode = "off"
		}
	}
	if deadline, err := time.Parse(time.RFC3339, toString(raw["deadlineUtc"])); err == nil {
		cfg.DeadlineUTC = deadline.UTC()
	}
	if cfg.DeadlineUTC.IsZero() {
		cfg.DeadlineUTC = time.Now().UTC().Add(time.Duration(cfg.Timeout) * time.Second)
	}
	return cfg
}

// ParseBannerConfig lê o bloco banner do payload.
func ParseBannerConfig(payload map[string]any) *BannerConfig {
	raw, ok := payload["banner"].(map[string]any)
	if !ok || raw == nil {
		return nil
	}
	return &BannerConfig{
		Enabled:         toBool(raw["enabled"]),
		Text:            strings.TrimSpace(toString(raw["text"])),
		Operator:        strings.TrimSpace(toString(raw["operator"])),
		AllowDisconnect: toBool(raw["allowDisconnect"]),
	}
}

// TimeoutDecision aplica a ação configurada quando o prompt expira.
func (c *ConsentConfig) TimeoutDecision(reason string) ConsentDecision {
	accepted := c.TimeoutAction == "approve"
	outcome := "timeout-denied"
	if accepted {
		outcome = "timeout-granted"
	}
	return ConsentDecision{
		Accepted:      accepted,
		Auto:          true,
		Outcome:       outcome,
		Reason:        reason,
		PromptBackend: "timeout",
		DecidedAt:     time.Now().UTC(),
	}
}

func (c *ConsentConfig) PromptRequest(sessionID string) ConsentRequest {
	timeout := time.Until(c.DeadlineUTC)
	if timeout <= 0 {
		timeout = time.Duration(c.Timeout) * time.Second
	}
	return ConsentRequest{
		SessionID:     sessionID,
		Title:         c.Title,
		Message:       c.Message,
		Operator:      c.Operator,
		Timeout:       timeout,
		TimeoutAction: c.TimeoutAction,
	}
}

func toBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	case float64:
		return typed != 0
	default:
		return false
	}
}

// WithTimeoutContext cria o contexto do prompt limitado pelo deadline do
// servidor (+2s de tolerância). Deadline já vencido devolve um contexto
// imediatamente cancelado — nunca abre um prompt fora do prazo.
func (c *ConsentConfig) WithTimeoutContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, c.DeadlineUTC.Add(2*time.Second))
}
