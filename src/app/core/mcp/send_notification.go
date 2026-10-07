package mcp

import (
	"context"
	"fmt"
	"strings"
)

// registerSendNotificationTool registra a tool "send_notification", que usa o
// servico de notificacoes do agente (toast/centro de notificacoes).
func registerSendNotificationTool(reg *Registry, app AppBridge) {
	reg.Register(Tool{
		Name: "send_notification",
		Description: "Exibe uma notificacao (toast) para o usuario deste computador. " +
			"title e message sao obrigatorios; level: info | warning | error (padrao info).",
		Params: []ToolParam{
			{Name: "title", Type: "string", Description: "Titulo da notificacao", Required: true},
			{Name: "message", Type: "string", Description: "Mensagem da notificacao", Required: true},
			{Name: "level", Type: "string", Description: "Nivel: info, warning ou error (padrao info)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			title, err := requiredStringArg(args, "title")
			if err != nil {
				return nil, err
			}
			message, err := requiredStringArg(args, "message")
			if err != nil {
				return nil, err
			}
			level := strings.ToLower(optionalStringArg(args, "level"))
			switch level {
			case "", "info", "informativo", "low":
				level = "info"
			case "warning", "warn", "alerta", "medium":
				level = "warning"
			case "error", "erro", "high", "critical":
				level = "error"
			default:
				return nil, fmt.Errorf("level invalido: %q — valores aceitos: info, warning, error", level)
			}
			return app.SendAgentNotification(title, message, level)
		},
	})
}
