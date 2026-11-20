package remotesession

import (
	"os"
	"strings"
	"testing"
)

// TestBannerNaoApareceParaRdp garante que a sessao kind=rdp NAO exibe o banner
// "Sessao remota em andamento" nem o icone da bandeja.
//
// MOTIVO: o banner e um aviso ao usuario LOCAL de que a tela DELE esta sendo
// vista. No acesso via tunel RDP isso nao se aplica — o RDP abre um LOGON
// SEPARADO no TermService, sem espelhar o console de quem esta na maquina.
// Na pratica o banner aparecia SOBRE o proprio desktop remoto (ruido) e
// sugeria, errado, que o operador estava vendo a sessao do usuario local.
//
// IMPORTANTE: o CONSENTIMENTO continua valendo para RDP. Banner e consentimento
// sao controles distintos — um informa, o outro autoriza.
func TestBannerNaoApareceParaRdp(t *testing.T) {
	src, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("ler manager.go: %v", err)
	}
	code := string(src)

	idx := strings.Index(code, "if kind == KindScreen || kind == KindAll || kind == KindProxy")
	if idx < 0 {
		t.Fatal("condicao do banner nao encontrada; o formato mudou")
	}
	// A condicao do banner nao pode conter KindRdp.
	cond := code[idx:]
	if end := strings.Index(cond, "{"); end > 0 {
		cond = cond[:end]
	}
	if strings.Contains(cond, "KindRdp") {
		t.Error("KindRdp voltou para a condicao do banner: o aviso apareceria sobre o " +
			"proprio desktop remoto, sem sentido para acesso via RDP")
	}

	// O consentimento DEVE continuar cobrindo RDP (controle distinto).
	if !strings.Contains(code, "kind == KindProxy || kind == KindRdp") {
		t.Error("o gate de consentimento perdeu o KindRdp; consentimento e banner sao " +
			"controles distintos e o consentimento deve continuar valendo para RDP")
	}
}
