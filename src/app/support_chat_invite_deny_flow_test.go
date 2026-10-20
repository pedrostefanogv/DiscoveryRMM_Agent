package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"discovery/app/supportchat"
)

// ── Fase 3: recusa robusta + fim do loop do cartão ─────────────────────────
//
// Logs de um teste real:
//   (a) o cartão do convite foi FECHADO e REAPRESENTADO 4 vezes
//       ("cartao do convite ainda na tela apos o prazo; fechando pela rede de
//       seguranca" + "estado waiting sem cartao na tela; reapresentando"), e
//       cada fechamento foi uma DECISÃO PERDIDA;
//   (b) ao clicar em "Agora não" o processo de UI logou "convite recusado ...
//       e reportado ao servidor" mas o SERVIÇO nunca soube (o erro do POST só ia
//       para o log local).

// newInviteResponseApp monta um App cujo POST responde com um HTTP status fixo
// (409 vira ProblemDetails com code=session_declined) e conta as chamadas.
//
// A URL de TESTE escapa o sessionId bem no meio da rota
// (".../support-chat/sessions/<id>/invite-response"): o Prefixo do caminho tem
// um "%2F" que some ao ser re-encodado, então casar pelo sufixo
// "/invite-response" NÃO funciona — conta-se qualquer POST.
func newInviteResponseApp(t *testing.T, status int, hits *int32Locked) *App {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			hits.inc()
		}
		w.Header().Set("Content-Type", "application/json")
		if status >= 400 {
			if status == http.StatusConflict {
				w.WriteHeader(status)
				body, _ := json.Marshal(map[string]string{
					"title":   "session_declined",
					"message": "a sessão não está mais pendente.",
					"code":    "session_declined",
				})
				_, _ = w.Write(body)
				return
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "{\"message\":\"falha simulada\"}")
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return newSupportChatTestApp(t, srv.URL)
}

// int32Locked é um contador simples (sem atomic.Int32 para manter o teste
// legível na inspeção).
type int32Locked struct {
	mu sync.Mutex
	n  int
}

func (c *int32Locked) inc() { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *int32Locked) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// O erro de entrega da recusa precisa voltar ao consumidor da decisão
// (result.Error) — antes ele só ia para o log e o serviço nunca sabia.
func TestReportSupportChatInviteDecision_ErroPropagadoNoResultado(t *testing.T) {
	hits := &int32Locked{}
	a := newInviteResponseApp(t, http.StatusInternalServerError, hits)

	err := a.reportSupportChatInviteDecision(supportChatInviteNotificationResult{
		SessionID: "sessao-recusa",
		Accepted:  false,
	})
	if err == nil {
		t.Fatal("falha do POST de recusa precisa virar erro para quem consome a decisão")
	}
	if hits.get() != 1 {
		t.Fatalf("o cartão deve fazer UM POST; obtidos %d", hits.get())
	}
}

// 409 session_declined é "já resolvido": não é erro e NÃO deve ser retentado.
func TestReportSupportChatInviteDecision_ConflictJaResolvidoNaoRetenta(t *testing.T) {
	a := newInviteResponseApp(t, http.StatusConflict, &int32Locked{})

	err := a.reportSupportChatInviteDecision(supportChatInviteNotificationResult{
		SessionID: "sessao-ja-recusada",
		Accepted:  false,
	})
	if err != nil {
		t.Fatalf("409 session_declined deveria ser tratado como já resolvido: err=%q", err.Error())
	}

	// O caso 409 é o do TESTE REAL/API: "a conversa não está mais ativa." é o
	// texto LOCALIZADO que chega aqui (o apiErrorMessage do serviço traduz o
	// code antes de devolver o erro) — o erro de rede cru tem de ser retentado.
	if !isSupportChatInviteAlreadyResolved(errString("a conversa não está mais ativa.")) {
		t.Fatal("a mensagem localizada do 409 precisa ser reconhecida")
	}
	if isSupportChatInviteAlreadyResolved(errString("HTTP 500")) {
		t.Fatal("HTTP 500 NÃO é 'já resolvido'")
	}
}

// isSupportChatInviteAlreadyResolved reconhece os códigos do ProblemDetails do
// servidor e ignora erros de rede genéricos.
func TestIsSupportChatInviteAlreadyResolved(t *testing.T) {
	resolved := []string{
		"session_declined",
		"session_ended",
		"session_expired",
		"session_not_pending",
		"a conversa não está mais ativa.",
	}
	for _, msg := range resolved {
		if !isSupportChatInviteAlreadyResolved(errString(msg)) {
			t.Fatalf("%q deveria ser tratado como já resolvido", msg)
		}
	}
	for _, msg := range []string{"context deadline exceeded", "dial tcp: no route to host", "o servidor recusou a operação (HTTP 500)"} {
		if isSupportChatInviteAlreadyResolved(errString(msg)) {
			t.Fatalf("%q NÃO é 'já resolvido' — precisa ser retentado", msg)
		}
	}
	if isSupportChatInviteAlreadyResolved(nil) {
		t.Fatal("nil não é 'já resolvido'")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// 3.2: quando o cartão não consegue entregar a recusa, o consumidor RETENTA
// (com backoff vazio no teste) e só limpa o estado local depois do sucesso.
func TestRunDecision_RecusaLimpaEstadoSoDepoisDeEntregue(t *testing.T) {
	restore := supportChatInviteDenyRetryDelays
	supportChatInviteDenyRetryDelays = []time.Duration{0, 0, 0}
	defer func() { supportChatInviteDenyRetryDelays = restore }()

	hits := &int32Locked{}
	a := newInviteResponseApp(t, http.StatusOK, hits)
	a.supportChatService().PrepareInvite(supportchat.Invite{SessionID: "sessao-limpa", TicketTitle: "Notebook"})

	resultCh := make(chan supportChatInviteNotificationResult, 1)
	resultCh <- supportChatInviteNotificationResult{SessionID: "sessao-limpa", Accepted: false}
	close(resultCh)

	a.runSupportChatInviteNotificationDecision(supportchat.Invite{SessionID: "sessao-limpa"}, nil, resultCh)

	state := a.supportChatService().State()
	if state.Status == supportchat.StatusWaiting || state.SessionID == "sessao-limpa" {
		t.Fatalf("recusa entregue precisa limpar o estado local; obtido %+v", state)
	}
	if !a.supportChatInviteNotificationWasAnswered("sessao-limpa") {
		t.Fatal("convite recusado precisa ficar marcado como respondido (sem reapresentar)")
	}
}

// 3.2 (o bug do teste real): a recusa NÃO pode limpar o estado quando o POST
// falhou — o convite continua pendente e o console não fica "aguardando" um
// estado que o agent já esqueceu.
func TestRunDecision_RecusaNaoEntregueMantemConvitePendente(t *testing.T) {
	restore := supportChatInviteDenyRetryDelays
	supportChatInviteDenyRetryDelays = []time.Duration{0}
	restoreAttempts := supportChatInviteDenyMaxAttempts
	supportChatInviteDenyMaxAttempts = 2
	defer func() {
		supportChatInviteDenyRetryDelays = restore
		supportChatInviteDenyMaxAttempts = restoreAttempts
	}()

	hits := &int32Locked{}
	a := newInviteResponseApp(t, http.StatusServiceUnavailable, hits)
	a.supportChatService().PrepareInvite(supportchat.Invite{SessionID: "sessao-servidor-fora"})

	resultCh := make(chan supportChatInviteNotificationResult, 1)
	resultCh <- supportChatInviteNotificationResult{
		SessionID: "sessao-servidor-fora",
		Accepted:  false,
		// O CARTÃO não conseguiu entregar (é o que ele publica em result.Error).
		Error: errString("HTTP 503"),
	}
	close(resultCh)

	a.runSupportChatInviteNotificationDecision(supportchat.Invite{SessionID: "sessao-servidor-fora"}, nil, resultCh)

	state := a.supportChatService().State()
	if state.Status != supportchat.StatusWaiting || state.SessionID != "sessao-servidor-fora" {
		t.Fatalf("sem entrega da recusa o convite NÃO pode ser limpo; obtido %+v", state)
	}
	if got := hits.get(); got < 2 {
		t.Fatalf("a recusa deveria ser retentada; POSTs=%d", got)
	}
}

// 3.3: a recusa de um cartão cujo convite foi SUBSTITUÍDO continua válida — o
// POST é por SessionID e é assim que a sessão antiga deixa de ficar
// "aguardando" no console.
func TestRunDecision_RecusaDeCartaoSubstituidoChegaAoServidor(t *testing.T) {
	hits := &int32Locked{}
	a := newInviteResponseApp(t, http.StatusOK, hits)
	// Estado corrente é OUTRO convite (o do cartão foi substituído).
	a.supportChatService().PrepareInvite(supportchat.Invite{SessionID: "convite-novo"})

	resultCh := make(chan supportChatInviteNotificationResult, 1)
	resultCh <- supportChatInviteNotificationResult{
		SessionID: "convite-antigo",
		Accepted:  false,
		// O cartão antigo JÁ reportou a recusa; o consumidor não refaz o POST.
		Error: nil,
	}
	close(resultCh)

	a.runSupportChatInviteNotificationDecision(supportchat.Invite{SessionID: "convite-antigo"}, nil, resultCh)

	if hits.get() != 0 {
		t.Fatalf("a recusa de um cartão substituído NÃO pode ser descartada (nem gerar POST extra); POSTs=%d", hits.get())
	}
	// E o convite corrente NÃO pode ser derrubado pela recusa do antigo.
	state := a.supportChatService().State()
	if state.Status != supportchat.StatusWaiting || state.SessionID != "convite-novo" {
		t.Fatalf("recusa do cartão antigo não pode limpar o convite corrente: %+v", state)
	}
}
