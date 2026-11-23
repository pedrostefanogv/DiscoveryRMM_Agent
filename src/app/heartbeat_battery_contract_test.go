package app

import (
	"encoding/json"
	"testing"

	"discovery/app/core/agentconn"
)

// TestHeartbeatPayload_EnviaEnergia trava o CONTRATO do heartbeat com a API e
// com o painel: hasBattery/batteryPercent/batteryOnAc/batteryCharging precisam
// sair no JSON quando conhecidos, e ficar AUSENTES quando desconhecidos.
//
// É o canal ao vivo (≈15 s): sem ele o painel só teria o estado de energia do
// inventário de 24 h e diria "na tomada" para um notebook já desplugado.
func TestHeartbeatPayload_EnviaEnergia(t *testing.T) {
	hasBattery := true
	percent := 63
	onAC := false
	charging := false

	hb := agentconn.BuildHeartbeatWithMetrics(agentconn.Config{}, "10.0.0.9", agentconn.AgentHeartbeatMetrics{
		Hostname:        "NOTE-ANA",
		HasBattery:      &hasBattery,
		BatteryPercent:  &percent,
		BatteryOnAc:     &onAC,
		BatteryCharging: &charging,
	})

	body, err := json.Marshal(hb)
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal heartbeat: %v", err)
	}

	if got := payload["hasBattery"]; got != true {
		t.Fatalf("hasBattery = %v (%T), esperado true", got, got)
	}
	if got, ok := payload["batteryPercent"].(float64); !ok || int(got) != 63 {
		t.Fatalf("batteryPercent = %v, esperado 63", payload["batteryPercent"])
	}
	if got, ok := payload["batteryOnAc"].(bool); !ok || got {
		t.Fatalf("batteryOnAc = %v, esperado false", payload["batteryOnAc"])
	}
	if got, ok := payload["batteryCharging"].(bool); !ok || got {
		t.Fatalf("batteryCharging = %v, esperado false", payload["batteryCharging"])
	}
}

// TestHeartbeatPayload_EnergiaDesconhecidaNaoVai garante que desktop/leitura
// incerta não inventa "false" para presença de bateria: os campos simplesmente
// não viajam, e o painel esconde o bloco em vez de mostrar "sem bateria".
func TestHeartbeatPayload_EnergiaDesconhecidaNaoVai(t *testing.T) {
	hb := agentconn.BuildHeartbeatWithMetrics(agentconn.Config{}, "10.0.0.9", agentconn.AgentHeartbeatMetrics{
		Hostname: "VM-01",
	})

	body, _ := json.Marshal(hb)
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal heartbeat: %v", err)
	}

	for _, key := range []string{"hasBattery", "batteryPercent", "batteryOnAc", "batteryCharging"} {
		if _, present := payload[key]; present {
			t.Errorf("%s não deveria existir quando desconhecido (veio %v)", key, payload[key])
		}
	}
}
