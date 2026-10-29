//go:build windows

package thirdparty

// Validação em máquina REAL da verificação Authenticode (WU-21/D24).
// Roda com DISCOVERY_WUA_SMOKE=1 (mesma flag do smoke de WUA).
//
// Uso: $env:DISCOVERY_WUA_SMOKE='1'; go test ./app/core/thirdparty/ -run TestAuthenticodeSmoke -v

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthenticodeSmokeSignedSystemBinary(t *testing.T) {
	if strings.TrimSpace(os.Getenv("DISCOVERY_WUA_SMOKE")) != "1" {
		t.Skip("defina DISCOVERY_WUA_SMOKE=1 para validar assinatura em maquina real")
	}

	windir := os.Getenv("WINDIR")
	if windir == "" {
		windir = `C:Windows`
	}
	signed := filepath.Join(windir, "System32", "notepad.exe")
	if _, err := os.Stat(signed); err != nil {
		t.Skipf("binario assinado do sistema ausente: %v", err)
	}

	if err := verifyAuthenticode(signed); err != nil {
		t.Fatalf("binario assinado pelo Windows deveria passar: %v", err)
	}
	t.Log("notepad.exe (assinado pela Microsoft): assinatura valida")
}

func TestAuthenticodeSmokeUnsignedFile(t *testing.T) {
	if strings.TrimSpace(os.Getenv("DISCOVERY_WUA_SMOKE")) != "1" {
		t.Skip("defina DISCOVERY_WUA_SMOKE=1 para validar assinatura em maquina real")
	}

	dir := t.TempDir()
	unsigned := filepath.Join(dir, "instalador.bin")
	if err := os.WriteFile(unsigned, []byte("conteudo sem assinatura"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := verifyAuthenticode(unsigned)
	if err == nil {
		t.Fatal("arquivo sem assinatura NAO pode passar")
	}
	t.Logf("arquivo sem assinatura recusado: %v", err)
}
