// shellprobe: sobe um PowerShell no ConPTY (a integração OSC 633 vai EMBUTIDA
// no command line do spawn — -EncodedCommand, ver
// app/core/terminal/shell_integration.go) e imprime os bytes recebidos.
// Diagnóstico do startup: o init NÃO pode aparecer digitado/ecoado no fluxo e
// a sequência ESC ] 633 ; P ; Cwd=... deve chegar junto do primeiro prompt.
// Uso: go run ./cmd/shellprobe [conpty]  — 'conpty' força o backend.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"discovery/app/core/terminal"
)

// osc633Needle é o começo da sequência emitida pelo hook do spawn:
// ESC (0x1b) seguido de "]633;".
func osc633Needle() string {
	return string(rune(27)) + "]633;"
}

func main() {
	force := len(os.Args) > 1 && os.Args[1] == "conpty"
	var allOut []byte
	start := time.Now()
	shell, _, err := terminal.NewShellInteractiveRunAs(terminal.ShellPowerShell, 120, 40, func(out string) {
		allOut = append(allOut, []byte(out)...)
	}, terminal.RunAsInherit)
	if err != nil {
		fmt.Println("FALHA:", err)
		os.Exit(1)
	}
	spawnBackend := terminal.ShellBackendName(shell)
	fmt.Println("spawn ok em:", time.Since(start), "backend:", spawnBackend)
	if spawnBackend != "conpty" && !force {
		fmt.Println("caiu no legacy — integração via command line não se aplica; encerrando.")
		shell.Close()
		os.Exit(2)
	}
	deadline := time.Now().Add(10 * time.Second)
	mark := 0
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if len(allOut) > mark {
			chunk := allOut[mark:]
			mark = len(allOut)
			fmt.Printf("+%v RX %d bytes: %q", time.Since(start).Round(250*time.Millisecond), len(chunk), string(chunk))
			fmt.Println()
		}
	}
	shell.Close()
	fmt.Println("=== resumo ===")
	fmt.Printf("OSC 633 recebidas: %d", strings.Count(string(allOut), osc633Needle()))
	fmt.Println(" (esperado >= 1 — hook do spawn ativo)")
	if strings.Contains(string(allOut), "__dshOsc633") {
		fmt.Println("FALHA: o script do init apareceu no fluxo (eco) — não deveria acontecer")
		os.Exit(3)
	}
	fmt.Println("init NÃO apareceu no fluxo (ok — vai embutido no command line)")
	fmt.Println("=== fim ===")
}
