package app

import (
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ── Trazer uma janela do agent para o PRIMEIRO PLANO ─────────────────────────
//
// BUG RELATADO (produção): o convite de chat de suporte "às vezes não abre por
// cima dos outros aplicativos — só fica o ícone na barra de tarefas". O usuário
// não via o convite e não tinha como dar o aceite.
//
// Causa: a janela do chat era exibida com Show() + Focus() e nada mais.
//
//   - Show() no Windows é SW_SHOW: mostra a janela, mas NÃO a ativa;
//   - Focus() chama SetForegroundWindow, que o Windows BLOQUEIA quando o
//     processo chamador não é o dono do primeiro plano (foreground lock). O
//     agent vive no tray, quase nunca tem uma janela em primeiro plano, e o
//     convite chega por comando do servidor (NATS) — o cenário exato em que o
//     SetForegroundWindow é recusado: a janela nasce atrás do app que o usuário
//     está usando (ou minimizada, só com o botão na barra de tarefas).
//
// Aqui a exibição é reforçada em três frentes, na ordem em que cada uma cobre a
// falha da anterior:
//
//  1. "Sempre no topo" enquanto a janela precisa ser vista (convite pendente):
//     mesmo sem o primeiro plano, ela fica acima das demais;
//  2. restauração (se minimizada), Show e Focus pelo Wails;
//  3. força bruta no Win32 (ver forceWindowToForeground): AttachThreadInput +
//     SetForegroundWindow, com o truque topmost/não-topmost como fallback;
//  4. se ainda assim não houver primeiro plano, piscar a barra de tarefas
//     (FlashWindowEx com FLASHW_TIMERNOFG, que para sozinho quando o usuário
//     olha a janela).

// windowRaiseRetryDelays são as re-tentativas de trazer a janela para a frente.
//
// A primeira tentativa acontece na hora; estas são as seguintes. Motivo de
// existir: a janela do chat nasce junto com o convite e o WebView2 ainda está
// sendo criado — nas primeiras centenas de milissegundos o Windows costuma
// IGNORAR SetForegroundWindow para uma janela que ainda não pintou.
var windowRaiseRetryDelays = []time.Duration{
	350 * time.Millisecond,
	900 * time.Millisecond,
	1800 * time.Millisecond,
}

// raiseWindowToFront exibe a janela, restaura se estiver minimizada e tenta
// levá-la ao primeiro plano. Devolve true quando a janela ficou em primeiro
// plano.
//
// keepTopmost mantém a janela "sempre no topo" durante a exibição. É usado no
// convite do chat de suporte; depois da resposta do usuário volta a false (uma
// janela sempre no topo durante a conversa inteira atrapalharia o trabalho).
func raiseWindowToFront(win application.Window, keepTopmost bool) bool {
	if win == nil {
		return false
	}

	// Antes de mostrar: se a janela já está visível e atrás das outras, o
	// "sempre no topo" a traz para a frente sem depender de primeiro plano.
	win.SetAlwaysOnTop(keepTopmost)

	if win.IsMinimised() {
		win.UnMinimise()
	}
	win.Show()
	win.Focus()

	focused := forceWindowToForeground(win, keepTopmost)
	if !focused {
		// Sem o primeiro plano, resta garantir que o usuário PERCEBA a janela:
		// ela já está acima das outras (topmost) e a barra de tarefas pisca.
		flashWindowUntilFocused(win)
	}
	return focused
}

// scheduleWindowRaise re-tenta trazer a janela para a frente depois da primeira
// tentativa (ver windowRaiseRetryDelays).
//
// alive devolve false quando a janela não é mais a janela corrente (foi fechada
// ou substituída) e encerra as tentativas. stop encerra as tentativas quando o
// contexto da aplicação acaba; pode ser nil.
func scheduleWindowRaise(win application.Window, keepTopmost bool, alive func() bool, stop <-chan struct{}) {
	if win == nil {
		return
	}

	go func() {
		for _, delay := range windowRaiseRetryDelays {
			timer := time.NewTimer(delay)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}

			if alive != nil && !alive() {
				return
			}
			// Já está na frente (o usuário clicou nela, por exemplo): nada a fazer.
			if win.IsFocused() {
				stopWindowFlash(win)
				return
			}
			if raiseWindowToFront(win, keepTopmost) {
				stopWindowFlash(win)
				return
			}
		}
	}()
}
