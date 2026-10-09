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
		{"Permitir esta captura", DecisionGranted},
		{"Negar", DecisionDenied},
		{"sim", DecisionGranted},
		{"pode", DecisionGranted},
		{"Allow this capture", DecisionGranted},
		{"Deny", DecisionDenied},
		{"não", DecisionDenied},
		{"nao", DecisionDenied},
		{"Não autorizo", DecisionDenied},
		{"", DecisionDenied},
		{"talvez", DecisionDenied},
		// "sempre" NÃO cria autorização permanente — vale só para esta captura.
		{"Permitir sempre", DecisionGranted},
		// Autorização de SESSÃO: exige menção explícita a "sessão/session".
		{"Permitir sempre nesta sessão", DecisionGrantedForSession},
		{"Always allow for this session", DecisionGrantedForSession},
		{"Permitir siempre en esta sesión", DecisionGrantedForSession},
		// Redação atual da opção (permissão permanente até desativar).
		{"Permitir sempre (até eu desativar)", DecisionGrantedForSession},
		{"Always allow (until I turn it off)", DecisionGrantedForSession},
		{"Permitir siempre (hasta que lo desactive)", DecisionGrantedForSession},
		// Fail-closed: a negação vence mesmo citando a sessão.
		{"Não, não permitir nesta sessão", DecisionDenied},
	}
	for _, tc := range cases {
		if got := ClassifyConsentAnswer(tc.answer); got != tc.want {
			t.Errorf("ClassifyConsentAnswer(%q) = %q, want %q", tc.answer, got, tc.want)
		}
	}
}

// Requisito de produto (2026-10-01): a IA pergunta TODAS as vezes, mesmo que a
// captura anterior tenha sido autorizada.
func TestConsentAsksEveryTime(t *testing.T) {
	var calls int32
	prompt := func(ctx context.Context, question string, options []string) (string, error) {
		atomic.AddInt32(&calls, 1)
		if len(options) != 3 {
			t.Errorf("opcoes = %v, want 3 (permitir/permitir na sessao/negar)", options)
		}
		return "Permitir esta captura", nil
	}
	m := NewConsentManager(prompt, nil, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		decision, err := m.Ensure(ctx, "diagnostico")
		if err != nil || decision != DecisionGranted {
			t.Fatalf("Ensure #%d = (%q, %v), want (granted, nil)", i+1, decision, err)
		}
	}
	if calls != 3 {
		t.Fatalf("prompt chamado %d vezes, want 3 (uma por captura)", calls)
	}
}

// Negar vale só para a captura em questão: o próximo pedido pergunta de novo.
func TestConsentDenialIsPerCapture(t *testing.T) {
	var calls int32
	prompt := func(ctx context.Context, question string, options []string) (string, error) {
		atomic.AddInt32(&calls, 1)
		if atomic.LoadInt32(&calls) == 1 {
			return "Negar", nil
		}
		return "Permitir esta captura", nil
	}
	m := NewConsentManager(prompt, nil, nil)
	ctx := context.Background()

	if _, err := m.Ensure(ctx, ""); err == nil {
		t.Fatal("esperava erro quando o usuario nega")
	}
	decision, err := m.Ensure(ctx, "")
	if err != nil || decision != DecisionGranted {
		t.Fatalf("segundo pedido = (%q, %v), want (granted, nil) — a negativa nao pode ser permanente", decision, err)
	}
	if calls != 2 {
		t.Fatalf("prompt chamado %d vezes, want 2", calls)
	}
}

// A opção de sessão precisa voltar como DecisionGrantedForSession (sem erro) e
// NÃO pode virar autorização permanente: quem guarda o estado de sessão é o
// app, e a captura de cada pedido continua registrada na auditoria.
func TestConsentSessionOptionGrantsSessionWithoutError(t *testing.T) {
	var calls int32
	prompt := func(ctx context.Context, question string, options []string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "Permitir sempre (até eu desativar)", nil
	}
	m := NewConsentManager(prompt, nil, nil)
	decision, err := m.Ensure(context.Background(), "diagnostico")
	if err != nil || decision != DecisionGrantedForSession {
		t.Fatalf("Ensure = (%q, %v), want (%q, nil)", decision, err, DecisionGrantedForSession)
	}

	// A decisão de sessão não é estado interno do manager: o próximo Ensure
	// volta a perguntar (o curto-circuito de sessão é do app, na tool).
	decision, err = m.Ensure(context.Background(), "diagnostico")
	if err != nil || decision != DecisionGrantedForSession {
		t.Fatalf("segundo Ensure = (%q, %v)", decision, err)
	}
	if calls != 2 {
		t.Fatalf("prompt chamado %d vezes, want 2 (manager nao guarda sessao)", calls)
	}
}

func TestConsentDialogLocalized(t *testing.T) {
	pt := BuildConsentQuestion("diagnóstico de erro", "pt-BR")
	if !strings.Contains(pt, "Permite esta captura?") || !strings.Contains(pt, "diagnóstico de erro") {
		t.Fatalf("pergunta pt inesperada: %q", pt)
	}
	en := BuildConsentQuestion("error diagnosis", "en-US")
	if !strings.Contains(en, "Allow this capture?") {
		t.Fatalf("pergunta en inesperada: %q", en)
	}
	es := BuildConsentQuestion("diagnóstico", "es-ES")
	if !strings.Contains(es, "Permitir esta captura?") {
		t.Fatalf("pergunta es inesperada: %q", es)
	}
	if got := ConsentOptions("en-US"); len(got) != 3 || got[0] != "Allow this capture" ||
		got[1] != "Always allow (until I turn it off)" || got[2] != "Deny" {
		t.Fatalf("opcoes en inesperadas: %v", got)
	}
	if got := ConsentOptions("es-ES"); len(got) != 3 || got[0] != "Permitir esta captura" ||
		got[1] != "Permitir siempre (hasta que lo desactive)" || got[2] != "Negar" {
		t.Fatalf("opcoes es inesperadas: %v", got)
	}
	if got := ConsentOptions("pt-BR"); len(got) != 3 || got[1] != "Permitir sempre (até eu desativar)" {
		t.Fatalf("opcoes pt inesperadas: %v", got)
	}
	if got := ConsentLanguage("de-DE"); got != "en" {
		t.Fatalf("ConsentLanguage(de-DE) = %q, want en", got)
	}
	if got := ConsentLanguage(""); got != "pt" {
		t.Fatalf("ConsentLanguage(vazio) = %q, want pt", got)
	}
}

func TestConsentUsesPreferredLocale(t *testing.T) {
	var asked string
	prompt := func(ctx context.Context, question string, options []string) (string, error) {
		asked = question
		return "Allow this capture", nil
	}
	m := NewConsentManager(prompt, nil, func() string { return "en-US" })
	if _, err := m.Ensure(context.Background(), "disk error"); err != nil {
		t.Fatalf("Ensure falhou: %v", err)
	}
	if !strings.Contains(asked, "Allow this capture?") {
		t.Fatalf("pergunta deveria estar em inglês: %q", asked)
	}
}

// Concorrência: cada pedido pergunta, mas NUNCA dois diálogos ao mesmo tempo.
func TestConsentConcurrentEnsureSerialized(t *testing.T) {
	var calls int32
	var inFlight int32
	var maxInFlight int32
	prompt := func(ctx context.Context, question string, options []string) (string, error) {
		atomic.AddInt32(&calls, 1)
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			max := atomic.LoadInt32(&maxInFlight)
			if cur <= max || atomic.CompareAndSwapInt32(&maxInFlight, max, cur) {
				break
			}
		}
		atomic.AddInt32(&inFlight, -1)
		return "Permitir esta captura", nil
	}
	m := NewConsentManager(prompt, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Ensure(context.Background(), "captura concorrente")
		}()
	}
	wg.Wait()
	if calls != 8 {
		t.Fatalf("prompt chamado %d vezes, want 8 (um por pedido)", calls)
	}
	if maxInFlight != 1 {
		t.Fatalf("dialogos simultaneos = %d, want 1 (promptMu)", maxInFlight)
	}
}

// Ao mudar para miniaturas WebP, o MIME precisa acompanhar a entrada de
// auditoria — o painel de privacidade montava "data:image/jpeg" fixo e a
// miniatura saía quebrada.
func TestConsentAuditKeepsThumbnailMime(t *testing.T) {
	m := NewConsentManager(nil, nil, nil)
	entry := m.RecordCapture(AuditEntry{Mode: "window", Thumbnail: []byte{1, 2, 3}, ThumbnailMIME: "image/webp"})
	if !entry.HasThumbnail {
		t.Fatal("HasThumbnail deveria ser true com miniatura")
	}
	if entry.ThumbnailMIME != "image/webp" {
		t.Fatalf("ThumbnailMIME = %q, want image/webp", entry.ThumbnailMIME)
	}
}

func TestConsentAuditRing(t *testing.T) {
	m := NewConsentManager(nil, nil, nil)
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
	if audit[len(audit)-1].ID == 0 {
		t.Fatal("entrada de auditoria sem ID")
	}
}
