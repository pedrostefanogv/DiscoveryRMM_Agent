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
	// BackendConsoleBuffer (Fase 5 — spike, estilo MeshCentral): screen buffer
	// real com AllocConsole + ReadConsoleOutput/WriteConsoleInput. Opt-in via
	// DISCOVERY_TERM_BACKEND=consolebuffer; NUNCA no modo auto.
	BackendConsoleBuffer
)

// ResolveBackendPolicy lê DISCOVERY_TERM_BACKEND (auto|conpty|legacy).
func ResolveBackendPolicy() TerminalBackendPolicy {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DISCOVERY_TERM_BACKEND"))) {
	case "legacy", "console", "pipe":
		return BackendLegacy
	case "conpty", "strict":
		return BackendConPTY
	case "consolebuffer", "screenbuffer", "meshcentral":
		return BackendConsoleBuffer
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
	// Teto CURTO (3 tentativas in-process) — 2026-10-27: a escada antiga (9
	// tentativas + backoff até 1 s) custava ~5,5 s em máquinas onde o AV mata
	// TODO filho do ConPTY (0xC0000142). 3 tentativas preservam a recuperação
	// dos casos intermitentes (tentativa 2/3) sem pagar segundos por abertura.
	conptyMaxRetries   = 2
	conptyRetryBackoff = 200 * time.Millisecond

	// dispatcherInnerRetries é o retry DENTRO do processo dispatcher. O pai já
	// repete a escada inteira; manter este número baixo evita que um dispatcher
	// preso segure a sessão por muito tempo. 2026-10-27: 1 (2 tentativas).
	dispatcherInnerRetries = 1

	// dispatcherProbeTimeout cobre o boot do dispatcher E as tentativas
	// internas dele (5 × spawn + sonda). O caminho feliz sai em ~500 ms.
	dispatcherProbeTimeout = 5 * time.Second
)

// NewShellInteractive cria um shell do melhor backend disponível, com o ConPTY
// como caminho padrão:
//
//  1. ConPTY in-process (menor latência) — com retry/backoff;
//  2. dispatcher (ConPTY isolado em processo filho) — mesma tecnologia;
//  3. console real (legacy) APENAS se o ConPTY não existir na máquina ou se
//     DISCOVERY_TERM_ALLOW_LEGACY=1. Caso contrário devolve erro com a causa,
//     que o viewer mostra ao operador (em vez de um shell de pipe que "abre"
//     mas não executa nada).
func NewShellInteractive(shell ShellKind, cols, rows int, onOutput func(string)) (IShell, error) {
	s, _, err := NewShellInteractiveRunAs(shell, cols, rows, onOutput, RunAsInherit)
	return s, err
}

// NewShellInteractiveRunAs cria o shell com a identidade pedida e devolve
// (shell, runAs efetivo, erro). O efetivo vai no term.ready ("user"/"system"/
// "inherit"): quando runAs=user não há usuário logado, o shell degrada para a
// identidade herdada com aviso no log (o terminal nunca deixa de abrir).
func NewShellInteractiveRunAs(shell ShellKind, cols, rows int, onOutput func(string), runAs RunAs) (IShell, string, error) {
	policy := ResolveBackendPolicy()
	if policy == BackendLegacy {
		log.Printf("[terminal] backend=legacy forçado por DISCOVERY_TERM_BACKEND")
		s, err := NewLegacyShellRunAs(shell, cols, rows, onOutput, runAs)
		return s, EffectiveRunAs(runAs), err
	}

	// Fase 5 (spike): console-buffer estilo MeshCentral. Opt-in EXPLICITO —
	// sem escada, sem fallback: o erro sobe para o viewer diagnosticar.
	if policy == BackendConsoleBuffer {
		log.Printf("[terminal] backend=console-buffer (spike) forçado por DISCOVERY_TERM_BACKEND")
		s, err := NewConsoleBufferShellRunAs(shell, cols, rows, onOutput, runAs)
		return s, EffectiveRunAs(runAs), err
	}

	if !IsConPTYAvailable() {
		// Sem as APIs de pseudoconsole não existe alternativa: o console real
		// (pipes) é o único shell possível nesta máquina.
		log.Printf("[terminal] ConPTY indisponível neste sistema (requer Windows 10 1809+); usando console real (legacy)")
		s, err := NewLegacyShellRunAs(shell, cols, rows, onOutput, runAs)
		return s, EffectiveRunAs(runAs), err
	}

	// Hint de saúde vindo do serviço (payload do worker): a máquina acabou de
	// falhar a escada INTEIRA por injeção de DLL. Abrir legacy direto evita
	// repetir ~1,5 s de tentativas a cada abertura. Só vale no modo AUTO: o
	// upgrade forçado (DISCOVERY_TERM_BACKEND=conpty) precisa tentar de verdade.
	if policy == BackendAuto && ConptyBlocked() {
		log.Printf("[terminal] hint: ConPTY bloqueado até %s — iniciando legacy direto",
			ConptyBlockedUntil().UTC().Format(time.RFC3339))
		s, err := NewLegacyShellRunAs(shell, cols, rows, onOutput, runAs)
		return s, EffectiveRunAs(runAs), err
	}

	// Acumula TODAS as causas: se a escada inteira falhar, o operador precisa
	// ver as duas (antes a mensagem do dispatcher sobrescrevia a do in-process).
	var failures []string

	// 1) ConPTY in-process: caminho preferido.
	if s, err := tryConPTY(shell, cols, rows, onOutput, runAs); err != nil {
		failures = append(failures, "conpty: "+err.Error())
	} else {
		// O efetivo REAL vem do shell (tryConPTY pode ter degradado
		// run-as=user → herdado quando o host rejeitou o token) — reportar
		// "user" quando o shell roda como SYSTEM enganaria o operador.
		return s, shellEffectiveRunAs(s, runAs), nil
	}

	// 2) ConPTY isolado em processo filho (dispatcher).
	if DispatchersAvailable() {
		if ds, err := tryDispatcher(shell, cols, rows, onOutput, runAs); err != nil {
			failures = append(failures, "dispatcher: "+err.Error())
		} else {
			// O dispatcher resolve o run-as no processo filho; o pai não vê o
			// degradé interno — efetivo estimado pelo pedido (o dispatcher
			// loga o fallback real com [term-dispatcher]).
			return ds, EffectiveRunAs(runAs), nil
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
	injectionFailure := allConPTYInjectionFailures(failures)
	if legacyAllowExplicit() {
		log.Printf("[terminal] fallback para console real liberado por DISCOVERY_TERM_ALLOW_LEGACY=1: %s", detail)
		if injectionFailure {
			MarkConptyBlocked("0xC0000142")
		}
		s, err := NewLegacyShellRunAs(shell, cols, rows, onOutput, runAs)
		return s, EffectiveRunAs(runAs), err
	}

	// ── Fallback automático: console real quando o ConPTY morre por injeção ──
	// Padrão medido em campo (2026-10-26, Kaspersky klhk): TODAS as tentativas
	// ConPTY (in-process e dispatcher) morrem com 0xC0000142 no boot do filho,
	// enquanto o spawn legacy (CREATE_NEW_CONSOLE + pipes) sobrevive — a
	// injeção de DLL do AV mata especificamente os filhos do pseudoconsole.
	// Nesse cenário, um terminal degradado (sem VT: sem setas/TAB/histórico,
	// com edição local no viewer) é infinitamente melhor que um terminal
	// morto: degrada AUTOMATICAMENTE e o banner do legacy avisa o operador.
	// DISCOVERY_TERM_NO_AUTO_LEGACY=1 desliga este fallback.
	if injectionFailure && !envFlagOn("DISCOVERY_TERM_NO_AUTO_LEGACY") {
		log.Printf("[terminal] ConPTY morto por injecao de DLL (0xC0000142) em todas as tentativas — degradando automaticamente para o console real: %s", detail)
		// A escada INTEIRA (in-process + dispatcher) falhou por injeção: registra
		// o hint para a próxima abertura desta máquina ir direto ao legacy.
		MarkConptyBlocked("0xC0000142")
		s, err := NewLegacyShellRunAs(shell, cols, rows, onOutput, runAs)
		return s, EffectiveRunAs(runAs), err
	}

	// A dica da variável de ambiente fica no LOG (diagnóstico), não na mensagem
	// que chega ao operador no banner do terminal.
	log.Printf("[terminal] ConPTY indisponivel na pratica (%s); para forcar o console real (pipes, sem VT) use DISCOVERY_TERM_ALLOW_LEGACY=1", detail)
	return nil, EffectiveRunAs(runAs), fmt.Errorf("nao foi possivel iniciar o terminal ConPTY neste computador (%s)", detail)
}

// tryConPTY tenta o ConPTY in-process com retry/backoff. Com runAs=user, o
// token é resolvido UMA vez (cache) e reusado por todas as tentativas; falha
// recorrente do CreateProcessAsUserW (token rejeitado pelo host, ex.: política
// corporativa) degrada para a identidade herdada nas tentativas seguintes.
func tryConPTY(shell ShellKind, cols, rows int, onOutput func(string), runAs RunAs) (IShell, error) {
	var lastErr error
	effRunAs := runAs
	for attempt := 0; attempt <= conptyMaxRetries; attempt++ {
		firstOutput := &atomic.Bool{}
		s, err := NewConPTYShellRunAs(shell, cols, rows, func(out string) {
			firstOutput.Store(true)
			onOutput(out)
		}, effRunAs)
		if err != nil {
			lastErr = err
			if effRunAs == RunAsUser && strings.Contains(err.Error(), "CreateProcessAsUserW") {
				log.Printf("[terminal] spawn run-as=user rejeitado (%v); tentando novamente com identidade herdada", err)
				effRunAs = RunAsInherit
			}
			log.Printf("[terminal] ConPTY falhou no spawn (tentativa %d/%d): %v", attempt+1, conptyMaxRetries+1, err)
		} else if probeForEarlyDeath(s, conptyProbeTimeout, firstOutput) {
			lastErr = fmt.Errorf("ConPTY morreu prematuramente no startup")
			// Exit code do filho: 0xC0000142 (DLL injection de AV) vs outros
			// códigos — sem isso o diagnóstico fica cego (o código de saída do
			// Wait() só chega depois, se chegar).
			if code, ok := processExitCode(s); ok {
				log.Printf("[terminal] ConPTY morreu prematuramente (tentativa %d/%d) exitCode=0x%08X; %s",
					attempt+1, conptyMaxRetries+1, uint32(code), retryLabel(attempt))
			} else {
				log.Printf("[terminal] ConPTY morreu prematuramente (tentativa %d/%d); %s",
					attempt+1, conptyMaxRetries+1, retryLabel(attempt))
			}
			_ = s.Close()
		} else {
			MarkConptyHealthy()
			return s, nil
		}
		if attempt < conptyMaxRetries {
			// Backoff progressivo (150ms → até 1s): martelar spawns com 150ms
			// fixos aciona regras comportamentais de AV/EDR (diagnóstico
			// 2026-10-26: 13 spawns em ~3s → 0xC0000142 em 100% das tentativas;
			// medido fora do agente: taxa de sobrevivência cai a ~40% sob
			// rajada). Espaçar as tentativas dá tempo ao injetor de DLL
			// terminar e reduz a taxa de bloqueio.
			backoff := conptyRetryBackoff * time.Duration(attempt+1)
			if backoff > time.Second {
				backoff = time.Second
			}
			time.Sleep(backoff)
		}
	}
	return nil, lastErr
}

// tryDispatcher tenta o ConPTY isolado no processo filho (dispatcher). O
// run-as é propagado por flag (--terminal-runas) — o dispatcher (filho do
// agente, ainda SYSTEM quando o serviço é) resolve o token localmente.
//
// INCIDENTE 2026-10-27: o pai aceitava o dispatcher apenas pelo PROCESSO
// dispatcher estar vivo (janela estável de 500ms) — mas o ConPTY INTERNO do
// dispatcher podia ainda estar morrendo (0xC0000142). Resultado: "console
// pronto" publicado com shell morto, viewer em tela branca e "shell saiu"
// ~1s depois. O acceptance agora exige SAÍDA REAL do shell (o dispatcher só
// escreve no pipe quando um ConPTY vivo começa a emitir).
func tryDispatcher(shell ShellKind, cols, rows int, onOutput func(string), runAs RunAs) (IShell, error) {
	firstOutput := &atomic.Bool{}
	ds, err := NewDispatcherShell(shell, cols, rows, func(out string) {
		firstOutput.Store(true)
		onOutput(out)
	}, runAs)
	if err != nil {
		log.Printf("[terminal] dispatcher indisponivel (%v)", err)
		return nil, err
	}
	deadline := time.Now().Add(dispatcherProbeTimeout)
	for time.Now().Before(deadline) {
		if !ds.Alive() {
			log.Printf("[terminal] dispatcher morreu prematuramente (ConPTY isolado nao subiu)")
			_ = ds.Close()
			return nil, fmt.Errorf("dispatcher morreu prematuramente")
		}
		if firstOutput.Load() {
			break // shell vivo e produzindo saída — acceptance seguro
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !firstOutput.Load() {
		log.Printf("[terminal] dispatcher não reportou shell vivo (sem saída em %v)", dispatcherProbeTimeout)
		_ = ds.Close()
		return nil, fmt.Errorf("dispatcher não reportou shell vivo")
	}
	MarkConptyHealthy()
	log.Printf("[terminal] usando dispatcher (ConPTY remoto isolado)")
	return ds, nil
}

// allConPTYInjectionFailures reporta se TODAS as causas acumuladas na escada
// indicam morte prematura por injeção de DLL (0xC0000142) — o padrão do
// bloqueio de AV/EDR (ex.: Kaspersky klhk) sobre os filhos do ConPTY.
func allConPTYInjectionFailures(failures []string) bool {
	if len(failures) == 0 {
		return false
	}
	for _, f := range failures {
		// Padrões de morte por bloqueio de AV/EDR:
		//   - "morreu prematuramente": filho do ConPTY morto (0xC0000142) ou
		//     o processo dispatcher morreu antes da sonda passar;
		//   - "não conectou aos pipes": o dispatcher morreu/foi morto durante
		//     as tentativas internas, sem terminar o Accept dos pipes
		//     (validado em campo 2026-10-26 — mesmo bloqueio).
		if !strings.Contains(f, "morreu prematuramente") &&
			!strings.Contains(f, "não conectou aos pipes") {
			return false
		}
	}
	return true
}

// envFlagOn lê um flag booleano de ambiente (1/true/yes/on).
func envFlagOn(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// processExitCode devolve o exit code corrente do processo do shell
// (OpenProcess + GetExitCodeProcess), sem consumir o Wait(). ok=false quando
// o processo já foi reaped ou o handle não está disponível.
func processExitCode(s IShell) (int, bool) {
	// ConPTYShell/dispatcherShell implementam ExitCodeNow (OpenProcess +
	// GetExitCodeProcess). O legacy não — devolve ok=false (log cai no
	// formato sem exit code).
	if pp, ok := s.(interface{ ExitCodeNow() (int, bool) }); ok {
		return pp.ExitCodeNow()
	}
	return 0, false
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

// shellEffectiveRunAs devolve a identidade EFETIVA do shell criado: quando o
// shell é um ConPTYShell ele carrega o run-as real usado no spawn (o tryConPTY
// pode ter degradado o pedido); senão, a estimativa pelo pedido.
func shellEffectiveRunAs(s IShell, requested RunAs) string {
	if c, ok := s.(*ConPTYShell); ok && c.runAs != "" {
		return string(c.runAs)
	}
	return EffectiveRunAs(requested)
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
	if _, ok := s.(*ConsoleBufferShell); ok {
		// Fase 5 (spike): screen buffer real do console do worker.
		return "consolebuffer"
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
	return NewLegacyShellRunAs(shell, cols, rows, onOutput, RunAsInherit)
}

// NewLegacyShellRunAs é a variante run-as do legacy: com token do usuário, o
// processo é criado via CreateProcessAsUser (SysProcAttr.Token do Go).
func NewLegacyShellRunAs(shell ShellKind, cols, rows int, onOutput func(string), runAs RunAs) (IShell, error) {
	key := string(shell)
	if strings.HasPrefix(key, "wsl") {
		key = "wsl"
	}
	ra := resolveRunAs(runAs)
	if ra.tok != 0 {
		stdLog("[terminal] legacy shell spawnado como %s (requested=%s)", ra.identity, string(runAs))
	}
	s, err := NewShellWithToken(key, onOutput, ra.tok)
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
