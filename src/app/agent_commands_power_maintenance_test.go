package app

import (
	"testing"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/maintenance"
)

// mustParseAgentConfigForMaintenance parseia uma config flat para os testes.
func mustParseAgentConfigForMaintenance(t *testing.T, raw string) agentconfig.AgentConfiguration {
	t.Helper()
	cfg, err := agentconfig.ParseAgentConfiguration([]byte(raw))
	if err != nil {
		t.Fatalf("parse agentconfig: %v", err)
	}
	return cfg
}

// utcAt monta um instante UTC para os testes de janela de manutenção.
func utcAtPower(y, mo, d, h, mi int) time.Time {
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, time.UTC)
}

// Deferral do comando de reboot com janela FECHADA (fora da janela): deve
// adiar com NextStart. Relógio injetado (evaluation pura) — nada roda.
func TestEvaluatePowerMaintenanceDefersOutsideWindow(t *testing.T) {
	win := maintenance.Window{
		Enabled:         true,
		StartUTC:        "02:00",
		DurationMinutes: 60,
	}
	// Dentro da janela: executa normal.
	if d := evaluatePowerMaintenance(win, utcAtPower(2024, 1, 8, 2, 30)); d != nil {
		t.Fatalf("dentro da janela não deveria adiar: %s", d.NextRunAt)
	}
	// Fora da janela: adia para a próxima janela (amanhã 02:00).
	d := evaluatePowerMaintenance(win, utcAtPower(2024, 1, 8, 5, 0))
	if d == nil {
		t.Fatal("fora da janela deveria adiar")
	}
	if want := utcAtPower(2024, 1, 9, 2, 0); !d.NextRunAt.Equal(want) {
		t.Fatalf("nextRunAt=%s, want %s", d.NextRunAt, want)
	}
	// Resultado serializado para o canal de comandos.
	out := maintenanceDeferredResult(d)
	if want := `{"status":"deferred_to_window","nextRunAt":"2024-01-09T02:00:00Z"}`; out != want {
		t.Fatalf("resultado=%s, want %s", out, want)
	}
}

// Janela desligada (kill switch): comportamento atual inalterado.
func TestEvaluatePowerMaintenanceDisabledNeverDefers(t *testing.T) {
	win := maintenance.Window{Enabled: false, StartUTC: "02:00"}
	if d := evaluatePowerMaintenance(win, utcAtPower(2024, 1, 8, 5, 0)); d != nil {
		t.Fatalf("janela desligada não deveria adiar: %s", d.NextRunAt)
	}
}

// deferPowerForMaintenance usa o relógio injetável (maintenanceNow) e a
// config efetiva do App: com clock simulado FORA da janela, adia; DENTRO, não.
func TestDeferPowerForMaintenanceUsesInjectedClock(t *testing.T) {
	a := &App{}
	a.AgentConfig = mustParseAgentConfigForMaintenance(t, `{"maintenanceWindow":{"enabled":true,"startUtc":"02:00","durationMinutes":60}}`)

	orig := maintenanceNow
	defer func() { maintenanceNow = orig }()

	maintenanceNow = func() time.Time { return utcAtPower(2024, 1, 8, 5, 0) } // fora
	if d := a.deferPowerForMaintenance("restart"); d == nil {
		t.Fatal("fora da janela deveria adiar")
	}

	maintenanceNow = func() time.Time { return utcAtPower(2024, 1, 8, 2, 30) } // dentro
	if d := a.deferPowerForMaintenance("restart"); d != nil {
		t.Fatalf("dentro da janela não deveria adiar: %s", d.NextRunAt)
	}
}

// Kill switch enabled=false: nunca adia, independente do relógio.
func TestDeferPowerForMaintenanceKillSwitchOff(t *testing.T) {
	a := &App{}
	a.AgentConfig = mustParseAgentConfigForMaintenance(t, `{"maintenanceWindow":{"enabled":false,"startUtc":"02:00"}}`)
	if d := a.deferPowerForMaintenance("restart"); d != nil {
		t.Fatal("kill switch off não deveria adiar")
	}
}

// rebootGraceSeconds: default 60; 0 explícito = sem aviso.
func TestRebootGraceSeconds(t *testing.T) {
	a := &App{}
	a.AgentConfig = mustParseAgentConfigForMaintenance(t, `{}`)
	if g := a.rebootGraceSeconds(); g != 60 {
		t.Fatalf("grace default=%d, want 60", g)
	}
	a.AgentConfig = mustParseAgentConfigForMaintenance(t, `{"maintenanceWindow":{"enabled":true,"rebootGraceSeconds":0}}`)
	if g := a.rebootGraceSeconds(); g != 0 {
		t.Fatalf("grace explicito=%d, want 0", g)
	}
	a.AgentConfig = mustParseAgentConfigForMaintenance(t, `{"maintenanceWindow":{"enabled":true,"rebootGraceSeconds":120}}`)
	if g := a.rebootGraceSeconds(); g != 120 {
		t.Fatalf("grace config=%d, want 120", g)
	}
}
