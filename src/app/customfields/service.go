// Package customfields encapsula o envio de campos customizados coletados
// pelo agente para a API v1, separado do App.
package customfields

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
)

// UpsertCollectedCustomFieldRequest payload para envio de campo coletado.
type UpsertCollectedCustomFieldRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Scope string `json:"scope"`
}

// DebugConfig é uma visão mínima da configuração de debug usada pelo envio.
type DebugConfig struct {
	ApiScheme string
	ApiServer string
	AuthToken string
	AgentID   string
}

// Deps são as dependências injetadas no Service.
type Deps struct {
	// GetDebugConfig retorna a configuração de debug.
	GetDebugConfig func() DebugConfig
}

// Service encapsula o envio de campos customizados.
type Service struct {
	getDebugConfig func() DebugConfig
}

// New cria um CustomFieldsService.
func New(deps Deps) *Service {
	return &Service{getDebugConfig: deps.GetDebugConfig}
}

// UpsertCollectedCustomField envia um campo customizado coletado para o servidor.
func (s *Service) UpsertCollectedCustomField(ctx context.Context, name, value, scope string) error {
	cfg := s.getDebugConfig()
	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/custom-fields/collected"

	payload, _ := json.Marshal(UpsertCollectedCustomFieldRequest{
		Name:  strings.TrimSpace(name),
		Value: strings.TrimSpace(value),
		Scope: strings.TrimSpace(scope),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("upsert custom field: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upsert custom field HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
