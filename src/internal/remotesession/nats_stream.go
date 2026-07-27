package remotesession

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"
)

// NatsStreamHandler manages NATS-based bidirectional communication for remote sessions.
type NatsStreamHandler struct {
	nc       *nats.Conn
	agentID  string // used for constructing subject patterns
}

// NewNatsStreamHandler creates a new NATS stream handler.
func NewNatsStreamHandler(nc *nats.Conn, agentID string) *NatsStreamHandler {
	return &NatsStreamHandler{
		nc:      nc,
		agentID: agentID,
	}
}

// SubscribeToControl subscreve ao subject de controle da sessao (Server→Agent).
func (h *NatsStreamHandler) SubscribeToControl(sessionID string, handler func(action string, payload json.RawMessage)) (*nats.Subscription, error) {
	subject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.control", sessionID)
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
func (h *NatsStreamHandler) PublishFrame(sessionID string, frameData []byte) error {
	subject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.frame", sessionID)
	return h.nc.Publish(subject, frameData)
}

// PublishTermOut envia saida do terminal para o viewer.
func (h *NatsStreamHandler) PublishTermOut(sessionID string, data string) error {
	subject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.term.out", sessionID)
	return h.nc.Publish(subject, []byte(data))
}

// PublishEvent envia um evento de sessao.
func (h *NatsStreamHandler) PublishEvent(sessionID string, eventType string, data any) error {
	payload, _ := json.Marshal(map[string]any{
		"sessionId": sessionID,
		"eventType": eventType,
		"data":      data,
	})
	subject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.event", sessionID)
	return h.nc.Publish(subject, payload)
}

// PublishSignal envia sinalizacao WebRTC (SDP/ICE).
func (h *NatsStreamHandler) PublishSignal(sessionID string, signalData []byte) error {
	subject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.signal", sessionID)
	return h.nc.Publish(subject, signalData)
}

// SubscribeToInput subscreve a eventos de input (mouse/teclado) do viewer.
func (h *NatsStreamHandler) SubscribeToInput(sessionID string, handler func(inputData []byte)) (*nats.Subscription, error) {
	subject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.input", sessionID)
	return h.nc.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Data)
	})
}

// SubscribeToTermIn subscreve a stdin do terminal enviado pelo viewer.
func (h *NatsStreamHandler) SubscribeToTermIn(sessionID string, handler func(data []byte)) (*nats.Subscription, error) {
	subject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.term.in", sessionID)
	return h.nc.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Data)
	})
}

// SubscribeToFilesReq subscreve a requisicoes de arquivos (list/get/put/delete).
func (h *NatsStreamHandler) SubscribeToFilesReq(sessionID string, handler func(reqData []byte) []byte) (*nats.Subscription, error) {
	subject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.files.req", sessionID)
	return h.nc.Subscribe(subject, func(msg *nats.Msg) {
		resp := handler(msg.Data)
		if resp != nil {
			respSubject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.files.resp", sessionID)
			_ = h.nc.Publish(respSubject, resp)
		}
	})
}

// SubscribeToProxyReq subscreve a requisicoes de proxy HTTP.
func (h *NatsStreamHandler) SubscribeToProxyReq(sessionID string, handler func(reqData []byte) []byte) (*nats.Subscription, error) {
	subject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.proxy.req", sessionID)
	return h.nc.Subscribe(subject, func(msg *nats.Msg) {
		resp := handler(msg.Data)
		if resp != nil {
			respSubject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.proxy.resp", sessionID)
			_ = h.nc.Publish(respSubject, resp)
		}
	})
}

// SubscribeToSignal subscreve a sinalizacao WebRTC do viewer.
func (h *NatsStreamHandler) SubscribeToSignal(sessionID string, handler func(signalData []byte)) (*nats.Subscription, error) {
	subject := fmt.Sprintf("tenant.*.site.*.agent.*.remote.session.%s.signal", sessionID)
	return h.nc.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Data)
	})
}

// Close encerra o handler (no-op por enquanto, a conexao NATS e gerenciada externamente).
func (h *NatsStreamHandler) Close() error {
	return nil
}

// Subscription helpers para gerenciar subscricoes

type SessionSubscriptions struct {
	Control   *nats.Subscription
	Input     *nats.Subscription
	TermIn    *nats.Subscription
	FilesReq  *nats.Subscription
	ProxyReq  *nats.Subscription
	Signal    *nats.Subscription
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

	return subs, nil
}

// SessionHandlers define os handlers de eventos para uma sessao.
type SessionHandlers struct {
	OnControl func(action string, payload json.RawMessage)
	OnInput   func(data []byte)
}
