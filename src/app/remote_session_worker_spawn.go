//go:build windows

package app

// Spawner de remote session worker (PLANO_AGENT_SERVICE_SYSTEM.md §7.2,
// base MeshAgent kvm_relay_restart / ILibProcessPipe_SpawnTypes).
//
// O serviço SYSTEM (sessão 0) não tem desktop: quando não há UI companion
// conectada, spawn este binário com --remote-session-worker:
//   1. Com usuário logado: WTSQueryUserToken(sessão do console) +
//      CreateProcessAsUser → worker roda na sessão interativa (captura e
//      input funcionam).
//   2. Sem usuário (tela de logon): token do processo winlogon da sessão do
//      console + CreateProcessAsUser em winsta0\winlogon → captura a tela de
//      logon (mesmo mecanismo do MeshAgent SpawnTypes_WINLOGON).
//
// O payload do comando vai via stdin framed (4B len + JSON) — nunca na linha
// de comando (visível em WMI/Task Manager). O worker publica frames no NATS
// diretamente; o serviço apenas monitora o processo (stop via stdin "stop").

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"discovery/app/core/platform"

	"golang.org/x/sys/windows"
)

// remoteSessionWorkerState guarda os workers ativos por sessionId.
var remoteSessionWorkers struct {
	mu   sync.Mutex
	byID map[string]*remoteSessionWorkerProc
}

// maxRemoteSessionRespawns limita quantas vezes o watchdog relança o worker de
// uma mesma sessão dentro do serviço (evita loop em caso de falha persistente).
const maxRemoteSessionRespawns = 3

// respawnBudgetStore conta, por sessão, quantos relançamentos o watchdog ainda
// pode fazer. Zerado quando chega um start explícito novo.
type respawnBudgetStore struct {
	mu   sync.Mutex
	byID map[string]int
}

// remoteSessionRespawnBudget é o orçamento global do processo de serviço.
var remoteSessionRespawnBudget respawnBudgetStore

// reset zera o orçamento de uma sessão (novo start explícito).
func (s *respawnBudgetStore) reset(id string, n int) {
	s.mu.Lock()
	s.byID[id] = n
	s.mu.Unlock()
}

// consume decrementa o orçamento; false se esgotado (e limpa a entrada para
// não acumular sessões encerradas no mapa).
func (s *respawnBudgetStore) consume(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.byID[id]
	if n <= 0 {
		delete(s.byID, id)
		return false
	}
	s.byID[id] = n - 1
	return true
}

func init() {
	remoteSessionWorkers.byID = make(map[string]*remoteSessionWorkerProc)
	remoteSessionRespawnBudget.byID = make(map[string]int)
}

// remoteSessionWorkerProc representa um worker spawnado para uma sessão.
type remoteSessionWorkerProc struct {
	sessionID string
	proc      *os.Process
	stdin     *os.File
	// payload/parent permitem relançar o worker se ele morrer com a sessão
	// ativa (watchdog de logoff/shutdown).
	payload map[string]any
	parent  context.Context
	// stopping marca encerramento explícito (stop/teardown) — o watchdog não
	// relança nesse caso.
	stopping atomic.Bool
	// stderrDrainDone é fechado quando a goroutine de dreno do stderr termina.
	stderrDrainDone chan struct{}
	// handshake é fechado quando o worker confirma no stderr que entrou no
	// MODO worker ("iniciando sessão <id>", impresso por RunRemoteSessionWorker
	// logo após ler o payload do stdin). Um processo que IGNORA a flag — ex.:
	// um binário de serviço sem o dispatch --remote-session-worker, que bootava
	// uma segunda instância completa do serviço (bug do acesso remoto de
	// 13/09/2026) — nunca imprime essa linha. O handshake troca a heurística
	// "processo vivo após 3s" (que mascarava o bug com exitCode=0 falso) por
	// confirmação real do modo worker.
	handshake chan struct{}
	// stderrMu/stderrLast guardam as últimas linhas do stderr do worker para
	// diagnóstico quando ele morre ou não faz handshake.
	stderrMu   sync.Mutex
	stderrLast []string
	done       chan struct{}
}

// spawnRemoteSessionWorker lança o worker na sessão interativa
// (winsta0\winlogon, seguindo depois o input desktop ativo) e escreve o
// payload do comando no stdin dele. Retorna erro se não houver sessão
// interativa nem winlogon acessível.
func spawnRemoteSessionWorker(parent context.Context, payload map[string]any) error {
	return spawnRemoteSessionWorkerAttempt(parent, payload, false)
}

// spawnRemoteSessionWorkerAttempt é o spawn com controle de relançamento: um
// isRespawn=true (watchdog) não reseta o orçamento de relançamentos da sessão.
func spawnRemoteSessionWorkerAttempt(parent context.Context, payload map[string]any, isRespawn bool) error {
	sessionID, _ := payload["sessionId"].(string)
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("payload sem sessionId")
	}

	remoteSessionWorkers.mu.Lock()
	defer remoteSessionWorkers.mu.Unlock()

	// Worker já ativo para esta sessão? Reusa (comandos quality/stop vão ao
	// mesmo processo).
	if w, ok := remoteSessionWorkers.byID[sessionID]; ok {
		select {
		case <-w.done:
			delete(remoteSessionWorkers.byID, sessionID)
		default:
			return writeWorkerPayload(w, payload)
		}
	}

	if !isRespawn {
		remoteSessionRespawnBudget.reset(sessionID, maxRemoteSessionRespawns)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolvendo executável: %w", err)
	}

	// ── Token da sessão interativa ──
	// 1) Usuário logado: token da sessão do console.
	// 2) Sem usuário: token do winlogon da sessão do console (tela de logon).
	tok, source, tokErr := acquireInteractiveSessionToken()
	if tokErr != nil {
		return fmt.Errorf("sem sessão interativa: %w", tokErr)
	}
	defer tok.Close()

	// Diagnóstico UIPI: o que decide se o input chega nas janelas elevadas é
	// a INTEGRIDADE DO TOKEN (não IsElevated): Gerenciador de Tarefas roda
	// SEMPRE High (manifest autoElevate) e a própria UI do agente também
	// (requireAdministrator). Um worker Medium tem o SendInput descartado
	// silenciosamente quando qualquer uma delas está em primeiro plano — o
	// controle remoto "morre" e volta ao fechá-la. Falha aqui é fail-visible:
	// integridade insuficiente fica marcada como ERRO no log do serviço.
	il := platform.TokenIntegrityLevel(tok)
	log.Printf("[remote-session] worker token: source=%s elevated=%t integrity=%s", source, tok.IsElevated(), il)
	if !platform.IntegritySupportsUipiInjection(il) {
		log.Printf("[remote-session] ERRO: worker com integridade %s — UIPI descartara o input em janelas elevadas (Gerenciador de Tarefas, UI do agente). Controle remoto parcial ate a janela elevada perder o foco.", il)
	}

	proc, stdin, stderr, err := spawnWorkerInSession(exe, tok, source)
	if err != nil {
		return err
	}

	w := &remoteSessionWorkerProc{
		sessionID:       sessionID,
		proc:            proc,
		stdin:           stdin,
		payload:         cloneWorkerPayload(payload),
		parent:          parent,
		stderrDrainDone: make(chan struct{}),
		handshake:       make(chan struct{}),
		done:            make(chan struct{}),
	}
	remoteSessionWorkers.byID[sessionID] = w

	// Dreno do stderr: as mensagens do worker ([remote-session-worker] ...)
	// vão para o log do serviço. Antes (cmd.Stderr = nil) eram descartadas e
	// falhas de startup do worker eram invisíveis.
	go func() {
		defer close(w.stderrDrainDone)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			log.Printf("[remote-session-worker] %s", line)
			w.rememberStderr(line)
			// Handshake: RunRemoteSessionWorker imprime "iniciando sessão <id>"
			// no stderr imediatamente após ler o payload do stdin — ANTES de
			// conectar o NATS. Se esta linha nunca chegar, o binário não está
			// rodando o modo worker (flag não tratada / binário errado).
			if w.handshake != nil && strings.Contains(line, "iniciando sessão ") {
				select {
				case <-w.handshake:
				default:
					close(w.handshake)
				}
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("[remote-session-worker] stderr interrompido: %v", err)
		}
		_ = stderr.Close()
	}()

	go func() {
		_, _ = proc.Wait()
		close(w.done)
		remoteSessionWorkers.mu.Lock()
		if cur, ok := remoteSessionWorkers.byID[sessionID]; ok && cur == w {
			delete(remoteSessionWorkers.byID, sessionID)
		}
		remoteSessionWorkers.mu.Unlock()
		// Watchdog: se o worker morreu com a sessão ativa (ex.: logoff no
		// meio do shutdown), relança para o remoto voltar.
		go w.maybeRespawn()
	}()

	// Monitor do parent: serviço parando → manda stop ao worker e desarma o
	// watchdog (encerramento explícito não relança).
	go func() {
		<-parent.Done()
		w.stopping.Store(true)
		_ = writeWorkerPayload(w, map[string]any{"action": "stop", "sessionId": sessionID})
	}()

	if err := writeWorkerPayload(w, payload); err != nil {
		return fmt.Errorf("enviando payload ao worker: %w", err)
	}

	// Confirma que o filho realmente entrou no MODO worker — não basta estar
	// vivo. Bug de referência (13/09/2026): um binário que ignora a flag
	// --remote-session-worker (ex.: discovery-service.exe sem o dispatch no
	// main) bootava um segundo serviço completo, ficava vivo para sempre e o
	// spawn reportava exitCode=0 falso — o acesso remoto nunca iniciava.
	// O handshake ("iniciando sessão <id>" no stderr) chega em <1s quando o
	// modo worker corre.
	//
	// LIMITAÇÃO CONHECIDA (documentada, não corrigida aqui): o handshake prova
	// apenas que o binário entrou no modo worker — NÃO que o worker conseguiu
	// conectar o NATS (a conexão de streaming acontece DEPOIS do handshake).
	// Se o worker morrer por falta de NATS ("NATS indisponível"), o spawn já
	// publicou exitCode=0 — o mesmo falso positivo do bug de referência, mas
	// agora VISÍVEL no agent-service.log: o dreno do stderr registra
	// "[remote-session-worker] NATS falhou (...)" / "nats conectado: ..."
	// (logger.RedirectStdLog no modo serviço teea o stdlib log para o arquivo).
	// O teto de 20s cobre o pior caso de dial (candidato nativo travado em
	// rede que filtra a 4222 + tentativa wss seguinte).
	select {
	case <-w.done:
		return fmt.Errorf("worker encerrou imediatamente após o spawn (sessionId=%s): %s",
			sessionID, w.stderrTail())
	case <-w.handshake:
		// worker confirmou o modo worker — payload lido, sessão iniciando
	case <-time.After(20 * time.Second):
		return fmt.Errorf("worker não confirmou handshake em 20s (sessionId=%s) — binário sem modo --remote-session-worker? stderr: %s",
			sessionID, w.stderrTail())
	}

	// log.Printf (não fmt.Printf): esta linha precisa chegar ao log persistido
	// do serviço (agent-service.log) — o stdout do serviço não é capturado.
	log.Printf("[remote-session] worker spawnado na sessão interativa (%s, desktop=%s): sessionId=%s pid=%d\n",
		source, workerDesktopName(source), sessionID, proc.Pid)
	return nil
}

// workerDesktopName retorna o desktop alvo do spawn.
//
// REGRESSÃO 2026-09-25: usar SEMPRE winsta0\winlogon com token de USUÁRIO
// (source=user-session, fallback via WTSQueryUserToken) cria o processo, mas a
// inicialização de DLLs de shell (shell32 — o init() do pacote
// github.com/adrg/xdg chama SHGetKnownFolderPath) falha com "A dynamic link
// library (DLL) initialization routine failed" e o worker morre no init().
// O desktop winlogon é restrito a SYSTEM: só é seguro quando o token já está
// na sessão correta (source=winlogon, do processo winlogon). Para token de
// usuário usamos winsta0\default — o comportamento com o qual o remote
// sempre funcionou.
func workerDesktopName(source string) string {
	if source == "winlogon" {
		return `winsta0\winlogon`
	}
	return `winsta0\default`
}

// spawnWorkerInSession lança o binário com CreateProcessAsUser no token da
// sessão interativa, definindo STARTUPINFOW.lpDesktop explicitamente.
//
// POR QUÊ (bug 2026-09-05): o syscall.SysProcAttr do Go NÃO expõe lpDesktop —
// com exec.Command + SysProcAttr.Token o filho herda a window station do
// serviço (sessão 0, sem desktop) e a captura/input falham SEMPRE, inclusive
// na tela de logon. O MeshAgent resolve isso com info.lpDesktop no spawn
// (ILibProcessPipe.c: SpawnTypes_WINLOGON → "Winsta0\\Winlogon").
//
// Retorna (processo, stdin do filho, stderr do filho, erro). O stdin é um
// pipe anônimo herdado (payload framed via STARTF_USESTDHANDLES); o stderr é
// drenado pelo serviço para o log.
func spawnWorkerInSession(exe string, tok windows.Token, source string) (*os.Process, *os.File, *os.File, error) {
	// ── Pipes anônimos herdados (stdin e stderr do filho) ──
	// stdin: serviço escreve → filho lê (read end herdado).
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	// Torna o read end herdável pelo filho.
	if err := setHandleInheritable(stdinR.Fd()); err != nil {
		stdinR.Close()
		stdinW.Close()
		return nil, nil, nil, fmt.Errorf("stdin inheritable: %w", err)
	}
	// stderr: filho escreve → serviço lê (write end herdado).
	serrR, serrW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return nil, nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := setHandleInheritable(serrW.Fd()); err != nil {
		stdinR.Close()
		stdinW.Close()
		serrR.Close()
		serrW.Close()
		return nil, nil, nil, fmt.Errorf("stderr inheritable: %w", err)
	}

	// Evita que o filho herde os ends que não são dele.
	_ = setHandleNonInheritable(windows.Handle(stdinW.Fd()))
	_ = setHandleNonInheritable(windows.Handle(serrR.Fd()))

	argv0, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		cleanupSpawnPipes(stdinR, stdinW, serrR, serrW)
		return nil, nil, nil, fmt.Errorf("caminho do exe inválido: %w", err)
	}
	cmdline, err := windows.UTF16PtrFromString(`"` + exe + `" --remote-session-worker`)
	if err != nil {
		cleanupSpawnPipes(stdinR, stdinW, serrR, serrW)
		return nil, nil, nil, fmt.Errorf("cmdline inválida: %w", err)
	}
	// Desktop alvo definido por workerDesktopName. Fallback defensivo: se o
	// CreateProcessAsUser falhar no desktop primário, tenta o Default.
	desktopCandidates := []string{workerDesktopName(source)}
	if desktopCandidates[0] != `winsta0\default` {
		desktopCandidates = append(desktopCandidates, `winsta0\default`)
	}

	var (
		spawned bool
		lastErr error
		pi      *windows.ProcessInformation
	)
	for _, desktopName := range desktopCandidates {
		desktop, derr := windows.UTF16PtrFromString(desktopName)
		if derr != nil {
			lastErr = fmt.Errorf("desktop %q inválido: %w", desktopName, derr)
			continue
		}
		si := &windows.StartupInfo{
			Desktop:    desktop, // CRÍTICO: sem isso o filho fica na window station da sessão 0
			Flags:      windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW,
			ShowWindow: windows.SW_HIDE,
			StdInput:   windows.Handle(stdinR.Fd()),
			StdOutput:  windows.Handle(serrW.Fd()),
			StdErr:     windows.Handle(serrW.Fd()),
		}
		pi = new(windows.ProcessInformation)
		lastErr = windows.CreateProcessAsUser(
			tok,
			argv0,
			cmdline,
			nil,  // process attributes
			nil,  // thread attributes
			true, // inheritHandles (pipes stdin/stderr)
			windows.CREATE_BREAKAWAY_FROM_JOB|windows.CREATE_NO_WINDOW,
			nil, // environment (herda do SYSTEM — worker não usa vars custom)
			nil, // current directory
			si,
			pi,
		)
		if lastErr == nil {
			spawned = true
			break
		}
		log.Printf("[remote-session] spawn falhou no desktop %s (source=%s): %v — tentando próximo", desktopName, source, lastErr)
	}

	// O pai não precisa mais dos ends herdados pelo filho.
	stdinR.Close()
	serrW.Close()
	if !spawned {
		stdinW.Close()
		serrR.Close()
		return nil, nil, nil, fmt.Errorf("CreateProcessAsUser (%s, desktop=%s): %w", source, strings.Join(desktopCandidates, ","), lastErr)
	}

	// Fecha o handle do thread primário (não usado).
	_ = windows.CloseHandle(pi.Thread)

	proc, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		_ = windows.CloseHandle(pi.Process)
		stdinW.Close()
		serrR.Close()
		return nil, nil, nil, fmt.Errorf("FindProcess(%d): %w", pi.ProcessId, err)
	}
	// NOTA: proc.Wait() do Go usa o handle do Process — repõe o handle que o
	// FindProcess duplicou internamente a partir do pid (OpenProcess). O
	// pi.Process original é fechado para não vazar handle.
	_ = windows.CloseHandle(pi.Process)

	return proc, stdinW, serrR, nil
}

func cleanupSpawnPipes(pipes ...*os.File) {
	for _, p := range pipes {
		_ = p.Close()
	}
}

// setHandleInheritable marca o handle como herdável (SetHandleInformation
// HANDLE_FLAG_INHERIT).
func setHandleInheritable(h uintptr) error {
	return windows.SetHandleInformation(windows.Handle(h), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)
}

// setHandleNonInheritable remove a flag de herança do handle.
func setHandleNonInheritable(h windows.Handle) error {
	return windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, 0)
}

// writeWorkerPayload envia um comando framed ao stdin do worker.
func writeWorkerPayload(w *remoteSessionWorkerProc, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(data)))
	copy(buf[4:], data)
	_, err = w.stdin.Write(buf)
	return err
}

// rememberStderr guarda as últimas linhas do stderr do worker (teto 8) para
// diagnóstico em mensagens de erro do spawn.
func (w *remoteSessionWorkerProc) rememberStderr(line string) {
	w.stderrMu.Lock()
	defer w.stderrMu.Unlock()
	w.stderrLast = append(w.stderrLast, line)
	if len(w.stderrLast) > 8 {
		w.stderrLast = w.stderrLast[len(w.stderrLast)-8:]
	}
}

// stderrTail devolve as últimas linhas do stderr do worker (resumo em uma
// linha, para compor mensagens de erro quando o worker morre sem handshake).
func (w *remoteSessionWorkerProc) stderrTail() string {
	w.stderrMu.Lock()
	defer w.stderrMu.Unlock()
	if len(w.stderrLast) == 0 {
		return "(sem stderr)"
	}
	return strings.Join(w.stderrLast, " | ")
}

// cloneWorkerPayload copia o payload para o watchdog poder relançar o worker
// sem compartilhar o mapa com o chamador (que pode mutá-lo).
func cloneWorkerPayload(payload map[string]any) map[string]any {
	cp := make(map[string]any, len(payload))
	for k, v := range payload {
		cp[k] = v
	}
	return cp
}

// maybeRespawn relança o worker se ele morreu com a sessão ainda ativa e o
// serviço NÃO está encerrando. Limitado por maxRemoteSessionRespawns por sessão.
func (w *remoteSessionWorkerProc) maybeRespawn() {
	if w.stopping.Load() || w.parent == nil || w.parent.Err() != nil {
		return
	}

	// Já existe um worker registrado para a sessão (ex.: um start novo)?
	remoteSessionWorkers.mu.Lock()
	_, exists := remoteSessionWorkers.byID[w.sessionID]
	remoteSessionWorkers.mu.Unlock()
	if exists {
		return
	}

	if !remoteSessionRespawnBudget.consume(w.sessionID) {
		log.Printf("[remote-session] watchdog: orçamento de relançamento esgotado (sessionId=%s) — não relança", w.sessionID)
		return
	}

	select {
	case <-w.parent.Done():
		return
	case <-time.After(2 * time.Second):
	}

	if w.stopping.Load() || w.parent.Err() != nil {
		return
	}

	log.Printf("[remote-session] watchdog: worker morreu com sessão ativa — relançando sessionId=%s", w.sessionID)
	if err := spawnRemoteSessionWorkerAttempt(w.parent, w.payload, true); err != nil {
		log.Printf("[remote-session] watchdog: relançamento falhou (sessionId=%s): %v", w.sessionID, err)
	}
}

// acquireInteractiveSessionToken obtém um token primário da sessão interativa:
// primeiro tenta o usuário logado (WTSQueryUserToken da sessão do console);
// sem usuário, usa o token do próprio serviço (SYSTEM) com a sessão ajustada
// via SetTokenInformation(TokenSessionId) — padrão MeshAgent
// (ILibProcessPipe.c SpawnTypes_WINLOGON), que NÃO duplica o token do
// processo winlogon: o token SYSTEM do serviço já tem acesso ao desktop
// winsta0\winlogon, e o lpDesktop do STARTUPINFO direciona o filho para lá.
func acquireInteractiveSessionToken() (windows.Token, string, error) {
	consoleSession := windows.WTSGetActiveConsoleSessionId()
	if consoleSession == 0xFFFFFFFF {
		return 0, "", fmt.Errorf("nenhuma sessão de console ativa")
	}

	// 1) M-fix: token SYSTEM com a sessão ajustada para a do console — o
	// worker roda como SYSTEM na SESSÃO INTERATIVA. SendInput de um processo
	// SYSTEM não é bloqueado por UIPI: o input funciona em TODAS as janelas,
	// incluindo elevadas (Gerenciador de Tarefas). Com usuário logado o desktop
	// é winsta0\default; sem usuário (tela de logon), winsta0\winlogon.
	if tok, err := systemTokenForSession(consoleSession); err == nil {
		// Checagem por INTEGRIDADE do token (System/High), não IsElevated —
		// o que decide o UIPI é o rótulo de integridade, não o flag de UAC.
		il := platform.TokenIntegrityLevel(tok)
		if !platform.IntegritySupportsUipiInjection(il) {
			// Caller não-SYSTEM (ex.: UI standalone Medium): o token duplicado
			// não pode ser elevado a System — cair para o fallback do usuário
			// logado em vez de rotular um token Medium como "system-session"
			// (UIPI bloquearia o input em janelas elevadas silenciosamente).
			log.Printf("[remote-session] token SYSTEM/sessão descartado: integrity=%s (insuficiente para UIPI) — usando token do usuário", il)
			tok.Close()
		} else {
			source := "system-session"
			if _, userErr := wtsQueryUserToken(consoleSession); userErr != nil {
				source = "winlogon" // sem usuário logado → desktop de logon
			}
			return tok, source, nil
		}
	} else {
		log.Printf("[remote-session] token SYSTEM/sessão indisponível: %v — usando token do usuário (SetTokenInformation exige SeTcbPrivilege)", err)
	}

	// 2) Usuário logado na sessão do console (fallback). M-fix definitivo
	// (bug "controle remoto morre com Gerenciador de Tarefas/UI do agente em
	// foco"): WTSQueryUserToken devolve o token de logon da sessão — para
	// usuários UAC é o token FILTERED (Medium IL). Um worker Medium perde o
	// input EXATAMENTE quando o Gerenciador de Tarefas (autoElevate→High) ou
	// a própria UI do agente (requireAdministrator→High) ganham o primeiro
	// plano: o UIPI descarta o SendInput e o controle volta ao fechá-las.
	// O token é ELEVADO para High aqui (hardenUserSessionToken); se a
	// elevação não for possível, o motivo fica no log (fail-visible) em vez
	// de um worker Medium silencioso.
	if tok, err := wtsQueryUserToken(consoleSession); err == nil {
		hard, hardErr := hardenUserSessionToken(tok)
		if hardErr != nil {
			il := platform.TokenIntegrityLevel(hard)
			log.Printf("[remote-session] AVISO UIPI: token do usuário permanece %s (%v) — input será descartado em janelas elevadas (Gerenciador de Tarefas, UI do agente) enquanto estiverem em primeiro plano", il, hardErr)
		}
		return hard, "user-session", nil
	}

	// 3) Fallback: token do processo winlogon da sessão do console (caminho
	// anterior — menos confiável: depende de direitos de duplicação sobre o
	// processo winlogon, mas cobre casos onde SetTokenInformation falha).
	if tok, err := tokenFromWinlogon(consoleSession); err == nil {
		return tok, "winlogon", nil
	}
	return 0, "", fmt.Errorf("sessão %d sem usuário e sem token SYSTEM disponível", consoleSession)
}

// systemTokenForSession duplica o token do processo atual (SYSTEM, quando
// chamado pelo serviço) e ajusta a sessão via SetTokenInformation —
// equivalente ao caminho WINLOGON do ILibProcessPipe.c do MeshAgent:
//
//	OpenProcessToken(GetCurrentProcess(), TOKEN_DUPLICATE, &token)
//	DuplicateTokenEx(token, MAXIMUM_ALLOWED, ..., TokenPrimary, &userToken)
//	SetTokenInformation(userToken, TokenSessionId, &sessionId, ...)
//	info.lpDesktop = L"Winsta0\\Winlogon"
//	CreateProcessAsUserW(userToken, ...)
func systemTokenForSession(sessionID uint32) (windows.Token, error) {
	cur, err := windows.GetCurrentProcess()
	if err != nil {
		return 0, fmt.Errorf("GetCurrentProcess: %w", err)
	}
	defer windows.CloseHandle(cur)

	var tok windows.Token
	if err := windows.OpenProcessToken(cur, windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &tok); err != nil {
		return 0, fmt.Errorf("OpenProcessToken(self): %w", err)
	}
	defer tok.Close()

	var dup windows.Token
	if err := windows.DuplicateTokenEx(tok,
		windows.MAXIMUM_ALLOWED,
		nil, windows.SecurityImpersonation, windows.TokenPrimary, &dup); err != nil {
		return 0, fmt.Errorf("DuplicateTokenEx(self): %w", err)
	}

	// Ajusta a sessão do token duplicado para a sessão do console. Requer
	// TOKEN_ADJUST_SESSIONID (incluído em MAXIMUM_ALLOWED) — disponível para
	// SYSTEM. Sem isso o filho rodaria na sessão 0 (sem desktop).
	sid := sessionID
	var sidBytes [4]byte
	sidBytes[0] = byte(sid)
	sidBytes[1] = byte(sid >> 8)
	sidBytes[2] = byte(sid >> 16)
	sidBytes[3] = byte(sid >> 24)
	if err := windows.SetTokenInformation(dup, windows.TokenSessionId,
		(*byte)(unsafe.Pointer(&sidBytes[0])), uint32(len(sidBytes))); err != nil {
		dup.Close()
		return 0, fmt.Errorf("SetTokenInformation(TokenSessionId=%d): %w", sessionID, err)
	}
	return dup, nil
}

// hardenUserSessionToken eleva um token de usuário (típico Medium IL) para
// High, para que o worker de remote session não seja vítima do UIPI.
//
// CONTRATO DE POSSE: assume a posse de tok; fecha-o se devolver OUTRO token.
// Quando devolve o próprio tok (já High/System ou falha total), ele continua
// aberto para o caller fechar.
//
// Ordem das tentativas (a primeira que funcionar vale):
//  1. Token já High/System → devolve como está.
//  2. TokenLinkedToken (GetLinkedToken) — o token COMPLETO do UAC (High
//     nativo) do mesmo logon. Duplicado como primário com MAXIMUM_ALLOWED.
//  3. Rótulo de integridade do DUPLICADO forçado a High (S-1-16-12288) —
//     SetTokenInformation(TokenIntegrityLevel) só é permitido a partir de um
//     processo System/High (o serviço SYSTEM tem IL 0x4000 > 0x3000). A
//     elevação NÃO concede privilégios de administrador: apenas relaxa o
//     UIPI contra janelas High — exatamente o que a sessão remota precisa
//     (captura continua no nível do usuário).
//
// De um caller Medium o passo 3 falha com access denied: devolve o erro para
// o caller logar (fail-visible) em vez de produzir um worker Medium mudo.
func hardenUserSessionToken(tok windows.Token) (windows.Token, error) {
	if platform.IntegritySupportsUipiInjection(platform.TokenIntegrityLevel(tok)) {
		return tok, nil
	}

	// 2) Token LINKED do UAC (token completo do usuário — High nativo).
	if linked, err := tok.GetLinkedToken(); err == nil && linked != 0 {
		if platform.IntegritySupportsUipiInjection(platform.TokenIntegrityLevel(linked)) {
			var dup windows.Token
			derr := windows.DuplicateTokenEx(linked, windows.MAXIMUM_ALLOWED, nil,
				windows.SecurityImpersonation, windows.TokenPrimary, &dup)
			linked.Close()
			if derr == nil {
				tok.Close() // substituído — o caller fecha apenas o retornado
				return dup, nil
			}
			// Duplicação falhou: mantém tok aberto e cai para o passo 3.
		} else {
			linked.Close()
		}
	}

	// 3) Eleva o rótulo de integridade do duplicado para High.
	var dup windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.MAXIMUM_ALLOWED, nil,
		windows.SecurityImpersonation, windows.TokenPrimary, &dup); err != nil {
		return tok, fmt.Errorf("duplicando token do usuário: %w", err)
	}
	high, err := windows.StringToSid("S-1-16-12288") // SECURITY_MANDATORY_HIGH_RID
	if err != nil {
		dup.Close()
		return tok, fmt.Errorf("SID de integridade High: %w", err)
	}
	tml := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: high}}
	if err := windows.SetTokenInformation(dup, windows.TokenIntegrityLevel,
		(*byte)(unsafe.Pointer(&tml)), tml.Size()); err != nil {
		dup.Close()
		return tok, fmt.Errorf("SetTokenInformation(TokenIntegrityLevel=High): %w (o caller precisa ser System/High)", err)
	}
	tok.Close()
	return dup, nil
}

// wtsQueryUserToken via wtsapi32.WTSQueryUserToken.
func wtsQueryUserToken(sessionID uint32) (windows.Token, error) {
	var tok windows.Token
	wtsapi := windows.NewLazySystemDLL("wtsapi32.dll")
	proc := wtsapi.NewProc("WTSQueryUserToken")
	ret, _, callErr := proc.Call(uintptr(sessionID), uintptr(unsafe.Pointer(&tok)))
	if ret == 0 {
		return 0, fmt.Errorf("WTSQueryUserToken(%d): %v", sessionID, callErr)
	}
	return tok, nil
}

// tokenFromWinlogon duplica o token primário do processo winlogon da sessão
// (mecanismo MeshAgent SpawnTypes_WINLOGON: o winlogon roda no desktop de
// logon, então o processo filho herda acesso à winsta0\winlogon).
func tokenFromWinlogon(sessionID uint32) (windows.Token, error) {
	// Enumera processos e acha o winlogon da sessão.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, fmt.Errorf("snapshot de processos falhou: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return 0, fmt.Errorf("Process32First: %w", err)
	}
	var winlogonPID uint32
	for {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "winlogon.exe") {
			// Confirma que o winlogon está na sessão do console.
			if procSession, sidErr := procSessionID(entry.ProcessID); sidErr == nil && procSession == sessionID {
				winlogonPID = entry.ProcessID
				break
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	if winlogonPID == 0 {
		return 0, fmt.Errorf("winlogon não encontrado na sessão %d", sessionID)
	}

	// Abre o winlogon (SYSTEM tem acesso) e duplica o token primário.
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, winlogonPID)
	if err != nil {
		return 0, fmt.Errorf("OpenProcess(winlogon): %w", err)
	}
	defer windows.CloseHandle(h)

	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &tok); err != nil {
		return 0, fmt.Errorf("OpenProcessToken(winlogon): %w", err)
	}
	defer tok.Close()

	var dup windows.Token
	if err := windows.DuplicateTokenEx(tok,
		windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE,
		nil, windows.SecurityImpersonation, windows.TokenPrimary, &dup); err != nil {
		return 0, fmt.Errorf("DuplicateTokenEx(winlogon): %w", err)
	}
	return dup, nil
}

// procSessionID obtém a sessão de um PID via kernel32.ProcessIdToSessionId.
func procSessionID(pid uint32) (uint32, error) {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := kernel32.NewProc("ProcessIdToSessionId")
	var sid uint32
	ret, _, _ := proc.Call(uintptr(pid), uintptr(unsafe.Pointer(&sid)))
	if ret == 0 {
		return 0, fmt.Errorf("ProcessIdToSessionId falhou")
	}
	return sid, nil
}

// stopRemoteSessionWorker envia stop ao worker de uma sessão (se ativo).
func stopRemoteSessionWorker(sessionID string) {
	remoteSessionWorkers.mu.Lock()
	w := remoteSessionWorkers.byID[sessionID]
	remoteSessionWorkers.mu.Unlock()
	if w == nil {
		return
	}
	// Encerramento explícito: desarma o watchdog de relançamento.
	w.stopping.Store(true)
	// Worker já morto? Não espera o timeout à toa.
	select {
	case <-w.done:
		return
	default:
	}
	_ = writeWorkerPayload(w, map[string]any{"action": "stop", "sessionId": sessionID})
	// Aguarda o worker encerrar (teto 5s) antes de liberar.
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		_ = w.proc.Kill()
	}
}
