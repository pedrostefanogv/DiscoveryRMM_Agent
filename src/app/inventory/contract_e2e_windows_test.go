package inventory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"discovery/app/core/models"
)

// TestContratoFimAFim_WindowsServer trava o CONTRATO entre o agente Go e o
// parser C# (AgentHardwareHandlers). Cada chave abaixo É LIDA pelo servidor:
// se um lado renomear, este teste quebra e evita o "campo que nunca chega".
func TestContratoFimAFim_WindowsServer(t *testing.T) {
	report := models.InventoryReport{}
	report.OS.IsServer = true
	report.OS.ProductType = "LanmanNT"
	report.OS.Domain = "corp.local"
	report.OS.PartOfDomain = true

	env := buildAgentHardwareEnvelope(context.Background(), report, "1.0.0", "", nil)
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Chaves que o parser C# lê (osIsServer/osProductType/osDomain/osWorkgroup/osPartOfDomain)
	for _, key := range []string{"osIsServer", "osProductType", "osDomain", "osPartOfDomain"} {
		if !strings.Contains(string(raw), "\""+key+"\"") {
			t.Errorf("chave %q ausente no payload — o servidor NAO vai ler este campo", key)
		}
	}

	// Em DC (LanmanNT) os coletores de servidor devem ter rodado e os campos
	// existirem no JSON (mesmo que vazios na coleta real desta maquina de teste).
	for _, key := range []string{"serverHardware", "hyperV", "cluster", "storageRds"} {
		if !strings.Contains(string(raw), "\""+key+"\"") {
			t.Errorf("campo de inventario de servidor %q ausente (IsServer=true)", key)
		}
	}

	t.Logf("contrato OK; payload tem %d bytes", len(raw))
}
