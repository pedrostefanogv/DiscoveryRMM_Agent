//go:build windows

package terminal

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// ConPTYShell representa um terminal interativo usando a API ConPTY do Windows.
// Usa CreatePseudoConsole para fornecer um PTY real, suportando aplicativos TUI
// (nano, vim, htop), cores ANSI/sequencias VT e redimensionamento dinamico.
type ConPTYShell struct {
	hpc HPCON
	cmd *exec.Cmd

	stdinPipe  *os.File // escrita: dados enviados ao processo filho
	stdoutPipe *os.File // leitura: saida do processo filho

	// Fila de input drenada por goroutine dedicada: a escrita no pipe do
	// ConPTY pode BLOQUEAR quando o buffer de input do ConPTY enche (filho
	// travado/não lendo). Escrever direto em WriteStdin (sob s.mu) travava
	// Resize/Dimensions/Close da sessão inteira até o pipe destravar.
	stdinQueue chan string

	shell ShellKind
	cols  int
	rows  int

	mu       sync.Mutex
	closed   bool
	onOutput func(string)
	waitOnce sync.Once
}

// NewConPTYShell cria um novo shell interativo usando ConPTY.
func NewConPTYShell(shell ShellKind, cols, rows int, onOutput func(string)) (*ConPTYShell, error) {
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 40
	}

	exePath, exeArgs := resolveShellCommand(shell)
	fullPath, err := exec.LookPath(exePath)
	if err != nil {
		// Shell inexistente (ex.: PowerShell ausente) — tenta o shell
		// alternativo (cmd) antes de falhar.
		resolvedKind, resolvedPath := ResolveShell(shell)
		if resolvedKind == shell || resolvedPath == "" {
			return nil, fmt.Errorf("shell %q nao encontrado: %w", exePath, err)
		}
		exePath, exeArgs = resolveShellCommand(resolvedKind)
		fullPath = resolvedPath
	}

	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("criar pipe stdin: %w", err)
	}

	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		stdinRead.Close()
		stdinWrite.Close()
		return nil, fmt.Errorf("criar pipe stdout: %w", err)
	}

	size := COORD{X: int16(cols), Y: int16(rows)}
	hpc, err := CreatePseudoConsole(
		size,
		syscall.Handle(stdinRead.Fd()),
		syscall.Handle(stdoutWrite.Fd()),
	)
	if err != nil {
		stdinRead.Close()
		stdinWrite.Close()
		stdoutRead.Close()
		stdoutWrite.Close()
		return nil, fmt.Errorf("CreatePseudoConsole: %w", err)
	}

	cmdLine := buildCommandLine(fullPath, exeArgs)

	var pi procInfo
	err = createProcessConPTY(cmdLine, hpc, &pi)
	if err != nil {
		ClosePseudoConsole(hpc)
		stdinRead.Close()
		stdinWrite.Close()
		stdoutRead.Close()
		stdoutWrite.Close()
		return nil, fmt.Errorf("CreateProcess: %w", err)
	}

	stdinRead.Close()
	stdoutWrite.Close()

	syscall.CloseHandle(syscall.Handle(pi.Thread))

	// os.FindProcess espera um PID, nao um HANDLE. pi.Process guarda o HANDLE
	// retornado pelo CreateProcessW; o PID correto esta em pi.ProcessId.
	process, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		// NÃO deixar o shell órfão: o CreateProcessW já criou o processo, então
		// matamos pelo handle ORIGINAL antes de fechá-lo (o FindProcess pode
		// falhar por acesso, mas o processo está vivo e sem dono).
		_ = syscall.TerminateProcess(syscall.Handle(pi.Process), 1)
		syscall.CloseHandle(syscall.Handle(pi.Process))
		ClosePseudoConsole(hpc)
		stdinWrite.Close()
		stdoutRead.Close()
		return nil, fmt.Errorf("FindProcess: %w", err)
	}
	// O os.FindProcess abre um handle proprio (OpenProcess) a partir do PID,
	// portanto o handle original do CreateProcessW pode (e deve) ser fechado
	// aqui para evitar vazamento.
	syscall.CloseHandle(syscall.Handle(pi.Process))

	s := &ConPTYShell{
		hpc:        hpc,
		cmd:        &exec.Cmd{Process: process},
		stdinPipe:  stdinWrite,
		stdoutPipe: stdoutRead,
		stdinQueue: make(chan string, 256),
		shell:      shell,
		cols:       cols,
		rows:       rows,
		onOutput:   onOutput,
	}

	go s.readLoop(stdoutRead)
	go s.stdinWriterLoop()

	return s, nil
}

// stdinWriterLoop drena a fila de input para o pipe do ConPTY em goroutine
// dedicada (WriteStdin apenas enfileira — nunca bloqueia o chamador).
func (s *ConPTYShell) stdinWriterLoop() {
	for data := range s.stdinQueue {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue // shell fechado — descarta o restante da fila
		}
		s.mu.Unlock()
		if _, err := s.stdinPipe.Write([]byte(data)); err != nil {
			log.Printf("[terminal] stdin write falhou (shell=%s): %v", s.shell, err)
		}
	}
}

func resolveShellCommand(shell ShellKind) (exe string, args []string) {
	if IsWSL(shell) {
		distro := strings.TrimSpace(ShellKindToWSLDistro(shell))
		if distro == "" {
			return "wsl.exe", nil
		}
		// A14: distro vem do servidor (campo shell da sessão) sem validação
		// e antes era concatenada na command line ("wsl:Ubuntu --exec cmd
		// /c whoami" → RCE). Camadas de defesa:
		//   1) charset restrito (IsValidWSLDistro) — rejeita flags/metas;
		//   2) quando a lista de distros instaladas está disponível, aceita
		//      apenas distros existentes na máquina;
		//   3) quoting do argumento em buildCommandLine (espaços legítimos).
		if !IsValidWSLDistro(distro) {
			return "wsl.exe", nil // fallback seguro: distribuição default
		}
		if _, installed := IsWSLAvailable(); len(installed) > 0 && !containsFold(installed, distro) {
			return "wsl.exe", nil // distro não instalada → default
		}
		return "wsl.exe", []string{"-d", distro}
	}

	switch shell {
	case ShellPowerShell:
		// PowerShell 7+ quando presente (mesma política do ResolveShell).
		if p, err := exec.LookPath("pwsh.exe"); err == nil {
			return p, []string{"-NoLogo", "-NoExit"}
		}
		return "powershell.exe", []string{"-NoLogo", "-NoExit"}
	case ShellCmd:
		return "cmd.exe", nil
	case ShellWSL:
		return "wsl.exe", nil
	default:
		return "powershell.exe", []string{"-NoLogo", "-NoExit"}
	}
}

// containsFold compara ignorando case (helper local para a allowlist WSL).
func containsFold(list []string, want string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

// buildCommandLine monta a command line para CreateProcessW. Argumentos que
// contêm espaço são entre aspas (A14: a distro WSL pode ter espaço legítimo,
// ex.: "Ubuntu 22.04 LTS", e não pode virar dois argumentos).
func buildCommandLine(exePath string, args []string) *uint16 {
	var b strings.Builder
	fmt.Fprintf(&b, `%s`, "\""+exePath+"\"")
	for _, a := range args {
		b.WriteString(" ")
		if strings.ContainsAny(a, " \t") {
			b.WriteString("\"")
			b.WriteString(a)
			b.WriteString("\"")
		} else {
			b.WriteString(a)
		}
	}
	return syscall.StringToUTF16Ptr(b.String())
}

// ── CreateProcess via CreateProcessW ──

type procInfo struct {
	Process   uintptr
	Thread    uintptr
	ProcessId uint32
}

func createProcessConPTY(cmdLine *uint16, hpc HPCON, pi *procInfo) error {
	si := &startupInfoEx{}

	// cb deve ser o tamanho de STARTUPINFOEXW (STARTUPINFOW + lpAttributeList),
	// ou seja 112 bytes em 64-bit. Usar 104 (só STARTUPINFOW) faz o
	// CreateProcessW falhar com ERROR_INVALID_PARAMETER.
	si.cb = startupInfoExSize
	// SEM ISTO O CONPTY NÃO FUNCIONA: o pseudoconsole só é aplicado ao filho
	// quando lpStartupInfo pede os std handles (ver startupInfoFlagUseStdHandles).
	si.dwFlags = startupInfoFlagUseStdHandles
	si.lpAttributeList = nil

	// Monta a PROC_THREAD_ATTRIBUTE_LIST via API oficial. A estrutura possui um
	// cabeçalho interno (Flags/Size/Count/Reserved) que o CreateProcessW valida;
	// montá-la manualmente (como na implementação anterior) produz
	// ERROR_INVALID_PARAMETER mesmo com cb correto.
	var attrSize uintptr
	// 1) Descobre o tamanho necessário (espera-se retorno false com *size preenchido).
	_ = initializeProcThreadAttributeList(nil, 1, 0, &attrSize)
	if attrSize == 0 {
		return fmt.Errorf("InitializeProcThreadAttributeList (tamanho) retornou 0")
	}

	attrBuf := make([]byte, attrSize)
	// 2) Inicializa a lista no buffer.
	if err := initializeProcThreadAttributeList(unsafe.Pointer(&attrBuf[0]), 1, 0, &attrSize); err != nil {
		return fmt.Errorf("InitializeProcThreadAttributeList: %v", err)
	}
	defer deleteProcThreadAttributeList(unsafe.Pointer(&attrBuf[0]))

	// 3) Adiciona o atributo PSEUDOCONSOLE (lpValue = HPCON, cbSize = sizeof(HPCON)).
	if err := updateProcThreadAttribute(
		unsafe.Pointer(&attrBuf[0]),
		0,
		PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		uintptr(hpc),
		unsafe.Sizeof(hpc),
		nil,
		nil,
	); err != nil {
		return fmt.Errorf("UpdateProcThreadAttribute: %v", err)
	}

	si.lpAttributeList = unsafe.Pointer(&attrBuf[0])

	var piNative procInfoNative
	err := createProcessW(
		nil, cmdLine,
		nil, nil,
		false,
		extendedStartupinfoPresent|syscall.CREATE_UNICODE_ENVIRONMENT,
		nil, nil,
		si,
		&piNative,
	)
	if err != nil {
		return err
	}

	pi.Process = uintptr(piNative.Process)
	pi.Thread = uintptr(piNative.Thread)
	pi.ProcessId = piNative.ProcessId
	return nil
}

const (
	// STARTUPINFOW tem 104 bytes (64-bit); STARTUPINFOEXW adiciona o campo
	// lpAttributeList (8 bytes), totalizando 112 — ver startupInfoEx abaixo.

	// EXTENDED_STARTUPINFO_PRESENT not in Go's syscall package
	extendedStartupinfoPresent = 0x00080000

	// STARTF_USESTDHANDLES (wingdi.h) = 0x00000100. É OBRIGATÓRIO com
	// pseudoconsole: sem este flag o Windows NÃO aplica os handles do ConPTY ao
	// processo filho. O filho passa a rodar no console do PAI (a saída vaza
	// para o stdout do agente), morre no boot com STATUS_DLL_INIT_FAILED
	// (0xC0000142) e o pseudoconsole nunca recebe um byte — era exatamente isso
	// que fazia o ConPTY "sempre falhar" e o terminal cair para o modo
	// compatibilidade. Confirmado por bisseção: sem o flag, 0 bytes e processo
	// morto; com o flag, a saída volta pelo pseudoconsole e o shell permanece
	// vivo.
	// CUIDADO: não confundir com STARTF_USESHOWWINDOW (0x1), que é o valor
	// errado mais fácil de escrever aqui.
	startupInfoFlagUseStdHandles = 0x00000100
)

// startupInfoEx espelha STARTUPINFOEXW (x64) campo a campo — ordem e tamanho
// importam para o CreateProcessW. STARTUPINFOW = 104 bytes + lpAttributeList.
// O layout é validado por test (startup_info_layout_windows_test.go).
type startupInfoEx struct {
	cb              uint32
	lpReserved      uintptr
	lpDesktop       uintptr
	lpTitle         uintptr
	dwX             uint32
	dwY             uint32
	dwXSize         uint32
	dwYSize         uint32
	dwXCountChars   uint32
	dwYCountChars   uint32
	dwFillAttribute uint32
	dwFlags         uint32
	wShowWindow     uint16
	cbReserved2     uint16
	lpReserved2     uintptr
	hStdInput       uintptr
	hStdOutput      uintptr
	hStdError       uintptr
	lpAttributeList unsafe.Pointer
}

// startupInfoExSize é o cb do STARTUPINFOEXW (112 em x64).
var startupInfoExSize = uint32(unsafe.Sizeof(startupInfoEx{}))

type procInfoNative struct {
	Process   syscall.Handle
	Thread    syscall.Handle
	ProcessId uint32
	ThreadId  uint32
}

func createProcessW(
	appName, cmdLine *uint16,
	procAttr, threadAttr *syscall.SecurityAttributes,
	inheritHandles bool,
	creationFlags uint32,
	env, currentDir *uint16,
	startupInfo *startupInfoEx,
	procInfo *procInfoNative,
) error {
	var inherit uint32
	if inheritHandles {
		inherit = 1
	}
	r1, _, e1 := procCreateProcessW.Call(
		uintptr(unsafe.Pointer(appName)),
		uintptr(unsafe.Pointer(cmdLine)),
		uintptr(unsafe.Pointer(procAttr)),
		uintptr(unsafe.Pointer(threadAttr)),
		uintptr(inherit),
		uintptr(creationFlags),
		uintptr(unsafe.Pointer(env)),
		uintptr(unsafe.Pointer(currentDir)),
		uintptr(unsafe.Pointer(startupInfo)),
		uintptr(unsafe.Pointer(procInfo)),
	)
	if r1 == 0 {
		return fmt.Errorf("CreateProcessW: %w", e1)
	}
	return nil
}

var procCreateProcessW = kernel32.NewProc("CreateProcessW")

// ── Metodos publicos ──

// WriteStdin enfileira o input (não-bloqueante). A escrita real acontece na
// goroutine stdinWriterLoop — ver comentário no campo stdinQueue.
func (s *ConPTYShell) WriteStdin(data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shell fechado")
	}
	select {
	case s.stdinQueue <- data:
		return nil
	default:
		return fmt.Errorf("fila de input cheia (%d/%d)", len(s.stdinQueue), cap(s.stdinQueue))
	}
}

func (s *ConPTYShell) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shell fechado")
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("dimensoes invalidas: %dx%d", cols, rows)
	}
	if err := ResizePseudoConsole(s.hpc, COORD{X: int16(cols), Y: int16(rows)}); err != nil {
		return err
	}
	s.cols = cols
	s.rows = rows
	return nil
}

func (s *ConPTYShell) Dimensions() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

func (s *ConPTYShell) ShellKind() ShellKind {
	return s.shell
}

// Alive reporta se o processo shell ainda está em execução, sem consumir o
// Wait() (que é guardado por sync.Once e reservado ao monitor de exit da
// sessão). Usado para detecção de morte prematura no startup.
func (s *ConPTYShell) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if s.cmd == nil || s.cmd.Process == nil {
		return false
	}
	return processAlive(s.cmd.Process.Pid)
}

func (s *ConPTYShell) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true

	// Fecha a fila primeiro: a goroutine stdinWriterLoop descarta o restante
	// (checa s.closed) e sai. Escritas em andamento/pós-close no pipe falham
	// graciosamente (os.File.Write em handle fechado retorna erro, sem panic).
	close(s.stdinQueue)
	s.stdinPipe.Close()
	ClosePseudoConsole(s.hpc)
	cmd := s.cmd
	s.mu.Unlock()

	// Kill/Wait FORA do mutex: se o Kill não matar o processo (raro, mas
	// possível com handle/injetor), Wait bloqueia indefinidamente — e com o
	// mutex travado Resize/Dimensions/Alive/Close ficariam presos, travando o
	// encerramento da sessão inteira.
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		// Reaproveita o mesmo sync.Once do Wait() para não chamar
		// Process.Wait() duas vezes (a goroutine de exit já pode tê-lo feito).
		s.waitOnce.Do(func() {
			_, _ = cmd.Process.Wait()
		})
	}

	s.stdoutPipe.Close()
	return nil
}

func (s *ConPTYShell) readLoop(r io.Reader) {
	buf := make([]byte, 32*1024) // 32KB — menos syscalls e menos mensagens NATS
	for {
		n, err := r.Read(buf)
		if n > 0 {
			output := string(buf[:n])
			// Captura o callback sob o mutex e o INVOCA fora dele: onOutput
			// encadeia até um publish NATS (que pode bloquear). Chamá-lo com
			// s.mu travado prendia Resize/Dimensions/Close/Alive — inclusive a
			// sonda de morte prematura do startup.
			s.mu.Lock()
			cb := s.onOutput
			closed := s.closed
			s.mu.Unlock()
			if cb != nil && !closed {
				cb(output)
			}
		}
		if err != nil {
			return
		}
	}
}

// Wait aguarda o processo shell terminar e retorna o erro de saida.
// Retorna nil se o shell saiu normalmente (exit code 0).
// Thread-safe e seguro para chamadas multiplas (sync.Once).
func (s *ConPTYShell) Wait() error {
	var waitErr error
	s.waitOnce.Do(func() {
		if s.cmd == nil || s.cmd.Process == nil {
			waitErr = fmt.Errorf("processo nao inicializado")
			return
		}
		state, err := s.cmd.Process.Wait()
		if err != nil {
			waitErr = fmt.Errorf("shell exit error: %w", err)
			return
		}
		if code := state.ExitCode(); code != 0 {
			waitErr = fmt.Errorf("shell exit code: %d", code)
		}
	})
	return waitErr
}
