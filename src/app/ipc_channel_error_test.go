package app

// Testes do bloco P2 do PLANO_CORRECAO_CANAL_ADMIN_IPC.md no lado do AGENTE:
//   P2.9  envelope estruturado {code, scope, retryable, detail}
//   P2.11 circuit breaker (1 falha local não-retentável corta as próximas)
//   P2.12 mensagem com nome do pipe + erro do SO
//
// Roda em qualquer SO (não usa named pipe): o que está sob teste é o CONTRATO
// que a IA consome.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func resetIPCWriteBreaker() {
	ipcWriteBreaker.close("teste")
	ipcWriteBreaker.tripCount.Store(0)
	ipcWriteBreaker.rejectCount.Store(0)
}

// P2.12: a mensagem tem de citar o pipe admin e a causa do SO. O texto antigo
// ("serviço sem autenticação?") foi o que fez a IA culpar o servidor.
func TestIPCWriteUnavailableDetailCitaPipeNoECausaDoSO(t *testing.T) {
	err := newIPCWriteUnavailable("support:agent_ticket_create",
		"falha ao abrir o pipe admin "+IPCPipeAdminName+": Incorrect function.")

	if !strings.Contains(err.Detail, IPCPipeAdminName) {
		t.Fatalf("detail deveria citar o nome do pipe admin; got %q", err.Detail)
	}
	if !strings.Contains(err.Detail, "Incorrect function") {
		t.Fatalf("detail deveria citar o erro do SO; got %q", err.Detail)
	}
	if strings.Contains(strings.ToLower(err.Detail), "sem autenticação") {
		t.Fatalf("detail não pode repetir o texto enganoso 'sem autenticação'; got %q", err.Detail)
	}
	if strings.Contains(err.Error(), "servidor") {
		t.Fatalf("mensagem não pode sugerir problema de servidor; got %q", err.Error())
	}
}

// P2.9: o envelope tem de ter code/scope/retryable/detail com os valores
// combinados no plano, e ser JSON válido (o servidor faz json parse).
func TestIPCWriteUnavailableEnvelopeEstruturado(t *testing.T) {
	err := newIPCWriteUnavailable("support:comment_add", "pipe inexistente")

	var doc struct {
		Error struct {
			Code      string `json:"code"`
			Scope     string `json:"scope"`
			Retryable bool   `json:"retryable"`
			Detail    string `json:"detail"`
			Method    string `json:"method"`
			Pipe      string `json:"pipe"`
		} `json:"error"`
	}
	if uErr := json.Unmarshal([]byte(err.EnvelopeJSON()), &doc); uErr != nil {
		t.Fatalf("envelope não é JSON válido: %v (%s)", uErr, err.EnvelopeJSON())
	}
	if doc.Error.Code != "LOCAL_IPC_WRITE_UNAVAILABLE" {
		t.Fatalf("code = %q, want LOCAL_IPC_WRITE_UNAVAILABLE", doc.Error.Code)
	}
	if doc.Error.Scope != "local_agent" {
		t.Fatalf("scope = %q, want local_agent", doc.Error.Scope)
	}
	if doc.Error.Retryable {
		t.Fatal("retryable deveria ser false — é o que impede o retry cego da IA")
	}
	if doc.Error.Detail == "" {
		t.Fatal("detail não pode ser vazio")
	}
	if doc.Error.Method != "support:comment_add" {
		t.Fatalf("method = %q, want support:comment_add", doc.Error.Method)
	}
	if doc.Error.Pipe != IPCPipeAdminName {
		t.Fatalf("pipe = %q, want %q", doc.Error.Pipe, IPCPipeAdminName)
	}
}

// A sentinela antiga continua sendo tratada como erro LOCAL não-retentável
// depois de convertida — é a condição que abre o breaker.
func TestIsIPCLocalNonRetryable(t *testing.T) {
	err := newIPCWriteUnavailable("support:create", "pipe não existe")
	if !isIPCLocalNonRetryable(err) {
		t.Fatal("falha do canal admin deveria ser local e não-retentável")
	}
	if isIPCLocalNonRetryable(errors.New("timeout de rede")) {
		t.Fatal("erro genérico não pode ser classificado como falha local do canal")
	}
	if !isIPCLocalNonRetryable(fmtWrap(err)) {
		t.Fatal("erro embrulhado com %%w deveria continuar sendo reconhecido")
	}
}

func fmtWrap(err error) error { return &wrapErr{err} }

type wrapErr struct{ err error }

func (w *wrapErr) Error() string { return "ipc request x: " + w.err.Error() }
func (w *wrapErr) Unwrap() error { return w.err }

// P2.11: uma falha abre o breaker e a PRÓXIMA mutação é recusada NA HORA, com
// o mesmo envelope (só o código muda para LOCAL_IPC_WRITE_BLOCKED_BY_SESSION).
func TestCircuitBreakerRecusaProximaMutacaoImediatamente(t *testing.T) {
	resetIPCWriteBreaker()
	defer resetIPCWriteBreaker()

	if IPCWriteCircuitOpen() {
		t.Fatal("breaker deveria começar fechado")
	}
	cause := newIPCWriteUnavailable("support:agent_ticket_create", "pipe não existe")
	OpenIPCWriteCircuit("support:agent_ticket_create", cause)
	if !IPCWriteCircuitOpen() {
		t.Fatal("falha local não-retentável deveria ABRIR o breaker")
	}

	client := &IPCClient{}
	client.requestSeq.Store(0)
	// Sonda já consumida para garantir a recusa imediata.
	ipcWriteBreaker.mu.Lock()
	ipcWriteBreaker.lastProbe = time.Now()
	ipcWriteBreaker.mu.Unlock()

	blocked, refused := client.guardIPCWrite("support:agent_comment_add")
	if !refused || blocked == nil {
		t.Fatal("com o breaker aberto a mutação deveria ser recusada sem tocar o pipe")
	}
	if blocked.Scope != IPCErrorScopeLocalAgent || blocked.Retryable {
		t.Fatalf("recusa precisa manter scope=local_agent e retryable=false; got %+v", blocked)
	}
	if blocked.Code != IPCErrorCodeWriteBlockedBySession {
		t.Fatalf("code = %q, want %q", blocked.Code, IPCErrorCodeWriteBlockedBySession)
	}
	if !strings.Contains(blocked.Detail, IPCPipeAdminName) {
		t.Fatalf("detalhe do breaker deveria citar o pipe; got %q", blocked.Detail)
	}
	trips, rejects, open := IPCWriteCircuitStats()
	if trips != 1 || rejects != 1 || !open {
		t.Fatalf("stats = (trips=%d rejects=%d open=%v), want (1,1,true)", trips, rejects, open)
	}
}

// Um erro que NÃO é falha local não pode abrir o breaker (timeout, argumento
// inválido, erro remoto do servidor de suporte).
func TestCircuitBreakerNaoAbreComErroNaoLocal(t *testing.T) {
	resetIPCWriteBreaker()
	defer resetIPCWriteBreaker()

	OpenIPCWriteCircuit("support:create", errors.New("timeout aguardando resposta do servidor"))
	if IPCWriteCircuitOpen() {
		t.Fatal("erro genérico não pode abrir o breaker")
	}
	client := &IPCClient{}
	if _, refused := client.guardIPCWrite("support:create"); refused {
		t.Fatal("sem breaker aberto a mutação deve seguir para o pipe")
	}
}

// O breaker é CURTO-CIRCUITO, não recusa permanente: quando o handshake do
// canal admin volta, ele fecha e as mutações voltam a passar.
func TestCircuitBreakerFechaQuandoCanalVolta(t *testing.T) {
	resetIPCWriteBreaker()
	defer resetIPCWriteBreaker()

	OpenIPCWriteCircuit("support:create", newIPCWriteUnavailable("support:create", "pipe não existe"))
	if !IPCWriteCircuitOpen() {
		t.Fatal("pré-condição: breaker aberto")
	}
	CloseIPCWriteCircuit("handshake admin autenticado")
	if IPCWriteCircuitOpen() {
		t.Fatal("breaker deveria fechar após o canal admin voltar")
	}
	client := &IPCClient{}
	if _, refused := client.guardIPCWrite("support:create"); refused {
		t.Fatal("com o canal restabelecido a mutação não pode ser recusada")
	}
}

// A sonda tem de ser limitada: com o breaker aberto, requests em sequência não
// podem pagar um handshake de 3s cada (era o custo de 3 × ~13s do incidente).
func TestCircuitBreakerLimitaSondas(t *testing.T) {
	resetIPCWriteBreaker()
	defer resetIPCWriteBreaker()

	OpenIPCWriteCircuit("support:create", newIPCWriteUnavailable("support:create", "pipe não existe"))
	now := time.Now()
	if !ipcWriteBreaker.shouldProbe(now) {
		t.Fatal("a primeira chamada após abrir o breaker deveria poder sondar")
	}
	if ipcWriteBreaker.shouldProbe(now.Add(time.Second)) {
		t.Fatal("sondas em sequência não podem ser permitidas")
	}
	if !ipcWriteBreaker.shouldProbe(now.Add(ipcCircuitProbeInterval + time.Second)) {
		t.Fatal("após o intervalo de sonda a tentativa de reconexão deveria ser permitida")
	}
}
