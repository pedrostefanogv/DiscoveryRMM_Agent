package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"discovery/app/agentcommands"
)

// ── Power Action Commands ──

type powerCommandPayload = agentcommands.PowerCommandPayload

// powerActionRestartFirmware é o verbo do comando genérico "poweraction" que
// reinicia a máquina direto no setup do firmware (BIOS/UEFI). Espelha
// agentcommands.PowerActionRestartFirmware.
const powerActionRestartFirmware = agentcommands.PowerActionRestartFirmware

// isPowerActionCommandType checks whether cmdType is a restart/reboot/shutdown command.
func isPowerActionCommandType(cmdType string) bool {
	return agentcommands.IsPowerActionCommandType(cmdType)
}

// parsePowerCommandPayload extracts delaySeconds, force, and message from the raw payload.
func parsePowerCommandPayload(payload any) powerCommandPayload {
	return agentcommands.ParsePowerCommandPayload(payload)
}

// powerExecutionMode descreve COMO o comando de power é aplicado.
type powerExecutionMode int

const (
	// powerModeNotify: nosso aviso nativo com contador (cartão notifyui).
	powerModeNotify powerExecutionMode = iota
	// powerModeSilentTimer: NENHUMA notificação nativa do Windows — o atraso é
	// contado pelo agente e o SO é acionado com /t 0.
	powerModeSilentTimer
)

// powerStrategy é a decisão pura (testável) de como aplicar um comando de power.
type powerStrategy struct {
	Mode         powerExecutionMode
	DelaySeconds int
	Force        bool
	// Deferrable indica se o usuário pode adiar na nossa notificação.
	Deferrable bool
}

// resolvePowerStrategy decide o modo de execução a partir do payload:
//   - notifyUser=true  → notificação Fluent (adiavel somente se force=false);
//   - notifyUser=false → timer interno, sem diálogo nativo do Windows.
func resolvePowerStrategy(pp powerCommandPayload) powerStrategy {
	mode := powerModeNotify
	if !pp.NotifyUser {
		mode = powerModeSilentTimer
	}
	return powerStrategy{
		Mode:         mode,
		DelaySeconds: pp.DelaySeconds,
		Force:        pp.Force,
		// Adiável só existe na nossa notificação (notifyUser) e nunca com
		// force=true; no modo silencioso não há nada a adiar.
		Deferrable: mode == powerModeNotify && !pp.Force,
	}
}

// cancelSilentPowerAction cancela o timer interno de power silencioso, se
// houver um pendente. Usado quando chega um novo comando de power e no
// shutdown do agente.
func (a *App) cancelSilentPowerAction() {
	if a == nil {
		return
	}
	a.silentPowerMu.Lock()
	defer a.silentPowerMu.Unlock()
	if a.silentPowerTimer != nil {
		a.silentPowerTimer.Stop()
		a.silentPowerTimer = nil
	}
}

// scheduleSilentPowerAction agenda restart/shutdown SEM notificação nativa:
// espera delaySeconds internamente e executa shutdown.exe com /t 0 (é o /t N>0
// que faz o Windows exibir o diálogo nativo de contagem).
func (a *App) scheduleSilentPowerAction(action string, pp powerCommandPayload) {
	if a == nil {
		return
	}
	delay := pp.DelaySeconds
	if delay < 0 {
		delay = 0
	}
	force := pp.Force
	a.Logs.Append(fmt.Sprintf("[agent] %s-action [SILENT-TIMER] sem aviso nativo — executa em %ds (force=%t)", action, delay, force))

	// Cancela um timer anterior: um novo comando de power substitui o antigo.
	a.silentPowerMu.Lock()
	if a.silentPowerTimer != nil {
		a.silentPowerTimer.Stop()
	}
	a.silentPowerTimer = time.AfterFunc(time.Duration(delay)*time.Second, func() {
		_, _, _ = a.executeSystemPowerAction(context.Background(), action, 0, force, "")
	})
	a.silentPowerMu.Unlock()
}

// resolvePowerVerb traduz o cmdType (e, no tipo genérico "poweraction", o campo
// action do payload) no verbo interno consumido por executeSystemPowerAction.
// Tipos legados: restart/reboot → "restart"; shutdown → "shutdown".
func resolvePowerVerb(cmdType string, pp powerCommandPayload) string {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "shutdown":
		return "shutdown"
	case "poweraction":
		return strings.ToLower(strings.TrimSpace(pp.Action))
	default:
		return "restart"
	}
}

// handleFirmwareRestart executa o verbo "restart-firmware": reinicia a máquina
// direto no setup do firmware (BIOS/UEFI) via shutdown.exe /r /fw.
//
// Fluxo próprio, mais simples que restart/shutdown: o operador pediu
// explicitamente o setup do firmware, que reinicia a máquina logo em seguida —
// não há contador/adiamento do PSADT. O delay (quando informado) é contado pelo
// timer interno silencioso, como no modo notifyUser=false.
//
// Capacidade: em BIOS legado (Legacy/CSM) o /fw é recusado pelo Windows. Nesse
// caso devolvemos uma falha TIPADA no contrato (status=failed + message clara)
// em vez de agendar um reinício inútil.
func (a *App) handleFirmwareRestart(parent context.Context, pp powerCommandPayload) (bool, int, string, string) {
	if parent == nil {
		parent = context.Background()
	}
	a.cancelDeferredRestart()
	a.cancelSilentPowerAction()

	if ok, reason := firmwareBootSupportFunc(); !ok {
		msg := "reinicio no firmware (BIOS/UEFI) indisponivel: " + reason
		a.Logs.Append(fmt.Sprintf("[agent] %s [UNSUPPORTED] %s", powerActionRestartFirmware, reason))
		return true, 1, newPowerOutput("failed", powerActionRestartFirmware, msg).line(), msg
	}

	a.warnInteractivePowerOutsideMaintenanceWindow(powerActionRestartFirmware)

	if pp.DelaySeconds > 0 {
		a.scheduleSilentPowerAction(powerActionRestartFirmware, pp)
		msg := fmt.Sprintf("reinicio no firmware agendado em %ds", pp.DelaySeconds)
		out := newPowerOutput("scheduled", powerActionRestartFirmware, msg)
		out.DelaySeconds = pp.DelaySeconds
		return true, 0, out.line(), ""
	}

	exitCode, output, errText := a.executePowerWithContract(parent, powerActionRestartFirmware, pp.Force, pp.Message)
	return true, exitCode, output, errText
}
