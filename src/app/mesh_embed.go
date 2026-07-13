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
	"discovery/internal/tlsutil"
)

// ── MeshCentral Embed URL Bridge (API v1) ───────────────────────────────────
//
// Implementa POST /api/v1/agent-auth/me/support/meshcentral/embed-url
// para obter URL de acesso remoto via MeshCentral.

// MeshCentralEmbedRequest payload para solicitar embed URL.
type MeshCentralEmbedRequest struct {
	DurationMinutes int `json:"durationMinutes"`
}

// MeshCentralEmbedResponse resposta com a URL de acesso.
type MeshCentralEmbedResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
	SessionID string `json:"sessionId"`
}

// CreateMeshCentralEmbedURL solicita uma URL de acesso remoto via MeshCentral.
func (a *App) CreateMeshCentralEmbedURL(ctx context.Context, durationMinutes int) (*MeshCentralEmbedResponse, error) {
	cfg := a.GetDebugConfig()
	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/support/meshcentral/embed-url"

	payload, _ := json.Marshal(MeshCentralEmbedRequest{DurationMinutes: durationMinutes})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("meshcentral embed url: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("meshcentral embed url HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result MeshCentralEmbedResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("meshcentral embed url parse: %w", err)
	}
	return &result, nil
}
