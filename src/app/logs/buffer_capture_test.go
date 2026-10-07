package logs

import (
	"path/filepath"
	"testing"

	"discovery/app/core/logstore"
)

// Regressão: cada linha aparecia DUAS vezes em logs.db (ids consecutivos, mesmo
// ts_ms) porque o logger gravava no banco por dois caminhos:
//  1. storeSink do logger (logger.SetStoreSink, app/logstore.go);
//  2. buffer (EnableStore) — para onde o MESMO logger também entrega a linha.
//
// O contrato agora é explícito: linhas capturadas do logger são gravadas pelo
// storeSink (o buffer só as guarda em memória/arquivo); linhas de
// a.Logs.Append(...) — que não passam pelo logger — são gravadas pelo buffer.
func TestBufferCapturedLinesDoNotDoubleWrite(t *testing.T) {
	dir := t.TempDir()
	st, err := logstore.Open(logstore.Options{
		Path:    filepath.Join(dir, "logs.db"),
		Kind:    logstore.KindLogs,
		Source:  "test",
		Process: "test",
	})
	if err != nil {
		t.Fatalf("logstore.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	b := New()
	b.EnableStore(st)

	// Linha "de aplicação" (Append explícito): grava no banco.
	b.Append("linha explicita")
	// Linha capturada do logger: NÃO grava no banco por este caminho.
	b.appendCaptured("[INFO] linha capturada do logger")

	st.Flush()
	n, err := st.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Fatalf("esperava exatamente 1 linha no banco (sem duplicacao), veio %d", n)
	}

	// Nada pode sumir da visão da UI/terminal: as duas linhas seguem no buffer.
	lines := b.GetAll()
	if len(lines) != 2 {
		t.Fatalf("buffer deveria manter as 2 linhas, veio %d (%#v)", len(lines), lines)
	}
	if lines[0] != "linha explicita" || lines[1] != "[INFO] linha capturada do logger" {
		t.Fatalf("conteúdo do buffer inesperado: %#v", lines)
	}
}
