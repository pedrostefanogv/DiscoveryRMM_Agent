package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"discovery/app/core/mcp"
)

// ─── FIX-6 / D5: o orçamento do turno não pode ser estourado ───────────────

// TestClampToBudget cobre a regra central do D5: nem o timeout do round nem o
// de cada tool podem ultrapassar o que resta do orçamento total do turno.
// Produção: multi_round_done com latency_ms=731456 (12,2 min), 131 s ALÉM do
// orçamento de 600 s — o deadline só era checado no topo do loop.
func TestClampToBudget(t *testing.T) {
	cases := []struct {
		name       string
		timeout    time.Duration
		budget     time.Duration
		want       time.Duration
		wantClampe bool
	}{
		{"orçamento folgado: mantém o timeout", 180 * time.Second, 600 * time.Second, 180 * time.Second, false},
		{"orçamento menor: usa o restante", 240 * time.Second, 90 * time.Second, 90 * time.Second, true},
		{"exatamente igual: mantém", 180 * time.Second, 180 * time.Second, 180 * time.Second, false},
		{"orçamento zerado: piso mínimo", 240 * time.Second, 0, minBudgetTimeout, true},
		{"orçamento negativo: piso mínimo", 240 * time.Second, -5 * time.Second, minBudgetTimeout, true},
		{"restante abaixo do piso: usa o restante, nunca o piso", 240 * time.Second, time.Millisecond, time.Millisecond, true},
		{"restante de 1s: não é elevado ao piso", time.Second, 500 * time.Millisecond, 500 * time.Millisecond, true},
		{"teto de 30 min clampado pelo turno", interactiveToolTimeout, 4 * time.Minute, 4 * time.Minute, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, clamped := clampToBudget(c.timeout, c.budget)
			if got != c.want {
				t.Fatalf("clampToBudget(%s, %s) = %s, esperado %s", c.timeout, c.budget, got, c.want)
			}
			if clamped != c.wantClampe {
				t.Fatalf("clamped = %v, esperado %v", clamped, c.wantClampe)
			}
			// Contexto com prazo ~0 cancelaria a operação antes de começar
			// (o servidor nem receberia o request), virando falha opaca.
			if got <= 0 {
				t.Fatalf("timeout efetivo precisa ser > 0: %s", got)
			}
		})
	}
}

// TestClampToBudget_NeverExceedsBudget garante a INVARIANTE do D5: com
// orçamento POSITIVO o timeout efetivo nunca o ultrapassa. Com orçamento
// esgotado vale o piso minBudgetTimeout, e o estouro é limitado exatamente a
// ele — nunca ao timeout originalmente pedido (que pode ser 30 min).
func TestClampToBudget_NeverExceedsBudget(t *testing.T) {
	timeouts := []time.Duration{time.Second, 60 * time.Second, 180 * time.Second, 240 * time.Second, interactiveToolTimeout}
	budgets := []time.Duration{0, time.Second, 30 * time.Second, 3 * time.Minute, 10 * time.Minute, time.Hour}
	for _, timeout := range timeouts {
		for _, budget := range budgets {
			got, _ := clampToBudget(timeout, budget)
			if budget > 0 && got > budget {
				t.Fatalf("timeout %s excedeu o orçamento positivo %s (efetivo %s)", timeout, budget, got)
			}
			if budget <= 0 {
				// Orçamento esgotado: o único estouro admitido é o piso.
				if got > minBudgetTimeout {
					t.Fatalf("orçamento esgotado deveria limitar ao piso %s, obtido %s", minBudgetTimeout, got)
				}
				if got != minBudgetTimeout {
					t.Fatalf("orçamento esgotado deveria devolver o piso %s, obtido %s", minBudgetTimeout, got)
				}
				continue
			}
			if got > timeout {
				t.Fatalf("clamp AUMENTOU o timeout: %s -> %s", timeout, got)
			}
		}
	}
}

// TestClampToBudget_NormalTurnIsNotClamped garante que o clamp não dispara em
// turnos normais: o maior timeout de round (240s) cabe folgado no orçamento de
// 600s. Sem esta checagem, um erro de sinal geraria log de "reduzido" em toda
// linha de round e encheria o chat.db de ruído.
func TestClampToBudget_NormalTurnIsNotClamped(t *testing.T) {
	full := maxMultiRoundTotal // 600s, turno recém-iniciado
	for round := 0; round < maxMultiRounds; round++ {
		got, clamped := clampToBudget(roundTimeout(round), full)
		if clamped {
			t.Fatalf("round %d: clamp disparou com orçamento cheio (timeout %s)", round, got)
		}
		if got != roundTimeout(round) {
			t.Fatalf("round %d: timeout alterado sem necessidade: %s -> %s", round, roundTimeout(round), got)
		}
	}
	// A tool comum (60s) também não é clavada com orçamento cheio.
	if _, clamped := clampToBudget(60*time.Second, full); clamped {
		t.Fatal("timeout padrão de tool não deveria ser clavado com orçamento cheio")
	}
}

// ─── FIX-6 / D6: cancelamento pelo nosso prazo vs falha real ───────────────

// TestToolDeadlineCause distingue o cancelamento causado pelo NOSSO deadline de
// uma falha própria da tool. Produção: tool_exec_error id=497 com 114027 ms —
// o timeout do round (180/210/240 s) cancelava o streamCtx, a tool devolvia
// "context canceled", esse texto ia ao servidor e o turno era descartado.
func TestToolDeadlineCause(t *testing.T) {
	bg := context.Background()
	deadlineCtx, cancelDeadline := context.WithDeadline(bg, time.Now().Add(-time.Second))
	defer cancelDeadline()
	canceledCtx, cancelNow := context.WithCancel(bg)
	cancelNow()

	cases := []struct {
		name    string
		err     error
		ctx     context.Context
		budget  time.Duration
		want    string
		comment string
	}{
		{
			name: "tool venceu o próprio deadline", err: context.DeadlineExceeded,
			ctx: bg, budget: 5 * time.Minute, want: "deadline",
		},
		{
			name: "erro embrulhado de deadline", err: fmt.Errorf("falha: %w", context.DeadlineExceeded),
			ctx: bg, budget: 5 * time.Minute, want: "deadline",
		},
		{
			name: "cancelado e orçamento JÁ esgotado", err: context.Canceled,
			ctx: canceledCtx, budget: 0, want: "turno_sem_orcamento",
		},
		{
			name: "cancelado com orçamento NEGATIVO", err: context.Canceled,
			ctx: canceledCtx, budget: -2 * time.Second, want: "turno_sem_orcamento",
		},
		{
			name: "texto cru 'context canceled' com orçamento esgotado",
			err:  errors.New("rpc error: code = Canceled desc = context canceled"),
			ctx:  bg, budget: 0, want: "turno_sem_orcamento",
		},
		{
			name: "ctx da tool vencido (mesmo com erro textual genérico)",
			err:  errors.New("comando interrompido"),
			ctx:  deadlineCtx, budget: 2 * time.Minute, want: "deadline",
		},
		{
			// Botão "Parar" do usuário: cancelou com orçamento DISPONÍVEL.
			// Não é estouro de prazo e não deve virar mensagem de timeout.
			name: "usuário clicou Parar (orçamento disponível)", err: context.Canceled,
			ctx: canceledCtx, budget: 3 * time.Minute, want: "",
			comment: "cancelamento do usuário não é deadline",
		},
		{
			name: "falha própria da tool", err: errors.New("winget não encontrado"),
			ctx: bg, budget: 3 * time.Minute, want: "",
		},
		{
			name: "erro nil", err: nil, ctx: bg, budget: time.Minute, want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toolDeadlineCause(c.err, c.ctx, c.budget); got != c.want {
				t.Fatalf("toolDeadlineCause = %q, esperado %q (%s)", got, c.want, c.comment)
			}
		})
	}
}

// TestMultiRound_ToolDeadlineIsNotFatal garante que uma tool interrompida pelo
// NOSSO prazo (a) não derruba o turno e (b) chega ao servidor como resultado
// EXPLICATIVO, não como "context canceled" opaco.
func TestMultiRound_ToolDeadlineIsNotFatal(t *testing.T) {
	var round int
	var secondRequest agentStreamRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		round++
		if round == 1 {
			_, _ = fmt.Fprint(w,
				"data: {\"type\":\"tool_call\",\"toolCallId\":\"c1\",\"toolName\":\"get_inventory\",\"toolArguments\":\"{}\"}\n"+
					"data: {\"type\":\"round_end\",\"sessionId\":\"sess-to\"}\n")
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&secondRequest)
		_, _ = fmt.Fprint(w, "data: {\"type\":\"done\",\"content\":\"concluido\",\"sessionId\":\"sess-to\"}\n")
	}))
	defer server.Close()

	s := NewService(mcp.NewRegistry())
	s.SetConfig(Config{
		Endpoint: server.URL,
		APIKey:   "mdz_test-token",
		AgentID:  "11111111-1111-1111-1111-111111111111",
	})
	// Devolve EXATAMENTE o erro que a produção registrou (tool_exec_error id=497):
	// o contexto venceu, ctx.Err() == context.DeadlineExceeded.
	executor := func(ctx context.Context, toolName, argsJSON string) (string, error) {
		short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()
		<-short.Done()
		return "", short.Err()
	}

	assistant, err := s.SendStreamMultiRound(context.Background(), "verifique o pc", nil, nil, executor)
	if err != nil {
		t.Fatalf("tool interrompida pelo prazo não deveria derrubar o turno: %v", err)
	}
	if strings.TrimSpace(assistant) == "" {
		t.Fatal("turno encerrou sem resposta")
	}

	// O tool result enviado ao servidor precisa explicar a interrupção.
	if len(secondRequest.ToolResults) == 0 {
		t.Fatal("o round 2 não recebeu tool results")
	}
	sent := secondRequest.ToolResults[0].Result
	if !strings.Contains(sent, "prazo do turno") {
		t.Fatalf("tool result não explica a interrupção pelo prazo: %s", sent)
	}
	if !strings.Contains(sent, "\"retryable\":true") {
		t.Fatalf("tool result deveria marcar retryable=true: %s", sent)
	}
}
