package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"discovery/app/debug"
	"discovery/internal/models"
)

type agentHardwareEnvelope struct {
	Hostname               string                  `json:"hostname"`
	DisplayName            string                  `json:"displayName"`
	MeshCentralNodeID      string                  `json:"meshCentralNodeId,omitempty"`
	Status                 string                  `json:"status"`
	OperatingSystem        string                  `json:"operatingSystem"`
	OSVersion              string                  `json:"osVersion"`
	AgentVersion           string                  `json:"agentVersion"`
	LastIPAddress          string                  `json:"lastIpAddress"`
	MACAddress             string                  `json:"macAddress"`
	Hardware               agentHardwareInfo       `json:"hardware"`
	Components             agentHardwareComponents `json:"components"`
	InventoryRaw           json.RawMessage         `json:"inventoryRaw"`
	InventorySchemaVersion string                  `json:"inventorySchemaVersion"`
	InventoryCollectedAt   string                  `json:"inventoryCollectedAt"`
}

type agentHardwareComponents struct {
	Disks           []agentDiskInfo           `json:"disks"`
	NetworkAdapters []agentNetworkAdapterInfo `json:"networkAdapters"`
	MemoryModules   []agentMemoryModuleInfo   `json:"memoryModules"`
	Printers        []agentPrinterInfo        `json:"printers"`
}

type agentHardwareInfo struct {
	InventoryRaw            json.RawMessage `json:"inventoryRaw"`
	InventorySchemaVersion  string          `json:"inventorySchemaVersion"`
	InventoryCollectedAt    string          `json:"inventoryCollectedAt"`
	Manufacturer            string          `json:"manufacturer"`
	Model                   string          `json:"model"`
	SerialNumber            string          `json:"serialNumber"`
	MotherboardManufacturer string          `json:"motherboardManufacturer"`
	MotherboardModel        string          `json:"motherboardModel"`
	MotherboardSerialNumber string          `json:"motherboardSerialNumber"`
	Processor               string          `json:"processor"`
	ProcessorCores          int             `json:"processorCores"`
	ProcessorThreads        int             `json:"processorThreads"`
	ProcessorArchitecture   string          `json:"processorArchitecture"`
	TotalMemoryBytes        int64           `json:"totalMemoryBytes"`
	BIOSVersion             string          `json:"biosVersion"`
	BIOSManufacturer        string          `json:"biosManufacturer"`
	OSName                  string          `json:"osName"`
	OSVersion               string          `json:"osVersion"`
	OSBuild                 string          `json:"osBuild"`
	OSArchitecture          string          `json:"osArchitecture"`
	CollectedAt             string          `json:"collectedAt"`
	UpdatedAt               string          `json:"updatedAt"`
}

type agentDiskInfo struct {
	DriveLetter    string `json:"driveLetter"`
	Label          string `json:"label"`
	FileSystem     string `json:"fileSystem"`
	TotalSizeBytes int64  `json:"totalSizeBytes"`
	FreeSpaceBytes int64  `json:"freeSpaceBytes"`
	MediaType      string `json:"mediaType"`
	CollectedAt    string `json:"collectedAt"`
}

type agentNetworkAdapterInfo struct {
	Name          string `json:"name"`
	MACAddress    string `json:"macAddress"`
	IPAddress     string `json:"ipAddress"`
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

type agentSoftwareEnvelope struct {
	CollectedAt string              `json:"collectedAt"`
	Software    []agentSoftwareItem `json:"software"`
}

type agentSoftwareItem struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	InstallID string `json:"installId"`
	Serial    string `json:"serial"`
	Source    string `json:"source"`
}

// SyncInventoryOnStartup sends inventory payloads when credentials are available.
func (s *Service) SyncInventoryOnStartup(ctx context.Context, report models.InventoryReport) {
	s.pulseInventoryHeartbeat()
	cfg := s.debugConfig()
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))

	hardwarePayload := buildAgentHardwareEnvelope(report, s.version)
	if s.resolveMeshCentralNodeID != nil {
		hardwarePayload.MeshCentralNodeID = strings.TrimSpace(s.resolveMeshCentralNodeID())
	}
	hardwareBody, err := json.Marshal(hardwarePayload)
	if err != nil {
		s.logf("[agent-sync] falha ao serializar inventario: " + err.Error())
		return
	}

	softwarePayload := buildAgentSoftwareEnvelope(report)
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
		s.logf("[agent-sync] ignorado: apiScheme invalido (use http ou https)")
		return
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
		len(hardwarePayload.Components.Disks),
		len(hardwarePayload.Components.NetworkAdapters),
		len(hardwarePayload.Components.MemoryModules),
		len(hardwarePayload.Components.Printers),
		hardwarePayload.Hostname,
	))

	hardwareEndpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/hardware"
	hardwareSuccess := false
	s.pulseInventoryHeartbeat()
	if err := s.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPost, hardwareBody); err != nil {
		s.logf("[agent-sync] POST hardware falhou: " + err.Error())
		s.pulseInventoryHeartbeat()
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
	s.pulseInventoryHeartbeat()
	if err := s.sendAgentInventoryRequest(ctx, softwareEndpoint, cfg, http.MethodPost, softwareBody); err != nil {
		s.logf("[agent-sync] POST software falhou: " + err.Error())
		s.pulseInventoryHeartbeat()
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

	if hardwareSuccess && s.onHardwareReportSuccess != nil {
		s.onHardwareReportSuccess(strings.TrimSpace(hardwarePayload.MeshCentralNodeID))
	}
}

func (s *Service) sendAgentInventoryRequest(parent context.Context, endpoint string, cfg debug.Config, method string, body []byte) error {
	s.pulseInventoryHeartbeat()
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
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	req.Header.Set("X-Agent-ID", cfg.AgentID)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		s.pulseInventoryHeartbeat()
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	s.pulseInventoryHeartbeat()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	s.pulseInventoryHeartbeat()
	return nil
}

func buildAgentSoftwareEnvelope(report models.InventoryReport) agentSoftwareEnvelope {
	collected := strings.TrimSpace(report.CollectedAt)
	if collected == "" {
		collected = time.Now().UTC().Format(time.RFC3339)
	}

	software := make([]agentSoftwareItem, 0, len(report.Software))
	for _, s := range report.Software {
		name := trimToMaxLen(strings.TrimSpace(s.Name), 300)
		if name == "" {
			continue
		}
		source := trimToMaxLen(strings.TrimSpace(s.Source), 120)
		if source == "" {
			source = "osquery/programs"
		}
		software = append(software, agentSoftwareItem{
			Name:      name,
			Version:   trimToMaxLen(strings.TrimSpace(s.Version), 120),
			Publisher: trimToMaxLen(strings.TrimSpace(s.Publisher), 300),
			InstallID: trimToMaxLen(strings.TrimSpace(s.InstallID), 1000),
			Serial:    trimToMaxLen(strings.TrimSpace(s.Serial), 1000),
			Source:    source,
		})
	}

	return agentSoftwareEnvelope{
		CollectedAt: collected,
		Software:    software,
	}
}

func buildAgentHardwareEnvelope(report models.InventoryReport, version string) agentHardwareEnvelope {
	collected := strings.TrimSpace(report.CollectedAt)
	if collected == "" {
		collected = time.Now().UTC().Format(time.RFC3339)
	}
	updated := time.Now().UTC().Format(time.RFC3339)

	memTotalBytes := int64(report.Hardware.MemoryGB * 1024 * 1024 * 1024)
	if memTotalBytes < 0 {
		memTotalBytes = 0
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
			MediaType:      trimToMaxLen(strings.TrimSpace(d.Type), 50),
			CollectedAt:    collected,
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
			IPAddress:     trimToMaxLen(firstNonEmptyString(strings.TrimSpace(n.IPv4), strings.TrimSpace(n.IPv6)), 45),
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

	if version == "" {
		version = "dev"
	}
	envelope := agentHardwareEnvelope{
		Hostname:        hostname,
		DisplayName:     trimToMaxLen(hostname, 100),
		Status:          "Online",
		OperatingSystem: osName,
		OSVersion:       osVersion,
		AgentVersion:    trimToMaxLen(strings.TrimSpace(version), 100),
		LastIPAddress:   lastIP,
		MACAddress:      primaryMAC,
		Hardware: agentHardwareInfo{
			InventoryRaw:            rawJSON,
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
			BIOSVersion:             trimToMaxLen(strings.TrimSpace(report.Hardware.BIOSVersion), 100),
			BIOSManufacturer:        trimToMaxLen(strings.TrimSpace(report.Hardware.BIOSVendor), 100),
			OSName:                  osName,
			OSVersion:               osVersion,
			OSBuild:                 trimToMaxLen(strings.TrimSpace(report.OS.Build), 100),
			OSArchitecture:          trimToMaxLen(strings.TrimSpace(report.OS.Architecture), 100),
			CollectedAt:             collected,
			UpdatedAt:               updated,
		},
		Components: agentHardwareComponents{
			Disks:           disks,
			NetworkAdapters: adapters,
			MemoryModules:   modules,
			Printers:        printers,
		},
		InventoryRaw:           rawJSON,
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
	if len(token) <= 8 {
		return token
	}
	return token[:4] + "***" + token[len(token)-4:]
}

func truncateLogBody(body []byte, max int) string {
	if len(body) <= max {
		return string(body)
	}
	return string(body[:max]) + "..."
}
