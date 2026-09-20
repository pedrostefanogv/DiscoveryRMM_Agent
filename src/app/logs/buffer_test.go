package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discovery/app/core/buildinfo"
)

// Toda linha persistida no agent.log deve carregar a assinatura de revisão
// (hash curto do commit) junto da data — contrato compartilhado com
// chat_logs.jsonl e agent-service.log.
func TestBufferFilePersistenceStampsCodeRev(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.log")
	b := New()
	if err := b.EnableFilePersistence(path); err != nil {
		t.Fatalf("EnableFilePersistence: %v", err)
	}
	b.Append("linha de teste")
	b.CloseFile()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	line := strings.TrimSpace(string(data))
	if !strings.Contains(line, "linha de teste") {
		t.Fatalf("conteúdo ausente: %q", line)
	}
	if !strings.Contains(line, "["+buildinfo.Revision()+"]") {
		t.Fatalf("assinatura de revisão ausente: %q", line)
	}
}
