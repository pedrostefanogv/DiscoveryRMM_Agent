package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	p2pmeta "discovery/app/p2pmeta"
)

const (
	p2pSeedPlanEndpointPath    = "/api/v1/agent-auth/me/p2p-seed-plan"
	p2pTelemetryEndpointPath   = "/api/v1/agent-auth/me/p2p-telemetry"
	p2pDistributionStatusPath  = "/api/v1/agent-auth/me/p2p-distribution-status"
	p2pSeedPlanRefreshInterval = 5 * time.Minute
)

// cachedP2PSeedPlan stores the last server recommendation for local reuse.
type cachedP2PSeedPlan = p2pmeta.CachedSeedPlan

// P2PDistributionStatusQueryOptions defines optional filters used by
// GET /api/v1/agent-auth/me/p2p-distribution-status.
type P2PDistributionStatusQueryOptions struct {
	ArtifactID string
	Limit      int
	Offset     int
}

type p2pAPIErrorEnvelope struct {
	Error             string `json:"error"`
	Field             string `json:"field,omitempty"`
	Code              string `json:"code,omitempty"`
	RetryAfterSeconds int    `json:"retryAfterSeconds,omitempty"`
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
	endpoint, token, agentID, err := a.p2pAPIAuthEndpoint(p2pSeedPlanEndpointPath)
	if err != nil {
		return P2PSeedPlanRecommendation{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return P2PSeedPlanRecommendation{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Agent-ID", agentID)

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
	payload, err := a.buildP2PTelemetryPayload()
	if err != nil {
		return err
	}
	if err := a.postP2PTelemetryPayload(ctx, payload, ""); err != nil {
		if qErr := a.enqueueP2PTelemetryOutbox(payload, err); qErr != nil {
			a.logs.append("[p2p][api] falha ao enfileirar telemetria offline: " + qErr.Error())
		}
		return err
	}
	return nil
}

// postP2PTelemetryPayload envia o payload ao servidor.
// idempotencyKey é enviado no header Idempotency-Key para deduplicação server-side;
// passe string vazia quando não aplicável (primeira tentativa sem outbox).
func (a *App) postP2PTelemetryPayload(ctx context.Context, payload P2PTelemetryPayload, idempotencyKey string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if until, blocked := a.getP2PTelemetryRateLimitUntil(); blocked {
		return fmt.Errorf("telemetry API temporariamente bloqueada por rate limit ate %s", until.UTC().Format(time.RFC3339))
	}
	endpoint, token, agentID, err := a.p2pAPIAuthEndpoint(p2pTelemetryEndpointPath)
	if err != nil {
		return err
	}
	body, err := marshalP2PTelemetryPayload(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("Content-Type", "application/json")
	if k := strings.TrimSpace(idempotencyKey); k != "" {
		req.Header.Set("Idempotency-Key", k)
	}

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		if until, ok := p2pRetryAfterFromResponse(resp, time.Now()); ok {
			a.setP2PTelemetryRateLimitUntil(until)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p2pAPIResponseError("telemetry API", resp)
	}
	return nil
}

// GetP2PDistributionStatus fetches distribution visibility used by operations dashboards.
func (a *App) GetP2PDistributionStatus(ctx context.Context) ([]P2PDistributionStatus, error) {
	return a.GetP2PDistributionStatusWithOptions(ctx, P2PDistributionStatusQueryOptions{})
}

// GetP2PDistributionStatusWithOptions fetches distribution visibility using
// optional filters and pagination.
func (a *App) GetP2PDistributionStatusWithOptions(ctx context.Context, opts P2PDistributionStatusQueryOptions) ([]P2PDistributionStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Limit < 0 || opts.Limit > 500 {
		return nil, fmt.Errorf("limit invalido: esperado 0 ou entre 1 e 500")
	}
	if opts.Offset < 0 {
		return nil, fmt.Errorf("offset invalido: esperado valor >= 0")
	}
	endpoint, token, agentID, err := a.p2pAPIAuthEndpoint(p2pDistributionStatusPath)
	if err != nil {
		return nil, err
	}
	endpoint, err = p2pDistributionStatusEndpointWithOptions(endpoint, opts)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Agent-ID", agentID)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p2pAPIResponseError("distribution-status API", resp)
	}

	var out []P2PDistributionStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func p2pDistributionStatusEndpointWithOptions(endpoint string, opts P2PDistributionStatusQueryOptions) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if artifactID := strings.TrimSpace(opts.ArtifactID); artifactID != "" {
		q.Set("artifactId", artifactID)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Offset > 0 {
		q.Set("offset", strconv.Itoa(opts.Offset))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func p2pAPIResponseError(apiName string, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	trimmedBody := strings.TrimSpace(string(body))
	if trimmedBody == "" {
		return fmt.Errorf("%s HTTP %d", apiName, resp.StatusCode)
	}
	var parsed p2pAPIErrorEnvelope
	if err := json.Unmarshal(body, &parsed); err == nil && strings.TrimSpace(parsed.Error) != "" {
		details := strings.TrimSpace(parsed.Error)
		if code := strings.TrimSpace(parsed.Code); code != "" {
			details += " (code=" + code + ")"
		}
		if field := strings.TrimSpace(parsed.Field); field != "" {
			details += " (field=" + field + ")"
		}
		if parsed.RetryAfterSeconds > 0 {
			details += " (retryAfterSeconds=" + strconv.Itoa(parsed.RetryAfterSeconds) + ")"
		}
		return fmt.Errorf("%s HTTP %d: %s", apiName, resp.StatusCode, details)
	}
	return fmt.Errorf("%s HTTP %d: %s", apiName, resp.StatusCode, trimmedBody)
}

func p2pRetryAfterFromResponse(resp *http.Response, now time.Time) (time.Time, bool) {
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			return now.Add(time.Duration(seconds) * time.Second), true
		}
		if ts, err := http.ParseTime(raw); err == nil {
			if ts.After(now) {
				return ts, true
			}
		}
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > 0 {
		var parsed p2pAPIErrorEnvelope
		if err := json.Unmarshal(body, &parsed); err == nil && parsed.RetryAfterSeconds > 0 {
			return now.Add(time.Duration(parsed.RetryAfterSeconds) * time.Second), true
		}
	}
	return time.Time{}, false
}

func (a *App) getP2PTelemetryRateLimitUntil() (time.Time, bool) {
	a.p2pMu.RLock()
	until := a.p2pTelemetryRateLimitUntil
	a.p2pMu.RUnlock()
	if until.IsZero() {
		return time.Time{}, false
	}
	if time.Now().After(until) {
		return time.Time{}, false
	}
	return until, true
}

func (a *App) setP2PTelemetryRateLimitUntil(until time.Time) {
	a.p2pMu.Lock()
	if until.After(a.p2pTelemetryRateLimitUntil) {
		a.p2pTelemetryRateLimitUntil = until
	}
	a.p2pMu.Unlock()
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
			// ConsolidationEngine gate: skip telemetry send if window hasn't elapsed.
			shouldFlush := true
			if a.consolEngine != nil {
				ok, err := a.consolEngine.ShouldFlush("p2p_telemetry", time.Now())
				if err != nil {
					a.logs.append("[p2p][api] consolidation engine erro: " + err.Error())
				}
				shouldFlush = ok
			}
			if shouldFlush {
				if err := a.PostP2PTelemetry(ctx); err != nil {
					a.logs.append("[p2p][api] falha ao enviar telemetria: " + err.Error())
				} else if a.consolEngine != nil {
					_ = a.consolEngine.RecordFlush("p2p_telemetry", time.Now())
				}
			}
			if err := a.drainP2PTelemetryOutbox(ctx, p2pTelemetryDrainLimit); err != nil {
				a.logs.append("[p2p][api] falha ao drenar backlog de telemetria: " + err.Error())
			}
		}
	}
}

func (a *App) p2pAPIAuthEndpoint(path string) (string, string, string, error) {
	cfg := a.GetDebugConfig()
	scheme := strings.TrimSpace(cfg.ApiScheme)
	server := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)
	if scheme == "" || server == "" {
		return "", "", "", fmt.Errorf("apiScheme/apiServer ausentes")
	}
	if token == "" {
		return "", "", "", fmt.Errorf("authToken ausente")
	}
	if agentID == "" {
		return "", "", "", fmt.Errorf("agentId ausente")
	}
	endpoint := strings.TrimRight(scheme+"://"+server, "/") + path
	return endpoint, token, agentID, nil
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
