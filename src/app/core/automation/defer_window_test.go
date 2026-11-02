package automation

import (
	"testing"
	"time"

	"discovery/app/core/maintenance"
)

func spLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	return loc
}

func activeWindowAt(t *testing.T, startLocal string, duration int) maintenance.Window {
	t.Helper()
	return maintenance.Window{
		Enabled:         true,
		StartUTC:        "05:00",
		StartLocal:      startLocal,
		Location:        spLocation(t),
		DurationMinutes: duration,
	}
}

// deferTaskRecord captura o agendamento feito pelo deferPackageActionToWindow
// (hook de teste/observabilidade no lugar de scheduleDeferRetry).
type deferTaskRecord struct {
	agentID   string
	taskID    string
	trigger   TriggerType
	nextStart time.Time
}

func deferTestService(hook func(deferTaskRecord)) *Service {
	svc := NewService(func() RuntimeConfig {
		return RuntimeConfig{AgentID: "agent-1"}
	}, func(string) {})
	svc.deferRetryHook = func(agentID string, task AutomationTask, _ AutomationExecutionSourceType, triggerType TriggerType, at time.Time) {
		hook(deferTaskRecord{agentID: agentID, taskID: task.TaskID, trigger: triggerType, nextStart: at})
	}
	return svc
}

func deferTestTask() AutomationTask {
	return AutomationTask{
		TaskID:                       "task-1",
		ActionType:                   ActionInstallPackage,
		InstallationType:             InstallationWinget,
		PackageID:                    "pkg.teste",
		TaskRespectMaintenanceWindow: true,
	}
}

// FORA da janela ativa (com Location IANA): devolve true e agenda o retry no
// NextStart da janela.
func TestDeferPackageActionToWindowForaDaJanelaIANA(t *testing.T) {
	win := activeWindowAt(t, "02:00", 60)
	svc := deferTestService(nil)
	svc.SetMaintenanceWindowSource(func() maintenance.Window { return win })

	// Fora da janela 02:00-03:00 America/Sao_Paulo. O instante de referência
	// do defer é time.Now() real (o método não recebe clock); o teste valida a
	// FORMA do NextStart (02:00 SP, futuro), não a data absoluta.
	loc := spLocation(t)
	task := deferTestTask()
	var got deferTaskRecord
	svc.deferRetryHook = func(agentID string, task AutomationTask, _ AutomationExecutionSourceType, triggerType TriggerType, at time.Time) {
		got = deferTaskRecord{agentID: agentID, taskID: task.TaskID, trigger: triggerType, nextStart: at}
	}

	deferred := svc.deferPackageActionToWindow("agent-1", task, sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate)
	if !deferred {
		t.Fatal("fora da janela deveria adiar (true)")
	}
	if got.agentID != "agent-1" || got.taskID != "task-1" || got.trigger != TriggerTypeImmediate {
		t.Fatalf("registro inesperado: %+v", got)
	}
	// O método usa time.Now() real: o esperado é o início da PRÓXIMA janela
	// (02:00 America/Sao_Paulo — dias todos habilitados), sempre no futuro.
	if got.nextStart.IsZero() {
		t.Fatal("nextStart deveria estar preenchido")
	}
	if !got.nextStart.After(time.Now()) {
		t.Fatalf("nextStart deveria estar no futuro: %s", got.nextStart)
	}
	if inSP := got.nextStart.In(loc); inSP.Format("15:04") != "02:00" {
		t.Fatalf("nextStart deveria ser 02:00 America/Sao_Paulo, veio %s", inSP.Format("15:04"))
	}
}

// DENTRO da janela: sem defer (false) — executa agora.
func TestDeferPackageActionToWindowDentroDaJanela(t *testing.T) {
	win := activeWindowAt(t, "02:00", 1440) // janela do dia inteiro: sempre dentro
	svc := deferTestService(func(rec deferTaskRecord) {
		t.Fatalf("não deveria agendar dentro da janela: %+v", rec)
	})
	svc.SetMaintenanceWindowSource(func() maintenance.Window { return win })
	if svc.deferPackageActionToWindow("agent-1", deferTestTask(), sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate) {
		t.Fatal("dentro da janela deveria executar (false)")
	}
}

// Janela desligada: sem defer (false) — sem gate.
func TestDeferPackageActionToWindowJanelaDesligada(t *testing.T) {
	win := activeWindowAt(t, "02:00", 60)
	win.Enabled = false
	svc := deferTestService(func(rec deferTaskRecord) {
		t.Fatalf("não deveria agendar com janela desligada: %+v", rec)
	})
	svc.SetMaintenanceWindowSource(func() maintenance.Window { return win })
	if svc.deferPackageActionToWindow("agent-1", deferTestTask(), sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate) {
		t.Fatal("janela desligada deveria executar (false)")
	}
}

// Task com TaskRespectMaintenanceWindow=false: o executor não aplicaria o
// gate — o defer também não se aplica.
func TestDeferPackageActionToWindowFlagOff(t *testing.T) {
	win := activeWindowAt(t, "02:00", 60)
	svc := deferTestService(func(rec deferTaskRecord) {
		t.Fatalf("não deveria agendar com flag off: %+v", rec)
	})
	svc.SetMaintenanceWindowSource(func() maintenance.Window { return win })
	task := deferTestTask()
	task.TaskRespectMaintenanceWindow = false
	if svc.deferPackageActionToWindow("agent-1", task, sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate) {
		t.Fatal("flag off deveria executar (false)")
	}
}

// Defensivos: fonte nil, task de script e NextStart zero.
func TestDeferPackageActionToWindowDefensivos(t *testing.T) {
	win := activeWindowAt(t, "02:00", 60)

	// Fonte de janela nil (agente sem config): comportamento legado.
	svc := deferTestService(nil)
	if svc.deferPackageActionToWindow("agent-1", deferTestTask(), sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate) {
		t.Fatal("fonte nil deveria executar (false)")
	}

	// Task de script (não é ação de pacote): sem defer.
	svc = deferTestService(nil)
	svc.SetMaintenanceWindowSource(func() maintenance.Window { return win })
	script := deferTestTask()
	script.ActionType = ActionRunScript
	if svc.deferPackageActionToWindow("agent-1", script, sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate) {
		t.Fatal("script não deveria adiar (false)")
	}

	// Janela ativa com horário de início inválido: NextStart zero → sem defer.
	broken := maintenance.Window{Enabled: true, StartUTC: "25:99", DurationMinutes: 60}
	svc = deferTestService(func(rec deferTaskRecord) {
		t.Fatalf("não deveria agendar com NextStart zero: %+v", rec)
	})
	svc.SetMaintenanceWindowSource(func() maintenance.Window { return broken })
	if svc.deferPackageActionToWindow("agent-1", deferTestTask(), sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate) {
		t.Fatal("NextStart zero deveria executar (false)")
	}
}
