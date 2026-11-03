package app

import (
	"context"
	"encoding/json"
	"fmt"
)

// ── Contrato de saída dos comandos de power (JSON de uma linha) ──
//
// O output do comando restart/reboot/shutdown é a fonte de verdade nas três
// camadas (agente → API → Site). Status possíveis: executed, scheduled,
// deferred_by_user, denied, failed (deferred_to_window é apenas legado — o
// comando INTERATIVO não é mais adiado pela janela de manutenção).
//
// Exit codes mantidos: executed/scheduled/deferred_by_user=0; denied=10;
// failed=1 (o exit code real da execução vira failed quando != 0).
//
// A mensagem humana (pt-BR) vai SEMPRE no campo "message": os textos legados
// ("adiado pelo usuário", "agendado silenciosamente") são preservados dentro
// dela para o fallback de classificação dos agentes antigos.

// powerDeferInfo descreve o adiamento pelo usuário (defer=current/max/min).
type powerDeferInfo struct {
	Current int `json:"current"`
	Max     int `json:"max"`
	Minutes int `json:"minutes"`
}

// powerCommandOutput é o envelope JSON do contrato. Campos opcionais são
// omitidos (omitempty) para manter uma linha limpa por status.
type powerCommandOutput struct {
	Status       string          `json:"status"`
	Action       string          `json:"action,omitempty"`
	DelaySeconds int             `json:"delaySeconds,omitempty"`
	Defer        *powerDeferInfo `json:"defer,omitempty"`
	NextRunAt    string          `json:"nextRunAt,omitempty"`
	Message      string          `json:"message"`
}

// newPowerOutput monta o envelope base do contrato para um status.
func newPowerOutput(status, action, message string) powerCommandOutput {
	return powerCommandOutput{Status: status, Action: action, Message: message}
}

// line serializa o contrato como JSON de UMA linha (sem quebras).
func (o powerCommandOutput) line() string {
	b, err := json.Marshal(o)
	if err != nil {
		// Marshal de struct com strings nunca deve falhar; cai para o texto
		// legado em vez de perder o resultado.
		return fmt.Sprintf("%s executado (erro ao serializar contrato: %v)", o.Action, err)
	}
	return string(b)
}

// ── Seams de teste para o handler de power ──
//
// O handler real toca o SO (PSADT/countdown e shutdown.exe): para testar o
// handler ponta a ponta sem executar nada, os hooks abaixo são usados quando
// não-nil. Em produção ficam nil (chamadas reais).
var (
	// powerCountdownHook substitui showNativePowerCountdown.
	powerCountdownHook func(a *App, action string, delaySeconds int, message string, force bool) string
	// powerExecHook substitui executeSystemPowerAction.
	powerExecHook func(a *App, parent context.Context, action string, delaySeconds int, force bool, message string) (int, string, string)
)

// firstNonEmptyPower devolve a primeira string não vazia. (Nome distinto de
// firstNonEmpty de automation_p2p.go para evitar colisão no pacote.)
func firstNonEmptyPower(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// executePowerWithContract executa a ação de power (hook de teste quando
// definido, executeSystemPowerAction em produção) e devolve o output no
// contrato JSON: status=executed quando exit==0, failed caso contrário (com a
// mensagem de erro real). O exit code e o errText reais seguem intactos para
// o canal de comandos.
func (a *App) executePowerWithContract(parent context.Context, action string, force bool, message string) (int, string, string) {
	var exitCode int
	var raw, errText string
	if powerExecHook != nil {
		exitCode, raw, errText = powerExecHook(a, parent, action, 0, force, message)
	} else {
		exitCode, raw, errText = a.executeSystemPowerAction(parent, action, 0, force, message)
	}
	if exitCode != 0 {
		msg := firstNonEmptyPower(errText, raw, fmt.Sprintf("%s falhou (exit %d)", action, exitCode))
		return exitCode, newPowerOutput("failed", action, msg).line(), errText
	}
	return exitCode, newPowerOutput("executed", action, firstNonEmptyPower(raw, fmt.Sprintf("%s executado", action))).line(), errText
}

// deferredByUserOutput monta o JSON deferred_by_user com o bloco defer.
func deferredByUserOutput(action string, current, max, minutes int) string {
	msg := fmt.Sprintf("%s adiado pelo usuário (defer=%d/%d, %dmin)", action, current, max, minutes)
	out := newPowerOutput("deferred_by_user", action, msg)
	out.Defer = &powerDeferInfo{Current: current, Max: max, Minutes: minutes}
	return out.line()
}
