package remotedebug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"discovery/app/netutil"

	"github.com/nats-io/nats.go"
)

// Publisher publica mensagens de log em um transporte remoto.
type Publisher interface {
	Name() string
	Publish(ctx context.Context, msg LogMessage) error
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

func (p *natsPublisher) Close() error {
	if p.conn != nil {
		p.conn.Close()
	}
	return nil
}

// BuildPublishers cria a lista de publishers (NATS + NATS-WSS) com fallback.
func BuildPublishers(cfg Config, stream StreamConfig, token string, clientID, siteID string) ([]Publisher, error) {
	var publishers []Publisher
	subject := ResolveSubject(strings.TrimSpace(stream.NatsSubject), clientID, siteID, strings.TrimSpace(cfg.AgentID))
	if subject == "" {
		return nil, fmt.Errorf("subject NATS ausente no comando de remote debug")
	}
	if !IsCanonicalSubject(subject) {
		return nil, fmt.Errorf("subject NATS inválido para remote debug: esperado sufixo .remote-debug.log, recebido=%q", subject)
	}

	if p, err := newNATSPublisher(strings.TrimSpace(cfg.NatsServer), token, subject, "nats"); err == nil {
		publishers = append(publishers, p)
	}

	wss := strings.TrimSpace(stream.NatsWssURL)
	if wss == "" {
		wss = strings.TrimSpace(cfg.NatsWsServer)
	}
	if p, err := newNATSPublisher(wss, token, subject, "nats-wss"); err == nil {
		publishers = append(publishers, p)
	}

	if len(publishers) == 0 {
		return nil, fmt.Errorf("nenhum transporte remoto disponivel")
	}
	return publishers, nil
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

	nc, err := nats.Connect(server,
		nats.Name("discovery-remote-debug"),
		nats.Token(normalizedToken),
		nats.Timeout(5*time.Second),
		nats.ReconnectWait(2*time.Second),
		// Reconexão ilimitada, como a sessão principal do agenteconn. Com
		// MaxReconnects(1) um blip de rede fechava a conexão em definitivo: o
		// manager aposentava este transporte (activeIndex avança de forma
		// permanente) e a sessão — que dura até 20 min — ficava muda mesmo com
		// a rede de volta. Durante a reconexão o cliente NATS bufferiza, então
		// os logs saem quando a conexão volta; se o buffer estourar, o Publish
		// devolve erro e o fallback para o próximo transporte acontece.
		nats.MaxReconnects(-1),
	)
	if err != nil {
		return nil, err
	}
	return &natsPublisher{name: name, subject: subject, conn: nc}, nil
}
