//go:build windows

package app

// IPC serviço ↔ UI via named pipe (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 2).
//
// Contrato: JSON lines (uma mensagem JSON por linha, delimitada por \n)
// sobre pipe duplex \\.\pipe\discovery-agent-ipc.
//
// SDDL explícito obrigatório (revisão 2026-09-04): SYSTEM (SY) e Users (BU)
// com GenericAll no DACL — o default do winio bloquearia a UI do usuário.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Microsoft/go-winio"
)

// IPCPipeName é o caminho do named pipe do serviço.
const IPCPipeName = `\\.\pipe\discovery-agent-ipc`

// ipcPipeSDDL concede acesso a SYSTEM e ao grupo Users builtin.
const ipcPipeSDDL = "D:(A;;GA;;;SY)(A;;GA;;;BU)"

// IPCMessageType identifica a mensagem do contrato IPC.
type IPCMessageType string

const (
	IPCMsgHello               IPCMessageType = "hello"
	IPCMsgHelloAck            IPCMessageType = "hello_ack"
	IPCMsgStatus              IPCMessageType = "status"
	IPCMsgEvent               IPCMessageType = "event"
	IPCMsgNotificationRespond IPCMessageType = "notification:respond"
	IPCMsgCommandResult       IPCMessageType = "command_result"
)

// IPCMessage é o envelope do contrato JSON-lines.
type IPCMessage struct {
	Type      IPCMessageType `json:"type"`
	Payload   map[string]any `json:"payload,omitempty"`
	Timestamp int64          `json:"ts,omitempty"`
}

// NewIPCMessage cria um envelope com timestamp atual.
func NewIPCMessage(t IPCMessageType, payload map[string]any) IPCMessage {
	return IPCMessage{Type: t, Payload: payload, Timestamp: time.Now().UnixMilli()}
}

// EncodeIPCMessage serializa uma mensagem como JSON line (com \n final).
func EncodeIPCMessage(msg IPCMessage) []byte {
	b, err := json.Marshal(msg)
	if err != nil {
		return nil
	}
	return append(b, '\n')
}

// DecodeIPCMessage lê uma linha JSON do reader.
func DecodeIPCMessage(r *bufio.Reader) (IPCMessage, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return IPCMessage{}, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return IPCMessage{}, fmt.Errorf("linha vazia")
	}
	var msg IPCMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return IPCMessage{}, fmt.Errorf("json inválido: %w", err)
	}
	return msg, nil
}

// ── Servidor (lado do serviço) ──────────────────────────────────────────────

// IPCServer escuta o pipe e distribui eventos do serviço para os clientes UI.
type IPCServer struct {
	listener net.Listener
	mu       sync.Mutex
	conns    map[net.Conn]*bufio.Reader
	closed   atomic.Bool

	// OnMessage é chamado para cada mensagem recebida da UI.
	OnMessage func(msg IPCMessage)
}

// StartIPCServer inicia o listener do pipe do serviço (chamado no modo
// --service). Roda em goroutine própria; erros vão para o log.
func StartIPCServer(onMessage func(IPCMessage)) *IPCServer {
	l, err := winio.ListenPipe(IPCPipeName, &winio.PipeConfig{
		SecurityDescriptor: ipcPipeSDDL,
		MessageMode:        false,
	})
	if err != nil {
		log.Printf("[ipc] falha ao escutar %s: %v", IPCPipeName, err)
		return nil
	}

	s := &IPCServer{
		listener:  l,
		conns:     make(map[net.Conn]*bufio.Reader),
		OnMessage: onMessage,
	}
	go s.acceptLoop()
	log.Printf("[ipc] servidor ativo em %s (SDDL: %s)", IPCPipeName, ipcPipeSDDL)
	return s
}

// acceptLoop aceita clientes em loop até o servidor ser fechado.
func (s *IPCServer) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.closed.Load() {
				return
			}
			log.Printf("[ipc] accept falhou: %v", err)
			time.Sleep(time.Second)
			continue
		}
		log.Printf("[ipc] UI conectada: %s", conn.RemoteAddr())
		go s.handleConn(conn)
	}
}

// handleConn lê mensagens de um cliente conectado.
func (s *IPCServer) handleConn(conn net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		conn.Close()
		log.Printf("[ipc] UI desconectada")
	}()

	reader := bufio.NewReader(conn)
	s.mu.Lock()
	s.conns[conn] = reader
	s.mu.Unlock()

	for {
		if s.closed.Load() {
			return
		}
		msg, err := DecodeIPCMessage(reader)
		if err != nil {
			return // EOF ou erro de parse — encerra a conexão
		}
		// hello é respondido DIRETAMENTE na conexão (o probe IsServicePresent
		// espera hello_ack na mesma conexão antes de fechar).
		if msg.Type == IPCMsgHello {
			conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if _, err := conn.Write(EncodeIPCMessage(NewIPCMessage(IPCMsgHelloAck, map[string]any{
				"service": ServiceName,
			}))); err != nil {
				log.Printf("[ipc] falha ao responder hello_ack: %v", err)
			}
		}
		if s.OnMessage != nil {
			s.OnMessage(msg)
		}
	}
}

// Broadcast envia uma mensagem para todos os clientes UI conectados.
func (s *IPCServer) Broadcast(msg IPCMessage) {
	data := EncodeIPCMessage(msg)
	if data == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.conns {
		conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write(data); err != nil {
			log.Printf("[ipc] broadcast falhou para %s: %v", conn.RemoteAddr(), err)
		}
	}
}

// ClientCount retorna quantas UIs estão conectadas.
func (s *IPCServer) ClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Close encerra o servidor e todas as conexões.
func (s *IPCServer) Close() {
	if s == nil || !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.listener.Close()
	s.mu.Lock()
	for conn := range s.conns {
		conn.Close()
	}
	s.conns = make(map[net.Conn]*bufio.Reader)
	s.mu.Unlock()
}

// ── Cliente (lado da UI) ────────────────────────────────────────────────────

// IPCClient conecta a UI ao serviço. Reconecta automaticamente com backoff.
type IPCClient struct {
	pipeName string

	conn net.Conn
	mu   sync.Mutex
	rx   *bufio.Reader

	closed     atomic.Bool
	reconnectN atomic.Int32

	// downCh é sinalizado pelo readLoop quando a conexão cai — substitui
	// polling (busy-wait) no RunConnectLoop.
	downCh chan struct{}

	// OnMessage é chamado para cada mensagem do serviço (event, hello_ack).
	OnMessage func(msg IPCMessage)
	// OnStateChange é chamado quando a conexão abre (true) ou cai (false).
	OnStateChange func(connected bool)
}

// NewIPCClient cria um cliente apontando ao pipe do serviço.
func NewIPCClient(onMessage func(IPCMessage), onStateChange func(bool)) *IPCClient {
	return &IPCClient{
		pipeName:      IPCPipeName,
		OnMessage:     onMessage,
		OnStateChange: onStateChange,
		downCh:        make(chan struct{}, 1),
	}
}

// Connect tenta conectar imediatamente (handshake rápido: "serviço ativo?").
func (c *IPCClient) Connect(timeout time.Duration) bool {
	conn, err := winio.DialPipe(c.pipeName, &timeout)
	if err != nil {
		return false
	}
	// Consome sinal down residual de uma conexão anterior.
	select {
	case <-c.downCh:
	default:
	}
	c.mu.Lock()
	c.conn = conn
	c.rx = bufio.NewReader(conn)
	c.mu.Unlock()
	if c.OnStateChange != nil {
		c.OnStateChange(true)
	}
	go c.readLoop()
	return true
}

// RunConnectLoop mantém a conexão com backoff exponencial (2s→60s), padrão
// do agentconn. Bloqueia; chamar em goroutine. Encerra com Close().
func (c *IPCClient) RunConnectLoop() {
	backoff := 2 * time.Second
	for {
		if c.closed.Load() {
			return
		}
		if c.Connect(3 * time.Second) {
			n := c.reconnectN.Add(1)
			log.Printf("[ipc] conectado ao serviço (tentativa %d)", n)
			backoff = 2 * time.Second
			// Bloqueia até o readLoop sinalizar a queda da conexão.
			select {
			case <-c.downCh:
			case <-time.After(90 * time.Second):
				// Safety-net: se nenhum sinal chegar (ex.: pipe travado sem
				// EOF), força verificação do estado da conexão.
				c.mu.Lock()
				conn := c.conn
				c.mu.Unlock()
				if conn == nil {
					continue
				}
			}
		} else {
			n := c.reconnectN.Add(1)
			log.Printf("[ipc] serviço indisponível (tentativa %d)", n)
		}
		if c.closed.Load() {
			return
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

// readLoop consome mensagens do serviço até a conexão cair.
func (c *IPCClient) readLoop() {
	defer func() {
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
			c.rx = nil
		}
		c.mu.Unlock()
		if c.OnStateChange != nil {
			c.OnStateChange(false)
		}
		// Sinaliza o RunConnectLoop para reconectar (não-bloqueante).
		select {
		case c.downCh <- struct{}{}:
		default:
		}
	}()

	for {
		c.mu.Lock()
		rx := c.rx
		c.mu.Unlock()
		if rx == nil {
			return
		}
		msg, err := DecodeIPCMessage(rx)
		if err != nil {
			return
		}
		if c.OnMessage != nil {
			c.OnMessage(msg)
		}
	}
}

// Send envia uma mensagem ao serviço.
func (c *IPCClient) Send(msg IPCMessage) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("não conectado ao serviço")
	}
	data := EncodeIPCMessage(msg)
	conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, err := conn.Write(data)
	return err
}

// Close encerra o cliente e o loop de reconexão.
func (c *IPCClient) Close() {
	c.closed.Store(true)
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()
}

// IsServicePresent faz um probe rápido de handshake ("serviço ativo?") —
// usado pela UI no startup para decidir companion vs standalone.
func IsServicePresent(timeout time.Duration) bool {
	conn, err := winio.DialPipe(IPCPipeName, &timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(EncodeIPCMessage(NewIPCMessage(IPCMsgHello, map[string]any{"pid": os.Getpid()}))); err != nil {
		return false
	}
	reader := bufio.NewReader(conn)
	msg, err := DecodeIPCMessage(reader)
	return err == nil && msg.Type == IPCMsgHelloAck
}
