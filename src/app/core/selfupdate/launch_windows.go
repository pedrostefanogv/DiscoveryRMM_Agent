//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	seeMaskNoConsole       = 0x00008000
	seeMaskNoZoneChecks    = 0x00800000
	seeMaskNoCloseProcess  = 0x00000040 // SEE_MASK_NOCLOSEPROCESS — mantém hProcess aberto
	installerStartupVerify = 3 * time.Second
	installerWatchTimeout  = 5 * time.Minute
)

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

type shellExecuteInfoW struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.HWND
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     unsafe.Pointer
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

func mustUTF16Ptr(s string) *uint16 {
	if s == "" {
		return nil
	}
	ptr, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return ptr
}

// removeMarkOfTheWeb remove o Zone.Identifier ADS de um arquivo baixado.
// Arquivos de sources HTTP ganham Zone.Identifier=3 (Internet) que pode
// fazer o SmartScreen/Defender bloquear ShellExecuteEx("runas") silenciosamente.
func (u *Updater) removeMarkOfTheWeb(exePath string) {
	zonePath := exePath + ":Zone.Identifier"
	err := os.Remove(zonePath)
	if err == nil {
		u.logf("[selfupdate] Mark of the Web removido: %s", exePath)
		return // removido com sucesso
	}
	// Se o arquivo não tem ADS (erro "file not found" ou "cannot find"),
	// não é um problema — o arquivo pode ter vindo do P2P ou de cache local.
	if os.IsNotExist(err) || strings.Contains(err.Error(), "cannot find") {
		return
	}
	// Erro não-esperado (ex.: permissão), mas não bloqueamos o launch.
	u.logf("[selfupdate] aviso: nao foi possivel remover Mark of the Web de %s: %v", exePath, err)
}

// LaunchInstallerElevated lanca o instalador com elevação UAC via ShellExecuteEx("runas").
// Retorna o PID e handle do processo criado para verificação de startup e watchdog.
// Exportado para que o callback PSADT no pacote app possa chamar a elevação UAC
// diretamente de dentro da sessão PSADT.
func LaunchInstallerElevated(exePath string, args string) (pid uint32, processHandle windows.Handle, err error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exePath)
	params := mustUTF16Ptr(args)

	var sei shellExecuteInfoW
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	sei.fMask = seeMaskNoConsole | seeMaskNoZoneChecks | seeMaskNoCloseProcess
	sei.lpVerb = verb
	sei.lpFile = file
	sei.lpParameters = params
	sei.nShow = 0 // SW_HIDE

	ret, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if ret == 0 {
		if callErr != nil && callErr != windows.ERROR_CANCELLED {
			return 0, 0, fmt.Errorf("ShellExecuteEx runas falhou: %v", callErr)
		}
		return 0, 0, fmt.Errorf("ShellExecuteEx runas retornou falso (usuário pode ter negado elevação)")
	}

	// hInstApp contém o valor retornado pela API (>32 indica sucesso).
	// hProcess contém o handle do processo.
	if sei.hProcess == 0 || sei.hProcess == windows.InvalidHandle {
		return 0, 0, fmt.Errorf("ShellExecuteEx retornou sucesso mas hProcess é inválido")
	}
	pid, _ = windows.GetProcessId(windows.Handle(sei.hProcess))
	processHandle = windows.Handle(sei.hProcess)
	return pid, processHandle, nil
}

// launchInstaller lanca o instalador NSIS com elevação admin e verifica que
// o processo iniciou corretamente. O fluxo completo:
//  1. Remove Mark of the Web (Zone.Identifier ADS) para evitar bloqueio SmartScreen.
//  2. ShellExecuteEx("runas") com SEE_MASK_NOCLOSEPROCESS para obter handle.
//  3. Verifica que o processo ainda está rodando após 3s (não morreu imediatamente).
//  4. Se falhou na verificação, retry uma vez com CreateProcess (se já elevado).
//  5. Se o processo iniciou, dispara goroutine de watchdog para capturar exit code.
func (u *Updater) launchInstaller(exePath string) error {
	exePath, err := validateExecutablePath(exePath)
	if err != nil {
		return err
	}

	// ── Remove Mark of the Web ──
	// Arquivos baixados via HTTP ganham Zone.Identifier ADS que pode bloquear
	// ShellExecuteEx("runas") silenciosamente via SmartScreen/Defender.
	u.removeMarkOfTheWeb(exePath)

	// ── ShellExecuteEx("runas") com SEE_MASK_NOCLOSEPROCESS ──
	pid, hProcess, elevateErr := LaunchInstallerElevated(exePath, "/S /UPDATE")
	if elevateErr != nil {
		// Se usuário negou UAC, não tenta fallback.
		if strings.Contains(elevateErr.Error(), "negado") || strings.Contains(elevateErr.Error(), "ERROR_CANCELLED") {
			return fmt.Errorf("instalador requer elevacao administrativa (UAC negado): %w", elevateErr)
		}

		// Fallback: se o processo atual já está elevado (ex.: Task Scheduler
		// rodando como SYSTEM), CreateProcess direto funciona. Caso contrário,
		// tenta ShellExecuteEx("runas") uma segunda vez antes de desistir.
		u.logf("[selfupdate] ShellExecuteEx runas falhou (%v), tentando fallback", elevateErr)
		return u.launchInstallerFallback(exePath, elevateErr)
	}

	u.logf("[selfupdate] instalador elevado iniciado: %s PID=%d", exePath, pid)
	return u.finishLaunchInstaller(exePath, pid, hProcess)
}

// verifyInstallerStarted aguarda installerStartupVerify e verifica se o processo
// ainda está ativo. Retorna true se o processo sobreviveu ao período inicial.
//
// NOTA (fail-open): se não for possível duplicar o handle ou aguardar o processo,
// retorna true (assume que funcionou) para não bloquear o update com um falso
// negativo. Porém, loga claramente o motivo para diagnóstico — um falso positivo
// (retornar false sem motivo) seria pior, pois abortaria um instalador válido.
func (u *Updater) verifyInstallerStarted(pid uint32, hProcess windows.Handle) bool {
	// Abre um segundo handle para WaitForSingleObject com timeout,
	// sem fechar o handle principal que será usado pelo watchdog.
	duplicateHandle, err := windows.GetCurrentProcess()
	if err != nil {
		u.logf("[selfupdate] aviso: nao foi possivel obter handle do processo atual para verificar PID=%d: %v (assumindo que iniciou)", pid, err)
		return true // não consegue verificar, assume que funcionou
	}

	var dupHandle windows.Handle
	err = windows.DuplicateHandle(
		duplicateHandle, hProcess,
		duplicateHandle, &dupHandle,
		0, false, windows.DUPLICATE_SAME_ACCESS,
	)
	if err != nil {
		u.logf("[selfupdate] aviso: nao foi possivel duplicar handle para verificar PID=%d: %v (assumindo que iniciou)", pid, err)
		return true // não consegue verificar, assume que funcionou
	}
	defer windows.CloseHandle(dupHandle)

	// Aguarda installerStartupVerify (3s). Se o processo terminar nesse período,
	// significa que ele falhou ao iniciar.
	waitResult, _ := windows.WaitForSingleObject(dupHandle, uint32(installerStartupVerify.Milliseconds()))
	if waitResult == windows.WAIT_OBJECT_0 {
		// Processo terminou durante o período de verificação.
		var exitCode uint32
		if err := windows.GetExitCodeProcess(dupHandle, &exitCode); err == nil {
			u.logf("[selfupdate] verificacao: instalador PID=%d terminou com exitCode=0x%X durante startup", pid, exitCode)
		} else {
			u.logf("[selfupdate] verificacao: instalador PID=%d terminou durante startup (exit code indisponivel: %v)", pid, err)
		}
		return false
	}
	if waitResult == uint32(windows.WAIT_TIMEOUT) {
		// Processo ainda está rodando após 3s — sucesso!
		return true
	}
	// Erro inesperado (WAIT_FAILED, WAIT_ABANDONED).
	u.logf("[selfupdate] verificacao: WaitForSingleObject retornou 0x%X para PID=%d (assumindo que iniciou)", uint32(waitResult), pid)
	return true // assume que funcionou em caso de erro no wait
}

// waitForProcess aguarda o processo terminar e retorna o exit code.
func waitForProcess(hProcess windows.Handle, timeout time.Duration) (uint32, error) {
	waitResult, err := windows.WaitForSingleObject(hProcess, uint32(timeout.Milliseconds()))
	if err != nil {
		return 0, err
	}
	if waitResult != windows.WAIT_OBJECT_0 {
		return 0, fmt.Errorf("WaitForSingleObject: resultado inesperado 0x%X", uint32(waitResult))
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(hProcess, &exitCode); err != nil {
		return 0, err
	}
	return exitCode, nil
}

// launchInstallerFallback decide o caminho de fallback quando ShellExecuteEx("runas")
// falha por um motivo que não seja UAC negado:
//   - Se o erro original indica que elevação é requerida (ERROR_ELEVATION_REQUIRED),
//     tenta ShellExecuteEx("runas") novamente (não CreateProcess, que falharia).
//   - Se o processo atual já está elevado (SYSTEM/admin), usa CreateProcess direto.
//   - Caso contrário, tenta ShellExecuteEx("runas") uma segunda vez.
func (u *Updater) launchInstallerFallback(exePath string, previousErr error) error {
	// Erro de elevação requerida: CreateProcess sem elevação falharia com
	// Access Denied ao escrever em Program Files. Tenta runas de novo.
	if isElevationRequiredError(previousErr) {
		u.logf("[selfupdate] erro indica elevacao requerida — tentando ShellExecuteEx(runas) novamente")
		pid, hProcess, err := LaunchInstallerElevated(exePath, "/S /UPDATE")
		if err != nil {
			return fmt.Errorf("instalador requer elevacao administrativa (fallback runas falhou): %w (erro anterior: %v)", err, previousErr)
		}
		return u.finishLaunchInstaller(exePath, pid, hProcess)
	}

	if isProcessElevated() {
		u.logf("[selfupdate] processo atual elevado — usando CreateProcess direto")
		return u.launchInstallerCreateProcess(exePath, previousErr)
	}

	u.logf("[selfupdate] processo atual NAO elevado — tentando ShellExecuteEx(runas) novamente")
	pid, hProcess, err := LaunchInstallerElevated(exePath, "/S /UPDATE")
	if err != nil {
		return fmt.Errorf("instalador requer elevacao administrativa (fallback runas falhou): %w (erro anterior: %v)", err, previousErr)
	}
	return u.finishLaunchInstaller(exePath, pid, hProcess)
}

// finishLaunchInstaller executa a verificação de startup e o watchdog para um
// processo de instalador já criado (via ShellExecuteEx ou CreateProcess).
func (u *Updater) finishLaunchInstaller(exePath string, pid uint32, hProcess windows.Handle) error {
	u.logf("[selfupdate] instalador elevado iniciado: %s PID=%d", exePath, pid)

	// ── Verifica que o processo não morreu imediatamente ──
	// O instalador pode falhar ao iniciar (ex.: SmartScreen bloqueou mesmo
	// após remoção do MotW, binário corrompido, etc.) sem que ShellExecuteEx
	// reporte erro — ele só retorna o fato de que o processo foi criado.
	if !u.verifyInstallerStarted(pid, hProcess) {
		windows.CloseHandle(hProcess)
		u.logf("[selfupdate] instalador PID=%d nao sobreviveu ao startup — tentando CreateProcess fallback", pid)
		return u.launchInstallerCreateProcess(exePath, fmt.Errorf("instalador PID=%d terminou durante startup (ShellExecuteEx retornou sucesso mas processo morreu)", pid))
	}

	// Persiste o PID no pending state para correlação entre reinícios.
	u.persistInstallerPID(pid)
	u.incLaunchOK()

	// ── Watchdog goroutine: captura exit code quando o instalador terminar ──
	u.safeGo(func() {
		defer windows.CloseHandle(hProcess)
		u.lastLaunchedPID.Store(pid)
		exitCode, waitErr := waitForProcess(hProcess, installerWatchTimeout)
		if waitErr != nil {
			u.logf("[selfupdate] watchdog: erro ao aguardar PID=%d: %v", pid, waitErr)
			return
		}
		u.lastInstallerExitCode.Store(int32(exitCode))
		u.hasInstallerRun.Store(true)
		if exitCode == 0 {
			u.incInstallComplete()
			u.logf("[selfupdate] watchdog: instalador PID=%d concluiu com sucesso (exitCode=0)", pid)
		} else {
			u.logf("[selfupdate] watchdog: instalador PID=%d terminou com exitCode=0x%X (%d)", pid, exitCode, exitCode)
		}
	})

	return nil
}

// tokenElevation é a struct TOKEN_ELEVATION retornada por GetTokenInformation
// com a classe TokenElevation. O campo TokenIsElevated indica se o token está
// elevado (1) ou não (0).
type tokenElevation struct {
	TokenIsElevated uint32
}

// isProcessElevated verifica se o processo atual roda com privilégios de admin.
// Usa OpenProcessToken + GetTokenInformation(TokenElevation).
func isProcessElevated() bool {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()

	var elevation tokenElevation
	var size uint32
	err := windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &size)
	if err != nil {
		return false
	}
	return elevation.TokenIsElevated != 0
}

// launchInstallerCreateProcess é o fallback via CreateProcess para quando
// ShellExecuteEx("runas") falha. Só funciona se o processo atual já tem
// privilégios de admin (ex.: Task Scheduler rodando como SYSTEM).
func (u *Updater) launchInstallerCreateProcess(exePath string, previousErr error) error {
	argv0, argvErr := windows.UTF16PtrFromString(exePath)
	if argvErr != nil {
		return fmt.Errorf("UTF16PtrFromString: %w (erro anterior: %v)", argvErr, previousErr)
	}

	cmdLine := windows.StringToUTF16Ptr(fmt.Sprintf(`"%s" /S /UPDATE`, exePath))

	var si windows.StartupInfo
	si.Flags = windows.STARTF_USESHOWWINDOW
	si.ShowWindow = 0 // SW_HIDE

	var pi windows.ProcessInformation
	createFlags := uint32(windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS)
	createFlags |= breakawayFromJobFlag
	createFlags |= newProcessGroupFlag

	err := windows.CreateProcess(
		argv0,
		cmdLine,
		nil,
		nil,
		false,
		createFlags,
		nil,
		nil,
		&si,
		&pi,
	)
	if err != nil {
		return fmt.Errorf("CreateProcess falhou: %w (erro anterior: %v)", err, previousErr)
	}

	u.logf("[selfupdate] instalador iniciado via CreateProcess fallback (PID=%d)", pi.ProcessId)

	// ── Verifica que o processo não morreu imediatamente ──
	if !u.verifyInstallerStarted(pi.ProcessId, pi.Process) {
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		return fmt.Errorf("CreateProcess fallback: instalador PID=%d nao sobreviveu ao startup", pi.ProcessId)
	}

	// Persiste o PID no pending state para correlação entre reinícios.
	u.persistInstallerPID(pi.ProcessId)
	u.incLaunchOK()

	// Watchdog goroutine via CreateProcess.
	u.safeGo(func() {
		defer windows.CloseHandle(pi.Thread)
		defer windows.CloseHandle(pi.Process)
		u.lastLaunchedPID.Store(pi.ProcessId)
		exitCode, waitErr := waitForProcess(pi.Process, installerWatchTimeout)
		if waitErr != nil {
			u.logf("[selfupdate] watchdog(CreateProcess): erro ao aguardar PID=%d: %v", pi.ProcessId, waitErr)
			return
		}
		u.lastInstallerExitCode.Store(int32(exitCode))
		u.hasInstallerRun.Store(true)
		if exitCode == 0 {
			u.incInstallComplete()
			u.logf("[selfupdate] watchdog(CreateProcess): instalador PID=%d concluiu com sucesso (exitCode=0)", pi.ProcessId)
		} else {
			u.logf("[selfupdate] watchdog(CreateProcess): instalador PID=%d terminou com exitCode=0x%X (%d)", pi.ProcessId, exitCode, exitCode)
		}
	})

	return nil
}

func validateExecutablePath(exePath string) (string, error) {
	if exePath == "" {
		return "", fmt.Errorf("installer path vazio")
	}
	info, err := os.Stat(exePath)
	if err != nil {
		return "", fmt.Errorf("installer nao encontrado: %w", err)
	}
	// Verifica que não é diretório
	if info.IsDir() {
		return "", fmt.Errorf("installer path aponta para diretorio: %s", exePath)
	}
	return exePath, nil
}

// isElevationRequiredError detecta erros de "required elevation" / operação requer elevação.
func isElevationRequiredError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return containsAnyIgnoreCase(errStr,
		"required elevation",
		"requer elevação",
		"requires elevation",
		"elevation required",
		"740", // ERROR_ELEVATION_REQUIRED
	)
}

func containsAnyIgnoreCase(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
