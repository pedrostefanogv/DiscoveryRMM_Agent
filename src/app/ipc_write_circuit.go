package app

// Circuit breaker de MUTAÇÕES do canal admin (P2.11 do
// PLANO_CORRECAO_CANAL_ADMIN_IPC.md).
//
// Regra do plano: "1 falha local não-retentável em método mutante → as próximas
// mutações da sessão retornam retryable=false imediatamente (sem 3 × 38 s)".
//
// Por que é necessário: cada request mutante chama adminAuth(), que tenta
// connectAdmin(3s) quando não há conexão admin. Com o serviço em modo
// somente-leitura (incidente: pipe admin inexistente), CADA tentativa paga o
// handshake inteiro e falha — e o LLM, sem indicação de "não repetir", refez a
// mesma chamada 3 vezes no mesmo turno. O breaker transforma a segunda em diante
// em resposta instantânea com o MESMO envelope estruturado (retryable=false),
// que é o que o modelo respeita.
//
// Estado GLOBAL de propósito: a UI companion tem um único IPCClient e as
// mutações do chat passam todas por ele. Um breaker "por sessão de chat" não
// cortaria o retry quando cada round reusa o mesmo client — que é o caminho real.
// A recuperação (close) é automática quando o canal admin volta a subir.

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ipcCircuitProbeInterval é o intervalo mínimo entre duas TENTATIVAS de
// reconexão enquanto o breaker está aberto. Sem ele, um chat que insiste em
// mutações pagaria um handshake de 3s por chamada; com ele, pagamos no máximo
// um por intervalo e as demais recebem recusa imediata.
const ipcCircuitProbeInterval = 15 * time.Second

type ipcWriteCircuit struct {
	mu sync.Mutex
	// typedErr é o erro que ABRIU o breaker (guarda detail/pipe originais).
	typedErr *ipcLocalChannelError
	openedAt time.Time
	// lastProbe é quando tentamos reabrir o canal por conta própria.
	lastProbe time.Time
	// tripCount e rejectCount alimentam a observabilidade (status/UI).
	tripCount   atomic.Int64
	rejectCount atomic.Int64
}

// ipcWriteBreaker é a instância global consultada por IPCClient.Request.
var ipcWriteBreaker = &ipcWriteCircuit{}

// open abre (ou reforça) o breaker e registra o motivo.
func (c *ipcWriteCircuit) open(method string, err *ipcLocalChannelError) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	alreadyOpen := c.typedErr != nil
	c.typedErr = err
	c.openedAt = time.Now()
	c.mu.Unlock()
	if !alreadyOpen {
		c.tripCount.Add(1)
		log.Printf("[ipc] circuit breaker ABERTO: %s — mutações locais bloqueadas até o canal admin voltar (pipe %s)",
			err.Detail, err.Pipe)
	}
	_ = method
}

// close fecha o breaker: o canal admin voltou (handshake bem-sucedido).
func (c *ipcWriteCircuit) close(reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	wasOpen := c.typedErr != nil
	c.typedErr = nil
	c.openedAt = time.Time{}
	c.lastProbe = time.Time{}
	c.mu.Unlock()
	if wasOpen {
		log.Printf("[ipc] circuit breaker FECHADO: canal admin restabelecido (%s)", reason)
	}
}

// Open informa se o breaker está aberto.
func (c *ipcWriteCircuit) Open() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.typedErr != nil
}

// reason devolve uma CÓPIA do erro que abriu o breaker.
func (c *ipcWriteCircuit) reason() *ipcLocalChannelError {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.typedErr == nil {
		return nil
	}
	cp := *c.typedErr
	return &cp
}

// shouldProbe decide se este request pode tentar reabrir o canal. Verdadeiro no
// máximo uma vez por ipcCircuitProbeInterval — uma sonda, não uma tentativa por
// chamada.
func (c *ipcWriteCircuit) shouldProbe(now time.Time) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.typedErr == nil {
		return true
	}
	if !c.lastProbe.IsZero() && now.Sub(c.lastProbe) < ipcCircuitProbeInterval {
		return false
	}
	c.lastProbe = now
	return true
}

// Stats expõe os contadores para o snapshot de status/heartbeat (P1.4).
func (c *ipcWriteCircuit) Stats() (trips, rejects int64, open bool) {
	if c == nil {
		return 0, 0, false
	}
	c.mu.Lock()
	open = c.typedErr != nil
	c.mu.Unlock()
	return c.tripCount.Load(), c.rejectCount.Load(), open
}

// OpenIPCWriteCircuit é o ponto de entrada chamado pelo cliente IPC quando uma
// mutação falha por indisponibilidade LOCAL do canal admin.
func OpenIPCWriteCircuit(method string, err error) {
	var typed *ipcLocalChannelError
	if !asIPCLocalError(err, &typed) {
		return
	}
	ipcWriteBreaker.open(method, typed)
}

// CloseIPCWriteCircuit é chamado quando o handshake do canal admin volta a
// funcionar (connectAdmin bem-sucedido) — o breaker é apenas um curto-circuito,
// nunca uma recusa permanente.
func CloseIPCWriteCircuit(reason string) { ipcWriteBreaker.close(reason) }

// IPCWriteCircuitOpen informa o estado atual do breaker (observabilidade).
func IPCWriteCircuitOpen() bool { return ipcWriteBreaker.Open() }

// IPCWriteCircuitStats devolve (trips, recusas, aberto) para status/heartbeat.
func IPCWriteCircuitStats() (int64, int64, bool) { return ipcWriteBreaker.Stats() }

// guardIPCWrite é a checagem de entrada do caminho mutante em IPCClient.Request.
//
// Devolve (erroTipado, true) quando a mutação deve ser RECUSADA sem tocar o
// pipe. Quando o breaker está aberto mas a janela de sonda venceu, devolve
// (nil,false) e o request segue para o handshake — é assim que o canal volta
// sozinho depois de o serviço ser reiniciado.
func (c *IPCClient) guardIPCWrite(method string) (*ipcLocalChannelError, bool) {
	if !ipcWriteBreaker.Open() {
		return nil, false
	}
	if ipcWriteBreaker.shouldProbe(time.Now()) {
		return nil, false
	}
	reason := ipcWriteBreaker.reason()
	if reason == nil {
		return nil, false
	}
	ipcWriteBreaker.rejectCount.Add(1)
	// Mesmo envelope/código da falha original; o código muda para deixar claro
	// que a recusa veio do breaker (e não de uma nova tentativa no pipe).
	blocked := *reason
	blocked.Code = IPCErrorCodeWriteBlockedBySession
	blocked.Method = method
	blocked.Detail = "canal de gravação local indisponível (pipe " + IPCPipeAdminName +
		"); mutações desta sessão bloqueadas após falha anterior: " + reason.Detail
	return &blocked, true
}

// ── Observabilidade do canal de gravação (P1.4) ────────────────────────────

// AdminChannelAuthenticated informa se o cliente TEM uma conexão admin
// autenticada neste momento. Diferente de "houve handshake alguma vez": reflete
// a conexão viva, que é o que decide se uma mutação vai funcionar agora.
func (c *IPCClient) AdminChannelAuthenticated() bool {
	if c == nil {
		return false
	}
	c.adminMu.Lock()
	defer c.adminMu.Unlock()
	return c.adminConn != nil && strings.TrimSpace(c.adminToken) != ""
}

// IPCWriteBreakerLastError devolve o detalhe do último erro do canal admin
// (vazio quando não houve). Alimenta o painel de diagnóstico da UI (P1.4) —
// estado GLOBAL do processo, independente de haver client atribuído.
func IPCWriteBreakerLastError() string {
	reason := ipcWriteBreaker.reason()
	if reason == nil {
		return ""
	}
	return reason.Detail
}

// LastAdminChannelError é o mesmo dado pelo cliente (nil-safe).
func (c *IPCClient) LastAdminChannelError() string { return IPCWriteBreakerLastError() }

// asIPCLocalError é errors.As sem importar "errors" neste arquivo (mantém o
// helper de conversão junto do tipo que ele converte).
func asIPCLocalError(err error, out **ipcLocalChannelError) bool {
	if err == nil {
		return false
	}
	for err != nil {
		if typed, ok := err.(*ipcLocalChannelError); ok {
			*out = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
