package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ServiceClient é o cliente que UI apps usam para conectar ao service
type ServiceClient struct {
	pipeName string
	conn     serviceConn
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
// Usa Named Pipe no Windows e retorna erro explícito fora dele.
func (sc *ServiceClient) Connect(ctx context.Context) error {
	conn, err := connectServicePipe(ctx, sc.pipeName, sc.timeout)
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
	err := sc.conn.Close()
	sc.conn = nil
	return err
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

// GetServiceHealth obtém saúde detalhada dos componentes monitorados pelo service.
func (sc *ServiceClient) GetServiceHealth(ctx context.Context) (*ServiceResponse, error) {
	return sc.SendCommand(ctx, "getServiceHealth", nil)
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

// Ping testa conectividade com o service usando getStatus com timeout curto.
// Retorna true se o service responder com sucesso, false caso contrário.
// A conexão é fechada após a verificação; não altera estado persistente do client.
func (sc *ServiceClient) Ping(ctx context.Context) bool {
	probe := NewServiceClient()
	if err := probe.Connect(ctx); err != nil {
		return false
	}
	defer probe.Close()
	resp, err := probe.GetStatus(ctx)
	return err == nil && resp != nil && resp.Code == 200
}

// GetActionStatus consulta o status atual de uma ação previamente enfileirada.
func (sc *ServiceClient) GetActionStatus(ctx context.Context, actionID string) (*ServiceResponse, error) {
	return sc.SendCommand(ctx, "getActionStatus", map[string]interface{}{
		"action_id": actionID,
	})
}

// GetActionHistory consulta o histórico persistido de uma ação.
func (sc *ServiceClient) GetActionHistory(ctx context.Context, actionID string, limit int) (*ServiceResponse, error) {
	payload := map[string]interface{}{
		"action_id": actionID,
	}
	if limit > 0 {
		payload["limit"] = limit
	}
	return sc.SendCommand(ctx, "getActionHistory", payload)
}

func (sc *ServiceClient) TriggerUpdateCheck(ctx context.Context, source string) (*ServiceResponse, error) {
	payload := map[string]interface{}{}
	if source = strings.TrimSpace(source); source != "" {
		payload["source"] = source
	}
	return sc.SendCommand(ctx, "triggerUpdateCheck", payload)
}
