// legacyprobe: testa sobrevivência do spawn legacy (CREATE_NEW_CONSOLE + pipes)
// sob o Kaspersky — alternativo ao ConPTY. Uso: go run ./cmd/legacyprobe
package main

import (
	"fmt"
	"os/exec"
	"time"

	"golang.org/x/sys/windows"
)

func main() {
	okCount := 0
	for i := 0; i < 5; i++ {
		cmd := exec.Command("powershell.exe", "-NoLogo", "-NoExit", "-Command", "chcp 65001>$null")
		cmd.SysProcAttr = &windows.SysProcAttr{
			HideWindow:    true,
			CreationFlags: windows.CREATE_NEW_CONSOLE,
		}
		stdin, err := cmd.StdinPipe()
		if err != nil { panic(err) }
		stdout, err := cmd.StdoutPipe()
		if err != nil { panic(err) }
		if err := cmd.Start(); err != nil {
			fmt.Println("spawn falhou:", err)
			continue
		}
		// lê até 2s: o legacy (pipe) não imprime banner no stdout, mas
		// chcp via WriteStdin deve produzir saída
		time.Sleep(1500 * time.Millisecond)
		alive := true
		var code uint32
		h, herr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
		if herr == nil {
			_ = windows.GetExitCodeProcess(h, &code)
			alive = code == 259
			windows.CloseHandle(h)
		}
		if alive {
			// testa I/O: manda um comando e vê se sai algo
			stdin.Write([]byte("Write-Output LEGACY_PROBE_OK\r\n"))
			buf := make([]byte, 4096)
			n := 0
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && n == 0 {
				stdout.(interface{ SetReadDeadline(time.Time) error }).SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				if m, rerr := stdout.Read(buf); rerr == nil {
					n += m
				}
			}
			fmt.Printf("tentativa %d: vivo, io=%d bytes\n", i+1, n)
			okCount++
		} else {
			fmt.Printf("tentativa %d: morto exit=0x%08x\n", i+1, code)
		}
		cmd.Process.Kill()
		stdin.Close()
		time.Sleep(300 * time.Millisecond)
	}
	fmt.Println("legacy ok:", okCount, "/5")
}
