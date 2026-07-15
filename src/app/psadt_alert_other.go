//go:build !windows

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// isPsadtAlertCommandType checks command aliases for PSADT alerts.
func isPsadtAlertCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "9", "showpsadtalert", "show_psadt_alert", "psadt_alert", "psadtalert":
		return true
	default:
		return false
	}
}

// parsePsadtAlertPayload parses the ExecuteCommand payload into PsadtAlertPayload.
func parsePsadtAlertPayload(payload any) (PsadtAlertPayload, error) {
	if payload == nil {
		return PsadtAlertPayload{}, fmt.Errorf("payload ausente")
	}

	var raw []byte
	switch typed := payload.(type) {
	case string:
		raw = []byte(typed)
	default:
		var err error
		raw, err = json.Marshal(typed)
		if err != nil {
			return PsadtAlertPayload{}, fmt.Errorf("falha ao serializar payload psadt-alert: %w", err)
		}
	}

	var p PsadtAlertPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return PsadtAlertPayload{}, fmt.Errorf("payload psadt-alert invalido: %w", err)
	}

	p.Type = strings.ToLower(strings.TrimSpace(p.Type))
	p.Icon = normalizePsadtAlertIcon(p.Icon)
	if p.Type == "" {
		p.Type = "toast"
	}
	if p.TimeoutSeconds <= 0 {
		if p.Type == "toast" {
			p.TimeoutSeconds = 15
		} else {
			p.TimeoutSeconds = 120
		}
	}

	return p, nil
}

func normalizePsadtAlertIcon(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "warning", "warn":
		return "Warning"
	case "error", "stop":
		return "Error"
	case "success", "info", "information":
		return "Information"
	case "question":
		return "Question"
	default:
		return "Information"
	}
}

func (a *App) handlePsadtAlert(_ context.Context, p PsadtAlertPayload) (int, string, string) {
	body, _ := json.Marshal(map[string]string{"action": "skipped_non_windows"})
	if a != nil {
		a.logs.append("[agent] psadt-alert ignorado: não é windows type=" + p.Type + " alertId=" + p.AlertID)
	}
	return 0, string(body), ""
}

func (a *App) showPowerActionWarning(_ context.Context, _ string, _ int, _ bool, _ string) (string, error) {
	return "proceed", nil
}

func (a *App) executeSystemPowerAction(_ context.Context, action string, _ int, _ bool, _ string) (int, string, string) {
	return 1, "", fmt.Sprintf("acao %s nao suportada neste sistema operacional", strings.TrimSpace(action))
}
