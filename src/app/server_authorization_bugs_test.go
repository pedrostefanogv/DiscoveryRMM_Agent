package app

import (
	"net/http"
	"strings"
	"testing"
)

// BUG 1 (revisão): com o estado JÁ pending (403 detectado), uma falha de
// transporte não atualizava a razão — o guard "if !pending" pulava o bloco
// inteiro, inclusive o Store(true) do unreachable. Resultado: um agente
// pendente que perdia a rede continuava reportando "awaiting-approval"
// indefinidamente, mesmo sem nunca mais falar com o servidor.
func TestServerAuthorizationUpdatesReasonWhenTransportFailsWhilePending(t *testing.T) {
	a := provisionedApp(t, false)
	body := []byte(`{"error":"Agent registration is pending (zero-touch)."}`)
	a.noteServerAuthorizationRefusal(approvalResponse(), body, nil)

	if _, reason, _ := a.serverAuthorizationPending(); reason != onboardPendingReasonApproval {
		t.Fatalf("pré-condição: reason = %q", reason)
	}

	// O servidor fica inalcançável enquanto a pendência já é conhecida.
	a.noteServerAuthorizationRefusal(nil, nil, &fakeNetError{})

	_, reason, _ := a.serverAuthorizationPending()
	if reason != onboardingPendingReasonServerUnreachable {
		t.Fatalf("com o servidor inalcançável a razão precisa virar %q, veio %q",
			onboardingPendingReasonServerUnreachable, reason)
	}
}

// BUG 2 (revisão): isConnectionError classificava QUALQUER string contendo
// "timeout"/"eof" como falha de rede — inclusive erros de negócio que só citam
// a palavra. Isso ligava a tela de espera por engano.
func TestIsConnectionErrorDoesNotOverMatchBusinessErrors(t *testing.T) {
	casos := []struct {
		msg      string
		esperado bool
	}{
		{"app-store (Winget) retornou HTTP 503: Service Unavailable", false},
		{"timeout ao aguardar a fila de instalação interna", false},
		{"pacote sem eof no manifesto", false},
		{"dial tcp 10.0.0.1:443: connect: connection refused", true},
		{"lookup discovery.exemplo.com: no such host", true},
		{"context deadline exceeded", true},
	}
	for _, c := range casos {
		got := isConnectionError(authErrString(c.msg))
		if got != c.esperado {
			t.Errorf("isConnectionError(%q) = %v, esperado %v", c.msg, got, c.esperado)
		}
	}
}

// BUG 3 (revisão): uma resposta 2xx de OUTRO endpoint não pode limpar a
// pendência — só a confirmação explícita de /me/configuration autoriza.
func TestOnlyConfigurationConfirmationClearsPending(t *testing.T) {
	a := provisionedApp(t, false)
	body := []byte(`{"error":"Agent registration is pending (zero-touch)."}`)
	a.noteServerAuthorizationRefusal(approvalResponse(), body, nil)

	// Resposta 200 de um endpoint qualquer (ex.: /me/commands já permitido).
	a.noteServerAuthorizationRefusal(okResponse(), []byte(`{"ok":true}`), nil)

	if !a.IsServerAuthorizationPending() {
		t.Fatal("2xx de outro endpoint não pode limpar a pendência; só /me/configuration confirma")
	}
}

type authErrString string

func (e authErrString) Error() string { return string(e) }

// okResponse simula um 200 de um endpoint autenticado qualquer.
func okResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       http.NoBody,
		Header:     http.Header{},
	}
}

// BUG 4 (revisão): o log de "preso"/diagnóstico não deve vazar em cada
// chamada — markServerAuthorizationPending é idempotente e não pode emitir
// evento repetido (a UI recarregaria a cada tela aberta).
func TestMarkPendingIsIdempotent(t *testing.T) {
	a := provisionedApp(t, false)
	body := []byte(`{"error":"Agent registration is pending (zero-touch)."}`)
	a.noteServerAuthorizationRefusal(approvalResponse(), body, nil)
	_, _, first := a.serverAuthorizationPending()

	for i := 0; i < 5; i++ {
		a.noteServerAuthorizationRefusal(approvalResponse(), body, nil)
	}
	_, _, again := a.serverAuthorizationPending()
	if first != again {
		t.Fatalf("detectedAt mudou entre chamadas idempotentes: %q -> %q", first, again)
	}
	if !strings.Contains(first, "T") {
		t.Fatalf("detectedAt não parece RFC3339: %q", first)
	}
}
