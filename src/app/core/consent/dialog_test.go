package consent

import (
	"strings"
	"testing"
)

func TestLanguageMapping(t *testing.T) {
	cases := map[string]string{
		"":      "pt",
		"pt-BR": "pt",
		"pt":    "pt",
		"en-US": "en",
		"es-ES": "es",
		"de-DE": "en",
	}
	for locale, want := range cases {
		if got := Language(locale); got != want {
			t.Errorf("Language(%q) = %q, want %q", locale, got, want)
		}
	}
}

func TestBuildLocalizesActionAndLabels(t *testing.T) {
	req := Request{Kind: KindDestructive, Action: "service_stop", Target: "Spooler"}

	pt := Build(req, "pt-BR")
	if !strings.Contains(pt.Question, "PARAR um serviço do Windows") || !strings.Contains(pt.Question, "Spooler") {
		t.Fatalf("pt sem ação/alvo esperados: %q", pt.Question)
	}
	if pt.Approve != "Autorizar" || pt.Deny != "Negar" {
		t.Fatalf("rótulos pt inesperados: %q/%q", pt.Approve, pt.Deny)
	}

	en := Build(req, "en-US")
	if !strings.Contains(en.Question, "STOP a Windows service") || en.Approve != "Allow" || en.Deny != "Deny" {
		t.Fatalf("en inesperado: %q %q/%q", en.Question, en.Approve, en.Deny)
	}

	es := Build(req, "es-ES")
	if !strings.Contains(es.Question, "DETENER un servicio de Windows") || es.Approve != "Permitir" {
		t.Fatalf("es inesperado: %q %q", es.Question, es.Approve)
	}
}

func TestBuildFallsBackForUnknownAction(t *testing.T) {
	d := Build(Request{Kind: KindDestructive, Action: "acao_inexistente"}, "pt")
	if !strings.Contains(d.Question, "EXECUTAR uma ação no computador") {
		t.Fatalf("sem frase de fallback: %q", d.Question)
	}
}

func TestBuildIncludesTargetAndExtra(t *testing.T) {
	d := Build(Request{
		Kind:   KindWrite,
		Action: "write_file",
		Target: "relatorio (.pdf)",
		Extra:  []string{"C:/pasta"},
	}, "pt")
	if !strings.Contains(d.Question, "GRAVAR um arquivo no disco") {
		t.Fatalf("ação de gravação ausente: %q", d.Question)
	}
	if !strings.Contains(d.Question, "Alvo: relatorio (.pdf)") || !strings.Contains(d.Question, "C:/pasta") {
		t.Fatalf("alvo/extra ausentes: %q", d.Question)
	}
	if !strings.Contains(d.Question, "somente desta vez") {
		t.Fatalf("garantia de autorização única ausente: %q", d.Question)
	}
}

func TestApproveDenyLabelsKnownLanguages(t *testing.T) {
	if ApproveLabel("pt-BR") != "Autorizar" || DenyLabel("pt-BR") != "Negar" {
		t.Fatal("rótulos pt incorretos")
	}
	if ApproveLabel("en") != "Allow" || DenyLabel("en") != "Deny" {
		t.Fatal("rótulos en incorretos")
	}
	if ApproveLabel("es") != "Permitir" || DenyLabel("es") != "Negar" {
		t.Fatal("rótulos es incorretos")
	}
}
