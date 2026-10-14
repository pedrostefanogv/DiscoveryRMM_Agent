package logs

import (
	"strings"
	"testing"
)

func TestBufferRespectsByteBudget(t *testing.T) {
	b := New()
	// Linhas de 8 KB: 4 MB / 8 KB = ~512 linhas antes do corte por bytes.
	line := strings.Repeat("x", 8192)
	for i := 0; i < 2000; i++ {
		b.Append(line)
	}

	got := b.GetAll()
	if len(got) >= 2000 {
		t.Fatalf("buffer não aplicou o teto de bytes: %d linhas", len(got))
	}
	total := 0
	for _, l := range got {
		total += len(l)
	}
	// O limite é por bytes das linhas; uma linha pode ultrapassar sozinha.
	if total > maxBufferBytes+len(line) {
		t.Fatalf("bytes retidos = %d, acima do teto %d", total, maxBufferBytes)
	}
	if b.Count() != len(got) {
		t.Fatalf("Count() = %d, GetAll() = %d", b.Count(), len(got))
	}
}

func TestBufferKeepsMostRecentLinesAfterByteTrim(t *testing.T) {
	b := New()
	for i := 0; i < 2000; i++ {
		b.Append(strings.Repeat("y", 4096) + "-ultima-" + strings.Repeat("0", 4) + string(rune('a'+i%26)))
	}
	got := b.GetAll()
	if len(got) == 0 {
		t.Fatal("buffer vazio")
	}
	last := got[len(got)-1]
	if !strings.Contains(last, "ultima") {
		t.Fatalf("última linha = %q", last)
	}
}

func TestBufferClearResetsBytes(t *testing.T) {
	b := New()
	b.Append(strings.Repeat("z", 1000))
	b.Clear()
	if b.Count() != 0 {
		t.Fatalf("Count após Clear = %d", b.Count())
	}
	// Após limpar, o orçamento de bytes deve voltar a estar disponível.
	for i := 0; i < 100; i++ {
		b.Append(strings.Repeat("w", 8192))
	}
	if b.Count() != 100 {
		t.Fatalf("Count = %d, want 100 (orçamento não foi zerado no Clear)", b.Count())
	}
}
