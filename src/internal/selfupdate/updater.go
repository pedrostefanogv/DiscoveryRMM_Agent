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
	if !policy.Enabled {
		u.logf("self-update ignorado: policy disabled")
		return nil
	}

	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" {
		u.logf("self-update ignorado: token vazio")
		return nil
	}
	if agentID == "" {
		u.logf("self-update ignorado: agentId vazio")
		return nil
	}

	currentVersion := strings.TrimSpace(buildinfo.Version)
	if currentVersion == "" {
		currentVersion = "0.0.0"
	}
	correlationID := uuid.NewString()

	u.reportEvent(ctx, "CheckStarted", reportOpts{
		CurrentVersion: currentVersion,
		CorrelationID:  correlationID,
	})

	manifest, err := u.fetchManifest(ctx)
	if err != nil {
		u.reportEvent(ctx, "CheckCompleted", reportOpts{
			CurrentVersion: currentVersion,
			CorrelationID:  correlationID,
			Message:        "manifest fetch failed: " + err.Error(),
		})
		return err
	}

	// Em modo forçado (comando do servidor), ignora guards de elegibilidade mas respeita o kill-switch global (Enabled).
	if !manifest.Enabled {
		u.reportEvent(ctx, "CheckCompleted", reportOpts{
			ReleaseID:      manifest.ReleaseID,
			CurrentVersion: currentVersion,
			CorrelationID:  correlationID,
			Message:        "no eligible direct update",
		})
		return nil
	}
	if !force && (!manifest.UpdateAvailable || !manifest.RolloutEligible || !manifest.DirectUpdateSupported) {
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
	if !force && compareVersions(targetVersion, currentVersion) <= 0 {
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

	tempPath, err := u.downloadToTemp(ctx, manifest)
	if err != nil {
		u.reportEvent(ctx, "DownloadFailed", reportOpts{
			ReleaseID:      manifest.ReleaseID,
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}

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

	endpoint := strings.TrimSpace(u.ApiScheme) + "://" + strings.TrimSpace(u.ApiServer) + "/api/v1/agent-auth/me/update/manifest"
	q := url.Values{}
	q.Set("currentVersion", strings.TrimSpace(buildinfo.Version))
	q.Set("platform", platformWindows)
	q.Set("architecture", architectureAMD64)
	endpoint += "?" + q.Encode()

	ctxReq, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxReq, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	netutil.SetAgentAuthHeaders(req, token)
	req.Header.Set("X-Agent-ID", agentID)

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

	endpoint := strings.TrimSpace(u.ApiScheme) + "://" + strings.TrimSpace(u.ApiServer) + "/api/v1/agent-auth/me/update/download"
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
	netutil.SetAgentAuthHeaders(req, token)
	req.Header.Set("X-Agent-ID", agentID)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha HTTP")
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download status != 200")
		return "", fmt.Errorf("download status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
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
	attr := &syscall.SysProcAttr{}
	setSysProcCreationFlags(attr, detachedProcessFlag)
	cmd.SysProcAttr = attr
	return cmd.Start()
}

func (u *Updater) reportEvent(ctx context.Context, eventType string, opts reportOpts) {
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" || agentID == "" {
		return
	}
	endpoint := strings.TrimSpace(u.ApiScheme) + "://" + strings.TrimSpace(u.ApiServer) + "/api/v1/agent-auth/me/update/report"

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
	netutil.SetAgentAuthHeaders(req, token)
	req.Header.Set("X-Agent-ID", agentID)
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
