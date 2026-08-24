package terminal

import (
	"errors"
	"os/exec"
)

// IShell é a interface comum dos shells interativos do terminal remoto.
// Tanto a implementação ConPTY (ConPTYShell) quanto a legada (Shell/console
// real) satisfazem esta interface, permitindo trocar de backend em runtime
// quando ConPTY se mostra instável (ex.: injetor de DLL / AV mata o processo
// filho com 0xC0000142 = STATUS_DLL_INIT_FAILED).
type IShell interface {
	// WriteStdin envia dados (teclado) para o shell.
	WriteStdin(data string) error
	// Resize notifica o terminal sobre mudança de dimensões.
	Resize(cols, rows int) error
	// Close encerra o shell e libera recursos.
	Close() error
	// Wait aguarda o processo do shell terminar e retorna o erro de saída.
	Wait() error
	// ShellKind retorna o tipo de shell em uso (powershell, cmd, wsl...).
	ShellKind() ShellKind
	// Alive reporta se o processo do shell ainda está em execução, sem
	// bloquear nem consumir o Wait() — usado para detecção não-conflitante
	// de morte prematura no startup.
	Alive() bool
}

// STATUS_DLL_INIT_FAILED (0xC0000142) — uma DLL injetada (ex.: AV/EDR) falhou
// no DllMain e o sistema encerrou o processo imediatamente, antes de produzir
// qualquer output. É o indicador-chave de que ConPTY está instável no host.
const STATUS_DLL_INIT_FAILED = uint32(0xC0000142)

// IsDLLInitFailedError reporta se o erro (retornado por IShell.Wait())
// corresponde a um exit code 0xC0000142 = STATUS_DLL_INIT_FAILED.
func IsDLLInitFailedError(err error) bool {
	code, ok := ExitCodeOf(err)
	return ok && uint32(code) == STATUS_DLL_INIT_FAILED
}

// ExitCodeOf extrai o exit code de um erro retornado por IShell.Wait().
// Suporta *exec.ExitError (usado pelo shell legado). Retorna (code, true)
// quando o processo terminou com código não-zero.
func ExitCodeOf(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ProcessState.ExitCode(), true
	}
	return 0, false
}