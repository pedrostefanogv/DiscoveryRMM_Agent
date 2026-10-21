//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/ctxutil"
	"discovery/app/core/processutil"
)

// agentUIExecutableName é o binário da UI (sessão do usuário), irmão do
// discovery-service.exe no diretório de instalação.
const agentUIExecutableName = "discovery-agent.exe"

// uiWatchdogKillWait é o teto de espera para os processos encerrados realmente
// sumirem. O Wails mantém o lock de instância única até o processo morrer de
// fato: relançar antes disso faria a nova instância sair na hora.
const uiWatchdogKillWait = 5 * time.Second

// startUIWatchdog sobe o watchdog da UI companion. Só chamado no modo serviço
// (é o serviço que sobrevive à UI e pode relançá-la).
func (a *App) startUIWatchdog(ctx context.Context) {
	if a == nil {
		return
	}
	if a.uiWatchdog.Load() != nil {
		return
	}
	// Sem servidor IPC NENHUMA UI consegue se conectar: o watchdog relançaria a
	// interface para sempre (ela sobe, não conecta e é tratada como travada a
	// cada 2 minutos). Melhor não ligar — o log diz o porquê.
	if a.ipcServer == nil {
		a.logUIWatchdog("servidor IPC indisponível — watchdog da UI não será ativado")
		return
	}

	watchdog := newUIWatchdog(uiWatchdogDeps{
		HasUI:                a.hasSupportChatUI,
		ProcessCount:         countAgentUIProcesses,
		UserSessionAvailable: hasActiveUserSession,
		SpawnUI:              a.spawnAgentUIInUserSession,
		// Forense antes do kill: pedido de stacks (IPC) + minidump do zumbi —
		// sem isso o congelamento morre sem prova (o dump manual de 20/10 foi o
		// que revelou "ServeWebViewRequest em voo").
		KillZombieUI: a.killAgentUIProcessesWithForensics,
		Log:          a.logUIWatchdog,
		MarkUISpawn:  a.markSupportChatUISpawn,
		// Multiusuário: uma UI por sessão ativa, sem duplicar a que já existe.
		ActiveSessions:  activeUserSessions,
		UISessionCounts: uiProcessesBySession,
		SpawnUISession:  a.spawnAgentUIInSession,
	})
	a.uiWatchdog.Store(watchdog)

	a.logUIWatchdog("watchdog ativo: relança a UI minimizada no tray em CADA sessão ativa sem interface (limiar de 2 verificações, ou imediato em convite de chat)")
	go watchdog.Run(ctx)
}

// logUIWatchdog registra no logstore do agente (visível no console/coleta).
func (a *App) logUIWatchdog(message string) {
	if a == nil {
		return
	}
	a.Logs.Append("[ui-watchdog] " + message)
}

// hasActiveUserSession informa se existe sessão de console com usuário logado.
func hasActiveUserSession() bool {
	return windows.WTSGetActiveConsoleSessionId() != 0xFFFFFFFF
}

// ── Enumeração de sessões WTS (watchdog multiusuário) ──────────────────────
//
// Cada usuário logado — console ou RDP — ganha a sua UI. Sessão RDP
// DESCONECTADA (WTSDisconnected) entra na lista de propósito: o usuário
// continua logado, o desktop existe e a UI precisa estar viva no tray para
// receber chat/scripts quando ele reconectar. Antes só WTSActive entrava, então
// quem desconectava o RDP ficava sem UI (ou com a UI morta sem relançamento)
// até reconectar.
//
// A sessão 0 (serviços) e 0xFFFFFFFF ("sem sessão") nunca entram, e o
// comportamento anterior é mantido quando a API WTS não está disponível: cai
// para a sessão do console.

// wtsSessionEntry é a projeção testável de WTS_SESSION_INFO (a struct nativa
// exige ponteiros e não é injetável nos testes).
type wtsSessionEntry struct {
	SessionID uint32
	// State espelha WTS_CONNECTSTATE_CLASS (WTSActive, WTSDisconnected, ...).
	State uint32
}

// wtsSessionHasDesktop informa se o estado da sessão tem desktop de usuário.
// WTSActive cobre console/logon normal; WTSDisconnected cobre RDP/quick-switch
// em que o usuário segue logado (a UI fica no tray até a reconexão).
func wtsSessionHasDesktop(state uint32) bool {
	return state == windows.WTSActive || state == windows.WTSDisconnected
}

// uiSessionsFromWTS filtra as entradas cruas do WTS: só sessões com desktop de
// usuário, nunca a sessão 0 (serviços) nem 0xFFFFFFFF (sem sessão).
func uiSessionsFromWTS(entries []wtsSessionEntry) []uint32 {
	sessions := make([]uint32, 0, len(entries))
	for _, entry := range entries {
		if entry.SessionID == 0 || entry.SessionID == 0xFFFFFFFF {
			continue
		}
		if !wtsSessionHasDesktop(entry.State) {
			continue
		}
		sessions = append(sessions, entry.SessionID)
	}
	return sessions
}

// activeUserSessions enumera as SESSÕES INTERATIVAS via WTS (produção).
func activeUserSessions() []uint32 {
	return activeUserSessionsWith(enumerateWTSSessions, windows.WTSGetActiveConsoleSessionId)
}

// activeUserSessionsWith é activeUserSessions com as chamadas WTS injetáveis
// (usado pelos testes de regressão). Fallback preservado: erro na enumeração —
// API WTS indisponível — ou nenhuma sessão utilizável devolve a sessão do
// console, que é o comportamento single-session anterior.
func activeUserSessionsWith(
	enumerate func() ([]wtsSessionEntry, error),
	consoleSession func() uint32,
) []uint32 {
	entries, err := enumerate()
	if err == nil {
		if sessions := uiSessionsFromWTS(entries); len(sessions) > 0 {
			return sessions
		}
	}
	console := consoleSession()
	if console == 0xFFFFFFFF || console == 0 {
		return nil
	}
	return []uint32{console}
}

// enumerateWTSSessions chama WTSEnumerateSessions(WTS_CURRENT_SERVER_HANDLE) e
// devolve as entradas cruas. Devolve erro quando a chamada falha (fallback no
// chamador) e lista vazia quando o servidor não tem sessão nenhuma.
func enumerateWTSSessions() ([]wtsSessionEntry, error) {
	var info *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &info, &count); err != nil {
		return nil, fmt.Errorf("WTSEnumerateSessions: %w", err)
	}
	if info == nil || count == 0 {
		return nil, nil
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(info)))

	entrySize := unsafe.Sizeof(windows.WTS_SESSION_INFO{})
	entries := make([]wtsSessionEntry, 0, count)
	for i := uint32(0); i < count; i++ {
		entry := (*windows.WTS_SESSION_INFO)(unsafe.Pointer(uintptr(unsafe.Pointer(info)) + uintptr(i)*entrySize))
		entries = append(entries, wtsSessionEntry{SessionID: entry.SessionID, State: entry.State})
	}
	return entries, nil
}

// uiProcessesBySession conta os discovery-agent.exe por sessão (ProcessIdToSessionId).
// Erro em um PID é ignorado: o pior caso é tentar relançar onde já existe UI, e
// o single-instance POR SESSÃO barra a duplicata silenciosamente.
func uiProcessesBySession() map[uint32]int {
	counts := make(map[uint32]int)
	pids, err := findAgentUIPIDs()
	if err != nil {
		return counts
	}
	for _, pid := range pids {
		sid, sidErr := procSessionID(pid)
		if sidErr != nil {
			continue
		}
		counts[sid]++
	}
	return counts
}

// countAgentUIProcesses conta os discovery-agent.exe em execução.
//
// Inclui processos-filho do próprio agente que usam o mesmo binário
// (--screenshot-print-server, --terminal-dispatcher). Isso não é problema: só
// chegamos a consultar esta contagem quando NENHUMA UI está conectada no IPC, e
// nesse estado qualquer filho órfão é lixo — encerrar e relançar a UI é o
// resultado desejado.
//
// Erro na enumeração devolve 0 (nenhum processo). O pior caso é tentar relançar
// com uma UI já aberta — o single-instance barra a segunda instância e, por ser
// origem automática, sem roubar foco (main.go).
func countAgentUIProcesses() int {
	pids, err := findAgentUIPIDs()
	if err != nil {
		return 0
	}
	return len(pids)
}

// findAgentUIPIDs devolve os PIDs dos discovery-agent.exe (exclui o próprio
// processo, que é o serviço).
func findAgentUIPIDs() ([]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("Process32First: %w", err)
	}

	self := uint32(os.Getpid())
	var pids []uint32
	for {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), agentUIExecutableName) &&
			entry.ProcessID != self {
			pids = append(pids, entry.ProcessID)
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return pids, nil
}

// killAgentUIProcesses encerra as UIs que estão sem IPC (travadas). Como o
// watchdog só chega aqui quando NENHUMA UI está conectada, todo processo vivo é
// inútil para o core — inclusive em outra sessão, que também seguraria o lock
// de instância única.
func killAgentUIProcesses() int {
	pids, err := findAgentUIPIDs()
	if err != nil || len(pids) == 0 {
		return 0
	}

	killed := 0
	for _, pid := range pids {
		handle, openErr := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if openErr != nil {
			continue
		}
		termErr := windows.TerminateProcess(handle, 1)
		windows.CloseHandle(handle)
		if termErr == nil {
			killed++
		}
	}
	if killed == 0 {
		return 0
	}

	deadline := time.Now().Add(uiWatchdogKillWait)
	for time.Now().Before(deadline) {
		rest, listErr := findAgentUIPIDs()
		if listErr != nil || len(rest) == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return killed
}

// uiAgentExecutablePath resolve o binário da UI a partir do diretório do
// processo atual (serviço e UI ficam lado a lado no diretório de instalação).
func uiAgentExecutablePath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(self)
	path := filepath.Join(dir, agentUIExecutableName)
	if _, statErr := os.Stat(path); statErr != nil {
		return "", fmt.Errorf("%s não encontrado em %s: %w", agentUIExecutableName, dir, statErr)
	}
	return path, nil
}

// spawnAgentUIInSession relança a UI na sessão informada, já MINIMIZADA no
// tray (--startup-minimized) e marcada com a origem watchdog.
//
// Roda como o usuário da sessão (WTSQueryUserToken(sessionID) →
// CreateProcessAsUser via exec + ctxutil): um serviço em sessão 0 não pode
// criar janela na sessão interativa. O sessionID vem de WTSEnumerateSessions e
// define, sozinho, onde a janela nasce — nenhum estado global do serviço
// depende de "a UI".
func (a *App) spawnAgentUIInSession(sessionID uint32, reason string) error {
	if sessionID == 0 || sessionID == 0xFFFFFFFF {
		return fmt.Errorf("sessão %d não tem desktop interativo para a UI", sessionID)
	}
	tok, err := wtsQueryUserToken(sessionID)
	if err != nil {
		return fmt.Errorf("sem token de usuário na sessão %d: %w", sessionID, err)
	}
	if tok == 0 {
		return fmt.Errorf("sem token de usuário na sessão %d (token nulo)", sessionID)
	}
	defer tok.Close()

	userCtx := ctxutil.WithProcessUserToken(a.supportChatContext(), syscall.Token(tok))
	return a.startAgentUIProcess(userCtx, sessionID, reason)
}

// spawnAgentUIInUserSession é o caminho single-session (console). Mantido para
// compatibilidade do wiring legado e usado quando não há sessão enumerada.
func (a *App) spawnAgentUIInUserSession(reason string) error {
	userCtx, cleanup, ok := withActiveUserToken(a.supportChatContext())
	if !ok {
		return fmt.Errorf("sem sessão de usuário ativa")
	}
	defer cleanup()

	sessionID := windows.WTSGetActiveConsoleSessionId()
	return a.startAgentUIProcess(userCtx, sessionID, reason)
}

// startAgentUIProcess cria o processo da UI no contexto de usuário do ctx.
func (a *App) startAgentUIProcess(userCtx context.Context, sessionID uint32, reason string) error {
	uiPath, err := uiAgentExecutablePath()
	if err != nil {
		return err
	}

	// exec.Command (e não CommandContext): a UI não pode morrer junto com o
	// serviço — um restart do serviço (update, sc restart) não deve fechar a
	// janela do usuário.
	cmd := exec.Command(uiPath, "--startup-minimized", "--startup-source="+StartupSourceWatchdog)
	// O serviço roda com CWD em System32; a UI deve nascer no diretório de
	// instalação (mesmo contexto dos autostarts do instalador).
	cmd.Dir = filepath.Dir(uiPath)
	processutil.HideWindow(cmd)
	processutil.ApplyUserContext(userCtx, cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec %s (sessão %d): %w", uiPath, sessionID, err)
	}
	a.logUIWatchdog(fmt.Sprintf("UI iniciada (pid=%d, sessão=%d, motivo=%s)", cmd.Process.Pid, sessionID, reason))
	// Libera o handle do processo (a UI tem vida própria).
	go func() { _ = cmd.Wait() }()
	return nil
}
