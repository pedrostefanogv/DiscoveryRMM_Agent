package winget

import (
	"strings"
	"testing"
)

// `--scope machine` exige instalador de escopo de máquina. Pacotes cujo
// manifesto só oferece instalador USER-scope (ex.: Brave.Brave, `Scope: user`)
// falham com ele — e nenhuma tentativa como SYSTEM consegue instalar para o
// usuário. O caminho "como usuário" precisa OMITIR a flag para o winget
// escolher o instalador aplicável do manifesto.
func TestBuildInstallArgsScope(t *testing.T) {
	machine, err := buildInstallArgs("Brave.Brave", "/silent /install", "", "machine")
	if err != nil {
		t.Fatalf("escopo machine: %v", err)
	}
	if !strings.Contains(strings.Join(machine, " "), "--scope machine") {
		t.Fatalf("escopo machine ausente: %v", machine)
	}
	assertHasAcceptFlags(t, "machine", machine)

	user, err := buildInstallArgs("Brave.Brave", "/silent /install", "", "")
	if err != nil {
		t.Fatalf("escopo usuario: %v", err)
	}
	if strings.Contains(strings.Join(user, " "), "--scope") {
		t.Fatalf("escopo deveria ser OMITIDO no caminho do usuario: %v", user)
	}
	// Switches exe-only do catálogo NÃO vão por --custom (quebrariam msiexec);
	// nesse caso o winget usa os switches do próprio manifesto.
	if strings.Contains(strings.Join(user, " "), "--custom") {
		t.Fatalf("switch exe-only nao deveria ir por --custom: %v", user)
	}
	assertHasAcceptFlags(t, "usuario", user)

	// Propriedade de MSI (KEY=VALUE) continua indo por --custom.
	msi, err := buildInstallArgs("7zip.7zip", "INSTALLDIR=C:\\x", "", "")
	if err != nil {
		t.Fatalf("msi: %v", err)
	}
	if !strings.Contains(strings.Join(msi, " "), "--custom") {
		t.Fatalf("propriedade KEY=VALUE deveria ir por --custom: %v", msi)
	}

	if _, err := buildInstallArgs("", "", "", "machine"); err == nil {
		t.Fatal("id vazio deveria ser rejeitado")
	}
}
