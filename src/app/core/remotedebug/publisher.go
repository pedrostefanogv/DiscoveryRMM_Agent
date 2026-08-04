package remotedebug

import (
	"context"
	"encoding/json"
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

func (p *natsPublisher) Name() string { return p.name }

func (p *natsPublisher) Publish(_ context.Context, msg LogMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := p.conn.Publish(p.subject, payload); err != nil {
		return err
	}
	p.conn.Flush()
	return p.conn.LastError()
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
		nats.MaxReconnects(1),
	)
	if err != nil {
		return nil, err
	}
	return &natsPublisher{name: name, subject: subject, conn: nc}, nil
}
