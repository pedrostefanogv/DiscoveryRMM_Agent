package screen

import (
	"fmt"
	"sync"
)

// Logger de diagnóstico do pacote screen (ordem de canais, calibração de cor,
// fallback de capturer). Injetado pelo chamador (remotesession/screenshot) para
// as mensagens caírem no mesmo log do serviço/UI. nil = silencioso.
//
// Motivo: o pacote screen não importa "log" de propósito (é usado também por
// código de UI e por testes), então a decisão de formato fica registrada no
// log unificado sem acoplar o pacote ao logger global.
var (
	screenDebugMu  sync.RWMutex
	screenDebugLog func(string)
)

// SetDebugLogger registra o logger de diagnóstico do pacote screen.
// Passe nil para desabilitar.
func SetDebugLogger(f func(string)) {
	screenDebugMu.Lock()
	screenDebugLog = f
	screenDebugMu.Unlock()
}

func debugf(format string, args ...any) {
	screenDebugMu.RLock()
	f := screenDebugLog
	screenDebugMu.RUnlock()
	if f == nil {
		return
	}
	f(fmt.Sprintf(format, args...))
}
