package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesTab_VisibilityGateHerdavel(t *testing.T) {
	initPath := filepath.Join("..", "frontend", "js", "app-init.js")
	raw, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", initPath, err)
	}
	js := string(raw)

	body, ok := jsFunctionBody(js, "function applyAgentFeatureVisibility()")
	if !ok {
		t.Fatal("applyAgentFeatureVisibility não encontrada em app-init.js")
	}

	if !strings.Contains(body, "cfg.filesTabEnabled === false") {
		t.Fatal("applyAgentFeatureVisibility precisa conter \"cfg.filesTabEnabled === false\"")
	}
	if !strings.Contains(body, "!debugMode && cfg.filesTabEnabled === false") {
		t.Fatal("o gate da aba Arquivos precisa manter o prefixo \"!debugMode &&\"")
	}
	for _, needle := range []string{"tabFilesBtn", "filesViewEl"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("applyAgentFeatureVisibility precisa passar %q para o gate da aba Arquivos", needle)
		}
	}
	if strings.Contains(body, "hideTab(tabFilesBtn") {
		t.Fatal("a aba Arquivos não pode usar hideTab: use setTabVisibility")
	}
}
