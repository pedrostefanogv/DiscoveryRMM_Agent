//go:build windows

package remotesession

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

func TestClampTermDims(t *testing.T) {
	cases := []struct {
		cols, rows         int
		wantCols, wantRows int
	}{
		{120, 40, 120, 40},
		{0, 0, termMinCols, termMinRows},
		{1, 1, termMinCols, termMinRows},
		{termMaxCols + 1, termMaxRows + 1, termMaxCols, termMaxRows},
		{2000, 1000, termMaxCols, termMaxRows},
		{termMinCols, termMinRows, termMinCols, termMinRows},
	}
	for _, c := range cases {
		gotCols, gotRows := clampTermDims(c.cols, c.rows)
		if gotCols != c.wantCols || gotRows != c.wantRows {
			t.Fatalf("clampTermDims(%d,%d) = (%d,%d), want (%d,%d)",
				c.cols, c.rows, gotCols, gotRows, c.wantCols, c.wantRows)
		}
	}
}

// makeFrame gera um payload term.out realista (base64 + seq) do tamanho pedido.
func makeFrame(seq int64, rawBytes int) string {
	raw := make([]byte, rawBytes)
	for i := range raw {
		raw[i] = byte('a' + (i % 26))
	}
	payload, _ := json.Marshal(map[string]any{
		"data": base64.StdEncoding.EncodeToString(raw),
		"seq":  seq,
	})
	return string(payload)
}

func TestTermReplayRing_ReplaysOnlyMissingFrames(t *testing.T) {
	ring := newTermReplayRing(1 << 20)
	for i := int64(0); i < 5; i++ {
		ring.add(i, makeFrame(i, 8))
	}

	frames, ok := ring.since(2)
	if !ok {
		t.Fatal("since(2) com anel completo deve ser continuável")
	}
	if len(frames) != 2 {
		t.Fatalf("since(2) devolveu %d frames, want 2", len(frames))
	}
	if frames[0].seq != 3 || frames[1].seq != 4 {
		t.Fatalf("since(2) fora de ordem: %d,%d", frames[0].seq, frames[1].seq)
	}
}

func TestTermReplayRing_ViewerEmDiaNaoRecebeNada(t *testing.T) {
	ring := newTermReplayRing(1 << 20)
	for i := int64(0); i < 3; i++ {
		ring.add(i, makeFrame(i, 8))
	}
	frames, ok := ring.since(2) // já viu o último (2)
	if !ok || len(frames) != 0 {
		t.Fatalf("since(2) = %d frames, ok=%v; want 0, true", len(frames), ok)
	}
	// lastSeq = -1 (viewer novo) recebe TUDO o que está retido.
	frames, ok = ring.since(-1)
	if !ok || len(frames) != 3 {
		t.Fatalf("since(-1) = %d frames, ok=%v; want 3, true", len(frames), ok)
	}
}

func TestTermReplayRing_AnelVazio(t *testing.T) {
	ring := newTermReplayRing(1024)
	if frames, ok := ring.since(0); !ok || len(frames) != 0 {
		t.Fatalf("anel vazio: %d frames, ok=%v; want 0, true", len(frames), ok)
	}
}

func TestTermReplayRing_DetectaLacuna(t *testing.T) {
	ring := newTermReplayRing(1 << 20)
	ring.add(10, makeFrame(10, 16))
	ring.add(11, makeFrame(11, 16))

	// O viewer parou no seq 5: os frames 6..9 já saíram do anel → não dá para
	// continuar sem emendar saída nova em saída velha (o agent manda "reset").
	if _, ok := ring.since(5); ok {
		t.Fatal("since(5) com anel começando em 10 deveria detectar lacuna")
	}
	// Fronteira contínua: 9 é exatamente o anterior ao primeiro retido (10).
	if _, ok := ring.since(9); !ok {
		t.Fatal("since(9) com anel começando em 10 é contínuo")
	}
}

func TestTermReplayRing_LimitaMemoria(t *testing.T) {
	const maxBytes = 4096
	ring := newTermReplayRing(maxBytes)
	frameSize := 512
	var seq int64
	for i := 0; i < 100; i++ {
		ring.add(seq, makeFrame(seq, frameSize/2))
		seq++
	}
	ring.mu.Lock()
	bytes, n := ring.bytes, len(ring.frames)
	first := ring.frames[0].seq
	ring.mu.Unlock()

	if bytes > maxBytes {
		t.Fatalf("anel excedeu o teto: %d > %d", bytes, maxBytes)
	}
	if n == 0 {
		t.Fatal("anel ficou vazio (deveria reter os frames mais recentes)")
	}
	// O primeiro retido deve ser recente: o seq 0 certamente foi descartado.
	if first == 0 {
		t.Fatal("frames antigos não foram descartados")
	}
	// E o mais recente continua presente.
	if _, ok := ring.since(seq - 2); !ok {
		t.Fatal("os frames mais recentes deveriam estar retidos")
	}
}

func TestTermReplayRing_Clear(t *testing.T) {
	ring := newTermReplayRing(1 << 20)
	ring.add(0, makeFrame(0, 16))
	ring.clear()
	ring.mu.Lock()
	bytes, n := ring.bytes, len(ring.frames)
	ring.mu.Unlock()
	if bytes != 0 || n != 0 {
		t.Fatalf("clear deixou bytes=%d frames=%d", bytes, n)
	}
}

func TestTermReplayRing_UmFrameMaiorQueOTetoEhMantido(t *testing.T) {
	ring := newTermReplayRing(8)
	big := makeFrame(0, 4096)
	ring.add(0, big)
	if frames, ok := ring.since(-1); !ok || len(frames) != 1 {
		t.Fatalf("um único frame acima do teto deve ser retido: %d frames, ok=%v", len(frames), ok)
	}
}

func TestTermReplayRing_Concorrencia(t *testing.T) {
	ring := newTermReplayRing(1 << 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := int64(0); i < 200; i++ {
			ring.add(i, makeFrame(i, 32))
		}
	}()
	for i := 0; i < 200; i++ {
		_, _ = ring.since(int64(i))
	}
	<-done
	// Sem race detector o valor não importa; o teste garante que add/since
	// concorrentes não entram em pânico por corrupção de slice.
	if frames, ok := ring.since(-1); !ok || len(frames) == 0 {
		t.Fatalf("estado final inesperado: %d frames, ok=%v", len(frames), ok)
	}
	_ = fmt.Sprintf("%d", len(ring.frames))
}
