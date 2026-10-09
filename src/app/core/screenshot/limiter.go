package screenshot

import (
	"sync"
	"time"
)

// CaptureLimiter limita quantas capturas a IA pode fazer em uma janela de
// tempo. Protege contra loop de recaptura (a IA pedindo print sem parar) e
// contra custo de tokens/visão. O limite também é aplicado às capturas
// interativas — a seleção do usuário continua sendo o consentimento; o teto é
// anti-abuso.
type CaptureLimiter struct {
	mu     sync.Mutex
	events []time.Time
	max    int
	window time.Duration
}

const (
	// DefaultMaxCapturesPerWindow é o teto padrão de capturas por janela.
	DefaultMaxCapturesPerWindow = 10
	// DefaultCaptureWindowMinutes é o tamanho padrão da janela de tempo.
	// Ajuste de produto: a cota local padrão da IA é 10 capturas a cada 1 minuto.
	DefaultCaptureWindowMinutes = 1
)

// NewCaptureLimiter cria o limitador. max/window <= 0 usam os padrões.
func NewCaptureLimiter(max int, window time.Duration) *CaptureLimiter {
	if max <= 0 {
		max = DefaultMaxCapturesPerWindow
	}
	if window <= 0 {
		window = DefaultCaptureWindowMinutes * time.Minute
	}
	return &CaptureLimiter{max: max, window: window}
}

// Check informa se uma nova captura é permitida; quando não, retorna o tempo
// restante até o orçamento liberar (mínimo de 1s, para a mensagem ao LLM).
func (l *CaptureLimiter) Check(now time.Time) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	if len(l.events) < l.max {
		return true, 0
	}
	retry := l.window - now.Sub(l.events[0])
	if retry < time.Second {
		retry = time.Second
	}
	return false, retry
}

// Record registra uma captura concluída.
func (l *CaptureLimiter) Record(now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	l.events = append(l.events, now)
}

// Count retorna quantas capturas estão dentro da janela atual.
func (l *CaptureLimiter) Count(now time.Time) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	return len(l.events)
}

// Max retorna o teto configurado.
func (l *CaptureLimiter) Max() int {
	if l == nil {
		return 0
	}
	return l.max
}

// Window retorna a janela de tempo configurada.
func (l *CaptureLimiter) Window() time.Duration {
	if l == nil {
		return 0
	}
	return l.window
}

// Reconfigure troca teto/janela (política editada na UI) e descarta o histórico.
func (l *CaptureLimiter) Reconfigure(max int, window time.Duration) {
	if l == nil {
		return
	}
	if max <= 0 {
		max = DefaultMaxCapturesPerWindow
	}
	if window <= 0 {
		window = DefaultCaptureWindowMinutes * time.Minute
	}
	l.mu.Lock()
	l.max = max
	l.window = window
	l.events = nil
	l.mu.Unlock()
}

func (l *CaptureLimiter) pruneLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	kept := l.events[:0]
	for _, event := range l.events {
		if event.After(cutoff) {
			kept = append(kept, event)
		}
	}
	l.events = kept
}
