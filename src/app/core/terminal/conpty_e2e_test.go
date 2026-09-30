//go:build windows

package terminal

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// TestStartupInfoExLayout valida o espelho de STARTUPINFOEXW: tamanho total e
// os deslocamentos dos campos que o CreateProcessW lê (dwFlags e
// lpAttributeList). Um erro aqui quebra o ConPTY silenciosamente.
func TestStartupInfoExLayout(t *testing.T) {
	if got := unsafe.Sizeof(startupInfoEx{}); got != 112 {
		t.Fatalf("sizeof(startupInfoEx) = %d, want 112 (STARTUPINFOW 104 + lpAttributeList)", got)
	}
	if startupInfoExSize != 112 {
		t.Fatalf("startupInfoExSize = %d, want 112", startupInfoExSize)
	}
	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"cb", unsafe.Offsetof(startupInfoEx{}.cb), 0},
		{"dwFlags", unsafe.Offsetof(startupInfoEx{}.dwFlags), 60},
		{"wShowWindow", unsafe.Offsetof(startupInfoEx{}.wShowWindow), 64},
		{"lpReserved2", unsafe.Offsetof(startupInfoEx{}.lpReserved2), 72},
		{"hStdInput", unsafe.Offsetof(startupInfoEx{}.hStdInput), 80},
		{"hStdOutput", unsafe.Offsetof(startupInfoEx{}.hStdOutput), 88},
		{"hStdError", unsafe.Offsetof(startupInfoEx{}.hStdError), 96},
		{"lpAttributeList", unsafe.Offsetof(startupInfoEx{}.lpAttributeList), 104},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Fatalf("offset de %s = %d, want %d", c.name, c.got, c.want)
		}
	}
	// STARTF_USESTDHANDLES = 0x100 (STARTF_USESHOWWINDOW = 0x1 é o erro fácil).
	if startupInfoFlagUseStdHandles != 0x00000100 {
		t.Fatalf("STARTF_USESTDHANDLES = %#x, want 0x100", startupInfoFlagUseStdHandles)
	}
}

// TestConPTYEndToEnd é a regressão do bug que fazia o terminal cair para o modo
// compatibilidade: sem STARTF_USESTDHANDLES no STARTUPINFOEXW o filho NÃO é
// anexado ao pseudoconsole (roda no console do pai, morre com 0xC0000142 e nada
// chega ao viewer). Com o flag, a saída do shell volta pelo pseudoconsole.
//
// Escapes: DISCOVERY_TERM_SKIP_CONPTY_TEST=1 (máquina sem suporte a ConPTY).
func TestConPTYEndToEnd(t *testing.T) {
	if os.Getenv("DISCOVERY_TERM_SKIP_CONPTY_TEST") == "1" {
		t.Skip("DISCOVERY_TERM_SKIP_CONPTY_TEST=1")
	}
	if !IsConPTYAvailable() {
		t.Skip("ConPTY indisponivel nesta maquina (requer Windows 10 1809+)")
	}

	var mu sync.Mutex
	var out strings.Builder
	cb := func(chunk string) {
		mu.Lock()
		out.WriteString(chunk)
		mu.Unlock()
	}

	// A morte prematura do ConPTY ainda é intermitente (corrida de injeção de
	// DLL no boot), então a escada in-process é repetida algumas vezes — com o
	// bug do STARTF_USESTDHANDLES TODAS as tentativas falhavam e o teste ainda
	// pega a regressão. Usamos tryConPTY direto para não pagar a espera de 5 s
	// do dispatcher (que não existe num binário de teste).
	var s IShell
	var lastErr error
	start := time.Now()
	for round := 0; round < 3 && s == nil; round++ {
		s, lastErr = tryConPTY(ShellCmd, 80, 25, cb)
		if lastErr != nil {
			t.Logf("rodada %d nao subiu: %v", round+1, lastErr)
		}
	}
	if s == nil {
		t.Fatalf("ConPTY nao subiu em 3 rodadas: %v (suspeita: STARTF_USESTDHANDLES removido do startupInfoEx)", lastErr)
	}
	defer s.Close()
	backend := ShellBackendName(s)
	t.Logf("backend=%s aceito em %v", backend, time.Since(start))
	if backend != "conpty" {
		t.Fatalf("backend = %s, esperado conpty", backend)
	}

	marker := "CONPTY_E2E_MARKER"
	if err := s.WriteStdin("echo " + marker + "\r\n"); err != nil {
		t.Fatalf("WriteStdin: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := strings.Contains(out.String(), marker)
		mu.Unlock()
		if found {
			t.Logf("saida do shell voltou pelo pseudoconsole em %v", time.Since(start))
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	got := out.String()
	mu.Unlock()
	t.Fatalf("sem eco do marcador (backend=%s, %d bytes). Suspeita: STARTF_USESTDHANDLES removido do startupInfoEx. Saida: %q",
		backend, len(got), got)
}
