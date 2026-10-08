package app

import (
	"strings"
	"sync"
	"time"
)

// ─── Ponte de log do frontend (WebView) → logs.db ───
//
// Por que existe: os avisos do frontend ([a2ui] falha ao injetar tema, userAction
// handler error, surface sem componentes, etc.) só existiam no console do
// WebView — invisíveis para diagnóstico. O logs.db tem apenas as fontes "agent" e
// "service", e foi por isso que o bug de CSS do A2UI (2026-10-08) era silencioso
// nos logs: os console.warn("[a2ui] ...") nunca chegavam ao disco.
//
// O frontend chama de forma defensiva (typeof api.LogFrontend === "function"),
// então um exe antigo simplesmente não envia nada.
const (
	// Janela deslizante (10 min): o teto é POR JANELA, não por processo. Com um
	// teto vitalício, um app aberto por dias simplesmente pararia de registrar
	// avisos depois de 200 mensagens — justo quando o diagnóstico é necessário.
	frontendLogMaxPerWindow   = 200
	frontendLogWindowMs       = 10 * 60 * 1000
	frontendLogDedupeWindowMs = 10000
	frontendLogMaxLen         = 500
	// Limite do mapa de dedupe (poda entradas velhas).
	frontendLogSeenMaxSize = 512
)

var (
	frontendLogMu       sync.Mutex
	frontendLogSeen     = map[string]int64{}
	frontendLogCount    int
	frontendLogWindowAt int64
)

// LogFrontend registra uma mensagem de console do frontend no log do agent.
//
// Throttle: no máximo 1 registro por mensagem idêntica a cada 10s e um teto por
// execução — sem isso um erro em loop (ex.: tema falhando a cada mutação)
// inundaria o logs.db.
func (a *App) LogFrontend(level, message string) {
	if a == nil {
		return
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		return
	}
	if len(msg) > frontendLogMaxLen {
		msg = msg[:frontendLogMaxLen] + "…"
	}
	lvl := strings.ToLower(strings.TrimSpace(level))
	if lvl == "" {
		lvl = "warn"
	}

	now := time.Now().UnixMilli()
	frontendLogMu.Lock()
	if now-frontendLogWindowAt > frontendLogWindowMs {
		frontendLogWindowAt = now
		frontendLogCount = 0
	}
	if frontendLogCount >= frontendLogMaxPerWindow {
		frontendLogMu.Unlock()
		return
	}
	if last, ok := frontendLogSeen[msg]; ok && now-last < frontendLogDedupeWindowMs {
		frontendLogMu.Unlock()
		return
	}
	if len(frontendLogSeen) >= frontendLogSeenMaxSize {
		for k, ts := range frontendLogSeen {
			if now-ts >= frontendLogDedupeWindowMs {
				delete(frontendLogSeen, k)
			}
		}
	}
	frontendLogSeen[msg] = now
	frontendLogCount++
	frontendLogMu.Unlock()

	a.Logs.Append("[chat] [frontend:" + lvl + "] " + msg)
}
