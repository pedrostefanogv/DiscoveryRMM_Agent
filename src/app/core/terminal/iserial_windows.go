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

// dispatcherProbeTimeout é a janela de observação do shell do dispatcher logo
// após a conexão dos pipes. Precisa cobrir o boot do dispatcher E as suas
// tentativas internas de ConPTY (3 × spawn + sonda de 500ms ≈ 2s+ — o filho
// só sai quando TODAS falham). Se o dispatcher sair dentro desta janela
// (ConPTY derrubado por AV no filho), a cadeia cai para ConPTY in-process →
// legacy em vez de entregar um shell morto e mudo. No caso saudável a sonda
// sai CEDO (primeiro output), então uma janela maior não custa latência.
const dispatcherProbeTimeout = 4 * time.Second

// NewShellInteractive cria um shell do melhor backend disponível:
// ConPTY primeiro; se o processo morre prematuramente (com 0xC0000142 /
// STATUS_DLL_INIT_FAILED) no boot de DLL, tenta novamente (até conptyMaxRetries)
// e só então faz fallback para o console real (pipes + CREATE_NEW_CONSOLE), que
// é mais resistente a injetores/AV (como o terminal legado do MeshCentral).
func NewShellInteractive(shell ShellKind, cols, rows int, onOutput func(string)) (IShell, error) {
	// 0ª tentativa: dispatcher (ConPTY num processo filho isolado) quando
	// habilitado via DISCOVERY_TERM_DISPATCHER=1. Isola o ConPTY do processo
	// GUI do agente, reduzindo o 0xC0000142. Se falhar ou não habilitado,
	// segue para ConPTY in-process → legacy.
	if DispatchersAvailable() {
		ds, err := NewDispatcherShell(shell, cols, rows, onOutput)
		if err == nil {
			// Sonda de morte prematura: o NewDispatcherShell devolve sucesso
			// assim que os named pipes conectam — o ConPTY é criado DEPOIS,
			// dentro do filho. Se o AV derrubar o ConPTY no boot de DLL
			// (0xC0000142), o dispatcher sai em ~instantes e o shell fica
			// MUDO: sessão criada, term.ready publicado, zero output — o
			// "terminal não funciona" com banner de compatibilidade que
			// ninguém entendia. Sem esta sonda NÃO havia retry/fallback
			// depois do dispatcher (a escada só cobria o ConPTY in-process).
			if dead := probeForEarlyDeath(ds, dispatcherProbeTimeout); dead {
				log.Printf("[terminal] dispatcher morreu prematuramente (ConPTY isolado derrubado?); tentando ConPTY in-process")
				_ = ds.Close()
			} else {
				log.Printf("[terminal] usando dispatcher (ConPTY remoto isolado)")
				return ds, nil
			}
		} else {
			log.Printf("[terminal] dispatcher indisponível (%v); usando ConPTY in-process ou legacy", err)
		}
	} else {
		log.Printf("[terminal] dispatcher desabilitado por configuração; usando ConPTY in-process ou legacy")
	}

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
	if !IsConPTYAvailable() {
		log.Printf("[terminal] ConPTY indisponível neste sistema (requer Windows 10 1809+); usando console real (legacy)")
	}
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
	if _, ok := s.(*dispatcherShell); ok {
		// O dispatcher executa ConPTY num processo filho isolado: é ConPTY de
		// verdade (setas/história/TAB funcionam). Reportar "legacy" aqui —
		// comportamento antigo — fazia o viewer exibir o banner falso
		// "Modo compatibilidade (ConPTY indisponível)" e aplicar mitigações
		// de input de pipe (\x7f→\x08, interceptação de clear/cls) sobre
		// uma sessão ConPTY sã.
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
	// Banner sintético no stream: no legacy o stdout do shell é um PIPE e o
	// cmd/powershell NÃO imprime banner/prompt por ele (o banner vai para o
	// console real oculto) — provado em 17/09: terminal ConPTY morto por
	// 0xC0000142 (Kaspersky) → legacy → viewer com tela VAZIA, parecendo
	// terminal morto mesmo com o shell vivo. Com o aviso, o usuário sabe que
	// o terminal está vivo: digite o comando e Enter (setas/TAB não funcionam
	// neste backend — stdin é pipe, sem processamento VT).
	onOutput("\r\n\x1b[33m" + key + " iniciado em modo compatibilidade (sem ConPTY neste agente).\r\nEdição local ativa: ↑/↓ histórico, Backspace/Home/End/Delete, clear/cls e Ctrl+L.\r\nTAB e Ctrl+C (interromper execução) não funcionam neste modo.\x1b[0m\r\n\r\n")
	return s, nil
}

// Compila: garante que o pointer do Shell (legado) implementa IShell.
var _ IShell = (*Shell)(nil)
