package app

import (
	"fmt"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/maintenance"
)

// ── Janela de manutenção: deferral de comandos de power ──
//
// Quando maintenanceWindow.enabled=true e "agora" está FORA da janela, um
// comando restart/reboot/shutdown do servidor é ADIADO: nada é executado e o
// resultado volta pelo canal de comandos existente com status
// "deferred_to_window" e nextRunAt = início da próxima janela. Janela
// desligada (kill switch) ou dentro da janela → comportamento atual.

// maintenanceNow é o relógio usado pela avaliação da janela. Variável de
// pacote para injeção simples nos testes (sem parametrizar toda a cadeia).
var maintenanceNow = time.Now

// powerMaintenanceDeferral é a decisão pura (testável) de adiar um comando de
// power por janela de manutenção. nil = executa normalmente.
type powerMaintenanceDeferral struct {
	NextRunAt time.Time
}

// evaluatePowerMaintenance decide com base na Window e no instante informado.
func evaluatePowerMaintenance(win maintenance.Window, now time.Time) *powerMaintenanceDeferral {
	d := win.Evaluate(now)
	if !d.Deferred {
		return nil
	}
	return &powerMaintenanceDeferral{NextRunAt: d.NextRunAt}
}

// deferPowerForMaintenance aplica a avaliação com a config efetiva e o
// relógio corrente. nil = seguir o fluxo normal do comando.
func (a *App) deferPowerForMaintenance(action string) *powerMaintenanceDeferral {
	if a == nil {
		return nil
	}
	cfg := a.GetAgentConfiguration()
	if cfg.MaintenanceWindow == nil {
		return nil
	}
	win, _ := cfg.MaintenanceWindow.Effective()
	if !win.Enabled {
		return nil
	}
	return evaluatePowerMaintenance(win, maintenanceNow())
}

// maintenanceDeferredResult serializa o resultado de deferral para o canal de
// comandos (status + nextRunAt em RFC3339 UTC).
func maintenanceDeferredResult(d *powerMaintenanceDeferral) string {
	next := ""
	if d != nil && !d.NextRunAt.IsZero() {
		next = d.NextRunAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(`{"status":"deferred_to_window","nextRunAt":%q}`, next)
}

// ── Reboot coordenado: aviso (grace) ao usuário ──

// rebootGraceSeconds devolve o atraso de aviso antes do reboot coordenado na
// execução SILENCIOSA (notifyUser=false): rebootGraceSeconds da config
// (default 60; 0 = sem aviso, reboot imediato como hoje).
func (a *App) rebootGraceSeconds() int {
	if a == nil {
		return 0
	}
	cfg := a.GetAgentConfiguration()
	if cfg.MaintenanceWindow == nil {
		return agentconfig.DefaultRebootGraceSeconds
	}
	_, grace := cfg.MaintenanceWindow.Effective()
	return grace
}

// notifyRebootGrace mostra o toast local "Reboot agendado pela TI em Ns —
// salve seu trabalho" reaproveitando o pipeline de notificações existente
// (DispatchNotification, layout toast, sem confirmação). O countdown em si é
// o timer silencioso do agente (scheduleSilentPowerAction), cancelável pelo
// mesmo mecanismo já usado hoje: um novo comando de power (ou o cancelamento
// do servidor) chama cancelSilentPowerAction e aborta o reboot pendente.
func (a *App) notifyRebootGrace(action string, graceSeconds int) {
	if a == nil || graceSeconds <= 0 {
		return
	}
	verb := "Reinicialização"
	if action == "shutdown" {
		verb = "Desligamento"
	}
	a.DispatchNotification(NotificationDispatchRequest{
		NotificationID: fmt.Sprintf("reboot-grace-%d", time.Now().UnixNano()),
		Title:          fmt.Sprintf("%s agendada pela TI", verb),
		Message:        fmt.Sprintf("%s agendada pela TI em %ds — salve seu trabalho.", verb, graceSeconds),
		Mode:           "notify_only",
		Severity:       "high",
		EventType:      "system_restart",
		Layout:         "toast",
		TimeoutSeconds: graceSeconds,
	})
	a.Logs.Append(fmt.Sprintf("[agent] %s-action [GRACE] toast de %ds exibido ao usuário", action, graceSeconds))
}
