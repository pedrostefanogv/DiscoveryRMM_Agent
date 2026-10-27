// shellprobe: reproduz o fluxo ConPTY + init de integração OSC 633 e imprime
// a sequência de bytes recebida (diagnóstico do eco do init no viewer).
// Uso: go run ./cmd/shellprobe [conpty]  — 'conpty' força o backend.
package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"discovery/app/core/terminal"
)

func countClears(b []byte) int {
	n := 0
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0x1b && b[i+1] == '[' && b[i+2] == '2' {
			// \x1b[2J ou \x1b[2K...
			if i+3 < len(b) && b[i+3] == 'J' {
				n++
			}
		}
	}
	return n
}

func main() {
	force := len(os.Args) > 1 && os.Args[1] == "conpty"
	var allOut []byte
	start := time.Now()
	var shell terminal.IShell
	var pendingInit string
	var injectOnce sync.Once
	var spawnBackend string

	shell, _, err := terminal.NewShellInteractiveRunAs(terminal.ShellPowerShell, 120, 40, func(out string) {
		allOut = append(allOut, []byte(out)...)
		if pendingInit != "" {
			injectOnce.Do(func() { _ = shell.WriteStdin(pendingInit + "\r") })
		}
	}, terminal.RunAsInherit)
	if err != nil {
		fmt.Println("FALHA:", err)
		os.Exit(1)
	}
	spawnBackend = terminal.ShellBackendName(shell)
	fmt.Println("spawn ok em:", time.Since(start), "backend:", spawnBackend)
	if spawnBackend != "conpty" && !force {
		fmt.Println("caiu no legacy — init não seria injetado na sessão real; encerrando.")
		shell.Close()
		os.Exit(2)
	}
	pendingInit = "$global:__dshOsc633 = $true; if (-not $global:__dshPromptOriginal) { $global:__dshPromptOriginal = $function:prompt }; function global:prompt { $dshCwd = (Get-Location).Path; if ($dshCwd) { $dshE = [char]27; [Console]::Write($dshE + ']633;P;Cwd=' + $dshCwd + [char]7) }; if ($global:__dshPromptOriginal) { & $global:__dshPromptOriginal } else { 'PS ' + (Get-Location).Path + '> ' } }; Clear-Host"
	// fallback 3s
	go func() {
		time.Sleep(3 * time.Second)
		injectOnce.Do(func() { _ = shell.WriteStdin(pendingInit + "\r") })
	}()
	deadline := time.Now().Add(15 * time.Second)
	mark := 0
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if len(allOut) > mark {
			chunk := allOut[mark:]
			mark = len(allOut)
			fmt.Printf("+%v RX %d bytes: %q\n", time.Since(start).Round(250*time.Millisecond), len(chunk), string(chunk))
		}
	}
	shell.Close()
	fmt.Println("=== fim ===")
}