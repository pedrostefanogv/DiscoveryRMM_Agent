package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regressão do kill switch da aba "Atualizações" do painel do agent
// (cfg.updatesTabEnabled).
//
// HISTÓRICO: as abas Loja / Chat IA / Suporte / Base de Conhecimento já tinham
// gate por feature flag do servidor (cfg.appStoreEnabled, cfg.chatAIEnabled,
// cfg.supportEnabled, cfg.knowledgeBaseEnabled), mas a aba "Atualizações" (apps
// pendentes via winget/choco) NÃO tinha kill switch: um site/cliente que
// desligasse as atualizações continuava vendo a aba e o botão "Verificar
// Atualizacoes".
//
// CONTRATO: cfg.updatesTabEnabled é herdável servidor -> cliente -> site
// (o servidor SEMPRE envia o valor já resolvido; o default TRUE existe apenas
// para payload antigo/incompleto, ou seja, campo ausente = aba visível — zero
// mudança de comportamento). O gate tem que ser o MESMO formato REVERSÍVEL das
// demais features (setTabVisibility, nunca hideTab): quando as runtime flags
// mudam, applyAgentFeatureVisibility roda de novo e reexibe a aba.
//
// Este teste falha se o gate sumir, virar irreversível ou deixar de ser
// ignorado em modo debug.
func TestUpdatesTab_VisibilityGateHerdavel(t *testing.T) {
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

	// (a) A chave do contrato tem que ser lida EXATAMENTE como congelada:
	// só o valor false explícito esconde a aba (ausente/true = visível).
	if !strings.Contains(body, "cfg.updatesTabEnabled === false") {
		t.Fatal("applyAgentFeatureVisibility precisa conter \"cfg.updatesTabEnabled === false\" (kill switch herdável; ausente = aba visível)")
	}

	// (b) O gate precisa mirar o botão da sidebar e a view da aba, e a decisão
	// tem que ser REVERSÍVEL (setTabVisibility). hideTab só esconde: usá-lo aqui
	// deixaria a aba oculta para sempre, mesmo que o site reative a feature.
	for _, needle := range []string{
		"tabUpdatesBtn",
		"updatesViewEl",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("applyAgentFeatureVisibility precisa passar %q para o gate da aba Atualizações", needle)
		}
	}
	if got := strings.Count(body, "setTabVisibility("); got < 5 {
		t.Fatalf("a aba Atualizações precisa usar setTabVisibility (decisão reversível), junto das demais features: chamadas=%d, esperado>=5", got)
	}
	// O argumento !debugMode && cfg.updatesTabEnabled === false tem que estar
	// DENTRO de uma chamada setTabVisibility — não basta a ocorrência solta.
	updatesGateAt := strings.Index(body, "cfg.updatesTabEnabled === false")
	callBefore := strings.LastIndex(body[:updatesGateAt], "setTabVisibility(")
	if callBefore < 0 {
		t.Fatal("cfg.updatesTabEnabled === false precisa estar dentro de uma chamada setTabVisibility(...)")
	}
	between := body[callBefore:updatesGateAt]
	if strings.Contains(between, ");") {
		t.Fatal("a chamada setTabVisibility do gate da aba Atualizações foi fechada antes de cfg.updatesTabEnabled: o gate não está no formato reversível esperado")
	}

	// setTabVisibility tem que existir e aplicar a classe nos DOIS sentidos.
	for _, needle := range []string{
		"function setTabVisibility(tabBtn, viewEl, shouldHide)",
		"tabBtn.classList.toggle(\"hidden\", !!shouldHide);",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("app-init.js precisa conter %q para o gate ser reversível", needle)
		}
	}
	// O gate da aba Atualizações não pode ser irreversível (hideTab).
	if strings.Contains(body, "hideTab(tabUpdatesBtn") {
		t.Fatal("a aba Atualizações não pode usar hideTab (só esconde, nunca reexibe): use setTabVisibility")
	}

	// (c) Modo debug ignora o gate: o prefixo !debugMode && é obrigatório no
	// mesmo formato das demais abas.
	if !strings.Contains(body, "!debugMode && cfg.updatesTabEnabled === false") {
		t.Fatal("o gate da aba Atualizações precisa manter o prefixo \"!debugMode &&\" (em modo debug o menu mostra todas as entradas)")
	}
	if !strings.Contains(body, "var debugMode = isDebugRuntimeMode();") {
		t.Fatal("applyAgentFeatureVisibility precisa consultar isDebugRuntimeMode() para o modo debug ignorar o gate")
	}

	// O gate roda no boot, com fallback para Status se a aba ativa for ocultada.
	if !strings.Contains(js, "applyAgentFeatureVisibility()") {
		t.Fatal("bootstrapApp precisa chamar applyAgentFeatureVisibility() (senão o gate nunca roda)")
	}
	if !strings.Contains(js, "function ensureActiveTabVisible()") {
		t.Fatal("ensureActiveTabVisible precisa existir: se a aba Atualizações for ocultada enquanto ativa, o agent volta para Status")
	}
}

// (d) Os ids usados pelo gate precisam continuar existindo nos assets: o botão
// da sidebar (id="tabUpdates") e a view (id="updatesView", com o botão
// id="checkUpdatesBtn"). Sem eles o gate silenciosamente não acharia o alvo.
func TestUpdatesTab_AssetsMantemIdsDoGate(t *testing.T) {
	sidebarPath := filepath.Join("..", "frontend", "partials", "sidebar.html")
	sidebarRaw, err := os.ReadFile(sidebarPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", sidebarPath, err)
	}
	if sidebar := string(sidebarRaw); !strings.Contains(sidebar, "id=\"tabUpdates\"") {
		t.Fatal("sidebar.html precisa manter o botão da aba Atualizações (id=\"tabUpdates\") para o gate ter alvo")
	}

	viewPath := filepath.Join("..", "frontend", "partials", "views", "updatesView.html")
	viewRaw, err := os.ReadFile(viewPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", viewPath, err)
	}
	view := string(viewRaw)
	for _, needle := range []string{
		"id=\"updatesView\"",
		"id=\"checkUpdatesBtn\"",
	} {
		if !strings.Contains(view, needle) {
			t.Fatalf("updatesView.html precisa manter %q para o gate e a ação de verificação continuarem ligados", needle)
		}
	}

	// Os elementos globais consumidos pelo gate vivem em app-core-globals.js.
	globalsPath := filepath.Join("..", "frontend", "js", "app-core-globals.js")
	globalsRaw, err := os.ReadFile(globalsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", globalsPath, err)
	}
	globals := string(globalsRaw)
	for _, needle := range []string{
		"const tabUpdatesBtn = document.getElementById('tabUpdates');",
		"const updatesViewEl = document.getElementById('updatesView');",
	} {
		if !strings.Contains(globals, needle) {
			t.Fatalf("app-core-globals.js precisa continuar expondo %q (consumido por applyAgentFeatureVisibility)", needle)
		}
	}
}
