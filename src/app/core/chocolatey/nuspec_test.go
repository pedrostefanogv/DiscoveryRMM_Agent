package chocolatey

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanInstalledPackages_ReadsNuspecMetadata(t *testing.T) {
	base := t.TempDir()

	gitDir := filepath.Join(base, "lib", "git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitNuspec := `<?xml version="1.0"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">
  <metadata>
    <id>git</id>
    <title>Git</title>
    <version>2.45.1</version>
  </metadata>
</package>`
	if err := os.WriteFile(filepath.Join(gitDir, "git.nuspec"), []byte(gitNuspec), 0o644); err != nil {
		t.Fatal(err)
	}

	// Sem título: deve continuar válido e cair no id.
	zipDir := filepath.Join(base, "lib", "7zip")
	if err := os.MkdirAll(zipDir, 0o755); err != nil {
		t.Fatal(err)
	}
	zipNuspec := `<package><metadata><id>7zip</id><version>24.07</version></metadata></package>`
	if err := os.WriteFile(filepath.Join(zipDir, "7zip.nuspec"), []byte(zipNuspec), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ChocolateyInstall", base)

	items := ScanInstalledPackages()
	byID := map[string]InstalledPackageInfo{}
	for _, item := range items {
		byID[item.ID] = item
	}
	if len(items) != 2 {
		t.Fatalf("esperava 2 itens, veio %d: %+v", len(items), items)
	}
	if byID["git"].Title != "Git" || byID["git"].Version != "2.45.1" {
		t.Fatalf("git = %+v", byID["git"])
	}
	if byID["7zip"].Version != "24.07" {
		t.Fatalf("7zip = %+v", byID["7zip"])
	}
}
