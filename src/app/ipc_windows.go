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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"discovery/app/core/buildinfo"
)

// IPCPipeName é o caminho do named pipe do serviço. Deriva do prefixo comum
// (ver ipcPipePrefix) para que nome de pipe inválido seja impossível por typo.
const IPCPipeName = ipcPipePrefix + "discovery-agent-ipc"

// Liveness das UIs companion conectadas.
//
// A UI manda o snapshot de status a cada 5s (DefaultCompanionConfig). Se ela
// TRAVAR, o processo continua vivo e o pipe continua aberto: sem um sweep no
// lado do SERVIDOR, ClientCount() nunca cai e hasSupportChatUI() continua
// dizendo que existe interface — o watchdog da UI nunca entraria no ramo
// "travada". O teto é 12× a cadência normal; um falso positivo se cura sozinho
// (o cliente reconecta).
const (
	ipcServerIdleSweepInterval = 15 * time.Second
	ipcServerIdleTimeout       = 60 * time.Second
)

// DACL dos dois canais (decisão S1).
//
// O pipe de LEITURA não concede mais GenericAll a Users: SYSTEM fica com GA e
// Users com Read+Write (GRGW). Read puro (GR) quebraria o contrato JSON-lines:
// o cliente precisa ESCREVER hello/request/status no mesmo handle duplex — o
// probe IsServicePresent e a UI antiga deixariam de funcionar. O que Users
// perde é o GA (FILE_CREATE_PIPE_INSTANCE/posse), impedindo sequestro do pipe;
// mutações são bloqueadas no protocolo (canal admin + token).
const ipcReadPipeSDDL = "D:(A;;GA;;;SY)(A;;GRGW;;;BU)"

// O pipe ADMIN/MUTAÇÃO mantém GenericAll para Users (quem conecta é validado
// por processo/token e precisa de token de sessão), e só aceita métodos
// mutantes.
const ipcAdminPipeSDDL = "D:(A;;GA;;;SY)(A;;GA;;;BU)"

// ipcPipeSDDL mantém o nome histórico para logs/testes do pipe de leitura.
const ipcPipeSDDL = ipcReadPipeSDDL

// IPCMessageType identifica a mensagem do contrato IPC.
type IPCMessageType string

const (
	IPCMsgHello               IPCMessageType = "hello"
	IPCMsgHelloAck            IPCMessageType = "hello_ack"
	IPCMsgStatus              IPCMessageType = "status"
	IPCMsgEvent               IPCMessageType = "event"
	IPCMsgNotificationRespond IPCMessageType = "notification:respond"
	IPCMsgCommandResult       IPCMessageType = "command_result"
	// IPCMsgRemoteSession encaminha um comando de remote session (screen/terminal/
	// files) recebido via NATS no serviço para a UI companion executar na sessão
	// interativa do usuário. A sessão 0 (SYSTEM) não tem desktop: captura de tela
	// falha (0 frames) e SendInput é bloqueado por UIPI — ver PLANO_AGENT_SERVICE_SYSTEM.md §2.2.
	IPCMsgRemoteSession IPCMessageType = "remote_session"

	// IPCMsgRequest / IPCMsgResponse (PLANO_SEPARACAO_SERVICO_UI.md, Fase C):
	// request/response com correlation-id para métodos que a UI companion
	// consulta no serviço (status, config, inventário, memory, updates).
	// O request usa Payload["method"] + CorrelationID; a resposta ecoa o
	// mesmo CorrelationID com Payload["ok"]/Payload["data"]/Payload["error"].
	IPCMsgRequest  IPCMessageType = "request"
	IPCMsgResponse IPCMessageType = "response"
)

// IPCRequestTimeout é o fallback de espera por resposta quando o contexto do
// chamador NÃO tem deadline. Quem cria o contexto é dono do timeout: o
// ipcRequest padrão usa 10s e os bridges lentos (loja/suporte) usam 30s. Antes
// este valor era um teto global e cortava silenciosamente os timeouts maiores
// (ex.: 30s da loja viravam 10s) — ver Request.
const IPCRequestTimeout = 30 * time.Second

// IPCProtocolVersion é a versão do contrato JSON-lines (handshake hello/
// hello_ack, PLANO_SEPARACAO_SERVICO_UI.md §0.4 — item "versionar handshake").
// Diferenças em compatibilidade de mensagens devem incrementar este número;
// a UI compara com a versão informada pelo serviço para decidir entre
// operar em modo compatível ou reclamar (drift durante updates).
const IPCProtocolVersion = 2

// errIPCAdminConnClosed indica que a conexão do canal admin caiu com um
// request mutante ainda em voo (o serviço pode ter executado a operação e
// perdido a resposta). Quem chama NÃO deve repetir a operação localmente.
var errIPCAdminConnClosed = errors.New("canal admin encerrado antes da resposta")

// IPCMessage é o envelope do contrato JSON-lines.
type IPCMessage struct {
	Type          IPCMessageType `json:"type"`
	Payload       map[string]any `json:"payload,omitempty"`
	CorrelationID string         `json:"cid,omitempty"`
	Timestamp     int64          `json:"ts,omitempty"`
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

// ipcClientConn agrupa o estado por cliente conectado: o reader do read loop
// e um mutex de escrita.
//
// B3: escritas de rede não podem competir na mesma conn (Broadcast concorrente
// com RespondTo/hello_ack intercalaria writes parciais e corromperia o framing
// JSON-lines). Cada conn tem seu próprio mutex de escrita — broadcasts não se
// serializam entre conns (só writes na MESMA conn).
type ipcClientConn struct {
	reader  *bufio.Reader
	writeMu sync.Mutex
	// lastActivity (unix millis) alimenta o sweep de conexões travadas: uma UI
	// congelada para de mandar o snapshot de status (o CompanionController manda
	// a cada 5s) mas o pipe continua aberto do lado do sistema — sem este sinal
	// o serviço seguia contando uma UI morta como conectada.
	lastActivity atomic.Int64

	// Estado de segurança do canal (autenticação/limites).
	admin      bool
	connKey    string
	identity   ipcAuthIdentity
	identityOK bool
	// callerNotAllowed marca chamador válido (SID/PID/sessão) mas que NÃO é a
	// UI: o canal admin responde hello_ack SEM token e fecha (A.0.1).
	callerNotAllowed bool
	limiter          *ipcRequestLimiter
	// helloLimiter limita hello por conexão (cada hello emite/rotaciona token).
	helloLimiter *ipcRequestLimiter
	dedup        *ipcDedupCache

	// inFlight conta requests sendo executados NESTA conexão. Uma instalação
	// da loja pode durar muitos minutos sem mandar nada; sem este contador o
	// sweep de ociosidade (5 min no canal admin) fecharia a conexão no meio da
	// operação e a resposta se perderia.
	inFlight atomic.Int32

	authMu      sync.Mutex
	token       string
	tokenExpiry time.Time
	tokenValid  bool
}

// touch marca atividade do cliente (chamado a cada mensagem recebida).
func (c *ipcClientConn) touch() {
	c.lastActivity.Store(time.Now().UnixMilli())
}

// idleFor devolve há quanto tempo o cliente não manda nada.
func (c *ipcClientConn) idleFor(now time.Time) time.Duration {
	last := c.lastActivity.Load()
	if last == 0 {
		return 0
	}
	return now.Sub(time.UnixMilli(last))
}

// writeTo envia um frame para a conn com deadline, serializado por conn.
func (c *ipcClientConn) writeTo(conn net.Conn, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, err := conn.Write(data)
	return err
}

// IPCServer escuta o pipe de leitura e o pipe admin, e distribui eventos do
// serviço para os clientes UI do canal de leitura.
type IPCServer struct {
	listener      net.Listener
	adminListener net.Listener
	mu            sync.Mutex
	conns         map[net.Conn]*ipcClientConn
	tokens        *ipcTokenStore
	closed        atomic.Bool
	// adminIdleTimeout é injetável em teste (0 = ipcAdminIdleTimeout).
	adminIdleTimeout time.Duration

	// OnMessage é chamado para cada mensagem recebida da UI. A conn é
	// fornecida para que o handler possa responder DIRETAMENTE ao cliente de
	// origem (ex.: status_snapshot) sem depender do broadcast — que pode estar
	// bloqueado por conns zumbis de outras UIs.
	OnMessage func(conn net.Conn, msg IPCMessage)
}

// StartIPCServer inicia os dois listeners (leitura e admin) do serviço
// (chamado no modo --service). Roda em goroutines próprias; erros vão ao log.
// A falha do pipe admin NÃO derruba o pipe de leitura: a UI continua operando
// em modo somente-leitura (mutações retornam unauthorized tratado).
func StartIPCServer(onMessage func(net.Conn, IPCMessage)) *IPCServer {
	// Valida o nome ANTES de chamar o SO: um nome malformado devolve apenas
	// "Incorrect function" e o erro fica opaco (foi a causa do incidente de
	// 2026-10-17: o pipe admin nunca foi criado e ninguém viu no log).
	if nameErr := validateIPCPipeName(IPCPipeName); nameErr != nil {
		log.Printf("[ipc] ERRO: canal de leitura com nome inválido: %v", nameErr)
		return nil
	}
	l, err := winio.ListenPipe(IPCPipeName, &winio.PipeConfig{
		SecurityDescriptor: ipcReadPipeSDDL,
		MessageMode:        false,
	})
	if err != nil {
		log.Printf("[ipc] falha ao escutar %s: %v", IPCPipeName, err)
		return nil
	}

	s := &IPCServer{
		listener:  l,
		conns:     make(map[net.Conn]*ipcClientConn),
		tokens:    newIPCTokenStore(),
		OnMessage: onMessage,
	}
	go s.acceptLoop(l, false)

	// Canal admin: se ele não subir, TODAS as mutações da UI ficam bloqueadas
	// (chamados, comentários, anexos, config, updates, knowledge) enquanto as
	// leituras seguem funcionando — degradação silenciosa perigosa. Por isso:
	// valida o nome, loga em nível ERRO e conta a falha na telemetria.
	if nameErr := validateIPCPipeName(IPCPipeAdminName); nameErr != nil {
		ipcAuthStats.AdminListenFailed.Add(1)
		log.Printf("[ipc] ERRO: canal admin com nome inválido: %v — mutações ficarão indisponíveis", nameErr)
	} else if adminL, adminErr := winio.ListenPipe(IPCPipeAdminName, &winio.PipeConfig{
		SecurityDescriptor: ipcAdminPipeSDDL,
		MessageMode:        false,
	}); adminErr != nil {
		ipcAuthStats.AdminListenFailed.Add(1)
		log.Printf("[ipc] ERRO: falha ao escutar %s: %v — mutações ficarão indisponíveis", IPCPipeAdminName, adminErr)
	} else {
		s.adminListener = adminL
		go s.acceptLoop(adminL, true)
		log.Printf("[ipc] canal admin ativo em %s (SDDL: %s)", IPCPipeAdminName, ipcAdminPipeSDDL)
	}

	go s.idleSweepLoop()
	log.Printf("[ipc] servidor ativo em %s (SDDL: %s)", IPCPipeName, ipcReadPipeSDDL)
	return s
}

// idleSweepLoop fecha conexões que pararam de mandar mensagens (UI travada).
//
// Diferente do rxWatchdog do CLIENTE (que roda dentro da UI e por isso não roda
// quando ela trava), este sweep é do SERVIDOR: é ele que derruba a contagem de
// UIs conectadas e permite ao watchdog relançar a interface.
func (s *IPCServer) idleSweepLoop() {
	ticker := time.NewTicker(ipcServerIdleSweepInterval)
	defer ticker.Stop()

	for range ticker.C {
		if s.closed.Load() {
			return
		}
		s.sweepIdleConns(time.Now())
	}
}

// sweepIdleConns derruba as conexões sem mensagens há mais de
// ipcServerIdleTimeout e devolve quantas caíram (separado do loop para ser
// testável sem pipe real).
func (s *IPCServer) sweepIdleConns(now time.Time) int {
	s.mu.Lock()
	var idle []net.Conn
	var idleAdmin []bool
	for conn, client := range s.conns {
		// Request em execução (ex.: instalação da loja, minutos sem tráfego):
		// a conexão está viva, só não manda mensagens. Não derrubar.
		if client.inFlight.Load() > 0 {
			continue
		}
		timeout := ipcServerIdleTimeout
		if client.admin {
			// O canal admin não recebe o snapshot de 5s: o teto é a ociosidade
			// da CONEXÃO (5min), não o TTL do token — senão uma UI saudável
			// perderia o canal de mutação a cada minuto. O token continua 12h.
			timeout = s.adminIdle()
		}
		if client.idleFor(now) > timeout {
			idle = append(idle, conn)
			idleAdmin = append(idleAdmin, client.admin)
		}
	}
	for _, conn := range idle {
		delete(s.conns, conn)
	}
	s.mu.Unlock()

	for i, conn := range idle {
		if idleAdmin[i] {
			ipcAuthStats.AdminIdleClosed.Add(1)
			log.Printf("[ipc] canal admin ocioso há mais de %s — encerrando (a UI reabre sob demanda)", s.adminIdle())
		} else {
			log.Printf("[ipc] UI sem mensagens há %s — tratando como travada e desconectando", ipcServerIdleTimeout)
		}
		conn.Close()
	}
	return len(idle)
}

// adminIdle devolve o teto de ociosidade do canal admin (injetável em teste).
func (s *IPCServer) adminIdle() time.Duration {
	if s != nil && s.adminIdleTimeout > 0 {
		return s.adminIdleTimeout
	}
	return ipcAdminIdleTimeout
}

// acceptLoop aceita clientes de UM listener até o servidor ser fechado.
// admin=true identifica o canal mutante.
func (s *IPCServer) acceptLoop(l net.Listener, admin bool) {
	if l == nil {
		return
	}
	for {
		conn, err := l.Accept()
		if err != nil {
			if s.closed.Load() {
				return
			}
			log.Printf("[ipc] accept falhou (admin=%t): %v", admin, err)
			time.Sleep(time.Second)
			continue
		}
		if s.connCount() >= ipcMaxConnections {
			log.Printf("[ipc] recusando conexão (admin=%t): limite global de %d conexões atingido", admin, ipcMaxConnections)
			conn.Close()
			continue
		}
		if admin && s.adminConnCount() >= ipcMaxAdminConnections {
			ipcAuthStats.AdminConnRefused.Add(1)
			log.Printf("[ipc] recusando conexão admin: teto de %d conexões admin atingido (a UI reabre sob demanda)", ipcMaxAdminConnections)
			conn.Close()
			continue
		}
		log.Printf("[ipc] cliente conectado (admin=%t): %s", admin, conn.RemoteAddr())
		go s.handleConn(conn, admin)
	}
}

// connCount devolve o total de conexões (leitura + admin).
func (s *IPCServer) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// adminConnCount devolve quantas conexões do canal ADMIN existem (teto próprio,
// separado do global de leitura — A.3.2/DoS).
func (s *IPCServer) adminConnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adminConnCountLocked()
}

// adminConnCountLocked exige s.mu.
func (s *IPCServer) adminConnCountLocked() int {
	n := 0
	for _, client := range s.conns {
		if client.admin {
			n++
		}
	}
	return n
}

// handleConn lê mensagens de um cliente conectado.
//
// Segurança (decisão S1/S2/S3/C3):
//   - pipe admin: valida SID/PID/sessão do processo chamador ANTES de aceitar;
//     falhou a validação, fecha a conexão;
//   - rate limit (50/s e 8 em voo por conexão) e dedup de cid são aplicados
//     ANTES de despachar a goroutine do RPC;
//   - canal admin só aceita método mutante; canal de leitura só aceita leitura;
//   - todo request mutante precisa de token válido daquela conexão/sessão.
func (s *IPCServer) handleConn(conn net.Conn, admin bool) {
	client := &ipcClientConn{
		reader:       bufio.NewReader(conn),
		admin:        admin,
		connKey:      ipcNewConnKey(),
		limiter:      newIPCRequestLimiter(),
		helloLimiter: newIPCHelloLimiter(),
		dedup:        newIPCDedupCache(),
	}
	if id, err := ipcCallerValidator(conn); err == nil {
		client.identity = id
		// A.3.2/A.3.5: a MESMA validação (SID/PID/sessão + imagem da UI) roda
		// nos dois canais. No canal de leitura, um chamador que não é a UI
		// (probe/ferramenta/UI antiga) continua conectado, mas NÃO conta como
		// identidade autenticada — não quebra o probe IsServicePresent.
		client.identityOK = id.Valid && id.UIImage
		if id.Valid && !client.identityOK && ipcCallerIsTrustedUI(id) {
			// Bypass de dev (DISCOVERY_IPC_ALLOW_ANY_CALLER=1): a build de
			// desenvolvimento roda FORA do diretório de instalação, então o
			// bypass precisa valer nos DOIS canais. Antes ele só liberava o
			// token admin: a UI de dev mutava, mas não recebia entrega
			// targeted, não conseguia responder prompt (notification:respond) e
			// ficava fora da contagem de UIs online.
			client.identityOK = true
		}
		if admin && !client.identityOK {
			// A.0.1: chamador válido porém NÃO-UI no canal admin. Não emite
			// token: responde hello_ack sem token e fecha (mais abaixo).
			client.callerNotAllowed = true
			ipcAuthStats.CallerNotAllowed.Add(1)
			ipcAuthStats.CallerValidationFail.Add(1)
			logIPCStructured("ipc_caller_not_allowed", fmt.Sprintf(
				"admin=true %s image=%q reason=not_discovery_ui", id.String(), id.ImagePath))
		}
	} else {
		ipcAuthStats.CallerValidationFail.Add(1)
		log.Printf("[ipc] validação de chamador falhou (admin=%t): %v", admin, err)
		if admin {
			// Decisão: no canal administrativo, não validar = recusar.
			conn.Close()
			return
		}
	}

	client.touch()
	s.mu.Lock()
	if len(s.conns) >= ipcMaxConnections {
		s.mu.Unlock()
		log.Printf("[ipc] recusando conexão (admin=%t): limite global de %d conexões", admin, ipcMaxConnections)
		conn.Close()
		return
	}
	if admin && s.adminConnCountLocked() >= ipcMaxAdminConnections {
		s.mu.Unlock()
		ipcAuthStats.AdminConnRefused.Add(1)
		log.Printf("[ipc] recusando conexão admin: teto de %d conexões admin atingido (a UI reabre sob demanda)", ipcMaxAdminConnections)
		conn.Close()
		return
	}
	s.conns[conn] = client
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		if s.tokens != nil {
			s.tokens.invalidateConn(client.connKey)
		}
		conn.Close()
		log.Printf("[ipc] cliente desconectado (admin=%t)", admin)
	}()

	for {
		if s.closed.Load() {
			return
		}
		msg, err := DecodeIPCMessage(client.reader)
		if err != nil {
			return // EOF ou erro de parse — encerra a conexão
		}
		client.touch()

		// hello é respondido DIRETAMENTE na conexão (o probe IsServicePresent
		// espera hello_ack na mesma conexão antes de fechar). No canal admin o
		// hello emite/renova o token de sessão.
		if msg.Type == IPCMsgHello {
			// Rate limit próprio do hello (10/s por conexão): hello
			// emite/rotaciona token, então rajadas são cortadas fechando a
			// conexão. O probe manda UM hello em conexão nova — sempre passa.
			release, helloErr := client.helloLimiter.begin()
			if helloErr != nil {
				ipcAuthStats.HelloRateLimited.Add(1)
				logIPCStructured("hello_rate_limited", fmt.Sprintf(
					"admin=%t %s %v — fechando conexão", client.admin, client.identity.String(), helloErr))
				conn.Close()
				return
			}
			release()
			s.sendHelloAck(client, conn)
			if client.callerNotAllowed {
				// hello_ack sem token já foi emitido; não há mais nada a fazer
				// nesta conexão (A.0.1).
				return
			}
		}

		release := func() {}
		if msg.Type != IPCMsgHello {
			var limitErr error
			release, limitErr = client.limiter.begin()
			if limitErr != nil {
				if limitErr == errIPCRateLimited {
					ipcAuthStats.RateLimited.Add(1)
				} else {
					ipcAuthStats.InFlightLimited.Add(1)
				}
				logIPCStructured("request_limited", fmt.Sprintf("admin=%t %s %s cid=%q",
					client.admin, client.identity.String(), limitErr, msg.CorrelationID))
				code := "ipc_rate_limited"
				if limitErr == errIPCInFlight {
					code = "ipc_inflight_limit"
				}
				s.respondIPCError(client, conn, msg.CorrelationID, code, limitErr.Error())
				continue
			}
		}

		if s.OnMessage == nil {
			release()
			continue
		}
		if !s.authorizeInbound(client, conn, msg) {
			release()
			continue
		}

		// Dedup de cid por conexão (janela de 60s): repetido devolve a resposta
		// já emitida ou ignora enquanto o original está em voo.
		if msg.Type == IPCMsgRequest {
			if cached, duplicate := client.dedup.begin(msg.CorrelationID); duplicate {
				ipcAuthStats.DuplicateCID.Add(1)
				logIPCStructured("duplicate_cid", fmt.Sprintf("cid=%q %s", msg.CorrelationID, client.identity.String()))
				if cached != nil {
					reply := NewIPCMessage(IPCMsgResponse, cached)
					reply.CorrelationID = msg.CorrelationID
					_ = client.writeTo(conn, EncodeIPCMessage(reply))
				}
				release()
				continue
			}
		}

		// RPC (Fase C) é despachado em goroutine: um RPC lento (ex.: leitura
		// do snapshot offline de chamados/knowledge, muitos segundos) bloquearia
		// o read loop DESTA conexão — o CompanionController deixaria de receber
		// os snapshots de status e o rxWatchdog do cliente fecharia o pipe.
		// RespondTo serializa a escrita pelo writeMu da conn e o cliente roteia
		// as respostas por CorrelationID (out-of-order é seguro).
		if msg.Type == IPCMsgRequest {
			go func() {
				defer release()
				s.OnMessage(conn, msg)
			}()
		} else {
			s.OnMessage(conn, msg)
			release()
		}
	}
}

// ipcNewConnKey gera a chave aleatória que amarra o token a UMA conexão.
func ipcNewConnKey() string {
	key, err := ipcRandomToken()
	if err != nil {
		// Fallback improvável: ainda assim único por conexão.
		return fmt.Sprintf("conn-%d", time.Now().UnixNano())
	}
	return key
}

// authorizeInbound aplica o gate de canal/token/identidade ANTES do dispatch.
func (s *IPCServer) authorizeInbound(client *ipcClientConn, conn net.Conn, msg IPCMessage) bool {
	switch msg.Type {
	case IPCMsgRequest:
		method, _ := msg.Payload["method"].(string)
		mutating := isIPCMutatingRequest(method, msg.Payload)
		if client.admin && !mutating {
			ipcAuditMutating(method, client.identity, "denied_wrong_channel")
			s.respondIPCError(client, conn, msg.CorrelationID, "ipc_wrong_channel",
				"método de leitura não permitido no canal admin")
			return false
		}
		if !client.admin && mutating {
			ipcAuthStats.Unauthorized.Add(1)
			ipcAuditMutating(method, client.identity, "denied_read_channel")
			s.respondIPCError(client, conn, msg.CorrelationID, "ipc_unauthorized", "unauthorized")
			return false
		}
		if mutating {
			if err := s.validateMutatingToken(client, msg.Payload); err != nil {
				ipcAuthStats.Unauthorized.Add(1)
				ipcAuditMutating(method, client.identity, "denied_"+err.Error())
				s.respondIPCError(client, conn, msg.CorrelationID, "ipc_unauthorized", "unauthorized")
				return false
			}
			ipcAuditMutating(method, client.identity, "allowed")
		}
		return true

	case IPCMsgRemoteSession:
		// S2: nunca spawna worker sem canal admin + token + binding validados.
		if err := s.validateMutatingToken(client, msg.Payload); err != nil {
			ipcAuthStats.Unauthorized.Add(1)
			s.denyRemoteSession(client, "token: "+err.Error())
			return false
		}
		if err := ipcRemoteSessionAllowed(client.identity, client.admin, true, msg.Payload); err != nil {
			ipcAuthStats.RemoteSessionDenied.Add(1)
			s.denyRemoteSession(client, err.Error())
			return false
		}
		return true

	case IPCMsgNotificationRespond:
		// S3: só a conexão autenticada da sessão destinatária (ou SYSTEM).
		if !s.notificationRespondAllowed(client, msg.Payload) {
			ipcAuthStats.NotificationDenied.Add(1)
			return false
		}
		return true
	}
	return true
}

// authTokens devolve (inicializando se preciso) o store de tokens do servidor.
func (s *IPCServer) authTokens() *ipcTokenStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = newIPCTokenStore()
	}
	return s.tokens
}

// validateMutatingToken exige o token no payload e o valida contra a conexão e
// a identidade validada do chamador.
func (s *IPCServer) validateMutatingToken(client *ipcClientConn, payload map[string]any) error {
	if client == nil {
		return errIPCTokenMissing
	}
	if !client.admin {
		return errIPCWrongChannel
	}
	if !client.identityOK {
		return errIPCTokenMissing
	}
	token, _ := payload["token"].(string)
	_, err := s.authTokens().validate(token, client.connKey, client.identity)
	client.authMu.Lock()
	client.tokenValid = err == nil
	client.authMu.Unlock()
	return err
}

// denyRemoteSession registra a recusa sem jamais spawnar worker.
func (s *IPCServer) denyRemoteSession(client *ipcClientConn, reason string) {
	logIPCStructured("remote_session_denied", fmt.Sprintf("%s conn=%s reason=%q",
		client.identity.String(), client.connKey, reason))
}

// notificationRespondAllowed confere o vínculo notificationId → sessão.
func (s *IPCServer) notificationRespondAllowed(client *ipcClientConn, payload map[string]any) bool {
	notificationID, _ := payload["notificationId"].(string)
	allowed := client.identityOK && ipcNotificationTargetAllows(notificationID, client.identity)
	if !allowed {
		logIPCStructured("notification_respond_denied", fmt.Sprintf("notification=%q %s",
			notificationID, client.identity.String()))
	}
	return allowed
}

// sendHelloAck responde o handshake. No canal admin emite (ou renova) o token.
func (s *IPCServer) sendHelloAck(client *ipcClientConn, conn net.Conn) {
	payload := map[string]any{
		"service":  ServiceName,
		"protocol": IPCProtocolVersion,
		"version":  buildinfo.Version,
	}
	// A.3.5: identidade/sessão no handshake dos DOIS canais. Campos ADITIVOS —
	// a UI antiga ignora e o probe IsServicePresent só lê service/protocol.
	if client.identity.Valid {
		payload["sessionId"] = client.identity.SessionID
		if strings.TrimSpace(client.identity.SID) != "" {
			payload["sid"] = client.identity.SID
		}
	}
	if client.admin {
		if client.callerNotAllowed {
			// A.0.1: chamador não é a UI → hello_ack SEM token e conexão fechada
			// pelo handleConn. Não há como obter token por este canal.
			payload["code"] = "ipc_caller_not_allowed"
			payload["error"] = "ipc_caller_not_allowed"
			if err := client.writeTo(conn, EncodeIPCMessage(NewIPCMessage(IPCMsgHelloAck, payload))); err != nil {
				log.Printf("[ipc] falha ao responder hello_ack (chamador não autorizado): %v", err)
			}
			return
		}
		if !client.identityOK {
			return
		}
		token, expires, err := s.authTokens().issue(client.connKey, client.identity)
		if err != nil {
			log.Printf("[ipc] falha ao emitir token: %v", err)
			return
		}
		client.authMu.Lock()
		client.token = token
		client.tokenExpiry = expires
		client.tokenValid = true
		client.authMu.Unlock()
		ipcAuthStats.TokenIssued.Add(1)
		payload["token"] = token
		payload["expiresAt"] = expires.UTC().Format(time.RFC3339)
		logIPCStructured("token_issued", fmt.Sprintf("%s conn=%s expires=%s",
			client.identity.String(), client.connKey, expires.UTC().Format(time.RFC3339)))
	}
	if err := client.writeTo(conn, EncodeIPCMessage(NewIPCMessage(IPCMsgHelloAck, payload))); err != nil {
		log.Printf("[ipc] falha ao responder hello_ack: %v", err)
	}
}

// respondIPCError envia uma resposta de erro para o cid informado.
func (s *IPCServer) respondIPCError(client *ipcClientConn, conn net.Conn, cid, code, message string) {
	if client == nil || conn == nil {
		return
	}
	reply := NewIPCMessage(IPCMsgResponse, map[string]any{
		"ok":    false,
		"error": message,
		"code":  code,
	})
	reply.CorrelationID = cid
	if err := client.writeTo(conn, EncodeIPCMessage(reply)); err != nil {
		log.Printf("[ipc] falha ao responder erro IPC: %v", err)
	}
}

// ── Validação do chamador / sessão (Windows) ───────────────────────────────

// ipcCallerValidator é substituível em testes (net.Pipe não expõe handle de
// named pipe, então a validação real não roda nesses testes).
var ipcCallerValidator = ipcValidatePipeCaller

// ipcProcessImagePath resolve o caminho completo da imagem do processo
// chamador (QueryFullProcessImageName). Substituível em teste.
var ipcProcessImagePath = ipcQueryProcessImagePath

// ipcActiveSessionProvider resolve a sessão interativa ativa (console). Em
// testes pode ser substituído para fixar a sessão destinatária.
var ipcActiveSessionProvider = ipcActiveSessionIdentity

// ipcValidatePipeCaller prova quem está do outro lado do pipe:
// GetNamedPipeClientProcessId → ProcessIdToSessionId → OpenProcess →
// OpenProcessToken/GetTokenInformation(TokenUser). Qualquer falha = recusa.
func ipcValidatePipeCaller(conn net.Conn) (ipcAuthIdentity, error) {
	var id ipcAuthIdentity
	if conn == nil {
		return id, fmt.Errorf("conexão nil")
	}
	fdConn, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return id, fmt.Errorf("conexão sem handle de pipe (Fd)")
	}
	handle := windows.Handle(fdConn.Fd())

	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(handle, &pid); err != nil {
		return id, fmt.Errorf("GetNamedPipeClientProcessId: %w", err)
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(pid, &session); err != nil {
		return id, fmt.Errorf("ProcessIdToSessionId(%d): %w", pid, err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return id, fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(proc)
	var token windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &token); err != nil {
		return id, fmt.Errorf("OpenProcessToken(%d): %w", pid, err)
	}
	defer token.Close()
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return id, fmt.Errorf("GetTokenUser(%d): %w", pid, err)
	}
	sid := tokenUser.User.Sid.String()
	id = ipcAuthIdentity{
		SID:       sid,
		PID:       pid,
		SessionID: session,
		System:    strings.EqualFold(sid, "S-1-5-18"),
		Valid:     true,
	}
	// A.0.1: a identidade acima prova QUEM é o chamador; a imagem prova se ele
	// é a UI (discovery-agent.exe sob %ProgramFiles%/Discovery etc.). Falha ao
	// resolver a imagem NÃO invalida SID/PID/sessão — só deixa UIImage=false,
	// e o canal admin nega o token.
	if imagePath, imgErr := ipcProcessImagePath(pid); imgErr == nil {
		id.ImagePath = imagePath
		id.UIImage = ipcIsDiscoveryUIImagePath(imagePath, ipcAllowedUIDirectories())
	} else {
		log.Printf("[ipc] QueryFullProcessImageName(%d) falhou: %v", pid, imgErr)
	}
	return id, nil
}

// ipcQueryProcessImagePath devolve o caminho completo da imagem do processo.
func ipcQueryProcessImagePath(pid uint32) (string, error) {
	if pid == 0 {
		return "", fmt.Errorf("pid inválido")
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(proc)
	// Buffer folgado: caminhos longos sob Program Files continuam válidos.
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(proc, 0, &buf[0], &size); err != nil {
		return "", fmt.Errorf("QueryFullProcessImageName(%d): %w", pid, err)
	}
	return windows.UTF16ToString(buf[:size]), nil
}

// ipcActiveSessionIdentity devolve SID/sessão do usuário do console. SID pode
// ficar vazio (sem token de usuário na sessão); a sessão continua válida.
func ipcActiveSessionIdentity() ipcAuthIdentity {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF {
		return ipcAuthIdentity{}
	}
	id := ipcAuthIdentity{SessionID: session, Valid: true}
	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		log.Printf("[ipc] WTSQueryUserToken(session=%d) falhou: %v", session, err)
		return id
	}
	defer token.Close()
	if tokenUser, err := token.GetTokenUser(); err == nil && tokenUser.User.Sid != nil {
		id.SID = tokenUser.User.Sid.String()
		id.System = strings.EqualFold(id.SID, "S-1-5-18")
	}
	return id
}

// currentNotificationTarget resolve o destinatário do prompt: sessão do
// console quando disponível; senão a primeira UI autenticada conectada.
// (Sessões RDP não têm console — o fallback evita negar respostas legítimas.)
func (s *IPCServer) currentNotificationTarget() (ipcAuthIdentity, bool) {
	if id := ipcActiveSessionProvider(); id.Valid {
		return id, true
	}
	if s == nil {
		return ipcAuthIdentity{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, client := range s.conns {
		if client.admin || !client.identityOK {
			continue
		}
		return client.identity, true
	}
	return ipcAuthIdentity{}, false
}

// ── API de autorização usada pelo dispatch (ipc_app_integration.go) ────────

// clientFor devolve o estado da conexão (nil-safe).
func (s *IPCServer) clientFor(conn net.Conn) *ipcClientConn {
	if s == nil || conn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[conn]
}

// authorizeRPC revalida canal/token no momento do dispatch e devolve o
// context autenticado que o gate de handleIPCRequest exige para mutações.
func (s *IPCServer) authorizeRPC(conn net.Conn, msg IPCMessage) (context.Context, error) {
	ctx := context.Background()
	method, _ := msg.Payload["method"].(string)
	client := s.clientFor(conn)
	mutating := isIPCMutatingRequest(method, msg.Payload)
	if client == nil {
		if mutating {
			return ctx, errIPCTokenMissing
		}
		return ctx, nil
	}
	if client.admin {
		if !mutating {
			return ctx, errIPCWrongChannel
		}
		if err := s.validateMutatingToken(client, msg.Payload); err != nil {
			return ctx, err
		}
		return withIPCAuth(ctx, ipcRequestAuth{
			ConnKey:   client.connKey,
			SID:       client.identity.SID,
			PID:       client.identity.PID,
			SessionID: client.identity.SessionID,
			Admin:     true,
		}), nil
	}
	if mutating {
		return ctx, errIPCWrongChannel
	}
	return ctx, nil
}

// recordRequestResult alimenta o dedup de cid com a resposta realmente emitida.
func (s *IPCServer) recordRequestResult(conn net.Conn, cid string, response map[string]any) {
	if client := s.clientFor(conn); client != nil && client.dedup != nil {
		client.dedup.finish(cid, response)
	}
}

// authorizeRemoteSessionMessage é a checagem final antes de spawnar worker
// (S2): canal admin + token + vínculo pid/sessão do chamador.
func (s *IPCServer) authorizeRemoteSessionMessage(conn net.Conn, payload map[string]any) error {
	client := s.clientFor(conn)
	if client == nil {
		return errIPCTokenMissing
	}
	if err := s.validateMutatingToken(client, payload); err != nil {
		return err
	}
	return ipcRemoteSessionAllowed(client.identity, client.admin, true, payload)
}

// authorizeNotificationRespond é a checagem final da resposta de prompt (S3).
func (s *IPCServer) authorizeNotificationRespond(conn net.Conn, payload map[string]any) bool {
	client := s.clientFor(conn)
	if client == nil {
		return false
	}
	return s.notificationRespondAllowed(client, payload)
}

// SendToSession envia um evento SOMENTE às conexões AUTENTICADAS da sessão
// informada (A.3.5) e devolve quantas receberam. O canal admin não é destino de
// eventos e conexões não autenticadas (identityOK=false) nunca recebem targeted.
func (s *IPCServer) SendToSession(sessionID uint32, msg IPCMessage) int {
	if s == nil {
		return 0
	}
	data := EncodeIPCMessage(msg)
	if data == nil {
		return 0
	}

	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	clients := make([]*ipcClientConn, 0, len(s.conns))
	for conn, client := range s.conns {
		if client.admin || !client.identityOK || client.identity.SessionID != sessionID {
			continue
		}
		conns = append(conns, conn)
		clients = append(clients, client)
	}
	s.mu.Unlock()

	sent := 0
	var failed []net.Conn
	for i, conn := range conns {
		if err := clients[i].writeTo(conn, data); err != nil {
			log.Printf("[ipc] envio por sessão falhou para %s (removendo): %v", conn.RemoteAddr(), err)
			failed = append(failed, conn)
			continue
		}
		sent++
	}
	if len(failed) > 0 {
		s.mu.Lock()
		for _, conn := range failed {
			delete(s.conns, conn)
		}
		s.mu.Unlock()
		for _, conn := range failed {
			go conn.Close()
		}
	}
	return sent
}

// Broadcast envia uma mensagem para todos os clientes UI conectados.
// Revisão 2026-09-05: conns que falham na escrita são REMOVIDAS do map — antes
// ficavam para sempre como zumbis, e cada broadcast subsequente gastava o
// deadline de 3s tentando escrever nelas (log "broadcast falhou: i/o timeout"
// em loop), atrasando a entrega às UIs vivas.
//
// B3: a escrita de rede acontece FORA de s.mu — antes, o lock global era
// segurado durante todos os writes (até 3s por conn zumbi), serializando
// broadcasts entre si e travando RespondTo/handshake. O mutex de escrita é
// por conn (ipcClientConn.writeTo).
func (s *IPCServer) Broadcast(msg IPCMessage) {
	if s == nil {
		return
	}
	data := EncodeIPCMessage(msg)
	if data == nil {
		return
	}

	s.mu.Lock()
	clients := make([]*ipcClientConn, 0, len(s.conns))
	conns := make([]net.Conn, 0, len(s.conns))
	for conn, client := range s.conns {
		// Eventos são do canal de LEITURA: o admin não é uma UI e não deve
		// receber broadcast (evita poluir a conexão de controle).
		if client.admin {
			continue
		}
		conns = append(conns, conn)
		clients = append(clients, client)
	}
	s.mu.Unlock()

	var failed []net.Conn
	for i, conn := range conns {
		if err := clients[i].writeTo(conn, data); err != nil {
			log.Printf("[ipc] broadcast falhou para %s (removendo): %v", conn.RemoteAddr(), err)
			failed = append(failed, conn)
		}
	}
	if len(failed) == 0 {
		return
	}
	s.mu.Lock()
	for _, conn := range failed {
		delete(s.conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range failed {
		go conn.Close()
	}
}

// RespondTo envia uma mensagem diretamente a UM cliente (reply na conn de
// origem). Usado pelo handler de status para responder à UI que pediu.
// B3: serializa a escrita com broadcasts concorrentes via writeMu da conn.
func (s *IPCServer) RespondTo(conn net.Conn, msg IPCMessage) {
	if conn == nil {
		return
	}
	data := EncodeIPCMessage(msg)
	if data == nil {
		return
	}
	s.mu.Lock()
	client, ok := s.conns[conn]
	s.mu.Unlock()
	if !ok {
		// Conn já removida (desconectada) — write seria descartado de qualquer
		// forma; mantemos o comportamento anterior de tentar escrever direto
		// para não quebrar replies durante a desconexão.
		conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write(data); err != nil {
			log.Printf("[ipc] reply falhou para %s: %v", conn.RemoteAddr(), err)
		}
		return
	}
	if err := client.writeTo(conn, data); err != nil {
		log.Printf("[ipc] reply falhou para %s: %v", conn.RemoteAddr(), err)
	}
}

// UIAuthenticatedCount devolve quantas UIs AUTENTICADAS estão conectadas,
// contando PROCESSOS distintos (não conexões): uma mesma UI pode abrir mais de
// uma conexão de leitura e continua sendo UMA UI. Conexões não autenticadas
// (probe, ferramenta, UI antiga fora do diretório de instalação) e o canal admin
// não contam — é este número que alimenta uiOnline/uiOnlineCount no heartbeat.
func (s *IPCServer) UIAuthenticatedCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pids := make(map[uint32]struct{}, len(s.conns))
	unknownPID := 0
	for _, client := range s.conns {
		if client.admin || !client.identityOK {
			continue
		}
		if pid := client.identity.PID; pid != 0 {
			pids[pid] = struct{}{}
			continue
		}
		// Identidade sem PID (só em teste): não há como agrupar, conta a conexão.
		unknownPID++
	}
	return len(pids) + unknownPID
}

// ClientCount retorna quantas conexões do canal de leitura existem (o canal
// admin não conta). NÃO é "quantas UIs": inclui chamadores não autenticados e
// conta conexões, não processos — para presença/UI online use
// UIAuthenticatedCount.
func (s *IPCServer) ClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, client := range s.conns {
		if !client.admin {
			n++
		}
	}
	return n
}

// Close encerra o servidor e todas as conexões.
func (s *IPCServer) Close() {
	if s == nil || !s.closed.CompareAndSwap(false, true) {
		return
	}
	if s.listener != nil {
		s.listener.Close()
	}
	if s.adminListener != nil {
		s.adminListener.Close()
	}
	s.mu.Lock()
	for conn := range s.conns {
		conn.Close()
	}
	s.conns = make(map[net.Conn]*ipcClientConn)
	s.mu.Unlock()
}

// ── Cliente (lado da UI) ────────────────────────────────────────────────────

// IPCClient conecta a UI ao serviço. Reconecta automaticamente com backoff.
type IPCClient struct {
	pipeName string

	conn net.Conn
	mu   sync.Mutex
	rx   *bufio.Reader
	// writeMu serializa escritas na conexão de LEITURA (C1): Send concorrente
	// intercalava bytes e corrompia o framing JSON-lines.
	writeMu sync.Mutex

	// Canal admin (mutações): conexão separada, token de sessão e mutex de
	// escrita próprio.
	adminMu      sync.Mutex
	adminConn    net.Conn
	adminRx      *bufio.Reader
	adminWriteMu sync.Mutex
	adminToken   string
	adminExpires time.Time
	adminHelloCh chan IPCServiceHelloAck
	// adminDown é fechado quando a conexão admin da vez morre. Um Request
	// mutante pendente (ex.: instalação de vários minutos) acorda na hora com
	// errIPCAdminConnClosed em vez de esperar o timeout inteiro sem saber que
	// a conexão sumiu.
	adminDown    chan struct{}
	adminRenewMu sync.Mutex
	// serviceVersion é a versão reportada pelo serviço no hello_ack (A.2.2).
	// Comparada com buildinfo.Version para avisar sobre par UI×serviço misto.
	serviceVersion   string
	serviceProtocol  int
	serviceVersionMu sync.Mutex

	closed     atomic.Bool
	reconnectN atomic.Int32

	// pendingRequests guarda canais de resposta por correlation-id
	// (PLANO_SEPARACAO_SERVICO_UI.md, Fase C — request/response).
	pendingRequests   map[string]chan IPCMessage
	pendingRequestsMu sync.Mutex
	requestSeq        atomic.Int64

	// downCh é sinalizado pelo readLoop quando a conexão cai — substitui
	// polling (busy-wait) no RunConnectLoop.
	downCh chan struct{}

	// lastRx guarda o timestamp da última mensagem/recebimento — usado pelo
	// watchdog para detectar pipe travado sem EOF (WinAPI pode não devolver
	// erro numa conn zumbi de named pipe).
	lastRx atomic.Int64

	// closedCh é fechado no Close() para encerrar o watchdog.
	closedCh chan struct{}

	// OnMessage é chamado para cada mensagem do serviço (event, hello_ack).
	OnMessage func(msg IPCMessage)
	// OnStateChange é chamado quando a conexão abre (true) ou cai (false).
	OnStateChange func(connected bool)
}

// NewIPCClient cria um cliente apontando ao pipe do serviço.
func NewIPCClient(onMessage func(IPCMessage), onStateChange func(bool)) *IPCClient {
	return &IPCClient{
		pipeName:        IPCPipeName,
		OnMessage:       onMessage,
		OnStateChange:   onStateChange,
		downCh:          make(chan struct{}, 1),
		closedCh:        make(chan struct{}),
		pendingRequests: make(map[string]chan IPCMessage),
		adminHelloCh:    make(chan IPCServiceHelloAck, 4),
	}
}

// Connect tenta conectar imediatamente (handshake rápido: "serviço ativo?").
func (c *IPCClient) Connect(timeout time.Duration) bool {
	conn, err := winio.DialPipe(c.pipeName, &timeout)
	if err != nil {
		return false
	}
	// Fecha qualquer conexão anterior ainda viva (nunca deveria acontecer — o
	// readLoop fecha ao sair — mas protege contra vazamento de conns zumbis).
	c.mu.Lock()
	old := c.conn
	c.conn = conn
	c.rx = bufio.NewReader(conn)
	c.lastRx.Store(time.Now().UnixMilli())
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	// Consome sinal down residual de uma conexão anterior.
	select {
	case <-c.downCh:
	default:
	}
	if c.OnStateChange != nil {
		c.OnStateChange(true)
	}
	// readLoop recebe conn/reader LOCAIS — nunca lê c.rx compartilhado. Isso
	// elimina a data race em que dois readLoops liam o mesmo bufio.Reader
	// (bufio não é thread-safe) quando a conexão era substituída.
	go c.readLoop(conn, c.rx)

	// Handshake do canal admin (token de sessão). Serviço antigo (sem pipe
	// admin) ou falha de validação deixam a UI em modo somente-leitura —
	// métodos mutantes retornam erro tratado.
	if !c.connectAdmin(3 * time.Second) {
		log.Printf("[ipc] canal admin indisponível — mutações ficarão bloqueadas até reconectar")
	}
	return true
}

// connectAdmin abre o pipe admin e faz o hello para obter o token de sessão.
func (c *IPCClient) connectAdmin(timeout time.Duration) bool {
	conn, err := winio.DialPipe(IPCPipeAdminName, &timeout)
	if err != nil {
		return false
	}
	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(EncodeIPCMessage(NewIPCMessage(IPCMsgHello, map[string]any{
		"pid":      os.Getpid(),
		"protocol": IPCProtocolVersion,
	}))); err != nil {
		conn.Close()
		return false
	}
	reader := bufio.NewReader(conn)
	msg, err := DecodeIPCMessage(reader)
	if err != nil || msg.Type != IPCMsgHelloAck {
		conn.Close()
		return false
	}
	ack := helloAckFromPayload(msg.Payload)
	if strings.TrimSpace(ack.Token) == "" {
		conn.Close()
		log.Printf("[ipc] canal admin recusou o handshake (sem token): %v", msg.Payload)
		return false
	}
	conn.SetDeadline(time.Time{})

	down := make(chan struct{})
	c.adminMu.Lock()
	old := c.adminConn
	c.adminConn = conn
	c.adminRx = reader
	c.adminDown = down
	c.adminToken = ack.Token
	c.adminExpires = ack.ExpiresAt
	c.adminMu.Unlock()
	c.recordServiceHandshake(ack)
	if old != nil {
		old.Close()
	}
	go c.adminReadLoop(conn, reader, down)
	log.Printf("[ipc] canal admin autenticado (token expira em %s)", ack.ExpiresAt.Format(time.RFC3339))
	return true
}

// adminReadLoop roteia respostas de RPC do canal admin e hello_ack de renovação.
func (c *IPCClient) adminReadLoop(conn net.Conn, rx *bufio.Reader, down chan struct{}) {
	defer func() {
		c.adminMu.Lock()
		if c.adminConn == conn {
			c.adminConn = nil
			c.adminRx = nil
			c.adminDown = nil
			c.adminToken = ""
			c.adminExpires = time.Time{}
		}
		c.adminMu.Unlock()
		conn.Close()
		// Acorda Requests mutantes pendentes: sem isso eles esperariam o
		// timeout inteiro (45 min na instalação da loja) por uma resposta que
		// nunca chegará.
		close(down)
		log.Printf("[ipc] canal admin encerrado")
	}()
	for {
		msg, err := DecodeIPCMessage(rx)
		if err != nil {
			return
		}
		switch {
		case msg.Type == IPCMsgHelloAck:
			select {
			case c.adminHelloCh <- helloAckFromPayload(msg.Payload):
			default:
			}
		case msg.Type == IPCMsgResponse && msg.CorrelationID != "":
			c.routeResponse(msg)
		default:
			// O canal admin não recebe eventos; ignora mensagens inesperadas.
		}
	}
}

// routeResponse entrega uma resposta ao canal pendente pelo cid.
func (c *IPCClient) routeResponse(msg IPCMessage) {
	c.pendingRequestsMu.Lock()
	ch, ok := c.pendingRequests[msg.CorrelationID]
	if ok {
		delete(c.pendingRequests, msg.CorrelationID)
	}
	c.pendingRequestsMu.Unlock()
	if ok {
		select {
		case ch <- msg:
		default:
		}
	}
}

// adminAuth devolve uma conexão admin com token válido, renovando via hello
// quando necessário, e o canal "down" daquela conexão. Nunca segura adminMu
// durante I/O.
func (c *IPCClient) adminAuth() (net.Conn, string, <-chan struct{}, error) {
	c.adminMu.Lock()
	conn := c.adminConn
	token := c.adminToken
	expires := c.adminExpires
	down := c.adminDown
	c.adminMu.Unlock()

	if conn == nil {
		if !c.connectAdmin(3 * time.Second) {
			return nil, "", nil, fmt.Errorf("canal admin indisponível (serviço sem autenticação?)")
		}
		c.adminMu.Lock()
		conn, token, expires, down = c.adminConn, c.adminToken, c.adminExpires, c.adminDown
		c.adminMu.Unlock()
	}
	if strings.TrimSpace(token) == "" || (!expires.IsZero() && time.Until(expires) < time.Minute) {
		if err := c.renewAdminToken(); err != nil {
			return nil, "", nil, err
		}
		c.adminMu.Lock()
		conn, token, down = c.adminConn, c.adminToken, c.adminDown
		c.adminMu.Unlock()
	}
	if conn == nil || strings.TrimSpace(token) == "" {
		return nil, "", nil, fmt.Errorf("canal admin sem token válido")
	}
	return conn, token, down, nil
}

// renewAdminToken envia hello no canal admin e aguarda o novo hello_ack.
func (c *IPCClient) renewAdminToken() error {
	c.adminRenewMu.Lock()
	defer c.adminRenewMu.Unlock()

	// Outra goroutine pode já ter renovado enquanto esperávamos o mutex.
	c.adminMu.Lock()
	conn := c.adminConn
	currentToken := c.adminToken
	currentExpiry := c.adminExpires
	c.adminMu.Unlock()
	if conn != nil && strings.TrimSpace(currentToken) != "" && !currentExpiry.IsZero() &&
		time.Until(currentExpiry) > time.Minute {
		return nil
	}
	if conn == nil {
		if !c.connectAdmin(3 * time.Second) {
			return fmt.Errorf("canal admin indisponível")
		}
		return nil
	}
	if err := c.sendOn(conn, &c.adminWriteMu, NewIPCMessage(IPCMsgHello, map[string]any{
		"pid":      os.Getpid(),
		"protocol": IPCProtocolVersion,
	})); err != nil {
		return err
	}
	select {
	case ack := <-c.adminHelloCh:
		if strings.TrimSpace(ack.Token) == "" {
			return fmt.Errorf("hello_ack admin sem token")
		}
		c.adminMu.Lock()
		c.adminToken = ack.Token
		c.adminExpires = ack.ExpiresAt
		c.adminMu.Unlock()
		c.recordServiceHandshake(ack)
		return nil
	case <-time.After(3 * time.Second):
		return fmt.Errorf("timeout aguardando renovação do token admin")
	}
}

// applyAdminToken injeta o token no payload (cópia) de um request mutante.
func (c *IPCClient) applyAdminToken(payload map[string]any, token string) map[string]any {
	cp := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		cp[k] = v
	}
	cp["token"] = token
	return cp
}

// sendOn serializa a escrita em UMA conexão.
func (c *IPCClient) sendOn(conn net.Conn, mu *sync.Mutex, msg IPCMessage) error {
	if conn == nil {
		return fmt.Errorf("não conectado ao serviço")
	}
	data := EncodeIPCMessage(msg)
	if data == nil {
		return fmt.Errorf("falha ao serializar mensagem IPC")
	}
	mu.Lock()
	defer mu.Unlock()
	conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, err := conn.Write(data)
	return err
}

// RunConnectLoop mantém a conexão com backoff exponencial (2s→60s), padrão
// do agentconn. Bloqueia; chamar em goroutine. Encerra com Close().
//
// NOTA (revisão 2026-09-05): o safety-net anterior de 90s foi REMOVIDO — ele
// disparava sempre em conexões saudáveis e substituía a conn sem fechar a
// antiga, criando zumbis no servidor e duas readLoops lendo o mesmo reader
// (data race → crash da UI). A detecção de queda agora é: EOF/erro no
// readLoop + watchdog de inatividade (ver runRxWatchdog).
func (c *IPCClient) RunConnectLoop() {
	backoff := 2 * time.Second
	go c.runRxWatchdog()
	for {
		if c.closed.Load() {
			return
		}
		if c.Connect(3 * time.Second) {
			n := c.reconnectN.Add(1)
			log.Printf("[ipc] conectado ao serviço (tentativa %d)", n)
			backoff = 2 * time.Second
			// Bloqueia até o readLoop sinalizar a queda da conexão.
			<-c.downCh
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

// rxWatchdogInterval e rxWatchdogTimeout definem o heartbeat de inatividade:
// o serviço responde ao polling de status a cada 5s, então 30s sem nenhum
// recebimento indica pipe travado (sem EOF) — fecha e reconecta.
const (
	rxWatchdogInterval = 10 * time.Second
	rxWatchdogTimeout  = 30 * time.Second
	// ipcClientReadIdleTimeout é o prazo do read deadline do cliente: maior que
	// rxWatchdogTimeout para o diagnóstico do watchdog aparecer primeiro. Uma UI
	// saudável recebe resposta ao poll de status a cada 5s, então 45s sem
	// NENHUM byte indica conexão morta (o serviço fechou o lado dele após
	// "broadcast falhou" e o ReadFile em named pipe NÃO é desbloqueado pelo
	// Close() de outra goroutine no Windows — sem deadline o readLoop ficava
	// preso para sempre: UI viva, IPC morto, "agent travando").
	ipcClientReadIdleTimeout = 45 * time.Second
)

// runRxWatchdog fecha a conexão quando não há recebimento há rxWatchdogTimeout.
// O Close() faz o readLoop sair com erro → downCh → reconexão limpa.
func (c *IPCClient) runRxWatchdog() {
	ticker := time.NewTicker(rxWatchdogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.closedCh:
			return
		case <-ticker.C:
			if c.closed.Load() {
				return
			}
			c.mu.Lock()
			conn := c.conn
			last := c.lastRx.Load()
			c.mu.Unlock()
			if conn != nil && time.Since(time.UnixMilli(last)) > rxWatchdogTimeout {
				log.Printf("[ipc] watchdog: sem mensagens há %s — fechando conexão para reconectar", rxWatchdogTimeout)
				conn.Close()
			}
		}
	}
}

// readLoop consome mensagens do serviço até a conexão cair. Recebe conn e
// reader como parâmetros LOCAIS: nunca acessa c.conn/c.rx (que podem ter sido
// substituídos por uma reconexão), eliminando a race de dois readLooms lendo
// o mesmo bufio.Reader.
func (c *IPCClient) readLoop(conn net.Conn, rx *bufio.Reader) {
	defer func() {
		c.mu.Lock()
		// Só limpa c.conn se ainda aponta para ESTA conexão (evita apagar a
		// referência de uma reconexão mais recente).
		if c.conn == conn {
			c.conn = nil
			c.rx = nil
		}
		c.mu.Unlock()
		conn.Close()
		if c.OnStateChange != nil {
			c.OnStateChange(false)
		}
		// Sinaliza o RunConnectLoop para reconectar (não-bloqueante).
		select {
		case c.downCh <- struct{}{}:
		default:
		}
	}()

	deadlineWarned := false
	for {
		// Read deadline a cada iteração: renova o prazo para ipcClientReadIdleTimeout
		// a partir de agora. Se o pipe não entregar nada nesse prazo, a leitura
		// RETORNA por si (o Close() externo não desbloqueia ReadFile em named
		// pipe do Windows) e o readLoop sai → downCh → reconexão limpa.
		if err := conn.SetReadDeadline(time.Now().Add(ipcClientReadIdleTimeout)); err != nil && !deadlineWarned {
			deadlineWarned = true
			log.Printf("[ipc] read deadline indisponivel neste pipe: %v", err)
		}
		msg, err := DecodeIPCMessage(rx)
		c.mu.Lock()
		c.lastRx.Store(time.Now().UnixMilli())
		c.mu.Unlock()
		if err != nil {
			if strings.Contains(err.Error(), "i/o timeout") || errors.Is(err, os.ErrDeadlineExceeded) {
				log.Printf("[ipc] leitura ociosa há %s — descartando a conexão para reconectar", ipcClientReadIdleTimeout)
			} else {
				// Diagnóstico: registra POR QUE a leitura terminou (EOF limpo no
				// shutdown do serviço, pipe quebrado, timeout etc.). Sem este log
				// não dá para distinguir queda do serviço de pipe travado.
				log.Printf("[ipc] leitura do pipe encerrada: %v", err)
			}
			return
		}
		// Request/response (Fase C): respostas com correlation-id são roteadas
		// para o canal pendente correspondente, não ao OnMessage geral.
		if msg.Type == IPCMsgResponse && msg.CorrelationID != "" {
			c.pendingRequestsMu.Lock()
			ch, ok := c.pendingRequests[msg.CorrelationID]
			if ok {
				delete(c.pendingRequests, msg.CorrelationID)
			}
			c.pendingRequestsMu.Unlock()
			if ok {
				select {
				case ch <- msg:
				default:
				}
			}
			continue
		}
		if c.OnMessage != nil {
			c.OnMessage(msg)
		}
	}
}

// Send envia uma mensagem ao serviço.
//
// C1: a escrita é serializada por writeMu (antes, Send concorrente intercalava
// bytes e corrompia o framing JSON-lines). Mensagens de remote_session usam o
// canal ADMIN com token — o serviço só spawna worker para chamador autenticado.
func (c *IPCClient) Send(msg IPCMessage) error {
	if msg.Type == IPCMsgRemoteSession {
		conn, token, _, err := c.adminAuth()
		if err != nil {
			return err
		}
		msg.Payload = c.applyAdminToken(msg.Payload, token)
		return c.sendOn(conn, &c.adminWriteMu, msg)
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	return c.sendOn(conn, &c.writeMu, msg)
}

// Request envia um request ao serviço e aguarda a resposta com timeout
// (PLANO_SEPARACAO_SERVICO_UI.md, Fase C). method identifica o RPC
// (ex.: "status:pending_counts"); payload é enviado junto. Retorna
// (payload da resposta, nil) quando ok=true; erro caso contrário.
func (c *IPCClient) Request(ctx context.Context, method string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	// Cópia defensiva (revisão 2 — bug B6): não mutar o map do caller
	// ao injetar "method"; callers podem reutilizar o payload.
	payload = func() map[string]any {
		cp := make(map[string]any, len(payload)+1)
		for k, v := range payload {
			cp[k] = v
		}
		cp["method"] = method
		return cp
	}()
	cid := fmt.Sprintf("r-%d-%d", time.Now().UnixNano(), c.requestSeq.Add(1))

	ch := make(chan IPCMessage, 1)
	c.pendingRequestsMu.Lock()
	c.pendingRequests[cid] = ch
	c.pendingRequestsMu.Unlock()
	defer func() {
		c.pendingRequestsMu.Lock()
		delete(c.pendingRequests, cid)
		c.pendingRequestsMu.Unlock()
	}()

	reqMsg := IPCMessage{Type: IPCMsgRequest, Payload: payload, CorrelationID: cid, Timestamp: time.Now().UnixMilli()}
	// adminDown só é preenchido no caminho mutante: se a conexão admin cair
	// antes da resposta, o select abaixo acorda com errIPCAdminConnClosed.
	var adminDown <-chan struct{}
	if isIPCMutatingRequest(method, payload) {
		// Mutações vão pelo canal admin com token de sessão; se o serviço for
		// antigo (sem canal admin) devolvemos erro tratado — nunca caímos para
		// o pipe de leitura.
		conn, token, down, err := c.adminAuth()
		if err != nil {
			return nil, fmt.Errorf("ipc request %s: %w", method, err)
		}
		adminDown = down
		reqMsg.Payload = c.applyAdminToken(reqMsg.Payload, token)
		if err := c.sendOn(conn, &c.adminWriteMu, reqMsg); err != nil {
			return nil, err
		}
	} else if err := c.Send(reqMsg); err != nil {
		return nil, err
	}

	// O deadline do ctx do chamador é a fonte de verdade; IPCRequestTimeout só
	// vale quando não há deadline (contexto sem timeout).
	timeout := IPCRequestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	select {
	case resp := <-ch:
		if ok, _ := resp.Payload["ok"].(bool); !ok {
			errMsg, _ := resp.Payload["error"].(string)
			return resp.Payload, fmt.Errorf("ipc request %s falhou: %s", method, errMsg)
		}
		return resp.Payload, nil
	case <-adminDown:
		// A conexão admin morreu com o request em voo. NÃO esperar o timeout
		// (45 min na loja): o serviço pode estar executando a operação; a UI
		// recebe erro definitivo em vez de ficar pendurada.
		return nil, fmt.Errorf("ipc request %s: %w", method, errIPCAdminConnClosed)
	case <-time.After(timeout):
		return nil, fmt.Errorf("ipc request %s timeout (%s)", method, timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close encerra o cliente e o loop de reconexão.
func (c *IPCClient) Close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	close(c.closedCh) // encerra o watchdog
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()
	c.adminMu.Lock()
	if c.adminConn != nil {
		c.adminConn.Close()
		c.adminConn = nil
	}
	c.adminToken = ""
	c.adminExpires = time.Time{}
	c.adminMu.Unlock()
}

// IPCServiceHelloAck carrega a resposta do handshake do serviço.
type IPCServiceHelloAck struct {
	Service   string    `json:"service"`
	Protocol  int       `json:"protocol"`
	Version   string    `json:"version,omitempty"`
	Token     string    `json:"token,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

// helloAckFromPayload decodifica o payload do hello_ack (compatível com
// serviços antigos, que só mandavam service/protocol).
func helloAckFromPayload(payload map[string]any) IPCServiceHelloAck {
	var ack IPCServiceHelloAck
	ack.Service, _ = payload["service"].(string)
	// Serviços antigos (sem "protocol") = protocolo 1.
	ack.Protocol = 1
	if v, ok := payload["protocol"].(float64); ok {
		ack.Protocol = int(v)
	}
	ack.Version, _ = payload["version"].(string)
	ack.Token, _ = payload["token"].(string)
	switch v := payload["expiresAt"].(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			ack.ExpiresAt = t
		}
	case float64:
		ack.ExpiresAt = time.UnixMilli(int64(v))
	}
	return ack
}

// IsServicePresent faz um probe rápido de handshake ("serviço ativo?") —
// usado pela UI no startup para decidir companion vs standalone. Retorna
// true quando o serviço responde hello_ack com protocolo compatível
// (revisão 3 — handshake versionado: drift serviço/UI durante updates).
func IsServicePresent(timeout time.Duration) bool {
	_, ok := probeServiceHello(timeout)
	return ok
}

// probeServiceHello envia hello e devolve o hello_ack decodificado.
// ok=false quando o serviço não responde ou o payload é inválido.
func probeServiceHello(timeout time.Duration) (IPCServiceHelloAck, bool) {
	var ack IPCServiceHelloAck
	conn, err := winio.DialPipe(IPCPipeName, &timeout)
	if err != nil {
		return ack, false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(EncodeIPCMessage(NewIPCMessage(IPCMsgHello, map[string]any{
		"pid":      os.Getpid(),
		"protocol": IPCProtocolVersion,
	}))); err != nil {
		return ack, false
	}
	reader := bufio.NewReader(conn)
	msg, err := DecodeIPCMessage(reader)
	if err != nil || msg.Type != IPCMsgHelloAck {
		return ack, false
	}
	ack = helloAckFromPayload(msg.Payload)
	return ack, true
}

// IsServiceProtocolCompatible informa se o protocolo do serviço é compatível
// com a versão desta build. UI antiga + serviço novo: compatível enquanto o
// serviço não abandonar a versão 1/2 (regras de compat por faixa).
func IsServiceProtocolCompatible(protocol int) bool {
	return protocol >= 1 && protocol <= IPCProtocolVersion
}
