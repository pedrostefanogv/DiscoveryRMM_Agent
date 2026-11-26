//go:build windows

package remotesession

import "testing"

// A entrada do data channel precisa chegar ao MESMO handler do .term.in.
func TestSessionTerminal_DeliverInputUsaOMesmoHandler(t *testing.T) {
	st := NewSessionTerminal("sess-term-1", nil, false)

	if st.DeliverInput([]byte("antes")) {
		t.Fatal("sem handler registrado, DeliverInput deveria devolver false")
	}

	var recebido []string
	st.SetInputHandler(func(data []byte) { recebido = append(recebido, string(data)) })

	if !st.DeliverInput([]byte("ola")) {
		t.Fatal("com handler registrado, DeliverInput deveria devolver true")
	}
	if len(recebido) != 1 || recebido[0] != "ola" {
		t.Fatalf("handler recebeu %v, esperava [ola]", recebido)
	}
}

// A saída precisa PREFERIR o data channel e, quando ele aceita, não tocar o
// NATS. O natsStream é nil de propósito: se publishOut escorregar para o NATS
// com o canal ativo, o teste entra em pânico em vez de passar em silêncio.
func TestSessionTerminal_PublishOutPrefereODataChannel(t *testing.T) {
	st := NewSessionTerminal("sess-term-2", nil, false)

	var enviado []string
	st.SetWebRtcSend(func(payload string) bool {
		enviado = append(enviado, payload)
		return true
	})

	if err := st.publishOut("saida"); err != nil {
		t.Fatalf("publishOut devolveu erro: %v", err)
	}
	if len(enviado) != 1 || enviado[0] != "saida" {
		t.Fatalf("data channel recebeu %v, esperava [saida]", enviado)
	}
}

// Regressão (commit ec14f20): o fallback para o NATS era st.publishOut(...), isto
// é, a própria função — recursão infinita e "fatal error: stack overflow" sempre
// que o data channel NÃO aceita (o caso normal com WebRTC desligado). Antes da
// correção este teste derrubava o binário de teste por stack overflow.
func TestSessionTerminal_PublishOutSemCanalNemNATSNaoRecursa(t *testing.T) {
	st := NewSessionTerminal("sess-term-4", nil, false)
	st.SetWebRtcSend(func(string) bool { return false })

	err := st.publishOut("saida")
	if err == nil {
		t.Fatal("sem data channel que aceite e sem NATS, publishOut deveria devolver erro (e nao recursar)")
	}
}

// Mesma costura do StartRunAs: o setter é chamado pelo manager FORA de qualquer
// lock do terminal. Mantido como teste direto porque o deadlock que já ocorreu
// aqui era exatamente "setter chamado com st.mu preso" — e sync.RWMutex não é
// reentrante.
func TestSessionTerminal_SettersNaoDependemDeLockExterno(t *testing.T) {
	st := NewSessionTerminal("sess-term-3", nil, false)

	st.SetWebRtcSend(func(string) bool { return false })
	st.SetInputHandler(func([]byte) {})

	// Se qualquer um dos setters tivesse deixado st.mu travado, o RLock abaixo
	// bloquearia para sempre (o go test encerraria por timeout).
	_ = st.HasTerminal()
}
