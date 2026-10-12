package app

import (
	"sync/atomic"
	"time"
)

// systemShutdownFlag marca que o SCM notificou encerramento/reinício do SO
// (PreShutdown/Shutdown). Setado pelo handler de serviço e consultado pelo
// caminho de power action.
//
// Motivo: o RestartPrompt do PSADT dispara o reboot e o processo cliente é
// morto antes de imprimir o marcador "RestartPrompt encerrado" — o agente via
// exit=1 e logava FALSO NEGATIVO ("PSADT indisponível"), chegando a exibir uma
// segunda notificação (fallback) com a máquina já reiniciando. Com este sinal,
// o encerramento em curso conta como sucesso.
var systemShutdownFlag atomic.Bool

// powerRebootDetectTimeout é quanto esperamos pelo sinal de encerramento do SO
// depois que o prompt retornou sem marcador. O Windows inicia o shutdown ~2-3s
// depois do RestartPrompt, então o timeout precisa cobrir essa janela.
const powerRebootDetectTimeout = 10 * time.Second

// noteSystemShutdown registra que o SO entrou em encerramento/reinício.
func noteSystemShutdown() {
	systemShutdownFlag.Store(true)
}

// SystemShutdownInProgress reporta se o SO já iniciou encerramento/reinício.
func SystemShutdownInProgress() bool {
	return systemShutdownFlag.Load()
}

// waitForSystemShutdown aguarda (poll curto) o sinal de encerramento do SO
// aparecer, até o timeout. Retorna o estado final.
func waitForSystemShutdown(timeout time.Duration) bool {
	if SystemShutdownInProgress() {
		return true
	}
	if timeout <= 0 {
		return false
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if SystemShutdownInProgress() {
			return true
		}
	}
	return SystemShutdownInProgress()
}

// resetSystemShutdownForTest limpa o flag (apenas para testes).
func resetSystemShutdownForTest() {
	systemShutdownFlag.Store(false)
}
