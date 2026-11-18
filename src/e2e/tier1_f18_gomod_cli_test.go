package e2e_test

import (
	"encoding/binary"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestF18_T1_GoModNoUnusedWebRTCBloat(t *testing.T) {
	gomod := readSrcFile(t, "go.mod")
	if strings.Contains(gomod, "github.com/pion/webrtc") {
		t.Fatalf("F18 violacao: go.mod ainda contem github.com/pion/webrtc (bloat nao utilizado)")
	}

	code, out, err := runGoCmd(t, 20*time.Second, "mod", "why", "github.com/pion/webrtc/v4")
	if err == nil && code == 0 && !strings.Contains(out, "does not need package") {
		t.Fatalf("F18 violacao: `go mod why github.com/pion/webrtc/v4` indica dependencia ativa:\n%s", out)
	}
}

func TestF18_T1_GoModToolchainAndDirectDepsValid(t *testing.T) {
	gomod := readSrcFile(t, "go.mod")

	required := []string{
		"github.com/wailsapp/wails/v3 v3.1.",
		"github.com/nats-io/nats-server/v2 v2.15.",
		"github.com/nats-io/nats.go v1.55.",
		"github.com/Microsoft/go-winio v0.6.",
	}
	for _, req := range required {
		if !strings.Contains(gomod, req) {
			t.Fatalf("go.mod nao contem dependencia esperada %q", req)
		}
	}

	code, out, err := runGoCmd(t, 60*time.Second, "vet", "./app/...")
	if err != nil || code != 0 {
		t.Fatalf("`go vet ./app/...` falhou (code=%d): %v\n%s", code, err, out)
	}
}

func TestF18_T1_AgentCLIHelpAndVersionFamilies(t *testing.T) {
	code, out, errOut, err := execCLI(t, 5*time.Second, nil, "discovery-agent.exe", "--version")
	if err != nil || code != 0 {
		t.Fatalf("--version falhou (code=%d, err=%v): stdout=%q stderr=%q", code, err, out, errOut)
	}
	reVer := regexp.MustCompile(`discovery-agent version .+ commit [0-9a-fA-F]+`)
	if !reVer.MatchString(out) {
		t.Fatalf("--version stdout inesperado: %q", out)
	}

	cases := []struct {
		flag   string
		banner string
	}{
		{"--help", "Opcoes (prefixo --):"},
		{"/?", "Opcoes (prefixo /):"},
		{"-h", "Opcoes (prefixo -):"},
	}
	for _, tc := range cases {
		code, out, errOut, err := execCLI(t, 5*time.Second, nil, "discovery-agent.exe", tc.flag)
		if err != nil || code != 0 {
			t.Fatalf("%s falhou (code=%d, err=%v): stdout=%q stderr=%q", tc.flag, code, err, out, errOut)
		}
		if !strings.Contains(out, tc.banner) {
			t.Fatalf("%s nao imprimiu %q; stdout=%q", tc.flag, tc.banner, out)
		}
	}
}

func TestF18_T1_AgentDeleteCleanupFailsClosedNonElevated(t *testing.T) {
	code, out, errOut, _ := execCLI(t, 5*time.Second, nil, "discovery-agent.exe", "--agent-delete-cleanup")
	if code == 0 {
		t.Fatalf("--agent-delete-cleanup nao pode retornar exit 0 quando nao elevado")
	}
	combined := out + "\n" + errOut
	if !strings.Contains(combined, "requer execucao elevada") {
		t.Fatalf("saida inesperada de --agent-delete-cleanup (code=%d): %q", code, combined)
	}
}

func TestF18_T1_RemoteSessionWorkerRejectsMalformedStdinBothBinaries(t *testing.T) {
	for _, bin := range []string{"discovery-agent.exe", "discovery-service.exe"} {
		code, out, errOut, _ := execCLI(t, 5*time.Second, []byte("not-valid-4byte-frame"), bin, "--remote-session-worker")
		if code != 2 {
			t.Fatalf("%s --remote-session-worker com stdin invalido retornou code=%d (esperado 2); stdout=%q stderr=%q",
				bin, code, out, errOut)
		}
		if !strings.Contains(errOut, "[remote-session-worker] falha ao ler payload") {
			t.Fatalf("%s stderr inesperado: %q", bin, errOut)
		}

		// Tambem valida frame JSON sintaticamente corrompido (4B big-endian length=4 + "{bad")
		buf := make([]byte, 8)
		binary.BigEndian.PutUint32(buf[:4], 4)
		copy(buf[4:], "{bad")
		code2, _, errOut2, _ := execCLI(t, 5*time.Second, buf, bin, "--remote-session-worker")
		if code2 != 2 {
			t.Fatalf("%s com JSON corrompido retornou code=%d (esperado 2); stderr=%q", bin, code2, errOut2)
		}
	}
}
