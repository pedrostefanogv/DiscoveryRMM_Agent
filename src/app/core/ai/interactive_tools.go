package ai

import (
	"encoding/json"
	"strings"

	"discovery/app/core/mcp"
)

// alwaysInteractiveTools são tools que SEMPRE aguardam interação do usuário no
// chat, independente dos argumentos (pergunta própria, não a de consentimento).
var alwaysInteractiveTools = map[string]bool{
	"ask_user":           true,
	"capture_screenshot": true,
	"read_file":          true,
}

// interactiveToolRequiresUser informa se a tool AGUARDA interação do usuário
// (por exemplo, a pergunta de autorização exibida no chat). Nesses casos o loop
// multi-round NÃO aplica timeout: o timer de 60s mataria a espera pela resposta
// e a ação nunca seria confirmada (a pergunta ficaria pendurada numa pergunta
// morta). O cancelamento do stream (botão Parar) continua interrompendo.
//
// A fonte de verdade das tools com efeito no computador é mcp.ToolConsentFor —
// a MESMA política usada pelo gate em services/chat. Assim não existe uma
// segunda lista que possa divergir quando uma tool nova é adicionada.
func interactiveToolRequiresUser(name, argsJSON string) bool {
	if alwaysInteractiveTools[name] {
		return true
	}

	args := map[string]any{}
	if strings.TrimSpace(argsJSON) != "" {
		_ = json.Unmarshal([]byte(argsJSON), &args)
	}
	return mcp.ToolConsentFor(name, args) != nil
}
