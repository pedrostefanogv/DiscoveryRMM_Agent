//go:build windows

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regressão: runUninstallCommand executa via cmd /C com a linha crua
// (processutil.ApplyRawCmdLine). O UninstallString do registro vem citado
// (ex.: "C:\Program Files\App\unins.exe" /quiet) e, com o quoting padrão do
// exec do Go, o cmd.exe recebia \\" no meio da linha e rejeitava o comando.
func TestRunUninstallCommandQuotedPathWithSpace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Space Dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("criar diretorio: %v", err)
	}
	script := filepath.Join(dir, "uninstall tool.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\necho uninstall-ok %1\r\n"), 0o755); err != nil {
		t.Fatalf("gravar script: %v", err)
	}

	command := "\"" + script + "\" /quiet"
	out, err := runUninstallCommand(nil, command)
	if err != nil {
		t.Fatalf("comando citado com espaco falhou: %v (saida=%q)", err, out)
	}
	if !strings.Contains(out, "uninstall-ok /quiet") {
		t.Fatalf("saida inesperada: %q", out)
	}
}


// Regressão /D /S /C: UninstallString com caminho citado E argumento citado
// (ex.: /DEST="C:... com espaço") — o /C simples quebrava no primeiro espaço.
func TestRunUninstallCommandQuotedPathAndQuotedArg(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Space Dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("criar diretorio: %v", err)
	}
	dest := filepath.Join(dir, "Dest Two")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("criar dest: %v", err)
	}
	script := filepath.Join(dir, "uninstall tool.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\necho dest-ok %2\r\n"), 0o755); err != nil {
		t.Fatalf("gravar script: %v", err)
	}

	command := "\"" + script + "\" /DEST=\"" + dest + "\""
	out, err := runUninstallCommand(nil, command)
	if err != nil {
		t.Fatalf("caminho citado + arg citado falhou: %v (saida=%q)", err, out)
	}
	if !strings.Contains(out, "dest-ok") {
		t.Fatalf("saida inesperada: %q", out)
	}
}

// O formato clássico do msiexec — executável sem aspas + argumentos, GUID entre
// chaves — continua passando intacto pela linha crua do cmd /C.
func TestRunUninstallCommandMsiexecFormatStillWorks(t *testing.T) {
	command := "echo msi-faux-ok /x {12345678-9ABC-DEF0-1234-567890ABCDEF} /qn"
	out, err := runUninstallCommand(nil, command)
	if err != nil {
		t.Fatalf("formato msiexec falhou: %v (saida=%q)", err, out)
	}
	if !strings.Contains(out, "msi-faux-ok /x {12345678-9ABC-DEF0-1234-567890ABCDEF} /qn") {
		t.Fatalf("saida inesperada: %q", out)
	}
}
