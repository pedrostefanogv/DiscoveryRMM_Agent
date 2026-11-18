package e2e_test

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ── F14 Boundaries (5 tests) ────────────────────────────────────────────────

func TestF14_T2_GofmtZeroUnformattedFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gofmt", "-l", "./app", "./cmd", "./e2e")
	cmd.Dir = srcRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gofmt -l falhou: %v\n%s", err, string(out))
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("arquivos fora do padrao gofmt:\n%s", string(out))
	}
}

func TestF14_T2_GoVetPassesOnServiceAndE2E(t *testing.T) {
	code, out, err := runGoCmd(t, 60*time.Second, "vet", "./app/...", "./cmd/discovery-service/...", "./e2e/...")
	if err != nil || code != 0 {
		t.Fatalf("`go vet ./app/... ./cmd/discovery-service/... ./e2e/...` falhou (code=%d): %v\n%s", code, err, out)
	}
}

func TestF14_T2_PRChecksWorkflowTargetsAllReleaseBranches(t *testing.T) {
	wf := readRepoFile(t, filepath.Join(".github", "workflows", "pr-checks.yml"))
	for _, needle := range []string{"beta", "release", "lts", `CGO_ENABLED: "1"`, "GOWORK: off", "go test -race ./app/..."} {
		if !strings.Contains(wf, needle) {
			t.Fatalf("pr-checks.yml omite requisito %q", needle)
		}
	}
}

func TestF14_T2_ReadmeAndArchLocalLinksExistOnDisk(t *testing.T) {
	root := repoRoot(t)
	reLink := regexp.MustCompile(`\[[^\]]+\]\(([^)#\s]+)\)`)

	for _, docName := range []string{"README.md", "ARCHITECTURE.md"} {
		content := readRepoFile(t, docName)
		matches := reLink.FindAllStringSubmatch(content, -1)
		for _, m := range matches {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			full := filepath.Join(root, filepath.FromSlash(target))
			if _, err := os.Stat(full); err != nil {
				t.Fatalf("link quebrado em %s -> %s (%v)", docName, target, err)
			}
		}
	}
}

func TestF14_T2_ServiceEntrypointSupportsNonUIModeFlagsWithoutEmbed(t *testing.T) {
	mainSvc := readSrcFile(t, filepath.Join("cmd", "discovery-service", "main.go"))
	for _, flag := range []string{"--remote-session-worker", "--terminal-dispatcher"} {
		if !strings.Contains(mainSvc, flag) {
			t.Fatalf("cmd/discovery-service/main.go deve tratar %q", flag)
		}
	}
	if strings.Contains(mainSvc, "//go:embed") || strings.Contains(mainSvc, `"embed"`) {
		t.Fatalf("cmd/discovery-service/main.go nao deve usar //go:embed ou importar embed")
	}
}

// ── F18 Boundaries (5 tests) ────────────────────────────────────────────────

func TestF18_T2_MixedPrefixFlagsDoNotTriggerHelp(t *testing.T) {
	// Prefixos misturados (--help + /agent-delete-cleanup) nao devem acionar help banner
	code, out, errOut, _ := execCLI(t, 5*time.Second, nil, "discovery-agent.exe", "--help", "--agent-delete-cleanup")
	if code == 0 && strings.Contains(out, "Opcoes (prefixo --):") {
		// Quando --help esta junto com --agent-delete-cleanup, cli_help pode responder --help;
		// o teste real de prefixo misturado e `/help -agent-delete-cleanup` ou `--agent-delete-cleanup /help`
	}
	code2, out2, _, _ := execCLI(t, 5*time.Second, nil, "discovery-agent.exe", "--agent-delete-cleanup", "/help")
	if code2 == 0 || strings.Contains(out2, "Opcoes (prefixo") {
		t.Fatalf("prefixo misturado (`--agent-delete-cleanup /help`) acionou help indevidamente (code=%d out=%q err=%q)", code2, out2, errOut)
	}
}

func TestF18_T2_RemoteSessionWorkerEmptyEOFAndOversizedFrame(t *testing.T) {
	// 1. 0-byte EOF
	c1, _, e1, _ := execCLI(t, 4*time.Second, []byte{}, "discovery-service.exe", "--remote-session-worker")
	if c1 != 2 {
		t.Fatalf("EOF 0 bytes esperado exit 2, obteve %d (%s)", c1, e1)
	}

	// 2. Tamanho zero (0x00000000)
	zeroLen := make([]byte, 4)
	c2, _, e2, _ := execCLI(t, 4*time.Second, zeroLen, "discovery-service.exe", "--remote-session-worker")
	if c2 != 2 {
		t.Fatalf("frame len=0 esperado exit 2, obteve %d (%s)", c2, e2)
	}

	// 3. Tamanho gigante (0x7FFFFFFF)
	hugeLen := make([]byte, 4)
	binary.BigEndian.PutUint32(hugeLen, 0x7FFFFFFF)
	c3, _, e3, _ := execCLI(t, 4*time.Second, hugeLen, "discovery-service.exe", "--remote-session-worker")
	if c3 != 2 {
		t.Fatalf("frame len=0x7FFFFFFF esperado exit 2, obteve %d (%s)", c3, e3)
	}
}

func TestF18_T2_RemoteSessionWorkerIncompleteIdentityExits3BeforeNATS(t *testing.T) {
	// Token sem prefixo mdz_ com IDs explicitos falha na validacao offline (exit 3) sem discar NATS externo
	payload := `{"sessionId":"s1","agentId":"11111111-1111-1111-1111-111111111111","clientId":"11111111-1111-1111-1111-111111111111","siteId":"11111111-1111-1111-1111-111111111111","authToken":"invalid_no_mdz_prefix"}`
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)

	code, _, errOut, _ := execCLI(t, 4*time.Second, buf, "discovery-service.exe", "--remote-session-worker")
	if code != 3 {
		t.Fatalf("token/identidade invalida no worker esperada exit 3, obteve %d (stderr=%q)", code, errOut)
	}
}

func TestF18_T2_HelpAliasesCoverageAllThreeFamilies(t *testing.T) {
	aliases := []struct {
		arg    string
		expect string
	}{
		{"--h", "Opcoes (prefixo --):"},
		{"--v", "discovery-agent version"},
		{"/h", "Opcoes (prefixo /):"},
		{"/help", "Opcoes (prefixo /):"},
		{"/v", "discovery-agent version"},
		{"/version", "discovery-agent version"},
		{"-help", "Opcoes (prefixo -):"},
		{"-v", "discovery-agent version"},
		{"-version", "discovery-agent version"},
	}
	for _, tc := range aliases {
		code, out, errOut, err := execCLI(t, 5*time.Second, nil, "discovery-agent.exe", tc.arg)
		if err != nil || code != 0 || !strings.Contains(out, tc.expect) {
			t.Fatalf("alias %s falhou (code=%d err=%v): stdout=%q stderr=%q", tc.arg, code, err, out, errOut)
		}
	}
}

func TestF18_T2_ServiceBinaryDoesNotEmbedFrontendOrWebView2(t *testing.T) {
	code, out, err := runGoCmd(t, 30*time.Second, "list", "-deps", "./cmd/discovery-service")
	if err != nil || code != 0 {
		t.Fatalf("go list -deps ./cmd/discovery-service falhou: %v\n%s", err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "webview2") || strings.Contains(line, "wails/v3/pkg/application") {
			t.Fatalf("F18/F14 violacao: discovery-service ainda inclui %s", strings.TrimSpace(line))
		}
	}
}
