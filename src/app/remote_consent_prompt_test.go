package app

import (
	"testing"
	"time"
)

func TestConsentCountdownClock(t *testing.T) {
	cases := map[time.Duration]string{
		0:                        "00:00",
		-5 * time.Second:         "00:00",
		1 * time.Second:          "00:01",
		59 * time.Second:         "00:59",
		60 * time.Second:         "01:00",
		61 * time.Second:         "01:01",
		600 * time.Second:        "10:00",
		90500 * time.Millisecond: "01:31", // arredonda para cima
	}
	for remaining, want := range cases {
		if got := consentCountdownClock(remaining); got != want {
			t.Errorf("consentCountdownClock(%s) = %q, want %q", remaining, got, want)
		}
	}
}

func TestConsentDefaultActionText(t *testing.T) {
	deny := consentDefaultActionText("deny")
	if deny != "Se você não responder, o acesso será NEGADO automaticamente." {
		t.Fatalf("texto deny inesperado: %q", deny)
	}
	approve := consentDefaultActionText("APPROVE")
	if approve != "Se você não responder, o acesso será PERMITIDO automaticamente." {
		t.Fatalf("texto approve inesperado: %q", approve)
	}
	// Valor vazio/desconhecido é fail closed: nega.
	if got := consentDefaultActionVerb(""); got != "NEGADO" {
		t.Fatalf("ação padrão vazia = %q, want NEGADO", got)
	}
}
