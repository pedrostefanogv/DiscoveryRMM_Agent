//go:build windows

package inventory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discovery/app/appstore"
)

func TestBuildCustomCommandSpecCommandKind(t *testing.T) {
	spec, err := buildCustomCommandSpec(appstore.CommandKindCommand, "setup.exe /S", `C:\Temp`)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	want := []string{"cmd", "/C", "setup.exe /S"}
	if len(spec.argv) != len(want) {
		t.Fatalf("argv = %v, esperado %v", spec.argv, want)
	}
	for i := range want {
		if spec.argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, esperado %q", i, spec.argv[i], want[i])
		}
	}
	if spec.scriptBody != "" || spec.scriptPath != "" {
		t.Fatalf("modo comando nao deve gerar arquivo de script: %+v", spec)
	}
}

func TestBuildCustomCommandSpecCmdScript(t *testing.T) {
	spec, err := buildCustomCommandSpec(appstore.CommandKindCmdScript, "setup.exe /S\r\ncopy a b", `C:\Temp dir`)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !strings.EqualFold(filepath.Ext(spec.scriptPath), ".cmd") {
		t.Fatalf("script CMD deveria ser .cmd, veio %q", spec.scriptPath)
	}
	if !strings.HasPrefix(spec.scriptPath, `C:\Temp dir`) {
		t.Fatalf("script deveria ficar no workdir, veio %q", spec.scriptPath)
	}
	if spec.argv[0] != "cmd" || spec.argv[1] != "/C" {
		t.Fatalf("argv inesperado: %v", spec.argv)
	}
	// O caminho vai cru no argv: o exec do Go cita quando há espaços. Aspas
	// manuais fariam o cmd /C procurar um arquivo com aspas no nome.
	if spec.argv[2] != spec.scriptPath {
		t.Fatalf("caminho do .cmd deveria ir sem aspas manuais, veio %q", spec.argv[2])
	}
	if !strings.Contains(spec.scriptBody, "setup.exe /S") {
		t.Fatalf("corpo do script deveria conter o comando original, veio %q", spec.scriptBody)
	}
}

func TestBuildCustomCommandSpecPowerShellScript(t *testing.T) {
	spec, err := buildCustomCommandSpec(appstore.CommandKindPowerShellScript, "Start-Process msiexec -ArgumentList '/i app.msi /qn'", `C:\Temp`)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !strings.EqualFold(filepath.Ext(spec.scriptPath), ".ps1") {
		t.Fatalf("script PowerShell deveria ser .ps1, veio %q", spec.scriptPath)
	}
	want := []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", spec.scriptPath}
	if len(spec.argv) != len(want) {
		t.Fatalf("argv = %v, esperado %v", spec.argv, want)
	}
	for i := range want {
		if spec.argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, esperado %q", i, spec.argv[i], want[i])
		}
	}
	if !strings.Contains(spec.scriptBody, "Start-Process msiexec") {
		t.Fatalf("corpo do .ps1 deveria conter o comando original, veio %q", spec.scriptBody)
	}
}

func TestBuildCustomCommandSpecUnknownKindFallsBackToCommand(t *testing.T) {
	spec, err := buildCustomCommandSpec(42, "setup.exe", "")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if spec.argv[0] != "cmd" || spec.argv[1] != "/C" || spec.argv[2] != "setup.exe" {
		t.Fatalf("tipo desconhecido deveria cair no cmd /C, veio %v", spec.argv)
	}
}

func TestBuildCustomCommandSpecScriptWithoutWorkDirUsesTemp(t *testing.T) {
	spec, err := buildCustomCommandSpec(appstore.CommandKindCmdScript, "echo oi", "")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !strings.HasPrefix(filepath.Clean(spec.scriptPath), filepath.Clean(os.TempDir())) {
		t.Fatalf("sem workdir o script deveria ir para o temp, veio %q", spec.scriptPath)
	}
}

// runCustomShellCommandKind gravando .cmd e executando de verdade.
func TestRunCustomShellCommandKindCmdScriptExecutes(t *testing.T) {
	// Workdir com espaço de propósito: o caminho do .cmd passa pelo exec do Go.
	workDir := filepath.Join(t.TempDir(), "dir com espaco")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("criar workdir: %v", err)
	}
	svc := &Service{}

	out, err := svc.runCustomShellCommandKind(context.Background(), "@echo off\r\necho script-cmd-ok", workDir, appstore.CommandKindCmdScript)
	if err != nil {
		t.Fatalf("execucao do .cmd falhou: %v (saida=%q)", err, out)
	}
	if !strings.Contains(out, "script-cmd-ok") {
		t.Fatalf("saida do .cmd nao contem o eco esperado: %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "discovery-custom-install.cmd")); statErr != nil {
		t.Fatalf("arquivo .cmd nao foi gravado no workdir: %v", statErr)
	}
}

func TestRunCustomShellCommandKindPowerShellScriptExecutes(t *testing.T) {
	workDir := t.TempDir()
	svc := &Service{}

	out, err := svc.runCustomShellCommandKind(context.Background(), "Write-Output 'script-ps-ok'", workDir, appstore.CommandKindPowerShellScript)
	if err != nil {
		t.Fatalf("execucao do .ps1 falhou: %v (saida=%q)", err, out)
	}
	if !strings.Contains(out, "script-ps-ok") {
		t.Fatalf("saida do .ps1 nao contem o texto esperado: %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "discovery-custom-install.ps1")); statErr != nil {
		t.Fatalf("arquivo .ps1 nao foi gravado no workdir: %v", statErr)
	}
}

func TestRunCustomShellCommandKindRejectsEmptyAndNonZeroExit(t *testing.T) {
	svc := &Service{}
	if _, err := svc.runCustomShellCommandKind(context.Background(), "   ", "", appstore.CommandKindCommand); err == nil {
		t.Fatal("comando vazio deveria ser rejeitado")
	}

	_, err := svc.runCustomShellCommandKind(context.Background(), "exit /b 1", t.TempDir(), appstore.CommandKindCmdScript)
	if err == nil {
		t.Fatal("script com exit != 0 deveria devolver erro")
	}
	if !strings.Contains(err.Error(), "erro executando comando custom") {
		t.Fatalf("mensagem de erro inesperada: %v", err)
	}
}

// Sem workdir o script é gravado no %TEMP% do sistema e precisa ser removido ao
// final: desinstalação/upgrade remoto não têm uma pasta temporária que limpe o
// arquivo depois (antes ele ficava acumulando no %TEMP%).
func TestRunCustomShellCommandKindWithoutWorkDirRemovesScript(t *testing.T) {
	svc := &Service{}

	if _, err := svc.runCustomShellCommandKind(context.Background(), "@echo off\r\necho sem-workdir", "", appstore.CommandKindCmdScript); err != nil {
		t.Fatalf("execucao do .cmd falhou: %v", err)
	}

	path := filepath.Join(os.TempDir(), "discovery-custom-install.cmd")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("script temporario deveria ter sido removido ao final; stat err=%v", err)
	}
}

// runCustomShellCommand mantém o comportamento legado (cmd /C) para os
// chamadores que não informam o tipo.
func TestRunCustomShellCommandKeepsLegacyBehaviour(t *testing.T) {
	out, err := (&Service{}).runCustomShellCommand(context.Background(), "echo legado-ok", t.TempDir())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !strings.Contains(out, "legado-ok") {
		t.Fatalf("saida inesperada: %q", out)
	}
}
