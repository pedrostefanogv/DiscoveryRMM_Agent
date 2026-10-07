//go:build windows

package remotesession

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// ── R2: janela de rate limit ──

func TestRateWindow_AllowsUpToMaxThenBlocks(t *testing.T) {
	w := &rateWindow{}
	for i := 0; i < 3; i++ {
		if !w.allow(3) {
			t.Fatalf("chamada %d deveria ser permitida", i+1)
		}
	}
	if w.allow(3) {
		t.Fatal("quarta chamada deveria ser bloqueada")
	}
}

// ── R9: comparação de contadores ignora tempo ──

func TestTermStatsPayload_CountersEqualIgnoresTime(t *testing.T) {
	a := termStatsPayload{TimestampMs: 1, UptimeMs: 10, FramesOut: 5, InputFrames: 2}
	b := termStatsPayload{TimestampMs: 999, UptimeMs: 99999, FramesOut: 5, InputFrames: 2}
	if !a.countersEqual(b) {
		t.Fatal("timestamp/uptime não deveriam contar como mudança")
	}
	b.FramesOut = 6
	if a.countersEqual(b) {
		t.Fatal("mudança em framesOut deveria ser detectada")
	}
}

// ── R4: frames de gravação recebem seq da sequência do terminal ──

func TestRecordFrame_AllocatesSeqFromSeqAlloc(t *testing.T) {
	server := startLivenessNATS(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("conectar NATS: %v", err)
	}
	t.Cleanup(nc.Close)

	h := NewNatsStreamHandler(nc, "client-1", "site-1", "agent-1")
	tap := &RecordingTap{sessionID: "sess-seq", natsStream: h, enabled: true}
	st := &SessionTerminal{sessionID: "sess-seq", natsStream: h, recordingTap: tap}

	var n int64
	st.seqAlloc = func() int64 { n++; return n }

	subject := h.publishSubject("sess-seq", "recording.term")
	msgs := make(chan TermRecordingFrame, 4)
	sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		var f TermRecordingFrame
		if json.Unmarshal(msg.Data, &f) == nil {
			msgs <- f
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	_ = nc.Flush()

	// seq=0 => aloca do seqAlloc (não repete 0 entre frames de ciclo de vida).
	st.recordFrame("ready", "", nil, nil, nil)
	select {
	case f := <-msgs:
		if f.Seq != 1 {
			t.Fatalf("seq alocado = %d, want 1", f.Seq)
		}
		if f.Kind != "ready" {
			t.Fatalf("kind = %q, want ready", f.Kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("frame ready não chegou")
	}

	// seq explícito é preservado.
	st.recordFrameSeq("error", "boom", 42, nil, nil, nil)
	select {
	case f := <-msgs:
		if f.Seq != 42 {
			t.Fatalf("seq explícito = %d, want 42", f.Seq)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("frame error não chegou")
	}
}
