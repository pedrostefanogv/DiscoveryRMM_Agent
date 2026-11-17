package app

// Regressão do relato 2026-11-17 (ver server_authorization.go): enquanto o
// agente aguardava liberação do servidor, Loja/Suporte/Base de Conhecimento
// exibiam o erro CRU do backend (403 "Agent registration is pending
// (zero-touch)") em vez de explicar que faltava liberação.
//
// Estes testes travam o contrato do frontend: as três telas precisam consultar
// o estado compartilhado e renderizar a tela de espera, e o módulo de estado
// precisa existir/carregar antes delas.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFrontendAsset(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", "frontend"}, parts...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", path, err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// O módulo de estado precisa existir e expor o contrato consumido pelas telas.
func TestFrontend_ServerAuthorizationModuleContract(t *testing.T) {
	js := readFrontendAsset(t, "js", "server-authorization.js")
	for _, needle := range []string{
		"function isServerAuthorizationPending()",
		"function applyServerAuthorizationState(onboardingStatus)",
		"function onServerAuthorizationChange(fn)",
		"function renderServerAuthWaitingState(container, opts)",
		"function serverAuthLooksLikePendingError(err)",
		"function refreshServerAuthorizationState()",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("server-authorization.js precisa conter %q", needle)
		}
	}
	// O 403 do backend é reconhecido pela assinatura real da resposta.
	if !strings.Contains(js, "registration is pending") {
		t.Fatal("o detector precisa reconhecer a assinatura 'registration is pending' do 403 de agente pendente")
	}
}

// bootstrap-partials.js precisa carregar o módulo ANTES das três telas.
func TestFrontend_ServerAuthorizationModuleLoadsBeforeScreens(t *testing.T) {
	js := readFrontendAsset(t, "js", "bootstrap-partials.js")
	idxModule := strings.Index(js, "js/server-authorization.js")
	if idxModule < 0 {
		t.Fatal("bootstrap-partials.js precisa carregar js/server-authorization.js")
	}
	for _, screen := range []string{"js/app-store-updates.js", "js/app-support.js", "js/app-knowledge.js"} {
		idxScreen := strings.Index(js, screen)
		if idxScreen < 0 {
			t.Fatalf("bootstrap-partials.js precisa continuar carregando %s", screen)
		}
		if idxScreen < idxModule {
			t.Fatalf("%s precisa carregar DEPOIS de js/server-authorization.js (o módulo define o estado que ela consome)", screen)
		}
	}
}

// Cada tela precisa renderizar a espera em vez do erro cru quando o agente
// aguarda liberação — e recarregar sozinha quando a liberação chega.
func TestFrontend_ScreensShowWaitingStateInsteadOfRawError(t *testing.T) {
	cases := []struct {
		file     string
		needles  []string
		contesto string
	}{
		{
			file: "app-store-updates.js",
			needles: []string{
				"if (isServerAuthorizationPending() || serverAuthLooksLikePendingError(error))",
				"showServerAuthPendingStore();",
				"onServerAuthorizationChange(function (mode)",
			},
			contesto: "Loja",
		},
		{
			file: "app-support.js",
			needles: []string{
				"if (isServerAuthorizationPending() || serverAuthLooksLikePendingError(err))",
				"renderServerAuthWaitingState(supportTicketsListEl",
				"onServerAuthorizationChange(function (mode)",
			},
			contesto: "Suporte",
		},
		{
			file: "app-knowledge.js",
			needles: []string{
				"if (isServerAuthorizationPending() || serverAuthLooksLikePendingError(err))",
				"renderServerAuthWaitingState(kbArticlesListEl",
				"onServerAuthorizationChange(function (mode)",
			},
			contesto: "Base de Conhecimento",
		},
	}
	for _, c := range cases {
		js := readFrontendAsset(t, "js", c.file)
		for _, needle := range c.needles {
			if !strings.Contains(js, needle) {
				t.Fatalf("%s (%s) precisa conter %q para mostrar a espera de liberação em vez do erro cru", c.file, c.contesto, needle)
			}
		}
	}
}

// A tela de espera da Loja precisa renderizar no container REAL do catálogo.
// O bug corrigido na revisão: showServerAuthPendingStore procurava
// 'catalogGrid'/'storeCatalogGrid' — ids inexistentes — e o painel nunca
// aparecia (o usuário via só o toast, com a área do catálogo vazia).
func TestFrontend_StoreWaitingStateTargetsRealContainer(t *testing.T) {
	store := readFrontendAsset(t, "js", "app-store-updates.js")
	if strings.Contains(store, "catalogGrid") || strings.Contains(store, "storeCatalogGrid") {
		t.Fatal("showServerAuthPendingStore não pode usar ids inexistentes (catalogGrid/storeCatalogGrid)")
	}
	if !strings.Contains(store, "document.getElementById('cards')") {
		t.Fatal("showServerAuthPendingStore precisa renderizar no container real #cards")
	}

	// O id usado no partial precisa ser o mesmo que o JS procura.
	view := readFrontendAsset(t, "partials", "views", "storeView.html")
	if !strings.Contains(view, `id="cards"`) {
		t.Fatal("storeView.html precisa manter o container #cards consumido pela Loja")
	}
}

// i18n nos dois idiomas + overlay usando a mensagem específica de liberação.
func TestFrontend_ServerAuthorizationCopyAndOverlay(t *testing.T) {
	utils := readFrontendAsset(t, "js", "app-utils.js")
	if got := strings.Count(utils, "\"serverAuth.pending.message\""); got != 2 {
		t.Fatalf("serverAuth.pending.message precisa existir nos dois idiomas (pt-br/en); ocorrências=%d", got)
	}
	if got := strings.Count(utils, "\"serverAuth.pending.store\""); got != 2 {
		t.Fatalf("serverAuth.pending.store precisa existir nos dois idiomas; ocorrências=%d", got)
	}

	globals := readFrontendAsset(t, "js", "app-core-globals.js")
	// A overlay publica o modo RESOLVIDO (o que ela exibe) para as telas de
	// conteúdo — publicar o onboardingStatus cru deixaria as telas divergentes
	// quando o GetOnboardingStatus falha e o modo vem do fallback local.
	if !strings.Contains(globals, "publishServerAuthorizationMode(mode, onboardingStatus)") {
		t.Fatal("syncProvisioningOverlayFromConfig precisa publicar o modo resolvido para as telas de conteúdo")
	}
	// ...e usa a mensagem específica de "aguardando liberação" no corpo do card.
	if !strings.Contains(globals, "serverAuthWaitingMessage()") {
		t.Fatal("a overlay precisa usar a mensagem de 'aguardando liberação do servidor' no modo awaiting-approval")
	}
}
