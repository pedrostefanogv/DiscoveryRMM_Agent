package inventory

import (
	"context"
	"encoding/json"
	"testing"

	"discovery/app/core/models"
)

// TestBuildAgentHardwareEnvelope_EnviaCamposDeWindowsServer trava o CONTRATO
// com a API: o objeto hardware precisa expor osIsServer/osProductType/osDomain/
// osWorkgroup/osPartOfDomain, e o inventoryRaw também precisa carregar
// isServer/productType/domain/workgroup/partOfDomain no objeto de SO.
func TestBuildAgentHardwareEnvelope_EnviaCamposDeWindowsServer(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-05-01T10:00:00Z",
		Hardware: models.HardwareInfo{
			Hostname: "SRV-DC01",
		},
		OS: models.OperatingSystem{
			Name:         "Windows Server 2022 Standard",
			Version:      "10.0.20348",
			IsServer:     true,
			ProductType:  "LanmanNT",
			Domain:       "empresa.local",
			PartOfDomain: true,
		},
	}

	env := buildAgentHardwareEnvelope(context.Background(), report, "1.0.0", "", nil)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	hw, ok := payload["hardware"].(map[string]any)
	if !ok {
		t.Fatalf("hardware deve ser objeto JSON")
	}

	if got, ok := hw["osIsServer"].(bool); !ok || !got {
		t.Fatalf("hardware.osIsServer = %v (%T), esperado true (bool)", hw["osIsServer"], hw["osIsServer"])
	}
	if got := hw["osProductType"]; got != "LanmanNT" {
		t.Fatalf("hardware.osProductType = %v, esperado LanmanNT", got)
	}
	if got := hw["osDomain"]; got != "empresa.local" {
		t.Fatalf("hardware.osDomain = %v, esperado empresa.local", got)
	}
	if got, ok := hw["osPartOfDomain"].(bool); !ok || !got {
		t.Fatalf("hardware.osPartOfDomain = %v (%T), esperado true (bool)", hw["osPartOfDomain"], hw["osPartOfDomain"])
	}

	// inventoryRaw é o InventoryReport completo serializado: o objeto de SO
	// completo (models.OperatingSystem) vive em "os", com os campos novos.
	hwRaw, ok := hw["inventoryRaw"].(string)
	if !ok {
		t.Fatalf("hardware.inventoryRaw deve ser string")
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(hwRaw), &raw); err != nil {
		t.Fatalf("inventoryRaw deve conter JSON válido: %v", err)
	}
	osRaw, ok := raw["os"].(map[string]any)
	if !ok {
		t.Fatalf("inventoryRaw.os deve ser objeto JSON, veio %T", raw["os"])
	}
	if got := osRaw["productType"]; got != "LanmanNT" {
		t.Fatalf("inventoryRaw.os.productType = %v, esperado LanmanNT", got)
	}
	if got := osRaw["domain"]; got != "empresa.local" {
		t.Fatalf("inventoryRaw.os.domain = %v, esperado empresa.local", got)
	}
	if got, ok := osRaw["isServer"].(bool); !ok || !got {
		t.Fatalf("inventoryRaw.os.isServer = %v, esperado true", osRaw["isServer"])
	}
	if got, ok := osRaw["partOfDomain"].(bool); !ok || !got {
		t.Fatalf("inventoryRaw.os.partOfDomain = %v, esperado true", osRaw["partOfDomain"])
	}
}

// TestBuildAgentHardwareEnvelope_ClienteNaoEhServidor garante que em cliente
// Windows os campos saem neutros (osIsServer=false, osPartOfDomain=false) e
// que os campos opcionais vazios não poluem o payload.
func TestBuildAgentHardwareEnvelope_ClienteNaoEhServidor(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-05-01T10:00:00Z",
		Hardware: models.HardwareInfo{
			Hostname: "PC-123",
		},
		OS: models.OperatingSystem{
			Name:        "Windows 11 Pro",
			Version:     "10.0.26200",
			ProductType: "WinNT",
			Workgroup:   "WORKGROUP",
		},
	}

	env := buildAgentHardwareEnvelope(context.Background(), report, "1.0.0", "", nil)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	hw := payload["hardware"].(map[string]any)

	if got := hw["osIsServer"]; got != false {
		t.Fatalf("hardware.osIsServer = %v, esperado false em cliente", got)
	}
	if got := hw["osPartOfDomain"]; got != false {
		t.Fatalf("hardware.osPartOfDomain = %v, esperado false em cliente", got)
	}
	if got := hw["osProductType"]; got != "WinNT" {
		t.Fatalf("hardware.osProductType = %v, esperado WinNT", got)
	}
	if got := hw["osWorkgroup"]; got != "WORKGROUP" {
		t.Fatalf("hardware.osWorkgroup = %v, esperado WORKGROUP", got)
	}
	if _, existe := hw["osDomain"]; existe {
		t.Fatalf("osDomain deveria ser omitido quando vazio, veio %v", hw["osDomain"])
	}
}
