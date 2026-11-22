//go:build windows

package remotesession

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// Os canais de terminal e processos são novos: um LABEL errado deixaria o canal
// aberto e OCIOSO — o pior modo de falha, porque o selo ficaria verde enquanto o
// tráfego continuasse no NATS. Foi exatamente o que aconteceu na tela antes da
// correção do transport_info.
func TestMediaStack_RoteiaCanaisTerminalEProcesses(t *testing.T) {
	casos := []struct {
		nome     string
		label    string
		registra func(*WebRtcMediaStack, func([]byte))
	}{
		{
			nome:  "terminal",
			label: WebRtcChannelTerminal,
			registra: func(s *WebRtcMediaStack, fn func([]byte)) {
				s.SetTerminalHandler(fn)
			},
		},
		{
			nome:  "processes",
			label: WebRtcChannelProcesses,
			registra: func(s *WebRtcMediaStack, fn func([]byte)) {
				s.SetProcessesHandler(fn)
			},
		},
	}

	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			policy := WebRtcPolicy{Enabled: true, FallbackToNats: true}
			got := make(chan string, 1)

			stack := NewWebRtcMediaStack("sess-"+caso.nome, policy, nil, nil, nil, nil, nil)
			caso.registra(stack, func(d []byte) { got <- string(d) })
			defer stack.Close()

			viewerConn, err := webrtc.NewPeerConnection(webrtc.Configuration{})
			if err != nil {
				t.Fatalf("criar viewer PeerConnection: %v", err)
			}
			defer viewerConn.Close()

			dc, err := viewerConn.CreateDataChannel(caso.label, nil)
			if err != nil {
				t.Fatalf("criar data channel %s: %v", caso.label, err)
			}
			replyCh := make(chan string, 1)
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				replyCh <- string(msg.Data)
			})
			dc.OnOpen(func() {
				_ = dc.SendText("req")
			})

			offer, err := viewerConn.CreateOffer(nil)
			if err != nil {
				t.Fatalf("CreateOffer: %v", err)
			}
			gather := webrtc.GatheringCompletePromise(viewerConn)
			if err := viewerConn.SetLocalDescription(offer); err != nil {
				t.Fatalf("SetLocalDescription: %v", err)
			}
			<-gather

			answerSDP, err := stack.Negociate(viewerConn.LocalDescription().SDP)
			if err != nil {
				t.Fatalf("Negociate: %v", err)
			}
			if err := viewerConn.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeAnswer,
				SDP:  answerSDP,
			}); err != nil {
				t.Fatalf("SetRemoteDescription: %v", err)
			}

			// viewer → agente
			select {
			case msg := <-got:
				if msg != "req" {
					t.Fatalf("recebeu %q, esperava req", msg)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("timeout aguardando mensagem no canal %s", caso.label)
			}

			// agente → viewer
			if !stack.Send(caso.label, []byte("resp")) {
				t.Fatalf("Send em %s deveria retornar true", caso.label)
			}
			select {
			case msg := <-replyCh:
				if msg != "resp" {
					t.Fatalf("viewer recebeu %q, esperava resp", msg)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("timeout aguardando resposta no canal %s", caso.label)
			}
		})
	}
}

// O contrato com o console é o literal do label. Se um dos lados mudar sem o
// outro, o sintoma é "canal aberto e nada trafega" — sem erro em lugar nenhum.
func TestWebRtcChannelLabels_SaoOContratoComOConsole(t *testing.T) {
	if WebRtcChannelTerminal != "terminal" {
		t.Fatalf("label do terminal = %q, esperava %q", WebRtcChannelTerminal, "terminal")
	}
	if WebRtcChannelProcesses != "processes" {
		t.Fatalf("label de processos = %q, esperava %q", WebRtcChannelProcesses, "processes")
	}
}

// O manager despacha pelo sink registrado pela sessão. Sem este teste, um campo
// trocado no merge (ex.: onTerminal sobrescrevendo onProcesses) passaria
// despercebido até a falha em produção.
func TestManager_DespachaTerminalEProcessesParaOsSinks(t *testing.T) {
	m := NewManager(nil)

	gotTerminal := make(chan string, 1)
	gotProcesses := make(chan string, 1)

	m.RegisterWebRtcDataSink("sess-1", webrtcDataSink{
		onTerminal: func(d []byte) { gotTerminal <- string(d) },
	})
	m.RegisterWebRtcDataSink("sess-1", webrtcDataSink{
		onProcesses: func(d []byte) { gotProcesses <- string(d) },
	})

	m.handleWebRtcTerminal("sess-1", []byte("stdin"))
	m.handleWebRtcProcesses("sess-1", []byte("listProcesses"))

	select {
	case v := <-gotTerminal:
		if v != "stdin" {
			t.Fatalf("terminal recebeu %q", v)
		}
	default:
		t.Fatal("onTerminal nao foi chamado")
	}
	select {
	case v := <-gotProcesses:
		if v != "listProcesses" {
			t.Fatalf("processes recebeu %q", v)
		}
	default:
		t.Fatal("onProcesses nao foi chamado (merge sobrescreveu o callback?)")
	}
}

// Payload vazio não pode chegar ao handler: um frame vazio viraria uma
// requisição com JSON inválido e um erro espúrio no console.
func TestManager_IgnoraMensagemVaziaNosCanaisNovos(t *testing.T) {
	m := NewManager(nil)
	chamou := false
	m.RegisterWebRtcDataSink("sess-2", webrtcDataSink{
		onTerminal:  func([]byte) { chamou = true },
		onProcesses: func([]byte) { chamou = true },
	})

	m.handleWebRtcTerminal("sess-2", nil)
	m.handleWebRtcTerminal("sess-2", []byte{})
	m.handleWebRtcProcesses("sess-2", nil)

	if chamou {
		t.Fatal("payload vazio nao deveria chegar ao handler")
	}
}

// Sem sessão WebRTC não há stack: o enviador precisa devolver false para o
// chamador cair no NATS, em vez de entrar em pânico.
func TestWebRtcSendOn_SemSessaoDevolveFalse(t *testing.T) {
	m := NewManager(nil)
	send := m.webRtcSendOn("sess-inexistente", WebRtcChannelTerminal)
	if send([]byte("x")) {
		t.Fatal("sem sessao, o envio deveria devolver false (fallback para NATS)")
	}
}
