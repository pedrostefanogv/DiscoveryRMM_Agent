package remotesession

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
)

// Sinalização WebRTC no agente.
//
// Este arquivo implementa o LADO DE CONTROLE da negociação: recebe a oferta SDP
// do viewer, devolve a resposta, troca candidatos ICE e decide o fallback para
// NATS quando o ICE não fecha. A mídia em si (captura, encode, tracks) é
// responsabilidade do pacote de tela, que consome o resultado da negociação.
//
// DESENHO DELIBERADO — o controle nunca sai do NATS:
//
//   - A mídia pode ser peer-to-peer, mas o .control continua no NATS. É por ele
//     que o servidor encerra a sessão, o liveness detecta viewer ausente e o
//     KVM lock é renovado. Se o controle fosse P2P, um servidor perderia a
//     capacidade de matar uma sessão viva — foi exatamente o problema que o
//     Breeze resolve com um "revocation lease" obrigatório.
//
//   - A sinalização também passa pelo NATS (.webrtc.in/.webrtc.out), e não pelo
//     .control, porque o teto de 512 bytes daquele canal não comporta um SDP.
//
// FAIL-CLOSED em todo caminho: qualquer erro de negociação (SDP inválido, ICE
// sem par, timeout) resulta em queda para NATS quando FallbackToNats está
// ligado — o operador continua com a sessão — ou em encerramento com motivo
// explícito quando não está. Nunca em tela preta silenciosa.

// Tipos de frame no canal de sinalização. O valor vai em "kind" e é o MESMO
// contrato do wire que o navegador usa, para não inventar um dialeto próprio.
const (
	SignalKindOffer     = "offer"
	SignalKindAnswer    = "answer"
	SignalKindCandidate = "candidate"
	SignalKindError     = "error"
)

// SignalFrame é um frame do canal de sinalização.
//
// SDP e Candidate convivem no mesmo tipo porque o canal é o mesmo e o
// discriminante é "kind" — o mesmo desenho do protocolo de túnel, que distingue
// binário de JSON pelo primeiro byte.
type SignalFrame struct {
	Kind      string `json:"kind"`
	SDP       string `json:"sdp,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	// Generation descarta candidatos de uma negociação já substituída. Sem ele,
	// um candidato atrasado de uma tentativa anterior envenenaria a atual — é o
	// mesmo problema que o trickle do Breeze resolve com StartGeneration.
	Generation int    `json:"generation,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// DecodeSignalFrame lê um frame de sinalização com validação de forma.
//
// Rejeita kind desconhecido e frame vazio: um frame inválido no meio da
// negociação deixaria o peer connection pendurado até o timeout, então é melhor
// falhar cedo e cair para NATS.
func DecodeSignalFrame(data []byte) (SignalFrame, error) {
	if len(data) == 0 {
		return SignalFrame{}, fmt.Errorf("frame de sinalização vazio")
	}

	var frame SignalFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return SignalFrame{}, fmt.Errorf("frame de sinalização inválido: %w", err)
	}

	switch frame.Kind {
	case SignalKindOffer:
		if strings.TrimSpace(frame.SDP) == "" {
			return SignalFrame{}, fmt.Errorf("oferta sem SDP")
		}
	case SignalKindCandidate:
		if strings.TrimSpace(frame.Candidate) == "" {
			return SignalFrame{}, fmt.Errorf("candidato ICE vazio")
		}
	case SignalKindAnswer, SignalKindError:
		// aceitos (o agente pode recebê-los numa renegociação)
	default:
		return SignalFrame{}, fmt.Errorf("kind de sinalização desconhecido: %q", frame.Kind)
	}

	return frame, nil
}

// EncodeSignalFrame serializa um frame emitido pelo agente.
func EncodeSignalFrame(frame SignalFrame) ([]byte, error) {
	return json.Marshal(frame)
}

// WebRtcSession mantém o estado da negociação WebRTC de UMA sessão remota.
//
// Uma instância por sessão: o Manager cria ao autorizar o WebRTC e descarta no
// encerramento. O mutex serializa a negociação (duas ofertas concorrentes com o
// mesmo sessionId orfanavam o estado no Breeze — bug real, replicado aqui como
// proteção).
type WebRtcSession struct {
	sessionID string
	policy    WebRtcPolicy

	mu sync.Mutex
	// generation incrementa a cada nova oferta: candidatos de gerações antigas
	// são descartados em vez de envenenar a negociação corrente.
	generation int
	// negotiating indica que uma oferta foi recebida e a resposta ainda não foi
	// produzida. Serve para tornar o estado observável no indicador.
	negotiating bool
	// closed evita entregar frames a uma sessão já encerrada.
	closed bool

	// stack é o peer connection vivo (nil até a primeira oferta). Protegido por
	// mu: é criado sob demanda e lido pelo caminho de envio a cada frame.
	stack *WebRtcMediaStack

	// Callbacks do caminho de DADOS. O pacote de tela/túnel/arquivos injeta os
	// seus para receber o que chega pelos data channels.
	onMediaFrame      func([]byte)
	onControlFrame    func([]byte)
	onTunnelFrame     func([]byte)
	onFilesFrame      func([]byte)
	onPeerStateChange func(string)

	// onAnswer é chamado quando a resposta SDP estiver pronta (injetado pelo
	// pacote de tela, que faz a negociação de fato).
	onAnswer func(answerSDP string) error
	// onRemoteCandidate entrega um candidato ICE do viewer.
	onRemoteCandidate func(candidate string) error
	// onFallback é chamado quando a negociação falha e a política permite cair
	// para NATS. Recebe o motivo, que vai para o indicador.
	onFallback func(reason string)
	// onFatal é chamado quando a negociação falha e o fallback está desligado.
	onFatal func(reason string)
}

// NewWebRtcSession cria o estado de negociação de uma sessão.
func NewWebRtcSession(
	sessionID string,
	policy WebRtcPolicy,
	onAnswer func(string) error,
	onRemoteCandidate func(string) error,
	onFallback func(string),
	onFatal func(string),
) *WebRtcSession {
	return &WebRtcSession{
		sessionID:         sessionID,
		policy:            policy,
		onAnswer:          onAnswer,
		onRemoteCandidate: onRemoteCandidate,
		onFallback:        onFallback,
		onFatal:           onFatal,
	}
}

// Generation devolve a geração corrente (para diagnóstico).
func (w *WebRtcSession) Generation() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.generation
}

// Negotiating indica se há uma oferta aguardando resposta.
func (w *WebRtcSession) Negotiating() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.negotiating
}

// Close marca a sessão como encerrada: frames tardios passam a ser descartados
// em vez de reabrir uma negociação morta.
func (w *WebRtcSession) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	stack := w.stack
	w.stack = nil
	w.closed = true
	w.mu.Unlock()

	// Fecha FORA do mutex: peerConn.Close() pode bloquear no teardown do ICE, e
	// segurar o mutex aqui travaria qualquer envio em andamento.
	if stack != nil {
		stack.Close()
	}
}

// SetDataHandlers registra os callbacks do caminho de dados.
//
// Chamado DEPOIS da construção e ANTES da primeira oferta: os handlers são
// instalados no data channel no momento em que o peer connection é criado, então
// chegar tarde significaria perder os primeiros frames.
func (w *WebRtcSession) SetDataHandlers(
	onMedia, onControl, onTunnel, onFiles func([]byte),
	onPeerStateChange func(string),
) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onMediaFrame = onMedia
	w.onControlFrame = onControl
	w.onTunnelFrame = onTunnel
	w.onFilesFrame = onFiles
	w.onPeerStateChange = onPeerStateChange
}

// HandleFrame processa um frame de sinalização vindo do viewer.
//
// Devolve erro apenas para problemas de FORMA (frame inválido). Falhas de
// negociação são tratadas internamente — com fallback ou encerramento — para
// não deixar a sessão pendurada esperando um erro que ninguém lê.
func (w *WebRtcSession) HandleFrame(data []byte) error {
	if w == nil {
		return fmt.Errorf("sessão WebRTC não inicializada")
	}

	frame, err := DecodeSignalFrame(data)
	if err != nil {
		// Frame malformado: cai para NATS se a política permitir. Não encerra a
		// sessão por um erro de forma que pode ser transitório.
		w.fail(fmt.Sprintf("frame de sinalização inválido: %v", err))
		return err
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	currentGeneration := w.generation
	w.mu.Unlock()

	switch frame.Kind {
	case SignalKindOffer:
		return w.handleOffer(frame)
	case SignalKindCandidate:
		// Candidato de geração antiga é DESCARTADO, não aplicado: aplicá-lo
		// envenenaria a negociação corrente com um endereço de uma tentativa
		// que já não existe.
		//
		// A comparação é EXATA (frame.Generation != currentGeneration), e não
		// "0 = aceita qualquer": um viewer que não preenche a geração manda 0, e
		// tratar 0 como curinga reintroduziria exatamente o bug que a geração
		// existe para evitar — o candidato de uma negociação anterior entraria
		// na corrente. A geração é sempre conhecida pelo agente (ele a
		// incrementa), então exigir igualdade é o contrato correto.
		if frame.Generation != currentGeneration {
			log.Printf("[remote-session-webrtc] candidato de geração antiga descartado: session=%s frame=%d atual=%d\n",
				w.sessionID, frame.Generation, currentGeneration)
			return nil
		}
		if w.onRemoteCandidate != nil {
			if err := w.onRemoteCandidate(frame.Candidate); err != nil {
				w.fail(fmt.Sprintf("falha ao aplicar candidato ICE: %v", err))
				return err
			}
		}
	case SignalKindError:
		w.fail("viewer reportou erro de negociação: " + frame.Reason)
	}

	return nil
}

// handleOffer trata uma oferta SDP: incrementa a geração, marca a negociação e
// delega a produção da resposta.
func (w *WebRtcSession) handleOffer(frame SignalFrame) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	// Nova oferta invalida candidatos da anterior.
	w.generation++
	generation := w.generation
	w.negotiating = true
	w.mu.Unlock()

	log.Printf("[remote-session-webrtc] oferta recebida: session=%s generation=%d sdpBytes=%d\n",
		w.sessionID, generation, len(frame.SDP))

	if w.onAnswer == nil {
		w.fail("negociação WebRTC indisponível neste runtime")
		return fmt.Errorf("onAnswer não configurado")
	}

	answer, err := w.negotiate(frame.SDP)
	if err != nil {
		w.fail(fmt.Sprintf("negociação falhou: %v", err))
		return err
	}

	w.mu.Lock()
	w.negotiating = false
	w.mu.Unlock()

	if err := w.onAnswer(answer); err != nil {
		w.fail(fmt.Sprintf("falha ao publicar resposta: %v", err))
		return err
	}

	return nil
}

// negotiate produz a resposta SDP delegando ao stack de mídia (pion).
//
// O stack é criado sob demanda na primeira oferta: uma sessão que nunca negocia
// (porque o viewer ficou em NATS) não aloca peer connection nenhum.
//
// FAIL-CLOSED: qualquer erro aqui aciona a política de falha — fallback para NATS
// quando autorizado, ou encerramento com motivo explícito. Nunca uma resposta
// vazia, que faria o viewer acreditar que conectou e o INDICADOR MENTIR, que é o
// pior modo de falha possível para um selo cuja única função é dizer a verdade.
func (w *WebRtcSession) negotiate(offerSDP string) (string, error) {
	stack := w.mediaStack()
	if stack == nil {
		return "", fmt.Errorf("stack de mídia indisponível para esta sessão")
	}

	answer, err := stack.Negociate(offerSDP)
	if err != nil {
		return "", err
	}

	return answer, nil
}

// mediaStack devolve o stack de mídia da sessão, criando-o na primeira chamada.
//
// A criação é serializada pelo mutex de negociação: duas ofertas concorrentes com
// o mesmo sessionId criariam dois peer connections e o segundo orfanaria o
// primeiro (bug real observado no Breeze).
func (w *WebRtcSession) mediaStack() *WebRtcMediaStack {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return nil
	}

	if w.stack == nil {
		w.stack = NewWebRtcMediaStack(
			w.sessionID,
			w.policy,
			w.onMediaFrame,
			w.onControlFrame,
			w.onTunnelFrame,
			w.onFilesFrame,
			w.onPeerStateChange,
		)
	}

	return w.stack
}

// MediaStack devolve o stack para o caminho de dados (envio de frames/bytes).
// Pode ser nil quando o WebRTC não foi autorizado ou ainda não negociou.
func (w *WebRtcSession) MediaStack() *WebRtcMediaStack {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stack
}

// fail aplica a política de falha: cai para NATS ou encerra, sempre com motivo.
//
// O motivo NÃO é decorativo: ele vai para o indicador do console, que é como o
// operador descobre que a sessão está em NATS apesar de ele ter configurado
// WebRTC.
func (w *WebRtcSession) fail(reason string) {
	w.mu.Lock()
	alreadyClosed := w.closed
	w.negotiating = false
	w.mu.Unlock()

	if alreadyClosed {
		return
	}

	log.Printf("[remote-session-webrtc] negociação falhou: session=%s fallback=%v motivo=%q\n",
		w.sessionID, w.policy.FallbackToNats, reason)

	if w.policy.FallbackToNats {
		if w.onFallback != nil {
			w.onFallback(reason)
		}
		return
	}

	if w.onFatal != nil {
		w.onFatal(reason)
	}
}
