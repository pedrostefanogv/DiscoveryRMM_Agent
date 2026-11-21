package remotesession

import (
	"strings"
	"testing"

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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
