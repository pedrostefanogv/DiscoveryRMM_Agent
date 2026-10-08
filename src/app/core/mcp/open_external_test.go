package mcp

import (
	"context"
	"strings"
	"testing"
)

// As tools de abrir pasta/app precisam estar no registro (o LLM só conhece o
// que é exposto) e exigir AUTORIZAÇÃO por ação — sem isso um clique abriria
// Explorer/apps sem o usuário aprovar.
func TestOpenExternalToolsRegisteredAndGuarded(t *testing.T) {
	reg := NewRegistry()
	RegisterDiscoveryTools(reg, stubAppBridge{})

	for _, name := range []string{"open_folder", "open_app", "list_installed_apps"} {
		if reg.Find(name) == nil {
			t.Fatalf("ferramenta %q nao registrada", name)
		}
	}

	folder := ToolConsentFor("open_folder", map[string]any{"folder": "downloads", "reason": "abrir os downloads"})
	if folder == nil {
		t.Fatal("open_folder deveria exigir autorizacao")
	}
	if folder.Action != "open_folder" || folder.Target != "downloads" {
		t.Fatalf("pedido inesperado: %#v", folder)
	}
	if got := strings.Join(folder.Extra, " | "); !strings.Contains(got, "Motivo informado pela IA") {
		t.Fatalf("pedido sem o motivo informado: %q", got)
	}

	openApp := ToolConsentFor("open_app", map[string]any{"app": "Google Chrome"})
	if openApp == nil {
		t.Fatal("open_app deveria exigir autorizacao")
	}
	if openApp.Action != "open_app" || openApp.Target != "Google Chrome" {
		t.Fatalf("pedido inesperado: %#v", openApp)
	}

	// Listagem é leitura pura: NÃO pode pedir autorização.
	if req := ToolConsentFor("list_installed_apps", map[string]any{"query": "chrome"}); req != nil {
		t.Fatalf("list_installed_apps nao deveria exigir autorizacao: %#v", req)
	}

	// Apelido sensível ganha destaque no pedido.
	sens := ToolConsentFor("open_folder", map[string]any{"folder": "appdata"})
	if sens == nil || !strings.Contains(strings.Join(sens.Extra, " | "), "dados de aplicativos") {
		t.Fatalf("apelido sensivel sem aviso: %#v", sens)
	}
}

// Os parâmetros obrigatórios são validados ANTES de tocar no bridge.
func TestOpenExternalToolsRequireParams(t *testing.T) {
	reg := NewRegistry()
	RegisterDiscoveryTools(reg, stubAppBridge{})

	if _, err := reg.Call(context.Background(), "open_folder", []byte("{}")); err == nil {
		t.Fatal("open_folder sem 'folder' deveria falhar")
	}
	if _, err := reg.Call(context.Background(), "open_app", []byte("{}")); err == nil {
		t.Fatal("open_app sem 'app' deveria falhar")
	}
	// Contexto cancelado (botão Parar) não pode abrir nada.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reg.Call(ctx, "open_folder", []byte(`{"folder":"downloads"}`)); err == nil {
		t.Fatal("contexto cancelado deveria abortar a abertura")
	}
}
