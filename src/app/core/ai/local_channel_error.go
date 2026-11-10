package ai

// Reconhecimento do envelope de erro LOCAL do canal IPC.
//
// O agente (pacote app) emite, para falhas do canal admin, o envelope
//
//	error.code      = "LOCAL_IPC_WRITE_UNAVAILABLE"
//	error.scope     = "local_agent"
//	error.retryable = false
//	error.detail    = "<mensagem com o nome do pipe e o erro do SO>"
//
// Este arquivo é o lado do chat: reconhece o envelope e decide o que o loop
// multi-round faz com ele. A detecção é ESTRUTURAL (não por substring) de
// propósito — o pacote core/ai não pode importar app (app importa ai), e a
// decisão "abortar o loop" precisa ser conservadora: só dispara com
// scope=local_agent E retryable=false explícitos.
//
// Contexto (PLANO_CORRECAO_CANAL_ADMIN_IPC.md §1.7, §4 P2.9/P2.11): sem
// envelope, o erro viajava como texto cru e o modelo concluía "servidor fora do
// ar" e repetia a mutação 3 vezes (~38s).

import "encoding/json"

// Envelope de erro estruturado do canal local.
const (
	// LocalChannelScopeAgent é o escopo que marca falha na MÁQUINA do agente.
	LocalChannelScopeAgent = "local_agent"
	// LocalChannelCodeWriteUnavailable corresponde ao erro tipado do pacote app.
	LocalChannelCodeWriteUnavailable = "LOCAL_IPC_WRITE_UNAVAILABLE"
	// LocalChannelCodeWriteBlockedBySession é o código do circuit breaker.
	LocalChannelCodeWriteBlockedBySession = "LOCAL_IPC_WRITE_BLOCKED_BY_SESSION"
)

type localChannelErrorEnvelopeBody struct {
	Code      string `json:"code"`
	Scope     string `json:"scope"`
	Retryable *bool  `json:"retryable"`
}

// localChannelErrorEnvelope devolve o envelope estruturado embutido no erro.
//
// O executor MCP do chat devolve o erro já envelopado no texto do resultado
// (via bridge registrado por app); quando o app registrou o resolvedor tipado,
// o envelope é montado preservando code/scope/retryable reais.
func localChannelErrorEnvelope(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if localChannelErrorResolver != nil {
		if payload, ok := localChannelErrorResolver(err); ok && payload != "" {
			return payload, true
		}
	}
	return "", false
}

// localChannelErrorResolver é registrado pelo pacote app (que conhece o tipo
// concreto do erro). Evita dependência circular sem quebrar o envelope.
var localChannelErrorResolver func(error) (string, bool)

// RegisterLocalChannelErrorResolver registra o resolvedor tipado do envelope.
// Chamado uma vez no init do pacote app.
func RegisterLocalChannelErrorResolver(fn func(error) (string, bool)) {
	localChannelErrorResolver = fn
}

// localWriteFailureNotRetryable reporta se o RESULTADO de uma tool carrega o
// envelope de falha local não-retentável (P2.11).
//
// Só é verdadeiro com `scope == "local_agent"` E `retryable == false`
// EXPLÍCITOS: um tool result qualquer que mencione "local" não pode abortar o
// turno. Erro de parâmetro, validação ou timeout continua fluindo normalmente.
func localWriteFailureNotRetryable(result string) bool {
	env, ok := parseLocalChannelError(result)
	if !ok {
		return false
	}
	return env.Scope == LocalChannelScopeAgent && env.Retryable != nil && !*env.Retryable
}

// parseLocalChannelError extrai o corpo de "error" do tool result.
func parseLocalChannelError(result string) (localChannelErrorEnvelopeBody, bool) {
	var zero localChannelErrorEnvelopeBody
	trimmed := trimSpaceASCII(result)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return zero, false
	}
	var outer struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(trimmed), &outer); err != nil {
		return zero, false
	}
	if len(outer.Error) == 0 || outer.Error[0] != '{' {
		return zero, false
	}
	var body localChannelErrorEnvelopeBody
	if err := json.Unmarshal(outer.Error, &body); err != nil {
		return zero, false
	}
	if body.Code == "" || body.Scope == "" {
		return zero, false
	}
	return body, true
}

// trimSpaceASCII evita importar strings só para um TrimSpace (o pacote já usa
// strings em outros arquivos; mantido local para deixar a dependência explícita).
func trimSpaceASCII(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
