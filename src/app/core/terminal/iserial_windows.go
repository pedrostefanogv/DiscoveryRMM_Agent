//go:build windows

package terminal

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// ── Política de backend ──
//
// O console "legacy" (console real + pipes) NÃO é um terminal de verdade: o
// stdin é uma PIPE, então o filho não processa VT — setas/histórico/TAB/Ctrl+C
// não funcionam, o prompt apenas se repete e nada executa (sintoma relatado).
// Por isso o ConPTY é o caminho PREFERIDO e o legacy só é aceito quando o
// ConPTY não existe na máquina ou quando explicitamente liberado.

type TerminalBackendPolicy int

const (
	// BackendAuto (padrão): ConPTY primeiro; o console real só é usado quando
	// o ConPTY não existe (Windows < 10 1809) ou com DISCOVERY_TERM_ALLOW_LEGACY=1.
	BackendAuto TerminalBackendPolicy = iota
	// BackendConPTY: exige ConPTY — se todas as tentativas falharem devolve
	// erro com a causa, em vez de entregar um shell de pipe inutilizável.
	BackendConPTY
	// BackendLegacy: força o console real (pipes). Diagnóstico/compatibilidade.
	BackendLegacy
)

// ResolveBackendPolicy lê DISCOVERY_TERM_BACKEND (auto|conpty|legacy).
func ResolveBackendPolicy() TerminalBackendPolicy {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DISCOVERY_TERM_BACKEND"))) {
	case "legacy", "console", "pipe":
		return BackendLegacy
	case "conpty", "strict":
		return BackendConPTY
	default:
		return BackendAuto
	}
}

// legacyAllowExplicit libera o fallback para o console real MESMO quando o
// ConPTY existe (o antigo downgrade silencioso).
// Opt-in: DISCOVERY_TERM_ALLOW_LEGACY=1.
func legacyAllowExplicit() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DISCOVERY_TERM_ALLOW_LEGACY"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ── Timings do startup ──

const (
	// conptyStableWindow é a janela de risco do 0xC0000142
	// (STATUS_DLL_INIT_FAILED): o DllMain de uma DLL injetada falha em ~20–60 ms.
	// Sobreviver a esta janela — ou produzir saída — já caracteriza ConPTY
	// saudável. ANTES a sonda só olhava Alive() e queimava o timeout INTEIRO no
	// caminho feliz (4 s no dispatcher), atrasando o terminal sem motivo.
	conptyStableWindow = 500 * time.Millisecond

	// conptyProbeTimeout é o teto da sonda do ConPTY in-process (morte
	// prematura é detectada em ~40 ms; o caminho feliz sai em ~500 ms).
	conptyProbeTimeout = 1200 * time.Millisecond

	// conptyMaxRetries: a morte prematura do ConPTY (0xC0000142) ainda ocorre de
	// forma intermitente mesmo com o spawn correto (corrida de injeção de DLL de
	// AV/EDR no boot). Cada tentativa falha custa ~40 ms + backoff, então um
	// número alto de tentativas é barato e recupera praticamente todos os casos
	// (medido na estação: tentativa 1 falhou, tentativa 2 subiu).
	conptyMaxRetries   = 8
	conptyRetryBackoff = 150 * time.Millisecond

	// dispatcherInnerRetries é o retry DENTRO do processo dispatcher. O pai já
	// repete a escada inteira; manter este número baixo evita que um dispatcher
	// preso segure a sessão por muito tempo.
	dispatcherInnerRetries = 3

	// dispatcherProbeTimeout cobre o boot do dispatcher E as tentativas
	// internas dele (5 × spawn + sonda). O caminho feliz sai em ~500 ms.
	dispatcherProbeTimeout = 5 * time.Second
)

// NewShellInteractive cria um shell do melhor backend disponível, com o ConPTY
// como caminho padrão:
//
//	1. ConPTY in-process (menor latência) — com retry/backoff;
//	2. dispatcher (ConPTY isolado em processo filho) — mesma tecnologia;
//	3. console real (legacy) APENAS se o ConPTY não existir na máquina ou se
//	   DISCOVERY_TERM_ALLOW_LEGACY=1. Caso contrário devolve erro com a causa,
//	   que o viewer mostra ao operador (em vez de um shell de pipe que "abre"
//	   mas não executa nada).
func NewShellInteractive(shell ShellKind, cols, rows int, onOutput func(string)) (IShell, error) {
	policy := ResolveBackendPolicy()
	if policy == BackendLegacy {
		log.Printf("[terminal] backend=legacy forçado por DISCOVERY_TERM_BACKEND")
		return NewLegacyShell(shell, cols, rows, onOutput)
	}

	if !IsConPTYAvailable() {
		// Sem as APIs de pseudoconsole não existe alternativa: o console real
		// (pipes) é o único shell possível nesta máquina.
		log.Printf("[terminal] ConPTY indisponível neste sistema (requer Windows 10 1809+); usando console real (legacy)")
		return NewLegacyShell(shell, cols, rows, onOutput)
	}

	// Acumula TODAS as causas: se a escada inteira falhar, o operador precisa
	// ver as duas (antes a mensagem do dispatcher sobrescrevia a do in-process).
	var failures []string

	// 1) ConPTY in-process: caminho preferido.
	if s, err := tryConPTY(shell, cols, rows, onOutput); err != nil {
		failures = append(failures, "conpty: "+err.Error())
	} else {
		return s, nil
	}

	// 2) ConPTY isolado em processo filho (dispatcher).
	if DispatchersAvailable() {
		if ds, err := tryDispatcher(shell, cols, rows, onOutput); err != nil {
			failures = append(failures, "dispatcher: "+err.Error())
		} else {
			return ds, nil
		}
	} else {
		log.Printf("[terminal] dispatcher desabilitado (DISCOVERY_TERM_DISPATCHER=0)")
	}

	// 3) ConPTY existe mas não subiu. Entregar o shell de pipe produziria o
	// terminal "quebrado" (prompt repetindo, nada executa) — melhor falhar com
	// causa explícita, que o viewer mostra no banner de erro.
	detail := strings.Join(failures, "; ")
	if detail == "" {
		detail = "nenhum backend ConPTY disponivel"
	}
	if legacyAllowExplicit() {
		log.Printf("[terminal] fallback para console real liberado por DISCOVERY_TERM_ALLOW_LEGACY=1: %s", detail)
		return NewLegacyShell(shell, cols, rows, onOutput)
	}
	// A dica da variável de ambiente fica no LOG (diagnóstico), não na mensagem
	// que chega ao operador no banner do terminal.
	log.Printf("[terminal] ConPTY indisponivel na pratica (%s); para forcar o console real (pipes, sem VT) use DISCOVERY_TERM_ALLOW_LEGACY=1", detail)
	return nil, fmt.Errorf("nao foi possivel iniciar o terminal ConPTY neste computador (%s)", detail)
}

// tryConPTY tenta o ConPTY in-process com retry/backoff.
func tryConPTY(shell ShellKind, cols, rows int, onOutput func(string)) (IShell, error) {
	var lastErr error
	for attempt := 0; attempt <= conptyMaxRetries; attempt++ {
		firstOutput := &atomic.Bool{}
		s, err := NewConPTYShell(shell, cols, rows, func(out string) {
			firstOutput.Store(true)
			onOutput(out)
		})
		if err != nil {
			lastErr = err
			log.Printf("[terminal] ConPTY falhou no spawn (tentativa %d/%d): %v", attempt+1, conptyMaxRetries+1, err)
		} else if probeForEarlyDeath(s, conptyProbeTimeout, firstOutput) {
			lastErr = fmt.Errorf("ConPTY morreu prematuramente no startup")
			log.Printf("[terminal] ConPTY morreu prematuramente (tentativa %d/%d); %s",
				attempt+1, conptyMaxRetries+1, retryLabel(attempt))
			_ = s.Close()
		} else {
			return s, nil
		}
		if attempt < conptyMaxRetries {
			time.Sleep(conptyRetryBackoff)
		}
	}
	return nil, lastErr
}

// tryDispatcher tenta o ConPTY isolado no processo filho (dispatcher).
func tryDispatcher(shell ShellKind, cols, rows int, onOutput func(string)) (IShell, error) {
	firstOutput := &atomic.Bool{}
	ds, err := NewDispatcherShell(shell, cols, rows, func(out string) {
		firstOutput.Store(true)
		onOutput(out)
	})
	if err != nil {
		log.Printf("[terminal] dispatcher indisponivel (%v)", err)
		return nil, err
	}
	if probeForEarlyDeath(ds, dispatcherProbeTimeout, firstOutput) {
		log.Printf("[terminal] dispatcher morreu prematuramente (ConPTY isolado nao subiu)")
		_ = ds.Close()
		return nil, fmt.Errorf("dispatcher morreu prematuramente")
	}
	log.Printf("[terminal] usando dispatcher (ConPTY remoto isolado)")
	return ds, nil
}

// probeForEarlyDeath observa o shell no startup e devolve true se ele morreu.
// Sai CEDO em dois casos: (a) o processo morreu (0xC0000142 aparece em ~20–60 ms)
// ou (b) o shell já produziu saída. Sem (b), o caminho feliz esperava o timeout
// inteiro — 4 s no dispatcher. firstOutput pode ser nil.
func probeForEarlyDeath(s IShell, timeout time.Duration, firstOutput *atomic.Bool) bool {
	deadline := time.Now().Add(timeout)
	started := time.Now()
	for time.Now().Before(deadline) {
		if !s.Alive() {
			return true
		}
		if firstOutput != nil && firstOutput.Load() {
			return false
		}
		if time.Since(started) >= conptyStableWindow {
			return false // sobreviveu à janela de risco do DllMain
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !s.Alive()
}

// retryLabel devolve o texto de reação para o log conforme a tentativa restante.
func retryLabel(attempt int) string {
	if attempt < conptyMaxRetries {
		return "tentando novamente"
	}
	return "esgotadas as tentativas"
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
		// de input de pipe sobre uma sessão ConPTY sã.
		return "conpty"
	}
	return "legacy"
}

// NewLegacyShell cria um shell via console real (pipes + CREATE_NEW_CONSOLE).
// É o último recurso: o stdin é um pipe, então setas/histórico/TAB/Ctrl+C NÃO
// funcionam e o prompt apenas se repete. Só é usado quando o ConPTY não existe
// na máquina ou com DISCOVERY_TERM_ALLOW_LEGACY=1.
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
	// console real oculto). Sem o aviso o viewer ficaria em tela vazia.
	onOutput("\r\n\x1b[33mconsole real (modo compatibilidade): ConPTY indisponivel nesta maquina ou liberado por configuracao.\r\n" +
		"Stdin em pipe: setas, TAB, Home/End/Delete e Ctrl+C NAO funcionam no shell.\r\n" +
		"Edicao local no viewer: historico, Backspace, Home/End/Delete, clear/cls e Ctrl+L.\x1b[0m\r\n\r\n")
	return s, nil
}

// Compila: garante que o pointer do Shell (legado) implementa IShell.
var _ IShell = (*Shell)(nil)
