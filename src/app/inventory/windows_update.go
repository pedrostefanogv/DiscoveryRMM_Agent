package inventory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"discovery/app/core/windowsupdate"
	"discovery/app/debug"
	"discovery/app/netutil"
)

const (
	windowsUpdateUploadTimeout = 120 * time.Second
	windowsUpdateEndpointPath  = "/api/v1/agent-auth/me/windows-updates"
	pendingRebootEndpointPath  = "/api/v1/agent-auth/me/pending-reboot"
)

// WindowsUpdateCache persiste o ultimo scan/sonda no SQLite local.
type WindowsUpdateCache interface {
	GetWindowsUpdateCache(agentID string) (payload []byte, hash string, scannedAt time.Time, found bool, err error)
	SaveWindowsUpdateCache(agentID, hash string, payload []byte, scannedAt time.Time) error
	GetWindowsUpdateRebootState(agentID string) (reasons []string, checkedAt time.Time, uploadedAt time.Time, found bool, err error)
	SaveWindowsUpdateRebootState(agentID string, reasons []string, checkedAt time.Time, uploaded bool) error
}

// WindowsUpdateDeps agrupa as dependencias do gerenciador de patches.
type WindowsUpdateDeps struct {
	Client windowsupdate.Client
	Cache  WindowsUpdateCache
	// Enabled e o kill switch (server config). nil = habilitado.
	Enabled func() bool
}

// SetWindowsUpdateDeps injeta as dependencias do modulo de patches. Chamado
// pelo App quando o SQLite esta disponivel.
func (s *Service) SetWindowsUpdateDeps(deps WindowsUpdateDeps) {
	if s == nil {
		return
	}
	s.wuMu.Lock()
	defer s.wuMu.Unlock()
	if deps.Client != nil {
		s.wuClient = deps.Client
	}
	if deps.Cache != nil {
		s.wuCache = deps.Cache
	}
	if deps.Enabled != nil {
		s.wuEnabled = deps.Enabled
	}
}

// WindowsUpdateSupported informa se o modulo esta habilitado e tem cliente.
func (s *Service) WindowsUpdateSupported() bool {
	if s == nil {
		return false
	}
	s.wuMu.Lock()
	defer s.wuMu.Unlock()
	return s.wuClientEnabledLocked()
}

func (s *Service) wuClientEnabledLocked() bool {
	if s.wuClient == nil {
		return false
	}
	if s.wuEnabled != nil && !s.wuEnabled() {
		return false
	}
	return true
}

func (s *Service) wuDeps() (windowsupdate.Client, WindowsUpdateCache, bool) {
	s.wuMu.Lock()
	defer s.wuMu.Unlock()
	return s.wuClient, s.wuCache, s.wuClientEnabledLocked()
}

// WindowsUpdateProbe reporta suporte/servico/politica sem scan.
func (s *Service) WindowsUpdateProbe(ctx context.Context) windowsupdate.ProbeInfo {
	client, _, enabled := s.wuDeps()
	if client == nil {
		return windowsupdate.ProbeInfo{Supported: false, ErrorCode: windowsupdate.ScanErrorUnsupported}
	}
	if !enabled {
		return windowsupdate.ProbeInfo{Supported: false, ErrorCode: windowsupdate.ScanErrorUnsupported}
	}
	return client.Probe(ctx)
}

// ScanWindowsUpdates executa o scan WUA (pendentes + instaladas + sonda de
// reboot). Serializado por mutex: comandos e o loop periodico nao podem rodar
// dois scans ao mesmo tempo.
func (s *Service) ScanWindowsUpdates(ctx context.Context) (*windowsupdate.ScanResult, error) {
	client, _, enabled := s.wuDeps()
	if client == nil {
		return nil, windowsupdate.ErrUnsupportedOS
	}
	if !enabled {
		return nil, windowsupdate.ErrUnsupportedOS
	}

	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	s.logf("[windows-update] scan iniciado")
	result, err := client.Scan(ctx, windowsupdate.ScanOptions{IncludeInstalled: true})
	if err != nil {
		s.logf("[windows-update] scan falhou: " + err.Error())
		return nil, err
	}
	s.logf(fmt.Sprintf("[windows-update] scan concluido: pending=%d installed=%d skipped=%d reboot=%t (%dms)",
		result.PendingCount, result.InstalledCount, result.SkippedCount, result.PendingReboot.Pending, result.DurationMs))
	return result, nil
}

// SyncWindowsUpdates faz o upload do resultado do scan (somente se mudou) e
// persiste o cache local. Nunca falha o ciclo de inventario: erro so loga.
func (s *Service) SyncWindowsUpdates(ctx context.Context, result *windowsupdate.ScanResult) bool {
	if s == nil || result == nil {
		return false
	}
	_, cache, enabled := s.wuDeps()
	if !enabled {
		return false
	}

	cfg := s.debugConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	if agentID == "" {
		s.logf("[windows-update] upload ignorado: agentId ausente")
		return false
	}
	if !hasRemoteCredentials(cfg) {
		s.logf("[windows-update] upload ignorado: credenciais ausentes")
		return false
	}

	envelope := buildWindowsUpdateEnvelope(agentID, result)
	body, err := json.Marshal(envelope)
	if err != nil {
		s.logf("[windows-update] falha ao serializar payload: " + err.Error())
		return false
	}
	hash := hashPayload(body)

	if cache != nil {
		if cachedPayload, cachedHash, _, found, cacheErr := cache.GetWindowsUpdateCache(agentID); cacheErr == nil && found {
			if cachedHash == hash {
				s.logf("[windows-update] upload ignorado: payload sem mudancas")
				// A sonda de reboot pode ter mudado sem alterar o resto do payload;
				// o hash cobre tudo, entao nada a fazer.
				return false
			}
			_ = cachedPayload
		}
	}

	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + windowsUpdateEndpointPath
	if err := s.sendWindowsUpdateRequest(ctx, endpoint, cfg, http.MethodPost, body); err != nil {
		s.logf("[windows-update] POST falhou: " + err.Error())
		if err := s.sendWindowsUpdateRequest(ctx, endpoint, cfg, http.MethodPut, body); err != nil {
			s.logf("[windows-update] PUT falhou: " + err.Error())
			return false
		}
	}

	if cache != nil {
		if err := cache.SaveWindowsUpdateCache(agentID, hash, body, result.StartedAt); err != nil {
			s.logf("[windows-update] aviso: falha ao gravar cache local: " + err.Error())
		}
	}
	s.logf(fmt.Sprintf("[windows-update] inventario enviado: updates=%d pending=%d installed=%d",
		len(result.Updates), result.PendingCount, result.InstalledCount))
	return true
}

// ReportPendingReboot envia a sonda de reinicio SOMENTE quando o estado muda
// em relacao ao ultimo upload. Usado pelo loop de 10 min e apos instalacoes.
func (s *Service) ReportPendingReboot(ctx context.Context) (bool, error) {
	if s == nil {
		return false, nil
	}
	_, cache, enabled := s.wuDeps()
	if !enabled {
		return false, nil
	}

	cfg := s.debugConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	if agentID == "" || !hasRemoteCredentials(cfg) {
		return false, nil
	}

	info := windowsupdate.ProbePendingReboot()
	if cache != nil {
		reasons, checkedAt, _, found, err := cache.GetWindowsUpdateRebootState(agentID)
		if err == nil && found {
			same := info.Pending == (len(reasons) > 0) && sameReasons(reasons, info.Reasons)
			// Reenvia no maximo a cada 6h para o servidor ter um "checkedAt" fresco.
			if same && time.Since(checkedAt) < 6*time.Hour {
				return false, nil
			}
		}
	}

	payload := struct {
		Pending   bool     `json:"pending"`
		Reasons   []string `json:"reasons,omitempty"`
		CheckedAt string   `json:"checkedAt"`
	}{
		Pending:   info.Pending,
		Reasons:   info.Reasons,
		CheckedAt: info.CheckedAt.UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + pendingRebootEndpointPath
	if err := s.sendWindowsUpdateRequest(ctx, endpoint, cfg, http.MethodPost, body); err != nil {
		return false, err
	}
	if cache != nil {
		if err := cache.SaveWindowsUpdateRebootState(agentID, info.Reasons, info.CheckedAt, true); err != nil {
			s.logf("[windows-update] aviso: falha ao gravar estado de reboot: " + err.Error())
		}
	}
	return true, nil
}

// ScanAndSyncWindowsUpdates e o fluxo completo usado pelo comando remoto:
// scan + upload + sonda. Devolve o resultado e o erro do scan.
func (s *Service) ScanAndSyncWindowsUpdates(ctx context.Context) (*windowsupdate.ScanResult, error) {
	result, err := s.ScanWindowsUpdates(ctx)
	if err != nil {
		// Mesmo com scan falho, reporta a sonda de reinicio (barata).
		if _, reportErr := s.ReportPendingReboot(ctx); reportErr != nil {
			s.logf("[windows-update] falha ao reportar reinicio pendente: " + reportErr.Error())
		}
		return nil, err
	}
	s.SyncWindowsUpdates(ctx, result)
	if result.PendingReboot.Pending {
		// O envelope ja levou a sonda; apenas persiste o estado local.
		if _, cache, _ := s.wuDeps(); cache != nil {
			if cfgAgentID := strings.TrimSpace(s.debugConfig().AgentID); cfgAgentID != "" {
				_ = cache.SaveWindowsUpdateRebootState(cfgAgentID, result.PendingReboot.Reasons, result.PendingReboot.CheckedAt, true)
			}
		}
	}
	return result, nil
}

// InstallWindowsUpdates instala os alvos informados (ja revalidados no WUA pelo
// cliente) e, em seguida, atualiza o inventario local/servidor.
func (s *Service) InstallWindowsUpdates(ctx context.Context, targets []windowsupdate.InstallTarget) (*windowsupdate.InstallResult, error) {
	client, _, enabled := s.wuDeps()
	if client == nil || !enabled {
		return nil, windowsupdate.ErrUnsupportedOS
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	result, err := client.Install(ctx, targets)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// HideWindowsUpdates oculta/reexibe KBs no WUA (enforcement no SO, opt-in).
func (s *Service) HideWindowsUpdates(ctx context.Context, keys []string, hidden bool) (int, error) {
	client, _, enabled := s.wuDeps()
	if client == nil || !enabled {
		return 0, windowsupdate.ErrUnsupportedOS
	}
	return client.Hide(ctx, keys, hidden)
}

// ApplyWindowsUpdatePolicy aplica a politica de WU (opt-in) e devolve o backup.
func (s *Service) ApplyWindowsUpdatePolicy(settings windowsupdate.PolicySettings) (*windowsupdate.PolicyBackup, error) {
	if s == nil {
		return nil, windowsupdate.ErrUnsupportedOS
	}
	return windowsupdate.ApplyPolicy(settings)
}

// RestoreWindowsUpdatePolicy restaura o estado anterior da politica.
func (s *Service) RestoreWindowsUpdatePolicy(backup windowsupdate.PolicyBackup) error {
	return windowsupdate.RestorePolicy(backup)
}

// ReadWindowsUpdatePolicy le a politica atual.
func (s *Service) ReadWindowsUpdatePolicy() (windowsupdate.PolicySettings, error) {
	return windowsupdate.ReadPolicy()
}

// ---- envelope/upload helpers ----------------------------------------------

type agentWindowsUpdateScan struct {
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs"`
	StartedAt  string `json:"startedAt"`
	ErrorCode  string `json:"errorCode,omitempty"`
}

type agentWindowsUpdateReboot struct {
	Pending   bool     `json:"pending"`
	Reasons   []string `json:"reasons,omitempty"`
	CheckedAt string   `json:"checkedAt"`
}

type agentWindowsUpdateEnvelope struct {
	AgentID       string                   `json:"agentId"`
	CollectedAt   string                   `json:"collectedAt"`
	Scan          agentWindowsUpdateScan   `json:"scan"`
	PendingReboot agentWindowsUpdateReboot `json:"pendingReboot"`
	Updates       []windowsupdate.Update   `json:"updates"`
}

// scanStatusFor mapeia o resultado para o status do envelope. "partial"
// sinaliza que uma fonte falhou (ex.: QFE instaladas) e listas vazias NÃO devem
// ser tratadas como remoção pelo servidor.
func scanStatusFor(result *windowsupdate.ScanResult) string {
	if result != nil && result.Partial {
		return "partial"
	}
	return windowsupdate.ScanStatusOK
}

func buildWindowsUpdateEnvelope(agentID string, result *windowsupdate.ScanResult) agentWindowsUpdateEnvelope {
	collectedAt := result.StartedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	return agentWindowsUpdateEnvelope{
		AgentID:     agentID,
		CollectedAt: collectedAt.UTC().Format(time.RFC3339),
		Scan: agentWindowsUpdateScan{
			Status:     scanStatusFor(result),
			DurationMs: result.DurationMs,
			StartedAt:  result.StartedAt.UTC().Format(time.RFC3339),
		},
		PendingReboot: agentWindowsUpdateReboot{
			Pending:   result.PendingReboot.Pending,
			Reasons:   result.PendingReboot.Reasons,
			CheckedAt: result.PendingReboot.CheckedAt.UTC().Format(time.RFC3339),
		},
		Updates: result.Updates,
	}
}

var windowsUpdateHTTPClient = &http.Client{Timeout: windowsUpdateUploadTimeout}

func (s *Service) sendWindowsUpdateRequest(parent context.Context, endpoint string, cfg debug.Config, method string, body []byte) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, windowsUpdateUploadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return err
	}

	resp, err := windowsUpdateHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func hashPayload(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func hasRemoteCredentials(cfg debug.Config) bool {
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return false
	}
	scheme := strings.ToLower(strings.TrimSpace(cfg.ApiScheme))
	return scheme == "http" || scheme == "https"
}

func sameReasons(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, value := range a {
		seen[value] = struct{}{}
	}
	for _, value := range b {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

// WindowsUpdateDisabledReason devolve um motivo estavel quando o modulo nao
// pode rodar (kill switch/plataforma).
func (s *Service) WindowsUpdateDisabledReason() string {
	client, _, enabled := s.wuDeps()
	if client == nil {
		return windowsupdate.ScanErrorUnsupported
	}
	if !enabled {
		return windowsupdate.ScanErrorUnsupported
	}
	return ""
}

// ScanErrorCode traduz o erro do scan em codigo estavel para o servidor.
func ScanErrorCode(err error) string {
	switch {
	case err == nil:
		return windowsupdate.ScanErrorNone
	case errors.Is(err, windowsupdate.ErrUnsupportedOS):
		return windowsupdate.ScanErrorUnsupported
	case errors.Is(err, windowsupdate.ErrWuaUnavailable):
		return windowsupdate.ScanErrorWuaUnavailable
	case errors.Is(err, windowsupdate.ErrServiceDisabled):
		return windowsupdate.ScanErrorServiceDisabled
	case errors.Is(err, windowsupdate.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return windowsupdate.ScanErrorTimeout
	default:
		return windowsupdate.ScanErrorFailed
	}
}
