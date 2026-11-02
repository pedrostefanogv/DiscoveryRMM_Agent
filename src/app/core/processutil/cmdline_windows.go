//go:build windows

package processutil

import (
	"os/exec"
	"syscall"
)

// ApplyRawCmdLine entrega a LINHA DE COMANDO CRUA ao processo filho
// (syscall.SysProcAttr.CmdLine). O os/exec do Go escapa aspas embutidas como
// \" (regra CommandLineToArgvW), mas o cmd.exe faz parsing CRU da linha e não
// entende esse escape — o mesmo motivo pelo qual revealLocalFile
// (src/app/openexternal_windows.go) usa CmdLine para o explorer /select.
// Preserva qualquer SysProcAttr já definido (incluindo HideWindow).
func ApplyRawCmdLine(cmd *exec.Cmd, cmdLine string) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = cmdLine
}
