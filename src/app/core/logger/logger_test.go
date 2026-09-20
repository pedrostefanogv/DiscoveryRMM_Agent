package logger

import (
	"strings"
	"testing"

	"discovery/app/core/buildinfo"
)

// O writer do agent-service.log deve prefixar cada linha com a revisão de
// código do binário e repassar write vazio sem prefixo.
func TestRevPrefixWriter(t *testing.T) {
	var sb strings.Builder
	w := &revPrefixWriter{w: &sb}
	if _, err := w.Write([]byte("time=2026-09-20T12:00:00Z level=INFO msg=oi\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := sb.String()
	if !strings.HasPrefix(out, "["+buildinfo.Revision()+"] ") {
		t.Fatalf("prefixo de revisão ausente: %q", out)
	}
	if !strings.Contains(out, "msg=oi") {
		t.Fatalf("mensagem alterada: %q", out)
	}

	sb.Reset()
	if _, err := w.Write([]byte("\n")); err != nil {
		t.Fatalf("write vazio: %v", err)
	}
	if sb.String() != "\n" {
		t.Fatalf("write vazio alterado: %q", sb.String())
	}
}
