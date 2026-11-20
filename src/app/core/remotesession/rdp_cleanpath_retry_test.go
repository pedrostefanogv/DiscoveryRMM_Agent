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
	loopIdx := strings.Index(body, "for {")

	// REASSINATURA POR TENTATIVA: quando o transporte da iteracao e nulo (caso
	// normal a partir da 2a tentativa), precisa assinar de novo — Session.Run
	// fecha o transporte ao sair, entao a reconexao ficaria sem assinante.
	if !strings.Contains(body, "transport == nil") {
		t.Error("sem reassinatura por tentativa: reconexao ficaria sem assinante")
	}
	if !strings.Contains(body, "subscribeRdpCleanPath") {
		t.Error("nao reusa subscribeRdpCleanPath: assinatura inconsistente")
	}
	// A sessao precisa ser criada DENTRO do loop.
	sessionIdx := strings.Index(body, "rdcleanpath.NewSession")
	if sessionIdx < loopIdx {
		t.Error("Session criada FORA do loop: estado ficaria em StateClosed")
	}
	// Deve zerar o transporte apos usar, para a proxima iteracao reassinar.
	if !strings.Contains(body, "transport = nil") {
		t.Error("transporte nao e zerado apos a sessao: retry usaria transporte fechado")
	}
	// Deve sair do loop quando o contexto encerra.
	if !strings.Contains(body, "ctx.Err() != nil") {
		t.Error("loop sem checagem de contexto: nao encerraria com stop da sessao")
	}
}

// TestRdpAssinaCleanpathAntesDoReady trava a CORRECAO DA ORDEM, que era a causa
// do "timeout aguardando request" em produção.
//
// O site trata .proxy.ready como "pode conectar" e abre o WebSocket em seguida,
// e o IronRDP publica o RDCleanPath Request imediatamente. Se a assinatura de
// .rdp.cleanpath.in acontecer DEPOIS, o Request chega ao NATS sem assinante e é
// descartado (o NATS não retém mensagens) — o agent então espera 15 s em vão.
//
// No log de produção a inversão era visível: PublishProxyReady às 00:06:58.975 e
// SubscribeToRdpCleanpathIn só às 00:06:59.053 (78 ms depois).
func TestRdpAssinaCleanpathAntesDoReady(t *testing.T) {
	src, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("ler manager.go: %v", err)
	}
	code := string(src)

	idx := strings.Index(code, "func (m *Manager) runRdpSession")
	if idx < 0 {
		t.Fatal("runRdpSession nao encontrada")
	}
	body := code[idx:]
	if end := strings.Index(body, "\n// subscribeRdpCleanPath"); end > 0 {
		body = body[:end]
	}

	subscribeIdx := strings.Index(body, "m.subscribeRdpCleanPath(")
	readyIdx := strings.Index(body, "PublishProxyReady(")
	if subscribeIdx < 0 {
		t.Fatal("runRdpSession nao assina o cleanpath: WebRDP nao receberia requests")
	}
	if readyIdx < 0 {
		t.Fatal("runRdpSession nao publica proxy.ready")
	}
	if subscribeIdx > readyIdx {
		t.Error("ORDEM INVERTIDA: publica proxy.ready ANTES de assinar .rdp.cleanpath.in; " +
			"o Request do IronRDP pode ser publicado sem assinante e ser perdido")
	}
	// O transporte assinado precisa ser ENTREGUE ao runner (senao assinamos e
	// descartamos, deixando o handler orfao).
	if !strings.Contains(body, "rdpTransport") {
		t.Error("transporte assinado nao e passado ao runRdpCleanPath")
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
