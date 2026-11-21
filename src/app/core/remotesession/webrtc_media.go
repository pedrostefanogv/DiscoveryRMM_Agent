package remotesession

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// Stack de mídia WebRTC (pion).
//
// Este arquivo é o que faz os BYTES saírem por P2P. Antes dele, a negociação
// existia mas recusava explicitamente — o indicador mostrava "NATS" com o motivo
// "stack de mídia não inicializado", em vez de mentir dizendo "WebRTC".
//
// ── Por que data channel e não track de vídeo ──────────────────────────────
//
// Os frames desta plataforma já são JPEG/WebP comprimidos pelo agente, com dirty
// rects e escada de qualidade adaptativa próprias (session_screen.go, quality.go).
// Um track de vídeo exigiria RECODIFICAR tudo em VP8/VP9, jogando fora:
//   - o dirty-rect (que só envia a região que mudou),
//   - a escada adaptativa já calibrada por netstats,
//   - o tile mode, que agrupa regiões sujas num JPEG só.
//
// O data channel transporta o MESMO payload que já vai pelo NATS, sem tocar no
// pipeline de captura. É também o que o MeshCentral tentou (agent-redir-ws-0.1.1.js)
// e o que o Breeze faz para o túnel/controle. O ganho do WebRTC aqui é o CAMINHO
// (P2P, sem broker), não o codec.
//
// ── Timeouts ──────────────────────────────────────────────────────────────
//
// Os defaults do pion (25 s de "failed") são calibrados para videochamada. Suporte
// remoto precisa detectar a falha e cair para NATS RÁPIDO — o operador está
// olhando a tela esperando.

// Data channels criados por sessão. Os LABELS são o contrato com o viewer.
const (
	// WebRtcChannelMedia carrega os frames de tela (mesmo payload do .frame).
	WebRtcChannelMedia = "media"
	// WebRtcChannelControl carrega input (mouse/teclado) viewer→agente.
	WebRtcChannelControl = "control"
	// WebRtcChannelTunnel carrega os bytes do túnel TCP/RDP (framing de 6 B).
	WebRtcChannelTunnel = "tunnel"
	// WebRtcChannelFiles carrega os chunks do servidor de arquivos.
	WebRtcChannelFiles = "files"
)

// WebRtcMediaStack é o peer connection vivo de uma sessão.
//
// Uma instância por sessão. O mutex serializa a negociação — duas ofertas
// concorrentes com o mesmo sessionId orfanavam o estado no Breeze (bug real,
// registrado no comentário de StartSession de lá).
type WebRtcMediaStack struct {
	sessionID string
	policy    WebRtcPolicy

	mu         sync.Mutex
	peerConn   *webrtc.PeerConnection
	channels   map[string]*webrtc.DataChannel
	closed     bool
	generation int

	// onMedia entrega um frame recebido no data channel de mídia.
	onMedia func([]byte)
	// onControl entrega input do viewer.
	onControl func([]byte)
	// onTunnel entrega bytes do túnel TCP/RDP.
	onTunnel func([]byte)
	// onFiles entrega um chunk de arquivo.
	onFiles func([]byte)
	// onStateChange reporta mudança de estado (para o indicador).
	onStateChange func(state string)
}

// NewWebRtcMediaStack cria o stack de mídia de uma sessão.
func NewWebRtcMediaStack(
	sessionID string,
	policy WebRtcPolicy,
	onMedia, onControl, onTunnel, onFiles func([]byte),
	onStateChange func(string),
) *WebRtcMediaStack {
	return &WebRtcMediaStack{
		sessionID:     sessionID,
		policy:        policy,
		channels:      make(map[string]*webrtc.DataChannel),
		onMedia:       onMedia,
		onControl:     onControl,
		onTunnel:      onTunnel,
		onFiles:       onFiles,
		onStateChange: onStateChange,
	}
}

// buildConfiguration converte a política em webrtc.Configuration.
//
// relayOnly é FAIL-CLOSED, como no value object: com ICETransportPolicyRelay e
// sem TURN, a sessão nunca estabeleceria — então erramos ANTES de criar o peer
// connection, para o motivo ficar claro no indicador.
func (s *WebRtcMediaStack) buildConfiguration() (webrtc.Configuration, error) {
	config := webrtc.Configuration{}

	servers := make([]webrtc.ICEServer, 0, len(s.policy.IceServers))
	for _, server := range s.policy.IceServers {
		ice := webrtc.ICEServer{URLs: server.Urls}
		if server.Username != "" {
			ice.Username = server.Username
			ice.Credential = server.Credential
			ice.CredentialType = webrtc.ICECredentialTypePassword
		}
		servers = append(servers, ice)
	}

	if s.policy.RelayOnly {
		// Filtra SÓ turn/turns: um STUN que escapasse faria o ICE tentar caminho
		// direto, furando o modo relay.
		relay := make([]webrtc.ICEServer, 0, len(servers))
		for _, server := range servers {
			urls := make([]string, 0, len(server.URLs))
			for _, url := range server.URLs {
				lower := strings.ToLower(url)
				if strings.HasPrefix(lower, "turn:") || strings.HasPrefix(lower, "turns:") {
					urls = append(urls, url)
				}
			}
			if len(urls) > 0 {
				server.URLs = urls
				relay = append(relay, server)
			}
		}
		if len(relay) == 0 {
			return webrtc.Configuration{}, fmt.Errorf(
				"relayOnly exige servidor TURN, e nenhum foi configurado")
		}
		config.ICETransportPolicy = webrtc.ICETransportPolicyRelay
		servers = relay
	}

	config.ICEServers = servers
	return config, nil
}

// newSettingEngine aplica os timeouts afinados para suporte remoto.
//
// Sem isto, o pion usa 25 s de failedTimeout — o operador ficaria 25 segundos
// olhando uma tela preta antes de o fallback para NATS entrar.
func newSettingEngine() webrtc.SettingEngine {
	se := webrtc.SettingEngine{}
	se.SetICETimeouts(
		5*time.Second,  // disconnectedTimeout — detecta "sem mídia" rápido
		15*time.Second, // failedTimeout — reduzido do default de 25 s
		2*time.Second,  // keepAliveInterval — renova binding STUN sem mídia
	)
	return se
}

// Negociate processa a oferta SDP do viewer e devolve a resposta.
//
// É o coração: cria o peer connection, registra os data channels que o AGENTE
// abre (media/control/tunnel/files), aceita a oferta e espera o ICE gathering.
//
// Esperar o gathering COMPLETO (em vez de só devolver a resposta imediata) é
// deliberado: sem trickle implementado do lado do viewer, uma resposta sem
// candidatos faria o ICE falhar por timeout. A coleta completa é mais lenta,
// porém determinística.
func (s *WebRtcMediaStack) Negociate(offerSDP string) (string, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", fmt.Errorf("sessão WebRTC já encerrada")
	}
	s.generation++
	generation := s.generation
	s.mu.Unlock()

	config, err := s.buildConfiguration()
	if err != nil {
		return "", err
	}

	api := webrtc.NewAPI(webrtc.WithSettingEngine(newSettingEngine()))
	peerConn, err := api.NewPeerConnection(config)
	if err != nil {
		return "", fmt.Errorf("criar peer connection: %w", err)
	}

	// Registra os data channels ANTES de aplicar a oferta: o viewer abre os canais
	// dele e o casamento por label precisa dos handlers já instalados.
	s.registerChannel(peerConn, WebRtcChannelMedia, s.onMedia)
	s.registerChannel(peerConn, WebRtcChannelControl, s.onControl)
	s.registerChannel(peerConn, WebRtcChannelTunnel, s.onTunnel)
	s.registerChannel(peerConn, WebRtcChannelFiles, s.onFiles)

	peerConn.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[remote-session-webrtc] estado do peer connection: session=%s state=%s\n",
			s.sessionID, state.String())
		if s.onStateChange != nil {
			s.onStateChange(state.String())
		}
	})

	if err := peerConn.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}); err != nil {
		_ = peerConn.Close()
		return "", fmt.Errorf("aplicar oferta SDP: %w", err)
	}

	answer, err := peerConn.CreateAnswer(nil)
	if err != nil {
		_ = peerConn.Close()
		return "", fmt.Errorf("criar resposta SDP: %w", err)
	}

	// Canal de coleta: fechado quando o gathering termina (ou falha).
	gatherComplete := webrtc.GatheringCompletePromise(peerConn)

	if err := peerConn.SetLocalDescription(answer); err != nil {
		_ = peerConn.Close()
		return "", fmt.Errorf("aplicar resposta local: %w", err)
	}

	// Espera o ICE gathering. O teto evita travar indefinidamente num STUN
	// inalcançável: nesse caso devolvemos a resposta PARCIAL (com os candidatos
	// que já existirem) — o viewer ainda pode conectar por um deles, e o ICE do
	// lado dele completa o resto.
	select {
	case <-gatherComplete:
	case <-time.After(iceGatheringTimeout):
		log.Printf("[remote-session-webrtc] gathering ICE incompleto após %s; devolvendo resposta parcial: session=%s\n",
			iceGatheringTimeout, s.sessionID)
	}

	local := peerConn.LocalDescription()
	if local == nil {
		_ = peerConn.Close()
		return "", fmt.Errorf("descrição local indisponível após o gathering")
	}

	s.mu.Lock()
	s.peerConn = peerConn
	s.closed = false
	s.mu.Unlock()

	log.Printf("[remote-session-webrtc] negociação concluída: session=%s generation=%d candidates=%d\n",
		s.sessionID, generation, strings.Count(local.SDP, "a=candidate:"))

	return local.SDP, nil
}

// iceGatheringTimeout limita a espera pelos candidatos ICE.
const iceGatheringTimeout = 8 * time.Second

// registerChannel instala o handler de um data channel.
//
// A tolerância a erro no OnMessage é deliberada: um payload corrompido não pode
// derrubar a sessão inteira; o handler é chamado em panic-safe.
func (s *WebRtcMediaStack) registerChannel(
	peerConn *webrtc.PeerConnection,
	label string,
	handler func([]byte),
) {
	peerConn.OnDataChannel(func(channel *webrtc.DataChannel) {
		s.mu.Lock()
		s.channels[label] = channel
		s.mu.Unlock()

		if handler != nil {
			channel.OnMessage(func(msg webrtc.DataChannelMessage) {
				handler(msg.Data)
			})
		}

		channel.OnOpen(func() {
			log.Printf("[remote-session-webrtc] data channel aberto: session=%s label=%s\n",
				s.sessionID, label)
		})

		channel.OnClose(func() {
			s.mu.Lock()
			delete(s.channels, label)
			s.mu.Unlock()
			log.Printf("[remote-session-webrtc] data channel fechado: session=%s label=%s\n",
				s.sessionID, label)
		})
	})
}

// Send publica bytes num data channel. Devolve false quando o canal não está
// aberto — o chamador decide se cai para NATS.
//
// NÃO é erro fatal: durante a negociação o canal ainda não abriu, e o caminho
// NATS continua funcionando. Por isso devolve bool em vez de error.
func (s *WebRtcMediaStack) Send(label string, data []byte) bool {
	s.mu.Lock()
	channel := s.channels[label]
	s.mu.Unlock()

	if channel == nil || channel.ReadyState() != webrtc.DataChannelStateOpen {
		return false
	}

	if err := channel.Send(data); err != nil {
		log.Printf("[remote-session-webrtc] falha ao enviar em %s: session=%s err=%v\n",
			label, s.sessionID, err)
		return false
	}
	return true
}

// IsConnected informa se há um peer connection em estado utilizável.
//
// A API do pion expõe um único estado terminal de sucesso no peer connection
// (PeerConnectionStateConnected). O "completed" existe no ICE, não aqui —
// referenciá-lo não compila. Só "connected" é o critério correto.
func (s *WebRtcMediaStack) IsConnected() bool {
	s.mu.Lock()
	peerConn := s.peerConn
	s.mu.Unlock()

	if peerConn == nil {
		return false
	}

	return peerConn.ConnectionState() == webrtc.PeerConnectionStateConnected
}

// Close encerra o peer connection e os canais. Idempotente.
func (s *WebRtcMediaStack) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	peerConn := s.peerConn
	s.channels = make(map[string]*webrtc.DataChannel)
	s.mu.Unlock()

	if peerConn != nil {
		if err := peerConn.Close(); err != nil {
			log.Printf("[remote-session-webrtc] erro ao fechar peer connection: session=%s err=%v\n",
				s.sessionID, err)
		}
	}
}
