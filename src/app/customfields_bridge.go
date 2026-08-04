package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"discovery/app/netutil"
	"discovery/app/core/tlsutil"
)

// ── Custom Fields Bridge (API v1) ───────────────────────────────────────────
//
// Implementa POST /api/v1/agent-auth/me/custom-fields/collected
// para envio de campos customizados coletados pelo agente.

// UpsertCollectedCustomFieldRequest payload para envio de campo coletado.
type UpsertCollectedCustomFieldRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Scope string `json:"scope"`
}

// UpsertCollectedCustomField envia um campo customizado coletado para o servidor.
func (a *App) UpsertCollectedCustomField(ctx context.Context, name, value, scope string) error {
	cfg := a.GetDebugConfig()
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
