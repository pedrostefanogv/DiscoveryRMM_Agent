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
	"discovery/app/core/platform"
	"discovery/app/core/processutil"
)

// agentUIExecutableName é o binário da UI (sessão do usuário), irmão do
// discovery-service.exe no diretório de instalação.
const agentUIExecutableName = "discovery-agent.exe"

// Leitura do CommandLine via ntdll (ver processCommandLine).
var (
	modntdll                       = windows.NewLazySystemDLL("ntdll.dll")
	procNtQueryInformationProcess  = modntdll.NewProc("NtQueryInformationProcess")
)

// ntProcessCommandLineInformation = 60 (ProcessCommandLineInformation).
const ntProcessCommandLineInformation = 60

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

	a.logUIWatchdog("watchdog ativo: relança a UI minimizada no tray em CADA sessão ativa sem interface (limiar de 2 verificações; convite de chat NÃO relança a interface — ajuste 21/10)")
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

// ── Órfãos do WebView2 (ciclo matar/relançar) ───────────────────────────────
//
// CAUSA RAIZ do "UI sobe e trava sem nunca conectar" e do congelamento no
// aceite logo depois de um kill (log 20/10: 00:46:55 kill+relanço → 00:46:56
// aceite → travado): o TerminateProcess na UI NÃO mata os filhos
// msedgewebview2.exe. O browser órfão continua vivo segurando o lock da pasta
// de dados do WebView2 (ProgramData\Discovery\EBWebView); a UI nova nasce
// 2 s depois e o CreateCoreWebView2EnvironmentWithOptions fica esperando esse
// lock SEM PRAZO na main thread — a interface nova congela de novo. Limpar os
// órfãos antes do relanço quebra o ciclo.

// webviewOrphanName é o processo do navegador do WebView2.
const webviewOrphanName = "msedgewebview2.exe"

// procRecord é o mínimo do snapshot de processos para a análise de linhagem.
//
// cmdline é o CommandLine do processo quando legível (best-effort). Vazio
// significa "não foi possível ler OU não havia o que ler" — e a decisão de
// limpeza trata isso como "não mata" (fail-safe). Só é preenchido para
// msedgewebview2.exe: é a única linha que a correção A consulta, e ler o
// CommandLine exige abrir o processo (caro o bastante para não fazer em todos).
type procRecord struct {
	pid     uint32
	ppid    uint32
	name    string
	cmdline string
}

// extrairUserDataDir extrai o valor de --user-data-dir do CommandLine.
//
// O Chromium aceita as duas formas:
//
//	--user-data-dir="C:\ProgramData\Discovery\WebView2\session-1\EBWebView"
//	--user-data-dir=C:\ProgramData\Kaspersky   (sem aspas; termina no 1º espaço)
//
// Devolve ok=false quando a flag não existe ou não há valor. Caminho sem aspas
// contendo espaço é ambíguo por natureza: extraímos até o primeiro espaço e a
// validação por prefixo decide; se não casar com a raiz do agent, NÃO matamos.
func extrairUserDataDir(cmdline string) (string, bool) {
	if cmdline == "" {
		return "", false
	}
	idx := strings.Index(strings.ToLower(cmdline), webView2OrphanFlag)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimLeft(cmdline[idx+len(webView2OrphanFlag):], " \t")
	if rest == "" || rest[0] != '=' {
		return "", false
	}
	rest = strings.TrimLeft(rest[1:], " \t")
	if rest == "" {
		return "", false
	}
	if rest[0] == '"' {
		end := strings.Index(rest[1:], "\"")
		if end < 0 {
			return "", false
		}
		return rest[1 : 1+end], true
	}
	if end := strings.IndexAny(rest, " \t"); end >= 0 {
		return rest[:end], true
	}
	return rest, true
}

// pathHasDirPrefix informa se path está DENTRO de root (ou é o próprio root).
//
// PREFIXO DE DIRETÓRIO — nunca strings.Contains solto, que casaria
// "C:\ProgramData\Discovery\WebView2-evil" ou "...\WebView2Backup".
// Regras: separadores normalizados ('/' ≡ '\\'), case-insensitive (Windows),
// separador final ignorado, e o limite tem de cair num separador ("WebView2X"
// NÃO casa com "WebView2"). Caminho relativo ou vazio NÃO casa: não prova origem.
func pathHasDirPrefix(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	norm := func(s string) string {
		s = strings.ReplaceAll(s, "/", "\\")
		return strings.ToLower(strings.TrimRight(filepath.Clean(s), "\\"))
	}
	p, r := norm(path), norm(root)
	return p == r || strings.HasPrefix(p, r+"\\")
}

// agentWebView2Root devolve a raiz dos dados do WebView2 do agent
// (<DataDir>\WebView2), que cobre as subpastas session-<id> E o perfil base.
// É o CHAMADOR que a resolve — as funções de decisão a recebem por parâmetro,
// para continuarem puras e testáveis sem depender do ambiente da máquina.
func agentWebView2Root() string {
	return filepath.Join(platform.DataDir(), webView2RootSuffix)
}

// processCommandLine lê o CommandLine de um processo (best-effort).
//
// Por que não WMI: Get-CimInstance/Win32_Process custa uma conexão COM e
// centenas de ms no startup da UI — caro demais para uma limpeza de órfãos. A
// via nativa é NtQueryInformationProcess(ProcessCommandLineInformation),
// disponível desde o Windows 8.1.
//
// Devolve ok=false em QUALQUER falha (processo protegido, acesso negado, API
// indisponível, buffer). O chamador trata ok=false como "não mate": preferimos
// deixar um órfão nosso vivo a arriscar a vida de um processo de terceiro.
func processCommandLine(pid uint32) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)

	// Primeira chamada: descobre o tamanho necessário.
	var needed uint32
	status, _, _ := procNtQueryInformationProcess.Call(
		uintptr(h),
		uintptr(ntProcessCommandLineInformation),
		0,
		0,
		uintptr(unsafe.Pointer(&needed)),
	)
	// STATUS_INFO_LENGTH_MISMATCH (0xC0000004) é o esperado na sondagem de tamanho.
	const statusInfoLengthMismatch = 0xC0000004
	if status != statusInfoLengthMismatch || needed == 0 || needed > 64*1024 {
		return "", false
	}

	buf := make([]byte, needed)
	status, _, _ = procNtQueryInformationProcess.Call(
		uintptr(h),
		uintptr(ntProcessCommandLineInformation),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(needed),
		uintptr(unsafe.Pointer(&needed)),
	)
	if status != 0 { // NTSTATUS != STATUS_SUCCESS
		return "", false
	}
	// A API devolve UNICODE_STRING { USHORT Length; USHORT MaximumLength; PWSTR Buffer }
	// no INÍCIO do nosso buffer. O Buffer aponta para DENTRO do PRÓPRIO buffer
	// (medido nesta máquina: offset 16, logo após o cabeçalho da UNICODE_STRING),
	// e não para uma alocação separada — por isso decodificamos por ÍNDICE no
	// slice, sem converter uintptr em ponteiro.
	//
	// Isso é deliberado: converter uintptr->unsafe.Pointer é "possible misuse of
	// unsafe.Pointer" (go vet) porque o GC pode mover o objeto entre a conversão
	// e o uso. Indexando o slice que nós mesmos alocamos, não há essa janela.
	const unicodeStringHeader = 16 // USHORT+USHORT+pad+PWSTR
	length := *(*uint16)(unsafe.Pointer(&buf[0]))
	if length < 2 || int(length) > len(buf)-unicodeStringHeader {
		return "", false
	}
	bufferOffset := *(*uintptr)(unsafe.Pointer(&buf[8]))
	bufStart := uintptr(unsafe.Pointer(&buf[0]))
	// Sanidade: o ponteiro tem de apontar para dentro do buffer que alocamos.
	if bufferOffset < bufStart || bufferOffset+uintptr(length) > bufStart+uintptr(len(buf)) {
		return "", false
	}
	off := int(bufferOffset - bufStart)
	utf16 := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[off])), int(length)/2)
	return windows.UTF16ToString(utf16), true
}

// agentWebviewOrphans devolve os pids de msedgewebview2.exe órfãos do agent.
//
// FUNÇÃO PURA: recebe o snapshot e a raiz dos dados do WebView2, não faz I/O.
// A raiz vem por PARÂMETRO (o chamador passa agentWebView2Root()) para o teste
// cobrir os casos sem depender do ambiente da máquina.
//
// Um candidato só é órfão quando TODAS valem:
//  1. o nome é msedgewebview2.exe;
//  2. a linhagem é lineageBroken — nem alcança host nosso vivo, nem app alheio;
//  3. o --user-data-dir está DENTRO de webView2Root (trava da correção A).
//
// POR QUE A CONDIÇÃO 3 EXISTE (não remova): a linhagem sozinha decidia a
// identidade sobre um snapshot INSTANTÂNEO. Com 28 de 33 webviews tendo outro
// msedgewebview2.exe como pai, qualquer elo ausente do snapshot (processo
// nascendo/morrendo/reparentado no instante da captura) fazia a subida bater no
// vazio e a regra "pai ausente => órfão" classificar o webview de um TERCEIRO
// como nosso. Em produção isso fechou o MICROSOFT TEAMS do usuário ao abrir o
// agent pelo Menu Iniciar (log: "24 browser process(es) orfaos ... encerrados",
// quando o normal são 5-8). O --user-data-dir é inequívoco e não depende de
// corrida: Teams = %LOCALAPPDATA%\Packages\MSTeams_..., o nosso =
// <DataDir>\WebView2\session-N\EBWebView. A linhagem CONTINUA sendo exigida —
// a trava é ADICIONAL, não substituta.
func agentWebviewOrphans(procs []procRecord, webView2Root string) []uint32 {
	aliveAgents := map[uint32]bool{}
	for _, p := range procs {
		if strings.EqualFold(p.name, agentUIExecutableName) {
			aliveAgents[p.pid] = true
		}
	}

	var orphans []uint32
	for _, p := range procs {
		if !strings.EqualFold(p.name, webviewOrphanName) {
			continue
		}
		if classifyLineage(p, procs, aliveAgents) != lineageBroken {
			continue // host nosso vivo OU app alheio: nunca mexer
		}
		if !webviewUserDataDirIsOurs(p.cmdline, webView2Root) {
			continue // não provou que a pasta de dados é nossa: fail-safe
		}
		orphans = append(orphans, p.pid)
	}
	return orphans
}

// processSnapshotWithCommandLine captura pid/ppid/nome de todos os processos,
// acrescentando o CommandLine apenas para msedgewebview2.exe (a única linha que
// a trava da correção A consulta). Ler o CommandLine abre o processo, então
// fazê-lo para todos seria desperdício.
func processSnapshotWithCommandLine() ([]procRecord, error) {
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
	var procs []procRecord
	for {
		rec := procRecord{
			pid:  entry.ProcessID,
			ppid: entry.ParentProcessID,
			name: strings.ToLower(windows.UTF16ToString(entry.ExeFile[:])),
		}
		if rec.name == webviewOrphanName {
			if cl, ok := processCommandLine(rec.pid); ok {
				rec.cmdline = cl
			}
		}
		procs = append(procs, rec)
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return procs, nil
}

// killAgentWebviewOrphans encerra os webviews órfãos do agent e devolve
// quantos. Log é responsabilidade do chamador.
func killAgentWebviewOrphans() int {
	procs, err := processSnapshotWithCommandLine()
	if err != nil {
		return 0
	}
	root := agentWebView2Root()
	killed := 0
	for _, pid := range agentWebviewOrphans(procs, root) {
		h, openErr := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if openErr != nil {
			continue
		}
		if termErr := windows.TerminateProcess(h, 1); termErr == nil {
			killed++
		}
		windows.CloseHandle(h)
	}
	if killed == 0 {
		return 0
	}
	// Espera curta: o relanço precisa da pasta de dados LIVRE.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		procs, err = processSnapshotWithCommandLine()
		if err != nil {
			break
		}
		if len(agentWebviewOrphans(procs, root)) == 0 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	return killed
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
	// Última linha de defesa antes da UI nascer: qualquer webview órfão de um
	// kill anterior segurando a pasta de dados congela a UI NO PARTO (env
	// creation sem prazo). Limpa antes de criar o processo.
	if orphans := killAgentWebviewOrphans(); orphans > 0 {
		a.logUIWatchdog(fmt.Sprintf("%d webview(s) órfão(s) limpo(s) antes de relançar a UI (sessão %d)", orphans, sessionID))
	}

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
