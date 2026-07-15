package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strings"
	"time"

	"discovery/app/netutil"
	"discovery/internal/tlsutil"
)

// ── Backoff ───────────────────────────────────────────────────────────────────

// onboardingBackoff returns an exponential backoff with up to 20% jitter.
func onboardingBackoff(attempt int) time.Duration {
	exp := math.Pow(2, float64(attempt))
	base := float64(onboardingRetryBase) * exp
	if base > float64(onboardingRetryMax) {
		base = float64(onboardingRetryMax)
	}
	jitterMax := int64(base * 0.2)
	if jitterMax < 1 {
		jitterMax = 1
	}
	jitter, _ := rand.Int(rand.Reader, big.NewInt(jitterMax))
	return time.Duration(int64(base) + jitter.Int64())
}

// ── Provisioning Token ───────────────────────────────────────────────────────

// requestProvisioningToken solicita ao servidor um deploy key temporário para
// uso no auto-provisioning P2P. Retorna o deployKey, o expiresAt (RFC3339) e
// erro.
func (a *App) requestProvisioningToken(ctx context.Context) (deployKey, expiresAt string, err error) {
	cfg := a.GetDebugConfig()
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)

	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/zero-touch/deploy-token"
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if reqErr != nil {
		return "", "", fmt.Errorf("request build: %w", reqErr)
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, respErr := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if respErr != nil {
		return "", "", fmt.Errorf("http: %w", respErr)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("status %d: %s", resp.StatusCode, netutil.SanitizeHTTPErrorBody(resp.StatusCode, string(body)))
	}

	var parsed struct {
		Token     string `json:"token"`
		DeployKey string `json:"deployKey"` // fallback para compatibilidade
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("parse: %w", err)
	}

	// Prefere o campo "token" (formato canônico do ProvisioningTokenResponse).
	// Mantém fallback "deployKey" para retrocompatibilidade.
	result := strings.TrimSpace(parsed.DeployKey)
	if result == "" {
		result = strings.TrimSpace(parsed.Token)
	}
	if result == "" {
		return "", "", fmt.Errorf("parse: campo token vazio na resposta (esperado 'token' ou 'deployKey')")
	}

	return result, strings.TrimSpace(parsed.ExpiresAt), nil
}
