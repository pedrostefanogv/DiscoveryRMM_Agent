package remotesession

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// Estes testes exercitam a NEGOCIAÇÃO REAL com pion — a prova de que o ponto
// que antes recusava ("stack de mídia não inicializado") agora produz uma
// resposta SDP de verdade.
//
// Sem eles, o único teste possível seria "a função existe": o que importa é que
// uma oferta de navegador vire resposta com candidatos ICE.

// buildTestOffer cria uma oferta SDP usando o próprio pion (o mesmo caminho de um
// navegador real: cria data channels e gera a oferta).
func buildTestOffer(t *testing.T, labels ...string) (*webrtc.PeerConnection, string) {
	t.Helper()

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("criar peer connection de teste: %v", err)
	}

	for _, label := range labels {
		if _, err := pc.CreateDataChannel(label, nil); err != nil {
			t.Fatalf("criar data channel %q: %v", label, err)
		}
	}

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("criar oferta: %v", err)
	}

	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("aplicar oferta local: %v", err)
	}
	<-gatherComplete

	return pc, pc.LocalDescription().SDP
}

func TestNegotiate_ProduzRespostaSDPReal(t *testing.T) {
	policy := WebRtcPolicy{
		Enabled:        true,
		FallbackToNats: true,
		IceServers:     []IceServer{{Urls: []string{"stun:stun.l.google.com:19302"}}},
	}

	stack := NewWebRtcMediaStack("sess-neg-1", policy, nil, nil, nil, nil, nil)
	defer stack.Close()

	viewerConn, offerSDP := buildTestOffer(t, WebRtcChannelMedia, WebRtcChannelControl)
	defer viewerConn.Close()

	answer, err := stack.Negociate(offerSDP)
	if err != nil {
		t.Fatalf("Negociate falhou: %v", err)
	}

	if strings.TrimSpace(answer) == "" {
		t.Fatal("resposta SDP vazia — o viewer acreditaria ter conectado sem nada trafegar")
	}
	if !strings.HasPrefix(answer, "v=0") {
		t.Fatalf("resposta não parece SDP (começa com %q)", answer[:min(20, len(answer))])
	}
	if !strings.Contains(answer, "a=ice-ufrag:") {
		t.Fatal("resposta sem ice-ufrag: o ICE não poderia ser negociado")
	}
}

func TestNegotiate_SemTurnComRelayOnlyRecusa(t *testing.T) {
	// FAIL-CLOSED: relayOnly sem TURN produziria uma sessão que NUNCA conecta.
	// A recusa acontece ANTES de criar o peer connection, para o motivo ficar
	// claro no indicador em vez de virar timeout silencioso.
	policy := WebRtcPolicy{
		Enabled:        true,
		RelayOnly:      true,
		FallbackToNats: true,
		IceServers:     []IceServer{{Urls: []string{"stun:stun.l.google.com:19302"}}},
	}

	stack := NewWebRtcMediaStack("sess-neg-2", policy, nil, nil, nil, nil, nil)
	defer stack.Close()

	_, offerSDP := buildTestOffer(t, WebRtcChannelMedia)

	_, err := stack.Negociate(offerSDP)
	if err == nil {
		t.Fatal("relayOnly sem TURN deveria recusar a negociação")
	}
	if !strings.Contains(err.Error(), "TURN") {
		t.Fatalf("erro deveria citar TURN: %v", err)
	}
}

func TestNegotiate_ComTurnAceitaRelayOnly(t *testing.T) {
	// Com TURN configurado, relayOnly é válido — a recusa é específica da
	// AUSÊNCIA de TURN, não do modo.
	policy := WebRtcPolicy{
		Enabled:        true,
		RelayOnly:      true,
		FallbackToNats: true,
		IceServers: []IceServer{{
			Urls:       []string{"turn:turn.exemplo.com:3478"},
			Username:   "u",
			Credential: "p",
		}},
	}

	stack := NewWebRtcMediaStack("sess-neg-3", policy, nil, nil, nil, nil, nil)
	defer stack.Close()

	_, offerSDP := buildTestOffer(t, WebRtcChannelMedia)

	// A negociação deve PASSAR da validação de relay (pode falhar depois por o
	// TURN ser inalcançável, mas não pela regra de fail-closed).
	_, err := stack.Negociate(offerSDP)
	if err != nil && strings.Contains(err.Error(), "relayOnly exige servidor TURN") {
		t.Fatalf("relayOnly COM TURN não deveria ser recusado pela regra: %v", err)
	}
}

func TestNegotiate_AposCloseRecusa(t *testing.T) {
	policy := WebRtcPolicy{Enabled: true, IceServers: []IceServer{{Urls: []string{"stun:a:3478"}}}}

	stack := NewWebRtcMediaStack("sess-neg-4", policy, nil, nil, nil, nil, nil)
	stack.Close()

	_, offerSDP := buildTestOffer(t, WebRtcChannelMedia)

	if _, err := stack.Negociate(offerSDP); err == nil {
		t.Fatal("negociar numa stack fechada deveria falhar")
	}
}

func TestIsConnected_FalsoAntesDeNegociar(t *testing.T) {
	policy := WebRtcPolicy{Enabled: true, IceServers: []IceServer{{Urls: []string{"stun:a:3478"}}}}
	stack := NewWebRtcMediaStack("sess-neg-5", policy, nil, nil, nil, nil, nil)
	defer stack.Close()

	if stack.IsConnected() {
		t.Fatal("sem negociação não há conexão — o indicador não pode dizer WebRTC")
	}
}

func TestSend_SemCanalAbertoDevolveFalse(t *testing.T) {
	// O contrato que importa: Send NUNCA entra em pânico nem bloqueia quando o
	// canal não abriu. O chamador usa o false para decidir cair no NATS.
	policy := WebRtcPolicy{Enabled: true, IceServers: []IceServer{{Urls: []string{"stun:a:3478"}}}}
	stack := NewWebRtcMediaStack("sess-neg-6", policy, nil, nil, nil, nil, nil)
	defer stack.Close()

	if stack.Send(WebRtcChannelMedia, []byte("frame")) {
		t.Fatal("Send deveria devolver false sem data channel aberto")
	}
}

func TestBuildConfiguration_RelayOnlyFiltraStun(t *testing.T) {
	// Um STUN que escapasse no modo relay faria o ICE tentar caminho direto,
	// furando o isolamento que o relayOnly promete.
	policy := WebRtcPolicy{
		Enabled:   true,
		RelayOnly: true,
		IceServers: []IceServer{
			{Urls: []string{"stun:stun.l.google.com:19302"}},
			{Urls: []string{"turn:turn.exemplo.com:3478"}},
		},
	}

	stack := NewWebRtcMediaStack("sess-cfg-1", policy, nil, nil, nil, nil, nil)
	defer stack.Close()

	config, err := stack.buildConfiguration()
	if err != nil {
		t.Fatalf("buildConfiguration falhou: %v", err)
	}

	if config.ICETransportPolicy != webrtc.ICETransportPolicyRelay {
		t.Fatal("relayOnly deveria forçar ICETransportPolicyRelay")
	}

	for _, server := range config.ICEServers {
		for _, url := range server.URLs {
			if strings.HasPrefix(strings.ToLower(url), "stun:") {
				t.Fatalf("STUN vazou para o modo relay: %s", url)
			}
		}
	}
}

func TestBuildConfiguration_SemRelayMantemStunETurn(t *testing.T) {
	policy := WebRtcPolicy{
		Enabled: false,
		IceServers: []IceServer{
			{Urls: []string{"stun:stun.l.google.com:19302"}},
			{Urls: []string{"turn:t:3478"}, Username: "u", Credential: "p"},
		},
	}

	stack := NewWebRtcMediaStack("sess-cfg-2", policy, nil, nil, nil, nil, nil)
	defer stack.Close()

	config, err := stack.buildConfiguration()
	if err != nil {
		t.Fatalf("buildConfiguration falhou: %v", err)
	}

	if config.ICETransportPolicy == webrtc.ICETransportPolicyRelay {
		t.Fatal("sem relayOnly a política não pode ser relay")
	}
	if len(config.ICEServers) != 2 {
		t.Fatalf("esperava 2 servidores ICE, obteve %d", len(config.ICEServers))
	}
	if config.ICEServers[1].Username != "u" || config.ICEServers[1].Credential != "p" {
		t.Fatal("credenciais do TURN não foram repassadas ao pion")
	}
}

func TestMediaStack_RoteiaTodosOsQuatroCanaisSemSobrescrever(t *testing.T) {
	// Regressão C1: antes, chamar peerConn.OnDataChannel 4 vezes sobrescrevia os
	// primeiros callbacks e só o último canal ("files") recebia mensagens.
	policy := WebRtcPolicy{
		Enabled:        true,
		FallbackToNats: true,
	}

	got := make(map[string]string)
	var gotMu sync.Mutex
	allReceived := make(chan struct{})
	mark := func(label string, data []byte) {
		gotMu.Lock()
		got[label] = string(data)
		done := len(got) == 4
		gotMu.Unlock()
		if done {
			select {
			case <-allReceived:
			default:
				close(allReceived)
			}
		}
	}

	stack := NewWebRtcMediaStack(
		"sess-loopback-c1",
		policy,
		func(d []byte) { mark(WebRtcChannelMedia, d) },
		func(d []byte) { mark(WebRtcChannelControl, d) },
		func(d []byte) { mark(WebRtcChannelTunnel, d) },
		func(d []byte) { mark(WebRtcChannelFiles, d) },
		nil,
	)
	defer stack.Close()

	viewerConn, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("criar viewer PeerConnection: %v", err)
	}
	defer viewerConn.Close()

	channels := make(map[string]*webrtc.DataChannel)
	mediaReply := make(chan string, 1)
	for _, label := range []string{WebRtcChannelMedia, WebRtcChannelControl, WebRtcChannelTunnel, WebRtcChannelFiles} {
		lbl := label
		dc, err := viewerConn.CreateDataChannel(lbl, nil)
		if err != nil {
			t.Fatalf("criar data channel %s: %v", lbl, err)
		}
		if lbl == WebRtcChannelMedia {
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				select {
				case mediaReply <- string(msg.Data):
				default:
				}
			})
		}
		dc.OnOpen(func() {
			_ = dc.SendText("payload-" + lbl)
		})
		channels[lbl] = dc
	}

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

	select {
	case <-allReceived:
	case <-time.After(8 * time.Second):
		gotMu.Lock()
		t.Fatalf("timeout aguardando os 4 data channels; recebidos=%v", got)
	}

	for _, label := range []string{WebRtcChannelMedia, WebRtcChannelControl, WebRtcChannelTunnel, WebRtcChannelFiles} {
		if got[label] != "payload-"+label {
			t.Fatalf("canal %s recebeu %q, esperava %q", label, got[label], "payload-"+label)
		}
	}

	// Verifica também o envio agente -> viewer no canal media.
	if !stack.Send(WebRtcChannelMedia, []byte("frame-from-agent")) {
		t.Fatal("Send no canal media aberto deveria retornar true")
	}
	select {
	case msg := <-mediaReply:
		if msg != "frame-from-agent" {
			t.Fatalf("viewer recebeu %q, esperava frame-from-agent", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando frame enviado pelo agente no canal media")
	}
}

func TestMediaStack_CloseDuranteNegociateNaoRessuscita(t *testing.T) {
	// Regressão C4: Close() concorrente durante Negociate() não pode ser
	// revertido por s.closed = false ao final do gathering.
	policy := WebRtcPolicy{
		Enabled:    true,
		IceServers: []IceServer{{Urls: []string{"stun:stun.l.google.com:19302"}}},
	}
	stack := NewWebRtcMediaStack("sess-c4", policy, nil, nil, nil, nil, nil)

	viewerConn, offerSDP := buildTestOffer(t, WebRtcChannelMedia)
	defer viewerConn.Close()

	done := make(chan error, 1)
	go func() {
		_, err := stack.Negociate(offerSDP)
		done <- err
	}()

	time.Sleep(25 * time.Millisecond)
	stack.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Negociate não retornou após Close()")
	}

	stack.mu.Lock()
	closed := stack.closed
	pc := stack.peerConn
	stack.mu.Unlock()

	if !closed {
		t.Fatal("stack.closed foi revertido após Close()")
	}
	if pc != nil {
		t.Fatal("stack.peerConn não deveria permanecer preenchido após Close()")
	}
}

func TestMediaStack_SendAcimaDoTetoSCTPRetornaFalse(t *testing.T) {
	policy := WebRtcPolicy{Enabled: true}
	stack := NewWebRtcMediaStack("sess-max-msg", policy, nil, nil, nil, nil, nil)
	defer stack.Close()

	oversized := make([]byte, maxWebRtcMessageBytes+1)
	if stack.Send(WebRtcChannelMedia, oversized) {
		t.Fatal("Send acima de maxWebRtcMessageBytes deveria retornar false para cair no NATS JUMBO")
	}
}

func TestMediaStack_AddCandidateSemPeerConnNaoFalha(t *testing.T) {
	policy := WebRtcPolicy{Enabled: true}
	stack := NewWebRtcMediaStack("sess-cand", policy, nil, nil, nil, nil, nil)
	defer stack.Close()

	if err := stack.AddCandidate("candidate:1 1 UDP 2122252543 127.0.0.1 5000 typ host"); err != nil {
		t.Fatalf("AddCandidate sem peerConn deveria ser no-op, retornou %v", err)
	}
}

func TestNewSettingEngine_RespeitaTimeoutsDaPolitica(t *testing.T) {
	policy := WebRtcPolicy{
		DisconnectedTimeoutSeconds: 7,
		FailedTimeoutSeconds:       20,
		KeepAliveIntervalSeconds:   4,
	}
	disconnected, failed, keepAlive := resolveIceTimeouts(policy)
	if disconnected != 7*time.Second {
		t.Fatalf("ICEDisconnectedTimeout = %v, esperava 7s", disconnected)
	}
	if failed != 20*time.Second {
		t.Fatalf("ICEFailedTimeout = %v, esperava 20s", failed)
	}
	if keepAlive != 4*time.Second {
		t.Fatalf("ICEKeepaliveInterval = %v, esperava 4s", keepAlive)
	}
	_ = newSettingEngine(policy)
}

func TestManager_RegisterWebRtcDataSinkFazMergeNaoDestrutivo(t *testing.T) {
	m := NewManager(nil)
	var inputCalled, tunnelCalled bool

	m.RegisterWebRtcDataSink("sess-merge", webrtcDataSink{
		onInput: func([]byte) { inputCalled = true },
	})
	m.RegisterWebRtcDataSink("sess-merge", webrtcDataSink{
		onTunnel: func([]byte) { tunnelCalled = true },
	})

	sink := m.dataSinkFor("sess-merge")
	if sink.onInput == nil || sink.onTunnel == nil {
		t.Fatal("RegisterWebRtcDataSink sobrescreveu callback existente em vez de fazer merge")
	}
	sink.onInput(nil)
	sink.onTunnel(nil)
	if !inputCalled || !tunnelCalled {
		t.Fatal("callbacks preservados não foram acionados")
	}
}

func TestMediaStack_RoteiaCanalRdpCleanPath(t *testing.T) {
	policy := WebRtcPolicy{Enabled: true, FallbackToNats: true}
	gotRdp := make(chan string, 1)

	stack := NewWebRtcMediaStack("sess-rdp-ch", policy, nil, nil, nil, nil, nil)
	stack.SetRdpHandler(func(d []byte) {
		gotRdp <- string(d)
	})
	defer stack.Close()

	viewerConn, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("criar viewer PeerConnection: %v", err)
	}
	defer viewerConn.Close()

	dc, err := viewerConn.CreateDataChannel(WebRtcChannelRdp, nil)
	if err != nil {
		t.Fatalf("criar data channel rdp: %v", err)
	}
	replyCh := make(chan string, 1)
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		replyCh <- string(msg.Data)
	})
	dc.OnOpen(func() {
		_ = dc.SendText("rdp-req-der")
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

	select {
	case msg := <-gotRdp:
		if msg != "rdp-req-der" {
			t.Fatalf("recebeu %q, esperava rdp-req-der", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout aguardando mensagem no canal rdp-cleanpath")
	}

	if !stack.Send(WebRtcChannelRdp, []byte("rdp-resp-der")) {
		t.Fatal("Send em WebRtcChannelRdp deveria retornar true")
	}
	select {
	case msg := <-replyCh:
		if msg != "rdp-resp-der" {
			t.Fatalf("viewer recebeu %q, esperava rdp-resp-der", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando resposta no canal rdp-cleanpath")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestLimitesDeEnvio_GuardNaoExcedeOTetoSctpAnunciado fixa o alinhamento que
// faltava entre o guard de aplicação e o teto SCTP realmente negociado.
//
// Quando o guard valia 240 KB contra um anúncio de 64 KB (o default do pion),
// todo frame entre os dois valores era recusado pelo Send e caía para o NATS: o
// data channel ficava aberto e OCIOSO exatamente nos keyframes, que são os
// maiores. O sintoma era silencioso — nada no log dizia "a tela não está em P2P".
func TestLimitesDeEnvio_GuardNaoExcedeOTetoSctpAnunciado(t *testing.T) {
	if maxWebRtcMessageBytes > sctpMaxMessageBytes {
		t.Fatalf("guard de aplicação (%d) excede o teto SCTP anunciado (%d): o Send recusaria e cairia para o NATS",
			maxWebRtcMessageBytes, sctpMaxMessageBytes)
	}
	// O default do pion (64 KB) é o que motivou anunciar um teto maior: um
	// keyframe de 3440x1440 WebP não cabe em 64 KB.
	if sctpMaxMessageBytes <= 64*1024 {
		t.Fatalf("teto SCTP efetivo (%d) é o default do pion: keyframes não passariam pelo data channel", sctpMaxMessageBytes)
	}
}
