package app

import (
	"testing"
	"time"

	"discovery/app/core/agentconn"
)

// TestMergeHeartbeatMetrics_CopiesLoggedUser é a regressão do bug em que
// getHeartbeatMetrics remonta o struct e o merge descartava o usuário logado,
// fazendo o heartbeat real sair sem o campo.
func TestMergeHeartbeatMetrics_CopiesLoggedUser(t *testing.T) {
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	dst := &agentconn.AgentHeartbeatMetrics{}
	src := &agentconn.AgentHeartbeatMetrics{LoggedUser: "CORP\\pedro", LoggedUserSince: since}

	mergeHeartbeatMetrics(dst, src)

	if dst.LoggedUser != "CORP\\pedro" {
		t.Fatalf("LoggedUser = %q, want CORP\\pedro", dst.LoggedUser)
	}
	if !dst.LoggedUserSince.Equal(since) {
		t.Fatalf("LoggedUserSince = %v, want %v", dst.LoggedUserSince, since)
	}
}

// Sem sessão o merge deve preservar o vazio do destino (o heartbeat envia ""
// de propósito, sinalizando "agent novo sem sessão").
func TestMergeHeartbeatMetrics_KeepsEmptyLoggedUser(t *testing.T) {
	dst := &agentconn.AgentHeartbeatMetrics{}
	mergeHeartbeatMetrics(dst, &agentconn.AgentHeartbeatMetrics{})

	if dst.LoggedUser != "" {
		t.Fatalf("LoggedUser = %q, want empty", dst.LoggedUser)
	}
}

// TestMergeHeartbeatMetrics_CopiesEnergiaDaBateria é a MESMA regressão, agora
// para a energia: getHeartbeatMetrics remonta o struct e este merge recopia
// campo a campo. Sem as linhas de energia, o CollectHeartbeatMetrics lia a
// bateria e o heartbeat real saía SEM ela — o painel nunca mostraria "na
// tomada" ao vivo, só o retrato de 24 h do inventário.
func TestMergeHeartbeatMetrics_CopiesEnergiaDaBateria(t *testing.T) {
	hasBattery := true
	percent := 63
	onAC := false
	charging := false

	dst := &agentconn.AgentHeartbeatMetrics{}
	src := &agentconn.AgentHeartbeatMetrics{
		HasBattery:      &hasBattery,
		BatteryPercent:  &percent,
		BatteryOnAc:     &onAC,
		BatteryCharging: &charging,
	}

	mergeHeartbeatMetrics(dst, src)

	if dst.HasBattery == nil || *dst.HasBattery != hasBattery {
		t.Fatalf("HasBattery = %v, want %v", dst.HasBattery, hasBattery)
	}
	if dst.BatteryPercent == nil || *dst.BatteryPercent != percent {
		t.Fatalf("BatteryPercent = %v, want %d", dst.BatteryPercent, percent)
	}
	if dst.BatteryOnAc == nil || *dst.BatteryOnAc != onAC {
		t.Fatalf("BatteryOnAc = %v, want %v", dst.BatteryOnAc, onAC)
	}
	if dst.BatteryCharging == nil || *dst.BatteryCharging != charging {
		t.Fatalf("BatteryCharging = %v, want %v", dst.BatteryCharging, charging)
	}
}

// Leitura indisponível (API falhou/desconhecida) não pode virar false: o
// destino precisa continuar nil para o painel esconder o bloco em vez de
// afirmar "sem bateria" em um notebook real.
func TestMergeHeartbeatMetrics_SemEnergiaContinuaNil(t *testing.T) {
	dst := &agentconn.AgentHeartbeatMetrics{}
	mergeHeartbeatMetrics(dst, &agentconn.AgentHeartbeatMetrics{})

	if dst.HasBattery != nil || dst.BatteryPercent != nil || dst.BatteryOnAc != nil || dst.BatteryCharging != nil {
		t.Fatalf("energia deveria continuar nil: has=%v percent=%v onAC=%v charging=%v",
			dst.HasBattery, dst.BatteryPercent, dst.BatteryOnAc, dst.BatteryCharging)
	}
}
