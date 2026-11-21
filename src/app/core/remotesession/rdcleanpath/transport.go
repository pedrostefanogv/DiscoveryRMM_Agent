//go:build windows

package rdcleanpath

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

// SuffixSubjectIn e o sufixo do subject de entrada do RDCleanPath (cliente -> agent).
// Fica em um subject DEDICADO, separado de ".tunnel.in/.tunnel.out", porque o
// canal do tunel carrega frames multiplexados do proxy LAN (header 0x01 + JSON) e
// o RDCleanPath carrega DER (tag 0x30) seguido de bytes RDP crus. Misturar os dois
// no mesmo subject faria componentes diferentes (RemoteProxy e RemoteRdp)
// interpretarem bytes um do outro — ver decisao 3b.
const SuffixSubjectIn = "rdp.cleanpath.in"

// SuffixSubjectOut e o sufixo do subject de saida (agent -> cliente).
const SuffixSubjectOut = "rdp.cleanpath.out"

// MaxTransportBlockBytes limita um bloco individual do transporte NATS.
// Alinhado ao chunk do tunel (32 KiB): o RDP trafega bitmaps de tela e o
// payload maximo do NATS e 2 MiB, entao fragmentar e obrigatorio.
const MaxTransportBlockBytes = 32 * 1024

// ErrTransportClosed indica uso de transporte ja encerrado.
var ErrTransportClosed = errors.New("rdcleanpath: transporte encerrado")

// Publisher abstrai a publicacao NATS (permite teste sem broker).
type Publisher interface {
	// PublishRdpCleanpathOut publica bytes no subject dedicado de saida.
	PublishRdpCleanpathOut(sessionID string, data []byte) error
}

// RecvWithContext e implementado por transports que respeitam cancelamento
// NATIVAMENTE. Quando presente, Session a usa em vez do fallback com goroutine,
// evitando goroutines orfas e perda de blocos no timeout.
type RecvWithContext interface {
	RecvContext(ctx context.Context) ([]byte, error)
}

// SubscribeFunc registra o handler de entrada no subject dedicado.
// Devolve uma funcao de cancelamento.
type SubscribeFunc func(sessionID string, handler func(data []byte)) (cancel func(), err error)

// NatsTransport implementa Transport sobre callbacks de subscribe/publish.
//
// O transporte e orientado a eventos: o NATS entrega os blocos via handler.
// Recv() bloqueia ate haver um bloco disponivel, um erro ou o fechamento.
type NatsTransport struct {
	sessionID  string
	pub        Publisher
	cancelSub  func()
	webrtcSend func([]byte) bool

	mu      sync.Mutex
	queue   [][]byte
	notify  chan struct{}
	closed  bool
	failErr error
	once    sync.Once
}

// SetWebRtcSend registra o envio opcional via data channel WebRTC ("rdp-cleanpath").
// Quando retorna false, Send cai automaticamente para PublishRdpCleanpathOut.
func (tr *NatsTransport) SetWebRtcSend(fn func([]byte) bool) {
	if tr == nil {
		return
	}
	tr.mu.Lock()
	tr.webrtcSend = fn
	tr.mu.Unlock()
}

// Deliver entrega bytes recebidos pelo data channel WebRTC na mesma fila do NATS.
func (tr *NatsTransport) Deliver(data []byte) {
	if tr == nil {
		return
	}
	tr.onData(data)
}

// NewNatsTransport cria o transporte e registra a assinatura de entrada.
func NewNatsTransport(sessionID string, pub Publisher, subscribe SubscribeFunc) (*NatsTransport, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("rdcleanpath: sessionID obrigatorio")
	}
	if pub == nil {
		return nil, fmt.Errorf("rdcleanpath: publisher obrigatorio")
	}
	if subscribe == nil {
		return nil, fmt.Errorf("rdcleanpath: subscribe obrigatorio")
	}

	tr := &NatsTransport{
		sessionID: sessionID,
		pub:       pub,
		notify:    make(chan struct{}, 1),
	}

	cancel, err := subscribe(sessionID, tr.onData)
	if err != nil {
		return nil, fmt.Errorf("rdcleanpath: assinar subject de entrada: %w", err)
	}
	tr.cancelSub = cancel
	return tr, nil
}

// onData enfileira um bloco recebido do cliente.
func (tr *NatsTransport) onData(data []byte) {
	if len(data) == 0 {
		return
	}
	cp := make([]byte, len(data))
	copy(cp, data)

	tr.mu.Lock()
	if tr.closed {
		tr.mu.Unlock()
		return
	}
	tr.queue = append(tr.queue, cp)
	tr.mu.Unlock()

	select {
	case tr.notify <- struct{}{}:
	default: // ja ha sinal pendente
	}
}

// Send publica bytes ao cliente via WebRTC (quando conectado) ou no subject dedicado de saida.
func (tr *NatsTransport) Send(data []byte) error {
	tr.mu.Lock()
	closed := tr.closed
	webrtcSend := tr.webrtcSend
	tr.mu.Unlock()
	if closed {
		return ErrTransportClosed
	}
	if len(data) == 0 {
		return nil
	}
	if webrtcSend != nil && webrtcSend(data) {
		return nil
	}
	return tr.pub.PublishRdpCleanpathOut(tr.sessionID, data)
}

// RecvContext devolve o proximo bloco respeitando o contexto SEM criar
// goroutines orfas: o cancelamento e observado no proprio select.
//
// Isto substitui o padrao (goroutine + channel + timeout) que vazava goroutines
// e perdia blocos: cada timeout deixava um leitor preso em Recv() e um bloco
// posterior podia ser entregue a esse leitor abandonado.
func (tr *NatsTransport) RecvContext(ctx context.Context) ([]byte, error) {
	for {
		tr.mu.Lock()
		if len(tr.queue) > 0 {
			blk := tr.queue[0]
			tr.queue = tr.queue[1:]
			tr.mu.Unlock()
			return blk, nil
		}
		closed := tr.closed
		failErr := tr.failErr
		tr.mu.Unlock()

		if failErr != nil {
			return nil, failErr
		}
		if closed {
			return nil, io.EOF
		}

		select {
		case <-tr.notify:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Recv devolve o proximo bloco, bloqueando ate haver dado, erro ou fechamento.
func (tr *NatsTransport) Recv() ([]byte, error) {
	return tr.RecvContext(context.Background())
}

// Close encerra o transporte e cancela a assinatura (idempotente).
func (tr *NatsTransport) Close() error {
	tr.once.Do(func() {
		tr.mu.Lock()
		tr.closed = true
		tr.mu.Unlock()
		if tr.cancelSub != nil {
			tr.cancelSub()
		}
		select {
		case tr.notify <- struct{}{}:
		default:
		}
	})
	return nil
}

// fail marca o transporte como falho, acordando o Recv.
func (tr *NatsTransport) fail(err error) {
	tr.mu.Lock()
	if tr.failErr == nil {
		tr.failErr = err
	}
	tr.mu.Unlock()
	select {
	case tr.notify <- struct{}{}:
	default:
	}
}

// NormalizeSessionID remove hifens do sessionID para casar com o formato do
// backend (mesma regra de stripHyphens em nats_stream.go).
func NormalizeSessionID(sessionID string) string {
	return strings.ReplaceAll(strings.TrimSpace(sessionID), "-", "")
}

// SubjectFor monta o subject literal dedicado do RDCleanPath.
// Formato: <base>.<sessionID>.<suffix>, com a MESMA base do tunel para herdar a
// permissao literal do backend.
func SubjectFor(baseSubject, sessionID, suffix string) string {
	return fmt.Sprintf("%s.%s.%s", strings.TrimSuffix(baseSubject, "."), NormalizeSessionID(sessionID), suffix)
}

// LogTransportError registra falhas do transporte sem derrubar a sessao.
func LogTransportError(sessionID string, err error) {
	if err != nil {
		log.Printf("[rdcleanpath] transporte session=%s err=%v", sessionID, err)
	}
}

// KeepAliveInterval e o intervalo sugerido de ping no canal dedicado.
const KeepAliveInterval = 15 * time.Second
