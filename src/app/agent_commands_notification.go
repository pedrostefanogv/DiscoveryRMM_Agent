package app

import (
	"discovery/app/agentcommands"
)

// ── Notification Dispatch Commands ──

func isNotificationDispatchCommandType(cmdType string) bool {
	return agentcommands.IsNotificationDispatchCommandType(cmdType)
}

func parseNotificationDispatchPayload(payload any) (NotificationDispatchRequest, error) {
	return agentcommands.ParseNotificationDispatchPayload(payload)
}
