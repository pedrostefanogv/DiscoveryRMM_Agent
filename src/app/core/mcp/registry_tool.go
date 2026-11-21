package mcp

import (
	"context"
	"fmt"

	"discovery/app/core/winregistry"
)

// registerRegistryReadTool registra a tool somente leitura "registry_read",
// protegida pelo guard fail-closed de core/winregistry (bloqueia SAM,
// SECURITY, secrets e caminhos sensiveis).
func registerRegistryReadTool(reg *Registry) {
	reg.Register(Tool{
		Name: "registry_read",
		Description: "Leitura segura (somente leitura) do Registro do Windows via guard fail-closed. action: " +
			"list (lista subchaves paginadas e valores de uma chave) | " +
			"get_value (le um valor especifico da chave). " +
			"Hives aceitos: HKLM, HKCU, HKCR, HKU, HKCC.",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: list, get_value", Required: true},
			{Name: "hive", Type: "string", Description: "Hive raiz: HKLM, HKCU, HKCR, HKU, HKCC", Required: true},
			{Name: "path", Type: "string", Description: "Subcaminho da chave (ex.: SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run)", Required: true},
			{Name: "valueName", Type: "string", Description: "Nome do valor (obrigatorio em get_value; use '' ou '(Default)' para o valor padrao)", Required: false},
			{Name: "token", Type: "string", Description: "Token de paginacao retornado por list (opcional)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			action, err := requireAction(args, "list", "get_value")
			if err != nil {
				return nil, err
			}
			hiveRaw, err := requiredStringArg(args, "hive")
			if err != nil {
				return nil, err
			}
			hive, ok := winregistry.ParseHive(hiveRaw)
			if !ok {
				return nil, fmt.Errorf("hive invalido %q: use HKLM, HKCU, HKCR, HKU ou HKCC", hiveRaw)
			}
			keyPath, err := requiredStringArg(args, "path")
			if err != nil {
				return nil, err
			}

			switch action {
			case "list":
				token := optionalStringArg(args, "token")
				return winregistry.ListKey(hive, keyPath, token)
			case "get_value":
				valueName, hasVal := args["valueName"]
				if !hasVal {
					return nil, fmt.Errorf("parametro obrigatorio 'valueName' ausente para get_value")
				}
				nameStr, _ := valueName.(string)
				return winregistry.GetValue(hive, keyPath, nameStr)
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}
