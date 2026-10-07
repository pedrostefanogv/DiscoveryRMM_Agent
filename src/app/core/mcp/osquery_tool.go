package mcp

import (
	"context"
	"fmt"

	"discovery/app/core/inventory"
)

// registerOsqueryTool registra a tool "osquery": status e query read-only.
// A execucao da query vai pelo AppBridge (que usa o cliente osquery-go quando
// ha socket e cai para o binario osqueryi).
func registerOsqueryTool(reg *Registry, app AppBridge) {
	reg.Register(Tool{
		Name: "osquery",
		Description: "osquery (familia action-based). action: " +
			"status (verifica instalacao e caminho do binario) | " +
			"query (consulta SQL READ-ONLY: precisa comecar com SELECT ou WITH; uma unica instrucao; " +
			"maximo 200 linhas; tabelas/funcoes que leem arquivos ou acessam a rede - read_file, curl, wget - sao BLOQUEADAS).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: status, query", Required: true},
			{Name: "sql", Type: "string", Description: "Consulta SQL read-only (query) — deve comecar com SELECT ou WITH", Required: false},
			{Name: "limit", Type: "integer", Description: "Maximo de linhas retornadas (padrao 200, max 200)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "status", "query")
			if err != nil {
				return nil, err
			}
			switch action {
			case "status":
				return app.GetOsqueryStatusJSON()
			case "query":
				sql, err := requiredStringArg(args, "sql")
				if err != nil {
					return nil, err
				}
				// Gate de seguranca ANTES de tocar no osquery: bloqueia consultas
				// que leriam arquivos ou acessariam a rede (read_file/curl/...).
				if err := inventory.ValidateReadOnlyOsqueryQuery(sql); err != nil {
					return nil, err
				}
				limit := optionalIntArg(args, "limit")
				if limit <= 0 || limit > 200 {
					limit = 200
				}
				return app.RunOsqueryQueryJSON(sql, limit)
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}
