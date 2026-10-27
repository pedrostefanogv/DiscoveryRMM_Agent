// dispatchprobe: replica o cliente do worker (winio.DialPipe) para diagnosticar
// a cadeia dispatcher → pipe → output. Uso: go run ./cmd/dispatchprobe
package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/google/uuid"
)

func main() {
	exe := `C:\Projetos\RMM_Discovery\Discovery\src\bin\discovery-agent.exe`
	session := strings.ReplaceAll(uuid.NewString(), "-", "")
	inPipe := fmt.Sprintf(`\\.\pipe\discovery-term-%s-in`, session)
	outPipe := fmt.Sprintf(`\\.\pipe\discovery-term-%s-out`, session)

	cmd := exec.Command(exe, "--terminal-dispatcher", "--terminal-session="+session, "--terminal-shell=powershell", "--terminal-cols=80", "--terminal-rows=25")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	var inConn, outConn interface{}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if inConn == nil {
			if c, err := winio.DialPipe(inPipe, nil); err == nil {
				inConn = c
			}
		}
		if outConn == nil {
			if c, err := winio.DialPipe(outPipe, nil); err == nil {
				outConn = c
			}
		}
		if inConn != nil && outConn != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if inConn == nil || outConn == nil {
		fmt.Println("FALHA ao conectar nos pipes")
		return
	}
	fmt.Println("CONECTADO in+out")
	out := outConn.(interface{ Read([]byte) (int, error) })
	inW := inConn.(interface{ Write([]byte) (int, error) })

	total := 0
	sample := make([]byte, 0, 1024)
	buf := make([]byte, 4096)
	outRead := make(chan int, 1)
	go func() {
		for {
			n, err := out.Read(buf)
			if err != nil {
				fmt.Println("out read fim:", err)
				return
			}
			total += n
			if len(sample) < 500 {
				sample = append(sample, buf[:n]...)
			}
			outRead <- n
		}
	}()
	select {
	case <-outRead:
	case <-time.After(3 * time.Second):
	}
	fmt.Printf("bytes apos 3s: %d\n", total)
	// manda echo
	inW.Write([]byte(base64.StdEncoding.EncodeToString([]byte("echo PROBE_ECHO_OK\r\n")) + "\n"))
	time.Sleep(4 * time.Second)
	fmt.Printf("bytes totais: %d\n", total)
	fmt.Printf("sample: %q\n", string(sample[:min(len(sample), 300)]))
}

func min(a, b int) int { if a < b { return a }; return b }
