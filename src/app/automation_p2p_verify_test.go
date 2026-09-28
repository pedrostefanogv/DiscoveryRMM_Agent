package app

import (
	"errors"
	"testing"
)

// Tabelas simuladas do "winget list"/"winget upgrade" contendo o Brave.
const stubInstalledWithBrave = "Google Chrome  Google.Chrome.EXE  153.0\nBrave  brave.brave  1.66\n"
const stubUpgradableWithBrave = "Brave  brave.brave  1.66  1.67\n"
const stubInstalledWithoutBrave = "Google Chrome  Google.Chrome.EXE  153.0\nFoxit  Foxit.FoxitReader  2026.2\n"
const stubUpgradableWithoutBrave = "Google Chrome  Google.Chrome.EXE  153.0  154.0\n"

// O instalador local (cache P2P) pode sair com exit 0 sem instalar: stub/online
// (BraveSilentSetup instala por usuário) fica travado como SYSTEM. Nesses casos
// o router deve cair para o winget install (escopo machine, elevado).
func TestLocalInstallVerified(t *testing.T) {
	cases := []struct{
		name          string
		installed     string
		installedErr  error
		upgradable    string
		upgradableErr error
		op            string
		want          bool
	}{
		{name: "install: pacote instalado", installed: stubInstalledWithBrave, op: "install", want: true},
		{name: "install: pacote ausente precisa fallback", installed: stubInstalledWithoutBrave, op: "install", want: false},
		{name: "install: match case-insensitive", installed: "Brave  Brave.Brave  1.66\n", op: "install", want: true},
		{name: "install: winget list falhou nao força fallback", installedErr: errors.New("context canceled"), op: "install", want: true},
		{name: "upgrade: ainda atualizavel precisa fallback", installed: stubInstalledWithBrave, upgradable: stubUpgradableWithBrave, op: "upgrade", want: false},
		{name: "upgrade: atualizado", installed: stubInstalledWithBrave, upgradable: stubUpgradableWithoutBrave, op: "upgrade", want: true},
		{name: "upgrade: lista indisponivel assume sucesso", installed: stubInstalledWithBrave, upgradableErr: errors.New("timeout"), op: "upgrade", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := localInstallVerified("brave.brave", tc.installed, tc.installedErr, tc.upgradable, tc.upgradableErr, tc.op)
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// O caso real do Brave: instalador stub saiu/travou sem instalar machine-scope.
func TestLocalInstallVerified_BraveStubDoesNotInstall(t *testing.T) {
	if localInstallVerified("brave.brave", stubInstalledWithoutBrave, nil, "", nil, "install") {
		t.Fatalf("brave ausente deveria exigir fallback para winget")
	}
}
