package remotedebug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"discovery/app/core/agentconn"
	"discovery/app/netutil"

	"github.com/nats-io/nats.go"
)

// Publisher publica mensagens (log e controle) em um transporte remoto e
// assina o canal de controle da sessao.
type Publisher interface {
	Name() string
	Publish(ctx context.Context, msg LogMessage) error
	// PublishRaw publica bytes crus no subject informado (canal de controle).
	PublishRaw(ctx context.Context, subject string, payload []byte) error
	// Subscribe assina o subject e devolve o disposer. O erro NAO e fatal
	// para o log: quem chama decide (o manager exige sucesso no start).
	Subscribe(subject string, handler func([]byte)) (func(), error)
	Close() error
}

// natsPublisher publica logs via NATS (TCP ou WebSocket).
type natsPublisher struct {
	name    string
	subject string
	conn    *nats.Conn
}

// flushTimeout limita a espera pela confirmação do servidor.
//
// Flush() sem timeout bloqueia indefinidamente numa conexão viva mas que não
// responde (o servidor aceita o TCP e para de drenar). Como o publishLoop é um
// único goroutine por sessão, travar ali significa sessão parada e fila
// enchendo até descartar logs — sem nenhum sinal para o operador.
const flushTimeout = 2 * time.Second

func (p *natsPublisher) Name() string { return p.name }

func (p *natsPublisher) Publish(_ context.Context, msg LogMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	// LastError() devolve nc.err, que é "sticky": permanece até a conexão
	// reconectar. Sem comparar antes/depois, um erro antigo (ou de outro
	// subject) faria TODO publish seguinte parecer falho e o
	// publishWithFallback avançaria o activeIndex de forma permanente,
	// derrubando o transporte para sempre por causa de um erro transitório.
	before := p.conn.LastError()

	if err := p.conn.Publish(p.subject, payload); err != nil {
		return err
	}
	if err := p.conn.FlushTimeout(flushTimeout); err != nil {
		if p.conn.IsClosed() {
			return err
		}
		// Conexão viva/reconectando: a mensagem já está no buffer do cliente
		// NATS e é enviada quando a conexão voltar — mesma semântica do Flush()
		// anterior, mas sem o risco de bloquear o publishLoop para sempre.
		// NÃO retornar erro aqui é essencial: o manager fecharia este publisher
		// e avançaria o activeIndex de forma permanente, deixando a sessão muda
		// por causa de um blip de rede.
		return nil
	}

	after := p.conn.LastError()
	if after != nil && !errors.Is(after, before) {
		return fmt.Errorf("servidor recusou o publish em %q: %w", p.subject, after)
	}
	return nil
}

// PublishRaw publica bytes crus no subject informado (canal de controle).
// Mesma semantica de erro do Publish de log: LastError e sticky e por isso e
// comparado antes/depois; timeout com conexao viva conta como sucesso porque
// a mensagem ja esta no buffer do cliente NATS.
func (p *natsPublisher) PublishRaw(_ context.Context, subject string, payload []byte) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return fmt.Errorf("subject de controle ausente")
	}
	before := p.conn.LastError()
	if err := p.conn.Publish(subject, payload); err != nil {
		return err
	}
	if err := p.conn.FlushTimeout(flushTimeout); err != nil {
		if p.conn.IsClosed() {
			return err
		}
		return nil
	}
	after := p.conn.LastError()
	if after != nil && !errors.Is(after, before) {
		return fmt.Errorf("servidor recusou o publish em %q: %w", subject, after)
	}
	return nil
}

// Subscribe assina o canal de controle e devolve o disposer.
func (p *natsPublisher) Subscribe(subject string, handler func([]byte)) (func(), error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return nil, fmt.Errorf("subject de controle ausente")
	}
	if handler == nil {
		return nil, fmt.Errorf("handler de controle ausente")
	}
	// O NATS entrega erros de permissao de SUBSCRIBE de forma ASSINCRONA:
	// Subscribe() retorna nil mesmo quando o servidor vai responder
	// -ERR Permissions Violation. Sem o flush + LastError abaixo, o fail-fast
	// do start nao detectaria a ACL negada e a sessao abriria para morrer em
	// InitialGrace (viewer nunca recebe pong). Comparar antes/depois evita
	// reprovar a assinatura por um erro antigo e alheio (LastError e sticky).
	before := p.conn.LastError()
	sub, err := p.conn.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Data)
	})
	if err != nil {
		return nil, err
	}
	if flushErr := p.conn.FlushTimeout(flushTimeout); flushErr == nil {
		after := p.conn.LastError()
		if after != nil && !errors.Is(after, before) {
			_ = sub.Unsubscribe()
			return nil, fmt.Errorf("servidor recusou a assinatura em %q: %w", subject, after)
		}
	}
	// Timeout de flush com conexao viva: a assinatura permanece valida no
	// cliente e sera registrada quando a conexao voltar.
	return func() {
		if sub != nil {
			_ = sub.Unsubscribe()
		}
	}, nil
}

func (p *natsPublisher) Close() error {
	if p.conn != nil {
		p.conn.Close()
	}
	return nil
}

// Timeouts de dial por família de transporte. Espelham a política já validada
// do worker de remote session (connectWorkerNATS): o nativo tem timeout curto
// porque, quando a porta 4222 está dropada/inalcançável, o dial TRAVA até
// estourar — e cada segundo aqui sai direto da grace do viewer.
const (
	remoteDebugNativeDialTimeout = 3 * time.Second
	remoteDebugWSDialTimeout     = 8 * time.Second // TLS + handshake do proxy
)

// remoteDebugCandidate é um endpoint candidato já normalizado.
type remoteDebugCandidate struct {
	raw       string
	url       string
	proxyPath string
}

// remoteDebugEndpointCandidates monta a lista ORDENADA de transportes do remote
// debug.
//
// FIX 2026-10-14 (debug remoto nunca conecta quando a 4222 é inalcançável):
// os candidatos vinham APENAS de cfg.NatsServer, cfg.NatsWsServer e do
// stream.natsWssUrl. Em deployments em que o NATS só é exposto por WebSocket na
// 443 (proxy reverso/Tunnel — ex. discovery.pstefano.com.br), o servidor manda
// NatsWsServer vazio e NÃO inclui natsWsUrl no payload do comando remotedebug
// (esse campo só vai para o viewer, na resposta HTTP). O único candidato que
// sobrava era nats://host:4222 → timeout → "nenhum transporte remoto
// disponivel": a sessão morria antes de assinar .remote-debug.control e o
// console exibia "O agente não respondeu ao canal de controle".
//
// Agora os endpoints também derivam do ApiServer — mesma política do worker de
// remote session e do agentconn (wss://<host>/nats/ e nats://<host>:4222) — e
// todos passam por agentconn.NormalizeClientEndpoint, que fixa porta explícita
// (o nats.go não aplica porta padrão em ws/wss → "dial tcp host:0") e devolve o
// ProxyPath (o dialer ignora o path da URL → o handshake cairia no "/" do site).
//
// Ordem: WSS primeiro. O primeiro transporte que conecta vira o primário da
// sessão, então não se paga o timeout do nativo quando já existe WSS funcional.
func remoteDebugEndpointCandidates(cfg Config, stream StreamConfig) []remoteDebugCandidate {
	apiHost := endpointHost(cfg.ApiServer)

	raw := make([]string, 0, 5)
	if wss := strings.TrimSpace(stream.NatsWssURL); wss != "" {
		raw = append(raw, wss)
	}
	if wss := strings.TrimSpace(cfg.NatsWsServer); wss != "" {
		raw = append(raw, wss)
	}
	if apiHost != "" {
		raw = append(raw, "wss://"+apiHost+"/nats/")
	}
	if srv := strings.TrimSpace(cfg.NatsServer); srv != "" {
		raw = append(raw, srv)
	}
	if apiHost != "" {
		raw = append(raw, "nats://"+apiHost+":4222")
	}

	candidates := make([]remoteDebugCandidate, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		ep, err := agentconn.NormalizeClientEndpoint(r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[remote-debug] endpoint NATS ignorado (%q): %v\n", r, err)
			continue
		}
		if _, dup := seen[ep.URL]; dup {
			continue
		}
		seen[ep.URL] = struct{}{}
		candidates = append(candidates, remoteDebugCandidate{raw: r, url: ep.URL, proxyPath: ep.ProxyPath})
	}
	return candidates
}

// endpointHost extrai apenas o host de um valor que pode chegar como "host",
// "host:porta" ou "https://host/path" — o campo do admin aceita as três formas.
func endpointHost(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	if idx := strings.Index(v, "://"); idx >= 0 {
		v = v[idx+3:]
	}
	if idx := strings.IndexAny(v, "/?#"); idx >= 0 {
		v = v[:idx]
	}
	if host, _, err := net.SplitHostPort(v); err == nil {
		v = host
	}
	return strings.Trim(strings.TrimSpace(v), "[]")
}

func remoteDebugTransportLabel(url string) string {
	if strings.HasPrefix(url, "ws://") || strings.HasPrefix(url, "wss://") {
		return "nats-wss"
	}
	return "nats"
}

func remoteDebugDialTimeout(url string) time.Duration {
	if strings.HasPrefix(url, "nats://") {
		return remoteDebugNativeDialTimeout
	}
	return remoteDebugWSDialTimeout
}

// BuildPublishers cria o publisher de uma sessão de remote debug.
//
// Usa o primeiro transporte que conectar como primário: a sessão precisa subir
// dentro da grace do viewer (era exatamente aí que falhava) e o natsPublisher
// já restabelece a conexão sozinho (MaxReconnects=-1), então gastar 3-8s com
// transportes secundários só atrasaria o start.
func BuildPublishers(cfg Config, stream StreamConfig, token string, clientID, siteID string) ([]Publisher, error) {
	subject := ResolveSubject(strings.TrimSpace(stream.NatsSubject), clientID, siteID, strings.TrimSpace(cfg.AgentID))
	if subject == "" {
		return nil, fmt.Errorf("subject NATS ausente no comando de remote debug")
	}
	if !IsCanonicalSubject(subject) {
		return nil, fmt.Errorf("subject NATS inválido para remote debug: esperado sufixo .remote-debug.log, recebido=%q", subject)
	}

	candidates := remoteDebugEndpointCandidates(cfg, stream)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("nenhum transporte remoto disponivel")
	}

	var lastErr error
	for _, c := range candidates {
		fmt.Fprintf(os.Stderr, "[remote-debug] tentando NATS %s (timeout %s)\n", c.url, remoteDebugDialTimeout(c.url))
		p, err := newNATSPublisher(c.raw, token, subject, remoteDebugTransportLabel(c.url))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[remote-debug] NATS falhou (%s): %v\n", c.url, err)
			lastErr = err
			continue
		}
		fmt.Fprintf(os.Stderr, "[remote-debug] nats conectado: %s\n", c.url)
		return []Publisher{p}, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("nenhum transporte remoto disponivel: %w", lastErr)
	}
	return nil, fmt.Errorf("nenhum transporte remoto disponivel")
}

func newNATSPublisher(server, token, subject, name string) (Publisher, error) {
	server = strings.TrimSpace(server)
	token = strings.TrimSpace(token)
	subject = strings.TrimSpace(subject)
	if server == "" || token == "" || subject == "" {
		return nil, fmt.Errorf("config NATS incompleta")
	}
	normalizedToken, err := netutil.NormalizeAgentToken(token)
	if err != nil {
		return nil, err
	}

	// Porta explícita + ProxyPath: sem isso o candidato wss falha de forma
	// instantânea e silenciosa (dial "host:0" / "invalid websocket connection")
	// — mesma correção já aplicada ao worker de remote session.
	ep, err := agentconn.NormalizeClientEndpoint(server)
	if err != nil {
		return nil, err
	}

	opts := []nats.Option{
		nats.Name("discovery-remote-debug"),
		nats.Token(normalizedToken),
		nats.Timeout(remoteDebugDialTimeout(ep.URL)),
		nats.ReconnectWait(2 * time.Second),
		// Reconexão ilimitada, como a sessão principal do agenteconn. Com
		// MaxReconnects(1) um blip de rede fechava a conexão em definitivo: o
		// manager aposentava este transporte (activeIndex avança de forma
		// permanente) e a sessão — que dura até 20 min — ficava muda mesmo com
		// a rede de volta. Durante a reconexão o cliente NATS bufferiza, então
		// os logs saem quando a conexão volta; se o buffer estourar, o Publish
		// devolve erro e o fallback para o próximo transporte acontece.
		nats.MaxReconnects(-1),
	}
	if ep.ProxyPath != "" {
		opts = append(opts, nats.ProxyPath(ep.ProxyPath))
	}

	nc, err := nats.Connect(ep.URL, opts...)
	if err != nil {
		return nil, err
	}
	return &natsPublisher{name: name, subject: subject, conn: nc}, nil
}
