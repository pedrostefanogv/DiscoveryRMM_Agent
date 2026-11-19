package remotesession

import (
	"os"
	"strings"
	"testing"
)

// TestRdpCleanPathServesMultipleAttempts garante que o agent atende REPETIDAS
// conexoes WebRDP na mesma sessao.
//
// Bug corrigido: runRdpCleanPath criava UMA Session e retornava. Como cada
// Session consome um unico request e encerra, a segunda tentativa do operador
// ("Tentar novamente" apos falha, ou reconexao) ficava SEM RESPOSTA — o subject
// .rdp.cleanpath.in nao tinha mais assinante e o canvas ficava preto.
//
// A correcao introduz um loop de supervisao que recria transporte e sessao.
func TestRdpCleanPathServesMultipleAttempts(t *testing.T) {
	src, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("ler manager.go: %v", err)
	}
	code := string(src)

	idx := strings.Index(code, "func (m *Manager) runRdpCleanPath")
	if idx < 0 {
		t.Fatal("runRdpCleanPath nao encontrada")
	}
	body := code[idx:]
	if end := strings.Index(body, "\n// isAwaitRequestTimeout"); end > 0 {
		body = body[:end]
	}

	// O corpo deve ter um loop de supervisao.
	if !strings.Contains(body, "for {") {
		t.Error("runRdpCleanPath nao tem loop: so atenderia UMA tentativa")
	}
	// O transporte precisa ser criado DENTRO do loop (Session.Run fecha ao sair).
	loopIdx := strings.Index(body, "for {")
	transportIdx := strings.Index(body, "rdcleanpath.NewNatsTransport")
	if transportIdx < loopIdx {
		t.Error("transporte criado FORA do loop: a 2a tentativa nao teria assinatura")
	}
	// A sessao tambem precisa ser recriada por tentativa.
	sessionIdx := strings.Index(body, "rdcleanpath.NewSession")
	if sessionIdx < loopIdx {
		t.Error("Session criada FORA do loop: estado ficaria em StateClosed")
	}
	// Deve sair do loop quando o contexto encerra.
	if !strings.Contains(body, "ctx.Err() != nil") {
		t.Error("loop sem checagem de contexto: nao encerraria com stop da sessao")
	}
}

// TestIsAwaitRequestTimeout cobre o helper que evita logar erro no caso normal
// (operador abriu a aba e nao conectou).
func TestIsAwaitRequestTimeout(t *testing.T) {
	if !isAwaitRequestTimeout(errAwaitRequestForTest()) {
		t.Error("timeout de espera pelo request deveria ser reconhecido")
	}
	if isAwaitRequestTimeout(nil) {
		t.Error("nil nao e timeout")
	}
	if isAwaitRequestTimeout(errOtherForTest()) {
		t.Error("erro de outra natureza nao pode ser tratado como timeout")
	}
}
