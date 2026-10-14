package logstore

import (
	"path/filepath"
	"strings"
	"testing"
)

// O teto por BYTES da fila protege o processo de uma rajada de linhas grandes
// sem descartar o caso normal (muitas linhas pequenas).
func TestQueueByteBudgetDropsOnlyOversizedBurst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	// Fila mínima + teto de 64 KB: cada linha de 16 KB cabe, mas só algumas.
	s := openTest(t, Options{
		Path:           path,
		Kind:           KindLogs,
		QueueSize:      64,
		MaxQueuedBytes: 64 << 10,
	})

	// Sem deixar o writer drenar rápido demais: escreve em rajada.
	msg := strings.Repeat("x", 16<<10)
	for i := 0; i < 200; i++ {
		s.AppendLog("INFO", "rev", msg)
	}

	if s.Dropped() == 0 {
		t.Fatal("esperava descarte com rajada acima do teto de bytes da fila")
	}

	s.Flush()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// O que foi gravado precisa estar íntegro (mensagem clampada no teto).
	if n := queryInt(t, path, "SELECT COUNT(*) FROM log_entries"); n == 0 {
		t.Fatal("nenhuma linha gravada")
	}
}

// Linhas pequenas (caso real) não são afetadas pelo teto por bytes.
func TestQueueByteBudgetKeepsNormalBurst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	s := openTest(t, Options{Path: path, Kind: KindLogs, QueueSize: 4096, MaxQueuedBytes: DefaultMaxQueuedBytes})

	msg := strings.Repeat("y", 512)
	for i := 0; i < 2000; i++ {
		s.AppendLog("INFO", "rev", msg)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s.Dropped() != 0 {
		t.Fatalf("liner/queued bytes derrubaram linhas pequenas: dropped=%d", s.Dropped())
	}
}
