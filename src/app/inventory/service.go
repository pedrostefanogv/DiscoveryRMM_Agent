package inventory

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"time"

	"discovery/app/appstore"
	"discovery/app/core/inventory"
	"discovery/app/core/models"
	"discovery/app/core/processutil"
	"discovery/app/debug"
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
}

// Service handles inventory, installs and sync operations.
type Service struct {
	apps                             AppsService
	inventory                        InventoryService
	cache                            InventoryCache
	resolveAllowed                   func(context.Context, string) (appstore.Item, error)
	resolveAllowedByType             func(context.Context, string, string) (appstore.Item, error)
	getCatalog                       func(context.Context) (models.Catalog, error)
	beginActivity                    ActivityFunc
	dispatchNotification             InventoryNotificationDispatcher
	logf                             func(string)
	ctx                              func() context.Context
	db                               DB
	debugConfig                      func() debug.Config
	version                          string
	commitHash                       string
	shouldDeferNonCritical           func() (time.Duration, bool, string)
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
		beginActivity:                    opts.BeginActivity,
		dispatchNotification:             opts.DispatchNotification,
		logf:                             logf,
		ctx:                              opts.Ctx,
		db:                               normalizeInventoryDB(opts.DB),
		debugConfig:                      opts.DebugConfig,
		version:                          opts.Version,
		commitHash:                       opts.CommitHash,
		shouldDeferNonCritical:           opts.ShouldDeferNonCritical,
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
	default:
		err = fmt.Errorf("installationType %q não suportado", allowed.InstallationType)
	}
	s.logf(out)

	if err != nil {
		s.emitInstallNotification(correlationID, packageID, "install_failed", "instalacao", "failed", "Falha na instalação", "Não foi possível concluir a instalação do aplicativo.", map[string]any{
			"error": err.Error(),
		})
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
	default:
		err = fmt.Errorf("installationType %q não suportado", allowed.InstallationType)
	}
	s.logf(out)
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
	default:
		err = fmt.Errorf("installationType %q não suportado", allowed.InstallationType)
	}
	s.logf(out)
	if err == nil {
		s.scheduleInventoryRefreshAfterPackageChange("upgrade", packageID)
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
	s.logf("[inventory-refresh] inventário atualizado após alterações de software")
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
