//go:build windows

package terminal

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// conptyProbeTimeout é o tempo de observação do ConPTY logo após o spawn. Se o
// processo filho morre dentro dessa janela (sintoma de 0xC0000142 /
// STATUS_DLL_INIT_FAILED), considera-se ConPTY instável e faz-se fallback.
const conptyProbeTimeout = 500 * time.Millisecond

// conptyMaxRetries define quantas tentativas extras de ConPTY são feitas após
// uma morte prematura antes de desistir e cair para o console real (legacy).
// Como o 0xC0000142 é intermitente (injeção de DLL de AV/antivírus), uma
// segunda tentativa costuma estabilizar — preservando TUI/ANSI completos.
const conptyMaxRetries = 2

// NewShellInteractive cria um shell do melhor backend disponível:
// ConPTY primeiro; se o processo morre prematuramente (com 0xC0000142 /
// STATUS_DLL_INIT_FAILED) no boot de DLL, tenta novamente (até conptyMaxRetries)
// e só então faz fallback para o console real (pipes + CREATE_NEW_CONSOLE), que
// é mais resistente a injetores/AV (como o terminal legado do MeshCentral).
func NewShellInteractive(shell ShellKind, cols, rows int, onOutput func(string)) (IShell, error) {
	// 1ª tentativa: ConPTY (contribui com TUI/ANSI quando estável).
	if IsConPTYAvailable() {
		var lastErr error
		for attempt := 0; attempt <= conptyMaxRetries; attempt++ {
			s, err := NewConPTYShell(shell, cols, rows, onOutput)
			if err != nil {
				lastErr = err
				log.Printf("[terminal] ConPTY falhou no spawn (tentativa %d/%d): %v", attempt+1, conptyMaxRetries+1, err)
				continue
			}
			// O 0xC0000142 termina em milissegundos. Observamos por uma janela
			// curta usando Alive() (não-bloqueante, não consome Wait): se o
			// processo segui vivo, aceitamos ConPTY; se morreu prematuramente,
			// tentamos novamente antes de partir para o console real oculto.
			if dead := probeForEarlyDeath(s, conptyProbeTimeout); dead {
				lastErr = fmt.Errorf("ConPTY morreu prematuramente no startup")
				log.Printf("[terminal] ConPTY morreu prematuramente (tentativa %d/%d); %s",
					attempt+1, conptyMaxRetries+1, retryLabel(attempt))
				_ = s.Close()
				continue
			}
			return s, nil
		}
		log.Printf("[terminal] ConPTY instável após %d tentativas (último: %v); usando console real", conptyMaxRetries+1, lastErr)
	}

	// 2ª: ConPTY indisponível ou falhou no spawn — usa console real.
	return NewLegacyShell(shell, cols, rows, onOutput)
}

// retryLabel devolve o texto de reação para o log conforme a tentativa restante.
func retryLabel(attempt int) string {
	if attempt < conptyMaxRetries {
		return "tentando novamente"
	}
	return "usando console real"
}

// probeForEarlyDeath observa o shell por um curto intervalo de startup,
// verificando periodicamente Alive() (que NÃO consome o Wait() da sessão).
// Retorna true se o processo morreu dentro da janela (morte prematura).
func probeForEarlyDeath(s IShell, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !s.Alive() {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return !s.Alive()
}

// ShellBackendName retorna o nome legível do backend de shell em uso, para
// diagnóstico (ex.: "conpty" ou "legacy").
func ShellBackendName(s IShell) string {
	if s == nil {
		return "none"
	}
	if _, ok := s.(*ConPTYShell); ok {
		return "conpty"
	}
	return "legacy"
}

// NewLegacyShell cria um shell via console real (pipes + CREATE_NEW_CONSOLE),
// o mais tolerante a injetores/AV.
func NewLegacyShell(shell ShellKind, cols, rows int, onOutput func(string)) (IShell, error) {
	key := string(shell)
	if strings.HasPrefix(key, "wsl") {
		key = "wsl"
	}
	s, err := NewShell(key, onOutput)
	if err != nil {
		return nil, err
	}
	_ = s.Resize(cols, rows)
	return s, nil
}

// Compila: garante que o pointer do Shell (legado) implementa IShell.
var _ IShell = (*Shell)(nil)