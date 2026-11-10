package app

// Erros ESTRUTURADOS do canal admin para a IA (P2.9) e circuit breaker de
// mutações (P2.11).
//
// ── Por que existe ─────────────────────────────────────────────────────────
// Incidente de 2026-10-17: o pipe admin nunca subia (nome malformado) e TODA
// mutação falhava. O erro chegava ao LLM como texto cru
// (`ipc request support:agent_ticket_create: canal admin indisponível (serviço
// sem autenticação?)`) — sem código, sem escopo, sem indicação de "não
// repetir". Somado ao "(serviço sem autenticação?)", o modelo concluiu que a
// FALHA ERA DO SERVIDOR de suporte e repetiu a MESMA chamada mutante 3 vezes
// (~38s desperdiçados) — ver PLANO_CORRECAO_CANAL_ADMIN_IPC.md §1.7, §2 e §7.1.
//
// A correção (bloco P2 do plano):
//   1. o agente emite `{"error":{"code":"LOCAL_IPC_WRITE_UNAVAILABLE",
//      "scope":"local_agent","retryable":false,"detail":"..."}}` — o servidor
//      já repassa erro estruturado intacto (AiChatToolOrchestrator.WrapAgentToolError);
//   2. o circuit breaker corta as PRÓXIMAS mutações da sessão na hora, sem
//      pagar outro handshake de 3s (a UI abre uma conexão admin nova por
//      request que falha em connectAdmin);
//   3. a mensagem passa a citar o NOME DO PIPE e o erro do SO, para o
//      diagnóstico apontar a máquina local em vez de "servidor fora do ar".

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Códigos de erro do canal local. São o contrato consumido pelo servidor
// (prompt efetivo + testes de regressão) — mudar um literal aqui exige atualizar
// AiChatHelpers/AiChatSystemPromptBuilder do lado da API.
const (
	// IPCErrorCodeWriteUnavailable: o canal admin (o ÚNICO que muta) não está
	// disponível NESTA máquina — serviço sem o pipe admin, pipe não criado ou
	// handshake recusado. Não é falha do servidor de suporte.
	IPCErrorCodeWriteUnavailable = "LOCAL_IPC_WRITE_UNAVAILABLE"
	// IPCErrorCodeWriteBlockedBySession: circuit breaker aberto — já houve uma
	// falha local não-retentável de mutação nesta sessão.
	IPCErrorCodeWriteBlockedBySession = "LOCAL_IPC_WRITE_BLOCKED_BY_SESSION"
)

// Escopos do envelope de erro. "local_agent" é a marca que o prompt efetivo do
// servidor usa para proibir o diagnóstico "servidor fora do ar".
const (
	IPCErrorScopeLocalAgent = "local_agent"
	IPCErrorScopeRemote     = "remote_service"
)

// ipcEnvelopeError é o corpo de `error` do envelope estruturado.
type ipcEnvelopeError struct {
	Code      string `json:"code"`
	Scope     string `json:"scope"`
	Retryable bool   `json:"retryable"`
	Detail    string `json:"detail"`
	// Method é o método RPC que falhou (ex.: "support:agent_ticket_create").
	Method string `json:"method,omitempty"`
	// Pipe é o named pipe do canal admin. Vai no envelope porque foi EXATAMENTE
	// a ausência dele que causou o incidente — o usuário/suporte precisa poder
	// conferir o pipe sem abrir o log.
	Pipe string `json:"pipe,omitempty"`
	// Hint é a orientação de tratamento; o servidor também tem a regra no
	// system prompt (P2.10), este texto é reforço para o caso de o cliente
	// enviar o tool result sem passar pelo prompt.
	Hint string `json:"hint,omitempty"`
}

// ipcLocalChannelError é o erro tipado devolvido por toda falha do canal admin.
// Implementa error; o envelope é montado por EnvelopeJSON.
type ipcLocalChannelError struct {
	Code      string
	Scope     string
	Retryable bool
	Detail    string
	Method    string
	Pipe      string
}

func (e *ipcLocalChannelError) Error() string {
	if e == nil {
		return ""
	}
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		detail = "canal admin indisponível"
	}
	return fmt.Sprintf("ipc request %s: %s", e.Method, detail)
}

// EnvelopeJSON serializa o envelope `{"error":{...}}` esperado pela IA.
// Nunca devolve erro: em falha de marshal (impossível para estes campos)
// devolve um envelope mínimo — o chat precisa SEMPRE de JSON válido no result.
func (e *ipcLocalChannelError) EnvelopeJSON() string {
	if e == nil {
		return ""
	}
	body := ipcEnvelopeError{
		Code:      e.Code,
		Scope:     e.Scope,
		Retryable: e.Retryable,
		Detail:    strings.TrimSpace(e.Detail),
		Method:    e.Method,
		Pipe:      e.Pipe,
		Hint:      ipcLocalChannelHint(e.Retryable),
	}
	if b, err := json.Marshal(map[string]any{"error": body}); err == nil {
		return string(b)
	}
	return `{"error":{"code":"` + IPCErrorCodeWriteUnavailable + `","scope":"local_agent","retryable":false,"detail":"canal admin indisponivel"}}`
}

// ipcLocalChannelHint é a orientação entregue ao LLM junto do erro.
const ipcLocalChannelHintValue = "Falha LOCAL desta máquina (canal de gravação do serviço do agente). " +
	"NÃO é problema do servidor de suporte. NÃO repita a mesma chamada. " +
	"Informe o usuário e oriente reiniciar o serviço DiscoveryAgent."

const ipcLocalChannelRepeatHintValue = "Falha LOCAL desta máquina já registrada nesta sessão (circuit breaker). " +
	"NÃO repita chamadas de gravação. " +
	"Informe o usuário e oriente reiniciar o serviço DiscoveryAgent."

func ipcLocalChannelHint(retryable bool) string {
	if retryable {
		return ""
	}
	return ipcLocalChannelHintValue
}

// As sentinelas continuam existindo (código legado/testes as comparam), mas
// toda falha do canal admin agora é convertida em *ipcLocalChannelError.

// newIPCWriteUnavailable monta o erro tipado a partir de uma causa do SO.
//
// detail SEMPRE cita o nome do pipe e a causa real (ex.: "Incorrect function"),
// porque foi o texto antigo — "canal admin indisponível (serviço sem
// autenticação?)" — que induziu o diagnóstico errado "servidor fora do ar".
func newIPCWriteUnavailable(method, cause string) *ipcLocalChannelError {
	return &ipcLocalChannelError{
		Code:      IPCErrorCodeWriteUnavailable,
		Scope:     IPCErrorScopeLocalAgent,
		Retryable: false,
		Detail:    fmt.Sprintf("canal de gravação local indisponível (pipe %s): %s", IPCPipeAdminName, cause),
		Method:    method,
		Pipe:      IPCPipeAdminName,
	}
}

// isIPCLocalNonRetryable reporta se o erro é uma falha LOCAL não-retentável de
// escrita — a condição que abre o circuit breaker (P2.11).
func isIPCLocalNonRetryable(err error) bool {
	var typed *ipcLocalChannelError
	if errors.As(err, &typed) {
		return typed.Scope == IPCErrorScopeLocalAgent && !typed.Retryable
	}
	return false
}

// ipcEnvelopeJSON converte o erro da tool em envelope estruturado.
//
// Contrato com o servidor (AiChatToolOrchestrator.WrapAgentToolError): resultado
// com a chave "error" é repassado INTACTO — o envelope é o mecanismo que impede
// o servidor de aplicar o reflexo genérico "corrija e tente de novo".
func ipcEnvelopeJSON(err error, method string) (string, bool) {
	if err == nil {
		return "", false
	}
	var typed *ipcLocalChannelError
	if errors.As(err, &typed) {
		return typed.EnvelopeJSON(), true
	}
	return "", false
}
