package agentconn

import (
	"errors"
	"testing"
)

// TestIsPlannedReconnect garante que apenas os encerramentos planejados do
// loop de conexão são classificados como tal. Esses erros NÃO devem derrubar
// o indicador de status para offline (flicker online→offline→online da página
// de Status em homologação); falhas reais de rede continuam derrubando.
func TestIsPlannedReconnect(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"reload de configuração", errors.New("reload solicitado"), true},
		{"troca para NATS nativo", errors.New("NATS nativo disponivel (nats://srv:4222) — alternando de nats-wss para nats://"), true},
		{"watchdog de global pong", errors.New("watchdog global pong: sem sinal recente (6m0s)"), true},
		{"falha real de rede", errors.New("falha ao conectar NATS: dial tcp 1.2.3.4:4222: connectex: connection refused"), false},
		{"nenhum transporte configurado", errors.New("nenhum transporte configurado"), false},
		{"sessao genérica", errors.New("falha ao inscrever no subject de comando unicast: boom"), false},
	}
	for _, c := range cases {
		if got := isPlannedReconnect(c.err); got != c.want {
			t.Errorf("%s: isPlannedReconnect(%v) = %v, esperado %v", c.name, c.err, got, c.want)
		}
	}
}

// TestMarkReconnecting_PreservesConnectedState verifica que markReconnecting
// nunca LIGA o status a partir de offline e nunca DESLIGA a partir de online —
// apenas atualiza LastEvent (sem evento de conectividade para a UI).
func TestMarkReconnecting_PreservesConnectedState(t *testing.T) {
	r := &Runtime{}

	// Partindo de offline: permanece offline.
	r.markReconnecting("reload solicitado")
	if st := r.GetStatus(); st.Connected {
		t.Fatalf("markReconnecting partindo de offline não deve conectar: %+v", st)
	}

	// Partindo de online (sessão nats-wss ativa): permanece online, preserva
	// o transporte e registra o motivo no LastEvent.
	r.setStatusConnected(testHomologAgentID, "wss://srv:443/nats/", "nats-wss")
	r.markReconnecting("watchdog global pong: sem sinal recente (6m0s)")
	st := r.GetStatus()
	if !st.Connected {
		t.Fatalf("markReconnecting deve preservar Connected=true: %+v", st)
	}
	if st.Transport != "nats-wss" {
		t.Fatalf("markReconnecting deve preservar Transport: got %q", st.Transport)
	}
	if st.LastEvent == "" || !contains(st.LastEvent, "reconectando (planejado)") {
		t.Fatalf("markReconnecting deve registrar o motivo no LastEvent: got %q", st.LastEvent)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
