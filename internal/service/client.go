package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
)

// ServiceClient é o cliente que UI apps usam para conectar ao service
type ServiceClient struct {
	pipeName string
	conn     net.Conn
	timeout  time.Duration
}

// NewServiceClient cria um novo cliente
func NewServiceClient() *ServiceClient {
	return &ServiceClient{
		pipeName: PipeName,
		timeout:  10 * time.Second,
	}
}

// Connect conecta ao service
// Usa net.Dial com protocolo "pipe" no Windows
func (sc *ServiceClient) Connect(ctx context.Context) error {
	var d net.Dialer
	d.Timeout = sc.timeout
	conn, err := d.DialContext(ctx, "pipe", sc.pipeName)

	if err != nil {
		return fmt.Errorf("cannot connect to service at %s: %w", sc.pipeName, err)
	}

	sc.conn = conn
	return nil
}

// Close fecha a conexão
func (sc *ServiceClient) Close() error {
	if sc.conn == nil {
		return nil
	}
	return sc.conn.Close()
}

// IsConnected verifica se está conectado
func (sc *ServiceClient) IsConnected() bool {
	return sc.conn != nil
}

// SendCommand envia um comando genérico ao service
func (sc *ServiceClient) SendCommand(ctx context.Context, command string, payload map[string]interface{}) (*ServiceResponse, error) {
	if !sc.IsConnected() {
		return nil, fmt.Errorf("not connected to service")
	}

	req := ClientRequest{
		ID:       uuid.New().String(),
		Command:  command,
		UserName: "DESKTOP\\unknown",
		Payload:  payload,
		Elevated: false,
	}

	encoder := json.NewEncoder(sc.conn)
	if err := encoder.Encode(&req); err != nil {
		sc.conn.Close()
		sc.conn = nil
		return nil, err
	}

	var resp ServiceResponse
	decoder := json.NewDecoder(sc.conn)
	if err := decoder.Decode(&resp); err != nil {
		sc.conn.Close()
		sc.conn = nil
		return nil, err
	}

	return &resp, nil
}

// GetStatus obtém status do service
func (sc *ServiceClient) GetStatus(ctx context.Context) (*ServiceResponse, error) {
	return sc.SendCommand(ctx, "getStatus", nil)
}

// GetConfig obtém configuração
func (sc *ServiceClient) GetConfig(ctx context.Context) (*ServiceResponse, error) {
	return sc.SendCommand(ctx, "getConfig", nil)
}

// Execute executa uma ação
func (sc *ServiceClient) Execute(ctx context.Context, action string, params map[string]interface{}) (*ServiceResponse, error) {
	payload := map[string]interface{}{
		"action": action,
	}
	for k, v := range params {
		payload[k] = v
	}
	return sc.SendCommand(ctx, "execute", payload)
}

// GetPolicies obtém políticas ativas
func (sc *ServiceClient) GetPolicies(ctx context.Context) (*ServiceResponse, error) {
	return sc.SendCommand(ctx, "getPolicies", nil)
}
