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

// Verbos do comando genérico "poweraction". Espelham as constantes de
// agentcommands (fonte única dos valores de wire).
const (
	powerActionRestartFirmware = agentcommands.PowerActionRestartFirmware
	powerActionRestartAdvanced = agentcommands.PowerActionRestartAdvanced
	powerActionCancel          = agentcommands.PowerActionCancel
	powerActionLock            = agentcommands.PowerActionLock
)

// isPowerActionVerb informa se o verbo (campo action) pertence ao comando
// genérico "poweraction".
func isPowerActionVerb(verb string) bool {
	return agentcommands.IsPowerActionVerb(verb)
}

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

// powerVerbSpec descreve COMO um verbo do comando genérico "poweraction" é
// aplicado pelo agent.
//
//   - ProbeFirmwareUEFI: exige boot UEFI (verbo restart-firmware). Em BIOS
//     legado o Windows recusa o /fw, então devolvemos falha tipada ANTES de
//     agendar um reinício inútil.
//   - UnavailablePrefix: antecede o motivo quando a capacidade falta.
//   - Immediate: ignora delaySeconds e age na hora (cancel/lock).
//   - CancelOnly: não executa energia — só cancela o que estiver pendente.
type powerVerbSpec struct {
	ProbeFirmwareUEFI bool
	UnavailablePrefix string
	Immediate         bool
	CancelOnly        bool
}

// resolvePowerVerbSpec devolve a spec do verbo e se ele é conhecido.
func resolvePowerVerbSpec(verb string) (powerVerbSpec, bool) {
	switch verb {
	case powerActionRestartFirmware:
		return powerVerbSpec{
			ProbeFirmwareUEFI: true,
			UnavailablePrefix: "reinicio no firmware (BIOS/UEFI) indisponivel: ",
		}, true
	case powerActionRestartAdvanced:
		return powerVerbSpec{}, true
	case powerActionCancel:
		return powerVerbSpec{Immediate: true, CancelOnly: true}, true
	case powerActionLock:
		return powerVerbSpec{Immediate: true}, true
	default:
		return powerVerbSpec{}, false
	}
}

// powerVerbLabel devolve o rótulo humano (pt-BR) do verbo para logs/mensagens.
func powerVerbLabel(verb string) string {
	switch verb {
	case powerActionRestartFirmware:
		return "reinicio no firmware (BIOS/UEFI)"
	case powerActionRestartAdvanced:
		return "reinicio nas Opcoes Avancadas"
	case powerActionCancel:
		return "cancelamento de energia pendente"
	case powerActionLock:
		return "bloqueio de sessao"
	default:
		return verb
	}
}

// sanitizePowerMessage achata espaços em branco (o output do shutdown.exe vem
// com quebras de linha) para a mensagem do contrato ficar em uma linha.
func sanitizePowerMessage(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// handleDirectPowerAction executa um verbo do comando genérico "poweraction".
//
// Fluxo próprio, mais simples que restart/shutdown (legados): o operador pediu
// explicitamente uma ação de boot/energia, então não há contador/adiamento do
// PSADT. O delay (quando informado e suportado pelo verbo) é contado pelo timer
// interno silencioso; verbos imediatos (cancel/lock) ignoram o delay.
//
// Capacidade: cada verbo pode exigir um recurso local (o /fw exige UEFI). A
// checagem acontece AQUI, antes de qualquer agendamento, e devolve falha TIPADA
// no contrato (status=failed + message clara) em vez de um reinício inútil.
func (a *App) handleDirectPowerAction(parent context.Context, verb string, pp powerCommandPayload) (bool, int, string, string) {
	if parent == nil {
		parent = context.Background()
	}

	spec, known := resolvePowerVerbSpec(verb)
	if !known {
		msg := fmt.Sprintf("acao de energia desconhecida: %q", verb)
		a.Logs.Append("[agent] poweraction [INVALID] " + msg)
		return true, 1, newPowerOutput("failed", "poweraction", msg).line(), msg
	}

	// Qualquer comando direto de energia substitui um agendamento anterior:
	// tanto o timer interno (silent) quanto um restart adiado pelo usuário.
	a.cancelDeferredRestart()
	a.cancelSilentPowerAction()

	if spec.ProbeFirmwareUEFI {
		if supported, reason := firmwareBootSupportFunc(); !supported {
			msg := spec.UnavailablePrefix + reason
			a.Logs.Append(fmt.Sprintf("[agent] %s [UNSUPPORTED] %s", verb, reason))
			return true, 1, newPowerOutput("failed", verb, msg).line(), msg
		}
	}

	a.warnInteractivePowerOutsideMaintenanceWindow(verb)

	// "cancel": não executa energia — só limpa o que estiver pendente. Os
	// timers do agent já foram cancelados acima; o shutdown /a limpa o que o
	// Windows tiver agendado.
	if spec.CancelOnly {
		exitCode, output, errText := a.handlePowerCancel(parent)
		return true, exitCode, output, errText
	}

	// "lock": bloqueio de estação é imediato e reversível (não perde trabalho).
	if verb == powerActionLock {
		exitCode, output, errText := a.handleSessionLock()
		return true, exitCode, output, errText
	}

	if !spec.Immediate && pp.DelaySeconds > 0 {
		a.scheduleSilentPowerAction(verb, pp)
		msg := fmt.Sprintf("%s agendado em %ds", powerVerbLabel(verb), pp.DelaySeconds)
		out := newPowerOutput("scheduled", verb, msg)
		out.DelaySeconds = pp.DelaySeconds
		return true, 0, out.line(), ""
	}

	exitCode, output, errText := a.executePowerWithContract(parent, verb, pp.Force, pp.Message)
	return true, exitCode, output, errText
}

// handlePowerCancel cancela um desligamento/reinício pendente do Windows
// (`shutdown /a`). "Nada pendente" NÃO é erro para o operador: os timers
// internos do agent já foram cancelados e a intenção (não reiniciar/desligar)
// foi cumprida.
func (a *App) handlePowerCancel(parent context.Context) (int, string, string) {
	cancel := a.cancelPendingSystemShutdown
	if powerCancelHook != nil {
		cancel = func(ctx context.Context) (string, string) { return powerCancelHook(a, ctx) }
	}
	output, errText := cancel(parent)
	if errText != "" {
		msg := sanitizePowerMessage(errText)
		a.Logs.Append(fmt.Sprintf("[agent] %s [ERRO] %s", powerActionCancel, msg))
		return 1, newPowerOutput("failed", powerActionCancel, msg).line(), msg
	}

	msg := "energia pendente cancelada (timers do agent e agendamento do Windows)"
	if out := sanitizePowerMessage(output); out != "" {
		msg = msg + ": " + out
	}
	a.Logs.Append(fmt.Sprintf("[agent] %s [OK] %s", powerActionCancel, msg))
	return 0, newPowerOutput("executed", powerActionCancel, msg).line(), ""
}

// handleSessionLock bloqueia a sessão interativa (LockWorkStation). O bloqueio
// é reversível e não perde trabalho — por isso não tem aviso nem delay.
func (a *App) handleSessionLock() (int, string, string) {
	if err := powerLockFunc(context.Background()); err != nil {
		msg := sanitizePowerMessage(err.Error())
		a.Logs.Append(fmt.Sprintf("[agent] %s [ERRO] %s", powerActionLock, msg))
		return 1, newPowerOutput("failed", powerActionLock, msg).line(), msg
	}
	a.Logs.Append(fmt.Sprintf("[agent] %s [OK] sessao bloqueada", powerActionLock))
	return 0, newPowerOutput("executed", powerActionLock, "sessao bloqueada").line(), ""
}
