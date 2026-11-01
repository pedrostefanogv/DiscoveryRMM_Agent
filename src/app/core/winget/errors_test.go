package winget

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestFriendlyFailureKnownCodes(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		wantCode string
		wantMsg  string
	}{
		{
			name:     "hash mismatch via exit code",
			text:     "erro executando winget install --id google.chrome: exit status 0x8a150011",
			wantCode: "0x8a150011",
			wantMsg:  "Hash do instalador não confere",
		},
		{
			name:     "codigo em caixa alta",
			text:     "exit status 0X8A150011",
			wantCode: "0x8a150011",
			wantMsg:  "Hash do instalador não confere",
		},
		{
			name:     "sem upgrade aplicavel",
			text:     "exit status 0x8a15002b",
			wantCode: "0x8a15002b",
			wantMsg:  "Nenhuma atualização aplicável",
		},
		{
			name:     "hash mismatch via marcador de output sem codigo",
			text:     "Installer hash does not match; this cannot be overridden when running as admin",
			wantCode: "",
			wantMsg:  "Hash do instalador não confere",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg, ok := FriendlyFailure(tc.text)
			if !ok {
				t.Fatalf("esperava reconhecimento, ok=false")
			}
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
			if !strings.Contains(msg, tc.wantMsg) {
				t.Fatalf("msg = %q, want contains %q", msg, tc.wantMsg)
			}
		})
	}
}

func TestFriendlyFailureCombinesErrorAndOutput(t *testing.T) {
	err := fmt.Errorf("erro executando winget install: %w", errors.New("exit status 0x8a150011"))
	code, msg, ok := FriendlyFailure(err.Error(), "Downloading https://dl.google.com/...\nInstaller hash does not match")
	if !ok || code != "0x8a150011" || !strings.Contains(msg, "Hash do instalador") {
		t.Fatalf("got code=%q msg=%q ok=%v", code, msg, ok)
	}
}

func TestFriendlyFailureUnknown(t *testing.T) {
	if _, _, ok := FriendlyFailure("exit status 1"); ok {
		t.Fatal("exit status 1 genérico não deveria ser reconhecido")
	}
	if _, msg, ok := FriendlyFailure("exit status 0x8a150099"); ok && msg != "" {
		// código do espaço winget não mapeado: pode devolver ok com código, mas
		// nunca uma mensagem vazia sinalizada como válida
		t.Fatal("código desconhecido não deveria carregar mensagem")
	}
}
