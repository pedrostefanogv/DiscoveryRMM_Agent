package agentconn

import (
	"testing"
	"time"
)

// Item 1/2: a carência só marca o transporte como indisponível depois de
// transportDownGrace; 0 significa conectado.
func TestTransportDownBeyondGrace(t *testing.T) {
	now := time.Now()
	if transportDownBeyondGrace(now, 0, 15*time.Second) {
		t.Fatal("0 (conectado) não pode exceder a carência")
	}
	if transportDownBeyondGrace(now, now.Add(-10*time.Second).UnixNano(), 15*time.Second) {
		t.Fatal("10s não deve exceder a carência de 15s")
	}
	if !transportDownBeyondGrace(now, now.Add(-20*time.Second).UnixNano(), 15*time.Second) {
		t.Fatal("20s deveria exceder a carência de 15s")
	}
}

// markTransportDown preserva o PRIMEIRO horário da queda e clear zera.
func TestTransportDownClock(t *testing.T) {
	r := &Runtime{}
	r.markTransportDown()
	first := r.transportDownSince.Load()
	if first == 0 {
		t.Fatal("markTransportDown deveria gravar o horário")
	}
	time.Sleep(2 * time.Millisecond)
	r.markTransportDown()
	if r.transportDownSince.Load() != first {
		t.Fatal("markTransportDown não pode sobrescrever o primeiro horário")
	}
	r.clearTransportDown()
	if r.transportDownSince.Load() != 0 {
		t.Fatal("clearTransportDown deveria zerar")
	}
}
