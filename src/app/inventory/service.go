package inventory

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"discovery/app/appstore"
	"discovery/app/core/inventory"
	"discovery/app/core/models"
	"discovery/app/core/processutil"
	"discovery/app/core/tlsutil"
	"discovery/app/debug"
	"discovery/app/netutil"
	"discovery/app/services/hardwareid"
)

const (
	inventoryProvisioningRequiredMessage    = "inventário indisponível enquanto o agente não estiver provisionado"
	postInstallInventoryRefreshDelayDefault = 2 * time.Minute
	packageManagerTargetSeparator           = "::"
)

// AppsService defines the package manager surface used by inventory operations.
type AppsService interface {
	Install(ctx context.Context, id string) (string, error)
	Uninstall(ctx context.Context, id string) (string, error)
	Upgrade(ctx context.Context, id string) (string, error)
	UpgradeAll(ctx context.Context) (string, error)
	ListInstalled(ctx context.Context) (string, error)
}

// InventoryService exposes inventory collection operations.
type InventoryService interface {
	GetInventory(ctx context.Context) (models.InventoryReport, error)
	GetNetworkConnections(ctx context.Context) (models.NetworkConnectionsReport, error)
	CollectSoftware(ctx context.Context) ([]models.SoftwareItem, error)
	CollectStartupItems(ctx context.Context) ([]models.StartupItem, error)
	CollectScheduledTasks(ctx context.Context) ([]models.ScheduledTaskInfo, error)
	CollectListeningPorts(ctx context.Context) ([]models.ListeningPortInfo, error)
}

// InventoryCache handles caching of inventory reports.
type InventoryCache interface {
	Get() (models.InventoryReport, bool)
	Set(models.InventoryReport)
}

// DB handles inventory sync persistence.
type DB interface {
	ShouldSyncInventory(agentID string, hardwareBody, softwareBody []byte) (bool, string, error)
	SaveInventorySnapshot(agentID string, hardwareBody, softwareBody []byte) error
	// GetLatestInventory lê o snapshot mais recente do agente. Usado no caminho
	// "SYNC IGNORADO" para confirmar que já existe uma linha idêntica antes de
	// regravar ~840 KB de payload sem mudança (otimização de RAM/disco).
	GetLatestInventory(agentID string) (hardwareJSON, softwareJSON []byte, collectedAt time.Time, err error)
	UpdateLastSyncTime(key, status string) error
}

// ActivityFunc starts and ends a user-visible activity.
type ActivityFunc func(string) func()

// InventoryNotification is the inventory-level payload sent to the app notification center.
type InventoryNotification struct {
	NotificationID string
	IdempotencyKey string
	Title          string
	Message        string
	Mode           string
	Severity       string
	EventType      string
	Layout         string
	TimeoutSeconds int
	Metadata       map[string]any
}

// InventoryNotificationResponse wraps notification dispatch result.
type InventoryNotificationResponse struct {
	Accepted    bool
	Result      string
	AgentAction string
	Message     string
}

// InventoryNotificationDispatcher defines the callback injected by app layer.
type InventoryNotificationDispatcher func(InventoryNotification) InventoryNotificationResponse

// Options wires the inventory service.
type Options struct {
	Apps                 AppsService
	Inventory            InventoryService
	Cache                InventoryCache
	ResolveAllowed       func(context.Context, string) (appstore.Item, error)
	ResolveAllowedByType func(context.Context, string, string) (appstore.Item, error)
	GetCatalog           func(context.Context) (models.Catalog, error)
	// RefreshCatalog força um fetch novo do catálogo da loja (ignora cache).
	RefreshCatalog       func(context.Context) (models.Catalog, error)
	BeginActivity        ActivityFunc
	DispatchNotification InventoryNotificationDispatcher
	Logf                 func(string)
	Ctx                  func() context.Context
	DB                   DB
	DebugConfig          func() debug.Config
	Version              string
	CommitHash           string

	ShouldDeferNonCritical func() (time.Duration, bool, string)
	// HardwareIdentity retorna a identidade de hardware (TPM EK + SMBIOS UUID)
	// usada como fingerprint na Recuperação de Dispositivos. Pode ser nil.
	HardwareIdentity func() hardwareid.Info

	// PendingUpdates retorna os updates pendentes (winget upgrade + choco
	// outdated) usados para enriquecer o inventário de software com a versão
	// disponível. Best-effort: falha não impede o envio do inventário.
	PendingUpdates func(context.Context) ([]models.UpgradeItem, error)

	// InstalledPackages lista os apps instalados reconhecidos pelo gerenciador
	// (winget list: Name/Id/Version) para correlacionar o inventário de registro
	// ao Id real do pacote. Best-effort.
	InstalledPackages func(context.Context) ([]models.InstalledPackage, error)

	// PublishCustomInstallerForP2P publica no cache P2P (P2P_Temp) o instalador
	// de um app custom recém-baixado da API, tornando este agent seed para o
	// site, e devolve o caminho do artifact publicado para que a instalação
	// rode a partir dele. Best-effort: erro aqui não impede a instalação.
	// Pode ser nil (P2P off).
	PublishCustomInstallerForP2P func(ctx context.Context, packageID, version, installerPath string) (string, error)

	// CustomInstallerFromP2P tenta obter o instalador de um app custom já
	// publicado por um peer (mesma versão) antes de baixar da API. Devolve o
	// caminho local e ok=true quando conseguiu. Pode ser nil (P2P off).
	CustomInstallerFromP2P func(ctx context.Context, packageID, version string) (string, bool)
}

// Service handles inventory, installs and sync operations.
type Service struct {
	apps                   AppsService
	inventory              InventoryService
	cache                  InventoryCache
	resolveAllowed         func(context.Context, string) (appstore.Item, error)
	resolveAllowedByType   func(context.Context, string, string) (appstore.Item, error)
	getCatalog             func(context.Context) (models.Catalog, error)
	refreshCatalog         func(context.Context) (models.Catalog, error)
	beginActivity          ActivityFunc
	dispatchNotification   InventoryNotificationDispatcher
	logf                   func(string)
	ctx                    func() context.Context
	db                     DB
	debugConfig            func() debug.Config
	version                string
	commitHash             string
	shouldDeferNonCritical func() (time.Duration, bool, string)
	pendingUpdates         func(context.Context) ([]models.UpgradeItem, error)
	pendingUpdatesMu       sync.Mutex
	pendingUpdatesLast     []models.UpgradeItem
	pendingUpdatesLastAt   time.Time
	pendingUpdatesLoaded   bool
	// pendingUpdatesGoodAt é o carimbo do último scan NÃO-VAZIO bem-sucedido.
	// Diferente de pendingUpdatesLastAt, ele não é zerado pela invalidação do
	// cache: é a referência para decidir se um scan vazio é suspeito.
	pendingUpdatesGoodAt         time.Time
	installedPackages            func(context.Context) ([]models.InstalledPackage, error)
	installedPackagesMu          sync.Mutex
	installedPackagesLast        []models.InstalledPackage
	installedPackagesLastAt      time.Time
	publishCustomInstallerForP2P func(ctx context.Context, packageID, version, installerPath string) (string, error)
	customInstallerFromP2P       func(ctx context.Context, packageID, version string) (string, bool)
	installedPackagesLoaded      bool
	// installedPackagesGoodAt: idem, para o "winget list".
	installedPackagesGoodAt          time.Time
	catalogChocoMu                   sync.Mutex
	catalogChocoLast                 map[string]string
	catalogChocoAt                   time.Time
	catalogChocoLoaded               bool
	postInstallInventoryRefreshDelay time.Duration
	postInstallInventoryRefreshMu    sync.Mutex
	postInstallInventoryRefreshTimer *time.Timer
	hardwareIdentity                 func() hardwareid.Info

	// ── Lifecycle (Fase 4.1) ──
	// lifecycleCtx é o contexto de ciclo de vida, cancelado no Shutdown.
	lifecycleCtx     context.Context
	lifecycleCancel  context.CancelFunc
	lifecycleStarted bool
}

// NewService builds an inventory service.
func NewService(opts Options) *Service {
	logf := opts.Logf
	if logf == nil {
		logf = func(string) {}
	}
	return &Service{
		apps:                             opts.Apps,
		inventory:                        opts.Inventory,
		cache:                            opts.Cache,
		resolveAllowed:                   opts.ResolveAllowed,
		resolveAllowedByType:             opts.ResolveAllowedByType,
		getCatalog:                       opts.GetCatalog,
		refreshCatalog:                   opts.RefreshCatalog,
		beginActivity:                    opts.BeginActivity,
		dispatchNotification:             opts.DispatchNotification,
		logf:                             logf,
		ctx:                              opts.Ctx,
		db:                               normalizeInventoryDB(opts.DB),
		debugConfig:                      opts.DebugConfig,
		version:                          opts.Version,
		commitHash:                       opts.CommitHash,
		shouldDeferNonCritical:           opts.ShouldDeferNonCritical,
		pendingUpdates:                   opts.PendingUpdates,
		installedPackages:                opts.InstalledPackages,
		publishCustomInstallerForP2P:     opts.PublishCustomInstallerForP2P,
		customInstallerFromP2P:           opts.CustomInstallerFromP2P,
		postInstallInventoryRefreshDelay: postInstallInventoryRefreshDelayDefault,
		hardwareIdentity:                 opts.HardwareIdentity,
	}
}

// SetDB updates the persistence backend used by inventory sync routines.
func (s *Service) SetDB(db DB) {
	if s == nil {
		return
	}
	s.db = normalizeInventoryDB(db)
}

// Startup prepara o contexto de ciclo de vida do inventory.Service.
// É chamado pelo *App em seu startup (após DB estar disponível).
//
// NOTA: Este método NÃO implementa a interface ServiceStartup do Wails v3
// (que requer application.ServiceOptions) para evitar dependência do pacote
// inventory (domínio puro) no Wails. O *App chama este método explicitamente.
func (s *Service) Startup(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.lifecycleStarted {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	s.lifecycleCtx = ctx
	s.lifecycleCancel = cancel
	s.lifecycleStarted = true
	log.Println("[inventory] Startup concluído")
	return nil
}

// Shutdown cancela o contexto de ciclo de vida do inventory.Service.
// Para o timer de refresh pós-instalação se ativo.
// É chamado pelo *App em seu shutdown.
func (s *Service) Shutdown() error {
	if s == nil || !s.lifecycleStarted {
		return nil
	}
	// Para o timer de refresh pós-instalação se ativo.
	if s.postInstallInventoryRefreshTimer != nil {
		s.postInstallInventoryRefreshTimer.Stop()
	}
	if s.lifecycleCancel != nil {
		s.lifecycleCancel()
	}
	s.lifecycleStarted = false
	log.Println("[inventory] Shutdown concluído")
	return nil
}

// LifecycleCtx retorna o contexto de ciclo de vida do service.
// Retorna context.Background() se Startup ainda não foi chamado.
func (s *Service) LifecycleCtx() context.Context {
	if s == nil || s.lifecycleCtx == nil {
		return context.Background()
	}
	return s.lifecycleCtx
}

func normalizeInventoryDB(db DB) DB {
	if db == nil {
		return nil
	}
	v := reflect.ValueOf(db)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return nil
	}
	return db
}

// GetCatalog resolves the package catalog.
func (s *Service) GetCatalog() (models.Catalog, error) {
	if s.getCatalog == nil {
		return models.Catalog{}, fmt.Errorf("catálogo indisponível")
	}
	ctx := s.ctx()
	return s.getCatalog(ctx)
}

// RefreshCatalog força um fetch novo do catálogo da loja no servidor. Sem o
// callback (modo sem app-store) cai no GetCatalog normal.
func (s *Service) RefreshCatalog() (models.Catalog, error) {
	if s.refreshCatalog == nil {
		return s.GetCatalog()
	}
	return s.refreshCatalog(s.ctx())
}

// Install installs the selected package.
func (s *Service) Install(id string) (string, error) {
	done := s.beginActivity("instalação")
	if done != nil {
		defer done()
	}

	packageID := strings.TrimSpace(id)
	if packageID == "" {
		return "", fmt.Errorf("id do pacote é obrigatório")
	}

	s.logf("[install " + packageID + "] " + time.Now().Format("15:04:05"))
	correlationID := fmt.Sprintf("appstore-install-%s-%d", sanitizeNotificationIDPart(packageID), time.Now().UnixNano())

	// Um único install_start: emitir um evento por fase (download + instalacao)
	// gerava dois toasts idênticos para o usuário. A fase de download é coberta
	// pela própria execução do instalador (winget/chocolatey baixam e instalam
	// em uma chamada), então consolidamos em uma notificação.
	allowed, err := s.resolveAllowed(s.ctx(), packageID)
	if err != nil {
		s.logf("[install blocked] " + err.Error())
		s.emitInstallNotification(correlationID, packageID, "install_start", "download", "in_progress", "Instalação em andamento", "Baixando aplicativo...", nil)
		s.emitInstallNotification(correlationID, packageID, "install_failed", "precheck", "failed", "Instalação bloqueada", "Pacote não autorizado para este agente.", map[string]any{
			"error": err.Error(),
		})
		return "", err
	}

	// phase é um identifier técnico (não exibido ao usuário) — padronizado sem acento
	// para evitar dependência de locale em comparações e logs.
	s.emitInstallNotification(correlationID, packageID, "install_start", "instalacao", "in_progress", "Instalação em andamento", "Baixando e executando instalador...", nil)

	var out string
	switch normalizeAppStoreInstallationType(allowed.InstallationType) {
	case string(appstore.InstallationWinget):
		out, err = s.apps.Install(s.ctx(), packageID)
	case string(appstore.InstallationChocolatey):
		out, err = s.runChocolatey(s.ctx(), "install", packageID)
	case string(appstore.InstallationCustom):
		// App custom (cadastro manual no servidor): executa o InstallCommand
		// registrado no catálogo. O comando só chega aqui após o pacote estar
		// autorizado (resolveAllowed). Se o servidor ainda não publicou o
		// comando, retorna erro claro em vez de "não suportado" genérico.
		out, err = s.runCustomInstallCommand(s.ctx(), allowed, packageID)
	default:
		err = fmt.Errorf("installationType %q não suportado", allowed.InstallationType)
	}
	s.logf(out)

	if err != nil {
		// Falhas sem saída (ex.: download recusado) não deixavam rastro nenhum
		// no log local — só a notificação, que carrega o erro embrulhado
		// ("exit status 1") e esconde o motivo real.
		s.logf(fmt.Sprintf("[install %s] falhou: %v", packageID, err))

		details := map[string]any{"error": err.Error()}
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			details["output"] = truncateNotificationOutput(trimmed)
		}
		s.emitInstallNotification(correlationID, packageID, "install_failed", "instalacao", "failed", "Falha na instalação", "Não foi possível concluir a instalação do aplicativo.", details)
		return out, err
	}

	s.emitInstallNotification(correlationID, packageID, "install_end", "validacao", "completed", "Instalação concluída", "Aplicativo instalado com sucesso.", nil)
	if hasRebootSignal(out) {
		s.emitInstallNotification(correlationID, packageID, "reboot_required", "reinício", "completed", "Reinício necessário", "Reinicie o computador para concluir a instalação.", nil)
	}
	s.scheduleInventoryRefreshAfterPackageChange("install", packageID)

	return out, nil
}

// Uninstall removes a package.
func (s *Service) Uninstall(id string) (string, error) {
	done := s.beginActivity("desinstalação")
	if done != nil {
		defer done()
	}

	packageID := strings.TrimSpace(id)
	if packageID == "" {
		return "", fmt.Errorf("id do pacote é obrigatório")
	}

	s.logf("[uninstall " + packageID + "] " + time.Now().Format("15:04:05"))

	allowed, err := s.resolveAllowed(s.ctx(), packageID)
	if err != nil {
		s.logf("[uninstall blocked] " + err.Error())
		return "", err
	}

	var out string
	switch normalizeAppStoreInstallationType(allowed.InstallationType) {
	case string(appstore.InstallationWinget):
		out, err = s.apps.Uninstall(s.ctx(), packageID)
	case string(appstore.InstallationChocolatey):
		out, err = s.runChocolatey(s.ctx(), "uninstall", packageID)
	case string(appstore.InstallationCustom):
		// Comando de remoção opcional registrado no servidor. Sem ele não há
		// como remover com segurança (não inventamos um comando genérico).
		uninstallCommand := strings.TrimSpace(allowed.UninstallCommand)
		if uninstallCommand == "" {
			err = fmt.Errorf("app custom %q sem comando de desinstalação registrado no servidor", packageID)
			break
		}
		runCtx := s.ctx()
		if runCtx == nil {
			runCtx = context.Background()
		}
		runCtx, cancel := context.WithTimeout(runCtx, customInstallCommandTimeout)
		defer cancel()
		// O tipo do comando de remoção é independente do de instalação
		// (UninstallCommandKind), permitindo .cmd/.ps1 também na remoção.
		out, err = s.runCustomShellCommandKind(runCtx, uninstallCommand, "", allowed.UninstallCommandKind)
	default:
		err = fmt.Errorf("installationType %q não suportado", allowed.InstallationType)
	}
	s.logf(out)
	if err != nil {
		// Mesmo motivo do Install: sem isto a falha fica sem rastro local.
		s.logf(fmt.Sprintf("[uninstall %s] falhou: %v", packageID, err))
	}
	if err == nil {
		s.scheduleInventoryRefreshAfterPackageChange("uninstall", packageID)
	}
	return out, err
}

// Upgrade upgrades a package.
func (s *Service) Upgrade(id string) (string, error) {
	done := s.beginActivity("atualização")
	if done != nil {
		defer done()
	}
	sourceHint, packageID := splitPackageManagerTarget(id)
	if packageID == "" {
		return "", fmt.Errorf("id do pacote é obrigatório")
	}

	logTarget := packageID
	if sourceHint != "" {
		logTarget = sourceHint + packageManagerTargetSeparator + packageID
	}
	s.logf("[upgrade " + logTarget + "] " + time.Now().Format("15:04:05"))

	allowed, err := s.resolveAllowedPackage(s.ctx(), sourceHint, packageID)
	if err != nil {
		s.logf("[upgrade blocked] " + err.Error())
		return "", err
	}

	var out string
	switch normalizeAppStoreInstallationType(allowed.InstallationType) {
	case string(appstore.InstallationWinget):
		out, err = s.apps.Upgrade(s.ctx(), packageID)
	case string(appstore.InstallationChocolatey):
		out, err = s.runChocolatey(s.ctx(), "upgrade", packageID)
	case string(appstore.InstallationCustom):
		// Pacote custom não tem "comando de upgrade" separado: atualizar é
		// reexecutar o instalador publicado (mesmo fluxo do install, com
		// download + verificação de tamanho/checksum).
		out, err = s.runCustomInstallCommand(s.ctx(), allowed, packageID)
	default:
		err = fmt.Errorf("installationType %q não suportado", allowed.InstallationType)
	}
	s.logf(out)
	if err == nil {
		s.scheduleInventoryRefreshAfterPackageChange("upgrade", packageID)
	}
	return out, err
}

// UpgradeFromSource atualiza um pacote instalado sem consultar a política da
// loja. Usado pelo comando remoto de atualização (o servidor aplica o gate de
// confirmação). Continua passando pelo router de gerenciadores, preservando a
// otimização P2P e os switches silenciosos do catálogo.
func (s *Service) UpgradeFromSource(installationType, packageID string) (string, error) {
	done := s.beginActivity("atualização")
	if done != nil {
		defer done()
	}

	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return "", fmt.Errorf("id do pacote é obrigatório")
	}

	s.logf("[software-update " + packageID + "] " + time.Now().Format("15:04:05"))

	var out string
	var err error
	switch normalizeAppStoreInstallationType(installationType) {
	case string(appstore.InstallationWinget):
		out, err = s.apps.Upgrade(s.ctx(), packageID)
	case string(appstore.InstallationChocolatey):
		out, err = s.runChocolatey(s.ctx(), "upgrade", packageID)
	case string(appstore.InstallationCustom):
		// Custom exige o Item (comando/arquivo), que vem da política da loja.
		allowed, resolveErr := s.resolveAllowed(s.ctx(), packageID)
		if resolveErr != nil {
			err = resolveErr
			break
		}
		if normalizeAppStoreInstallationType(allowed.InstallationType) != string(appstore.InstallationCustom) {
			err = fmt.Errorf("pacote %q não é do tipo Custom", packageID)
			break
		}
		out, err = s.runCustomInstallCommand(s.ctx(), allowed, packageID)
	default:
		err = fmt.Errorf("installationType %q não suportado para atualização remota", installationType)
	}
	s.logf(out)
	if err == nil {
		s.scheduleInventoryRefreshAfterPackageChange("upgrade", packageID)
	}
	return out, err
}

// UninstallFromSource desinstala um pacote instalado sem consultar a política
// da loja. Usado pelo comando remoto de desinstalação do dashboard (remoção não
// é bloqueada pela política). Passa pelo router de gerenciadores (winget/choco).
func (s *Service) UninstallFromSource(installationType, packageID string) (string, error) {
	done := s.beginActivity("desinstalação")
	if done != nil {
		defer done()
	}

	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return "", fmt.Errorf("id do pacote é obrigatório")
	}

	s.logf("[software-uninstall " + packageID + "] " + time.Now().Format("15:04:05"))

	var out string
	var err error
	switch normalizeAppStoreInstallationType(installationType) {
	case string(appstore.InstallationWinget):
		out, err = s.apps.Uninstall(s.ctx(), packageID)
	case string(appstore.InstallationChocolatey):
		out, err = s.runChocolatey(s.ctx(), "uninstall", packageID)
	case string(appstore.InstallationCustom):
		allowed, resolveErr := s.resolveAllowed(s.ctx(), packageID)
		if resolveErr != nil {
			err = resolveErr
			break
		}
		uninstallCommand := strings.TrimSpace(allowed.UninstallCommand)
		if uninstallCommand == "" {
			err = fmt.Errorf("app custom %q sem comando de desinstalação registrado no servidor", packageID)
			break
		}
		// Remoção remota de app custom: mesma semântica de tipo de comando
		// (UninstallCommandKind) usada pelo botão da loja.
		out, err = s.runCustomShellCommandKind(s.ctx(), uninstallCommand, "", allowed.UninstallCommandKind)
	default:
		err = fmt.Errorf("installationType %q não suportado para desinstalação remota", installationType)
	}
	s.logf(out)
	if err == nil {
		s.scheduleInventoryRefreshAfterPackageChange("uninstall", packageID)
	}
	return out, err
}

// UpgradeAll upgrades all packages.
func (s *Service) UpgradeAll() (string, error) {
	done := s.beginActivity("atualização em lote")
	if done != nil {
		defer done()
	}
	s.logf("[upgrade --all] " + time.Now().Format("15:04:05"))
	out, err := s.apps.UpgradeAll(s.ctx())
	s.logf(out)
	if err == nil {
		s.scheduleInventoryRefreshAfterPackageChange("upgrade-all", "")
	}
	return out, err
}

// ListInstalled lists installed packages.
func (s *Service) ListInstalled() (string, error) {
	done := s.beginActivity("listagem de instalados")
	if done != nil {
		defer done()
	}
	out, err := s.apps.ListInstalled(s.ctx())
	s.logf("[list] " + time.Now().Format("15:04:05"))
	s.logf(out)
	return out, err
}

func (s *Service) resolveAllowedPackage(ctx context.Context, sourceHint, packageID string) (appstore.Item, error) {
	if sourceHint != "" && s.resolveAllowedByType != nil {
		return s.resolveAllowedByType(ctx, sourceHint, packageID)
	}
	if s.resolveAllowed == nil {
		return appstore.Item{}, fmt.Errorf("resolução de pacote indisponível")
	}
	return s.resolveAllowed(ctx, packageID)
}

func (s *Service) scheduleInventoryRefreshAfterPackageChange(action, packageID string) {
	delay := s.postInstallInventoryRefreshDelay
	if delay <= 0 {
		return
	}

	s.postInstallInventoryRefreshMu.Lock()
	if s.postInstallInventoryRefreshTimer != nil {
		s.postInstallInventoryRefreshTimer.Stop()
	}
	s.postInstallInventoryRefreshTimer = time.AfterFunc(delay, s.runDelayedInventoryRefreshAfterPackageChange)
	s.postInstallInventoryRefreshMu.Unlock()

	action = strings.TrimSpace(strings.ToLower(action))
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		s.logf(fmt.Sprintf("[inventory-refresh] refresh agendado em %s apos %s", delay.Round(time.Second), action))
		return
	}
	s.logf(fmt.Sprintf("[inventory-refresh] refresh agendado em %s apos %s %s", delay.Round(time.Second), action, packageID))
}

func (s *Service) runDelayedInventoryRefreshAfterPackageChange() {
	s.postInstallInventoryRefreshMu.Lock()
	s.postInstallInventoryRefreshTimer = nil
	s.postInstallInventoryRefreshMu.Unlock()

	if err := s.requireProvisionedInventory(); err != nil {
		s.logf("[inventory-refresh] ignorado: " + err.Error())
		return
	}
	if s.inventory == nil {
		s.logf("[inventory-refresh] ignorado: provider de inventário indisponível")
		return
	}

	ctx := context.Background()
	if s.ctx != nil {
		ctx = s.ctx()
	}

	report, err := s.collectInventoryWithHeartbeat(ctx)
	if err != nil {
		s.logf("[inventory-refresh] falha ao atualizar inventário: " + err.Error())
		return
	}
	if s.cache != nil {
		s.cache.Set(report)
	}

	// Invalida o cache de updates pendentes: sem isso o upload reaproveitaria a
	// lista anterior e ainda reportaria como disponível o update recém-aplicado.
	s.invalidatePendingUpdates()

	// Envia o inventário atualizado ao servidor (best-effort). Sem este upload,
	// o dashboard só enxergaria a mudança no próximo sync periódico (~6h) —
	// justamente o que o botão remoto de atualização precisa refletir.
	s.SyncInventoryOnStartup(ctx, report)

	s.logf("[inventory-refresh] inventário atualizado e sincronizado após alterações de software")
}

// InvalidatePendingUpdatesCache força o próximo SyncInventoryOnStartup a
// refazer o scan de updates (winget upgrade + choco outdated) e o "winget list",
// em vez de reaproveitar o cache de 10 minutos. Usado pelo refresh manual de
// software do dashboard ("Atualizar" no detalhe do agente).
func (s *Service) InvalidatePendingUpdatesCache() {
	if s == nil {
		return
	}
	s.invalidatePendingUpdates()
	s.logf("[inventory-refresh] cache de updates/instalados invalidado (refresh manual)")
}

// ScheduleInventoryRefreshAfterPackageChange agenda o refresh de inventário com
// debounce após uma alteração de pacote feita FORA do InventorySvc (ex.: tarefas
// de automação / Loja de aplicativos, que executam choco/winget diretamente).
// Reusa o MESMO caminho do update remoto: re-coleta o inventário, invalida o
// cache de updates/instalados e envia ao servidor. Deve ser chamado só no
// sucesso — em falha o pacote não mudou e não há o que reconciliar.
func (s *Service) ScheduleInventoryRefreshAfterPackageChange(action, packageID string) {
	if s == nil {
		return
	}
	s.scheduleInventoryRefreshAfterPackageChange(action, packageID)
}

func (s *Service) invalidatePendingUpdates() {
	// IMPORTANTE: invalidar NÃO descarta a última lista conhecida — apenas
	// expira o cache (lastAt = zero) para forçar um novo scan. A lista anterior
	// permanece como fallback: se o novo scan vier vazio/errar, o load ainda
	// pode reutilizá-la em vez de enviar "sem updates" e limpar o servidor.
	s.pendingUpdatesMu.Lock()
	s.pendingUpdatesLastAt = time.Time{}
	s.pendingUpdatesMu.Unlock()

	// O índice do "winget list" também muda após instalar/remover/atualizar.
	s.installedPackagesMu.Lock()
	s.installedPackagesLastAt = time.Time{}
	s.installedPackagesMu.Unlock()

	s.catalogChocoMu.Lock()
	s.catalogChocoLoaded = false
	s.catalogChocoAt = time.Time{}
	s.catalogChocoMu.Unlock()
}

// catalogChocoCacheTTL evita remapear o catálogo da loja a cada sincronização.
const catalogChocoCacheTTL = 30 * time.Minute

// loadCatalogChocoIndex mapeia nome de exibição → packageId dos itens Chocolatey
// do catálogo da loja. É o elo entre o nome do registro e o Id real do pacote
// choco (o "choco list" só devolve o Id, sem nome de exibição). Best-effort.
func (s *Service) loadCatalogChocoIndex(
	ctx context.Context,
	installed []models.InstalledPackage,
	pending []models.UpgradeItem,
) map[string]string {
	if s.getCatalog == nil {
		return nil
	}

	// Sem nenhum indício de Chocolatey (instalado ou com update pendente) o
	// catálogo não agrega: evita remapear o catálogo inteiro em máquinas sem choco.
	if !hasChocolateySignal(installed, pending) {
		return nil
	}

	s.catalogChocoMu.Lock()
	cached := s.catalogChocoLast
	cachedAt := s.catalogChocoAt
	loaded := s.catalogChocoLoaded
	s.catalogChocoMu.Unlock()

	if loaded && time.Since(cachedAt) < catalogChocoCacheTTL {
		return cached
	}

	catalog, err := s.getCatalog(ctx)
	if err != nil {
		s.logf("[agent-sync] aviso: catálogo indisponível para correlacionar Chocolatey: " + err.Error())
		if loaded {
			return cached
		}
		return nil
	}

	index := make(map[string]string, len(catalog.Packages))
	for _, item := range catalog.Packages {
		if !strings.Contains(strings.ToLower(item.InstallationType), "choco") {
			continue
		}
		key := normalizeUpdateKey(item.Name)
		if key == "" {
			continue
		}
		if _, exists := index[key]; !exists {
			index[key] = strings.TrimSpace(item.ID)
		}
	}

	s.catalogChocoMu.Lock()
	s.catalogChocoLast = index
	s.catalogChocoAt = time.Now()
	s.catalogChocoLoaded = true
	s.catalogChocoMu.Unlock()
	return index
}

func hasChocolateySignal(installed []models.InstalledPackage, pending []models.UpgradeItem) bool {
	for _, pkg := range installed {
		if strings.Contains(strings.ToLower(pkg.Source), "choco") {
			return true
		}
	}
	for _, update := range pending {
		if strings.Contains(strings.ToLower(update.Source), "choco") {
			return true
		}
	}
	return false
}

func (s *Service) inventoryProvisioned() bool {
	if s == nil || s.debugConfig == nil {
		return false
	}
	return s.debugConfig().IsProvisioned()
}

func (s *Service) requireProvisionedInventory() error {
	if s.inventoryProvisioned() {
		return nil
	}
	return fmt.Errorf(inventoryProvisioningRequiredMessage)
}

func (s *Service) collectInventoryWithHeartbeat(ctx context.Context) (models.InventoryReport, error) {
	return s.inventory.GetInventory(ctx)
}

func (s *Service) collectNetworkConnectionsWithHeartbeat(ctx context.Context) (models.NetworkConnectionsReport, error) {
	return s.inventory.GetNetworkConnections(ctx)
}

// GetInventory returns cached inventory or collects if needed.
func (s *Service) GetInventory() (models.InventoryReport, error) {
	done := s.beginActivity("coleta de inventário")
	if done != nil {
		defer done()
	}
	if s.cache != nil {
		if cached, ok := s.cache.Get(); ok {
			return cached, nil
		}
	}
	if err := s.requireProvisionedInventory(); err != nil {
		return models.InventoryReport{}, err
	}

	report, err := s.collectInventoryWithHeartbeat(s.ctx())
	if err != nil {
		return models.InventoryReport{}, err
	}
	if s.cache != nil {
		s.cache.Set(report)
	}
	return report, nil
}

// RefreshInventory collects inventory and refreshes cache.
func (s *Service) RefreshInventory() (models.InventoryReport, error) {
	done := s.beginActivity("atualização de inventário")
	if done != nil {
		defer done()
	}
	if err := s.requireProvisionedInventory(); err != nil {
		return models.InventoryReport{}, err
	}
	report, err := s.collectInventoryWithHeartbeat(s.ctx())
	if err != nil {
		return models.InventoryReport{}, err
	}
	if s.cache != nil {
		s.cache.Set(report)
	}
	return report, nil
}

// RefreshNetworkConnections collects only listening ports and open sockets.
func (s *Service) RefreshNetworkConnections() (models.NetworkConnectionsReport, error) {
	done := s.beginActivity("atualização de conexões de rede")
	if done != nil {
		defer done()
	}
	if err := s.requireProvisionedInventory(); err != nil {
		return models.NetworkConnectionsReport{}, err
	}
	report, err := s.collectNetworkConnectionsWithHeartbeat(s.ctx())
	if err != nil {
		return models.NetworkConnectionsReport{}, err
	}
	if s.cache != nil {
		if cached, ok := s.cache.Get(); ok {
			cached.ListeningPorts = report.ListeningPorts
			cached.OpenSockets = report.OpenSockets
			s.cache.Set(cached)
		}
	}
	return report, nil
}

// RefreshSoftware collects only installed software.
func (s *Service) RefreshSoftware() ([]models.SoftwareItem, error) {
	done := s.beginActivity("atualização de softwares")
	if done != nil {
		defer done()
	}
	if err := s.requireProvisionedInventory(); err != nil {
		return []models.SoftwareItem{}, err
	}
	software, err := s.inventory.CollectSoftware(s.ctx())
	if err != nil {
		return []models.SoftwareItem{}, err
	}
	if s.cache != nil {
		if cached, ok := s.cache.Get(); ok {
			cached.Software = software
			s.cache.Set(cached)
		}
	}
	return software, nil
}

// RefreshStartupItems collects only startup items.
func (s *Service) RefreshStartupItems() ([]models.StartupItem, error) {
	done := s.beginActivity("atualizacao de itens de inicializacao")
	if done != nil {
		defer done()
	}
	if err := s.requireProvisionedInventory(); err != nil {
		return []models.StartupItem{}, err
	}
	startupItems, err := s.inventory.CollectStartupItems(s.ctx())
	if err != nil {
		return []models.StartupItem{}, err
	}
	if s.cache != nil {
		if cached, ok := s.cache.Get(); ok {
			cached.StartupItems = startupItems
			s.cache.Set(cached)
		}
	}
	return startupItems, nil
}

// RefreshListeningPorts collects only listening ports.
func (s *Service) RefreshListeningPorts() ([]models.ListeningPortInfo, error) {
	done := s.beginActivity("atualizacao de portas em escuta")
	if done != nil {
		defer done()
	}
	if err := s.requireProvisionedInventory(); err != nil {
		return []models.ListeningPortInfo{}, err
	}
	ports, err := s.inventory.CollectListeningPorts(s.ctx())
	if err != nil {
		return []models.ListeningPortInfo{}, err
	}
	if s.cache != nil {
		if cached, ok := s.cache.Get(); ok {
			cached.ListeningPorts = ports
			s.cache.Set(cached)
		}
	}
	return ports, nil
}

// GetOsqueryStatus exposes osquery status info.
func (s *Service) GetOsqueryStatus() (models.OsqueryStatus, error) {
	return inventory.GetOsqueryStatus(), nil
}

// InstallOsquery installs osquery if needed.
func (s *Service) InstallOsquery() (string, error) {
	if err := s.requireProvisionedInventory(); err != nil {
		return "", err
	}
	status := inventory.GetOsqueryStatus()
	if status.Installed {
		if status.Path != "" {
			return "osquery ja instalado em " + status.Path, nil
		}
		return "osquery ja instalado", nil
	}
	allowed, err := s.resolveAllowed(s.ctx(), status.SuggestedPackageID)
	if err != nil {
		return "", err
	}

	var out string
	switch normalizeAppStoreInstallationType(allowed.InstallationType) {
	case string(appstore.InstallationWinget):
		out, err = s.apps.Install(s.ctx(), status.SuggestedPackageID)
	case string(appstore.InstallationChocolatey):
		out, err = s.runChocolatey(s.ctx(), "install", status.SuggestedPackageID)
	default:
		err = fmt.Errorf("installationType %q nao suportado", allowed.InstallationType)
	}
	if err != nil {
		return out, err
	}
	inventory.InvalidateOsqueryBinaryCache()
	return out, nil
}

// customInstallCommandTimeout limita a execução do InstallCommand de apps
// custom (instaladores podem ser lentos; 30min dá folga sem travar a UI).
const customInstallCommandTimeout = 30 * time.Minute

// runCustomInstallCommand executa o InstallCommand de um app custom (cadastro
// manual no servidor). Quando o servidor publica um instalador (FileURL), o
// arquivo é baixado com o token do agent e, se ExtractZip estiver marcado (ou a
// extensão for .zip), extraído no host. Os placeholders {{installer}} e
// {{installerDir}} do comando são substituídos pelo caminho real. Segue o
// padrão do executor de automação (core/automation/executor.go). Só é chamado
// após FindAllowedPackage autorizar o pacote — sem allow no escopo do agent não
// há execução.
func (s *Service) runCustomInstallCommand(ctx context.Context, allowed appstore.Item, packageID string) (string, error) {
	command := strings.TrimSpace(allowed.InstallCommand)
	if command == "" {
		return "", fmt.Errorf("app custom %q sem comando de instalação registrado no servidor", packageID)
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(runCtx, customInstallCommandTimeout)
	defer cancel()

	var installerPath, workDir string
	if strings.TrimSpace(allowed.FileURL) == "" && commandReferencesInstaller(command) {
		// Sem instalador publicado o placeholder nunca é resolvido e o shell falha
		// com "{{installer}} não é reconhecido como um comando..." — mensagem que
		// escondia a causa real (pacote sem arquivo publicado no servidor).
		return "", fmt.Errorf(
			"app custom %q usa {{installer}}/{{installerDir}} no comando, mas nenhum instalador foi publicado para este pacote no servidor",
			packageID)
	}
	if strings.TrimSpace(allowed.FileURL) != "" {
		downloaded := ""
		fromP2P := false

		// P2P primeiro: se algum agent do site já publicou este instalador na
		// mesma versão (ou se já está no cache local), baixar da LAN evita
		// repetir GBs de download da API.
		if s.customInstallerFromP2P != nil {
			if path, ok := s.customInstallerFromP2P(runCtx, packageID, allowed.Version); ok {
				downloaded, fromP2P = path, true
				s.logf(fmt.Sprintf("[install %s] instalador obtido do cache/rede P2P: %s", packageID, path))
			}
		}

		if !fromP2P {
			path, err := s.downloadCustomAsset(runCtx, allowed, packageID)
			if err != nil {
				return "", err
			}
			downloaded = path
			// NÃO remove a pasta imediatamente: instaladores podem disparar um
			// processo filho que continua lendo os arquivos após o cmd retornar.
			// A limpeza é agendada com folga (e sobras antigas são varridas).
			s.scheduleCustomAssetCleanup(filepath.Dir(path))

			// Vira seed: publica o instalador no cache P2P (P2P_Temp) para os
			// demais agents do site e passa a instalar a partir do artifact
			// publicado, deixando a cópia temporária apenas como sobra a limpar.
			// Best-effort — falha aqui não impede a instalação.
			if s.publishCustomInstallerForP2P != nil {
				publishedPath, pubErr := s.publishCustomInstallerForP2P(runCtx, packageID, allowed.Version, path)
				if pubErr != nil {
					s.logf(fmt.Sprintf("[install %s] aviso: falha ao publicar instalador no P2P: %v", packageID, pubErr))
				} else if strings.TrimSpace(publishedPath) != "" {
					downloaded, fromP2P = publishedPath, true
				}
			}
		}

		installerPath = downloaded
		workDir = filepath.Dir(downloaded)

		if allowed.ExtractZip || strings.EqualFold(filepath.Ext(downloaded), ".zip") {
			// A pasta do cache P2P não é área de trabalho: extrair ali deixaria
			// lixo dentro do diretório de artifacts publicados.
			if fromP2P {
				if tempDir, err := os.MkdirTemp("", "discovery-custom-*"); err == nil {
					s.scheduleCustomAssetCleanup(tempDir)
					workDir = tempDir
				} else {
					s.logf(fmt.Sprintf("[install %s] aviso: falha ao criar pasta de extração: %v", packageID, err))
				}
			}
			extractedDir := filepath.Join(workDir, "extracted")
			if err := extractZipArchive(downloaded, extractedDir); err != nil {
				return "", err
			}
			workDir = resolveExtractionRoot(extractedDir)
			s.logf(fmt.Sprintf("[install %s] ZIP extraido em %s", packageID, workDir))
		}
	}

	command = applyCustomCommandPlaceholders(command, installerPath, workDir)
	return s.runCustomShellCommandKind(runCtx, command, workDir, allowed.InstallCommandKind)
}

// runCustomShellCommand executa um comando de texto livre de pacote custom via
// cmd /C, com janela oculta e timeout (compatibilidade: equivale ao tipo
// Command). Comando vazio é rejeitado.
func (s *Service) runCustomShellCommand(ctx context.Context, command, workDir string) (string, error) {
	return s.runCustomShellCommandKind(ctx, command, workDir, appstore.CommandKindCommand)
}

// runCustomShellCommandKind respeita o tipo de comando configurado no servidor
// (contrato §1.3): Command (0) → `cmd /C`; CmdScript (1) → o texto é gravado num
// `.cmd` no workdir e executado; PowerShellScript (2) → gravado num `.ps1` e
// executado com `powershell -NoProfile -ExecutionPolicy Bypass -File`.
//
// Placeholders já vêm substituídos pelo chamador. Timeout, cmd.Dir, janela
// oculta e CombinedOutput permanecem iguais aos do modo comando.
func (s *Service) runCustomShellCommandKind(ctx context.Context, command, workDir string, kind int) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("comando custom vazio")
	}

	spec, err := buildCustomCommandSpec(kind, command, workDir)
	if err != nil {
		return "", err
	}

	if spec.scriptBody != "" {
		if err := os.WriteFile(spec.scriptPath, []byte(spec.scriptBody), 0o600); err != nil {
			return "", fmt.Errorf("falha ao gravar script custom em %s: %w", spec.scriptPath, err)
		}
		s.customDetectionLogf("script custom (%s) gravado em %s", spec.label, spec.scriptPath)
		// Sem workdir o script vai para o %TEMP% do sistema e nenhuma limpeza
		// posterior o remove — desinstalação/upgrade remoto deixavam .cmd/.ps1
		// acumulando ali. Com workdir (instalação) o arquivo fica junto do
		// instalador na pasta temporária, que é limpa depois; esse
		// comportamento é coberto por teste e permanece.
		if strings.TrimSpace(workDir) == "" {
			defer func(path string) { _ = os.Remove(path) }(spec.scriptPath)
		}
	}

	cmd := exec.CommandContext(ctx, spec.argv[0], spec.argv[1:]...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return text, fmt.Errorf("comando custom excedeu %s", customInstallCommandTimeout)
		}
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("erro executando comando custom: %w", err)
	}
	return text, nil
}

// customCommandSpec descreve como um comando/script custom será executado:
// argv pronto para exec.CommandContext e, nos modos de script, o corpo a gravar.
type customCommandSpec struct {
	argv       []string
	scriptPath string
	scriptBody string
	label      string
}

// buildCustomCommandSpec monta a invocação do comando custom conforme o tipo.
// Função pura (além de resolver o caminho do arquivo de script) para permitir
// teste sem executar nada.
func buildCustomCommandSpec(kind int, command, workDir string) (customCommandSpec, error) {
	switch kind {
	case appstore.CommandKindCmdScript:
		path := filepath.Join(customScriptDir(workDir), "discovery-custom-install.cmd")
		// O caminho vai SEM aspas no argv: o exec do Go já cita o argumento
		// quando ele contém espaços (o temp do Windows costuma ter o nome do
		// usuário). Aspas manuais viravam parte do nome e o cmd /C falhava com
		// "'\"C:\...\x.cmd\"' não é reconhecido como um comando interno".
		return customCommandSpec{
			argv:       []string{"cmd", "/C", path},
			scriptPath: path,
			scriptBody: command,
			label:      "cmd",
		}, nil
	case appstore.CommandKindPowerShellScript:
		path := filepath.Join(customScriptDir(workDir), "discovery-custom-install.ps1")
		return customCommandSpec{
			argv:       []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path},
			scriptPath: path,
			scriptBody: command,
			label:      "powershell",
		}, nil
	case appstore.CommandKindCommand:
		return customCommandSpec{
			argv:  []string{"cmd", "/C", command},
			label: "comando",
		}, nil
	default:
		// Tipo desconhecido (servidor mais novo): mantém o comportamento
		// retrocompatível de comando único em vez de falhar a instalação.
		return customCommandSpec{
			argv:  []string{"cmd", "/C", command},
			label: "comando",
		}, nil
	}
}

// customScriptDir escolhe onde gravar o arquivo de script: a pasta de trabalho
// (que costuma conter o instalador baixado e é limpa depois) ou o temp do
// sistema quando não há workdir.
func customScriptDir(workDir string) string {
	if dir := strings.TrimSpace(workDir); dir != "" {
		return dir
	}
	return os.TempDir()
}

// commandReferencesInstaller informa se o comando depende dos placeholders do
// instalador. Usa os MESMOS tokens (case-sensitive) de
// applyCustomCommandPlaceholders: tokens com caixa diferente não são
// substituídos lá e continuam caindo no erro genérico do shell.
func commandReferencesInstaller(command string) bool {
	for _, placeholder := range []string{"{{installer}}", "{{installerDir}}", "%INSTALLER%", "%INSTALLER_DIR%"} {
		if strings.Contains(command, placeholder) {
			return true
		}
	}
	return false
}

// applyCustomCommandPlaceholders troca os marcadores do comando pelos caminhos
// reais do arquivo baixado ({{installer}}/`%INSTALLER%`) e da pasta de trabalho
// ({{installerDir}}/`%INSTALLER_DIR%`). Sem arquivo baixado, o comando é
// devolvido intacto.
func applyCustomCommandPlaceholders(command, installerPath, workDir string) string {
	if strings.TrimSpace(installerPath) == "" {
		return command
	}

	replacements := map[string]string{
		"{{installer}}":    installerPath,
		"{{installerDir}}": workDir,
		"%INSTALLER%":      installerPath,
		"%INSTALLER_DIR%":  workDir,
	}
	for placeholder, value := range replacements {
		command = strings.ReplaceAll(command, placeholder, value)
	}
	return command
}

// downloadCustomAsset baixa o instalador publicado pelo servidor para uma pasta
// temporária usando o token do próprio agent. Aceita URL absoluta ou relativa
// (relativa é resolvida contra apiServer da configuração).
func (s *Service) downloadCustomAsset(ctx context.Context, item appstore.Item, packageID string) (string, error) {
	fileURL := strings.TrimSpace(item.FileURL)
	if s.debugConfig == nil {
		return "", fmt.Errorf("configuração do servidor indisponível para baixar o instalador do app custom %q", packageID)
	}

	cfg := s.debugConfig()
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	if apiServer == "" || token == "" {
		return "", fmt.Errorf("configuração do servidor API incompleta para baixar o instalador do app custom %q", packageID)
	}

	// Espaço em disco: falhar no meio de um download de ~2 GB deixa lixo no
	// temp e uma mensagem ruim. Checa antes quando o servidor informa o tamanho.
	if expected := item.FileSizeBytes; expected > 0 {
		if free, err := freeDiskBytes(os.TempDir()); err == nil && free > 0 && uint64(expected) > free {
			return "", fmt.Errorf(
				"espaço em disco insuficiente para baixar o instalador do app custom %q: necessário %s, livre %s",
				packageID, humanBytes(uint64(expected)), humanBytes(free))
		}
	}

	target := fileURL
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = cfg.APIScheme() + "://" + apiServer + target
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("falha ao preparar download do instalador %q: %w", packageID, err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, strings.TrimSpace(cfg.AgentID)); err != nil {
		return "", err
	}

	resp, err := tlsutil.NewHTTPClient(customInstallCommandTimeout).Do(req)
	if err != nil {
		return "", fmt.Errorf("falha ao baixar instalador do app custom %q: %w", packageID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download do instalador do app custom %q retornou HTTP %s", packageID, resp.Status)
	}

	// Varre sobras de execuções anteriores antes de criar uma nova pasta.
	cleanupStaleCustomAssets()

	tempDir, err := os.MkdirTemp("", "discovery-custom-*")
	if err != nil {
		return "", fmt.Errorf("falha ao criar pasta temporária para o instalador: %w", err)
	}

	// O nome vindo do servidor pode não ter extensão (ex.: Content-Disposition
	// gravado apenas com o packageId quando o cadastro não guardou o nome do
	// arquivo). Um PE sem extensão não é iniciado por `cmd /C` — a instalação
	// falha com "não é reconhecido como um comando interno" mesmo o binário
	// sendo válido. Detecta o tipo pelos primeiros bytes (e pelo Content-Type)
	// e completa a extensão antes de gravar; o stream é reconstruído para que
	// nada se perca.
	assetName := customAssetFileName(resp, target)
	body, renamed := ensureInstallerExtension(assetName, resp.Body, resp.Header.Get("Content-Type"))
	if renamed != "" {
		assetName = renamed
		s.logf(fmt.Sprintf("[install %s] instalador veio sem extensão; gravando como %s", packageID, assetName))
	}

	dest := filepath.Join(tempDir, assetName)
	out, err := os.Create(dest)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return "", fmt.Errorf("falha ao gravar instalador em %s: %w", dest, err)
	}

	// O SHA-256 é calculado durante a cópia (sem segunda passada) para validar
	// a integridade antes de executar qualquer coisa.
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hasher), body)
	closeErr := out.Close()

	if copyErr != nil {
		_ = os.RemoveAll(tempDir)
		return "", fmt.Errorf("falha ao gravar instalador do app custom %q: %w", packageID, copyErr)
	}
	if closeErr != nil {
		_ = os.RemoveAll(tempDir)
		return "", fmt.Errorf("falha ao finalizar instalador do app custom %q: %w", packageID, closeErr)
	}

	if expected := item.FileSizeBytes; expected > 0 && written != expected {
		_ = os.RemoveAll(tempDir)
		return "", fmt.Errorf(
			"tamanho do instalador do app custom %q não confere: esperado %d bytes, recebido %d",
			packageID, expected, written)
	}

	// Só validamos quando o checksum é de fato um SHA-256 (64 hex). Pacotes
	// cadastrados antes desta mudança gravaram o ETag do S3 (MD5) — comparar
	// isso com SHA-256 bloquearia instalações que já funcionavam.
	if expected := strings.TrimSpace(item.FileChecksum); isSHA256Hex(expected) {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(expected, got) {
			_ = os.RemoveAll(tempDir)
			return "", fmt.Errorf(
				"checksum do instalador do app custom %q não confere (esperado %s, obtido %s)",
				packageID, expected, got)
		}
		s.logf(fmt.Sprintf("[install %s] checksum SHA-256 validado", packageID))
	}

	s.logf(fmt.Sprintf("[install %s] instalador baixado para %s", packageID, dest))
	return dest, nil
}

// isSHA256Hex reconhece um SHA-256 em hexadecimal (64 dígitos). Valores com
// outro formato (ex.: ETag/MD5 de pacotes antigos) não são verificáveis como
// SHA-256 e são ignorados.
func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		isDigit := r >= '0' && r <= '9'
		isLower := r >= 'a' && r <= 'f'
		isUpper := r >= 'A' && r <= 'F'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}

// customAssetCleanupDelay é a folga antes de apagar a pasta do instalador:
// instaladores que disparam um processo filho continuam lendo os arquivos
// depois que o cmd retorna (instalação assíncrona).
const customAssetCleanupDelay = 30 * time.Minute

// scheduleCustomAssetCleanup agenda a remoção da pasta do instalador e, de
// quebra, varre pastas de instalações anteriores que ficaram para trás.
func (s *Service) scheduleCustomAssetCleanup(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}

	go func() {
		time.Sleep(customAssetCleanupDelay)
		_ = os.RemoveAll(dir)
	}()
}

// cleanupStaleCustomAssets remove pastas discovery-custom-* no temp com mais de
// 24h (sobras de execuções encerradas/abortadas).
func cleanupStaleCustomAssets() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "discovery-custom-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(os.TempDir(), entry.Name()))
	}
}

// humanBytes formata bytes para exibição em mensagens de erro.
func humanBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}

	units := []string{"KB", "MB", "GB", "TB"}
	size := float64(value)
	index := -1
	for size >= unit && index < len(units)-1 {
		size /= unit
		index++
	}
	return fmt.Sprintf("%.2f %s", size, units[index])
}

// customAssetFileName escolhe o nome do arquivo baixado a partir do
// Content-Disposition; fallback para o último segmento da URL.
func customAssetFileName(resp *http.Response, rawURL string) string {
	if disposition := resp.Header.Get("Content-Disposition"); disposition != "" {
		if _, params, err := mime.ParseMediaType(disposition); err == nil {
			if name := strings.TrimSpace(params["filename"]); name != "" {
				if base := filepath.Base(name); base != "." && base != string(filepath.Separator) {
					return base
				}
			}
		}
	}

	if parsed, err := url.Parse(rawURL); err == nil {
		if base := path.Base(parsed.Path); base != "." && base != "/" && base != "" {
			return base
		}
	}

	return "installer.bin"
}

// installerMagicBytes é quantos bytes são lidos do corpo do download para
// detectar o tipo do instalador quando o servidor manda um nome sem extensão.
const installerMagicBytes = 8

// allowedInstallerExtensions espelha a lista aceita no upload do servidor
// (CustomPackageAssetService). Nome já com uma dessas extensões é preservado.
var allowedInstallerExtensions = map[string]struct{}{
	".exe": {}, ".msi": {}, ".msix": {}, ".msixbundle": {}, ".appx": {},
	".zip": {}, ".7z": {}, ".rar": {}, ".ps1": {}, ".bat": {}, ".cmd": {},
	".msu": {}, ".cab": {}, ".iso": {}, ".tar": {}, ".gz": {}, ".pkg": {},
	".dmg": {},
}

// ensureInstallerExtension devolve o corpo do download e o nome corrigido
// quando o nome original não termina numa extensão de instalador reconhecida.
// Nome já utilizável volta intacto (segundo valor vazio) e o corpo é devolvido
// sem nenhuma leitura prévia.
func ensureInstallerExtension(name string, body io.Reader, contentType string) (io.Reader, string) {
	if hasInstallerExtension(name) {
		return body, ""
	}

	sniff := make([]byte, installerMagicBytes)
	n, _ := io.ReadFull(body, sniff)
	sniff = sniff[:n]

	renamed := appendInstallerExtension(name, sniff, contentType)
	if renamed == name {
		return body, ""
	}
	return io.MultiReader(bytes.NewReader(sniff), body), renamed
}

// hasInstallerExtension indica se o nome já termina numa extensão que o
// instalador aceita e que o `cmd /C` consegue iniciar.
func hasInstallerExtension(name string) bool {
	_, ok := allowedInstallerExtensions[strings.ToLower(filepath.Ext(name))]
	return ok
}

// appendInstallerExtension completa (ou corrige) a extensão de um instalador
// baixado sem extensão reconhecida, usando a assinatura do arquivo e, na
// sequência, o Content-Type. Sem nenhuma pista assume .exe: é a extensão que o
// `cmd /C` aceita iniciar e a alternativa é repetir a falha.
func appendInstallerExtension(name string, head []byte, contentType string) string {
	if strings.TrimSpace(name) == "" {
		name = "installer"
	}

	detected := installerExtensionFromMagic(head)
	if detected == "" {
		detected = installerExtensionFromContentType(contentType)
	}
	if detected == "" {
		detected = ".exe"
	}

	if existing := filepath.Ext(name); existing != "" {
		return strings.TrimSuffix(name, existing) + detected
	}
	return name + detected
}

// installerExtensionFromMagic reconhece as assinaturas dos instaladores mais
// comuns publicados na loja.
func installerExtensionFromMagic(head []byte) string {
	switch {
	case len(head) >= 2 && head[0] == 'M' && head[1] == 'Z':
		return ".exe"
	case len(head) >= 4 && head[0] == 'P' && head[1] == 'K' && head[2] == 0x03 && head[3] == 0x04:
		return ".zip"
	case len(head) >= 8 && bytes.Equal(head[:8], []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}):
		return ".msi" // contêiner OLE/CFB (MSI, MSIX, CAB antigo)
	case len(head) >= 6 && bytes.Equal(head[:6], []byte{'7', 'z', 0xBC, 0xAF, 0x27, 0x1C}):
		return ".7z"
	case len(head) >= 4 && bytes.Equal(head[:4], []byte{'R', 'a', 'r', '!'}):
		return ".rar"
	case len(head) >= 4 && bytes.Equal(head[:4], []byte{'M', 'S', 'C', 'F'}):
		return ".cab"
	}
	return ""
}

// installerExtensionFromContentType mapeia o Content-Type anunciado pelo
// servidor para a extensão correspondente.
func installerExtensionFromContentType(contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if index := strings.Index(ct, ";"); index >= 0 {
		ct = strings.TrimSpace(ct[:index])
	}

	switch ct {
	case "application/zip", "application/x-zip-compressed", "multipart/x-zip":
		return ".zip"
	case "application/x-msdownload", "application/x-msdos-program",
		"application/vnd.microsoft.portable-executable", "application/x-dosexec",
		"application/x-winexe":
		return ".exe"
	case "application/x-msi", "application/x-msinstaller":
		return ".msi"
	case "application/x-7z-compressed":
		return ".7z"
	case "application/vnd.rar", "application/x-rar-compressed":
		return ".rar"
	case "application/vnd.ms-cab-compressed":
		return ".cab"
	case "application/x-iso9660-image":
		return ".iso"
	}
	return ""
}

// extractZipArchive extrai um ZIP para destDir protegendo contra zip-slip
// (caminhos que escapam do diretório de destino).
func extractZipArchive(zipPath, destDir string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("falha ao abrir ZIP %q: %w", filepath.Base(zipPath), err)
	}
	defer reader.Close()

	cleanDest := filepath.Clean(destDir)
	if err := os.MkdirAll(cleanDest, 0o755); err != nil {
		return fmt.Errorf("falha ao criar pasta de extração: %w", err)
	}

	for _, entry := range reader.File {
		target := filepath.Join(cleanDest, entry.Name)
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("ZIP inválido %q: caminho %q fora do diretório de extração", filepath.Base(zipPath), entry.Name)
		}

		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("falha ao criar pasta %q: %w", target, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("falha ao criar pasta %q: %w", filepath.Dir(target), err)
		}

		if err := writeZipEntry(entry, target); err != nil {
			return err
		}
	}

	return nil
}

func writeZipEntry(entry *zip.File, target string) error {
	source, err := entry.Open()
	if err != nil {
		return fmt.Errorf("falha ao ler %q do ZIP: %w", entry.Name, err)
	}
	defer source.Close()

	destination, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("falha ao criar %q: %w", target, err)
	}
	defer destination.Close()

	if _, err := io.Copy(destination, source); err != nil {
		return fmt.Errorf("falha ao extrair %q: %w", entry.Name, err)
	}
	return nil
}

// resolveExtractionRoot devolve a pasta raiz real do conteúdo extraído: quando
// o ZIP tem um único diretório no topo (padrão de distribuição), usa esse
// diretório; caso contrário mantém a própria pasta de extração.
func resolveExtractionRoot(destDir string) string {
	entries, err := os.ReadDir(destDir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return destDir
	}
	return filepath.Join(destDir, entries[0].Name())
}

func (s *Service) runChocolatey(ctx context.Context, operation, packageID string) (string, error) {
	if _, err := exec.LookPath("choco"); err != nil {
		return "", fmt.Errorf("Chocolatey nao encontrado no host")
	}

	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return "", fmt.Errorf("id do pacote e obrigatorio")
	}

	args := []string{operation, packageID, "-y", "--no-progress"}
	if operation == "install" {
		args = []string{"install", packageID, "-y", "--no-progress"}
	}
	if operation == "upgrade" {
		args = []string{"upgrade", packageID, "-y", "--no-progress"}
	}
	if operation == "uninstall" {
		args = []string{"uninstall", packageID, "-y", "--no-progress"}
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	cmd := exec.CommandContext(runCtx, "choco", args...)
	processutil.HideWindow(cmd)
	// O serviço roda como LocalSystem: enriquece o PATH com as ferramentas do
	// Chocolatey que ficam só no PATH do usuário (ex.: dart em C:	ools),
	// senão scripts de pós-instalação que chamam ferramentas por nome falham.
	cmd.Env = packageManagerEnv()
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("erro executando chocolatey %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

func normalizeAppStoreInstallationType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "winget":
		return string(appstore.InstallationWinget)
	case "chocolatey":
		return string(appstore.InstallationChocolatey)
	default:
		return strings.TrimSpace(value)
	}
}

func splitPackageManagerTarget(raw string) (string, string) {
	target := strings.TrimSpace(raw)
	parts := strings.SplitN(target, packageManagerTargetSeparator, 2)
	if len(parts) != 2 {
		return "", target
	}
	sourceHint := normalizePackageManagerSourceHint(parts[0])
	packageID := strings.TrimSpace(parts[1])
	if sourceHint == "" || packageID == "" {
		return "", target
	}
	return sourceHint, packageID
}

func normalizePackageManagerSourceHint(raw string) string {
	source := strings.ToLower(strings.TrimSpace(raw))
	switch source {
	case "winget":
		return string(appstore.InstallationWinget)
	case "chocolatey", "choco":
		return string(appstore.InstallationChocolatey)
	default:
		return ""
	}
}

// notificationOutputLimit limita o texto do instalador anexado à notificação:
// o metadata trafega por NATS e não pode carregar saída ilimitada.
const notificationOutputLimit = 4000

// truncateNotificationOutput corta a saída do comando preservando o começo do
// texto (onde ficam os erros do instalador).
func truncateNotificationOutput(text string) string {
	if len(text) <= notificationOutputLimit {
		return text
	}
	return text[:notificationOutputLimit] + "... (truncado)"
}

func (s *Service) emitInstallNotification(correlationID, packageID, eventType, phase, status, title, message string, extra map[string]any) {
	if s.dispatchNotification == nil {
		return
	}

	event := strings.TrimSpace(strings.ToLower(eventType))
	phaseValue := strings.TrimSpace(strings.ToLower(phase))
	notificationKey := correlationID + "-" + event + "-" + phaseValue
	if phaseValue == "" {
		notificationKey = correlationID + "-" + event
	}

	severity := "medium"
	if event == "install_end" {
		severity = "low"
	}
	if event == "install_failed" {
		severity = "high"
	}

	metadata := map[string]any{
		"correlationId": correlationID,
		"taskId":        correlationID,
		"taskName":      "Install " + packageID,
		"packageId":     packageID,
		"status":        status,
		"phase":         phase,
		"totalTasks":    1,
		"sourceType":    "app_store",
	}
	for key, value := range extra {
		metadata[key] = value
	}

	s.dispatchNotification(InventoryNotification{
		NotificationID: notificationKey,
		IdempotencyKey: notificationKey,
		Title:          title,
		Message:        message,
		Mode:           "notify_only",
		Severity:       severity,
		EventType:      eventType,
		Layout:         "toast",
		TimeoutSeconds: 45,
		Metadata:       metadata,
	})
}

func hasRebootSignal(output string) bool {
	text := strings.ToLower(strings.TrimSpace(output))
	if text == "" {
		return false
	}
	markers := []string{"3010", "1641", "reboot", "reinici"}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func sanitizeNotificationIDPart(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-", "|", "-", "@", "-", "#", "-")
	clean := replacer.Replace(trimmed)
	clean = strings.Trim(clean, "-")
	if clean == "" {
		return "unknown"
	}
	return clean
}
