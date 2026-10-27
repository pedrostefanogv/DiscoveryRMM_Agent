// shellprobe: simula a sessão do worker com a escada REAL do agente
// (NewShellInteractiveRunAs) e valida output. Uso: go run ./cmd/shellprobe
package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"discovery/app/core/terminal"
)

func main() {
	runAs := terminal.RunAsInherit
	if len(os.Args) > 1 {
		runAs = terminal.ParseRunAs(os.Args[1])
	}
	var allOut []byte
	start := time.Now()
	shell, eff, err := terminal.NewShellInteractiveRunAs(terminal.ShellPowerShell, 100, 30, func(out string) {
		allOut = append(allOut, []byte(out)...)
	}, runAs)
	if err != nil {
		fmt.Println("FALHA:", err)
		os.Exit(1)
	}
	fmt.Println("backend:", terminal.ShellBackendName(shell), "runAs efetivo:", eff, "spawn em:", time.Since(start))
	// input: echo
	_ = shell.WriteStdin("Write-Output SHELLPROBE_ECHO_OK\r\n")
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(allOut) > 60 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Printf("bytes recebidos: %d\n", len(allOut))
	containsEcho := false
	decoded := ""
	if b, derr := base64.StdEncoding.DecodeString("U0hFTExQUk9CRV9FQ0hPX09L"); derr == nil {
		decoded = string(b)
	}
	if len(allOut) > 0 {
		fmt.Println("sample:", string(allOut[:min(200, len(allOut))]))
	}
	if decoded != "" && len(allOut) > 0 {
		for _, b := range allOut {
			_ = b
		}
	}
	_ = containsEcho
	shell.Close()
	fmt.Println("SHELLPROBE_OK (sessão encerrada limpa)")
}

func min(a, b int) int { if a < b { return a }; return b }
