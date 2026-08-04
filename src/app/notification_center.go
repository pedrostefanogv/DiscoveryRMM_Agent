package app

import "discovery/app/services/notifications"

// NotificationDispatchRequest é o payload de uma notificação.
type NotificationDispatchRequest = notifications.DispatchRequest

// NotificationDispatchResponse é a resposta de uma notificação.
type NotificationDispatchResponse = notifications.DispatchResponse

// mapNotificationPolicies converte as políticas de notificação do app para o service.
func mapNotificationPolicies(policies []AgentNotificationPolicy) []notifications.AgentNotificationPolicy {
	result := make([]notifications.AgentNotificationPolicy, 0, len(policies))
	for _, p := range policies {
		actions := make([]notifications.AgentNotificationAction, 0, len(p.Actions))
		for _, a := range p.Actions {
			actions = append(actions, notifications.AgentNotificationAction{
				ID:         a.ID,
				Label:      a.Label,
				ActionType: a.ActionType,
			})
		}
		result = append(result, notifications.AgentNotificationPolicy{
			EventType:      p.EventType,
			Mode:           p.Mode,
			Severity:       p.Severity,
			TimeoutSeconds: p.TimeoutSeconds,
			Actions:        actions,
			StyleOverride: notifications.AgentNotificationStyleOverride{
				Layout:     p.StyleOverride.Layout,
				Background: p.StyleOverride.Background,
				Text:       p.StyleOverride.Text,
			},
		})
	}
	return result
}

// DispatchNotification processa e despacha uma notificação.
func (a *App) DispatchNotification(req NotificationDispatchRequest) NotificationDispatchResponse {
	if a == nil || a.notificationSvc == nil {
		return NotificationDispatchResponse{Accepted: false, Message: "serviço de notificações indisponível"}
	}
	return a.notificationSvc.Dispatch(req)
}

// RespondToNotification processa a resposta do usuário a uma notificação.
func (a *App) RespondToNotification(notificationID, result string) bool {
	if a == nil || a.notificationSvc == nil {
		return false
	}
	return a.notificationSvc.Respond(notificationID, result)
}
