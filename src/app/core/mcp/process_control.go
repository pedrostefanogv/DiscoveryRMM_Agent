package mcp

import (
	"context"
	"fmt"
)

// registerProcessControlTool registra a tool "process_control": list, top (reusa
// a logica de performance.go) e kill (destrutiva, exige confirm=true).
func registerProcessControlTool(reg *Registry) {
	reg.Register(Tool{
		Name: "process_control",
		Description: "Controla processos no Windows (familia action-based). action: " +
			"list (todos os processos) | top (N processos por CPU ou memoria) | " +
			"kill (encerra pelo PID; DESTRUTIVA: exige confirm=true).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: list, top, kill", Required: true},
			{Name: "top", Type: "integer", Description: "Numero de processos no 'top' (1-50, padrao 10)", Required: false},
			{Name: "orderBy", Type: "string", Description: "'top': cpu ou memory (padrao cpu)", Required: false},
			{Name: "pid", Type: "integer", Description: "PID do processo a encerrar (kill)", Required: false},
			{Name: "confirm", Type: "boolean", Description: "Confirmacao explicita do usuario (kill)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "list", "top", "kill")
			if err != nil {
				return nil, err
			}
			switch action {
			case "list":
				return listProcessesNative()
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
