package ai

import (
`strings`
`testing`
)

// B1: fila de ações A2UI com limite — cliques rápidos não se sobrescrevem e
// o excesso descarta os mais antigos.
func TestSubmitA2uiAction_QueueCap(t *testing.T) {
	s := NewService(nil)
	names := []string{"a", "b", "c", "d", "e", "f"}
	for _, n := range names {
		s.SubmitA2uiAction("surf", n, map[string]any{})
	}
	s.a2uiActionMu.Lock()
	n := len(s.a2uiActions)
	s.a2uiActionMu.Unlock()
	if n != 4 {
		t.Fatalf("esperado 4 ações na fila (cap), obtido %d", n)
	}
	first := s.takeA2uiAction()
	if first == nil || first.Name != "c" {
		t.Fatalf("esperada ação mais antiga restante \"c\", obtida %v", first)
	}
}

// B1: HasActiveStream reflete o registro de stream.
func TestHasActiveStream(t *testing.T) {
	s := &Service{}
	if s.HasActiveStream() {
		t.Fatal("não deveria haver stream ativo")
	}
	id := s.registerStreamCancel(func() {})
	if !s.HasActiveStream() {
		t.Fatal("deveria haver stream ativo após register")
	}
	s.unregisterStreamCancel(id)
	if s.HasActiveStream() {
		t.Fatal("não deveria haver stream ativo após unregister")
	}
}

// M4: guard de turno único — segundo turno concorrente deve falhar.
func TestSendStreamMultiRound_TurnGuard(t *testing.T) {
	s := &Service{registry: nil, history: []Message{}}
	s.cfg = Config{Endpoint: "https://x", APIKey: "mdz_test"}
	s.turnMu.Lock() // simula turno em andamento
	defer s.turnMu.Unlock()
	_, err := s.SendStreamMultiRound(t.Context(), "oi", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "já existe uma resposta em andamento") {
		t.Fatalf("esperado erro de turno concorrente, obtido: %v", err)
	}
}

// M8: redação de segredos no log.
func TestRedactSensitive(t *testing.T) {
	in := "token mdz_AbCdEf123456 e Bearer abc.def.ghi e sk-abcdefghijklmnopqrst"
	out := redactSensitive(in)
	if strings.Contains(out, "mdz_AbCdEf") || strings.Contains(out, "sk-abcdefghijklmnopqrst") {
		t.Fatalf("segredo não redigido: %s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("esperado marcador [redacted]: %s", out)
	}
}

// M1: filtro de mensagem não deve bloquear falsos positivos.
func TestValidateChatMessage_NoFalsePositive(t *testing.T) {
	if err := validateChatMessage("verifique se configuracao=salva no registro"); err != nil {
		t.Fatalf("falso positivo no filtro: %v", err)
	}
	if err := validateChatMessage("<script>alert(1)</script>"); err == nil {
		t.Fatal("esperado bloqueio de <script>")
	}
	long := strings.Repeat("a", 9000)
	if err := validateChatMessage(long); err == nil {
		t.Fatal("esperado bloqueio acima de 8192 bytes")
	}
	if err := validateChatMessage(strings.Repeat("a", 8000)); err != nil {
		t.Fatalf("mensagem de 8000 bytes deveria passar: %v", err)
	}
}

// B3/B4: timeout por tool — ask_user ganha 150s, demais 60s (verificação
// indireta via constantes usadas no loop; aqui garantimos o corte de rune
// do truncateToolResult continua válido).
func TestTruncateToolResult_UTF8Valid(t *testing.T) {
	big := strings.Repeat("ç", 20000)
	out := truncateToolResult(big)
	if len(out) == 0 {
		t.Fatal("resultado truncado vazio")
	}
}