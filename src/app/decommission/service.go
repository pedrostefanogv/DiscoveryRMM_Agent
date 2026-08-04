// Package decommission encapsula a lógica de decommission do agente
// (DELETE remoto + limpeza local + outbox de retry), separado do App.
package decommission

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"discovery/app/core/database"
	"discovery/app/core/platform"
	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
)

const OutboxCacheKey = "agent_decommission_outbox"

// Target representa o alvo do DELETE remoto.
type Target struct {
	Scheme  string `json:"scheme"`
	Server  string `json:"server"`
	Token   string `json:"token"`
	AgentID string `json:"agentId"`
}

// OutboxEntry representa uma entrada do outbox de decommission.
type OutboxEntry struct {
	Target        Target `json:"target"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt string `json:"nextAttemptAt"`
	LastError     string `json:"lastError"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

// Deps são as dependências injetadas no Service.
type Deps struct {
	// LoadInstallerConfig carrega a configuração do instalador.
	LoadInstallerConfig func() (InstallerConfig, string, error)
	// GetDataDir retorna o diretório de dados.
	GetDataDir func() string
}

// InstallerConfig é uma visão mínima da config do instalador.
type InstallerConfig struct {
	APIScheme func() string
	ApiServer string
	ServerURL string
	AuthToken string
	AgentID   string
}

// Service encapsula a lógica de decommission.
type Service struct {
	loadInstallerConfig func() (InstallerConfig, string, error)
	getDataDir          func() string
}

// New cria um DecommissionService.
func New(deps Deps) *Service {
	return &Service{
		loadInstallerConfig: deps.LoadInstallerConfig,
		getDataDir:          deps.GetDataDir,
	}
}

// RunCleanup executa o DELETE do agente no backend.
// Em falha transitória, persiste um outbox local para retry no próximo startup.
func (s *Service) RunCleanup(ctx context.Context) error {
	remoteErr := s.runRemoteCleanup(ctx)
	localErr := s.cleanupLocalTempDirs()

	if remoteErr != nil && localErr != nil {
		return fmt.Errorf("falha no decommission remoto e na limpeza local: remoto=%v local=%w", remoteErr, localErr)
	}
	if remoteErr != nil {
		return remoteErr
	}
	if localErr != nil {
		return localErr
	}
	return nil
}

// RunRemoteCleanup executa o DELETE remoto do agente.
func (s *Service) RunRemoteCleanup(ctx context.Context) error {
	return s.runRemoteCleanup(ctx)
}

// CleanupLocalTempDirs limpa os diretórios temporários locais.
func (s *Service) CleanupLocalTempDirs() error {
	return s.cleanupLocalTempDirs()
}

// ResolveTargetFromInstaller resolve o alvo do DELETE a partir da config.
func (s *Service) ResolveTargetFromInstaller() (Target, error) {
	return s.resolveTargetFromInstaller()
}

func (s *Service) runRemoteCleanup(ctx context.Context) error {
	target, err := s.resolveTargetFromInstaller()
	if err != nil {
		return err
	}

	if err := PerformDelete(ctx, target); err == nil {
		return nil
	}

	db, dbErr := database.Open(s.getDataDir())
	if dbErr != nil {
		return fmt.Errorf("falha no delete remoto e não foi possível abrir DB para outbox: %w", dbErr)
	}
	defer db.Close()

	if queueErr := EnqueueOutbox(db, target, err); queueErr != nil {
		return fmt.Errorf("falha no delete remoto e no enqueue de outbox: %v | %w", err, queueErr)
	}
	return nil
}

func (s *Service) cleanupLocalTempDirs() error {
	return CleanupPaths([]string{
		platform.P2PTempDir(),
		platform.TempDir(),
	})
}

// CleanupPaths remove diretórios locais, ignorando duplicatas e ausentes.
func CleanupPaths(paths []string) error {
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}
		key := strings.ToLower(path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("falha ao remover diretório local %s: %w", path, err)
		}
	}
	return nil
}

func (s *Service) resolveTargetFromInstaller() (Target, error) {
	inst, _, err := s.loadInstallerConfig()
	if err != nil {
		return Target{}, err
	}

	scheme := inst.APIScheme()
	server := strings.TrimSpace(inst.ApiServer)
	if server == "" {
		parsedScheme, parsedServer := ParseInstallerServerURLLite(inst.ServerURL)
		if parsedServer != "" {
			server = parsedServer
			if parsedScheme == "http" {
				scheme = "http"
			}
		}
	}

	target := Target{
		Scheme:  scheme,
		Server:  server,
		Token:   strings.TrimSpace(inst.AuthToken),
		AgentID: strings.TrimSpace(inst.AgentID),
	}

	if target.Scheme == "" || target.Server == "" || target.Token == "" || target.AgentID == "" {
		return Target{}, fmt.Errorf("credenciais insuficientes para delete remoto do agente")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return Target{}, fmt.Errorf("apiScheme inválido para delete remoto do agente")
	}

	return target, nil
}

// ParseInstallerServerURLLite extrai scheme e host de uma URL.
func ParseInstallerServerURLLite(raw string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return "", ""
	}
	scheme := strings.TrimSpace(strings.ToLower(parsed.Scheme))
	server := strings.TrimSpace(parsed.Host)
	return scheme, server
}

// PerformDelete executa o DELETE do agente no backend.
func PerformDelete(ctx context.Context, target Target) error {
	endpoint := strings.TrimSpace(target.Scheme) + "://" + strings.TrimSpace(target.Server) + "/api/v1/agent-auth/me"
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, target.Token, target.AgentID); err != nil {
		return err
	}
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

// EnqueueOutbox persiste uma entrada de outbox para retry.
func EnqueueOutbox(db *database.DB, target Target, cause error) error {
	entry := OutboxEntry{}
	now := time.Now().UTC()

	if ok, err := db.CacheGetJSON(OutboxCacheKey, &entry); err != nil {
		return err
	} else if !ok {
		entry = OutboxEntry{
			Target:    target,
			CreatedAt: now.Format(time.RFC3339),
		}
	}

	entry.Target = target
	entry.Attempts++
	entry.LastError = strings.TrimSpace(cause.Error())
	entry.UpdatedAt = now.Format(time.RFC3339)
	entry.NextAttemptAt = now.Add(Backoff(entry.Attempts)).Format(time.RFC3339)

	return db.CacheSetJSON(OutboxCacheKey, entry, 30*24*time.Hour)
}

// DrainOutbox processa o outbox de delete pendente.
func DrainOutbox(db *database.DB, ctx context.Context) (bool, error) {
	entry := OutboxEntry{}
	ok, err := db.CacheGetJSON(OutboxCacheKey, &entry)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	nextAt := ParseRFC3339(entry.NextAttemptAt)
	if !nextAt.IsZero() && time.Now().UTC().Before(nextAt) {
		return false, nil
	}

	if opErr := PerformDelete(ctx, entry.Target); opErr == nil {
		if delErr := db.CacheDelete(OutboxCacheKey); delErr != nil {
			return false, delErr
		}
		return true, nil
	} else {
		err = opErr
	}

	entry.Attempts++
	entry.LastError = strings.TrimSpace(err.Error())
	entry.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	entry.NextAttemptAt = time.Now().UTC().Add(Backoff(entry.Attempts)).Format(time.RFC3339)
	if saveErr := db.CacheSetJSON(OutboxCacheKey, entry, 30*24*time.Hour); saveErr != nil {
		return false, saveErr
	}
	return false, nil
}

// ParseRFC3339 parseia uma string RFC3339.
func ParseRFC3339(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// Backoff calcula o backoff exponencial para retry.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := time.Duration(1<<uint(attempt-1)) * time.Minute
	if base > 6*time.Hour {
		base = 6 * time.Hour
	}
	return base
}
