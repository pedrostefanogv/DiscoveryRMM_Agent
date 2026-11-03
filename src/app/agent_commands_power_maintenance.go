package app

import (
	"fmt"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/maintenance"
)

// ── Janela de manutenção vs. comando de power INTERATIVO ──
//
// A janela de manutenção (maintenanceWindow) vale para AUTOMAÇÃO (patch,
// core/automation com TaskRespectMaintenanceWindow) e autoupdate. Um comando
// de power INTERATIVO (restart/reboot/shutdown que chega pelo canal de
// comandos a partir do console) NUNCA é adiado por ela: o operador pediu a
// ação agora, e o deferral antigo era um beco sem saída (nada reagendava a
// execução em NextRunAt e o resultado voltava exitCode=0 — sucesso falso).
//
// Quando a janela está ativa e "agora" está FORA dela, o comando segue o
// fluxo normal e um log WARN registra que a janela foi ignorada. force=true
// sempre executa, independente de janela (reforçado no fluxo: o comando
// interativo nem chega a avaliar deferral).

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

// windowSpanLabel monta o rótulo "HH:mm-HH:mm" da janela para logs, na zona
// em que ela é avaliada (local de Location + StartLocal quando configurados,
// senão o legado StartUTC em UTC). Config quebrada (horário inválido) →
// rótulo genérico. Usa apenas API exportada de maintenance (o pacote não
// expõe usesLocalClock/duration) e replica o default de duração (60min).
func windowSpanLabel(win maintenance.Window) string {
	clock, durMin := win.StartUTC, agentconfig.DefaultMaintenanceWindowDurationMinutes
	if win.Location != nil {
		if _, ok := maintenance.ParseClock(win.StartLocal); ok {
			clock = win.StartLocal
		}
	}
	if win.DurationMinutes > 0 {
		durMin = win.DurationMinutes
	}
	startMin, ok := maintenance.ParseClock(clock)
	if !ok {
		return "janela de manutenção"
	}
	endMin := (startMin + durMin) % 1440
	return fmt.Sprintf("%02d:%02d-%02d:%02d", startMin/60, startMin%60, endMin/60, endMin%60)
}

// warnInteractivePowerOutsideMaintenanceWindow avalia a janela de manutenção
// para um comando de power INTERATIVO (canal de comandos do console).
// NUNCA adia: quando a janela está ativa e "agora" está FORA dela, emite o
// log WARN correspondente e retorna true para o chamador registrar que a
// janela foi ignorada. Janela desligada (kill switch), sem config ou dentro
// da janela → false, fluxo normal.
func (a *App) warnInteractivePowerOutsideMaintenanceWindow(action string) bool {
	if a == nil {
		return false
	}
	cfg := a.GetAgentConfiguration()
	if cfg.MaintenanceWindow == nil {
		return false
	}
	win, _ := cfg.MaintenanceWindow.Effective()
	if !win.Enabled {
		return false
	}
	d := evaluatePowerMaintenance(win, maintenanceNow())
	if d == nil {
		// Dentro da janela: nada a registrar, segue o fluxo normal.
		return false
	}
	// LogBuffer.Append é nil-safe (Buffer não inicializado → no-op), o que
	// mantém o helper testável com um App{} zerado.
	a.Logs.Append(fmt.Sprintf("[agent] %s-action [MAINTENANCE] janela ativa (%s) mas comando interativo — executando agora (próxima janela: %s)", action, windowSpanLabel(win), nextRunAtRFC3339(d.NextRunAt)))
	return true
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

// nextRunAtRFC3339 formata o início da próxima janela para o WARN (RFC3339
// UTC). Zero time (janela sem próxima ocorrência agendável) → "desconhecida".
func nextRunAtRFC3339(next time.Time) string {
	if next.IsZero() {
		return "desconhecida"
	}
	return next.UTC().Format(time.RFC3339)
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
