package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ── Agent Update Commands ──

func isAgentUpdateCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "10", "update", "agentupdate", "selfupdate", "self-update":
		return true
	default:
		return false
	}
}

func parseAgentUpdatePayload(payload any) (string, string, string) {
	if payload == nil {
		return "check-update", "", ""
	}
	switch typed := payload.(type) {
	case string:
		action := strings.ToLower(strings.TrimSpace(typed))
		if action == "" {
			return "check-update", "", ""
		}
		return action, "", ""
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "", "", fmt.Sprintf("falha ao serializar payload de update: %v", err)
		}
		var parsed struct {
			Action  string `json:"action"`
			URL     string `json:"url"`     // URL direta do instalador (opcional)
			Version string `json:"version"` // versão alvo (opcional)
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", "", fmt.Sprintf("payload de update invalido: %v", err)
		}
		action := strings.ToLower(strings.TrimSpace(parsed.Action))
		if action == "" {
			action = "check-update"
		}
		return action, strings.TrimSpace(parsed.URL), strings.TrimSpace(parsed.Version)
	}
}

// agentUpdateCommand representa o payload completo de um comando de update
// enviado pelo servidor. Usado para extrair url/version para install direto.
// Version e URL usam *string para tolerar null vindo do servidor.
type agentUpdateCommand struct {
	Action  string  `json:"action"`
	Version *string `json:"version"`
	URL     *string `json:"url"`
}

func (c agentUpdateCommand) version() string {
	if c.Version != nil {
		return strings.TrimSpace(*c.Version)
	}
	return ""
}

func (c agentUpdateCommand) downloadURL() string {
	if c.URL != nil {
		return strings.TrimSpace(*c.URL)
	}
	return ""
}

func parseAgentUpdateCommand(payload any) (agentUpdateCommand, error) {
	if payload == nil {
		return agentUpdateCommand{Action: "check-update"}, nil
	}
	switch typed := payload.(type) {
	case string:
		var cmd agentUpdateCommand
		if err := json.Unmarshal([]byte(typed), &cmd); err != nil {
			return agentUpdateCommand{Action: "check-update"}, nil
		}
		cmd.Action = strings.ToLower(strings.TrimSpace(cmd.Action))
		if cmd.Action == "" {
			cmd.Action = "check-update"
		}
		return cmd, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return agentUpdateCommand{}, fmt.Errorf("falha ao serializar payload de update: %w", err)
		}
		var cmd agentUpdateCommand
		if err := json.Unmarshal(raw, &cmd); err != nil {
			return agentUpdateCommand{}, fmt.Errorf("payload de update invalido: %w", err)
		}
		cmd.Action = strings.ToLower(strings.TrimSpace(cmd.Action))
		if cmd.Action == "" {
			cmd.Action = "check-update"
		}
		return cmd, nil
	}
}

func (a *App) requestAgentUpdateCheck(_ context.Context, source string) error {
	if a == nil {
		return fmt.Errorf("app indisponível")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "manual"
	}

	if a.selfUpdater != nil {
		select {
		case a.selfUpdaterCh <- false:
			a.logs.append("[selfupdate] check disparado via InvalidateCh: source=" + source)
			return nil
		default:
			a.logs.append("[selfupdate] InvalidateCh cheio, ignorando duplicado: source=" + source)
			return nil
		}
	}

	// Fallback para o legacy updateTrigger
	select {
	case a.updateTrigger <- struct{}{}:
		a.logs.append("[update] force-check de update disparado localmente: source=" + source)
		return nil
	default:
		return fmt.Errorf("update check ja pendente")
	}
}
