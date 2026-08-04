package app

import (
	"discovery/app/agentcommands"
)

// ── Power Action Commands ──

type powerCommandPayload = agentcommands.PowerCommandPayload

// isPowerActionCommandType checks whether cmdType is a restart/reboot/shutdown command.
func isPowerActionCommandType(cmdType string) bool {
	return agentcommands.IsPowerActionCommandType(cmdType)
}

// parsePowerCommandPayload extracts delaySeconds, force, and message from the raw payload.
func parsePowerCommandPayload(payload any) powerCommandPayload {
	return agentcommands.ParsePowerCommandPayload(payload)
}
