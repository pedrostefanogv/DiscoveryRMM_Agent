package app

import (
	"discovery/app/agentcommands"
)

// ── Remote Session Helpers ──

func isRemoteSessionCommandType(cmdType string) bool {
	return agentcommands.IsRemoteSessionCommandType(cmdType)
}

func parseAnyMap(v any) map[string]any {
	return agentcommands.ParseAnyMap(v)
}
