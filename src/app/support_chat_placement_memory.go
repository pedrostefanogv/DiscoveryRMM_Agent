package app

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// ── Memória de posição da janela do chat ────────────────────────────────────
//
// BUG DE USABILIDADE ("parece que o chat não abriu"): a janela sempre nascia
// centralizada no monitor do cursor. Quando o usuário já tinha arrastado (ou
// apenas visto) o painel em outro ponto — ou reabria a conversa com a janela
// anterior na tela — a nova aparecia em outro lugar, o que dá a sensação de
// que o pedido de abertura falhou.
//
// Aqui a posição da última janela do chat é lembrada e a próxima nasce nela
// (desde que a tela continue conectada e o ponto continue na área de trabalho).
// É memória de processo: não persiste entre reinícios do agent, de propósito —
// num monitor desligado a posição antiga não faz sentido.

// supportChatPlacementRecord é a posição lembrada da última janela do chat.
type supportChatPlacementRecord struct {
	screen *application.Screen
	x, y   int
}

// rememberSupportChatWindowPlacement guarda a posição da janela que está sendo
// fechada.
func (a *App) rememberSupportChatWindowPlacement(win application.Window) {
	if a == nil || win == nil {
		return
	}
	screen, err := win.GetScreen()
	if err != nil || screen == nil {
		return
	}
	x, y := win.Position()
	if x < 0 || y < 0 {
		// Fora da área de trabalho (janela minimizada/escondida): não aprende.
		return
	}
	record := &supportChatPlacementRecord{screen: screen, x: x, y: y}
	a.supportChatMu.Lock()
	a.supportChatLastPlacement = record
	a.supportChatMu.Unlock()
}
