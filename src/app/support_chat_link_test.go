package app

import (
	"testing"
	"time"

	"discovery/app/supportchat"
)

// ── Prorrogação do prazo "convite aguardando a UI" ─────────────────────────
//
// RELATO: "mando abrir o chat e demora". No log desta máquina a interface levou
// ~54 s para subir (serviço → self-update → IPC → WebView2) e o convite era
// RECUSADO em 25 s com "no_ui": o atendente via "usuário recusou" e o usuário
// nunca via o pedido — ele aparecia segundos depois da recusa.
//
// A prorrogação só vale com PROGRESSO visível de UI; sem progresso a recusa
// imediata continua (o suporte não pode ficar esperando à toa).

func TestSupportChatInviteExtend_SoComProgressoDeUI(t *testing.T) {
	if supportChatInviteUIExtend <= 0 {
		t.Fatal("a prorrogação precisa ser positiva")
	}
	if supportChatInviteUIExtend <= supportChatInviteUIWait {
		t.Fatalf("a prorrogação (%s) precisa cobrir o tempo real de subida da UI (log: ~54 s; espera atual %s)",
			supportChatInviteUIExtend, supportChatInviteUIWait)
	}
}

func TestSupportChatInviteExtend_NaoProgressoSemSinal(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")

	supportChatUIProgress.mu.Lock()
	supportChatUIProgress.lastAt = time.Time{}
	supportChatUIProgress.mu.Unlock()

	// Sem UI conectada e sem relançamento recente: a recusa no_ui deve valer na
	// hora — nada de prorrogar um convite que ninguém vai ver.
	if a.supportChatUISpawnInProgress() {
		t.Fatal("sem sinal de UI não há progresso para prorrogar o prazo")
	}
}

func TestSupportChatInviteExtend_ComRelancamentoRecente(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")

	a.markSupportChatUISpawn("chat_invite")
	defer func() {
		supportChatUIProgress.mu.Lock()
		supportChatUIProgress.lastAt = time.Time{}
		supportChatUIProgress.mu.Unlock()
	}()

	if !a.supportChatUISpawnInProgress() {
		t.Fatal("UI relançada agora é progresso: o convite deve aguardar mais " +
			supportChatInviteUIExtend.String() + " em vez de recusar com no_ui")
	}
}

// Guarda de contrato: o status publicado no IPC precisa carregar o link REAL do
// canal — é o que a janela usa no lugar do Connected do próprio processo de UI.
func TestSupportChatStateEventPayload_LevaLinkRealDoCanal(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")

	a.setSupportChatLink(false)
	args := a.supportChatStateEventPayload(supportchat.State{
		Status:      supportchat.StatusActive,
		SessionID:   "s1",
		SupportName: "tecnico",
	})
	// UM argumento (mapa) — dois argumentos viram um slice em Event.Data e o
	// frontend (e o IPC) não enxergam mais os campos do estado.
	if len(args) != 1 {
		t.Fatalf("o evento deve carregar UM mapa; obtido %d argumento(s)", len(args))
	}
	payload, ok := args[0].(map[string]any)
	if !ok {
		t.Fatalf("o argumento precisa ser o mapa do estado; obtido %T", args[0])
	}
	if payload["status"] != supportchat.StatusActive {
		t.Fatalf("payload sem status do estado: %v", payload)
	}
	if payload["sessionId"] != "s1" {
		t.Fatalf("payload sem sessionId do estado: %v", payload)
	}
	realtime, ok := payload["realtime"].(map[string]any)
	if !ok {
		t.Fatalf("payload sem realtime: %v", payload)
	}
	// O serviço diz que o canal está fora; falta o link. O evento precisa
	// carregar essa informação — antes a janela só tinha o Connected do próprio
	// processo de UI (sempre false) e o aviso aparecia numa conversa aberta.
	if _, ok := realtime["active"]; !ok {
		t.Fatalf("realtime sem 'active': %v", realtime)
	}
	if down, _ := realtime["downSinceUtc"].(string); down == "" {
		t.Fatalf("link fora precisa informar desde quando: %v", realtime)
	}
}
