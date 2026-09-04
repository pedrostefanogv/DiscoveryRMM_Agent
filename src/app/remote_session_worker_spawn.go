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
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/processutil"
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
	cmd       *exec.Cmd
	stdin     interface{ Write([]byte) (int, error) }
	done      chan struct{}
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

	cmd := exec.Command(exe, "--remote-session-worker")
	processutil.HideWindow(cmd)
	cmd.Stderr = nil // worker loga em stderr; serviço não precisa capturar

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}

	// ── Token da sessão interativa ──
	// 1) Usuário logado: token da sessão do console.
	// 2) Sem usuário: token do winlogon da sessão do console (tela de logon).
	tok, source, tokErr := acquireInteractiveSessionToken()
	if tokErr != nil {
		stdinPipe.Close()
		return fmt.Errorf("sem sessão interativa: %w", tokErr)
	}
	defer tok.Close()

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Token = syscall.Token(tok)
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB

	if err := cmd.Start(); err != nil {
		stdinPipe.Close()
		return fmt.Errorf("CreateProcessAsUser (%s): %w", source, err)
	}

	w := &remoteSessionWorkerProc{
		sessionID: sessionID,
		cmd:       cmd,
		stdin:     stdinPipe,
		done:      make(chan struct{}),
	}
	remoteSessionWorkers.byID[sessionID] = w

	go func() {
		_ = cmd.Wait()
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

	// Confirma que o worker realmente iniciou (evita falso "ok" ao servidor:
	// o spawn é assíncrono e o worker pode falhar ao ler o payload, conectar o
	// NATS ou parsear o comando — nesse caso ele sai quase imediatamente).
	// Teto curto: 3s cobre leitura do stdin + conexão NATS sem atrasar o start.
	select {
	case <-w.done:
		return fmt.Errorf("worker encerrou imediatamente após o spawn (sessionId=%s)", sessionID)
	case <-time.After(3 * time.Second):
		// worker vivo após 3s — sessão presumivelmente ativa
	}

	fmt.Printf("[remote-session] worker spawnado na sessão interativa (%s): sessionId=%s pid=%d\n",
		source, sessionID, cmd.Process.Pid)
	return nil
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

// acquireInteractiveSessionToken obtém um token primário da sessão interativa:
// primeiro tenta o usuário logado (WTSQueryUserToken da sessão do console);
// sem usuário, usa o token do processo winlogon dessa sessão (tela de logon).
func acquireInteractiveSessionToken() (windows.Token, string, error) {
	consoleSession := windows.WTSGetActiveConsoleSessionId()
	if consoleSession == 0xFFFFFFFF {
		return 0, "", fmt.Errorf("nenhuma sessão de console ativa")
	}

	// 1) Usuário logado na sessão do console.
	if tok, err := wtsQueryUserToken(consoleSession); err == nil {
		return tok, "user-session", nil
	}

	// 2) Sem usuário: token do winlogon da sessão do console (desktop de logon).
	if tok, err := tokenFromWinlogon(consoleSession); err == nil {
		return tok, "winlogon", nil
	}
	return 0, "", fmt.Errorf("sessão %d sem usuário e sem token winlogon", consoleSession)
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
		_ = w.cmd.Process.Kill()
	}
}
