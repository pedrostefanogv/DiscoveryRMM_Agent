package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// writeConsentApproveLabel é o rótulo EXATO do botão de autorização de gravação.
// Qualquer outra resposta (inclusive texto livre) conta como NEGATIVA —
// fail-closed, mesmo padrão de readFileApproveLabel (read_file).
const writeConsentApproveLabel = "Autorizar gravação"

// RequestFileWriteConsent implementa AppBridge: pede AUTORIZAÇÃO do usuário
// antes de a IA GRAVAR um arquivo no disco, em CADA gravação (não existe
// "permitir sempre", igual a read_file e capture_screenshot).
//
// Motivação (bug relatado): as tools de exportação do inventário gravavam o
// arquivo direto em C:\Program Files\Discovery\DiscoveryExports sem nenhuma
// confirmação — a IA escrevia no disco do usuário só porque o LLM decidiu
// chamar a tool. Sem aprovação explícita nada é gravado; o handler devolve
// {"approved":false} para o LLM informar o usuário em vez de repetir.
func (a *App) RequestFileWriteConsent(ctx context.Context, description, destination string) (bool, error) {
	if ctx == nil {
		ctx = a.ctx
	}

	question := buildFileWriteQuestion(description, destination)
	optionsJSON, _ := json.Marshal([]string{writeConsentApproveLabel, "Negar"})
	answer, askErr := a.AskUserContext(ctx, question, string(optionsJSON), "false")
	if askErr != nil {
		a.logFileWrite("cancelado", description, askErr.Error())
		return false, askErr
	}

	if !strings.EqualFold(strings.TrimSpace(answer), writeConsentApproveLabel) {
		a.logFileWrite("negado", description, "resposta do usuario: "+strings.TrimSpace(answer))
		return false, nil
	}

	a.logFileWrite("autorizado", description, destination)
	return true, nil
}

// buildFileWriteQuestion monta o pedido de autorização com o que o usuário
// precisa para decidir (o que será gravado e onde).
func buildFileWriteQuestion(description, destination string) string {
	var b strings.Builder
	b.WriteString("A IA pediu para GRAVAR um arquivo no seu computador:\n")
	if d := strings.TrimSpace(description); d != "" {
		b.WriteString("- Conteúdo: " + d + "\n")
	}
	if dest := strings.TrimSpace(destination); dest != "" {
		b.WriteString("- Destino: " + dest + "\n")
	}
	b.WriteString("\nNenhum arquivo é gravado sem a sua autorização. Autorizar esta gravação?")
	return b.String()
}

// logFileWrite registra a decisão no log do agente (auditoria).
func (a *App) logFileWrite(outcome, description, detail string) {
	if a == nil {
		return
	}
	a.Logs.Append(fmt.Sprintf("[filewrite] %s conteudo=%q %s", outcome, description, detail))
}
