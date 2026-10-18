package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regressão do aviso de versão UI × serviço (A.2.2).
//
// BUG CORRIGIDO: o binding Go (GetIPCServiceInfo) e as mensagens i18n existiam,
// mas NENHUM código do frontend consumia o binding — o aviso de par misto nunca
// aparecia. Além disso, um binding ausente (build antiga/navegador) ou um JSON
// inesperado não pode derrubar o boot. Este teste falha se o wiring for
// removido ou perder as guardas de robustez.
func TestFrontend_ServiceVersionMismatchNoticeWired(t *testing.T) {
	jsPath := filepath.Join("..", "frontend", "js", "app-init.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", jsPath, err)
	}
	jsText := string(js)

	// Consumo do binding com guarda de ausência (o método só existe depois de
	// `wails3 generate bindings`; em build antiga/navegador é undefined).
	for _, needle := range []string{
		"GetIPCServiceInfo",
		"typeof api.GetIPCServiceInfo !== \"function\"",
		"JSON.parse(raw)",
		"info.connected !== true || info.mismatch !== true",
		"app.serviceVersionMismatch",
		"showToast(message, \"error\")",
		"service:ipc_state",
	} {
		if !strings.Contains(jsText, needle) {
			t.Fatalf("app-init.js precisa conter %q para o aviso de versão UI×serviço funcionar", needle)
		}
	}

	// O boot chama a checagem (uma vez) e ela é tolerante a falha: JSON inválido
	// e binding ausente são silenciosos.
	if !strings.Contains(jsText, "\n  initServiceVersionNotice();") {
		t.Fatal("bootstrapApp precisa chamar initServiceVersionNotice() (senão o aviso nunca roda)")
	}
	parseIdx := strings.Index(jsText, "JSON.parse(raw)")
	if parseIdx < 0 {
		t.Fatal("app-init.js precisa parsear o JSON devolvido por GetIPCServiceInfo")
	}
	tryIdx := strings.LastIndex(jsText[:parseIdx], "try {")
	catchIdx := strings.Index(jsText[parseIdx:], "} catch (_) {")
	if tryIdx < 0 || catchIdx < 0 {
		t.Fatal("JSON.parse do retorno do binding precisa estar dentro de try/catch (erro não pode quebrar o boot)")
	}

	// As duas locales precisam ter a mensagem e os MESMOS placeholders usados
	// pelo translate({ui, service}).
	utilsPath := filepath.Join("..", "frontend", "js", "app-utils.js")
	utils, err := os.ReadFile(utilsPath)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", utilsPath, err)
	}
	utilsText := string(utils)
	if got := strings.Count(utilsText, "\"app.serviceVersionMismatch\""); got < 2 {
		t.Fatalf("app.serviceVersionMismatch precisa existir em pt-br e en-us; ocorrências=%d", got)
	}
	if !strings.Contains(utilsText, "{ui}") || !strings.Contains(utilsText, "{service}") {
		t.Fatal("a mensagem precisa interpolar {ui} e {service} (nomes usados por checkServiceVersionMismatch)")
	}
}
