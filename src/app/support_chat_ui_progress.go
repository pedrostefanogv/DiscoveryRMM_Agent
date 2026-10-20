package app

import (
	"sync"
	"time"
)

// ── Progresso de subida da UI (tolerância do convite sem interface) ─────────
//
// RELATO ("mando abrir o chat e demora"): quando a UI não estava conectada, o
// convite aguardava supportChatInviteUIWait (25 s) e, no fim, era RECUSADO com
// "no_ui". No log desta máquina o nascimento da interface levou ~54 s (serviço,
// self-update, IPC e WebView2), então a recusa atingia convites que iam aparecer
// segundos depois — o atendente via "usuário recusado" e o usuário nunca via o
// pedido.
//
// Aqui o processo lembra quando houve PROGRESSO de UI (relançamento pedido e o
// processo chegou a existir). Com progresso visível o prazo é prorrogado uma
// vez; sem progresso, a recusa continua imediata (o suporte não pode esperar à
// toa).

// supportChatUIProgressTTL é por quanto tempo o progresso conta como atual.
const supportChatUIProgressTTL = 90 * time.Second

var supportChatUIProgress struct {
	mu      sync.Mutex
	lastAt  time.Time
	pidHint string
}

// markSupportChatUISpawn registra que a UI foi relançada/está subindo.
func (a *App) markSupportChatUISpawn(reason string) {
	supportChatUIProgress.mu.Lock()
	supportChatUIProgress.lastAt = time.Now()
	supportChatUIProgress.pidHint = reason
	supportChatUIProgress.mu.Unlock()
}

// supportChatUISpawnInProgress informa se há progresso RECENTE de UI: o
// relaçamento foi pedido agora há pouco (supportChatUIProgressTTL) — o
// processo pode já existir (ClientCount > 0) ou estar nascendo.
func (a *App) supportChatUISpawnInProgress() bool {
	if a == nil {
		return false
	}
	// Conexão já aberta (mesmo sem autenticar ainda): progresso evidente — a
	// UI existe e está negociando.
	if a.ipcServer != nil && a.ipcServer.ClientCount() > 0 {
		return true
	}

	supportChatUIProgress.mu.Lock()
	last := supportChatUIProgress.lastAt
	supportChatUIProgress.mu.Unlock()
	if last.IsZero() {
		return false
	}
	return time.Since(last) < supportChatUIProgressTTL
}
