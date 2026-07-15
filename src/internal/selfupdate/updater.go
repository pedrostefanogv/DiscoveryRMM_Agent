package selfupdate

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"discovery/internal/buildinfo"
	"discovery/internal/errutil"
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

	// OnSelfUpdateInstall é um callback opcional que, se definido, é chamado
	// ANTES do launchInstaller para mostrar UI (ex.: PSADT Welcome + Progress)
	// e executar o instalador de forma controlada (sem defer, sem cancelar).
	// O callback recebe (ctx, exePath, targetVersion) e deve:
	//   - Mostrar progresso visual (Welcome sem AllowDefer + Progress)
	//   - Executar o instalador com /S /UPDATE
	//   - Retornar nil em caso de sucesso, ou erro
	// Se o callback retornar erro, o fluxo trata como InstallFailed (sem fallback).
	// Se OnSelfUpdateInstall for nil, usa launchInstaller direto (comportamento legado).
	OnSelfUpdateInstall func(ctx context.Context, exePath string, targetVersion string) error

	// OnArtifactReady é chamado após um download HTTP bem-sucedido para
	// publicar o artifact no P2P. Recebe: path, artifactID (releaseID ou "sha256:<hex>"),
	// sha256 e version. É best-effort — falhas não interrompem o update.
	OnArtifactReady func(ctx context.Context, path, artifactID, sha256, version string) error

	// FindPeersByReleaseID consulta o P2P por peers que possuem o artifact
	// com o artifactID especificado. Retorna lista de agentIDs.
	FindPeersByReleaseID func(ctx context.Context, artifactID string) ([]string, error)

	// DownloadFromPeer baixa o artifact pelo artifactID de um peer específico.
	// Retorna o path do arquivo baixado.
	DownloadFromPeer func(ctx context.Context, artifactID, peerID string) (string, error)
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
	AgentID string               `json:"agentId"`
	Request reportPayloadRequest `json:"request"`
}

type reportPayloadRequest struct {
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
	u.logf("[selfupdate] iniciando download (mode=%s current=%s correlationId=%s)", mode, currentVersion, correlationID)

	_ = currentVersion // usado abaixo
	_ = correlationID  // mantido para logging ja embutido em downloadFromCacheOrPublic

	// Baixa do endpoint publico /api/v1/download/agent (P2P-first se disponivel).
	u.logf("[selfupdate] baixando do endpoint publico /api/v1/download/agent")
	tempPath, fileSha256, err := u.downloadFromCacheOrPublic(ctx, "")
	if err != nil {
		u.logf("[selfupdate] download falhou: %v", err)
		return err
	}
	u.logf("[selfupdate] download concluido: tempPath=%s sha256=%s", tempPath, fileSha256[:12])

	// Extrai a versao real do binario baixado
	targetVersion := extractFileVersion(tempPath)
	if targetVersion == "" {
		u.logf("[selfupdate] aviso: nao conseguiu extrair versao do arquivo, usando currentVersion=%s", currentVersion)
		targetVersion = currentVersion
	}
	u.logf("[selfupdate] versao alvo determinada: %s (current=%s)", targetVersion, currentVersion)

	// Se nao for force, verifica se realmente ha update (versao alvo > atual)
	if !force && compareVersions(targetVersion, currentVersion) <= 0 {
		u.logf("[selfupdate] ja esta na versao mais recente (current=%s >= target=%s)", currentVersion, targetVersion)
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp mesma versao")
		return nil
	}

	// Publica no P2P com artifactID fixo "agent-current" — o endpoint /api/v1/download/agent
	// sempre serve o build atual (current), entao o nome do artifact e estavel entre versoes.
	// O SHA256 e usado para validacao pos-download, nao como ID de busca.
	artifactID := "agent-current"
	if u.OnArtifactReady != nil {
		_ = u.OnArtifactReady(ctx, tempPath, artifactID, fileSha256, targetVersion)
	}

	if err := u.persistPendingInstallState(pendingInstallState{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		RecordedAtUTC:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de persistencia")
		return err
	}

	if err := u.launchInstallerWithUI(ctx, tempPath, targetVersion); err != nil {
		u.clearPendingInstallState()
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de launch")
		return err
	}

	u.logf("installer iniciado em background: %s", tempPath)
	return nil
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
