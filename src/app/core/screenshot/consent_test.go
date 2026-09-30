package screenshot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestClassifyConsentAnswer(t *testing.T) {
	cases := []struct {
		answer string
		want   Decision
	}{
		{"Permitir nesta conversa", DecisionSession},
		{"Permitir sempre", DecisionAlways},
		{"Negar", DecisionDenied},
		{"sim", DecisionSession},
		{"pode", DecisionSession},
		{"não", DecisionDenied},
		{"nao", DecisionDenied},
		{"Não autorizo", DecisionDenied},
		{"", DecisionDenied},
		{"talvez", DecisionDenied},
		{"SEMPRE", DecisionAlways},
	}
	for _, tc := range cases {
		if got := ClassifyConsentAnswer(tc.answer); got != tc.want {
			t.Errorf("ClassifyConsentAnswer(%q) = %q, want %q", tc.answer, got, tc.want)
		}
	}
}

func TestConsentDialogLocalized(t *testing.T) {
	pt := BuildConsentQuestion("diagnóstico de erro", "pt-BR")
	if !strings.Contains(pt, "Permite a captura?") || !strings.Contains(pt, "diagnóstico de erro") {
		t.Fatalf("pergunta pt inesperada: %q", pt)
	}
	en := BuildConsentQuestion("error diagnosis", "en-US")
	if !strings.Contains(en, "Allow the capture?") {
		t.Fatalf("pergunta en inesperada: %q", en)
	}
	es := BuildConsentQuestion("diagnóstico", "es-ES")
	if !strings.Contains(es, "Permitir la captura?") {
		t.Fatalf("pergunta es inesperada: %q", es)
	}
	if got := ConsentOptions("en-US"); len(got) != 3 || got[0] != "Allow this conversation" {
		t.Fatalf("opcoes en inesperadas: %v", got)
	}
	if got := ConsentOptions("es-ES"); got[1] != "Permitir siempre" {
		t.Fatalf("opcoes es inesperadas: %v", got)
	}
	// Locale desconhecido cai para inglês; vazio mantém pt-BR (compat).
	if got := ConsentLanguage("de-DE"); got != "en" {
		t.Fatalf("ConsentLanguage(de-DE) = %q, want en", got)
	}
	if got := ConsentLanguage(""); got != "pt" {
		t.Fatalf("ConsentLanguage(vazio) = %q, want pt", got)
	}
	if ClassifyConsentAnswer("Allow this conversation") != DecisionSession {
		t.Fatal("resposta en deveria ser session")
	}
	if ClassifyConsentAnswer("Always allow") != DecisionAlways {
		t.Fatal("resposta en deveria ser always")
	}
	if ClassifyConsentAnswer("Permitir siempre") != DecisionAlways {
		t.Fatal("resposta es deberia ser always")
	}
	if ClassifyConsentAnswer("Deny") != DecisionDenied {
		t.Fatal("resposta en deveria ser denied")
	}
}

func TestConsentUsesPreferredLocale(t *testing.T) {
	var asked string
	prompt := func(ctx context.Context, question string, options []string) (string, error) {
		asked = question
		return "Allow this conversation", nil
	}
	m := NewConsentManager(prompt, Persist{}, nil, func() string { return "en-US" })
	if _, err := m.Ensure(context.Background(), "disk error"); err != nil {
		t.Fatalf("Ensure falhou: %v", err)
	}
	if !strings.Contains(asked, "Allow the capture?") {
		t.Fatalf("pergunta deveria estar em inglês: %q", asked)
	}
}

func TestConsentEnsureAsksOncePerSession(t *testing.T) {
	var calls int32
	prompt := func(ctx context.Context, question string, options []string) (string, error) {
		atomic.AddInt32(&calls, 1)
		if len(options) < 2 {
			t.Errorf("opcoes insuficientes: %v", options)
		}
		return "Permitir nesta conversa", nil
	}
	m := NewConsentManager(prompt, Persist{}, nil, nil)
	ctx := context.Background()

	d, err := m.Ensure(ctx, "diagnostico de erro")
	if err != nil || d != DecisionSession {
		t.Fatalf("Ensure = (%q, %v), want (session, nil)", d, err)
	}
	if !m.Authorized() {
		t.Fatal("esperava autorizado apos consentimento")
	}
	if calls != 1 {
		t.Fatalf("prompt chamado %d vezes, want 1", calls)
	}
	// Segunda captura não deve reabrir o diálogo.
	if _, err := m.Ensure(ctx, "outra captura"); err != nil {
		t.Fatalf("segundo Ensure falhou: %v", err)
	}
	if calls != 1 {
		t.Fatalf("prompt chamado %d vezes apos segunda captura, want 1", calls)
	}
}

func TestConsentDeniedBlocksAndRevokeReasks(t *testing.T) {
	prompt := func(ctx context.Context, question string, options []string) (string, error) {
		return "Negar", nil
	}
	m := NewConsentManager(prompt, Persist{}, nil, nil)
	if _, err := m.Ensure(context.Background(), ""); err == nil {
		t.Fatal("esperava erro quando o usuario nega")
	}
	if m.Status() != DecisionDenied {
		t.Fatalf("status = %q, want denied", m.Status())
	}
	if _, err := m.Ensure(context.Background(), ""); err == nil {
		t.Fatal("captura deve continuar bloqueada apos negar")
	}
	// Autorizar manualmente pela UI destrava.
	if err := m.Set(DecisionSession); err != nil {
		t.Fatalf("Set falhou: %v", err)
	}
	if _, err := m.Ensure(context.Background(), ""); err != nil {
		t.Fatalf("Ensure apos Set falhou: %v", err)
	}
}

func TestConsentAlwaysPersists(t *testing.T) {
	persisted := DecisionUndecided
	persist := Persist{
		Load: func() (Decision, error) { return persisted, nil },
		Save: func(d Decision) error { persisted = d; return nil },
	}
	prompt := func(ctx context.Context, question string, options []string) (string, error) {
		return "Permitir sempre", nil
	}
	m := NewConsentManager(prompt, persist, nil, nil)
	if _, err := m.Ensure(context.Background(), ""); err != nil {
		t.Fatalf("Ensure falhou: %v", err)
	}
	if persisted != DecisionAlways {
		t.Fatalf("persistido = %q, want always", persisted)
	}

	// Reinício do agente: carrega a decisão persistida sem novo diálogo.
	var calls int32
	m2 := NewConsentManager(func(ctx context.Context, q string, o []string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", fmt.Errorf("nao deveria perguntar")
	}, persist, nil, nil)
	if !m2.Authorized() {
		t.Fatal("esperava autorizacao persistida apos restart")
	}
	if _, err := m2.Ensure(context.Background(), ""); err != nil {
		t.Fatalf("Ensure apos restart falhou: %v", err)
	}
	if calls != 0 {
		t.Fatalf("prompt chamado %d vezes, want 0", calls)
	}

	// Revogar limpa a persistencia.
	if err := m2.Revoke(); err != nil {
		t.Fatalf("Revoke falhou: %v", err)
	}
	if persisted != DecisionUndecided {
		t.Fatalf("persistido apos revogar = %q, want undecided", persisted)
	}
}

func TestConsentConcurrentEnsureSinglePrompt(t *testing.T) {
	var calls int32
	prompt := func(ctx context.Context, question string, options []string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "Permitir nesta conversa", nil
	}
	m := NewConsentManager(prompt, Persist{}, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Ensure(context.Background(), "captura concorrente")
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("prompt chamado %d vezes, want 1 (serializacao)", calls)
	}
	if !m.Authorized() {
		t.Fatal("esperava autorizado")
	}
}

func TestConsentAuditRing(t *testing.T) {
	m := NewConsentManager(nil, Persist{}, nil, nil)
	for i := 0; i < auditMax+10; i++ {
		m.RecordCapture(AuditEntry{Mode: "full", Detail: fmt.Sprintf("captura %d", i)})
	}
	audit := m.Audit()
	if len(audit) != auditMax {
		t.Fatalf("audit len = %d, want %d", len(audit), auditMax)
	}
	if audit[len(audit)-1].Detail != fmt.Sprintf("captura %d", auditMax+9) {
		t.Fatalf("ultima entrada = %q", audit[len(audit)-1].Detail)
	}
}
