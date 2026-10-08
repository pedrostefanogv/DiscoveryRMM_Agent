package ai

import "testing"

// terminalToolResult: contrato das tools que encerram o turno (power_action
// restart/shutdown). O chat precisa fechar a conversa na hora, porque a maquina
// sera reiniciada/desligada e nao havera outro round do LLM.
func TestTerminalToolResult(t *testing.T) {
	const fallback = "Acao agendada — a conversa foi encerrada. A maquina sera reiniciada/desligada."
	cases := []struct {
		name         string
		in           string
		wantMsg      string
		wantTerminal bool
	}{
		{"terminal com mensagem", `{"ok":true,"action":"restart","outcome":"notification_shown","terminal":true,"message":"Aviso exibido — encerre a conversa."}`, "Aviso exibido — encerre a conversa.", true},
		{"terminal sem mensagem usa fallback", `{"terminal":true}`, fallback, true},
		{"terminal false nao encerra", `{"ok":true,"terminal":false,"message":"x"}`, "", false},
		{"sem o campo nao encerra", `{"ok":true,"message":"x"}`, "", false},
		{"texto puro nao encerra", `Ações executadas.`, "", false},
		{"json malformado nao encerra", `{"terminal":tru`, "", false},
		{"vazio nao encerra", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, terminal := terminalToolResult(tc.in)
			if terminal != tc.wantTerminal {
				t.Fatalf("terminal=%v, esperado %v (in=%q)", terminal, tc.wantTerminal, tc.in)
			}
			if msg != tc.wantMsg {
				t.Fatalf("msg=%q, esperado %q", msg, tc.wantMsg)
			}
		})
	}
}
