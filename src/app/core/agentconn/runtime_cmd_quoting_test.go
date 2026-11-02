//go:build windows

package agentconn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regressão: o tipo wire "cmd" executa via cmd /C com a linha de comando CRUA
// (processutil.ApplyRawCmdLine). Com o quoting padrão do os/exec, as aspas
// embutidas viravam \" e o cmd.exe rejeitava o comando inteiro. Aqui o comando
// tem caminho citado E argumento citado (mais de duas aspas), que é o caso que o
// /C puro também quebra — por isso o envelope /D /S /C.
func TestExecuteCommandCmdQuotedPathsWithSpace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Space Dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("criar diretorio: %v", err)
	}
	script := filepath.Join(dir, "remote tool.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\necho remote-ok %1\r\n"), 0o755); err != nil {
		t.Fatalf("gravar script: %v", err)
	}
	dest := filepath.Join(dir, "Dest Two")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("criar destino: %v", err)
	}

	command := "\"" + script + "\" /DEST=\"" + dest + "\""
	exitCode, output, errText := executeCommand(context.Background(), "cmd", map[string]any{"command": command})
	if exitCode != 0 || errText != "" {
		t.Fatalf("execucao falhou: exit=%d err=%q out=%q", exitCode, errText, output)
	}
	if !strings.Contains(output, "remote-ok /DEST") {
		t.Fatalf("saida inesperada: %q", output)
	}
}

// Comando sem aspas continua funcionando com o envelope /D /S /C.
func TestExecuteCommandCmdSimpleCommandStillWorks(t *testing.T) {
	exitCode, output, errText := executeCommand(context.Background(), "cmd", "echo simples-ok")
	if exitCode != 0 || errText != "" {
		t.Fatalf("execucao falhou: exit=%d err=%q out=%q", exitCode, errText, output)
	}
	if !strings.Contains(output, "simples-ok") {
		t.Fatalf("saida inesperada: %q", output)
	}
}
