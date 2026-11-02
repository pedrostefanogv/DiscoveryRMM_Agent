//go:build !windows

package processutil

import "os/exec"

// ApplyRawCmdLine é no-op fora do Windows: sem cmd.exe não há parsing CRU de
// linha de comando e o argv padrão do os/exec é suficiente.
func ApplyRawCmdLine(cmd *exec.Cmd, cmdLine string) { _ = cmd }
