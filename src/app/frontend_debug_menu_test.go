package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regressão do relato: "abre o agent em modo de debug e não está mostrando uma
// entrada no menu para acessar a página de P2P e parece estar faltando mais
// páginas. Quando abre o agent em modo de debug deve mostrar no menu todas as
// entradas disponíveis."
//
// CAUSA RAIZ: applyAgentFeatureVisibility (frontend/js/app-init.js) rodava
// DEPOIS do GetAgentConfiguration e só ESCONDIA abas, sem considerar o modo
// debug. Com p2pFilesEnabled=false (ou sem caminho de descoberta) o item P2P
// sumia do menu mesmo em debug; o mesmo valia para Zero-Touch e para as features
// desabilitadas no servidor (Loja, Chat IA, Suporte, Base de Conhecimento).
//
// Este teste falha se o gate voltar a esconder abas no modo debug ou se a
// decisão voltar a ser irreversível ("só esconde", sem reexibir quando o modo
// muda).
func TestFrontend_DebugModeMostraTodasAsEntradasDoMenu(t *testing.T) {
	initPath := filepath.Join("..", "frontend", "js", "app-init.js")
	raw, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", initPath, err)
	}
	js := string(raw)

	// O gate precisa ser reversível: setTabVisibility aplica "deve ficar oculta"
	// nos dois sentidos. A versão antiga (hideTabIfNeeded) só adicionava a classe
	// hidden e por isso nunca reexibia uma aba escondida no modo normal.
	if strings.Contains(js, "function hideTabIfNeeded") {
		t.Fatal("app-init.js não pode voltar a usar hideTabIfNeeded (só esconde, nunca reexibe): use setTabVisibility")
	}
	for _, needle := range []string{
		"function setTabVisibility(tabBtn, viewEl, shouldHide)",
		"tabBtn.classList.toggle(\"hidden\", !!shouldHide);",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("app-init.js precisa conter %q para o gate ser reversível", needle)
		}
	}

	body, ok := jsFunctionBody(js, "function applyAgentFeatureVisibility()")
	if !ok {
		t.Fatal("applyAgentFeatureVisibility não encontrada em app-init.js")
	}

	// Modo debug: nenhuma aba é ocultada pelo gate de feature flags.
	if !strings.Contains(body, "var debugMode = isDebugRuntimeMode();") {
		t.Fatal("applyAgentFeatureVisibility precisa consultar isDebugRuntimeMode(): em modo debug o menu mostra todas as entradas")
	}
	if got := strings.Count(body, "!debugMode &&"); got < 6 {
		t.Fatalf("toda entrada condicionada a feature flag precisa ignorar o gate em modo debug (!debugMode &&): ocorrências=%d, esperado>=6", got)
	}

	// As entradas que o usuário citou como ausentes precisam existir na sidebar
	// e ser exibidas pelo gate quando debugMode=true.
	sidebarPath := filepath.Join("..", "frontend", "partials", "sidebar.html")
	sidebarRaw, err := os.ReadFile(sidebarPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", sidebarPath, err)
	}
	sidebar := string(sidebarRaw)
	for _, needle := range []string{
		"id=\"tabP2P\"",
		"id=\"tabZeroTouchConfig\"",
	} {
		if !strings.Contains(sidebar, needle) {
			t.Fatalf("sidebar.html precisa manter a entrada %q (em modo debug ela deve aparecer)", needle)
		}
	}
	for _, needle := range []string{
		"tabP2PBtn",
		"tabZeroTouchConfigBtn",
		"cfg.p2pFilesEnabled === false",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("applyAgentFeatureVisibility precisa continuar tratando %q", needle)
		}
	}

	// Em modo NORMAL o gate de feature flag NÃO pode reexibir P2P/Zero-Touch:
	// a visibilidade base dessas abas de diagnóstico é do modo de runtime
	// (applyRuntimeTabVisibility), que as esconde fora do debug. Se o gate usar
	// a decisão reversível (setTabVisibility), o P2P reaparece no menu normal
	// quando a feature está habilitada — regressão.
	if strings.Contains(body, "setTabVisibility(\n    tabP2PBtn") || strings.Contains(body, "setTabVisibility(\n    tabZeroTouchConfigBtn") {
		t.Fatal("P2P/Zero-Touch não podem usar setTabVisibility (reexibiria a aba no modo normal); use hideTab")
	}
	for _, needle := range []string{
		"function hideTab(tabBtn, viewEl)",
		"hideTab(tabP2PBtn, p2pViewEl);",
		"hideTab(tabZeroTouchConfigBtn, zeroTouchConfigViewEl);",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("app-init.js precisa conter %q para o gate só ocultar P2P/Zero-Touch no modo normal", needle)
		}
	}

	// A visibilidade por modo de runtime (app-core-globals.js) é a primeira
	// camada: em debug libera qualquer aba e reexibe os itens só-de-debug
	// (P2P incluso). Sem isso o gate de feature flags não teria efeito.
	globalsPath := filepath.Join("..", "frontend", "js", "app-core-globals.js")
	globalsRaw, err := os.ReadFile(globalsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", globalsPath, err)
	}
	globals := string(globalsRaw)
	allowedBody, ok := jsFunctionBody(globals, "function isRuntimeTabAllowed(tab)")
	if !ok {
		t.Fatal("isRuntimeTabAllowed não encontrada em app-core-globals.js")
	}
	if !strings.Contains(allowedBody, "if (isDebugRuntimeMode()) {") || !strings.Contains(allowedBody, "return true;") {
		t.Fatal("isRuntimeTabAllowed precisa liberar TODAS as abas em modo debug")
	}
	for _, needle := range []string{
		"tabP2PBtn.classList.toggle('hidden', hiddenInNormal)",
		"tabZeroTouchConfigBtn.classList.toggle('hidden', hiddenInNormal)",
	} {
		if !strings.Contains(globals, needle) {
			t.Fatalf("app-core-globals.js precisa reexibir/esconder a entrada %q conforme o modo de runtime", needle)
		}
	}
}

// jsFunctionBody devolve o corpo de uma função JS a partir da assinatura,
// até a primeira linha de fechamento "}" na coluna zero. Serve para afirmar
// regras DENTRO de uma função específica sem se confundir com outros trechos do
// arquivo (convenção dos testes de assets do frontend neste repositório).
func jsFunctionBody(src, signature string) (string, bool) {
	// Assets do frontend são versionados com CRLF no Windows; normaliza para
	// que os delimitadores procurados abaixo ("\n") funcionem em qualquer SO.
	src = strings.ReplaceAll(src, "\r\n", "\n")
	start := strings.Index(src, signature)
	if start < 0 {
		return "", false
	}
	rest := src[start:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}
