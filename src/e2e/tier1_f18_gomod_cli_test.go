package e2e_test

import (
	"encoding/binary"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestF18_T1_GoModNoUnusedWebRTCBloat guarda contra DEPENDENCIA MORTA.
//
// O contrato MUDOU em 2026-11-21: antes o pion nao podia existir porque nao era
// usado (a negociacao tinha um stub que recusava explicitamente). Agora o
// transporte WebRTC e ADITIVO e OPCIONAL, com uso REAL em
// app/core/remotesession/webrtc_media.go — entao o guard passa a exigir a
// EQUIVALENCIA: pion presente <=> existe call site.
//
// O que o guard NAO perdeu: continua pegando o caso que o motivou — declarar a
// dependencia sem usar (ou parar de usar e deixa-la no go.mod). So deixou de
// proibir a dependencia em si, que agora tem proposito.
func TestF18_T1_GoModNoUnusedWebRTCBloat(t *testing.T) {
	gomod := readSrcFile(t, "go.mod")
	declared := strings.Contains(gomod, "github.com/pion/webrtc")

	// Ha uso real? Inspeciona os imports do pacote de acesso remoto.
	code, out, err := runGoCmd(t, 30*time.Second, "list", "-f", "{{.ImportPath}}|{{range .Imports}}{{.}},{{end}}", "./app/core/remotesession/")
	if err != nil || code != 0 {
		t.Fatalf("go list ./app/core/remotesession/ falhou (code=%d): %v\n%s", code, err, out)
	}

	usesPion := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		for _, imp := range strings.Split(parts[1], ",") {
			if strings.Contains(strings.TrimSpace(imp), "github.com/pion/webrtc") {
				usesPion = true
			}
		}
	}

	if declared && !usesPion {
		t.Fatalf("F18 violacao: go.mod declara github.com/pion/webrtc mas NENHUM pacote o importa (dependencia morta). Remova a dependencia ou ligue o uso.")
	}

	if !declared && usesPion {
		t.Fatalf("F18 violacao: o pacote de acesso remoto importa github.com/pion/webrtc mas ele nao esta declarado no go.mod (rode go mod tidy).")
	}
}

func TestF18_T1_GoModToolchainAndDirectDepsValid(t *testing.T) {
	gomod := readSrcFile(t, "go.mod")

	required := []string{
		"github.com/wailsapp/wails/v3 v3.1.",
		"github.com/nats-io/nats-server/v2 v2.15.",
		"github.com/nats-io/nats.go v1.56.",
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
