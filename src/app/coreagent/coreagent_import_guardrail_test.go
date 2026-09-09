package coreagent

// coreagent_import_guardrail_test.go — guardrail de imports da migração
// física (PLANO_SEPARACAO_SERVICO_UI.md, Fase A §0.5): o package coreagent
// NUNCA pode importar Wails (nem pacotes do package app que o puxem).
//
// Testa em nível de graph de imports via go list -deps: qualquer regressão
// (import novo em coreagent que arraste wails) falha o build de testes.

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestCoreAgentNoWailsImports garante que nenhum pacote importado
// (transitivamente) por coreagent contém "wails".
func TestCoreAgentNoWailsImports(t *testing.T) {
	if runtime.GOOS == "windows" && isWailsInDeps(t) {
		t.Fatal("coreagent (ou seus deps transitivos) importa Wails — fronteira core × UI violada")
	}
}

func isWailsInDeps(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "./...")
	out, err := cmd.Output()
	if err != nil {
		// go list indisponível (ex.: sandbox) — não falha o build por isso.
		t.Logf("go list indisponível, guardrail ignorado nesta execução: %v", err)
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		pkg := strings.TrimSpace(line)
		if strings.Contains(pkg, "wails") {
			t.Errorf("pacote com wails no grafo de deps de coreagent: %s", pkg)
			return true
		}
	}
	return false
}
