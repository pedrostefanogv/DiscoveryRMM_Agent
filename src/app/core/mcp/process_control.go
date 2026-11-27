package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// registerProcessControlTool registra a tool "process_control": list, top (reusa
// a logica de performance.go) e kill (destrutiva, exige confirm=true).
func registerProcessControlTool(reg *Registry) {
	reg.Register(Tool{
		Name: "process_control",
		Description: "Controla processos no Windows (familia action-based). action: " +
			"list (processos, com filtro opcional query/limit) | top (N processos por CPU ou memoria) | " +
			"kill (encerra pelo PID; DESTRUTIVA: exige autorizacao do usuario no chat).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao a executar", Required: true, Enum: []string{"list", "top", "kill"}},
			{Name: "top", Type: "integer", Description: "Numero de processos no 'top' (1-50, padrao 10)", Required: false},
			{Name: "orderBy", Type: "string", Description: "'top': criterio de ordenacao", Required: false, Enum: []string{"cpu", "memory"}, Default: "cpu"},
			{Name: "pid", Type: "integer", Description: "PID do processo a encerrar (kill)", Required: false},
			{Name: "query", Type: "string", Description: "Filtro opcional por nome em 'list'", Required: false},
			{Name: "limit", Type: "integer", Description: "Limite de processos em 'list' (padrao 100)", Required: false, Default: 100},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "list", "top", "kill")
			if err != nil {
				return nil, err
			}
			switch action {
			case "list":
				raw, err := listProcessesNative()
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
						if !strings.Contains(strings.ToLower(nameStr), q) {
							continue
						}
					}
					filtered = append(filtered, item)
					if len(filtered) >= limit {
						break
					}
				}
				return filtered, nil
			case "top":
				return GetTopProcesses(ctx, optionalIntArg(args, "top"), optionalStringArg(args, "orderBy"))
			case "kill":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				pid, err := requiredIntArg(args, "pid")
				if err != nil {
					return nil, err
				}
				if err := killProcessNative(uint32(pid)); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true, "pid": pid}, nil
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}
