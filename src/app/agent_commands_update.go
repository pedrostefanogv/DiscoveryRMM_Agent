package app

import (
	"context"
	"fmt"
	"strings"

	"discovery/app/agentcommands"
)

// ── Agent Update Commands ──

func isAgentUpdateCommandType(cmdType string) bool {
	return agentcommands.IsAgentUpdateCommandType(cmdType)
}

func parseAgentUpdatePayload(payload any) (string, string, string) {
	return agentcommands.ParseAgentUpdatePayload(payload)
}

// agentUpdateCommand representa o payload completo de um comando de update.
type agentUpdateCommand = agentcommands.UpdateCommand

func parseAgentUpdateCommand(payload any) (agentUpdateCommand, error) {
	return agentcommands.ParseAgentUpdateCommand(payload)
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
