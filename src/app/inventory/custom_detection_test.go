package inventory

import (
	"testing"

	"discovery/app/appstore"
	"discovery/app/core/models"
)

func TestNormalizeDetectionText(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"  Foxit PDF Reader  ", "foxit pdf reader"},
		{"Aplicação Móvel Sênior", "aplicacao movel senior"},
		{"MICROSOFT®  Visual   C++", "microsoft® visual c++"},
		{"Ação", "acao"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := normalizeDetectionText(tc.in); got != tc.want {
			t.Fatalf("normalizeDetectionText(%q) = %q, esperado %q", tc.in, got, tc.want)
		}
	}
}

func TestMatchSoftwareByName(t *testing.T) {
	if !matchSoftwareByName("aplicacao movel", "Aplicação  Móvel") {
		t.Fatal("nome normalizado (sem acento, espacos colapsados) deveria casar")
	}
	if !matchSoftwareByName("Google Chrome", "google chrome") {
		t.Fatal("comparacao deveria ser case-insensitive")
	}
	if matchSoftwareByName("Google Chrome", "Google Chrome Beta") {
		t.Fatal("nomes diferentes nao podem casar")
	}
	if matchSoftwareByName("", "qualquer") {
		t.Fatal("nome de deteccao vazio nao casa")
	}
}

func TestMatchSoftwarePublisher(t *testing.T) {
	if !matchSoftwarePublisher("", "qualquer editora") {
		t.Fatal("fabricante vazio na configuracao deve aceitar qualquer publisher")
	}
	if !matchSoftwarePublisher("contoso", "Contoso Ltda") {
		t.Fatal("publisher que contem o texto configurado deveria casar")
	}
	if matchSoftwarePublisher("contoso", "Fabrikam") {
		t.Fatal("publisher diferente nao pode casar")
	}
	if matchSoftwarePublisher("contoso", "") {
		t.Fatal("sem publisher no inventario nao ha evidencia para casar")
	}
}

func TestCompareVersionsLoose(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"2.4.0", "2.0.0", 1, true},
		{"1.9.0", "2.0.0", -1, true},
		{"2.0.0", "2.0.0", 0, true},
		{"1.2.10", "1.2.9", 1, true},   // comparacao numerica, nao lexicografica
		{"2026.2", "2026.1.9", 1, true},
		{"v1.2.3", "1.2.3", 0, true},   // prefixo textual ignorado
		{"1.2.3-beta", "1.2.3", 0, true},
		{"3.0", "3.0.0", 0, true},      // componente ausente = 0
		{"", "1.0", 0, false},          // sem numero de um lado
		{"abc", "def", 0, false},
	}
	for _, tc := range cases {
		got, ok := compareVersionsLoose(tc.a, tc.b)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("compareVersionsLoose(%q,%q) = (%d,%v), esperado (%d,%v)", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMatchSoftwareMinVersion(t *testing.T) {
	if !matchSoftwareMinVersion("", "1.0") {
		t.Fatal("sem versao minima qualquer versao instalada serve")
	}
	if !matchSoftwareMinVersion("2.0.0", "2.4.0") {
		t.Fatal("versao instalada maior deveria atender")
	}
	if matchSoftwareMinVersion("2.0.0", "1.9.9") {
		t.Fatal("versao instalada menor nao atende")
	}
	if matchSoftwareMinVersion("2.0.0", "") {
		t.Fatal("sem versao instalada nao ha evidencia de que atende")
	}
	if !matchSoftwareMinVersion("2.0.0", "desconhecida") {
		t.Fatal("versao nao comparavel deve ser fail-safe (atende)")
	}
}

func TestFindInstalledSoftware(t *testing.T) {
	software := []models.SoftwareItem{
		{Name: "Aplicação Sênior", Version: "1.0.0", Publisher: "Acme"},
		{Name: "Outro App", Version: "9.9.9"},
	}

	installed, detail := findInstalledSoftware(software, "aplicacao senior", "", "")
	if !installed || detail == "" {
		t.Fatalf("deveria encontrar o app normalizado, veio (%v,%q)", installed, detail)
	}

	installed, _ = findInstalledSoftware(software, "Aplicação Sênior", "", "2.0.0")
	if installed {
		t.Fatal("versao 1.0.0 nao atende a minima 2.0.0")
	}

	installed, _ = findInstalledSoftware(software, "inexistente", "", "")
	if installed {
		t.Fatal("app ausente nao pode ser considerado instalado")
	}
}

func TestIsCustomInventoryLinkPackage(t *testing.T) {
	cases := []struct {
		pkg  models.AppItem
		want bool
	}{
		{models.AppItem{InstallationType: "Custom", DetectionMode: appstore.DetectionModeInventoryLink, DetectionName: "X"}, true},
		{models.AppItem{InstallationType: "custom", DetectionMode: appstore.DetectionModeInventoryLink, DetectionName: "X"}, true},
		{models.AppItem{InstallationType: "Custom", DetectionMode: appstore.DetectionModeInventoryLink}, false},
		{models.AppItem{InstallationType: "Custom", DetectionMode: appstore.DetectionModeCommand, DetectionName: "X"}, false},
		{models.AppItem{InstallationType: "Winget", DetectionMode: appstore.DetectionModeInventoryLink, DetectionName: "X"}, false},
	}
	for i, tc := range cases {
		if got := isCustomInventoryLinkPackage(tc.pkg); got != tc.want {
			t.Fatalf("caso %d: isCustomInventoryLinkPackage = %v, esperado %v", i, got, tc.want)
		}
	}
}

func TestTruncateDetectionDetail(t *testing.T) {
	if got := truncateDetectionDetail("curto"); got != "curto" {
		t.Fatalf("texto curto alterado: %q", got)
	}
	long := make([]byte, 400)
	for i := range long {
		long[i] = 'x'
	}
	got := truncateDetectionDetail(string(long))
	if len(got) >= len(long) {
		t.Fatalf("texto longo deveria ser truncado, veio com %d bytes", len(got))
	}
}
