//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

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
		KillZombieUI:         killAgentUIProcesses,
		Log:                  a.logUIWatchdog,
	})
	a.uiWatchdog.Store(watchdog)

	a.logUIWatchdog("watchdog ativo: sem UI companion por 2 verificações (ou convite sem UI) a UI é relançada minimizada no tray")
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

// spawnAgentUIInUserSession relança a UI na sessão do usuário logado, já
// MINIMIZADA no tray (--startup-minimized) e marcada com a origem watchdog.
//
// Roda como o usuário (WTSQueryUserToken → CreateProcessAsUser, o mesmo caminho
// da automação e do worker de sessão remota): um serviço em sessão 0 não pode
// criar janela na sessão interativa.
func (a *App) spawnAgentUIInUserSession(reason string) error {
	uiPath, err := uiAgentExecutablePath()
	if err != nil {
		return err
	}

	userCtx, cleanup, ok := withActiveUserToken(a.supportChatContext())
	if !ok {
		return fmt.Errorf("sem sessão de usuário ativa")
	}
	defer cleanup()

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
		return fmt.Errorf("exec %s: %w", uiPath, err)
	}
	a.logUIWatchdog(fmt.Sprintf("UI iniciada (pid=%d, motivo=%s)", cmd.Process.Pid, reason))
	// Libera o handle do processo (a UI tem vida própria).
	go func() { _ = cmd.Wait() }()
	return nil
}
