package mcp

import "strings"

// IsUserSessionTool informa se a tool/ação depende da sessão interativa do
// usuário logado (desktop, explorer, drives mapeados, apps do usuário) e
// portanto NÃO deve rodar em Session 0 (NT AUTHORITY\SYSTEM) quando o agente
// opera como serviço + UI companion.
func IsUserSessionTool(toolName string, args map[string]any) bool {
	name := strings.ToLower(strings.TrimSpace(toolName))
	action := actionArg(args)

	switch name {
	case "open_folder", "open_app", "list_installed_apps", "list_open_windows", "capture_screenshot":
		return true
	case "screen":
		return action == "capture" || action == "list_windows"
	case "software":
		return action == "list_start_menu"
	case "power_action":
		return action == "lock"
	case "shares":
		return action == "mapped_drives"
	default:
		return false
	}
}
