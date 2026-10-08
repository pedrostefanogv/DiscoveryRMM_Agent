package ai

// interactiveToolRequiresUser informa se a tool AGUARDA interação do usuário
// (pergunta de autorização exibida no chat). Nesses casos o loop multi-round
// NÃO aplica timeout: o timer de 60s mataria a espera pela resposta e a ação
// nunca seria confirmada (a pergunta ficaria pendurada numa pergunta morta).
// O cancelamento do stream (botão Parar) continua interrompendo a espera.
func interactiveToolRequiresUser(name string) bool {
	switch name {
	case "ask_user", "capture_screenshot", "read_file",
		"export_inventory_markdown", "export_inventory_pdf":
		return true
	default:
		return false
	}
}
