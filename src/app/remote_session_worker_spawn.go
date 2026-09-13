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
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// remoteSessionWorkerState guarda os workers ativos por sessionId.
var remoteSessionWorkers struct {
	mu   sync.Mutex
	byID map[string]*remoteSessionWorkerProc
}

func init() { remoteSessionWorkers.byID = make(map[string]*remoteSessionWorkerProc) }

// remoteSessionWorkerProc representa um worker spawnado para uma sessão.
type remoteSessionWorkerProc struct {
	sessionID string
	proc      *os.Process
	stdin     *os.File
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

// spawnRemoteSessionWorker lança o worker na sessão interativa (ou winlogon
// quando não há usuário) e escreve o payload do comando no stdin dele.
// Retorna erro se não houver sessão interativa nem winlogon acessível.
func spawnRemoteSessionWorker(parent context.Context, payload map[string]any) error {
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

	proc, stdin, stderr, err := spawnWorkerInSession(exe, tok, source)
	if err != nil {
		return err
	}

	w := &remoteSessionWorkerProc{
		sessionID:       sessionID,
		proc:            proc,
		stdin:           stdin,
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
	}()

	// Monitor do parent: serviço parando → manda stop ao worker.
	go func() {
		<-parent.Done()
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
	// modo worker corre; o teto de 10s cobre startup lento com segurança.
	select {
	case <-w.done:
		return fmt.Errorf("worker encerrou imediatamente após o spawn (sessionId=%s): %s",
			sessionID, w.stderrTail())
	case <-w.handshake:
		// worker confirmou o modo worker — payload lido, sessão iniciando
	case <-time.After(10 * time.Second):
		return fmt.Errorf("worker não confirmou handshake em 10s (sessionId=%s) — binário sem modo --remote-session-worker? stderr: %s",
			sessionID, w.stderrTail())
	}

	// log.Printf (não fmt.Printf): esta linha precisa chegar ao log persistido
	// do serviço (agent-service.log) — o stdout do serviço não é capturado.
	log.Printf("[remote-session] worker spawnado na sessão interativa (%s, desktop=%s): sessionId=%s pid=%d\n",
		source, workerDesktopName(source), sessionID, proc.Pid)
	return nil
}

// workerDesktopName retorna o desktop alvo do spawn (log/diagnóstico).
// Padrão MeshAgent (ILibProcessPipe.c): SpawnTypes_WINLOGON → "Winsta0\\Winlogon";
// sessão de usuário (user-session ou system-session) → "Winsta0\\Default".
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
	desktop, err := windows.UTF16PtrFromString(workerDesktopName(source))
	if err != nil {
		cleanupSpawnPipes(stdinR, stdinW, serrR, serrW)
		return nil, nil, nil, fmt.Errorf("desktop inválido: %w", err)
	}

	si := &windows.StartupInfo{
		Desktop:    desktop, // CRÍTICO: sem isso o filho fica na window station da sessão 0
		Flags:      windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW,
		ShowWindow: windows.SW_HIDE,
		StdInput:   windows.Handle(stdinR.Fd()),
		StdOutput:  windows.Handle(serrW.Fd()),
		StdErr:     windows.Handle(serrW.Fd()),
	}
	pi := new(windows.ProcessInformation)

	err = windows.CreateProcessAsUser(
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
	// O pai não precisa mais dos ends herdados pelo filho.
	stdinR.Close()
	serrW.Close()
	if err != nil {
		stdinW.Close()
		serrR.Close()
		return nil, nil, nil, fmt.Errorf("CreateProcessAsUser (%s, desktop=%s): %w", source, workerDesktopName(source), err)
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
		source := "system-session"
		if _, userErr := wtsQueryUserToken(consoleSession); userErr != nil {
			source = "winlogon" // sem usuário logado → desktop de logon
		}
		return tok, source, nil
	}

	// 2) Usuário logado na sessão do console (fallback — worker Medium:
	// input em janelas elevadas NÃO funciona, mas captura/uso básico sim).
	if tok, err := wtsQueryUserToken(consoleSession); err == nil {
		return tok, "user-session", nil
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
