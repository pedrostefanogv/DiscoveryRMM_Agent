//go:build windows

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// ── Forense de travamento: minidump da UI zumbi antes do kill ───────────────
//
// O dump é a ÚNICA prova do onde a main thread travou quando o processo é
// encerrado: o congelamento de 20/10 (16:38) só foi diagnosticado porque um
// dump manual revelou "ServeWebViewRequest em voo". Agora todo kill de zumbi
// captura o dump automaticamente (MiniDumpWriteDump/MiniDumpNormal — arquivo
// pequeno, só os stacks nativos).

// uiDumpKeep é quantos minidumps ficam retidos em logs/dumps.
const uiDumpKeep = 3

// uiStacksWaitBeforeKill é a espera após o pedido de stacks (IPCMsgDiagnostic
// Stacks) para o read loop da UI zumbi gravar o arquivo antes do Terminate
// Process. Curto de propósito: o dump nativo abaixo não depende dela.
const uiStacksWaitBeforeKill = 1500 * time.Millisecond

var dbghelpZombie = syscall.NewLazyDLL("dbghelp.dll")
var procMiniDumpWriteDump = dbghelpZombie.NewProc("MiniDumpWriteDump")

// requestUIStacksDump pede às UIs conectadas (cliente IPC) que gravem as
// stacks das goroutines. Best-effort: a pipe do zumbi pode já estar morta
// (write com timeout do servidor, erro ignorado).
func (a *App) requestUIStacksDump() {
	if a == nil || a.ipcServer == nil {
		return
	}
	a.ipcServer.Broadcast(NewIPCMessage(IPCMsgDiagnosticStacks, map[string]any{
		"reason": "zombie-kill",
	}))
}

// killAgentUIProcessesWithForensics é o KillZombieUI do watchdog com a
// captura forense ANTES do TerminateProcess:
//  1. pede as stacks (o read loop da UI zumbi grava mesmo com a main thread
//     presa);
//  2. espera uiStacksWaitBeforeKill;
//  3. grava o minidump nativo de cada pid;
//  4. mata (killAgentUIProcesses) e espera sair.
func (a *App) killAgentUIProcessesWithForensics() int {
	pids, err := findAgentUIPIDs()
	if err != nil || len(pids) == 0 {
		return 0
	}

	a.requestUIStacksDump()
	time.Sleep(uiStacksWaitBeforeKill)

	for _, pid := range pids {
		path, dumpErr := captureUIProcessDump(pid)
		if dumpErr != nil {
			a.logUIWatchdog(fmt.Sprintf("minidump da UI travada (pid=%d) falhou: %v", pid, dumpErr))
			continue
		}
		if path != "" {
			a.logUIWatchdog(fmt.Sprintf("minidump da UI travada gravado (pid=%d): %s", pid, path))
		}
	}
	killed := killAgentUIProcesses()
	// CRÍTICO para o relanço: os filhos msedgewebview2 da UI morta sobrevivem ao
	// TerminateProcess e seguram o lock da pasta de dados do WebView2 — a UI
	// relançada 2 s depois congela no CreateCoreWebView2EnvironmentWithOptions
	// esperando esse lock SEM PRAZO (o ciclo do log 20/10: kill às 00:46:55,
	// aceite às 00:46:56, travado; e o "UI sobe e trava sem nunca conectar").
	// Mata os órfãos e espera sumirem antes de devolver o controle ao relanço.
	if orphans := killAgentWebviewOrphans(); orphans > 0 {
		a.logUIWatchdog(fmt.Sprintf("%d webview(s) órfão(s) do agent encerrado(s) para liberar a pasta de dados do WebView2", orphans))
	}
	return killed
}

// captureUIProcessDump grava um minidump do pid em <LogDir>/dumps/. Nunca
// despeja o PRÓPRIO processo (o serviço). Falha é devolvida ao chamador: o
// kill prossegue de qualquer forma — forense não pode impedir a recuperação.
func captureUIProcessDump(pid uint32) (string, error) {
	if pid == 0 || pid == uint32(os.Getpid()) {
		return "", nil
	}
	dir := dumpDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("criar %s: %w", dir, err)
	}
	path := filepath.Join(dir, fmt.Sprintf("ui-zombie-%d-%s.dmp", pid, time.Now().Format("20060102-150405")))

	const openAccess = windows.PROCESS_QUERY_INFORMATION | windows.PROCESS_VM_READ | windows.PROCESS_DUP_HANDLE
	hProc, openErr := windows.OpenProcess(openAccess, false, pid)
	if openErr != nil {
		return "", fmt.Errorf("OpenProcess(%d): %w", pid, openErr)
	}
	defer windows.CloseHandle(hProc)

	f, createErr := os.Create(path)
	if createErr != nil {
		return "", fmt.Errorf("criar %s: %w", path, createErr)
	}
	// MiniDumpWriteDump(hProcess, ProcessId, hFile, DumpType, ExceptionParam,
	// UserStreamParam, CallbackParam) — MiniDumpNormal (0) basta para ver em
	// que syscall/thread o processo está preso.
	r, _, callErr := procMiniDumpWriteDump.Call(
		uintptr(hProc), uintptr(pid), uintptr(f.Fd()), 0, 0, 0, 0)
	f.Close()
	if r == 0 {
		_ = os.Remove(path)
		return "", fmt.Errorf("MiniDumpWriteDump(%d): %v", pid, callErr)
	}
	pruneOldestFiles(dir, "ui-zombie-", uiDumpKeep)
	return path, nil
}
