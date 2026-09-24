package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"discovery/app/core/chocolatey"
	"discovery/app/core/models"
	"discovery/app/core/winget"
	"discovery/app/debug"
	"discovery/app/netutil"
	"discovery/app/services/hardwareid"
)

type agentHardwareEnvelope struct {
	AgentID                string          `json:"agentId"`
	Hostname               string          `json:"hostname"`
	DisplayName            string          `json:"displayName"`
	Status                 string          `json:"status"`
	OperatingSystem        string          `json:"operatingSystem"`
	OSVersion              string          `json:"osVersion"`
	AgentVersion           string          `json:"agentVersion"`
	CommitHash             string          `json:"commitHash,omitempty"`
	LastIPAddress          string          `json:"lastIpAddress"`
	MACAddress             string          `json:"macAddress"`
	Hardware               json.RawMessage `json:"hardware"`
	Components             json.RawMessage `json:"components"`
	MachineScore           int             `json:"machineScore"`
	InventoryRaw           string          `json:"inventoryRaw"`
	InventorySchemaVersion string          `json:"inventorySchemaVersion"`
	InventoryCollectedAt   string          `json:"inventoryCollectedAt"`
}

type agentHardwareComponents struct {
	Disks           []agentDiskInfo           `json:"disks"`
	NetworkAdapters []agentNetworkAdapterInfo `json:"networkAdapters"`
	MemoryModules   []agentMemoryModuleInfo   `json:"memoryModules"`
	Printers        []agentPrinterInfo        `json:"printers"`
	ListeningPorts  []agentListeningPortInfo  `json:"listeningPorts"`
	OpenSockets     []agentOpenSocketInfo     `json:"openSockets"`

	// StartupItems/ScheduledTasks: omitempty — quando ausentes no envelope
	// (sync parcial), a API preserva as listas já armazenadas (merge).
	StartupItems   []agentStartupItemInfo   `json:"startupItems,omitempty"`
	ScheduledTasks []agentScheduledTaskInfo `json:"scheduledTasks,omitempty"`
}

// agentStartupItemInfo item de inicialização no formato do envelope da API.
type agentStartupItemInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Args     string `json:"args,omitempty"`
	Type     string `json:"type"`
	Source   string `json:"source"`
	Status   string `json:"status,omitempty"`
	Username string `json:"username,omitempty"`
	Detail   string `json:"detail,omitempty"`
	// Hive identifica a conta do item ("HKLM", "HKCU" ou "HKU:<SID>"), para o
	// comando de habilitar/desabilitar acertar o hive correto.
	Hive string `json:"hive,omitempty"`
}

// agentScheduledTaskInfo tarefa agendada no formato do envelope da API.
type agentScheduledTaskInfo struct {
	TaskPath    string `json:"taskPath"`
	TaskName    string `json:"taskName"`
	State       string `json:"state,omitempty"`
	Status      string `json:"status,omitempty"`
	Author      string `json:"author,omitempty"`
	ActionPath  string `json:"actionPath,omitempty"`
	ActionArgs  string `json:"actionArgs,omitempty"`
	TriggerType string `json:"triggerType,omitempty"`
	TriggerDesc string `json:"triggerDesc,omitempty"`
	NextRunTime string `json:"nextRunTime,omitempty"`
	LastRunTime string `json:"lastRunTime,omitempty"`
	LastResult  int64  `json:"lastResult,omitempty"`
}

type agentHardwareInfo struct {
	InventoryRaw            string  `json:"inventoryRaw"`
	InventorySchemaVersion  string  `json:"inventorySchemaVersion"`
	InventoryCollectedAt    string  `json:"inventoryCollectedAt"`
	Manufacturer            string  `json:"manufacturer"`
	Model                   string  `json:"model"`
	SerialNumber            string  `json:"serialNumber"`
	MotherboardManufacturer string  `json:"motherboardManufacturer"`
	MotherboardModel        string  `json:"motherboardModel"`
	MotherboardSerialNumber string  `json:"motherboardSerialNumber"`
	Processor               string  `json:"processor"`
	ProcessorCores          int     `json:"processorCores"`
	ProcessorThreads        int     `json:"processorThreads"`
	ProcessorArchitecture   string  `json:"processorArchitecture"`
	ProcessorTdpWatts       int     `json:"processorTdpWatts"`
	ProcessorSocket         string  `json:"processorSocket"`
	ProcessorFrequencyGhz   float64 `json:"processorFrequencyGhz"`
	ProcessorReleaseDate    string  `json:"processorReleaseDate"`
	TotalMemoryBytes        int64   `json:"totalMemoryBytes"`
	GpuModel                string  `json:"gpuModel"`
	GpuMemoryBytes          int64   `json:"gpuMemoryBytes"`
	GpuDriverVersion        string  `json:"gpuDriverVersion"`
	BIOSVersion             string  `json:"biosVersion"`
	BIOSManufacturer        string  `json:"biosManufacturer"`
	BIOSDate                string  `json:"biosDate"`
	BIOSSerialNumber        string  `json:"biosSerialNumber"`
	MachineScore            int     `json:"machineScore"`
	OSName                  string  `json:"osName"`
	OSVersion               string  `json:"osVersion"`
	OSEdition               string  `json:"osEdition,omitempty"`
	OSBuild                 string  `json:"osBuild"`
	OSArchitecture          string  `json:"osArchitecture"`
	TpmEkHash               string  `json:"tpmEk,omitempty"`
	SmbiosUuid              string  `json:"smbiosUuid,omitempty"`
	CollectedAt             string  `json:"collectedAt"`
	UpdatedAt               string  `json:"updatedAt"`
}

type agentDiskInfo struct {
	DriveLetter    string `json:"driveLetter"`
	Label          string `json:"label"`
	FileSystem     string `json:"fileSystem"`
	TotalSizeBytes int64  `json:"totalSizeBytes"`
	FreeSpaceBytes int64  `json:"freeSpaceBytes"`
	MediaType      string `json:"mediaType"`
	CollectedAt    string `json:"collectedAt"`

	// ── Saúde SMART (opcional) ──
	SmartStatus        string `json:"smartStatus,omitempty"`
	TemperatureC       *int   `json:"temperatureC,omitempty"`
	PowerOnHours       *int   `json:"powerOnHours,omitempty"`
	ReallocatedSectors *int   `json:"reallocatedSectors,omitempty"`
}

type agentNetworkAdapterInfo struct {
	Name          string `json:"name"`
	MACAddress    string `json:"macAddress"`
	IPAddress     string `json:"ipAddress"`
	Ipv6Address   string `json:"ipv6Address"`
	SubnetMask    string `json:"subnetMask"`
	Gateway       string `json:"gateway"`
	DNSServers    string `json:"dnsServers"`
	IsDhcpEnabled bool   `json:"isDhcpEnabled"`
	AdapterType   string `json:"adapterType"`
	Speed         string `json:"speed"`
	CollectedAt   string `json:"collectedAt"`
}

type agentMemoryModuleInfo struct {
	Slot          string `json:"slot"`
	CapacityBytes int64  `json:"capacityBytes"`
	SpeedMhz      int    `json:"speedMhz"`
	MemoryType    string `json:"memoryType"`
	Manufacturer  string `json:"manufacturer"`
	PartNumber    string `json:"partNumber"`
	SerialNumber  string `json:"serialNumber"`
	CollectedAt   string `json:"collectedAt"`
}

type agentPrinterInfo struct {
	Name             string  `json:"name"`
	DriverName       string  `json:"driverName"`
	PortName         string  `json:"portName"`
	PrinterStatus    string  `json:"printerStatus"`
	IsDefault        bool    `json:"isDefault"`
	IsNetworkPrinter bool    `json:"isNetworkPrinter"`
	Shared           bool    `json:"shared"`
	ShareName        *string `json:"shareName"`
	Location         string  `json:"location"`
	CollectedAt      string  `json:"collectedAt"`
}

type agentListeningPortInfo struct {
	ProcessName string `json:"processName"`
	ProcessID   int    `json:"processId"`
	ProcessPath string `json:"processPath"`
	Protocol    string `json:"protocol"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
}

type agentOpenSocketInfo struct {
	ProcessName   string `json:"processName"`
	ProcessID     int    `json:"processId"`
	ProcessPath   string `json:"processPath"`
	LocalAddress  string `json:"localAddress"`
	LocalPort     int    `json:"localPort"`
	RemoteAddress string `json:"remoteAddress"`
	RemotePort    int    `json:"remotePort"`
	Protocol      string `json:"protocol"`
	Family        string `json:"family"`
	State         string `json:"state,omitempty"`
}

type agentSoftwareEnvelope struct {
	AgentID     string              `json:"agentId"`
	CollectedAt string              `json:"collectedAt"`
	Software    []agentSoftwareItem `json:"software"`
}

type agentSoftwareItem struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Publisher     string `json:"publisher"`
	InstallID     string `json:"installId"`
	Serial        string `json:"serial"`
	Source        string `json:"source"`
	InstallDate   string `json:"installDate"`
	InstallSource string `json:"installSource"`

	// Update disponível (winget upgrade / choco outdated). omitempty: quando
	// não há update pendente os campos ficam ausentes do JSON e a API os
	// assume como false/vazio.
	AvailableVersion string `json:"availableVersion,omitempty"`
	UpdateAvailable  bool   `json:"updateAvailable,omitempty"`
	UpdateSource     string `json:"updateSource,omitempty"`
	// UpdatePackageID é o identificador do gerenciador de pacotes (winget/choco)
	// usado para executar o update remoto. Difere do installId do registro, que
	// costuma ser o ProductCode do MSI.
	UpdatePackageID string `json:"updatePackageId,omitempty"`
}

// SyncInventoryOnStartup sends inventory payloads when credentials are available.
func (s *Service) SyncInventoryOnStartup(ctx context.Context, report models.InventoryReport) {
	if ctx == nil {
		ctx = context.Background()
	}

	cfg := s.debugConfig()
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))

	hardwarePayload := buildAgentHardwareEnvelope(report, s.version, s.commitHash, s.hardwareIdentity)
	hardwarePayload.AgentID = strings.TrimSpace(cfg.AgentID)
	hardwareBody, err := json.Marshal(hardwarePayload)
	if err != nil {
		s.logf("[agent-sync] falha ao serializar inventario: " + err.Error())
		return
	}

	// Updates pendentes + pacotes reconhecidos pelo gerenciador. Estas listas são
	// a ÚNICA fonte de updateAvailable/updatePackageId do inventário — se
	// qualquer um dos dois vier vazio, TODOS os apps são reportados como "sem
	// atualização" e o botão "Atualizar" desaparece do dashboard.
	pendingUpdates := s.loadPendingUpdates(ctx)
	installedPackages := s.loadInstalledPackages(ctx)
	if len(pendingUpdates) == 0 || len(installedPackages) == 0 {
		// Diagnóstico explícito: sem isto o sintoma no dashboard é indistinguível
		// de "não há updates" (e já causou perda silenciosa de dados em produção).
		// O caminho do winget é incluído porque o caso mais comum de scan vazio
		// no serviço (LocalSystem) é o winget não ser localizável fora do perfil
		// do usuário interativo.
		wingetPath, wingetFrom := winget.ResolveExecutable()
		if wingetPath == "" {
			wingetPath = "(nao encontrado)"
		}
		s.logf(fmt.Sprintf(
			"[agent-sync] AVISO: scan do gerenciador incompleto (pending=%d installed=%d) — inventário será reportado sem atualizações/Ids de pacote; winget=%s origem=%s",
			len(pendingUpdates), len(installedPackages), wingetPath, wingetFrom))
	}
	softwarePayload := buildAgentSoftwareEnvelope(
		report,
		strings.TrimSpace(cfg.AgentID),
		pendingUpdates,
		installedPackages,
		s.loadCatalogChocoIndex(ctx, installedPackages, pendingUpdates),
	)
	softwareBody, err := json.Marshal(softwarePayload)
	if err != nil {
		s.logf("[agent-sync] falha ao serializar softwares: " + err.Error())
		return
	}

	snapshotAgentID := strings.TrimSpace(cfg.AgentID)
	if snapshotAgentID == "" {
		snapshotAgentID = "local:" + trimToMaxLen(strings.TrimSpace(report.Hardware.Hostname), 100)
	}

	hasRemoteCredentials := cfg.ApiServer != "" && strings.TrimSpace(cfg.AuthToken) != "" && strings.TrimSpace(cfg.AgentID) != ""
	validScheme := cfg.ApiScheme == "http" || cfg.ApiScheme == "https"

	if !hasRemoteCredentials || !validScheme {
		if s.db != nil {
			if err := s.db.SaveInventorySnapshot(snapshotAgentID, hardwareBody, softwareBody); err != nil {
				s.logf("[agent-sync] aviso: falha ao salvar snapshot local: " + err.Error())
			} else {
				s.logf("[agent-sync] snapshot local salvo sem envio remoto")
			}
		}

		if !hasRemoteCredentials {
			s.logf("[agent-sync] ignorado: faltam apiServer/token/agentId no Debug")
			return
		}
		s.logf("[agent-sync] ignorado: apiScheme inválido (use http ou https)")
		return
	}

	if s.shouldDeferNonCritical != nil {
		if delay, deferred, reason := s.shouldDeferNonCritical(); deferred {
			if delay <= 0 {
				delay = time.Second
			}
			if reason != "" {
				s.logf(fmt.Sprintf("[agent-sync] envio remoto adiado por sobrecarga do servidor por %s (motivo=%s)", delay.Round(time.Second), reason))
			} else {
				s.logf(fmt.Sprintf("[agent-sync] envio remoto adiado por sobrecarga do servidor por %s", delay.Round(time.Second)))
			}
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				s.logf("[agent-sync] envio remoto cancelado durante janela de adiamento")
				return
			case <-timer.C:
			}
		}
	}

	if s.db != nil {
		shouldSync, reason, err := s.db.ShouldSyncInventory(cfg.AgentID, hardwareBody, softwareBody)
		if err != nil {
			s.logf("[agent-sync] erro ao verificar diff: " + err.Error())
		} else if !shouldSync {
			s.logf(fmt.Sprintf("[agent-sync] SYNC IGNORADO: %s", reason))
			if err := s.db.SaveInventorySnapshot(cfg.AgentID, hardwareBody, softwareBody); err != nil {
				s.logf("[agent-sync] aviso: falha ao salvar snapshot local: " + err.Error())
			}
			return
		} else {
			s.logf(fmt.Sprintf("[agent-sync] SYNC NECESSARIO: %s", reason))
		}
	}

	s.logf(fmt.Sprintf(
		"[agent-sync] hardware payload: collectedAt=%s disks=%d networkAdapters=%d memoryModules=%d printers=%d hostname=%s",
		hardwarePayload.InventoryCollectedAt,
		len(report.Disks),
		len(report.Networks),
		len(report.MemoryModules),
		len(report.Printers),
		hardwarePayload.Hostname,
	))

	hardwareEndpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/hardware"
	hardwareSuccess := false
	if err := s.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPost, hardwareBody); err != nil {
		s.logf("[agent-sync] POST hardware falhou: " + err.Error())
		if err := s.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPut, hardwareBody); err != nil {
			s.logf("[agent-sync] PUT hardware falhou: " + err.Error())
		} else {
			s.logf("[agent-sync] inventario de hardware atualizado via PUT")
			hardwareSuccess = true
		}
	} else {
		s.logf("[agent-sync] inventario de hardware enviado via POST")
		hardwareSuccess = true
	}

	s.logf(fmt.Sprintf(
		"[agent-sync] software payload: collectedAt=%s softwareCount=%d",
		softwarePayload.CollectedAt,
		len(softwarePayload.Software),
	))

	softwareEndpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/software"
	s.logf("[agent-sync] endpoint software: " + softwareEndpoint)
	softwareSuccess := false
	if err := s.sendAgentInventoryRequest(ctx, softwareEndpoint, cfg, http.MethodPost, softwareBody); err != nil {
		s.logf("[agent-sync] POST software falhou: " + err.Error())
		if err := s.sendAgentInventoryRequest(ctx, softwareEndpoint, cfg, http.MethodPut, softwareBody); err != nil {
			s.logf("[agent-sync] PUT software falhou: " + err.Error())
		} else {
			s.logf("[agent-sync] inventario de software atualizado via PUT")
			softwareSuccess = true
		}
	} else {
		s.logf("[agent-sync] inventario de software enviado via POST")
		softwareSuccess = true
	}

	if hardwareSuccess && softwareSuccess && s.db != nil {
		if err := s.db.SaveInventorySnapshot(cfg.AgentID, hardwareBody, softwareBody); err != nil {
			s.logf("[agent-sync] aviso: falha ao salvar snapshot: " + err.Error())
		}
		if err := s.db.UpdateLastSyncTime("inventory_sync:"+cfg.AgentID, "success"); err != nil {
			s.logf("[agent-sync] aviso: falha ao atualizar timestamp de sync: " + err.Error())
		} else {
			s.logf("[agent-sync] snapshot salvo e timestamp atualizado")
		}
	}
}
func (s *Service) SyncNetworkConnections(ctx context.Context) error {
	if err := s.requireProvisionedInventory(); err != nil {
		return err
	}
	report, err := s.collectNetworkConnectionsWithHeartbeat(ctx)
	if err != nil {
		return err
	}
	// Atualiza cache local
	if s.cache != nil {
		if cached, ok := s.cache.Get(); ok {
			cached.ListeningPorts = report.ListeningPorts
			cached.OpenSockets = report.OpenSockets
			s.cache.Set(cached)
		}
	}

	cfg := s.debugConfig()
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))

	hasRemoteCredentials := cfg.ApiServer != "" && strings.TrimSpace(cfg.AuthToken) != "" && strings.TrimSpace(cfg.AgentID) != ""
	validScheme := cfg.ApiScheme == "http" || cfg.ApiScheme == "https"
	if !hasRemoteCredentials || !validScheme {
		s.logf("[agent-sync] SyncNetworkConnections: sem credenciais remotas, pulando upload")
		return nil
	}

	// Build minimal envelope with just network data
	collected := time.Now().UTC().Format(time.RFC3339)
	components := agentHardwareComponents{
		ListeningPorts: mapAgentListeningPorts(report.ListeningPorts),
		OpenSockets:    mapAgentOpenSockets(report.OpenSockets),
	}
	compJSON, _ := json.Marshal(components)

	envelope := agentHardwareEnvelope{
		AgentID:                strings.TrimSpace(cfg.AgentID),
		Status:                 "online",
		InventoryCollectedAt:   collected,
		InventorySchemaVersion: "",
		Components:             compJSON,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal network envelope: %w", err)
	}

	hardwareEndpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/hardware"
	if err := s.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPost, body); err != nil {
		s.logf("[agent-sync] SyncNetworkConnections POST falhou: " + err.Error())
		if err := s.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPut, body); err != nil {
			s.logf("[agent-sync] SyncNetworkConnections PUT falhou: " + err.Error())
			return err
		}
	}
	s.logf(fmt.Sprintf("[agent-sync] SyncNetworkConnections enviado: ports=%d sockets=%d",
		len(report.ListeningPorts), len(report.OpenSockets)))
	return nil
}

// mapAgentStartupItems converte os itens de inicialização para o envelope da API.
// Entrada nil (coleta indisponível) → nil, para o merge server-side PRESERVAR
// a lista armazenada em vez de zerá-la por uma falha transitória.
func mapAgentStartupItems(items []models.StartupItem) []agentStartupItemInfo {
	if items == nil {
		return nil
	}
	result := make([]agentStartupItemInfo, 0, len(items))
	for _, it := range items {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		result = append(result, agentStartupItemInfo{
			Name:     trimToMaxLen(name, 200),
			Path:     trimToMaxLen(strings.TrimSpace(it.Path), 500),
			Args:     trimToMaxLen(strings.TrimSpace(it.Args), 500),
			Type:     trimToMaxLen(strings.TrimSpace(it.Type), 20),
			Source:   trimToMaxLen(strings.TrimSpace(it.Source), 100),
			Status:   trimToMaxLen(strings.TrimSpace(it.Status), 20),
			Username: trimToMaxLen(strings.TrimSpace(it.Username), 100),
			Detail:   trimToMaxLen(strings.TrimSpace(it.Detail), 100),
			Hive:     trimToMaxLen(strings.TrimSpace(it.Hive), 100),
		})
	}
	return result
}

// mapAgentScheduledTasks converte as tarefas agendadas para o envelope da API.
// Entrada nil (coleta indisponível) → nil, para o merge server-side PRESERVAR
// a lista armazenada em vez de zerá-la por uma falha transitória.
func mapAgentScheduledTasks(tasks []models.ScheduledTaskInfo) []agentScheduledTaskInfo {
	if tasks == nil {
		return nil
	}
	result := make([]agentScheduledTaskInfo, 0, len(tasks))
	for _, t := range tasks {
		name := strings.TrimSpace(t.TaskName)
		if name == "" {
			continue
		}
		result = append(result, agentScheduledTaskInfo{
			TaskPath:    trimToMaxLen(strings.TrimSpace(t.TaskPath), 300),
			TaskName:    trimToMaxLen(name, 200),
			State:       trimToMaxLen(strings.TrimSpace(t.State), 20),
			Status:      trimToMaxLen(strings.TrimSpace(t.Status), 60),
			Author:      trimToMaxLen(strings.TrimSpace(t.Author), 200),
			ActionPath:  trimToMaxLen(strings.TrimSpace(t.ActionPath), 500),
			ActionArgs:  trimToMaxLen(strings.TrimSpace(t.ActionArgs), 500),
			TriggerType: trimToMaxLen(strings.TrimSpace(t.TriggerType), 20),
			TriggerDesc: trimToMaxLen(strings.TrimSpace(t.TriggerDesc), 200),
			NextRunTime: trimToMaxLen(strings.TrimSpace(t.NextRunTime), 40),
			LastRunTime: trimToMaxLen(strings.TrimSpace(t.LastRunTime), 40),
			LastResult:  t.LastResult,
		})
	}
	return result
}

// SyncStartupAndScheduledTasks coleta itens de inicialização + tarefas
// agendadas e faz upload APENAS dessas listas no envelope de componentes.
// As demais listas ficam ausentes do JSON — a API preserva, no merge
// server-side, as listas não reportadas nesta sincronização parcial.
func (s *Service) SyncStartupAndScheduledTasks(ctx context.Context) error {
	if err := s.requireProvisionedInventory(); err != nil {
		return err
	}

	var startup []models.StartupItem
	var tasks []models.ScheduledTaskInfo
	if s.inventory != nil {
		if items, err := s.inventory.CollectStartupItems(ctx); err == nil {
			startup = items
		} else {
			s.logf("[agent-sync] falha ao coletar itens de inicialização: " + err.Error())
		}
		if t, err := s.inventory.CollectScheduledTasks(ctx); err == nil {
			tasks = t
		} else {
			s.logf("[agent-sync] falha ao coletar tarefas agendadas: " + err.Error())
		}
	}

	// Atualiza o cache local com os novos dados
	if s.cache != nil {
		if cached, ok := s.cache.Get(); ok {
			cached.StartupItems = startup
			cached.ScheduledTasks = tasks
			s.cache.Set(cached)
		}
	}

	cfg := s.debugConfig()
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))

	hasRemoteCredentials := cfg.ApiServer != "" && strings.TrimSpace(cfg.AuthToken) != "" && strings.TrimSpace(cfg.AgentID) != ""
	validScheme := cfg.ApiScheme == "http" || cfg.ApiScheme == "https"
	if !hasRemoteCredentials || !validScheme {
		s.logf("[agent-sync] SyncStartupAndScheduledTasks: sem credenciais remotas, pulando upload")
		return nil
	}

	collected := time.Now().UTC().Format(time.RFC3339)
	components := agentHardwareComponents{
		StartupItems:   mapAgentStartupItems(startup),
		ScheduledTasks: mapAgentScheduledTasks(tasks),
	}
	compJSON, _ := json.Marshal(components)

	envelope := agentHardwareEnvelope{
		AgentID:              strings.TrimSpace(cfg.AgentID),
		Status:               "online",
		InventoryCollectedAt: collected,
		Components:           compJSON,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal startup/tasks envelope: %w", err)
	}

	hardwareEndpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/hardware"
	if err := s.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPost, body); err != nil {
		s.logf("[agent-sync] SyncStartupAndScheduledTasks POST falhou: " + err.Error())
		if err := s.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPut, body); err != nil {
			return err
		}
	}
	s.logf(fmt.Sprintf("[agent-sync] SyncStartupAndScheduledTasks enviado: startup=%d tasks=%d",
		len(startup), len(tasks)))
	return nil
}

// mapAgentListeningPorts converts model ports to API envelope format.
func mapAgentListeningPorts(ports []models.ListeningPortInfo) []agentListeningPortInfo {
	result := make([]agentListeningPortInfo, 0, len(ports))
	for _, p := range ports {
		if p.Port == 0 {
			continue
		}
		result = append(result, agentListeningPortInfo{
			ProcessName: trimToMaxLen(strings.TrimSpace(p.ProcessName), 200),
			ProcessID:   p.ProcessID,
			ProcessPath: trimToMaxLen(strings.TrimSpace(p.ProcessPath), 500),
			Protocol:    trimToMaxLen(strings.TrimSpace(p.Protocol), 10),
			Address:     trimToMaxLen(strings.TrimSpace(p.Address), 45),
			Port:        p.Port,
		})
	}
	return result
}

// mapAgentOpenSockets converts model sockets to API envelope format.
func mapAgentOpenSockets(sockets []models.OpenSocketInfo) []agentOpenSocketInfo {
	result := make([]agentOpenSocketInfo, 0, len(sockets))
	for _, s := range sockets {
		if s.LocalPort == 0 && s.RemotePort == 0 {
			continue
		}
		result = append(result, agentOpenSocketInfo{
			ProcessName:   trimToMaxLen(strings.TrimSpace(s.ProcessName), 200),
			ProcessID:     s.ProcessID,
			ProcessPath:   trimToMaxLen(strings.TrimSpace(s.ProcessPath), 500),
			LocalAddress:  trimToMaxLen(strings.TrimSpace(s.LocalAddress), 45),
			LocalPort:     s.LocalPort,
			RemoteAddress: trimToMaxLen(strings.TrimSpace(s.RemoteAddress), 45),
			RemotePort:    s.RemotePort,
			Protocol:      trimToMaxLen(strings.TrimSpace(s.Protocol), 10),
			Family:        trimToMaxLen(strings.TrimSpace(s.Family), 10),
			State:         trimToMaxLen(strings.TrimSpace(s.State), 16),
		})
	}
	return result
}

var inventoryHTTPClient = &http.Client{Timeout: 20 * time.Second}

func (s *Service) sendAgentInventoryRequest(parent context.Context, endpoint string, cfg debug.Config, method string, body []byte) error {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()

	s.logf("[agent-sync] request: " + method + " " + endpoint)
	s.logf("[agent-sync] request headers: Authorization=Bearer " + sanitizeToken(cfg.AuthToken) + "; X-Agent-ID=" + cfg.AgentID + "; Content-Type=application/json")
	s.logf("[agent-sync] request body: " + truncateLogBody(body, 2000))

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return err
	}

	resp, err := inventoryHTTPClient.Do(req)
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

func buildAgentSoftwareEnvelope(
	report models.InventoryReport,
	agentID string,
	pending []models.UpgradeItem,
	installed []models.InstalledPackage,
	catalogChoco map[string]string,
) agentSoftwareEnvelope {
	return buildAgentSoftwareEnvelopeWithIndex(
		report, agentID, pending, installed, catalogChoco, indexInstalledDisplayNames(installed))
}

// buildAgentSoftwareEnvelopeWithIndex é o núcleo de buildAgentSoftwareEnvelope
// com o índice de nomes de exibição injetado (testável sem tocar o disco do
// Chocolatey).
func buildAgentSoftwareEnvelopeWithIndex(
	report models.InventoryReport,
	agentID string,
	pending []models.UpgradeItem,
	installed []models.InstalledPackage,
	catalogChoco map[string]string,
	displayNameIndex installedDisplayNameIndex,
) agentSoftwareEnvelope {
	collected := strings.TrimSpace(report.CollectedAt)
	if collected == "" {
		collected = time.Now().UTC().Format(time.RFC3339)
	}

	// O merge dos apps que só existem no gerenciador de pacotes é aplicado AQUI,
	// no ponto único de montagem do payload — não em cada chamador. Antes ele
	// morava apenas no SyncInventoryOnStartup e os outros caminhos de coleta
	// (startup, loop periódico, force-sync) enviavam o inventário "cru", sem os
	// apps do gerenciador e sem os Ids que habilitam update/desinstalação.
	report.Software = mergePackageManagerSoftware(report.Software, installed, pending, "", displayNameIndex)

	byInstallID, byName := indexPendingUpdates(pending)
	installedIndex := indexInstalledPackages(installed)

	software := make([]agentSoftwareItem, 0, len(report.Software))
	for _, s := range report.Software {
		name := trimToMaxLen(strings.TrimSpace(s.Name), 300)
		if name == "" {
			continue
		}
		source := trimToMaxLen(strings.TrimSpace(s.Source), 120)
		if source == "" {
			source = "native/registry"
		}
		item := agentSoftwareItem{
			Name:          name,
			Version:       trimToMaxLen(strings.TrimSpace(s.Version), 120),
			Publisher:     trimToMaxLen(strings.TrimSpace(s.Publisher), 300),
			InstallID:     trimToMaxLen(strings.TrimSpace(s.InstallID), 1000),
			Serial:        trimToMaxLen(strings.TrimSpace(s.Serial), 1000),
			Source:        source,
			InstallDate:   trimToMaxLen(strings.TrimSpace(s.InstallDate), 64),
			InstallSource: trimToMaxLen(strings.TrimSpace(s.InstallSource), 1000),
		}
		if update, ok := matchPendingUpdate(s, byInstallID, byName, installedIndex, displayNameIndex, catalogChoco); ok {
			item.AvailableVersion = trimToMaxLen(strings.TrimSpace(update.AvailableVersion), 120)
			item.UpdateAvailable = true
			item.UpdateSource = normalizeSoftwareUpdateSource(update.Source)
			item.UpdatePackageID = trimToMaxLen(strings.TrimSpace(update.ID), 1000)
		} else if update, ok := matchPendingUpdateByManagerName(s, byName, displayNameIndex); ok {
			// A linha de upgrades cita um nome que não é o do registro (ex.: o
			// "winget upgrade" pode citar o Id no lugar do nome): usa o update.
			item.AvailableVersion = trimToMaxLen(strings.TrimSpace(update.AvailableVersion), 120)
			item.UpdateAvailable = true
			item.UpdateSource = normalizeSoftwareUpdateSource(update.Source)
			item.UpdatePackageID = trimToMaxLen(strings.TrimSpace(update.ID), 1000)
		} else if pkg, ok := resolvePackageForSoftware(s, installedIndex, displayNameIndex); ok {
			// Sem update pendente, mas o gerenciador reconhece o app: guarda o
			// Id do pacote (e a origem) para viabilizar a desinstalação remota.
			item.UpdatePackageID = trimToMaxLen(strings.TrimSpace(pkg.ID), 1000)
			item.UpdateSource = normalizeSoftwareUpdateSource(pkg.Source)
		} else if id := strings.TrimSpace(catalogChoco[normalizeUpdateKey(s.Name)]); id != "" {
			// App Chocolatey do catálogo da loja: o "choco list" só traz o Id,
			// então o nome de exibição do registro é mapeado pelo catálogo.
			item.UpdatePackageID = trimToMaxLen(id, 1000)
			item.UpdateSource = "chocolatey"
		}
		software = append(software, item)
	}

	return agentSoftwareEnvelope{
		AgentID:     agentID,
		CollectedAt: collected,
		Software:    software,
	}
}

// indexPendingUpdates cria índices por installId e por nome (case-insensitive)
// a partir dos updates pendentes reportados por winget/chocolatey.
func indexPendingUpdates(pending []models.UpgradeItem) (map[string]models.UpgradeItem, map[string][]models.UpgradeItem) {
	byInstallID := make(map[string]models.UpgradeItem, len(pending))
	byName := make(map[string][]models.UpgradeItem, len(pending))
	for _, u := range pending {
		if id := normalizeUpdateKey(u.ID); id != "" {
			byInstallID[id] = u
		}
		if name := normalizeUpdateKey(u.Name); name != "" {
			byName[name] = append(byName[name], u)
		}
	}
	return byInstallID, byName
}

// pickNameMatch prefere o update cuja CurrentVersion casa com a versão do app
// instalado, reduzindo falso positivo quando dois pacotes compartilham o nome.
func pickNameMatch(entries []models.UpgradeItem, version string) (models.UpgradeItem, bool) {
	normalized := normalizeUpdateKey(version)
	if normalized == "" {
		return models.UpgradeItem{}, false
	}
	for _, entry := range entries {
		if normalizeUpdateKey(entry.CurrentVersion) == normalized {
			return entry, true
		}
	}
	return models.UpgradeItem{}, false
}

// installedPackageIndex indexa os apps instalados reconhecidos pelo winget por
// nome normalizado, permitindo resolver o Id real do pacote de um item do
// inventário (o InstallID do registro costuma ser o ProductCode do MSI).
type installedPackageIndex struct {
	byName map[string][]models.InstalledPackage
}

func indexInstalledPackages(installed []models.InstalledPackage) installedPackageIndex {
	index := installedPackageIndex{byName: make(map[string][]models.InstalledPackage, len(installed))}
	for _, pkg := range installed {
		key := normalizeUpdateKey(pkg.Name)
		if key == "" {
			continue
		}
		index.byName[key] = append(index.byName[key], pkg)
	}
	return index
}

// candidates retorna os pacotes instalados com o mesmo nome, priorizando a
// versão exata (desambigua homônimos x86/x64).
func (index installedPackageIndex) candidates(name, version string) []models.InstalledPackage {
	entries := index.byName[normalizeUpdateKey(name)]
	if len(entries) == 0 {
		return nil
	}
	normalizedVersion := normalizeUpdateKey(version)
	if normalizedVersion == "" {
		return entries
	}
	ordered := make([]models.InstalledPackage, 0, len(entries))
	for _, entry := range entries {
		if normalizeUpdateKey(entry.Version) == normalizedVersion {
			ordered = append(ordered, entry)
		}
	}
	for _, entry := range entries {
		if normalizeUpdateKey(entry.Version) != normalizedVersion {
			ordered = append(ordered, entry)
		}
	}
	return ordered
}

// resolve localiza o pacote instalado reconhecido pelo gerenciador (versão exata
// primeiro; caso contrário o primeiro com o mesmo nome).
func (index installedPackageIndex) resolve(name, version string) (models.InstalledPackage, bool) {
	candidates := index.candidates(name, version)
	if len(candidates) == 0 {
		return models.InstalledPackage{}, false
	}
	return candidates[0], true
}

// matchPendingUpdate liga um app instalado ao update pendente. A vinculação
// preferencial usa o Id do "winget list" para o app do inventário — não depende
// do nome que o "winget upgrade" imprime. Fallbacks: installId/serial do
// registro e, por fim, o nome do update.
func matchPendingUpdate(
	s models.SoftwareItem,
	byInstallID map[string]models.UpgradeItem,
	byName map[string][]models.UpgradeItem,
	installed installedPackageIndex,
	displayNames installedDisplayNameIndex,
	catalogChoco map[string]string,
) (models.UpgradeItem, bool) {
	// 1) Id do gerenciador reconhecido localmente (winget list / choco list).
	for _, pkg := range installed.candidates(s.Name, s.Version) {
		if u, ok := byInstallID[normalizeUpdateKey(pkg.ID)]; ok {
			return u, true
		}
	}
	// 1b) Título do .nuspec instalado (choco registra o Id, o registro registra
	// o título) e nome de exibição do "winget list". Sem isto, um app choco sem
	// correspondência por InstallId/Serial só casaria pelo nome — justamente o
	// nome que o winget não repete na tabela de upgrades.
	for _, candidate := range packageDisplayNameCandidates(s, displayNames) {
		if u, ok := byInstallID[normalizeUpdateKey(candidate)]; ok {
			return u, true
		}
	}
	// 1c) Id Chocolatey pelo nome de exibição (catálogo da loja).
	if id := strings.TrimSpace(catalogChoco[normalizeUpdateKey(s.Name)]); id != "" {
		if u, ok := byInstallID[normalizeUpdateKey(id)]; ok {
			return u, true
		}
	}
	// 2) ProductCode/UninstallString do registro.
	for _, candidate := range []string{s.InstallID, s.Serial} {
		if key := normalizeUpdateKey(candidate); key != "" {
			if u, ok := byInstallID[key]; ok {
				return u, true
			}
		}
	}
	if entries := byName[normalizeUpdateKey(s.Name)]; len(entries) > 0 {
		if u, ok := pickNameMatch(entries, s.Version); ok {
			return u, true
		}
		return entries[0], true
	}
	// 3) Último recurso: nome de exibição do gerenciador para o app do registro.
	// Cobre o caso em que o item nem entrou na tabela de upgrades (ex.: o Id do
	// "winget upgrade" consulta fontes que não existem no "winget list").
	for _, candidate := range packageDisplayNameCandidates(s, displayNames) {
		if entries := byName[normalizeUpdateKey(candidate)]; len(entries) > 0 {
			if u, ok := pickNameMatch(entries, s.Version); ok {
				return u, true
			}
			return entries[0], true
		}
	}
	return models.UpgradeItem{}, false
}

// matchPendingUpdateByManagerName correlaciona o app do registro a um update
// pendente cujo NOME ainda não foi resolvido pelos caminhos principais: o item
// pode citar o nome de exibição do gerenciador ou o próprio Id do pacote (ex.:
// "Microsoft.WSL") em vez do nome do registro.
func matchPendingUpdateByManagerName(
	s models.SoftwareItem,
	byName map[string][]models.UpgradeItem,
	displayNames installedDisplayNameIndex,
) (models.UpgradeItem, bool) {
	for _, pkg := range displayNames.candidates(s.Name, s.Version) {
		for _, key := range []string{pkg.Name, pkg.ID} {
			entries := byName[normalizeUpdateKey(key)]
			if len(entries) == 0 {
				continue
			}
			if u, ok := pickNameMatch(entries, s.Version); ok {
				return u, true
			}
			return entries[0], true
		}
	}
	return models.UpgradeItem{}, false
}

// installedDisplayNameIndex liga o TÍTULO de exibição do pacote (título do
// .nuspec no Chocolatey; Name do "winget list") ao pacote instalado. O
// inventário de registro guarda o nome de exibição ("Adobe Acrobat Reader DC",
// "Kudu 3.3.0"), enquanto o gerenciador trabalha com Ids ("adobereader",
// "AdventDevelopmentInc.Kudu") — sem este índice a correlação depende do nome
// que o update imprime, que costuma divergir do nome do registro.
type installedDisplayNameIndex struct {
	byName map[string][]models.InstalledPackage
	// byID é o índice reverso (Id do gerenciador → pacote). O merge usa para
	// reconhecer que um Id de pacote do choco/winget (ex.: "adobereader") já
	// está representado no inventário pelo nome de exibição do registro
	// ("Adobe Acrobat Reader DC") e não virar uma segunda linha.
	byID map[string][]models.InstalledPackage
}

func indexInstalledDisplayNames(installed []models.InstalledPackage) installedDisplayNameIndex {
	index := installedDisplayNameIndex{
		byName: make(map[string][]models.InstalledPackage, len(installed)),
		byID:   make(map[string][]models.InstalledPackage, len(installed)),
	}
	// Títulos do .nuspec (choco list só devolve id|versão).
	for _, info := range chocolatey.ScanInstalledPackages() {
		title := normalizeUpdateKey(info.Title)
		if title == "" {
			continue
		}
		pkg := models.InstalledPackage{
			Name:    info.Title,
			ID:      info.ID,
			Version: info.Version,
			Source:  "chocolatey",
		}
		index.byName[title] = append(index.byName[title], pkg)
		if id := normalizeUpdateKey(info.ID); id != "" {
			index.byID[id] = append(index.byID[id], pkg)
		}
	}
	// Nome de exibição do "winget list" — usado principalmente para resolver o
	// Id do pacote quando o app ainda não tem update pendente.
	for _, pkg := range installed {
		name := normalizeUpdateKey(pkg.Name)
		if name == "" {
			continue
		}
		index.byName[name] = append(index.byName[name], pkg)
		if id := normalizeUpdateKey(pkg.ID); id != "" {
			index.byID[id] = append(index.byID[id], pkg)
		}
	}
	return index
}

// aliasesFor devolve os nomes/Ids alternativos do gerenciador para um nome de
// exibição já presente no inventário (título do .nuspec no choco ou Name do
// "winget list"). É o que liga "Adobe Acrobat Reader DC" ao Id "adobereader".
func (index installedDisplayNameIndex) aliasesFor(name string) []string {
	entries := index.byName[normalizeUpdateKey(name)]
	if len(entries) == 0 {
		return nil
	}
	aliases := make([]string, 0, len(entries)*2)
	for _, pkg := range entries {
		if id := strings.TrimSpace(pkg.ID); id != "" {
			aliases = append(aliases, id)
		}
		if pkgName := strings.TrimSpace(pkg.Name); pkgName != "" {
			aliases = append(aliases, pkgName)
		}
	}
	return aliases
}

// displayNameFor devolve o título de exibição conhecido para um Id de pacote
// (título do .nuspec no choco; Name do "winget list"). Usado para nomear com o
// nome amigável os apps que só existem no gerenciador.
func (index installedDisplayNameIndex) displayNameFor(id string) string {
	key := normalizeUpdateKey(id)
	if key == "" {
		return ""
	}
	for _, pkg := range index.byID[key] {
		if name := strings.TrimSpace(pkg.Name); name != "" {
			return name
		}
	}
	return ""
}

// candidates devolve os pacotes cujo nome de exibição bate com o do inventário,
// priorizando a versão exata (desambigua homônimos).
func (index installedDisplayNameIndex) candidates(name, version string) []models.InstalledPackage {
	entries := index.byName[normalizeUpdateKey(name)]
	if len(entries) == 0 {
		return nil
	}
	normalizedVersion := normalizeUpdateKey(version)
	if normalizedVersion == "" {
		return entries
	}
	ordered := make([]models.InstalledPackage, 0, len(entries))
	for _, entry := range entries {
		if normalizeUpdateKey(entry.Version) == normalizedVersion {
			ordered = append(ordered, entry)
		}
	}
	for _, entry := range entries {
		if normalizeUpdateKey(entry.Version) != normalizedVersion {
			ordered = append(ordered, entry)
		}
	}
	return ordered
}

// packageDisplayNameCandidates lista os nomes pelos quais o app do inventário é
// conhecido no gerenciador de pacotes, sem repetições.
func packageDisplayNameCandidates(s models.SoftwareItem, displayNames installedDisplayNameIndex) []string {
	// Elegibilidade por versão: só aceitamos o nome do gerenciador quando a
	// versão confere com a do registro. Sem isso, um app homônimo de outra
	// versão (ou de outro produto) herdaria um packageId errado.
	if !sameSoftwareVersion(s.Version, displayNames.candidates(s.Name, s.Version)) {
		return nil
	}
	seen := map[string]struct{}{}
	candidates := make([]string, 0, 4)
	add := func(value string) {
		name := strings.TrimSpace(value)
		key := normalizeUpdateKey(name)
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, name)
	}
	add(s.Name)
	for _, pkg := range displayNames.candidates(s.Name, s.Version) {
		add(pkg.Name)
	}
	return candidates
}

// sameSoftwareVersion confirma que o pacote do gerenciador corresponde à MESMA
// instalação do app do registro. Quando qualquer um dos lados não informa
// versão, a comparação é inconclusiva e o candidato é aceito.
func sameSoftwareVersion(version string, candidates []models.InstalledPackage) bool {
	if len(candidates) == 0 {
		return false
	}
	want := normalizeUpdateKey(version)
	if want == "" {
		return true
	}
	for _, pkg := range candidates {
		if normalizeUpdateKey(pkg.Version) == want {
			return true
		}
	}
	return false
}

// resolvePackageForSoftware localiza o pacote do gerenciador correspondente ao
// app do inventário (winget list primeiro; depois o nome de exibição/título do
// .nuspec). É o que permite o botão "Atualizar" nos apps choco, cujo Id nunca
// aparece na listagem do winget.
func resolvePackageForSoftware(
	s models.SoftwareItem,
	installed installedPackageIndex,
	displayNames installedDisplayNameIndex,
) (models.InstalledPackage, bool) {
	if pkg, ok := installed.resolve(s.Name, s.Version); ok {
		return pkg, true
	}
	candidates := displayNames.candidates(s.Name, s.Version)
	if len(candidates) == 0 {
		return models.InstalledPackage{}, false
	}
	return candidates[0], true
}

// mergePackageManagerSoftware acrescenta ao inventário de registro os apps que
// SÓ existem no gerenciador de pacotes (o registro não tem entrada de ARP para
// eles — ex.: o pacote Microsoft.WSL). Sem esta união, esses apps nunca
// aparecem no inventário do agente e, portanto, nunca exibem update pendente.
//
// A decisão de "já existe" considera TODOS os apelidos do pacote: o Id e o nome
// do gerenciador, o nome de exibição do "winget list" e o título do .nuspec do
// Chocolatey. Era isto que faltava: o registro tem "Adobe Acrobat Reader DC" e o
// "choco outdated" só devolve o Id "adobereader" — sem cruzar os apelidos o
// mesmo app aparecia DUAS vezes (título + Id), cada uma com update pendente.
// Best-effort: entrada ausente não altera nada.
func mergePackageManagerSoftware(
	software []models.SoftwareItem,
	installed []models.InstalledPackage,
	pending []models.UpgradeItem,
	source string,
	displayNames installedDisplayNameIndex,
) []models.SoftwareItem {
	if len(installed) == 0 && len(pending) == 0 {
		return software
	}

	seen := make(map[string]struct{}, len(installed)+len(pending))
	mark := func(values ...string) {
		for _, value := range values {
			if key := normalizeUpdateKey(value); key != "" {
				seen[key] = struct{}{}
			}
		}
	}
	isKnown := func(values ...string) bool {
		for _, value := range values {
			if key := normalizeUpdateKey(value); key != "" {
				if _, exists := seen[key]; exists {
					return true
				}
			}
		}
		return false
	}
	// aliases devolve todas as formas pelas quais o pacote do gerenciador pode
	// já estar representado no inventário.
	aliases := func(id, name string) []string {
		values := []string{id, name}
		values = append(values, displayNames.aliasesFor(name)...)
		values = append(values, displayNames.displayNameFor(id))
		return values
	}
	// preferredName usa o nome amigável conhecido (título do .nuspec no choco,
	// Name do winget list) em vez do Id cru quando o app só existe no gerenciador.
	preferredName := func(id, name string) string {
		if title := displayNames.displayNameFor(id); title != "" {
			return title
		}
		return strings.TrimSpace(name)
	}

	for _, item := range software {
		mark(item.Name)
		// Registra também os apelidos dos itens já existentes: "Adobe Acrobat
		// Reader DC" marca o Id "adobereader" como já representado.
		mark(displayNames.aliasesFor(item.Name)...)
	}

	// Prefixo de origem dos itens adicionados: deriva da PRÓPRIA lista já
	// coletada (nunca de um palpite sobre qual provider está ativo). Sem uma
	// amostra, mantém o rótulo nativo do coletor Windows.
	source = strings.TrimSpace(source)
	if source == "" {
		source = detectSoftwareCollectionSource(software)
	}

	added := make([]models.SoftwareItem, 0, len(installed)+len(pending))
	for _, pkg := range installed {
		if normalizeUpdateKey(pkg.Name) == "" {
			continue
		}
		values := aliases(pkg.ID, pkg.Name)
		if isKnown(values...) {
			continue
		}
		mark(values...)
		added = append(added, models.SoftwareItem{
			Name:      preferredName(pkg.ID, pkg.Name),
			Version:   strings.TrimSpace(pkg.Version),
			Publisher: "Sem fabricante",
			Source:    source,
		})
	}

	// Updates pendentes sem pacote listado (pacote não reconhecido pelo
	// "winget list"): reporta o app com a versão atual e disponível.
	for _, update := range pending {
		if normalizeUpdateKey(update.Name) == "" {
			continue
		}
		values := aliases(update.ID, update.Name)
		if isKnown(values...) {
			continue
		}
		mark(values...)
		added = append(added, models.SoftwareItem{
			Name:      preferredName(update.ID, update.Name),
			Version:   strings.TrimSpace(update.CurrentVersion),
			Publisher: "Sem fabricante",
			Source:    source,
		})
	}

	if len(added) == 0 {
		return software
	}
	return append(software, added...)
}

// detectSoftwareCollectionSource identifica o prefixo de origem usado pela
// coleta atual a partir de uma amostra da lista já coletada (ex.: "registry"
// no coletor nativo, "osquery/programs" no coletor via osquery). Assim os itens
// adicionados pelo merge ficam com a MESMA origem do restante do inventário, em
// vez de um palpite sobre qual provider está ativo.
func detectSoftwareCollectionSource(software []models.SoftwareItem) string {
	for _, item := range software {
		if s := strings.TrimSpace(item.Source); s != "" {
			return s
		}
	}
	return "registry"
}

func normalizeUpdateKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func normalizeSoftwareUpdateSource(source string) string {
	normalized := strings.ToLower(strings.TrimSpace(source))
	switch {
	case strings.Contains(normalized, "choco"):
		return "chocolatey"
	case strings.Contains(normalized, "winget"):
		return "winget"
	case normalized == "":
		return "winget"
	default:
		return normalized
	}
}

// installedPackagesCacheTTL evita rodar "winget list" repetidamente em rajadas
// de sincronização (startup + pós-bootstrap).
const installedPackagesCacheTTL = 10 * time.Minute

// loadInstalledPackages consulta o "winget list" de forma best-effort e com
// cache curto, reutilizando a última lista em falha transitória (indexar o
// inventário com uma lista vazia perderia a correlação pelo Id do winget).
func (s *Service) loadInstalledPackages(ctx context.Context) []models.InstalledPackage {
	if s.installedPackages == nil {
		return nil
	}

	s.installedPackagesMu.Lock()
	cached := s.installedPackagesLast
	cachedAt := s.installedPackagesLastAt
	goodAt := s.installedPackagesGoodAt
	loaded := s.installedPackagesLoaded
	s.installedPackagesMu.Unlock()

	if loaded && time.Since(cachedAt) < installedPackagesCacheTTL {
		return cached
	}

	hadGoodScan := len(cached) > 0 && !goodAt.IsZero()
	items, err := s.installedPackages(ctx)
	if err != nil {
		s.logf("[agent-sync] aviso: falha ao listar pacotes instalados (winget list): " + err.Error())
		if hadGoodScan {
			s.logf("[agent-sync] reutilizando ultima lista de instalados conhecida para nao limpar o servidor")
			return cached
		}
		return nil
	}

	// Mesma proteção do scan de updates: uma lista vazia transitória do
	// "winget list" zeraria updatePackageId de todo o inventário no servidor.
	if len(items) == 0 && hadGoodScan && time.Since(goodAt) < emptyScanGraceTTL {
		s.logf("[agent-sync] winget list vazio suspeito; mantendo ultima lista conhecida para nao limpar o servidor")
		return cached
	}

	s.installedPackagesMu.Lock()
	s.installedPackagesLast = items
	s.installedPackagesLastAt = time.Now()
	s.installedPackagesLoaded = true
	if len(items) > 0 {
		s.installedPackagesGoodAt = s.installedPackagesLastAt
	}
	s.installedPackagesMu.Unlock()
	return items
}

// pendingUpdatesCacheTTL evita rodar winget/choco repetidamente em rajadas de
// sincronização (startup + pós-bootstrap). O próximo ciclo após o TTL — ou o
// sync periódico (~6h) — refaz o scan e detecta updates novos.
const pendingUpdatesCacheTTL = 10 * time.Minute

// emptyScanGraceTTL é a janela em que um scan VAZIO é tratado como suspeito
// logo após um scan com dados. O "winget upgrade" pode devolver tabela vazia
// por timeout, bloqueio de fonte ou saída localizada não reconhecida — e enviar
// esse vazio ao servidor ZERA updateAvailable/updatePackageId de todos os apps
// (o dashboard perde os botões de atualizar/desinstalar até o próximo scan bom).
const emptyScanGraceTTL = 15 * time.Minute

// loadPendingUpdates consulta os updates pendentes de forma best-effort e com
// cache curto. Em falha transitória (winget/choco indisponíveis, timeout)
// reutiliza a última lista conhecida: enviar "sem updates" num erro limparia no
// servidor a informação de atualização já reportada. O próximo scan
// bem-sucedido — que pode inclusive voltar vazio — a substitui normalmente.
func (s *Service) loadPendingUpdates(ctx context.Context) []models.UpgradeItem {
	if s.pendingUpdates == nil {
		return nil
	}

	s.pendingUpdatesMu.Lock()
	cached := s.pendingUpdatesLast
	cachedAt := s.pendingUpdatesLastAt
	goodAt := s.pendingUpdatesGoodAt
	loaded := s.pendingUpdatesLoaded
	s.pendingUpdatesMu.Unlock()

	if loaded && time.Since(cachedAt) < pendingUpdatesCacheTTL {
		return cached
	}

	// A última lista boa serve de fallback mesmo depois de invalidada: o
	// marcador "loaded" cai na invalidação, mas o conteúdo continua válido.
	hadGoodScan := len(cached) > 0 && !goodAt.IsZero()
	items, err := s.pendingUpdates(ctx)
	if err != nil {
		s.logf("[agent-sync] aviso: falha ao consultar updates pendentes: " + err.Error())
		if hadGoodScan {
			s.logf("[agent-sync] reutilizando ultima lista de updates conhecida para nao limpar o servidor")
			return cached
		}
		return nil
	}

	// Scan vazio logo após um scan com dados = quase sempre falha transitória do
	// winget/choco (timeout, fonte indisponível, saída não reconhecida), não
	// ausência real de updates. Enviar esse vazio apagaria no servidor os
	// updates/Ids já reportados, então reutilizamos a última lista boa dentro da
	// janela de graça. Após a janela, um vazio é aceito como verdade.
	if len(items) == 0 && hadGoodScan && time.Since(goodAt) < emptyScanGraceTTL {
		s.logf("[agent-sync] scan vazio suspeito; mantendo ultima lista de updates conhecida para nao limpar o servidor")
		return cached
	}

	s.pendingUpdatesMu.Lock()
	s.pendingUpdatesLast = items
	s.pendingUpdatesLastAt = time.Now()
	s.pendingUpdatesLoaded = true
	if len(items) > 0 {
		s.pendingUpdatesGoodAt = s.pendingUpdatesLastAt
	}
	s.pendingUpdatesMu.Unlock()
	return items
}

// computeMachineScore calcula um score de capacidade da máquina (sem limite superior) baseado em:
// - CPU: núcleos físicos e threads lógicos (50% do score)
// - RAM: total de memória em GB (50% do score)
// Quanto mais recursos, maior o valor.
// Referência: 16c/32t + 64 GB ≈ score 100 (não é teto, apenas referência).
func computeMachineScore(report models.InventoryReport) int {
	cores := report.Hardware.Cores
	threads := report.Hardware.LogicalCores
	ramGB := report.Hardware.MemoryGB

	// CPU score: núcleos físicos valem 1.0, threads extras valem 0.3 cada
	// Ex: 8c/16t => 8 + 8*0.3 = 10.4; referência: 16c/32t => 16 + 16*0.3 = 20.8
	cpuRaw := float64(cores)
	extraThreads := float64(threads - cores)
	if extraThreads < 0 {
		extraThreads = 0
	}
	cpuRaw += extraThreads * 0.3
	// Normalizado contra baseline 16c/32t = 20.8, sem teto
	cpuScore := cpuRaw / 20.8 * 100

	// RAM score: linear, sem teto (64 GB = referência 100)
	ramScore := ramGB / 64.0 * 100

	// Score final: média ponderada 50% CPU + 50% RAM, mínimo 1
	score := (cpuScore*0.5 + ramScore*0.5)
	return int(math.Round(math.Max(score, 1)))
}

func buildAgentHardwareEnvelope(report models.InventoryReport, version, commitHash string, hwIdentity func() hardwareid.Info) agentHardwareEnvelope {
	collected := strings.TrimSpace(report.CollectedAt)
	if collected == "" {
		collected = time.Now().UTC().Format(time.RFC3339)
	}
	updated := time.Now().UTC().Format(time.RFC3339)

	memTotalBytes := int64(report.Hardware.MemoryGB * 1024 * 1024 * 1024)
	if memTotalBytes < 0 {
		memTotalBytes = 0
	}

	machineScore := computeMachineScore(report)

	// GPU info: usa a primeira GPU dedicada (ou onboard) do inventário
	var gpuModel, gpuDriver string
	var gpuMemoryBytes int64
	if len(report.GPUs) > 0 {
		gpuModel = trimToMaxLen(strings.TrimSpace(report.GPUs[0].Name), 200)
		gpuDriver = trimToMaxLen(strings.TrimSpace(report.GPUs[0].DriverVersion), 100)
		gpuMemoryBytes = int64(report.GPUs[0].VRAMGB * 1024 * 1024 * 1024)
		if gpuMemoryBytes < 0 {
			gpuMemoryBytes = 0
		}
	}

	disks := make([]agentDiskInfo, 0, len(report.Disks))
	for _, d := range report.Disks {
		driveLetter := trimToMaxLen(normalizeDriveLetter(d.Device), 10)
		if driveLetter == "" {
			continue
		}
		total := int64(d.SizeGB * 1024 * 1024 * 1024)
		if total < 0 {
			total = 0
		}
		free := int64(d.FreeGB * 1024 * 1024 * 1024)
		if free < 0 || !d.FreeKnown {
			free = 0
		}
		disks = append(disks, agentDiskInfo{
			DriveLetter:    driveLetter,
			Label:          trimToMaxLen(strings.TrimSpace(d.Label), 200),
			FileSystem:     trimToMaxLen(strings.TrimSpace(d.FileSystem), 50),
			TotalSizeBytes: total,
			FreeSpaceBytes: free,
			MediaType:      trimToMaxLen(firstNonEmptyString(strings.TrimSpace(d.MediaType), strings.TrimSpace(d.Type)), 50),
			CollectedAt:    collected,

			SmartStatus:        trimToMaxLen(strings.TrimSpace(d.SmartStatus), 30),
			TemperatureC:       d.TemperatureC,
			PowerOnHours:       d.PowerOnHours,
			ReallocatedSectors: d.ReallocatedSectors,
		})
	}

	adapters := make([]agentNetworkAdapterInfo, 0, len(report.Networks))
	for _, n := range report.Networks {
		name := trimToMaxLen(firstNonEmptyString(strings.TrimSpace(n.FriendlyName), strings.TrimSpace(n.Interface)), 200)
		if name == "" {
			continue
		}
		adapters = append(adapters, agentNetworkAdapterInfo{
			Name:          name,
			MACAddress:    trimToMaxLen(strings.TrimSpace(n.MAC), 32),
			IPAddress:     trimToMaxLen(strings.TrimSpace(n.IPv4), 45),
			Ipv6Address:   trimToMaxLen(strings.TrimSpace(n.IPv6), 500),
			SubnetMask:    "",
			Gateway:       trimToMaxLen(strings.TrimSpace(n.Gateway), 45),
			DNSServers:    trimToMaxLen(normalizeDNSServers(n.DNSServers), 500),
			IsDhcpEnabled: n.DHCPEnabled,
			AdapterType:   trimToMaxLen(strings.TrimSpace(n.Type), 50),
			Speed:         trimToMaxLen(formatLinkSpeed(n.LinkSpeedMbps), 50),
			CollectedAt:   collected,
		})
	}

	modules := make([]agentMemoryModuleInfo, 0, len(report.MemoryModules))
	for _, m := range report.MemoryModules {
		capacity := int64(m.SizeGB * 1024 * 1024 * 1024)
		if capacity <= 0 {
			capacity = int64(m.SizeMB) * 1024 * 1024
		}
		if capacity < 0 {
			capacity = 0
		}
		modules = append(modules, agentMemoryModuleInfo{
			Slot:          trimToMaxLen(strings.TrimSpace(m.Slot), 50),
			CapacityBytes: capacity,
			SpeedMhz:      m.SpeedMHz,
			MemoryType:    trimToMaxLen(strings.TrimSpace(m.Type), 50),
			Manufacturer:  trimToMaxLen(strings.TrimSpace(m.Manufacturer), 200),
			PartNumber:    trimToMaxLen(strings.TrimSpace(m.PartNumber), 100),
			SerialNumber:  trimToMaxLen(strings.TrimSpace(m.Serial), 100),
			CollectedAt:   collected,
		})
	}
	printers := make([]agentPrinterInfo, 0, len(report.Printers))
	for _, p := range report.Printers {
		name := trimToMaxLen(strings.TrimSpace(p.Name), 200)
		if name == "" {
			continue
		}
		printers = append(printers, agentPrinterInfo{
			Name:             name,
			DriverName:       trimToMaxLen(strings.TrimSpace(p.DriverName), 200),
			PortName:         trimToMaxLen(strings.TrimSpace(p.PortName), 200),
			PrinterStatus:    trimToMaxLen(strings.TrimSpace(p.PrinterStatus), 60),
			IsDefault:        p.IsDefault,
			IsNetworkPrinter: p.IsNetworkPrinter,
			Shared:           p.Shared,
			ShareName:        optionalStringPtr(trimToMaxLen(strings.TrimSpace(p.ShareName), 200)),
			Location:         trimToMaxLen(strings.TrimSpace(p.Location), 200),
			CollectedAt:      collected,
		})
	}
	rawJSON := buildCleanInventoryRaw(report, disks, adapters, modules, printers)
	lastIP := ""
	primaryMAC := ""
	for _, n := range adapters {
		if lastIP == "" {
			lastIP = strings.TrimSpace(n.IPAddress)
		}
		if primaryMAC == "" {
			primaryMAC = strings.TrimSpace(n.MACAddress)
		}
		if lastIP != "" && primaryMAC != "" {
			break
		}
	}

	hostname := trimToMaxLen(strings.TrimSpace(report.Hardware.Hostname), 100)
	if len(hostname) < 2 {
		hostname = "unknown-host"
	}
	osName := trimToMaxLen(strings.TrimSpace(report.OS.Name), 100)
	osVersion := trimToMaxLen(strings.TrimSpace(report.OS.Version), 100)

	// version/commitHash vazios são aceitáveis: a API preserva o último valor
	// conhecido quando recebe vazio (nunca sobrescreve com "dev"/"unknown").
	// Serializa hardware e components como json.RawMessage (API v1)
	hwInfo := agentHardwareInfo{
		InventoryRaw:            string(rawJSON),
		InventorySchemaVersion:  "",
		InventoryCollectedAt:    collected,
		Manufacturer:            trimToMaxLen(strings.TrimSpace(report.Hardware.Manufacturer), 100),
		Model:                   trimToMaxLen(strings.TrimSpace(report.Hardware.Model), 100),
		SerialNumber:            trimToMaxLen(strings.TrimSpace(report.Hardware.BIOSSerial), 100),
		MotherboardManufacturer: trimToMaxLen(strings.TrimSpace(report.Hardware.MotherboardManufacturer), 100),
		MotherboardModel:        trimToMaxLen(strings.TrimSpace(report.Hardware.MotherboardModel), 100),
		MotherboardSerialNumber: trimToMaxLen(strings.TrimSpace(report.Hardware.MotherboardSerial), 100),
		Processor:               trimToMaxLen(strings.TrimSpace(report.Hardware.CPU), 100),
		ProcessorCores:          report.Hardware.Cores,
		ProcessorThreads:        report.Hardware.LogicalCores,
		ProcessorArchitecture:   trimToMaxLen(strings.TrimSpace(report.OS.Architecture), 100),
		TotalMemoryBytes:        memTotalBytes,
		GpuModel:                gpuModel,
		GpuMemoryBytes:          gpuMemoryBytes,
		GpuDriverVersion:        gpuDriver,
		BIOSVersion:             trimToMaxLen(strings.TrimSpace(report.Hardware.BIOSVersion), 100),
		BIOSManufacturer:        trimToMaxLen(strings.TrimSpace(report.Hardware.BIOSVendor), 100),
		BIOSDate:                trimToMaxLen(strings.TrimSpace(report.Hardware.BIOSReleaseDate), 100),
		BIOSSerialNumber:        trimToMaxLen(strings.TrimSpace(report.Hardware.BIOSSerial), 100),
		MachineScore:            machineScore,
		OSName:                  osName,
		OSVersion:               osVersion,
		OSEdition:               trimToMaxLen(strings.TrimSpace(report.OS.Edition), 100),
		OSBuild:                 trimToMaxLen(strings.TrimSpace(report.OS.Build), 100),
		OSArchitecture:          trimToMaxLen(strings.TrimSpace(report.OS.Architecture), 100),
		CollectedAt:             collected,
		UpdatedAt:               updated,
	}

	// Fingerprint de hardware (Recuperação de Dispositivos): TPM EK + SMBIOS UUID.
	if hwIdentity != nil {
		hw := hwIdentity()
		hwInfo.TpmEkHash = trimToMaxLen(strings.TrimSpace(hw.TPMEK), 64)
		hwInfo.SmbiosUuid = trimToMaxLen(strings.TrimSpace(hw.SMBIOSUUID), 64)
	}

	hwJSON, _ := json.Marshal(hwInfo)

	components := agentHardwareComponents{
		Disks:           disks,
		NetworkAdapters: adapters,
		MemoryModules:   modules,
		Printers:        printers,
		ListeningPorts:  mapAgentListeningPorts(report.ListeningPorts),
		OpenSockets:     mapAgentOpenSockets(report.OpenSockets),
		StartupItems:    mapAgentStartupItems(report.StartupItems),
		ScheduledTasks:  mapAgentScheduledTasks(report.ScheduledTasks),
	}
	compJSON, _ := json.Marshal(components)

	envelope := agentHardwareEnvelope{
		AgentID:                "",
		Hostname:               hostname,
		DisplayName:            trimToMaxLen(hostname, 100),
		Status:                 "Online",
		OperatingSystem:        osName,
		OSVersion:              osVersion,
		AgentVersion:           trimToMaxLen(strings.TrimSpace(version), 100),
		CommitHash:             trimToMaxLen(strings.TrimSpace(commitHash), 64),
		LastIPAddress:          lastIP,
		MACAddress:             primaryMAC,
		MachineScore:           computeMachineScore(report),
		Hardware:               hwJSON,
		Components:             compJSON,
		InventoryRaw:           string(rawJSON),
		InventorySchemaVersion: "",
		InventoryCollectedAt:   collected,
	}

	return envelope
}

func buildCleanInventoryRaw(report models.InventoryReport, disks []agentDiskInfo, adapters []agentNetworkAdapterInfo, modules []agentMemoryModuleInfo, printers []agentPrinterInfo) json.RawMessage {
	clean := report
	clean.Disks = nil
	clean.Networks = nil
	clean.MemoryModules = nil
	clean.Printers = nil

	raw, _ := json.Marshal(clean)
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}
	payload["disks"] = disks
	payload["networks"] = adapters
	payload["memoryModules"] = modules
	payload["printers"] = printers
	out, _ := json.Marshal(payload)
	return out
}

func normalizeDNSServers(raw string) string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == ' '
	})
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return strings.Join(out, ",")
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func formatLinkSpeed(linkSpeedMbps int) string {
	if linkSpeedMbps <= 0 {
		return ""
	}
	return fmt.Sprintf("%d Mbps", linkSpeedMbps)
}

func normalizeDriveLetter(device string) string {
	device = strings.TrimSpace(strings.ToUpper(device))
	if device == "" {
		return ""
	}
	if len(device) == 1 {
		return device + ":"
	}
	if len(device) == 2 && strings.HasSuffix(device, ":") {
		return device
	}
	return device
}

func trimToMaxLen(value string, max int) string {
	if len(value) <= max {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func optionalStringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func sanitizeToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		// B1: token curto não pode ir inteiro para o log (o buffer persiste
		// em arquivo) — mascara por completo em vez de devolver o valor.
		return "***"
	}
	return token[:4] + "***" + token[len(token)-4:]
}

func truncateLogBody(body []byte, max int) string {
	if len(body) <= max {
		return string(body)
	}
	return string(body[:max]) + "..."
}
