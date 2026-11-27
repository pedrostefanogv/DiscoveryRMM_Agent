package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func registerSoftwareTool(reg *Registry, app AppBridge) {
	reg.Register(Tool{
		Name: "software",
		Description: "Gerenciamento de aplicativos e pacotes winget (familia action-based). action: " +
			"search (query) | list_installed | list_start_menu (query, limit) | pending_updates | actions | " +
			"install (id; destrutiva) | upgrade (id; destrutiva) | upgrade_all (destrutiva) | uninstall (id; destrutiva).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao a executar", Required: true, Enum: []string{"search", "list_installed", "list_start_menu", "pending_updates", "actions", "install", "upgrade", "upgrade_all", "uninstall"}},
			{Name: "id", Type: "string", Description: "ID do pacote winget (install, upgrade, uninstall)", Required: false},
			{Name: "query", Type: "string", Description: "Termo de busca (search, list_start_menu)", Required: false},
			{Name: "limit", Type: "integer", Description: "Limite de resultados em list_start_menu", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args,
				"search", "list_installed", "list_start_menu", "pending_updates", "actions",
				"install", "upgrade", "upgrade_all", "uninstall")
			if err != nil {
				return nil, err
			}
			switch action {
			case "search":
				q, _ := args["query"].(string)
				if strings.TrimSpace(q) == "" {
					return nil, fmt.Errorf("query nao pode ser vazia")
				}
				return app.SearchCatalog(q)
			case "list_installed":
				out, err := app.ListInstalled()
				return map[string]string{"output": out}, err
			case "list_start_menu":
				return app.ListInstalledApps(optionalStringArg(args, "query"), optionalIntArg(args, "limit"))
			case "pending_updates":
				return app.GetPendingUpdatesJSON()
			case "actions":
				return app.GetPackageActionsJSON()
			case "install":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				id, _ := args["id"].(string)
				if strings.TrimSpace(id) == "" {
					return nil, fmt.Errorf("id do pacote nao pode ser vazio")
				}
				out, err := app.InstallPackage(id)
				return map[string]string{"output": out}, err
			case "upgrade":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				id, _ := args["id"].(string)
				if strings.TrimSpace(id) == "" {
					return nil, fmt.Errorf("id do pacote nao pode ser vazio")
				}
				out, err := app.UpgradePackage(id)
				return map[string]string{"output": out}, err
			case "upgrade_all":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				out, err := app.UpgradeAllPackages()
				return map[string]string{"output": out}, err
			case "uninstall":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				id, _ := args["id"].(string)
				if strings.TrimSpace(id) == "" {
					return nil, fmt.Errorf("id do pacote nao pode ser vazio")
				}
				out, err := app.UninstallPackage(id)
				return map[string]string{"output": out}, err
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}

func registerEventLogTool(reg *Registry) {
	reg.Register(Tool{
		Name: "event_log",
		Description: "Consulta o Windows Event Log (familia action-based). action: " +
			"query (logName: System|Application|Setup, level, source, maxEvents, lastHours) | " +
			"recent_errors (lastHours, padrao 24) | recent_crashes (lastHours, padrao 168).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao a executar", Required: true, Enum: []string{"query", "recent_errors", "recent_crashes"}},
			{Name: "logName", Type: "string", Description: "Nome do log (query)", Required: false, Enum: []string{"System", "Application", "Setup"}, Default: "System"},
			{Name: "level", Type: "string", Description: "Nivel do evento (query)", Required: false, Enum: []string{"Critical", "Error", "Warning", "Information"}},
			{Name: "source", Type: "string", Description: "Fonte/Provider (query)", Required: false},
			{Name: "maxEvents", Type: "integer", Description: "Maximo de eventos (query, padrao 50, max 100)", Required: false},
			{Name: "lastHours", Type: "integer", Description: "Janela de tempo em horas (max 168)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "query", "recent_errors", "recent_crashes")
			if err != nil {
				return nil, err
			}
			switch action {
			case "query":
				return QueryEventLog(
					ctx,
					optionalStringArg(args, "logName"),
					optionalStringArg(args, "level"),
					optionalStringArg(args, "source"),
					optionalIntArg(args, "maxEvents"),
					optionalIntArg(args, "lastHours"),
				)
			case "recent_errors":
				return GetRecentErrors(ctx, optionalIntArg(args, "lastHours"))
			case "recent_crashes":
				return GetRecentCrashes(ctx, optionalIntArg(args, "lastHours"))
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}

func registerLocalMemoryTool(reg *Registry, app AppBridge) {
	reg.Register(Tool{
		Name: "local_memory",
		Description: "Anotacoes locais persistentes neste computador (diferente de memory.search do servidor). action: " +
			"list | create (content; nunca grave senhas/tokens) | delete (id).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: list, create, delete", Required: true},
			{Name: "content", Type: "string", Description: "Conteudo da anotacao (create)", Required: false},
			{Name: "id", Type: "integer", Description: "ID da anotacao (delete)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "list", "create", "delete")
			if err != nil {
				return nil, err
			}
			switch action {
			case "list":
				return app.GetLocalMemories()
			case "create":
				content, err := requiredStringArg(args, "content")
				if err != nil {
					return nil, err
				}
				return app.AddLocalMemory(content)
			case "delete":
				id, err := requiredIntArg(args, "id")
				if err != nil {
					return nil, err
				}
				return map[string]bool{"ok": true}, app.DeleteLocalMemory(int64(id))
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}

func registerScreenTool(reg *Registry, app AppBridge) {
	reg.Register(Tool{
		Name: "screen",
		Description: "Captura de tela e janelas visiveis (familia action-based). action: " +
			"capture (mode: full|window|focused|monitor|region|interactive, reason, windowHandle, monitor, x, y, width, height, quality, maxDimension) | " +
			"list_windows (includeUntitled) | permission_status.",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: capture, list_windows, permission_status", Required: true},
			{Name: "mode", Type: "string", Description: "Modo de captura (capture): full, window, focused, monitor, region, interactive", Required: false},
			{Name: "windowHandle", Type: "integer", Description: "Handle da janela (capture mode=window)", Required: false},
			{Name: "monitor", Type: "integer", Description: "Indice do monitor (capture mode=monitor)", Required: false},
			{Name: "x", Type: "integer", Description: "X (capture mode=region)", Required: false},
			{Name: "y", Type: "integer", Description: "Y (capture mode=region)", Required: false},
			{Name: "width", Type: "integer", Description: "Largura (capture mode=region)", Required: false},
			{Name: "height", Type: "integer", Description: "Altura (capture mode=region)", Required: false},
			{Name: "reason", Type: "string", Description: "Motivo da captura", Required: false},
			{Name: "quality", Type: "integer", Description: "Qualidade 1-100", Required: false},
			{Name: "maxDimension", Type: "integer", Description: "Dimensao maxima (200-3840)", Required: false},
			{Name: "includeUntitled", Type: "boolean", Description: "Incluir janelas sem titulo (list_windows)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "capture", "list_windows", "permission_status")
			if err != nil {
				return nil, err
			}
			switch action {
			case "list_windows":
				includeUntitled := false
				switch v := args["includeUntitled"].(type) {
				case bool:
					includeUntitled = v
				case string:
					switch strings.ToLower(strings.TrimSpace(v)) {
					case "true", "1", "yes", "sim", "on":
						includeUntitled = true
					}
				case float64:
					includeUntitled = v != 0
				}
				return app.ListOpenWindowsJSON(includeUntitled)
			case "permission_status":
				return app.ScreenshotConsentStatusJSON()
			case "capture":
				raw, err := app.CaptureScreenshotForTool(ctx, args)
				if err != nil {
					return nil, err
				}
				var payload map[string]any
				if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
					return raw, nil
				}
				b64, _ := payload["image_base64"].(string)
				mime, _ := payload["mime"].(string)
				if strings.TrimSpace(b64) != "" && strings.TrimSpace(mime) != "" {
					if ref := cacheScreenshotImage("data:" + mime + ";base64," + b64); ref != "" {
						payload["image_ref"] = ref
					}
				}
				return payload, nil
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}
