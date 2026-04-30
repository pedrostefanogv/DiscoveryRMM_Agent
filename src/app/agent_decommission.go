package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"discovery/app/netutil"
	"discovery/internal/database"
	"discovery/internal/tlsutil"
)

const agentDecommissionOutboxCacheKey = "agent_decommission_outbox"

type agentDecommissionTarget struct {
	Scheme  string `json:"scheme"`
	Server  string `json:"server"`
	Token   string `json:"token"`
	AgentID string `json:"agentId"`
}

type agentDecommissionOutboxEntry struct {
	Target        agentDecommissionTarget `json:"target"`
	Attempts      int                     `json:"attempts"`
	NextAttemptAt string                  `json:"nextAttemptAt"`
	LastError     string                  `json:"lastError"`
	CreatedAt     string                  `json:"createdAt"`
	UpdatedAt     string                  `json:"updatedAt"`
}

// RunAgentDecommissionCleanup executa o DELETE do agente no backend.
// Em falha transitória, persiste um outbox local para retry no próximo startup.
func RunAgentDecommissionCleanup(ctx context.Context) error {
	target, err := resolveAgentDecommissionTargetFromInstaller()
	if err != nil {
		return err
	}

	if err := performAgentDecommissionDelete(ctx, target); err == nil {
		return nil
	}

	db, dbErr := database.Open(GetDataDir())
	if dbErr != nil {
		return fmt.Errorf("falha no delete remoto e nao foi possivel abrir DB para outbox: %w", dbErr)
	}
	defer db.Close()

	if queueErr := enqueueAgentDecommissionOutbox(db, target, err); queueErr != nil {
		return fmt.Errorf("falha no delete remoto e no enqueue de outbox: %v | %w", err, queueErr)
	}
	return nil
}

func (a *App) drainAgentDecommissionOutbox(ctx context.Context, reason string) {
	if a == nil || a.db == nil {
		return
	}
	sent, err := drainAgentDecommissionOutbox(a.db, ctx)
	if err != nil {
		a.logs.append("[agent][decommission] erro ao drenar outbox (" + strings.TrimSpace(reason) + "): " + err.Error())
		return
	}
	if sent {
		a.logs.append("[agent][decommission] outbox de delete processado com sucesso")
	}
}

func resolveAgentDecommissionTargetFromInstaller() (agentDecommissionTarget, error) {
	inst, _, err := loadInstallerConfig()
	if err != nil {
		return agentDecommissionTarget{}, err
	}

	scheme := strings.TrimSpace(strings.ToLower(inst.ApiScheme))
	server := strings.TrimSpace(inst.ApiServer)
	if scheme == "" || server == "" {
		if parsedScheme, parsedServer := parseInstallerServerURLLite(inst.ServerURL); parsedScheme != "" && parsedServer != "" {
			scheme = parsedScheme
			server = parsedServer
		}
	}

	target := agentDecommissionTarget{
		Scheme:  scheme,
		Server:  server,
		Token:   strings.TrimSpace(inst.AuthToken),
		AgentID: strings.TrimSpace(inst.AgentID),
	}

	if target.Scheme == "" || target.Server == "" || target.Token == "" || target.AgentID == "" {
		return agentDecommissionTarget{}, fmt.Errorf("credenciais insuficientes para delete remoto do agente")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return agentDecommissionTarget{}, fmt.Errorf("apiScheme invalido para delete remoto do agente")
	}

	return target, nil
}

func parseInstallerServerURLLite(raw string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return "", ""
	}
	scheme := strings.TrimSpace(strings.ToLower(parsed.Scheme))
	server := strings.TrimSpace(parsed.Host)
	return scheme, server
}

func performAgentDecommissionDelete(ctx context.Context, target agentDecommissionTarget) error {
	endpoint := strings.TrimSpace(target.Scheme) + "://" + strings.TrimSpace(target.Server) + "/api/v1/agent-auth/me"
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	netutil.SetAgentAuthHeaders(req, target.Token)
	req.Header.Set("X-Agent-ID", target.AgentID)
	req.Header.Set("Accept", "application/json")

	resp, err := tlsutil.NewHTTPClient(20 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil
	}
	return fmt.Errorf("delete agent retornou HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func enqueueAgentDecommissionOutbox(db *database.DB, target agentDecommissionTarget, cause error) error {
	entry := agentDecommissionOutboxEntry{}
	now := time.Now().UTC()

	if ok, err := db.CacheGetJSON(agentDecommissionOutboxCacheKey, &entry); err != nil {
		return err
	} else if !ok {
		entry = agentDecommissionOutboxEntry{
			Target:    target,
			CreatedAt: now.Format(time.RFC3339),
		}
	}

	entry.Target = target
	entry.Attempts++
	entry.LastError = strings.TrimSpace(cause.Error())
	entry.UpdatedAt = now.Format(time.RFC3339)
	entry.NextAttemptAt = now.Add(agentDecommissionBackoff(entry.Attempts)).Format(time.RFC3339)

	return db.CacheSetJSON(agentDecommissionOutboxCacheKey, entry, 30*24*time.Hour)
}

func drainAgentDecommissionOutbox(db *database.DB, ctx context.Context) (bool, error) {
	entry := agentDecommissionOutboxEntry{}
	ok, err := db.CacheGetJSON(agentDecommissionOutboxCacheKey, &entry)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	nextAt := parseRFC3339(entry.NextAttemptAt)
	if !nextAt.IsZero() && time.Now().UTC().Before(nextAt) {
		return false, nil
	}

	if opErr := performAgentDecommissionDelete(ctx, entry.Target); opErr == nil {
		if delErr := db.CacheDelete(agentDecommissionOutboxCacheKey); delErr != nil {
			return false, delErr
		}
		return true, nil
	} else {
		err = opErr
	}

	entry.Attempts++
	entry.LastError = strings.TrimSpace(err.Error())
	entry.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	entry.NextAttemptAt = time.Now().UTC().Add(agentDecommissionBackoff(entry.Attempts)).Format(time.RFC3339)
	if saveErr := db.CacheSetJSON(agentDecommissionOutboxCacheKey, entry, 30*24*time.Hour); saveErr != nil {
		return false, saveErr
	}
	return false, nil
}

func parseRFC3339(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func agentDecommissionBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := time.Duration(1<<uint(attempt-1)) * time.Minute
	if base > 6*time.Hour {
		base = 6 * time.Hour
	}
	return base
}
