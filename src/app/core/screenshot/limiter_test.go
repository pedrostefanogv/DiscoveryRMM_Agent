package screenshot

import (
	"testing"
	"time"
)

func TestCaptureLimiterBlocksAfterMax(t *testing.T) {
	l := NewCaptureLimiter(2, time.Minute)
	now := time.Now()
	if ok, _ := l.Check(now); !ok {
		t.Fatal("primeira captura deveria ser permitida")
	}
	l.Record(now)
	if ok, _ := l.Check(now.Add(time.Second)); !ok {
		t.Fatal("segunda captura deveria ser permitida")
	}
	l.Record(now.Add(time.Second))
	if ok, retry := l.Check(now.Add(2 * time.Second)); ok {
		t.Fatal("terceira captura deveria ser bloqueada")
	} else if retry <= 0 {
		t.Fatalf("retry deve ser positivo, got %s", retry)
	}
}

func TestCaptureLimiterReleasesAfterWindow(t *testing.T) {
	l := NewCaptureLimiter(1, time.Minute)
	start := time.Now()
	l.Record(start)
	if ok, _ := l.Check(start.Add(30 * time.Second)); ok {
		t.Fatal("deveria bloquear dentro da janela")
	}
	if ok, _ := l.Check(start.Add(61 * time.Second)); !ok {
		t.Fatal("deveria liberar depois da janela")
	}
	if got := l.Count(start.Add(2 * time.Minute)); got != 0 {
		t.Fatalf("contagem apos janela = %d, want 0", got)
	}
}

func TestCaptureLimiterReconfigureResets(t *testing.T) {
	l := NewCaptureLimiter(1, time.Minute)
	now := time.Now()
	l.Record(now)
	if ok, _ := l.Check(now); ok {
		t.Fatal("deveria estar bloqueado antes do reconfigure")
	}
	l.Reconfigure(5, 2*time.Minute)
	if l.Max() != 5 || l.Window() != 2*time.Minute {
		t.Fatalf("reconfigure nao aplicou: max=%d window=%s", l.Max(), l.Window())
	}
	if ok, _ := l.Check(now); !ok {
		t.Fatal("reconfigure deveria limpar o historico")
	}
}

func TestCaptureLimiterNilSafe(t *testing.T) {
	var l *CaptureLimiter
	if ok, _ := l.Check(time.Now()); !ok {
		t.Fatal("nil limiter deveria permitir")
	}
	l.Record(time.Now())
	if l.Count(time.Now()) != 0 || l.Max() != 0 {
		t.Fatal("nil limiter deveria ser inerte")
	}
}
