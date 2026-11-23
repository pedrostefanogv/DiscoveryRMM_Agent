package inventory

import (
	"context"
	"encoding/json"
	"testing"

	"discovery/app/core/models"
)

// TestBuildAgentHardwareEnvelope_ProductTypeDesconhecidoNaoAfirmaCliente trava
// a semântica "não reportado ≠ não é servidor".
//
// Histórico: quando a leitura do registro falhava (ProductType "unknown"), o
// envelope enviava osIsServer=false. O site confia no campo antes de cair na
// heurística do nome do SO, então um Windows Server real perdia o ícone de
// servidor — e, no agente, a classificação de formato físico podia tratá-lo como
// candidato a notebook (bastava uma UPS exposta como bateria).
//
// Agora os campos são OMITIDOS: a API preserva o último valor conhecido (ou
// mantém NULL) em vez de gravar um false inventado.
func TestBuildAgentHardwareEnvelope_ProductTypeDesconhecidoNaoAfirmaCliente(t *testing.T) {
	for _, productType := range []string{"", "unknown", "server", "WinNT5"} {
		t.Run("productType="+productType, func(t *testing.T) {
			report := models.InventoryReport{
				CollectedAt: "2026-11-23T12:00:00Z",
				Hardware:    models.HardwareInfo{Hostname: "SRV-?"},
				OS: models.OperatingSystem{
					Name:        "Windows Server 2022 Standard",
					ProductType: productType,
					IsServer:    false, // o coletor não conseguiu decidir
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

			if _, presente := hw["osIsServer"]; presente {
				t.Fatalf("osIsServer não deveria ser reportado quando o ProductType é %q (veio %v)", productType, hw["osIsServer"])
			}
			if _, presente := hw["osProductType"]; presente {
				t.Fatalf("osProductType não deveria ser reportado quando o valor é %q (veio %v)", productType, hw["osProductType"])
			}

			// O nome do SO continua indo: é o que permite o site cair na
			// heurística ("Windows Server ...") em vez de afirmar "cliente".
			if got := hw["osName"]; got != "Windows Server 2022 Standard" {
				t.Fatalf("osName = %v, esperado o nome do SO", got)
			}
		})
	}
}

// Valores canônicos continuam sendo reportados como boolean EXPLÍCITO — inclusive
// false, que é resposta positiva de "é cliente" (WinNT).
func TestBuildAgentHardwareEnvelope_ProductTypeCanonicoReportaBooleano(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-11-23T12:00:00Z",
		Hardware:    models.HardwareInfo{Hostname: "PC-1"},
		OS: models.OperatingSystem{
			Name:        "Windows 11 Pro",
			ProductType: "WinNT",
		},
	}

	env := buildAgentHardwareEnvelope(context.Background(), report, "1.0.0", "", nil)
	body, _ := json.Marshal(env)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	hw := payload["hardware"].(map[string]any)

	if got, ok := hw["osIsServer"].(bool); !ok || got {
		t.Fatalf("osIsServer = %v (%T), esperado false explícito em cliente Windows", hw["osIsServer"], hw["osIsServer"])
	}
	if got := hw["osProductType"]; got != "WinNT" {
		t.Fatalf("osProductType = %v, esperado WinNT", got)
	}
}
