package app

import (
	"strings"
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
	// Fora da janela: a avaliação aponta deferral com a próxima janela
	// (amanhã 02:00). ATENÇÃO: para comando de power INTERATIVO isso NÃO
	// causa adiamento — o chamador (remote_debug_commands.go) usa a
	// avaliação apenas para emitir WARN e executa agora.
	d := evaluatePowerMaintenance(win, utcAtPower(2024, 1, 8, 5, 0))
	if d == nil {
		t.Fatal("fora da janela deveria apontar deferral")
	}
	if want := utcAtPower(2024, 1, 9, 2, 0); !d.NextRunAt.Equal(want) {
		t.Fatalf("nextRunAt=%s, want %s", d.NextRunAt, want)
	}
}

// Janela desligada (kill switch): comportamento atual inalterado.
func TestEvaluatePowerMaintenanceDisabledNeverDefers(t *testing.T) {
	win := maintenance.Window{Enabled: false, StartUTC: "02:00"}
	if d := evaluatePowerMaintenance(win, utcAtPower(2024, 1, 8, 5, 0)); d != nil {
		t.Fatalf("janela desligada não deveria adiar: %s", d.NextRunAt)
	}
}

// warnInteractivePowerOutsideMaintenanceWindow usa o relógio injetável
// (maintenanceNow) e a config efetiva do App. Sempre "executa agora": FORA da
// janela → true (janela ignorada, WARN emitido); DENTRO → false; o comando
// interativo NUNCA recebe deferral (a função devolve bool, não struct —
// impossível adiar por contrato).
func TestWarnInteractivePowerOutsideMaintenanceWindow(t *testing.T) {
	// powerApp (agent_commands_power_handler_test.go) cria o App com buffer de
	// logs inicializado: o WARN precisa ser capturável via Logs.GetAll().
	a := powerApp(t, `{"maintenanceWindow":{"enabled":true,"startUtc":"02:00","durationMinutes":60}}`)

	orig := maintenanceNow
	defer func() { maintenanceNow = orig }()

	maintenanceNow = func() time.Time { return utcAtPower(2024, 1, 8, 5, 0) } // fora
	if !a.warnInteractivePowerOutsideMaintenanceWindow("restart") {
		t.Fatal("fora da janela deveria registrar janela ignorada (executa com WARN)")
	}
	// WARN inclui a próxima janela em RFC3339 UTC (2024-01-09 02:00 UTC).
	found := false
	for _, l := range a.Logs.GetAll() {
		if strings.Contains(l, "2024-01-09T02:00:00Z") {
			found = true
		}
	}
	if !found {
		t.Fatalf("WARN deveria citar nextRunAt=2024-01-09T02:00:00Z; logs:\n%s", strings.Join(a.Logs.GetAll(), "\n"))
	}

	maintenanceNow = func() time.Time { return utcAtPower(2024, 1, 8, 2, 30) } // dentro
	if a.warnInteractivePowerOutsideMaintenanceWindow("restart") {
		t.Fatal("dentro da janela não deveria registrar nada")
	}
}

// Comando de power INTERATIVO com janela FECHADA (kill switch off): nunca
// adia — prova do comportamento exigido (restart do console executa).
func TestInteractivePowerWithClosedWindowNeverDefers(t *testing.T) {
	a := &App{}
	a.AgentConfig = mustParseAgentConfigForMaintenance(t, `{"maintenanceWindow":{"enabled":false,"startUtc":"02:00"}}`)
	for _, action := range []string{"restart", "reboot", "shutdown"} {
		if a.warnInteractivePowerOutsideMaintenanceWindow(action) {
			t.Fatalf("%s: kill switch off não deveria registrar janela ignorada", action)
		}
	}
	// Sem config de janela: idem, executa sem WARN e sem deferral.
	a2 := &App{}
	if a2.warnInteractivePowerOutsideMaintenanceWindow("restart") {
		t.Fatal("sem maintenanceWindow não deveria registrar nada")
	}
}

// windowSpanLabel: rótulo "HH:mm-HH:mm" para o WARN (start + duração,
// enrolando a meia-noite quando necessário).
func TestWindowSpanLabel(t *testing.T) {
	win := maintenance.Window{Enabled: true, StartUTC: "02:00", DurationMinutes: 60}
	if got, want := windowSpanLabel(win), "02:00-03:00"; got != want {
		t.Fatalf("span=%s, want %s", got, want)
	}
	cross := maintenance.Window{Enabled: true, StartUTC: "23:30", DurationMinutes: 60}
	if got, want := windowSpanLabel(cross), "23:30-00:30"; got != want {
		t.Fatalf("span cruzando meia-noite=%s, want %s", got, want)
	}
	broken := maintenance.Window{Enabled: true, StartUTC: "x", DurationMinutes: 60}
	if got := windowSpanLabel(broken); got == "02:00-03:00" {
		t.Fatalf("config quebrada deveria dar rótulo genérico, got %s", got)
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
