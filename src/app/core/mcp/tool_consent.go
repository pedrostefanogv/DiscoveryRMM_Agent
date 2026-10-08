package mcp

import (
	"fmt"
	"strings"

	"discovery/app/core/consent"
)

// exportDestinationHint descreve o destino da exportação para o usuário
// decidir com contexto. A ordem REAL é a de exportDirCandidates (pastas
// graváveis pelo usuário primeiro); o caminho exato só é conhecido depois da
// aprovação. O texto precisa acompanhar essa ordem — antes ele dizia
// "C:\Program Files\..." e ficou obsoleto quando a pasta do usuário passou a
// ser a primeira opção (o pedido de consentimento informava um destino errado).
const exportDestinationHint = `Pasta de exportacao do agente (LocalAppData\Discovery\Exports ou Documentos\DiscoveryExports; fallback: pasta do executavel)`

// ToolConsentFor devolve o pedido de autorização do USUÁRIO para a tool/ação,
// ou nil quando a tool é de leitura/diagnóstico e não exige consentimento.
//
// A decisão é por AÇÃO: "service_control list" é livre, "service_control stop"
// exige autorização. Isso substitui a confiança no parâmetro confirm=true, que
// era preenchido pelo PRÓPRIO LLM — a confirmação passa a vir sempre do
// usuário, no chat (o gate fica em services/chat.mcpExecuteForChat).
//
// read_file NÃO entra aqui: a autorização de leitura tem fluxo próprio
// (App.ReadFileWithConsent), com destaque para caminhos sensíveis.
func ToolConsentFor(toolName string, args map[string]any) *consent.Request {
	if args == nil {
		args = map[string]any{}
	}

	switch toolName {
	case "export_inventory_markdown":
		return &consent.Request{
			Kind:   consent.KindWrite,
			Action: "write_file",
			Target: "relatorio de inventario completo em Markdown (.md)",
			Extra:  []string{exportDestinationHint},
		}
	case "export_inventory_pdf":
		return &consent.Request{
			Kind:   consent.KindWrite,
			Action: "write_file",
			Target: "relatorio de inventario completo em PDF (.pdf)",
			Extra:  []string{exportDestinationHint},
		}
	case "install_package":
		return destructive("install_package", optionalStringArg(args, "id"))
	case "uninstall_package":
		return destructive("uninstall_package", optionalStringArg(args, "id"))
	case "upgrade_package":
		return destructive("upgrade_package", optionalStringArg(args, "id"))
	case "upgrade_all_packages":
		return destructive("upgrade_all_packages", "")
	case "service_control":
		switch actionArg(args) {
		case "start":
			return destructive("service_start", optionalStringArg(args, "name"))
		case "stop":
			return destructive("service_stop", optionalStringArg(args, "name"))
		case "restart":
			return destructive("service_restart", optionalStringArg(args, "name"))
		}
	case "scheduled_task":
		if actionArg(args) == "run" {
			target := optionalStringArg(args, "taskName")
			if p := optionalStringArg(args, "taskPath"); p != "" {
				target = target + " " + p
			}
			return destructive("run_scheduled_task", target)
		}
	case "process_control":
		if actionArg(args) == "kill" {
			pid := optionalIntArg(args, "pid")
			target := ""
			if pid > 0 {
				target = fmt.Sprintf("PID %d", pid)
			}
			return destructive("kill_process", target)
		}
	case "printer":
		switch actionArg(args) {
		case "install":
			// Instalar impressora altera drivers/portas da máquina — não tem
			// confirm=true no handler, então dependia só da boa vontade do LLM.
			return destructive("printer_install", optionalStringArg(args, "name"))
		case "install_shared":
			return destructive("printer_install_shared", optionalStringArg(args, "connectionPath"))
		case "remove":
			return destructive("printer_remove", optionalStringArg(args, "name"))
		case "cancel_job":
			target := optionalStringArg(args, "name")
			if jobID := optionalIntArg(args, "jobId"); jobID > 0 {
				target = fmt.Sprintf("%s (job %d)", target, jobID)
			}
			return destructive("printer_cancel_job", target)
		case "restart_spooler":
			return destructive("printer_restart_spooler", "")
		case "clear_queue":
			return destructive("printer_clear_queue", optionalStringArg(args, "name"))
		}
	case "power_action":
		switch actionArg(args) {
		case "restart":
			return destructive("power_restart", "", powerActionExtra(args)...)
		case "shutdown":
			return destructive("power_shutdown", "", powerActionExtra(args)...)
		case "lock":
			return destructive("power_lock", "")
		}
	}

	return nil
}

// destructive monta um pedido de autorização para ação destrutiva.
func destructive(action, target string, extra ...string) *consent.Request {
	return &consent.Request{Kind: consent.KindDestructive, Action: action, Target: target, Extra: extra}
}

// actionArg normaliza o parâmetro action das tools de família.
func actionArg(args map[string]any) string {
	action, _ := args["action"].(string)
	return strings.ToLower(strings.TrimSpace(action))
}

// powerActionExtra monta as linhas de contexto do restart/shutdown (atraso e
// mensagem exibidos no aviso ao usuário).
func powerActionExtra(args map[string]any) []string {
	var extra []string
	if delay := optionalIntArg(args, "delaySeconds"); delay > 0 {
		extra = append(extra, fmt.Sprintf("Atraso: %ds (o usuario pode cancelar)", delay))
	}
	if msg := optionalStringArg(args, "message"); msg != "" {
		extra = append(extra, "Aviso: "+msg)
	}
	return extra
}
