package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// provisionedApp simula uma instalação que JÁ recebeu o token de comunicação
// (o cenário do relato): sem isso o estado correto seria "aguardando
// provisionamento", que é outra tela.
func provisionedApp(t *testing.T, authorized bool) *App {
	t.Helper()
	a := &App{}
	prev := hasServerCredentials
	hasServerCredentials = func() bool { return true }
	t.Cleanup(func() { hasServerCredentials = prev })
	a.serverAuth.verifiedOnce.Store(authorized)
	return a
}

func approvalResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Body:       http.NoBody,
		Header:     http.Header{},
	}
}

func TestServerAuthorizationDetectsZeroTouchPendingOn403(t *testing.T) {
	a := provisionedApp(t, true)

	body := []byte(`{"error":"Agent registration is pending (zero-touch)."}`)
	a.noteServerAuthorizationRefusal(approvalResponse(), body, nil)

	pending, reason, detectedAt := a.serverAuthorizationPending()
	if !pending {
		t.Fatal("403 de agente zero-touch pendente precisa marcar 'aguardando liberação do servidor'")
	}
	if reason != onboardPendingReasonApproval {
		t.Fatalf("reason = %q, esperado %q", reason, onboardPendingReasonApproval)
	}
	if strings.TrimSpace(detectedAt) == "" {
		t.Fatal("detectedAtUtc precisa ser preenchido para a UI explicar desde quando espera")
	}

	// GetOnboardingStatus é a fonte da verdade consumida pela overlay e pelas
	// telas de conteúdo: precisa refletir o modo "awaiting-approval".
	st := a.GetOnboardingStatus()
	if got, _ := st["mode"].(string); got != "awaiting-approval" {
		t.Fatalf("GetOnboardingStatus().mode = %q, esperado 'awaiting-approval'", got)
	}
	if got, _ := st["pendingReason"].(string); got != onboardPendingReasonApproval {
		t.Fatalf("pendingReason = %q", got)
	}
	if got, _ := st["configured"].(bool); !got {
		t.Fatal("agente com credenciais precisa reportar configured=true mesmo aguardando liberação")
	}
}

// 403 de OUTRA natureza (permissão de recurso) NÃO pode virar "aguardando
// liberação": a assinatura do corpo é o discriminador.
func TestServerAuthorizationIgnoresUnrelated403(t *testing.T) {
	a := provisionedApp(t, true)
	body := []byte(`{"error":"Forbidden"}`)
	a.noteServerAuthorizationRefusal(approvalResponse(), body, nil)
	if pending, _, _ := a.serverAuthorizationPending(); pending {
		t.Fatal("403 sem a assinatura de agente pendente não pode marcar espera de liberação")
	}
}

// A confirmação do servidor (zeroTouchPending ausente ou false) libera a UI.
func TestServerAuthorizationClearedByConfirmedConfiguration(t *testing.T) {
	a := provisionedApp(t, true)
	body := []byte(`{"error":"Agent registration is pending (zero-touch)."}`)
	a.noteServerAuthorizationRefusal(approvalResponse(), body, nil)
	if pending, _, _ := a.serverAuthorizationPending(); !pending {
		t.Fatal("pré-condição: deveria estar pendente")
	}

	a.serverAuthorizationConfirmed()

	if pending, _, _ := a.serverAuthorizationPending(); pending {
		t.Fatal("configuração confirmada pelo servidor precisa LIMPAR a pendência")
	}
	st := a.GetOnboardingStatus()
	if got, _ := st["mode"].(string); got != "normal" {
		t.Fatalf("após confirmação, mode = %q, esperado 'normal'", got)
	}
}

// Instalação que NUNCA foi autorizada + servidor inalcançável = o caso "acabou
// de receber o token" do relato: a UI mostra espera, não erro de API.
func TestServerAuthorizationMarksUnreachableWhenNeverAuthorized(t *testing.T) {
	a := provisionedApp(t, false)

	a.noteServerAuthorizationRefusal(nil, nil, &fakeNetError{})

	pending, reason, _ := a.serverAuthorizationPending()
	if !pending {
		t.Fatal("instalação nunca autorizada sem resposta do servidor precisa entrar em espera")
	}
	if reason != onboardingPendingReasonServerUnreachable {
		t.Fatalf("reason = %q, esperado %q", reason, onboardingPendingReasonServerUnreachable)
	}
}

// Depois de já ter sido autorizado uma vez, uma queda de rede é só offline —
// não pode reinstalar a tela de espera a cada oscilação.
func TestServerAuthorizationKeepsVerifiedInstallationOutOfPending(t *testing.T) {
	a := provisionedApp(t, true)

	a.noteServerAuthorizationRefusal(nil, nil, &fakeNetError{})

	if pending, _, _ := a.serverAuthorizationPending(); pending {
		t.Fatal("instalação já autorizada com rede fora não pode virar 'aguardando liberação'")
	}
}

// O snapshot IPC (UI companion) precisa carregar a razão da espera: sem ela a
// tela não distingue "sem credenciais" de "servidor não liberou".
func TestOnboardingSnapshotCarriesPendingReason(t *testing.T) {
	a := provisionedApp(t, true)
	// Companion: a UI não roda o sync, então adota o estado que o SERVIÇO manda
	// no snapshot IPC.
	a.ipcClient = &IPCClient{}
	a.storeCompanionOnboarding("awaiting-approval", true, onboardingApprovalMessage, onboardPendingReasonApproval)
	st := a.GetOnboardingStatus()

	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("estado de onboarding precisa serializar: %v", err)
	}
	if !strings.Contains(string(raw), onboardPendingReasonApproval) {
		t.Fatalf("estado de onboarding sem pendingReason: %s", raw)
	}
	if got, _ := st["mode"].(string); got != "awaiting-approval" {
		t.Fatalf("companion precisa adotar o modo do serviço, veio %q", got)
	}
}

// fakeNetError implementa net.Error para simular falha de transporte.
type fakeNetError struct{}

func (e *fakeNetError) Error() string   { return "dial tcp: connection refused" }
func (e *fakeNetError) Timeout() bool   { return false }
func (e *fakeNetError) Temporary() bool { return false }
