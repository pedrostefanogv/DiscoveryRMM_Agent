package inventory

import (
	"context"
	"encoding/json"
	"testing"

	"discovery/app/core/models"
)

// TestBuildAgentHardwareEnvelope_EnviaPerfilDeMaquina trava o CONTRATO com a
// API: o objeto hardware precisa expor isLaptop/machineType/hasBattery e o
// objeto components precisa carregar a lista batteries com o estado de tomada.
//
// Sem esse contrato o painel não tem como marcar "Notebook" na listagem nem
// mostrar bateria/energia no detalhe.
func TestBuildAgentHardwareEnvelope_EnviaPerfilDeMaquina(t *testing.T) {
	isLaptop := true
	hasBattery := true
	pcSystemType := 2
	onAC := false
	present := true

	report := models.InventoryReport{
		CollectedAt: "2026-11-23T12:00:00Z",
		Hardware: models.HardwareInfo{
			Hostname:     "NOTE-ANA",
			IsLaptop:     &isLaptop,
			MachineType:  models.MachineTypeNotebook,
			PCSystemType: &pcSystemType,
			HasBattery:   &hasBattery,
		},
		Battery: []models.BatteryInfo{
			{
				Manufacturer:        "SMP",
				Model:               "L19M3PD6",
				PercentRemaining:    63,
				DesignedCapacityMAh: 45000,
				MaxCapacityMAh:      41000,
				Chemistry:           "LiP",
				State:               "Discharging",
				OnAC:                &onAC,
				Present:             &present,
			},
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
	if got := hw["isLaptop"]; got != true {
		t.Fatalf("hardware.isLaptop = %v (%T), esperado true", got, got)
	}
	if got := hw["machineType"]; got != "notebook" {
		t.Fatalf("hardware.machineType = %v, esperado notebook", got)
	}
	if got := hw["hasBattery"]; got != true {
		t.Fatalf("hardware.hasBattery = %v (%T), esperado true", got, got)
	}
	if got, ok := hw["pcSystemType"].(float64); !ok || int(got) != 2 {
		t.Fatalf("hardware.pcSystemType = %v (%T), esperado 2", hw["pcSystemType"], hw["pcSystemType"])
	}

	comp, ok := payload["components"].(map[string]any)
	if !ok {
		t.Fatalf("components deve ser objeto JSON")
	}
	batteries, ok := comp["batteries"].([]any)
	if !ok || len(batteries) != 1 {
		t.Fatalf("components.batteries = %v, esperado 1 item", comp["batteries"])
	}
	bat, _ := batteries[0].(map[string]any)
	if got, ok := bat["onAc"].(bool); !ok || got {
		t.Fatalf("batteries[0].onAc = %v, esperado false", bat["onAc"])
	}
	if got, ok := bat["percentRemaining"].(float64); !ok || int(got) != 63 {
		t.Fatalf("batteries[0].percentRemaining = %v, esperado 63", bat["percentRemaining"])
	}

	// O inventoryRaw continua carregando o bloco de bateria do inventário cru
	// (compatibilidade com agentes/leitores antigos).
	hwRaw, ok := hw["inventoryRaw"].(string)
	if !ok {
		t.Fatalf("hardware.inventoryRaw deve ser string")
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(hwRaw), &raw); err != nil {
		t.Fatalf("inventoryRaw deve conter JSON válido: %v", err)
	}
	if _, ok := raw["battery"].([]any); !ok {
		t.Fatalf("inventoryRaw.battery deve continuar presente, veio %T", raw["battery"])
	}
	if _, ok := raw["hardware"].(map[string]any)["isLaptop"]; !ok {
		t.Fatalf("inventoryRaw.hardware.isLaptop deve estar presente")
	}
}

// TestBuildAgentHardwareEnvelope_PerfilDesconhecidoNaoPolui garante que um
// agente que não conseguiu classificar a máquina NÃO envia isLaptop=false nem
// machineType: a API guarda NULL ("não reportado") e o painel não rotula
// errado. Bateria ausente também não cria lista vazia explícita.
func TestBuildAgentHardwareEnvelope_PerfilDesconhecidoNaoPolui(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-11-23T12:00:00Z",
		Hardware:    models.HardwareInfo{Hostname: "VM-01"},
	}

	env := buildAgentHardwareEnvelope(context.Background(), report, "1.0.0", "", nil)
	body, _ := json.Marshal(env)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	hw := payload["hardware"].(map[string]any)
	if _, present := hw["isLaptop"]; present {
		t.Fatalf("hardware.isLaptop não deveria existir quando desconhecido: %v", hw["isLaptop"])
	}
	if _, present := hw["machineType"]; present {
		t.Fatalf("hardware.machineType não deveria existir quando desconhecido: %v", hw["machineType"])
	}

	comp := payload["components"].(map[string]any)
	if _, present := comp["batteries"]; present {
		t.Fatalf("components.batteries não deveria existir sem bateria: %v", comp["batteries"])
	}
}

// TestClampBatteryPercent trava a defesa contra 255 (sentinela de
// "desconhecido" em SYSTEM_POWER_STATUS) chegando ao painel como "255%".
func TestClampBatteryPercent(t *testing.T) {
	cases := map[int]int{-1: 0, 0: 0, 37: 37, 100: 100, 255: 0, 101: 0}
	for in, want := range cases {
		if got := clampBatteryPercent(in); got != want {
			t.Errorf("clampBatteryPercent(%d) = %d, esperado %d", in, got, want)
		}
	}
}
