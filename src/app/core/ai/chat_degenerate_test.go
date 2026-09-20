package ai

import "testing"

// Regressão do vazamento de raciocínio/saída degenerada visto em produção em
// 20/09: o modelo emitiu fragmentos ("...", "…", "---", palavras truncadas) e
// planejamento interno em inglês no lugar da resposta. O detector deve
// acionar o retry forçado (regeneração limpa) nesses casos e NUNCA em
// respostas legítimas.
func TestDetectDegenerateResponse(t *testing.T) {
	degenerate := "Desculpe, parece ** !\n...\n...\nSe você quiser **\n...\nVou\n...\nDesr\n...\nO\n...\nParece que a mensagem ficou\n...\nNão\n??\n---\n…\n…\n…\n"
	if !detectDegenerateResponse(degenerate) {
		t.Fatalf("saída degenerada (fragmentos/reticências) deveria ser detectada")
	}

	plan := "We need to respond properly. The user wants to verify Chrome also.\nWe have already added comment.\nSince they want verification, we can run listinstalledpackages. Let's do that.\nWe need to call the tool now."
	if !detectDegenerateResponse(plan) {
		t.Fatalf("raciocínio vazado em inglês deveria ser detectado")
	}

	normal := "Para melhorar a velocidade do seu PC, siga estas etapas:\n\n1. Feche as abas do navegador.\n2. Encerre o qBittorrent.\n\n---\n\nSe a lentidão persistir, posso abrir um chamado de suporte para uma análise mais detalhada da sua máquina."
	if detectDegenerateResponse(normal) {
		t.Fatalf("resposta legítima com um separador --- não deveria ser detectada como degenerada")
	}

	if detectDegenerateResponse("Ok") {
		t.Fatalf("resposta curta não deveria ser avaliada como degenerada")
	}
}
