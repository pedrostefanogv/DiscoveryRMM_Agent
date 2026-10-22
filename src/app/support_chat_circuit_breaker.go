package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ── Disjuntor da janela do chat (contenção 21/10) ───────────────────────────
//
// A criação da SEGUNDA janela WebView2 (a janela do chat) trava a main thread
// de forma determinística no processo real do agent (dump ui-zombie-23468:
// SendMessage cross-thread do host EmbeddedBrowserWebView em espera; reproduz
// sem antivírus, em UI manual, com browser fresco — a simulação isolada nunca
// trava). O main-watchdog encerra a UI aos 90 s, a UI volta no tray, a
// reconciliação acha a conversa ativa e tenta abrir a janela de novo — e
// congela de novo: LOOP que deixa a máquina inutilizável enquanto a sessão
// existir.
//
// O disjuntor não conserta o deadlock (isso exige o diagnóstico do dump
// simbolizado / mudança no Wails): ele IMPEDE O LOOP. Quando a criação trava,
// um marcador persistente desativa a criação da janela do chat por
// chatWindowBreakerTTL — o agent segue vivo (tray, IPC, acesso remoto,
// tudo) e as sessões novas não derrubam a máquina. O marcador expira sozinho.

const (
	chatWindowBreakerFile    = "chat-window-broken.json"
	chatWindowBreakerTTL     = 30 * time.Minute
	chatWindowBreakerAfterNs = 15 * time.Second // preso >15s = travamento real
)

type chatWindowBreakerState struct {
	Until     int64  `json:"until"` // unix nanos
	Failures  int    `json:"failures"`
	LastLabel string `json:"lastLabel,omitempty"`
}

var chatWindowBreakerMu sync.Mutex

func chatWindowBreakerPath() string {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		return ""
	}
	return filepath.Join(programData, "Discovery", chatWindowBreakerFile)
}

func chatWindowBreakerLoad() chatWindowBreakerState {
	var st chatWindowBreakerState
	raw, err := os.ReadFile(chatWindowBreakerPath())
	if err != nil {
		return st
	}
	_ = json.Unmarshal(raw, &st)
	return st
}

// chatWindowBreakerAtiva diz se a criação da janela do chat está desativada
// (marcador válido). Expirou = apaga o marcador e libera.
func chatWindowBreakerAtiva() bool {
	path := chatWindowBreakerPath()
	if path == "" {
		return false
	}
	st := chatWindowBreakerLoad()
	if st.Until == 0 || time.Now().UnixNano() < st.Until {
		return st.Until != 0
	}
	_ = os.Remove(path)
	return false
}

// chatWindowBreakerDispara grava o marcador (TTL) e devolve o estado gravado.
func chatWindowBreakerDispara(rotulo string) chatWindowBreakerState {
	chatWindowBreakerMu.Lock()
	defer chatWindowBreakerMu.Unlock()
	st := chatWindowBreakerLoad()
	if time.Now().UnixNano() < st.Until {
		return st // já disparado e válido
	}
	st = chatWindowBreakerState{
		Until:     time.Now().Add(chatWindowBreakerTTL).UnixNano(),
		Failures:  st.Failures + 1,
		LastLabel: rotulo,
	}
	if raw, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(chatWindowBreakerPath(), raw, 0o644)
	}
	return st
}

// chatWindowBreakerDisparaSeTravado é chamado pelo amostrador da main thread
// quando a criação da JANELA DO CHAT está presa acima do limiar.
func chatWindowBreakerDisparaSeTravado(rotulo string, preso time.Duration) {
	if preso < chatWindowBreakerAfterNs || chatWindowBreakerAtiva() {
		return
	}
	chatWindowBreakerDispara(rotulo)
}

// chatWindowBreakerRestante devolve quanto falta para liberar (para logs).
func chatWindowBreakerRestante() time.Duration {
	st := chatWindowBreakerLoad()
	if st.Until == 0 {
		return 0
	}
	d := time.Unix(0, st.Until).Sub(time.Now())
	if d < 0 {
		return 0
	}
	return d
}
