package app

import (
	"testing"
	"time"
)

func TestPackageKeyFromArtifactName(t *testing.T) {
	cases := map[string]string{
		"winget-foxitfoxitreader.exe": "foxitfoxitreader",
		"winget-googlechromeexe.exe":  "googlechromeexe",
		"winget-7zip7zip.msi":         "7zip7zip",
		"selfupdate-installed.json":   "selfupdateinstalled",
		"":                            "",
	}
	for in, want := range cases {
		if got := packageKeyFromArtifactName(in); got != want {
			t.Fatalf("packageKeyFromArtifactName(%q) = %q, want %q", in, got, want)
		}
	}
}

// O caso real do log: artifact anunciado só por nome (peer sem sidecar .meta).
func TestArtifactPackageKey(t *testing.T) {
	cases := []struct {
		id, name, want string
	}{
		{"winget:googlechromeexe", "winget-googlechromeexe.exe", "googlechromeexe"},
		{"name:winget-foxitfoxitreader.exe", "winget-foxitfoxitreader.exe", "foxitfoxitreader"},
		{"winget-foxitfoxitreader.exe", "winget-foxitfoxitreader.exe", "foxitfoxitreader"},
		{"selfupdate:9bd8a5c3", "selfupdate-9bd8a5c3.exe", "9bd8a5c3"},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := artifactPackageKey(tc.id, tc.name); got != tc.want {
			t.Fatalf("artifactPackageKey(%q,%q) = %q, want %q", tc.id, tc.name, got, tc.want)
		}
	}
}

func TestIsAgentUpdaterArtifact(t *testing.T) {
	if !isAgentUpdaterArtifact("selfupdate:abc", "selfupdate-abc.exe") {
		t.Fatal("selfupdate: deve ser do updater")
	}
	if !isAgentUpdaterArtifact("", "selfupdate-installed.json") {
		t.Fatal("nome selfupdate- deve ser do updater")
	}
	if isAgentUpdaterArtifact("winget:bravebrave", "winget-bravebrave.exe") {
		t.Fatal("pacote winget não é updater")
	}
}

// Gate de fetch: artefato de pacote instalado NÃO deve ser baixado, mesmo quando
// anunciado por nome (o bug do Foxit 368 MB) ou com ID normalizado sem pontos
// (winget:googlechromeexe x Google.Chrome.EXE).
func TestArtifactFetchDecision(t *testing.T) {
	known := map[string]string{
		"foxitfoxitreader": "foxit.foxitreader",
		"googlechromeexe":  "google.chrome.exe",
	}
	finalFor := func(final map[string]bool) func(string) bool {
		return func(packageID string) bool { return final[packageID] }
	}

	cases := []struct {
		name     string
		id, file string
		final    map[string]bool
		want     bool
	}{
		{"pacote instalado por nome: não baixa", "name:winget-foxitfoxitreader.exe", "winget-foxitfoxitreader.exe", map[string]bool{"foxit.foxitreader": true}, false},
		{"pacote instalado com id normalizado: não baixa", "winget:googlechromeexe", "winget-googlechromeexe.exe", map[string]bool{"google.chrome.exe": true}, false},
		{"pacote pendente: baixa", "winget:googlechromeexe", "winget-googlechromeexe.exe", map[string]bool{"google.chrome.exe": false}, true},
		{"atualização pendente: baixa", "name:winget-foxitfoxitreader.exe", "winget-foxitfoxitreader.exe", map[string]bool{"foxit.foxitreader": false}, true},
		{"pacote de outra task: não baixa", "winget:microsoftwindowsappruntime14", "winget-microsoftwindowsappruntime14.exe", map[string]bool{}, false},
		{"updater do agent: sempre baixa", "selfupdate:abc", "selfupdate-abc.exe", map[string]bool{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := artifactFetchDecision(tc.id, tc.file, known, finalFor(tc.final))
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestArtifactRetentionDecision(t *testing.T) {
	known := map[string]string{"foxitfoxitreader": "foxit.foxitreader"}
	final := func(string) bool { return true }
	if !artifactRetentionDecision("name:winget-foxitfoxitreader.exe", "winget-foxitfoxitreader.exe", known, final) {
		t.Fatal("instalador de pacote instalado deve ser removido do cache")
	}
	if artifactRetentionDecision("winget:microsoftwindowsappruntime14", "winget-microsoftwindowsappruntime14.exe", known, final) {
		t.Fatal("artifact de pacote desconhecido não deve ser removido")
	}
	if artifactRetentionDecision("selfupdate:abc", "selfupdate-abc.exe", known, final) {
		t.Fatal("artifact do updater não deve ser removido")
	}
	notFinal := func(string) bool { return false }
	if artifactRetentionDecision("name:winget-foxitfoxitreader.exe", "winget-foxitfoxitreader.exe", known, notFinal) {
		t.Fatal("pacote pendente não deve ser removido do cache")
	}
}

func TestPackageInFinalStateCached_CachesDecision(t *testing.T) {
	t.Cleanup(func() { packageFinalStateCache.entries = map[string]packageFinalStateEntry{} })
	packageFinalStateCache.entries = map[string]packageFinalStateEntry{}
	now := time.Now()
	calls := 0
	compute := func() bool { calls++; return true }

	if !packageInFinalStateCached(now, "brave.brave", compute) {
		t.Fatal("esperava true (instalado)")
	}
	if got := packageInFinalStateCached(now.Add(2*time.Minute), "brave.brave", compute); !got || calls != 1 {
		t.Fatalf("cache deveria atender: final=%v calls=%d", got, calls)
	}
	if got := packageInFinalStateCached(now.Add(4*time.Minute), "brave.brave", compute); !got || calls != 2 {
		t.Fatalf("TTL expirado deveria recomputar: final=%v calls=%d", got, calls)
	}
}

func TestPackageInFinalStateCached_DoesNotCacheEmptyPackage(t *testing.T) {
	t.Cleanup(func() { packageFinalStateCache.entries = map[string]packageFinalStateEntry{} })
	packageFinalStateCache.entries = map[string]packageFinalStateEntry{}
	now := time.Now()
	if packageInFinalStateCached(now, "  ", func() bool { t.Fatal("não deveria chamar compute"); return true }) {
		t.Fatal("pacote vazio é falso")
	}
	if len(packageFinalStateCache.entries) != 0 {
		t.Fatalf("pacote vazio não deve entrar no cache: %v", packageFinalStateCache.entries)
	}
}
