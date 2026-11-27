package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// registerServiceControlTool registra a tool "service_control": list/get via
// sysctrl.ListServices e start/stop/restart via sysctrl (destrutivas).
func registerServiceControlTool(reg *Registry) {
	reg.Register(Tool{
		Name: "service_control",
		Description: "Servicos do Windows (familia action-based). action: " +
			"list (servicos, com filtro opcional query/limit) | get (um servico por nome/displayName) | " +
			"start | stop | restart. start/stop/restart sao DESTRUTIVAS e exigem autorizacao do usuario no chat.",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao a executar", Required: true, Enum: []string{"list", "get", "start", "stop", "restart"}},
			{Name: "name", Type: "string", Description: "Nome (ou display name) do servico (get, start, stop, restart)", Required: false},
			{Name: "query", Type: "string", Description: "Filtro opcional por nome/displayName em 'list'", Required: false},
			{Name: "limit", Type: "integer", Description: "Limite de servicos em 'list' (padrao 100)", Required: false, Default: 100},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "list", "get", "start", "stop", "restart")
			if err != nil {
				return nil, err
			}
			if action == "list" {
				raw, err := listServicesNative()
				if err != nil {
					return nil, err
				}
				q := strings.ToLower(optionalStringArg(args, "query"))
				limit := optionalIntArg(args, "limit")
				if limit <= 0 {
					limit = 100
				}
				var items []map[string]any
				if json.Unmarshal(raw, &items) != nil {
					return raw, nil
				}
				filtered := make([]map[string]any, 0, min(limit, len(items)))
				for _, item := range items {
					if q != "" {
						nameStr, _ := item["name"].(string)
						dispStr, _ := item["displayName"].(string)
						if !strings.Contains(strings.ToLower(nameStr), q) && !strings.Contains(strings.ToLower(dispStr), q) {
							continue
						}
					}
					filtered = append(filtered, item)
					if len(filtered) >= limit {
						break
					}
				}
				return filtered, nil
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
