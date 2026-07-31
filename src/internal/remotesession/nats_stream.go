package remotesession

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/nats-io/nats.go"
)

// stripHyphens remove hifens de UUIDs para garantir compatibilidade com o
// formato de subject usado pelo servidor e viewer (UUIDs sem hifens).
func stripHyphens(id string) string {
	return strings.ReplaceAll(id, "-", "")
}

// NatsStreamHandler manages NATS-based bidirectional communication for remote sessions.
type NatsStreamHandler struct {
	nc       *nats.Conn
	agentID  string
	tenantID string
	siteID   string

	// maxPayloadBytes é o limite de payload do servidor NATS. Frames maiores
	// são fragmentados (JUMBO) para não exceder o limite.
	maxPayloadBytes int
}

// DefaultMaxPayloadBytes é o padrão (2MB), alinhado ao servidor NATS.
const DefaultMaxPayloadBytes = 2 * 1024 * 1024

// NewNatsStreamHandler creates a new NATS stream handler.
// tenantID, siteID, agentID are required to construct literal subject patterns for publish.
// UUIDs are normalized (hyphens stripped) to match server subject format.
func NewNatsStreamHandler(nc *nats.Conn, tenantID, siteID, agentID string) *NatsStreamHandler {
	return &NatsStreamHandler{
		nc:              nc,
		tenantID:        stripHyphens(tenantID),
		siteID:          stripHyphens(siteID),
		agentID:         stripHyphens(agentID),
		maxPayloadBytes: DefaultMaxPayloadBytes,
	}
}

// SetMaxPayloadBytes define o limite de payload (para alinhar ao servidor).
func (h *NatsStreamHandler) SetMaxPayloadBytes(max int) {
	if max > 0 {
		h.maxPayloadBytes = max
	}
}

// MaxPayloadBytes retorna o limite de payload configurado.
func (h *NatsStreamHandler) MaxPayloadBytes() int {
	return h.maxPayloadBytes
}

// subjectBase returns the literal base subject for this handler's scope (for publish).
// Format: tenant.{tenantID}.site.{siteID}.agent.{agentID}.remote.session
func (h *NatsStreamHandler) subjectBase() string {
	return fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session", h.tenantID, h.siteID, h.agentID)
}

// subscribeBase returns the wildcard pattern for subscribing across tenants/sites/agents.
// Format: tenant.*.site.*.agent.*.remote.session.{sessionID}
func (h *NatsStreamHandler) subscribePattern(sessionID, suffix string) string {
	return fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.%s", stripHyphens(sessionID), suffix)
}

// publishSubject builds the literal publish subject for a given session and suffix.
// Format: tenant.{tenantID}.site.{siteID}.agent.{agentID}.remote.session.{sessionID}.{suffix}
// All UUIDs are normalized (hyphens stripped) to match server subject format.
func (h *NatsStreamHandler) publishSubject(sessionID, suffix string) string {
	return fmt.Sprintf("%s.%s.%s", h.subjectBase(), stripHyphens(sessionID), suffix)
}

// SubscribeToControl subscreve ao subject de controle da sessao (Server→Agent).
func (h *NatsStreamHandler) SubscribeToControl(sessionID string, handler func(action string, payload json.RawMessage)) (*nats.Subscription, error) {
	subject := h.subscribePattern(sessionID, "control")
	return h.nc.Subscribe(subject, func(msg *nats.Msg) {
		var ctrl struct {
			Action  string          `json:"action"`
			Payload json.RawMessage `json:"payload,omitempty"`
		}
		if err := json.Unmarshal(msg.Data, &ctrl); err != nil {
			return
		}
		handler(ctrl.Action, ctrl.Payload)
	})
}

// PublishFrame envia um frame de tela para o viewer.
// Se o frame exceder maxPayloadBytes, é fragmentado (JUMBO) em múltiplos
// publishes .frame.frag com reassembly no viewer.
func (h *NatsStreamHandler) PublishFrame(sessionID string, frameData []byte) error {
	if len(frameData) <= h.maxPayloadBytes {
		subject := h.publishSubject(sessionID, "frame")
		return h.nc.Publish(subject, frameData)
	}

	// ── Fragmentação JUMBO ──
	// Formato de cada fragmento:
	//   [4B totalLen][4B offset][2B fragIndex][2B fragCount][payload]
	// O offset explícito torna o reassembly determinístico (o tamanho de
	// fragmento no agent pode não dividir o total uniformemente).
	const fragHeaderLen = 12
	maxFrag := h.maxPayloadBytes - 32 // reserva margem p/ header NATS
	if maxFrag <= fragHeaderLen {
		maxFrag = fragHeaderLen + 64
	}

	total := len(frameData)
	count := (total + maxFrag - 1) / maxFrag
	if count > 65535 {
		count = 65535 // limita nº de fragmentos
	}

	base := h.publishSubject(sessionID, "frame.frag")
	for i := 0; i < count; i++ {
		start := i * maxFrag
		end := start + maxFrag
		if end > total {
			end = total
		}
		part := frameData[start:end]

		buf := make([]byte, fragHeaderLen+len(part))
		binary.BigEndian.PutUint32(buf[0:4], uint32(total))
		binary.BigEndian.PutUint32(buf[4:8], uint32(start))
		binary.BigEndian.PutUint16(buf[8:10], uint16(i))
		binary.BigEndian.PutUint16(buf[10:12], uint16(count))
		copy(buf[fragHeaderLen:], part)

		if err := h.nc.Publish(base, buf); err != nil {
			return err
		}
	}
	return nil
}

// PublishTermOut envia saida do terminal para o viewer.
func (h *NatsStreamHandler) PublishTermOut(sessionID string, data string) error {
	subject := h.publishSubject(sessionID, "term.out")
	err := h.nc.Publish(subject, []byte(data))
	return err
}

// PublishTermReady notifica o viewer sobre shells disponiveis e console pronto.
func (h *NatsStreamHandler) PublishTermReady(sessionID string, data any) error {
	payload, _ := json.Marshal(data)
	subject := h.publishSubject(sessionID, "term.ready")
	return h.nc.Publish(subject, payload)
}

// PublishRecordingTerm envia frames de terminal para gravacao.
func (h *NatsStreamHandler) PublishRecordingTerm(sessionID string, data []byte) error {
	subject := h.publishSubject(sessionID, "recording.term")
	return h.nc.Publish(subject, data)
}

// PublishCursor envia a posição/estado do cursor separadamente do frame.
// Formato: 6 bytes (flags | x int16 | y int16) — pequeno e frequente.
func (h *NatsStreamHandler) PublishCursor(sessionID string, cursorData []byte) error {
	subject := h.publishSubject(sessionID, "cursor")
	return h.nc.Publish(subject, cursorData)
}

// PublishEvent envia um evento de sessao.
func (h *NatsStreamHandler) PublishEvent(sessionID string, eventType string, data any) error {
	payload, _ := json.Marshal(map[string]any{
		"sessionId": sessionID,
		"eventType": eventType,
		"data":      data,
	})
	subject := h.publishSubject(sessionID, "event")
	log.Printf("[remote-session-nats] PublishEvent: subject=%s eventType=%s\n", subject, eventType)
	return h.nc.Publish(subject, payload)
}

// PublishSignal envia sinalizacao WebRTC (SDP/ICE).
func (h *NatsStreamHandler) PublishSignal(sessionID string, signalData []byte) error {
	return h.nc.Publish(h.publishSubject(sessionID, "signal"), signalData)
}

// SubscribeToInput subscreve a eventos de input (mouse/teclado) do viewer.
func (h *NatsStreamHandler) SubscribeToInput(sessionID string, handler func(inputData []byte)) (*nats.Subscription, error) {
	return h.nc.Subscribe(h.subscribePattern(sessionID, "input"), func(msg *nats.Msg) {
		handler(msg.Data)
	})
}

// SubscribeToTermIn subscreve a stdin do terminal enviado pelo viewer.
func (h *NatsStreamHandler) SubscribeToTermIn(sessionID string, handler func(data []byte)) (*nats.Subscription, error) {
	return h.nc.Subscribe(h.subscribePattern(sessionID, "term.in"), func(msg *nats.Msg) {
		handler(msg.Data)
	})
}

// SubscribeToFilesReq subscreve a requisicoes de arquivos (list/get/put/delete).
func (h *NatsStreamHandler) SubscribeToFilesReq(sessionID string, handler func(reqData []byte) []byte) (*nats.Subscription, error) {
	return h.nc.Subscribe(h.subscribePattern(sessionID, "files.req"), func(msg *nats.Msg) {
		resp := handler(msg.Data)
		if resp != nil {
			_ = h.nc.Publish(h.publishSubject(sessionID, "files.resp"), resp)
		}
	})
}

// SubscribeToProxyReq subscreve a requisicoes de proxy HTTP.
func (h *NatsStreamHandler) SubscribeToProxyReq(sessionID string, handler func(reqData []byte) []byte) (*nats.Subscription, error) {
	return h.nc.Subscribe(h.subscribePattern(sessionID, "proxy.req"), func(msg *nats.Msg) {
		resp := handler(msg.Data)
		if resp != nil {
			_ = h.nc.Publish(h.publishSubject(sessionID, "proxy.resp"), resp)
		}
	})
}

// SubscribeToSignal subscreve a sinalizacao WebRTC do viewer.
func (h *NatsStreamHandler) SubscribeToSignal(sessionID string, handler func(signalData []byte)) (*nats.Subscription, error) {
	return h.nc.Subscribe(h.subscribePattern(sessionID, "signal"), func(msg *nats.Msg) {
		handler(msg.Data)
	})
}

// Close encerra o handler (no-op por enquanto, a conexao NATS e gerenciada externamente).
func (h *NatsStreamHandler) Close() error {
	return nil
}

// Subscription helpers para gerenciar subscricoes

type SessionSubscriptions struct {
	Control  *nats.Subscription
	Input    *nats.Subscription
	TermIn   *nats.Subscription
	FilesReq *nats.Subscription
	ProxyReq *nats.Subscription
	Signal   *nats.Subscription
}

// UnsubscribeAll cancela todas as subscricoes.
func (ss *SessionSubscriptions) UnsubscribeAll() {
	if ss.Control != nil {
		_ = ss.Control.Unsubscribe()
	}
	if ss.Input != nil {
		_ = ss.Input.Unsubscribe()
	}
	if ss.TermIn != nil {
		_ = ss.TermIn.Unsubscribe()
	}
	if ss.FilesReq != nil {
		_ = ss.FilesReq.Unsubscribe()
	}
	if ss.ProxyReq != nil {
		_ = ss.ProxyReq.Unsubscribe()
	}
	if ss.Signal != nil {
		_ = ss.Signal.Unsubscribe()
	}
}

// SubscribeAll cria as subscricoes necessarias para uma sessao, com contexto para cleanup.
func (h *NatsStreamHandler) SubscribeAll(ctx context.Context, sessionID string, handlers SessionHandlers) (*SessionSubscriptions, error) {
	subs := &SessionSubscriptions{}
	var err error

	subs.Control, err = h.SubscribeToControl(sessionID, handlers.OnControl)
	if err != nil {
		subs.UnsubscribeAll()
		return nil, fmt.Errorf("subscribe control: %w", err)
	}

	if handlers.OnInput != nil {
		subs.Input, err = h.SubscribeToInput(sessionID, handlers.OnInput)
		if err != nil {
			subs.UnsubscribeAll()
			return nil, fmt.Errorf("subscribe input: %w", err)
		}
	}

	if handlers.OnTermIn != nil {
		subs.TermIn, err = h.SubscribeToTermIn(sessionID, handlers.OnTermIn)
		if err != nil {
			subs.UnsubscribeAll()
			return nil, fmt.Errorf("subscribe term.in: %w", err)
		}
	}

	if handlers.OnFilesReq != nil {
		subs.FilesReq, err = h.SubscribeToFilesReq(sessionID, handlers.OnFilesReq)
		if err != nil {
			subs.UnsubscribeAll()
			return nil, fmt.Errorf("subscribe files.req: %w", err)
		}
	}

	if handlers.OnProxyReq != nil {
		subs.ProxyReq, err = h.SubscribeToProxyReq(sessionID, handlers.OnProxyReq)
		if err != nil {
			subs.UnsubscribeAll()
			return nil, fmt.Errorf("subscribe proxy.req: %w", err)
		}
	}

	if handlers.OnSignal != nil {
		subs.Signal, err = h.SubscribeToSignal(sessionID, handlers.OnSignal)
		if err != nil {
			subs.UnsubscribeAll()
			return nil, fmt.Errorf("subscribe signal: %w", err)
		}
	}

	return subs, nil
}

// SessionHandlers define os handlers de eventos para uma sessao.
type SessionHandlers struct {
	OnControl  func(action string, payload json.RawMessage)
	OnInput    func(data []byte)
	OnTermIn   func(data []byte)
	OnFilesReq func(reqData []byte) []byte
	OnProxyReq func(reqData []byte) []byte
	OnSignal   func(signalData []byte)
}
