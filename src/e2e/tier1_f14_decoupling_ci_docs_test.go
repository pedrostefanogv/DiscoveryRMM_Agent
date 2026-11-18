package e2e_test

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestF14_T1_DiscoveryServiceZeroWailsTransitiveDeps(t *testing.T) {
	code, out, err := runGoCmd(t, 30*time.Second, "list", "-deps", "./cmd/discovery-service")
	if err != nil || code != 0 {
		t.Fatalf("go list -deps ./cmd/discovery-service falhou (code=%d): %v\n%s", code, err, out)
	}
	var wailsDeps []string
	for _, line := range strings.Split(out, "\n") {
		pkg := strings.TrimSpace(line)
		if strings.Contains(pkg, "github.com/wailsapp/wails/") {
			wailsDeps = append(wailsDeps, pkg)
		}
	}
	if len(wailsDeps) > 0 {
		t.Fatalf("F14 violacao: ./cmd/discovery-service ainda puxa %d dependencias Wails UI transitivamente (ex.: %v)",
			len(wailsDeps), wailsDeps[:minInt(5, len(wailsDeps))])
	}
}

func TestF14_T1_AppCorePackagesZeroRootAppOrWailsImports(t *testing.T) {
	code, out, err := runGoCmd(t, 30*time.Second, "list", "-f", "{{.ImportPath}}|{{range .Imports}}{{.}},{{end}}", "./app/core/...")
	if err != nil || code != 0 {
		t.Fatalf("go list ./app/core/... falhou (code=%d): %v\n%s", code, err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		pkg, imports := parts[0], parts[1]
		for _, imp := range strings.Split(imports, ",") {
			imp = strings.TrimSpace(imp)
			if imp == "discovery/app" || strings.HasPrefix(imp, "github.com/wailsapp/wails/") {
				t.Fatalf("F14 violacao: pacote leaf %s importa %s", pkg, imp)
			}
		}
	}
}

func TestF14_T1_CriticalCoreSubpackagesHaveTests(t *testing.T) {
	code, out, err := runGoCmd(t, 30*time.Second, "list", "-f", "{{.ImportPath}}|{{len .TestGoFiles}}|{{len .XTestGoFiles}}", "./app/...")
	if err != nil || code != 0 {
		t.Fatalf("go list ./app/... falhou (code=%d): %v\n%s", code, err, out)
	}

	testCounts := make(map[string]int)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 3 {
			continue
		}
		t1, _ := strconv.Atoi(parts[1])
		t2, _ := strconv.Atoi(parts[2])
		testCounts[parts[0]] = t1 + t2
	}

	required := []string{
		"discovery/app/core/netproxy",
		"discovery/app/core/platform",
		"discovery/app/core/sysctrl",
		"discovery/app/installer",
		"discovery/app/netutil",
		"discovery/app/services/hardwareid",
	}
	for _, pkg := range required {
		if count, ok := testCounts[pkg]; !ok {
			t.Fatalf("pacote esperado nao encontrado em ./app/...: %s", pkg)
		} else if count == 0 {
			t.Fatalf("F14 violacao: subpacote critico %s possui 0 arquivos de teste", pkg)
		}
	}
}

func TestF14_T1_ReleaseWorkflowRunsVetAndTests(t *testing.T) {
	wf := readRepoFile(t, filepath.Join(".github", "workflows", "release-agent-on-tag.yml"))
	if !strings.Contains(wf, "go vet") {
		t.Fatalf("F14 violacao: .github/workflows/release-agent-on-tag.yml nao executa `go vet` antes do release")
	}
	if !strings.Contains(wf, "go test") {
		t.Fatalf("F14 violacao: .github/workflows/release-agent-on-tag.yml nao executa `go test` antes do release")
	}
}

func TestF14_T1_DocsReflectActualWailsVersionAndArchitecture(t *testing.T) {
	gomod := readSrcFile(t, "go.mod")
	reWailsVer := regexp.MustCompile(`github\.com/wailsapp/wails/v3\s+(v[^\s]+)`)
	m := reWailsVer.FindStringSubmatch(gomod)
	if len(m) < 2 {
		t.Fatalf("versao wails/v3 ausente em go.mod")
	}
	wantWails := m[1]

	arch := readRepoFile(t, "ARCHITECTURE.md")
	readme := readRepoFile(t, "README.md")

	if strings.Contains(arch, "v3.0.0-beta.34") || !strings.Contains(arch, wantWails) {
		t.Fatalf("F14 violacao: ARCHITECTURE.md ainda cita versao defasada ou omite %s", wantWails)
	}
	for _, doc := range []struct {
		name string
		body string
	}{
		{"ARCHITECTURE.md", arch},
		{"README.md", readme},
	} {
		if !strings.Contains(doc.body, "discovery-service") {
			t.Fatalf("F14 violacao: %s nao documenta discovery-service.exe / cmd/discovery-service", doc.name)
		}
	}
	if !strings.Contains(arch, "netproxy") || !strings.Contains(arch, "remotesession") {
		t.Fatalf("F14 violacao: ARCHITECTURE.md nao documenta os modulos core/netproxy e core/remotesession")
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
