package app

import (
	"testing"
	"time"

	"discovery/app/supportchat"
)

// REQUISITO (revisão 21/10): "Fechar" numa conversa JÁ ENCERRADA (histórico em
// leitura) não pode chamar /sessions/{id}/end de novo. O servidor devolve erro
// de sessão inativa, o agent tenta 3x e o frontend mostra "Falha ao encerrar" —
// o usuário fica preso na aba. Fechar é local e idempotente.
func TestSupportChatConfirmClose_ConversaEncerradaNaoBateNoServidor(t *testing.T) {
	// API inalcançável de propósito: se o binding tentasse encerrar, o teste
	// falharia pelo tempo (timeouts/retries) e/ou pelo erro devolvido.
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")
	svc := a.supportChatService()
	svc.AdoptSnapshot(supportchat.State{
		Status:      supportchat.StatusEnded,
		SessionID:   "s1",
		SupportName: "tecnico",
	})
	if got := svc.State().Status; got != supportchat.StatusEnded {
		t.Fatalf("pré-condição: status deveria ser ended, obtido %q", got)
	}

	start := time.Now()
	err := a.SupportChatConfirmClose()
	if err != nil {
		t.Fatalf("fechar conversa encerrada não pode devolver erro: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fechar conversa encerrada não pode fazer rede/retries (levou %s)", elapsed)
	}
	if got := svc.State().Status; got != supportchat.StatusNone {
		t.Fatalf("o estado local deveria ser limpo no fechamento; obtido %q", got)
	}
	if msgs := svc.State().Messages; len(msgs) != 0 {
		t.Fatalf("o transcript local deveria ser descartado; restaram %d mensagem(ns)", len(msgs))
	}
}

// REQUISITO (revisão 21/10): o /end remoto é best-effort. Se o servidor estiver
// inalcançável, o fechamento LOCAL (limpar estado + fechar a aba/janela) já
// aconteceu; devolver erro só mostrava "Falha ao encerrar" e prendia o usuário
// na tela por causa de uma sessão que expira sozinha no servidor.
func TestSupportChatConfirmClose_FalhaRemotaNaoImpedeFechamentoLocal(t *testing.T) {
	// 127.0.0.1:1 recusa conexão rápido: o End tenta 3x, falha, e o binding
	// ainda precisa devolver nil porque o fechamento local foi concluído.
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")
	svc := a.supportChatService()
	svc.AdoptSnapshot(supportchat.State{
		Status:    supportchat.StatusActive,
		SessionID: "s1",
	})
	if got := svc.State().Status; got != supportchat.StatusActive {
		t.Fatalf("pré-condição: status deveria ser active, obtido %q", got)
	}

	if err := a.SupportChatConfirmClose(); err != nil {
		t.Fatalf("falha no /end remoto não pode impedir o fechamento local: %v", err)
	}
	if got := svc.State().Status; got != supportchat.StatusNone {
		t.Fatalf("o estado local deveria ser limpo; obtido %q", got)
	}
}

// Os bindings validam o caminho ANTES de executar qualquer abertura (os erros
// abaixo não tocam no Explorer).
func TestSupportChatOpenRevealAttachment_ValidaCaminho(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")

	if err := a.SupportChatOpenAttachment(""); err == nil {
		t.Fatal("caminho vazio precisa ser recusado")
	}
	if err := a.SupportChatOpenAttachment("Z:\\definitivamente\\nao\\existe\\arquivo.pdf"); err == nil {
		t.Fatal("arquivo inexistente precisa ser recusado")
	}
	if err := a.SupportChatRevealAttachment(""); err == nil {
		t.Fatal("caminho vazio precisa ser recusado (reveal)")
	}
	if err := a.SupportChatRevealAttachment("Z:\\definitivamente\\nao\\existe\\arquivo.pdf"); err == nil {
		t.Fatal("arquivo inexistente precisa ser recusado (reveal)")
	}
	dir := t.TempDir()
	if err := a.SupportChatOpenAttachment(dir); err == nil {
		t.Fatal("abrir uma PASTA precisa ser recusado (é arquivo que se abre)")
	}
}
