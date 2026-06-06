package selfupdate

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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"discovery/internal/errutil"

	"github.com/google/uuid"

	"discovery/app/netutil"
	"discovery/internal/buildinfo"
	"discovery/internal/processutil"
)

const (
	defaultCheckInterval  = 12 * time.Hour
	initialStartupDelay   = 30 * time.Second
	inactivePolicyRefresh = 30 * time.Minute
	backoffFirstFailure   = 5 * time.Minute
	backoffSecondFailure  = 30 * time.Minute
	backoffThirdOrGreater = 2 * time.Hour
	platformWindows       = "windows"
	architectureAMD64     = "amd64"
	artifactInstaller     = "Installer"
	reportTimeout         = 30 * time.Second
	manifestTimeout       = 30 * time.Second
	downloadDeadline      = 30 * time.Minute
	signatureTimeout      = 2 * time.Minute
	detachedProcessFlag   = 0x00000008
	pendingInstallFile    = "pending-install.json"
)

type Policy struct {
	Enabled                    bool   `json:"enabled"`
	CheckOnStartup             bool   `json:"checkOnStartup"`
	CheckPeriodically          bool   `json:"checkPeriodically"`
	CheckOnSyncManifest        bool   `json:"checkOnSyncManifest"`
	CheckEveryHours            int    `json:"checkEveryHours"`
	PreferredArtifactType      string `json:"preferredArtifactType,omitempty"`
	RequireSignatureValidation bool   `json:"requireSignatureValidation"`
}

func DefaultPolicy() Policy {
	return Policy{
		Enabled:               true,
		CheckOnStartup:        true,
		CheckPeriodically:     true,
		CheckOnSyncManifest:   true,
		CheckEveryHours:       int(defaultCheckInterval / time.Hour),
		PreferredArtifactType: artifactInstaller,
	}
}

func NormalizePolicy(policy Policy) Policy {
	defaults := DefaultPolicy()
	if policy.CheckEveryHours <= 0 {
		policy.CheckEveryHours = defaults.CheckEveryHours
	}
	policy.PreferredArtifactType = normalizeArtifactType(policy.PreferredArtifactType)
	return policy
}

type Updater struct {
	ApiScheme    string
	ApiServer    string
	GetToken     func() string
	GetAgentID   func() string
	GetPolicy    func() Policy
	GetApiScheme func() string
	GetApiServer func() string
	TempDir      string
	Logf         func(string, ...any)
	InvalidateCh <-chan bool
}

type UpdateManifest struct {
	ReleaseID              *string `json:"releaseId"`
	Revision               string  `json:"revision"`
	Enabled                bool    `json:"enabled"`
	Channel                string  `json:"channel"`
	CurrentVersion         string  `json:"currentVersion"`
	LatestVersion          *string `json:"latestVersion"`
	MinimumRequiredVersion *string `json:"minimumRequiredVersion"`
	UpdateAvailable        bool    `json:"updateAvailable"`
	Mandatory              bool    `json:"mandatory"`
	RolloutEligible        bool    `json:"rolloutEligible"`
	DirectUpdateSupported  bool    `json:"directUpdateSupported"`
	Platform               string  `json:"platform"`
	Architecture           string  `json:"architecture"`
	ArtifactType           string  `json:"artifactType"`
	FileName               *string `json:"fileName"`
	Sha256                 *string `json:"sha256"`
	SizeBytes              *int64  `json:"sizeBytes"`
	PublishedAtUtc         *string `json:"publishedAtUtc"`
	ReleaseNotes           *string `json:"releaseNotes"`
	Message                string  `json:"message"`
}

type reportOpts struct {
	ReleaseID      *string
	CurrentVersion string
	TargetVersion  string
	Message        string
	CorrelationID  string
}

type reportPayload struct {
	ReleaseID      *string `json:"releaseId"`
	EventType      string  `json:"eventType"`
	CurrentVersion string  `json:"currentVersion"`
	TargetVersion  string  `json:"targetVersion"`
	Message        string  `json:"message"`
	CorrelationID  string  `json:"correlationId"`
	OccurredAtUTC  string  `json:"occurredAtUtc"`
}

type pendingInstallState struct {
	ReleaseID      *string `json:"releaseId,omitempty"`
	CurrentVersion string  `json:"currentVersion"`
	TargetVersion  string  `json:"targetVersion"`
	CorrelationID  string  `json:"correlationId"`
	RecordedAtUTC  string  `json:"recordedAtUtc"`
}

func (u *Updater) Run(ctx context.Context, checkInterval time.Duration) {
	if checkInterval <= 0 {
		checkInterval = defaultCheckInterval
	}

	failures := 0
	startupPending := u.policy().CheckOnStartup
	delay := u.nextDelay(checkInterval, startupPending)
	timer := time.NewTimer(delay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			u.logf("self-update finalizado")
			return
		case <-timer.C:
			policy := u.policy()
			ran := false
			var err error
			if !policy.Enabled {
				u.logf("self-update agendado ignorado: policy disabled")
			} else if startupPending {
				ran = true
				err = u.CheckAndUpdate(ctx, false)
			} else if policy.CheckPeriodically {
				ran = true
				err = u.CheckAndUpdate(ctx, false)
			} else {
				u.logf("self-update agendado ignorado: periodic check disabled")
			}
			startupPending = false
			if ran && err != nil {
				failures++
				delay = backoffForFailures(failures)
				u.logf("ciclo self-update com falha (consecutivas=%d, proximo em %s): %v", failures, delay, err)
			} else {
				failures = 0
				delay = u.nextDelay(checkInterval, false)
			}
			timer.Reset(delay)
		case force := <-u.InvalidateCh:
			policy := u.policy()
			if !policy.Enabled {
				u.logf("self-update invalidado ignorado: policy disabled")
				delay = u.nextDelay(checkInterval, startupPending)
			} else if !force && !policy.CheckOnSyncManifest {
				u.logf("self-update invalidado ignorado: sync-manifest trigger disabled")
				delay = u.nextDelay(checkInterval, startupPending)
			} else {
				if force {
					u.logf("self-update forcado externamente; ignorando guards de versao e elegibilidade")
				} else {
					u.logf("self-update invalidado externamente; antecipando check")
				}
				err := u.CheckAndUpdate(ctx, force)
				startupPending = false
				if err != nil {
					failures++
					delay = backoffForFailures(failures)
					u.logf("ciclo antecipado com falha (consecutivas=%d, proximo em %s): %v", failures, delay, err)
				} else {
					failures = 0
					delay = u.nextDelay(checkInterval, false)
				}
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
		}
	}
}

func (u *Updater) nextDelay(fallback time.Duration, startupPending bool) time.Duration {
	policy := u.policy()
	if !policy.Enabled {
		return inactivePolicyRefresh
	}
	if startupPending && policy.CheckOnStartup {
		return initialStartupDelay
	}
	if !policy.CheckPeriodically {
		return inactivePolicyRefresh
	}
	interval := time.Duration(policy.CheckEveryHours) * time.Hour
	if interval <= 0 {
		interval = fallback
	}
	if interval <= 0 {
		interval = defaultCheckInterval
	}
	return interval
}

func (u *Updater) CheckAndUpdate(ctx context.Context, force bool) error {
	policy := u.policy()
	if !force && !policy.Enabled {
		u.logf("[selfupdate] check ignorado: policy disabled")
		return nil
	}

	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" {
		u.logf("[selfupdate] check ignorado: token vazio")
		return nil
	}
	if agentID == "" {
		u.logf("[selfupdate] check ignorado: agentId vazio")
		return nil
	}

	currentVersion := strings.TrimSpace(buildinfo.Version)
	if currentVersion == "" {
		currentVersion = "0.0.0"
	}
	correlationID := uuid.NewString()
	mode := "periodic"
	if force {
		mode = "forcado"
	}
	u.logf("[selfupdate] iniciando check (mode=%s current=%s correlationId=%s)", mode, currentVersion, correlationID)

	u.reportEvent(ctx, "CheckStarted", reportOpts{
		CurrentVersion: currentVersion,
		CorrelationID:  correlationID,
	})

	manifest, err := u.fetchManifest(ctx)
	if err != nil {
		u.logf("[selfupdate] falha ao buscar manifest: %v", err)
		u.reportEvent(ctx, "CheckCompleted", reportOpts{
			CurrentVersion: currentVersion,
			CorrelationID:  correlationID,
			Message:        "manifest fetch failed: " + err.Error(),
		})
		return err
	}
	u.logf("[selfupdate] manifest obtido: enabled=%t updateAvailable=%t rolloutEligible=%t direct=%t latestVersion=%s",
		manifest.Enabled, manifest.UpdateAvailable, manifest.RolloutEligible,
		manifest.DirectUpdateSupported, ptrStr(manifest.LatestVersion))

	// Em modo forçado (comando do servidor), ignora completamente o manifest
	// e baixa direto do endpoint público /api/v1/download/agent sem validação
	// de versão, elegibilidade ou sha256. É um force-reinstall.
	if force {
		u.logf("[selfupdate] modo forcado: ignorando manifest, baixando direto do endpoint publico")
		return u.forceInstallFromPublicEndpoint(ctx, currentVersion, correlationID)
	}

	if !manifest.Enabled {
		u.reportEvent(ctx, "CheckCompleted", reportOpts{
			ReleaseID:      manifest.ReleaseID,
			CurrentVersion: currentVersion,
			CorrelationID:  correlationID,
			Message:        "no eligible direct update",
		})
		return nil
	}
	if !manifest.UpdateAvailable || !manifest.RolloutEligible || !manifest.DirectUpdateSupported {
		u.reportEvent(ctx, "CheckCompleted", reportOpts{
			ReleaseID:      manifest.ReleaseID,
			CurrentVersion: currentVersion,
			CorrelationID:  correlationID,
			Message:        "no eligible direct update",
		})
		return nil
	}

	if manifest.LatestVersion == nil || strings.TrimSpace(*manifest.LatestVersion) == "" {
		u.reportEvent(ctx, "CheckCompleted", reportOpts{
			ReleaseID:      manifest.ReleaseID,
			CurrentVersion: currentVersion,
			CorrelationID:  correlationID,
			Message:        "manifest without latestVersion",
		})
		return nil
	}
	targetVersion := strings.TrimSpace(*manifest.LatestVersion)
	if compareVersions(targetVersion, currentVersion) <= 0 {
		u.reportEvent(ctx, "CheckCompleted", reportOpts{
			ReleaseID:      manifest.ReleaseID,
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        "latestVersion not greater than currentVersion",
		})
		return nil
	}

	u.reportEvent(ctx, "CheckCompleted", reportOpts{
		ReleaseID:      manifest.ReleaseID,
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
	})
	u.reportEvent(ctx, "UpdateAvailable", reportOpts{
		ReleaseID:      manifest.ReleaseID,
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		Message:        strings.TrimSpace(manifest.Message),
	})
	u.logf("[selfupdate] update disponivel: current=%s target=%s %s", currentVersion, targetVersion, manifestFileLogDetail(manifest))

	if manifest.Sha256 == nil || strings.TrimSpace(*manifest.Sha256) == "" {
		msg := "manifest without sha256"
		u.reportEvent(ctx, "DownloadFailed", reportOpts{
			ReleaseID:      manifest.ReleaseID,
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        msg,
		})
		return errors.New(msg)
	}

	u.reportEvent(ctx, "DownloadStarted", reportOpts{
		ReleaseID:      manifest.ReleaseID,
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
	})

	u.logf("[selfupdate] iniciando download: target=%s", targetVersion)
	tempPath, err := u.downloadToTemp(ctx, manifest)
	if err != nil {
		u.logf("[selfupdate] download falhou: %v", err)
		u.reportEvent(ctx, "DownloadFailed", reportOpts{
			ReleaseID:      manifest.ReleaseID,
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}
	u.logf("[selfupdate] download concluido: tempPath=%s", tempPath)

	u.reportEvent(ctx, "DownloadCompleted", reportOpts{
		ReleaseID:      manifest.ReleaseID,
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
	})

	u.reportEvent(ctx, "InstallStarted", reportOpts{
		ReleaseID:      manifest.ReleaseID,
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
	})
	if err := u.persistPendingInstallState(pendingInstallState{
		ReleaseID:      manifest.ReleaseID,
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		RecordedAtUTC:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de persistencia")
		u.reportEvent(ctx, "InstallFailed", reportOpts{
			ReleaseID:      manifest.ReleaseID,
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        "falha ao persistir estado pendente: " + err.Error(),
		})
		return err
	}

	if err := u.launchInstaller(tempPath); err != nil {
		u.clearPendingInstallState()
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de launch")
		u.reportEvent(ctx, "InstallFailed", reportOpts{
			ReleaseID:      manifest.ReleaseID,
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}

	u.logf("installer iniciado em background: %s", tempPath)
	return nil
}

// forceInstallFromPublicEndpoint baixa o instalador do endpoint público
// /api/v1/download/agent e executa /S /UPDATE sem nenhuma validação de
// versão, manifest ou sha256. Usado exclusivamente em modo force=true
// (comando de update vindo do servidor).
func (u *Updater) forceInstallFromPublicEndpoint(ctx context.Context, currentVersion, correlationID string) error {
	downloadURL := u.apiScheme() + "://" + u.apiServer() + "/api/v1/download/agent"
	u.logf("[selfupdate] force-install: baixando de %s", downloadURL)

	u.reportEvent(ctx, "UpdateAvailable", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  currentVersion,
		CorrelationID:  correlationID,
		Message:        "force reinstall via public endpoint",
	})

	u.reportEvent(ctx, "DownloadStarted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  currentVersion,
		CorrelationID:  correlationID,
	})

	tempPath, fileSha256, err := u.downloadFromURL(ctx, downloadURL)
	if err != nil {
		u.logf("[selfupdate] force-install download falhou: %v", err)
		u.reportEvent(ctx, "DownloadFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  currentVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}

	u.logf("[selfupdate] force-install download concluido: tempPath=%s sha256=%s", tempPath, fileSha256)
	u.reportEvent(ctx, "DownloadCompleted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  currentVersion,
		CorrelationID:  correlationID,
		Message:        fmt.Sprintf("sha256=%s", fileSha256),
	})

	u.reportEvent(ctx, "InstallStarted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  currentVersion,
		CorrelationID:  correlationID,
	})

	if err := u.persistPendingInstallState(pendingInstallState{
		CurrentVersion: currentVersion,
		TargetVersion:  currentVersion,
		CorrelationID:  correlationID,
		RecordedAtUTC:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de persistencia")
		u.reportEvent(ctx, "InstallFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  currentVersion,
			CorrelationID:  correlationID,
			Message:        "falha ao persistir estado pendente: " + err.Error(),
		})
		return err
	}

	if err := u.launchInstaller(tempPath); err != nil {
		u.clearPendingInstallState()
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de launch")
		u.reportEvent(ctx, "InstallFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  currentVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}

	u.logf("[selfupdate] force-install: instalador iniciado em background: %s", tempPath)
	return nil
}

// InstallFromURL faz o download do instalador a partir de uma URL direta
// fornecida pelo servidor e o executa em background (/S /UPDATE).
// Usado quando o comando de update vem com action=install e url preenchida.
func (u *Updater) InstallFromURL(ctx context.Context, version, downloadURL string) error {
	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		return errors.New("url de download vazia")
	}

	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" {
		return errors.New("token vazio")
	}
	if agentID == "" {
		return errors.New("agentId vazio")
	}

	currentVersion := strings.TrimSpace(buildinfo.Version)
	if currentVersion == "" {
		currentVersion = "0.0.0"
	}
	targetVersion := strings.TrimSpace(version)
	if targetVersion == "" {
		targetVersion = "unknown"
	}
	correlationID := uuid.NewString()

	u.reportEvent(ctx, "UpdateAvailable", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		Message:        "install direto via comando do servidor",
	})

	u.reportEvent(ctx, "DownloadStarted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
	})

	tempPath, fileSha256, err := u.downloadFromURL(ctx, downloadURL)
	if err != nil {
		u.reportEvent(ctx, "DownloadFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}

	u.reportEvent(ctx, "DownloadCompleted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		Message:        fmt.Sprintf("sha256=%s", fileSha256),
	})

	u.reportEvent(ctx, "InstallStarted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
	})

	if err := u.persistPendingInstallState(pendingInstallState{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		RecordedAtUTC:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de persistencia")
		u.reportEvent(ctx, "InstallFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        "falha ao persistir estado pendente: " + err.Error(),
		})
		return err
	}

	if err := u.launchInstaller(tempPath); err != nil {
		u.clearPendingInstallState()
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de launch")
		u.reportEvent(ctx, "InstallFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}

	u.logf("installer direto iniciado em background: version=%s url=%s", targetVersion, downloadURL)
	return nil
}

// downloadFromURL faz o download do instalador a partir de uma URL pública.
// Retorna o caminho do arquivo temporário e o SHA256 do conteúdo.
func (u *Updater) downloadFromURL(ctx context.Context, downloadURL string) (string, string, error) {
	if err := os.MkdirAll(u.TempDir, 0o755); err != nil {
		return "", "", err
	}

	path := filepath.Join(u.TempDir, fmt.Sprintf("discovery-update-%s.exe", uuid.NewString()))
	f, err := os.Create(path)
	if err != nil {
		return "", "", err
	}

	ctxDownload, cancel := context.WithDeadline(ctx, time.Now().Add(downloadDeadline))
	defer cancel()

	req, err := http.NewRequestWithContext(ctxDownload, http.MethodGet, downloadURL, nil)
	if err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos erro de request")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download temp")
		return "", "", err
	}

	// Usa o mesmo header de auth que os outros endpoints da API
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos erro de header")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download credenciais invalidas")
		return "", "", err
	}

	client := &http.Client{Timeout: downloadDeadline}
	resp, err := client.Do(req)
	if err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos falha HTTP")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha HTTP")
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo status != 200")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download status != 200")
		return "", "", fmt.Errorf("download status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	buf := make([]byte, 128*1024)
	if _, err := io.CopyBuffer(f, resp.Body, buf); err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos falha de copy")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de copy")
		return "", "", err
	}
	if err := f.Sync(); err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos falha de sync")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de sync")
		return "", "", err
	}
	if err := f.Close(); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de close")
		return "", "", err
	}

	sha, err := fileSHA256(path)
	if err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha sha256")
		return "", "", err
	}

	return path, sha, nil
}

func (u *Updater) ResumePendingInstallReport(ctx context.Context) {
	state, err := u.loadPendingInstallState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		u.logf("falha ao carregar estado pendente de install: %v", err)
		return
	}
	currentVersion := strings.TrimSpace(buildinfo.Version)
	if currentVersion == "" {
		currentVersion = "0.0.0"
	}
	if compareVersions(currentVersion, state.TargetVersion) < 0 {
		u.logf("estado pendente de install mantido: versao atual=%s target=%s", currentVersion, state.TargetVersion)
		return
	}
	u.reportEvent(ctx, "InstallSucceeded", reportOpts{
		ReleaseID:      state.ReleaseID,
		CurrentVersion: state.CurrentVersion,
		TargetVersion:  state.TargetVersion,
		CorrelationID:  state.CorrelationID,
		Message:        "instalacao confirmada apos reinicio do processo",
	})
	u.clearPendingInstallState()
}

func (u *Updater) fetchManifest(ctx context.Context) (*UpdateManifest, error) {
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" {
		return nil, errors.New("token vazio")
	}
	if agentID == "" {
		return nil, errors.New("agentId vazio")
	}

	endpoint := u.apiScheme() + "://" + u.apiServer() + "/api/v1/agent-auth/me/update/manifest"
	q := url.Values{}
	q.Set("currentVersion", strings.TrimSpace(buildinfo.Version))
	q.Set("platform", platformWindows)
	q.Set("architecture", architectureAMD64)
	q.Set("artifactType", normalizeArtifactType(u.policy().PreferredArtifactType))
	endpoint += "?" + q.Encode()

	ctxReq, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxReq, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: manifestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return nil, fmt.Errorf("manifest status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var manifest UpdateManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (u *Updater) downloadToTemp(ctx context.Context, m *UpdateManifest) (string, error) {
	if m == nil {
		return "", errors.New("manifest nil")
	}
	if m.Sha256 == nil || strings.TrimSpace(*m.Sha256) == "" {
		return "", errors.New("sha256 ausente no manifest")
	}
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" {
		return "", errors.New("token vazio")
	}
	if agentID == "" {
		return "", errors.New("agentId vazio")
	}

	if err := os.MkdirAll(u.TempDir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(u.TempDir, fmt.Sprintf("discovery-update-%s.exe", uuid.NewString()))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer func() {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo de download")
	}()

	endpoint := u.apiScheme() + "://" + u.apiServer() + "/api/v1/agent-auth/me/update/download"
	q := url.Values{}
	if m.ReleaseID != nil && strings.TrimSpace(*m.ReleaseID) != "" {
		q.Set("releaseId", strings.TrimSpace(*m.ReleaseID))
	}
	if m.LatestVersion != nil && strings.TrimSpace(*m.LatestVersion) != "" {
		q.Set("version", strings.TrimSpace(*m.LatestVersion))
	}
	policy := u.policy()
	artifactType := strings.TrimSpace(policy.PreferredArtifactType)
	if artifactType == "" {
		artifactType = strings.TrimSpace(m.ArtifactType)
	}
	artifactType = normalizeArtifactType(artifactType)
	q.Set("platform", platformWindows)
	q.Set("architecture", architectureAMD64)
	q.Set("artifactType", artifactType)
	endpoint += "?" + q.Encode()

	ctxDownload, cancel := context.WithDeadline(ctx, time.Now().Add(downloadDeadline))
	defer cancel()

	req, err := http.NewRequestWithContext(ctxDownload, http.MethodGet, endpoint, nil)
	if err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download temp")
		return "", err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download credenciais invalidas")
		return "", err
	}

	client := &http.Client{Timeout: downloadDeadline}
	resp, err := client.Do(req)
	if err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha HTTP")
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()

		// Fallback: tenta o endpoint público para rebuilds de mesma versão.
		publicURL := u.apiScheme() + "://" + u.apiServer() + "/api/v1/download/agent"
		u.logf("selfupdate: download autenticado retornou %d — tentando endpoint público: %s", resp.StatusCode, publicURL)

		req2, err2 := http.NewRequestWithContext(ctxDownload, http.MethodGet, publicURL, nil)
		if err2 != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback erro request")
			return "", fmt.Errorf("download status=%d (fallback request error: %v)", resp.StatusCode, err2)
		}
		// Public endpoint is AllowAnonymous but we still send auth for consistency.
		if err2 := netutil.SetAgentAuthHeadersWithAgentID(req2, token, agentID); err2 != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback credenciais")
			return "", fmt.Errorf("download status=%d (fallback auth error: %v)", resp.StatusCode, err2)
		}

		resp2, err2 := client.Do(req2)
		if err2 != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback falha HTTP")
			return "", fmt.Errorf("download status=%d (fallback HTTP error: %v)", resp.StatusCode, err2)
		}
		defer resp2.Body.Close()

		if resp2.StatusCode != http.StatusOK {
			body2, _ := io.ReadAll(io.LimitReader(resp2.Body, 8*1024))
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback status != 200")
			return "", fmt.Errorf("download status=%d (fallback status=%d body=%s)", resp.StatusCode, resp2.StatusCode, strings.TrimSpace(string(body2)))
		}

		u.logf("selfupdate: fallback público OK — baixando de %s", publicURL)
		buf := make([]byte, 128*1024)
		if _, err := io.CopyBuffer(f, resp2.Body, buf); err != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback apos falha de copy")
			return "", err
		}
		if err := f.Sync(); err != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback apos falha de sync")
			return "", err
		}
		if err := f.Close(); err != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback apos falha de close")
			return "", err
		}

		actual, err := fileSHA256(path)
		if err != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback apos falha sha256")
			return "", err
		}
		expected := strings.ToLower(strings.TrimSpace(*m.Sha256))
		if expected != "" && actual != expected {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback sha256 mismatch")
			return "", fmt.Errorf("sha256 mismatch (fallback): expected=%s got=%s", expected, actual)
		}
		return path, nil
	}

	buf := make([]byte, 128*1024)
	if _, err := io.CopyBuffer(f, resp.Body, buf); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de copy")
		return "", err
	}
	if err := f.Sync(); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de sync")
		return "", err
	}
	if err := f.Close(); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de close")
		return "", err
	}

	actual, err := fileSHA256(path)
	if err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha sha256")
		return "", err
	}
	expected := strings.ToLower(strings.TrimSpace(*m.Sha256))
	if expected != "" && actual != expected {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download sha256 mismatch")
		return "", fmt.Errorf("sha256 mismatch: expected=%s got=%s", expected, actual)
	}
	if policy.RequireSignatureValidation {
		if err := validateAuthenticodeSignature(ctx, path); err != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download assinatura invalida")
			return "", err
		}
	}

	return path, nil
}

func (u *Updater) launchInstaller(exePath string) error {
	exePath = strings.TrimSpace(exePath)
	if exePath == "" {
		return errors.New("installer path vazio")
	}
	cmd := exec.Command(exePath, "/S", "/UPDATE")
	processutil.HideWindow(cmd)
	// DETACHED_PROCESS (0x00000008) desacopla do ciclo de vida do agent.
	// setSysProcCreationFlags usa reflexao para compatibilidade cross-platform.
	if cmd.SysProcAttr != nil {
		setSysProcCreationFlags(cmd.SysProcAttr, detachedProcessFlag)
	}
	return cmd.Start()
}

func (u *Updater) reportEvent(ctx context.Context, eventType string, opts reportOpts) {
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" || agentID == "" {
		return
	}
	endpoint := u.apiScheme() + "://" + u.apiServer() + "/api/v1/agent-auth/me/update/report"

	payload := reportPayload{
		ReleaseID:      opts.ReleaseID,
		EventType:      strings.TrimSpace(eventType),
		CurrentVersion: strings.TrimSpace(opts.CurrentVersion),
		TargetVersion:  strings.TrimSpace(opts.TargetVersion),
		Message:        strings.TrimSpace(opts.Message),
		CorrelationID:  strings.TrimSpace(opts.CorrelationID),
		OccurredAtUTC:  time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		u.logf("reportEvent marshal falhou (%s): %v", eventType, err)
		return
	}

	ctxReq, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctxReq, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		u.logf("reportEvent request falhou (%s): %v", eventType, err)
		return
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		u.logf("reportEvent credenciais invalidas (%s): %v", eventType, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: reportTimeout}
	resp, err := client.Do(req)
	if err != nil {
		u.logf("reportEvent envio falhou (%s): %v", eventType, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payloadBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		u.logf("reportEvent status invalido (%s): %d body=%s", eventType, resp.StatusCode, strings.TrimSpace(string(payloadBody)))
	}
}

func (u *Updater) getToken() string {
	if u.GetToken == nil {
		return ""
	}
	return u.GetToken()
}

func (u *Updater) getAgentID() string {
	if u.GetAgentID == nil {
		return ""
	}
	return u.GetAgentID()
}

func (u *Updater) apiScheme() string {
	if u.GetApiScheme != nil {
		return strings.TrimSpace(u.GetApiScheme())
	}
	return strings.TrimSpace(u.ApiScheme)
}

func (u *Updater) apiServer() string {
	if u.GetApiServer != nil {
		return strings.TrimSpace(u.GetApiServer())
	}
	return strings.TrimSpace(u.ApiServer)
}

func (u *Updater) policy() Policy {
	if u.GetPolicy == nil {
		return DefaultPolicy()
	}
	return NormalizePolicy(u.GetPolicy())
}

func (u *Updater) logf(format string, args ...any) {
	if u.Logf != nil {
		u.Logf(format, args...)
	}
}

func (u *Updater) pendingInstallStatePath() string {
	if strings.TrimSpace(u.TempDir) == "" {
		return ""
	}
	return filepath.Join(u.TempDir, pendingInstallFile)
}

func (u *Updater) persistPendingInstallState(state pendingInstallState) error {
	path := u.pendingInstallStatePath()
	if path == "" {
		return errors.New("diretorio temporario de update nao configurado")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

func (u *Updater) loadPendingInstallState() (pendingInstallState, error) {
	path := u.pendingInstallStatePath()
	if path == "" {
		return pendingInstallState{}, os.ErrNotExist
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return pendingInstallState{}, err
	}
	var state pendingInstallState
	if err := json.Unmarshal(body, &state); err != nil {
		return pendingInstallState{}, err
	}
	return state, nil
}

func (u *Updater) clearPendingInstallState() {
	path := u.pendingInstallStatePath()
	if path == "" {
		return
	}
	errutil.LogIfErr(os.Remove(path), "selfupdate: limpar estado de instalacao pendente")
}

func compareVersions(a, b string) int {
	ap := parseVersionTriplet(a)
	bp := parseVersionTriplet(b)
	for i := 0; i < 3; i++ {
		if ap[i] > bp[i] {
			return 1
		}
		if ap[i] < bp[i] {
			return -1
		}
	}
	return 0
}

func parseVersionTriplet(value string) [3]int {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ".")
	result := [3]int{0, 0, 0}
	for i := 0; i < 3; i++ {
		if i >= len(parts) {
			break
		}
		result[i] = parseLeadingInt(parts[i])
	}
	return result
}

func parseLeadingInt(part string) int {
	part = strings.TrimSpace(part)
	if part == "" {
		return 0
	}
	var b strings.Builder
	for _, r := range part {
		if r < '0' || r > '9' {
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return 0
	}
	v, err := strconv.Atoi(b.String())
	if err != nil {
		return 0
	}
	return v
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return strings.ToLower(hex.EncodeToString(h.Sum(nil))), nil
}

func backoffForFailures(failures int) time.Duration {
	if failures <= 1 {
		return backoffFirstFailure
	}
	if failures == 2 {
		return backoffSecondFailure
	}
	return backoffThirdOrGreater
}

func setSysProcCreationFlags(attr *syscall.SysProcAttr, value uint32) {
	if attr == nil {
		return
	}
	v := reflect.ValueOf(attr).Elem()
	f := v.FieldByName("CreationFlags")
	if !f.IsValid() || !f.CanSet() {
		return
	}
	switch f.Kind() {
	case reflect.Uint32, reflect.Uint, reflect.Uint64:
		f.SetUint(uint64(value))
	}
}

func normalizeArtifactType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return artifactInstaller
	}
	if strings.EqualFold(value, artifactInstaller) {
		return artifactInstaller
	}
	return value
}

// ptrStr retorna uma representação legível de um ponteiro string.
func ptrStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// manifestFileLogDetail retorna detalhes do arquivo do manifest para log.
func manifestFileLogDetail(m *UpdateManifest) string {
	if m == nil {
		return ""
	}
	parts := []string{}
	if m.FileName != nil && strings.TrimSpace(*m.FileName) != "" {
		parts = append(parts, fmt.Sprintf("file=%s", *m.FileName))
	}
	if m.SizeBytes != nil && *m.SizeBytes > 0 {
		parts = append(parts, fmt.Sprintf("size=%d", *m.SizeBytes))
	}
	if m.Sha256 != nil && strings.TrimSpace(*m.Sha256) != "" {
		s := strings.TrimSpace(*m.Sha256)
		if len(s) > 12 {
			s = s[:12] + "..."
		}
		parts = append(parts, fmt.Sprintf("sha256=%s", s))
	}
	if m.ArtifactType != "" {
		parts = append(parts, fmt.Sprintf("artifact=%s", m.ArtifactType))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("(%s)", strings.Join(parts, " "))
}

func validateAuthenticodeSignature(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, signatureTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"powershell",
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		"$sig = Get-AuthenticodeSignature -LiteralPath $args[0]; if ($null -eq $sig) { Write-Output 'UnknownError'; exit 3 }; Write-Output $sig.Status",
		path,
	)
	processutil.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	status := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return errors.New("timeout ao validar assinatura Authenticode")
	}
	if err != nil {
		if status == "" {
			status = err.Error()
		}
		return fmt.Errorf("falha ao validar assinatura Authenticode: %s", status)
	}
	if !strings.EqualFold(status, "Valid") {
		return fmt.Errorf("assinatura Authenticode invalida: %s", status)
	}
	return nil
}
