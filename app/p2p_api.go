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
)

const (
	p2pSeedPlanEndpointPath    = "/api/agent-auth/me/p2p-seed-plan"
	p2pTelemetryEndpointPath   = "/api/agent-auth/me/p2p-telemetry"
	p2pDistributionStatusPath  = "/api/agent-auth/me/p2p-distribution-status"
	p2pSeedPlanRefreshInterval = 5 * time.Minute
)

// cachedP2PSeedPlan stores the last server recommendation for local reuse.
type cachedP2PSeedPlan struct {
	Plan         P2PSeedPlanRecommendation
	FetchedAtUTC time.Time
}

// GetP2PSeedPlanRecommendation returns a cached plan when fresh, otherwise
// fetches from API and updates local cache.
func (a *App) GetP2PSeedPlanRecommendation(ctx context.Context) (P2PSeedPlanRecommendation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cached, ok := a.getCachedSeedPlan(); ok {
		return cached, nil
	}
	plan, err := a.fetchP2PSeedPlanRecommendation(ctx)
	if err != nil {
		return P2PSeedPlanRecommendation{}, err
	}
	a.setCachedSeedPlan(plan)
	return plan, nil
}

func (a *App) fetchP2PSeedPlanRecommendation(ctx context.Context) (P2PSeedPlanRecommendation, error) {
	endpoint, token, err := a.p2pAPIAuthEndpoint(p2pSeedPlanEndpointPath)
	if err != nil {
		return P2PSeedPlanRecommendation{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return P2PSeedPlanRecommendation{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return P2PSeedPlanRecommendation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return P2PSeedPlanRecommendation{}, fmt.Errorf("seed-plan API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out P2PSeedPlanRecommendation
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return P2PSeedPlanRecommendation{}, err
	}
	// Server may return partial payload; enforce fallback with local config.
	if out.Plan.SelectedSeeds == 0 && out.Plan.TotalAgents == 0 {
		out.Plan = a.ComputeP2PSeedPlan(max(1, len(a.GetP2PPeers())+1))
	}
	if strings.TrimSpace(out.GeneratedAtUTC) == "" {
		out.GeneratedAtUTC = time.Now().UTC().Format(time.RFC3339)
	}
	return out, nil
}

// PostP2PTelemetry sends runtime P2P metrics to server for observability.
func (a *App) PostP2PTelemetry(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if a.p2pCoord == nil {
		return fmt.Errorf("coordinator P2P indisponivel")
	}
	status := a.GetP2PDebugStatus()
	payload := P2PTelemetryPayload{
		AgentID:         strings.TrimSpace(a.GetDebugConfig().AgentID),
		CollectedAtUTC:  time.Now().UTC().Format(time.RFC3339),
		Metrics:         status.Metrics,
		CurrentSeedPlan: status.CurrentSeedPlan,
	}
	if info, err := a.GetAgentInfo(); err == nil {
		payload.SiteID = strings.TrimSpace(info.SiteID)
	}

	endpoint, token, err := a.p2pAPIAuthEndpoint(p2pTelemetryEndpointPath)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telemetry API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// GetP2PDistributionStatus fetches distribution visibility used by operations dashboards.
func (a *App) GetP2PDistributionStatus(ctx context.Context) ([]P2PDistributionStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint, token, err := a.p2pAPIAuthEndpoint(p2pDistributionStatusPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("distribution-status API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out []P2PDistributionStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// StartP2PTelemetryLoop periodically refreshes seed-plan cache and sends telemetry.
func (a *App) StartP2PTelemetryLoop(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(p2pSeedPlanRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := a.GetP2PSeedPlanRecommendation(ctx); err != nil {
				a.logs.append("[p2p][api] falha ao atualizar seed-plan: " + err.Error())
			}
			if err := a.PostP2PTelemetry(ctx); err != nil {
				a.logs.append("[p2p][api] falha ao enviar telemetria: " + err.Error())
			}
		}
	}
}

func (a *App) p2pAPIAuthEndpoint(path string) (string, string, error) {
	cfg := a.GetDebugConfig()
	scheme := strings.TrimSpace(cfg.ApiScheme)
	server := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	if scheme == "" || server == "" {
		return "", "", fmt.Errorf("apiScheme/apiServer ausentes")
	}
	if token == "" {
		return "", "", fmt.Errorf("authToken ausente")
	}
	endpoint := strings.TrimRight(scheme+"://"+server, "/") + path
	return endpoint, token, nil
}

func (a *App) getCachedSeedPlan() (P2PSeedPlanRecommendation, bool) {
	a.p2pMu.RLock()
	defer a.p2pMu.RUnlock()
	plan := a.p2pSeedPlanCache
	if plan.FetchedAtUTC.IsZero() {
		return P2PSeedPlanRecommendation{}, false
	}
	if time.Since(plan.FetchedAtUTC) > p2pSeedPlanRefreshInterval {
		return P2PSeedPlanRecommendation{}, false
	}
	return plan.Plan, true
}

func (a *App) setCachedSeedPlan(plan P2PSeedPlanRecommendation) {
	a.p2pMu.Lock()
	a.p2pSeedPlanCache = cachedP2PSeedPlan{Plan: plan, FetchedAtUTC: time.Now().UTC()}
	a.p2pMu.Unlock()
}
