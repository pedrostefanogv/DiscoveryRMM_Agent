package mcp

import (
	"context"
	"fmt"
)

// registerServiceControlTool registra a tool "service_control": list/get via
// sysctrl.ListServices e start/stop/restart via sysctrl (destrutivas).
func registerServiceControlTool(reg *Registry) {
	reg.Register(Tool{
		Name: "service_control",
		Description: "Servicos do Windows (familia action-based). action: " +
			"list (todos os servicos) | get (um servico por nome/displayName) | " +
			"start | stop | restart. start/stop/restart sao DESTRUTIVAS e exigem confirm=true.",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: list, get, start, stop, restart", Required: true},
			{Name: "name", Type: "string", Description: "Nome (ou display name) do servico (get, start, stop, restart)", Required: false},
			{Name: "confirm", Type: "boolean", Description: "Confirmacao explicita do usuario (start, stop, restart)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "list", "get", "start", "stop", "restart")
			if err != nil {
				return nil, err
			}
			if action == "list" {
				return listServicesNative()
			}
			name, err := requiredStringArg(args, "name")
			if err != nil {
				return nil, err
			}
			switch action {
			case "get":
				return getServiceNative(name)
			case "start":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				if err := startServiceNative(name); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true, "name": name, "action": action}, nil
			case "stop":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				if err := stopServiceNative(name); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true, "name": name, "action": action}, nil
			case "restart":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				if err := restartServiceNative(name); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true, "name": name, "action": action}, nil
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}
