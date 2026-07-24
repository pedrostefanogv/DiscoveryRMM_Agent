package selfupdate

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
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
	breakawayFromJobFlag  = 0x01000000 // CREATE_BREAKAWAY_FROM_JOB
	newProcessGroupFlag   = 0x00000200 // CREATE_NEW_PROCESS_GROUP
	pendingInstallFile    = "pending-install.json"
	maxInstallAttempts    = 3
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

	// installing é um flag atômico que previne execuções concorrentes de
	// CheckAndUpdate. Quando true, um instalador já foi lançado e o processo
	// está aguardando o NSIS fazer taskkill. Chamadas concorrentes (ex.:
	// InvalidateCh durante o download/launch) são ignoradas.
	installing atomic.Bool
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
	ReleaseID       *string `json:"releaseId,omitempty"`
	CurrentVersion  string  `json:"currentVersion"`
	TargetVersion   string  `json:"targetVersion"`
	CorrelationID   string  `json:"correlationId"`
	RecordedAtUTC   string  `json:"recordedAtUtc"`
	InstallAttempts int     `json:"installAttempts"`
	InstalledCommit string  `json:"installedCommit"`
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
	// ── Previne execuções concorrentes ──
	// Quando o InvalidateCh dispara durante um download/launch em andamento,
	// o segundo CheckAndUpdate faria download duplicado e incrementaria o
	// contador de tentativas incorretamente. A trava é liberada pelo
	// ResumePendingInstallReport no próximo startup (após NSIS taskkill).
	if !force && u.installing.Load() {
		u.logf("[selfupdate] check ignorado: instalador ja em andamento")
		return nil
	}

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
	currentCommit := strings.TrimSpace(buildinfo.Commit)
	correlationID := uuid.NewString()
	mode := "periodic"
	if force {
		mode = "forcado"
	}
	u.logf("[selfupdate] iniciando check (mode=%s current=%s commit=%s correlationId=%s)", mode, currentVersion, currentCommit, correlationID)

	// ── Fase 1: Consulta versao/commit do servidor (leve, ~200 bytes) ──
	// Se o servidor nao tiver o endpoint /version, faz fallback para o fluxo
	// antigo (download direto sem pre-check).
	if !force {
		serverInfo, err := u.fetchAgentVersion(ctx)
		if err != nil {
			u.logf("[selfupdate] aviso: endpoint /version indisponivel (%v) — usando fluxo sem pre-check", err)
			// Fallback: usa fluxo antigo sem decisao version+commit.
			return u.checkAndUpdateFallback(ctx, force, currentVersion, correlationID)
		}
		serverVersion := strings.TrimSpace(serverInfo.Version)
		serverCommit := strings.TrimSpace(serverInfo.CommitHash)
		serverSHA256 := strings.TrimSpace(serverInfo.Sha256)

		u.logf("[selfupdate] servidor: version=%s commit=%s sha256=%s", serverVersion, serverCommit, serverSHA256[:12])

		// Decisao version+commit:
		// - Versoes diferentes → download (update normal)
		// - Mesma versao, commit diferente → download (rebuild)
		// - Mesma versao E mesmo commit → skip (mesmo build)
		if compareVersions(serverVersion, currentVersion) == 0 &&
			currentCommit != "" && currentCommit != "unknown" &&
			serverCommit != "" && serverCommit != "unknown" &&
			strings.EqualFold(currentCommit, serverCommit) {
			u.logf("[selfupdate] skip: mesmo build (version=%s commit=%s)", currentVersion, currentCommit)
			return nil
		}

		// ── Trava instalação ANTES do download ──
		// Previne que InvalidateCh dispare um segundo CheckAndUpdate.
		// Só é liberada no próximo startup (ResumePendingInstallReport)
		// ou em clearPendingInstallState (caminho de erro).
		u.installing.Store(true)

		// Usa o SHA256 do servidor para P2P artifactID preciso.
		publicSHA256 := serverSHA256
		tempPath, fileSha256, fromP2P, err := u.downloadFromCacheOrPublic(ctx, publicSHA256)
		if err != nil {
			u.logf("[selfupdate] download falhou: %v", err)
			u.installing.Store(false)
			return err
		}
		u.logf("[selfupdate] download concluido: tempPath=%s sha256=%s fromP2P=%v", tempPath, fileSha256[:12], fromP2P)

		targetVersion := extractFileVersion(tempPath)
		if targetVersion == "" {
			targetVersion = serverVersion
		}
		// Se extractFileVersion retornou uma versão inferior à do servidor (ex.: NSIS
		// VIProductVersion desatualizado), prefere serverVersion que é a fonte canônica.
		if targetVersion != serverVersion && serverVersion != "" &&
			compareVersions(targetVersion, serverVersion) < 0 {
			u.logf("[selfupdate] versao PE=%s inferior a server=%s — usando serverVersion", targetVersion, serverVersion)
			targetVersion = serverVersion
		}
		u.logf("[selfupdate] versao alvo: %s (current=%s serverCommit=%s)", targetVersion, currentVersion, serverCommit)

		// ── Validação de versão: skip se alvo é inferior à versão atual ──
		// Isso evita loop de instalação quando extractFileVersion (PE) diverge de
		// buildinfo.Version (ldflags), por exemplo quando INFO_FILEVERSION do NSIS
		// não reflete o productVersion correto.
		// IMPORTANTE: NÃO bloqueia quando targetVersion == currentVersion (pode ser rebuild).
		if !force && compareVersions(targetVersion, currentVersion) < 0 &&
			compareVersions(serverVersion, currentVersion) <= 0 {
			u.logf("[selfupdate] ja esta na versao mais recente (current=%s > target=%s, server=%s) — ignorando",
				currentVersion, targetVersion, serverVersion)
			u.installing.Store(false)
			errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp mesma versao")
			return nil
		}

		// Publica no P2P (apenas se veio de HTTP — P2P já está indexado).
		if !fromP2P {
			artifactID := "selfupdate:" + strings.ToLower(fileSha256)
			if u.OnArtifactReady != nil {
				_ = u.OnArtifactReady(ctx, tempPath, artifactID, fileSha256, targetVersion)
			}
		}

		// ── Verifica maxInstallAttempts ANTES de persistir ──
		// Evita que o contador vá de 3 para 4 quando o InvalidateCh dispara
		// um novo ciclo antes do ResumePendingInstallReport rodar no startup.
		if existing, loadErr := u.loadPendingInstallState(); loadErr == nil &&
			existing.TargetVersion == targetVersion &&
			existing.CurrentVersion == currentVersion &&
			existing.InstallAttempts >= maxInstallAttempts {
			u.logf("[selfupdate] maximo de %d tentativas ja atingido para target=%s — abortando",
				maxInstallAttempts, targetVersion)
			u.clearPendingInstallState()
			errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp max attempts")
			u.reportInstallFailed(ctx, existing, "max-install-attempts: "+strconv.Itoa(existing.InstallAttempts)+" tentativas")
			return fmt.Errorf("max install attempts reached for %s", targetVersion)
		}

		if err := u.persistPendingInstallState(pendingInstallState{
			CurrentVersion:  currentVersion,
			TargetVersion:   targetVersion,
			CorrelationID:   correlationID,
			RecordedAtUTC:   time.Now().UTC().Format(time.RFC3339),
			InstalledCommit: currentCommit,
		}); err != nil {
			u.installing.Store(false)
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

	// ── Forced mode: ignora version+commit check, sempre baixa ──
	return u.checkAndUpdateFallback(ctx, force, currentVersion, correlationID)
}

// checkAndUpdateFallback executa o fluxo antigo de download+install sem pre-check
// version+commit. Usado em modo forcado ou quando o endpoint /version nao existe.
func (u *Updater) checkAndUpdateFallback(ctx context.Context, force bool, currentVersion, correlationID string) error {
	u.logf("[selfupdate] usando fluxo fallback (mode=forcado=%v current=%s)", force, currentVersion)

	// Trava instalação ANTES do download.
	u.installing.Store(true)

	publicSHA256, shaErr := u.fetchPublicSHA256(ctx)
	if shaErr != nil {
		u.logf("[selfupdate] aviso: nao foi possivel obter SHA256 do servidor: %v", shaErr)
	}

	tempPath, fileSha256, fromP2P, err := u.downloadFromCacheOrPublic(ctx, publicSHA256)
	if err != nil {
		u.logf("[selfupdate] download falhou: %v", err)
		u.installing.Store(false)
		return err
	}
	u.logf("[selfupdate] download concluido: tempPath=%s sha256=%s fromP2P=%v", tempPath, fileSha256[:12], fromP2P)

	if publicSHA256 != "" && !strings.EqualFold(fileSha256, publicSHA256) {
		u.logf("[selfupdate] ALERTA: SHA256 divergente do servidor! local=%s servidor=%s", fileSha256[:12], publicSHA256[:12])
	}

	targetVersion := extractFileVersion(tempPath)
	if targetVersion == "" {
		u.logf("[selfupdate] aviso: nao conseguiu extrair versao do arquivo, usando currentVersion=%s", currentVersion)
		targetVersion = currentVersion
	}
	u.logf("[selfupdate] versao alvo determinada: %s (current=%s)", targetVersion, currentVersion)

	if !force && compareVersions(targetVersion, currentVersion) <= 0 {
		u.logf("[selfupdate] ja esta na versao mais recente (current=%s >= target=%s)", currentVersion, targetVersion)
		u.installing.Store(false)
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp mesma versao")
		return nil
	}

	// Publica no P2P (apenas se veio de HTTP — P2P já está indexado).
	if !fromP2P {
		artifactID := "selfupdate:" + strings.ToLower(fileSha256)
		if u.OnArtifactReady != nil {
			_ = u.OnArtifactReady(ctx, tempPath, artifactID, fileSha256, targetVersion)
		}
	}

	currentCommit := strings.TrimSpace(buildinfo.Commit)
	// ── Verifica maxInstallAttempts ANTES de persistir ──
	if existing, loadErr := u.loadPendingInstallState(); loadErr == nil &&
		existing.TargetVersion == targetVersion &&
		existing.CurrentVersion == currentVersion &&
		existing.InstallAttempts >= maxInstallAttempts {
		u.logf("[selfupdate] maximo de %d tentativas ja atingido para target=%s — abortando",
			maxInstallAttempts, targetVersion)
		u.clearPendingInstallState()
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp max attempts")
		u.reportInstallFailed(ctx, existing, "max-install-attempts: "+strconv.Itoa(existing.InstallAttempts)+" tentativas")
		return fmt.Errorf("max install attempts reached for %s", targetVersion)
	}
	if err := u.persistPendingInstallState(pendingInstallState{
		CurrentVersion:  currentVersion,
		TargetVersion:   targetVersion,
		CorrelationID:   correlationID,
		RecordedAtUTC:   time.Now().UTC().Format(time.RFC3339),
		InstalledCommit: currentCommit,
	}); err != nil {
		u.installing.Store(false)
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
