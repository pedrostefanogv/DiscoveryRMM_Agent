//go:build windows

package terminal

import (
	"fmt"
	"io"
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
		return nil, fmt.Errorf("shell %q nao encontrado: %w", exePath, err)
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

	process, err := os.FindProcess(int(pi.Process))
	if err != nil {
		ClosePseudoConsole(hpc)
		stdinWrite.Close()
		stdoutRead.Close()
		syscall.CloseHandle(syscall.Handle(pi.Process))
		return nil, fmt.Errorf("FindProcess: %w", err)
	}

	s := &ConPTYShell{
		hpc:        hpc,
		cmd:        &exec.Cmd{Process: process},
		stdinPipe:  stdinWrite,
		stdoutPipe: stdoutRead,
		shell:      shell,
		cols:       cols,
		rows:       rows,
		onOutput:   onOutput,
	}

	go s.readLoop(stdoutRead)

	return s, nil
}

func resolveShellCommand(shell ShellKind) (exe string, args []string) {
	if IsWSL(shell) {
		distro := ShellKindToWSLDistro(shell)
		if distro != "" {
			return "wsl.exe", []string{"-d", distro}
		}
		return "wsl.exe", nil
	}

	switch shell {
	case ShellPowerShell:
		return "powershell.exe", []string{"-NoLogo", "-NoExit"}
	case ShellCmd:
		return "cmd.exe", nil
	case ShellWSL:
		return "wsl.exe", nil
	default:
		return "powershell.exe", []string{"-NoLogo", "-NoExit"}
	}
}

func buildCommandLine(exePath string, args []string) *uint16 {
	s := fmt.Sprintf(`"%s"`, exePath)
	if len(args) > 0 {
		s += " " + strings.Join(args, " ")
	}
	return syscall.StringToUTF16Ptr(s)
}

// ── CreateProcess via CreateProcessW ──

type procInfo struct {
	Process uintptr
	Thread  uintptr
}

func createProcessConPTY(cmdLine *uint16, hpc HPCON, pi *procInfo) error {
	si := startupInfoExPool.Get().(*startupInfoEx)
	defer startupInfoExPool.Put(si)
	si.cb = sizeOfStartupInfo
	si.lpAttributeList = nil

	buf := make([]byte, 2*sizeOfAttributeEntry+sizeOfPtr)
	*(*uintptr)(unsafe.Pointer(&buf[0])) = PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE
	*(*uintptr)(unsafe.Pointer(&buf[sizeOfPtr])) = sizeOfPtr
	*(*uintptr)(unsafe.Pointer(&buf[2*sizeOfPtr])) = uintptr(hpc)

	si.lpAttributeList = unsafe.Pointer(&buf[0])

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
	return nil
}

const (
	sizeOfStartupInfo    = 104
	sizeOfAttributeEntry = 24
	sizeOfPtr            = 8

	// EXTENDED_STARTUPINFO_PRESENT not in Go's syscall package
	extendedStartupinfoPresent = 0x00080000
)

type startupInfoEx struct {
	cb              uint32
	_               [sizeOfStartupInfo - 4]byte
	lpAttributeList unsafe.Pointer
}

var startupInfoExPool = sync.Pool{
	New: func() any { return &startupInfoEx{} },
}

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

func (s *ConPTYShell) WriteStdin(data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shell fechado")
	}
	_, err := s.stdinPipe.Write([]byte(data))
	return err
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

func (s *ConPTYShell) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true

	s.stdinPipe.Close()
	ClosePseudoConsole(s.hpc)

	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}

	s.stdoutPipe.Close()
	return nil
}

func (s *ConPTYShell) readLoop(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			output := string(buf[:n])
			s.mu.Lock()
			if s.onOutput != nil && !s.closed {
				s.onOutput(output)
			}
			s.mu.Unlock()
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

var _ io.Reader
var _ fmt.Stringer
