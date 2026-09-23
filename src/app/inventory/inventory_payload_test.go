package inventory

import (
	"encoding/json"
	"strings"
	"testing"

	"discovery/app/core/models"
)

func TestBuildAgentHardwareEnvelope_UsesStringStatusAndStringHardwareRawInventory(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-03-12T19:31:36Z",
		Source:      "osquery",
		Hardware: models.HardwareInfo{
			Hostname: "PC-123",
		},
		OS: models.OperatingSystem{
			Name:    "Windows 11 Pro",
			Version: "10.0.26220",
		},
	}

	env := buildAgentHardwareEnvelope(report, "dev", "", nil)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	status, ok := payload["status"].(string)
	if !ok {
		t.Fatalf("status deve ser string, veio %T", payload["status"])
	}
	if status != "Online" {
		t.Fatalf("status = %q, esperado %q", status, "Online")
	}

	// API v1: inventoryRaw na raiz é string (contém JSON válido)
	invRawStr, ok := payload["inventoryRaw"].(string)
	if !ok {
		t.Fatalf("inventoryRaw deve ser string no payload (raiz), veio %T", payload["inventoryRaw"])
	}
	var invRawObj map[string]any
	if err := json.Unmarshal([]byte(invRawStr), &invRawObj); err != nil {
		t.Fatalf("inventoryRaw string deve conter JSON válido: %v", err)
	}

	hw, ok := payload["hardware"].(map[string]any)
	if !ok {
		t.Fatalf("hardware deve ser objeto JSON")
	}
	hwRaw, ok := hw["inventoryRaw"].(string)
	if !ok {
		t.Fatalf("hardware.inventoryRaw deve ser string no payload (contrato da API), veio %T", hw["inventoryRaw"])
	}
	var hwRawObj map[string]any
	if err := json.Unmarshal([]byte(hwRaw), &hwRawObj); err != nil {
		t.Fatalf("hardware.inventoryRaw string deve conter JSON válido: %v", err)
	}
}

func TestBuildAgentHardwareEnvelope_FiltersInvalidRequiredComponents(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-03-12T19:31:36Z",
		Hardware: models.HardwareInfo{
			Hostname: "PC-123",
		},
		OS: models.OperatingSystem{
			Name: "Windows",
		},
		Disks: []models.DiskInfo{
			{Device: "", Label: "sem letra"},
			{Device: "c", Label: "tambem sem letra"},
			{Device: "C:", Label: "valido"},
		},
		Networks: []models.NetworkInfo{
			{FriendlyName: "", Interface: ""},
			{FriendlyName: "Ethernet", Interface: "eth0"},
		},
	}

	env := buildAgentHardwareEnvelope(report, "dev", "", nil)

	// Components agora é json.RawMessage; deserializar para acessar campos
	var comps agentHardwareComponents
	if err := json.Unmarshal(env.Components, &comps); err != nil {
		t.Fatalf("unmarshal components: %v", err)
	}

	if len(comps.Disks) != 2 {
		t.Fatalf("esperado 2 discos validos (somente o vazio deve ser filtrado), veio %d", len(comps.Disks))
	}
	if comps.Disks[1].DriveLetter != "C:" {
		t.Fatalf("driveLetter = %q, esperado %q", comps.Disks[1].DriveLetter, "C:")
	}

	if len(comps.NetworkAdapters) != 1 {
		t.Fatalf("esperado 1 adaptador valido, veio %d", len(comps.NetworkAdapters))
	}
	if comps.NetworkAdapters[0].Name != "Ethernet" {
		t.Fatalf("adapter name = %q, esperado %q", comps.NetworkAdapters[0].Name, "Ethernet")
	}
}

func TestBuildAgentSoftwareEnvelope_AppliesContractLimits(t *testing.T) {
	veryLong := strings.Repeat("x", 2000)
	report := models.InventoryReport{
		Software: []models.SoftwareItem{
			{
				Name:          veryLong,
				Version:       veryLong,
				Publisher:     veryLong,
				InstallID:     veryLong,
				Serial:        veryLong,
				Source:        "",
				InstallDate:   veryLong,
				InstallSource: veryLong,
			},
			{Name: ""},
		},
	}

	env := buildAgentSoftwareEnvelope(report, "test-agent-id", nil, nil, nil)
	if len(env.Software) != 1 {
		t.Fatalf("esperado 1 software valido, veio %d", len(env.Software))
	}
	item := env.Software[0]
	if len(item.Name) > 300 {
		t.Fatalf("name excedeu limite: %d", len(item.Name))
	}
	if len(item.Version) > 120 {
		t.Fatalf("version excedeu limite: %d", len(item.Version))
	}
	if len(item.Publisher) > 300 {
		t.Fatalf("publisher excedeu limite: %d", len(item.Publisher))
	}
	if len(item.InstallID) > 1000 {
		t.Fatalf("installId excedeu limite: %d", len(item.InstallID))
	}
	if len(item.Serial) > 1000 {
		t.Fatalf("serial excedeu limite: %d", len(item.Serial))
	}
	if len(item.InstallDate) > 64 {
		t.Fatalf("installDate excedeu limite: %d", len(item.InstallDate))
	}
	if len(item.InstallSource) > 1000 {
		t.Fatalf("installSource excedeu limite: %d", len(item.InstallSource))
	}
	if item.Source != "native/registry" {
		t.Fatalf("source = %q, esperado fallback %q", item.Source, "native/registry")
	}
}

func TestBuildAgentSoftwareEnvelope_IncludesPendingUpdates(t *testing.T) {
	report := models.InventoryReport{
		Software: []models.SoftwareItem{
			{Name: "Google Chrome", Version: "120.0.0", InstallID: "{CHROME}", Source: "registry"},
			{Name: "Contoso App", Version: "1.0.0", InstallID: "Contoso.App", Source: "registry"},
			{Name: "Notepad++", Version: "8.5", Source: "registry"},
		},
	}
	pending := []models.UpgradeItem{
		{Name: "Google Chrome", ID: "Google.Chrome", CurrentVersion: "120.0.0", AvailableVersion: "121.0.0", Source: "winget"},
		{Name: "Contoso", ID: "Contoso.App", CurrentVersion: "1.0.0", AvailableVersion: "2.0.0", Source: "choco"},
	}

	env := buildAgentSoftwareEnvelope(report, "agent-1", pending, nil, nil)

	byName := map[string]agentSoftwareItem{}
	for _, item := range env.Software {
		byName[item.Name] = item
	}

	chrome := byName["Google Chrome"]
	if !chrome.UpdateAvailable || chrome.AvailableVersion != "121.0.0" || chrome.UpdateSource != "winget" {
		t.Fatalf("chrome update = %+v", chrome)
	}
	if chrome.UpdatePackageID != "Google.Chrome" {
		t.Fatalf("chrome updatePackageId = %q, esperado Google.Chrome", chrome.UpdatePackageID)
	}

	contoso := byName["Contoso App"]
	if !contoso.UpdateAvailable || contoso.AvailableVersion != "2.0.0" || contoso.UpdateSource != "chocolatey" {
		t.Fatalf("contoso update (match por installId) = %+v", contoso)
	}

	if byName["Notepad++"].UpdateAvailable {
		t.Fatalf("notepad++ nao deveria ter update")
	}
}

func TestBuildAgentSoftwareEnvelope_CorrelatesByWingetListID(t *testing.T) {
	// O nome do update difere do nome instalado; a correlação deve acontecer
	// pelo Id do "winget list" para o app do inventário.
	report := models.InventoryReport{
		Software: []models.SoftwareItem{
			{Name: "Notepad++ (32-bit)", Version: "8.5.0", InstallID: "{NOTEPAD}", Source: "registry"},
		},
	}
	pending := []models.UpgradeItem{
		{Name: "Notepad++", ID: "Notepad++.Notepad++", CurrentVersion: "8.5.0", AvailableVersion: "8.6.0", Source: "winget"},
	}
	installed := []models.InstalledPackage{
		{Name: "Notepad++ (32-bit)", ID: "Notepad++.Notepad++", Version: "8.5.0"},
	}

	env := buildAgentSoftwareEnvelope(report, "agent-1", pending, installed, nil)
	// O build do envelope agora aplica o merge dos apps do gerenciador; a lista
	// pode ganhar entradas extras, então localizamos o item por nome.
	item, ok := findSoftwareByName(env, "Notepad++ (32-bit)")
	if !ok {
		t.Fatalf("app do registro nao esta no envelope: %+v", env.Software)
	}
	if !item.UpdateAvailable || item.AvailableVersion != "8.6.0" || item.UpdatePackageID != "Notepad++.Notepad++" {
		t.Fatalf("correlação por winget list falhou: %+v", item)
	}
}

// findSoftwareByName localiza um item do envelope pelo nome (case-insensitive).
func findSoftwareByName(env agentSoftwareEnvelope, name string) (agentSoftwareItem, bool) {
	for _, item := range env.Software {
		if strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(name)) {
			return item, true
		}
	}
	return agentSoftwareItem{}, false
}

func TestBuildAgentSoftwareEnvelope_NameFallbackPrefersMatchingVersion(t *testing.T) {
	report := models.InventoryReport{
		Software: []models.SoftwareItem{
			{Name: "Contoso Tool", Version: "1.0.0", Source: "registry"},
		},
	}
	pending := []models.UpgradeItem{
		{Name: "Contoso Tool", ID: "Contoso.Tool.Old", CurrentVersion: "0.9.0", AvailableVersion: "9.9.9", Source: "winget"},
		{Name: "Contoso Tool", ID: "Contoso.Tool", CurrentVersion: "1.0.0", AvailableVersion: "1.1.0", Source: "winget"},
	}

	env := buildAgentSoftwareEnvelope(report, "agent-1", pending, nil, nil)
	if len(env.Software) != 1 {
		t.Fatalf("esperado 1 software, veio %d", len(env.Software))
	}
	item := env.Software[0]
	if item.AvailableVersion != "1.1.0" || item.UpdatePackageID != "Contoso.Tool" {
		t.Fatalf("esperava o update da versao 1.0.0, veio %+v", item)
	}
}

func TestBuildAgentHardwareEnvelope_IncludesPrintersInComponents(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-03-12T19:31:36Z",
		Hardware: models.HardwareInfo{
			Hostname: "PC-123",
		},
		OS: models.OperatingSystem{
			Name: "Windows 11 Pro",
		},
		Printers: []models.PrinterInfo{
			{
				Name:             "HP LaserJet Pro M404",
				DriverName:       "HP Universal Printing PCL 6",
				PortName:         "IP_192.168.1.50",
				PrinterStatus:    "Ready",
				IsDefault:        true,
				IsNetworkPrinter: true,
				Shared:           false,
				Location:         "Financeiro",
			},
		},
	}

	env := buildAgentHardwareEnvelope(report, "dev", "", nil)

	// Components agora é json.RawMessage; deserializar para acessar campos
	var comps agentHardwareComponents
	if err := json.Unmarshal(env.Components, &comps); err != nil {
		t.Fatalf("unmarshal components: %v", err)
	}

	if len(comps.Printers) != 1 {
		t.Fatalf("esperado 1 impressora no components.printers, veio %d", len(comps.Printers))
	}
	p := comps.Printers[0]
	if p.Name != "HP LaserJet Pro M404" {
		t.Fatalf("name = %q", p.Name)
	}
	if p.DriverName != "HP Universal Printing PCL 6" {
		t.Fatalf("driverName = %q", p.DriverName)
	}
	if p.PortName != "IP_192.168.1.50" {
		t.Fatalf("portName = %q", p.PortName)
	}
	if !p.IsDefault || !p.IsNetworkPrinter {
		t.Fatalf("flags de impressora invalidas: isDefault=%v isNetworkPrinter=%v", p.IsDefault, p.IsNetworkPrinter)
	}
	if p.ShareName != nil {
		t.Fatalf("shareName esperado nil para impressora nao compartilhada, veio %v", *p.ShareName)
	}
}
