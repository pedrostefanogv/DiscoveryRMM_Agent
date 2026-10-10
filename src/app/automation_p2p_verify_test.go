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
//
// IMPORTANTE (correção P1.7): quando o winget NÃO dá evidência confiável, a
// verificação devolve false — antes devolvia true e o agente reportava
// "instalação concluída" sem nada instalado (foi o caminho que mascarou o
// caso Brave). O caller cai para `winget install`, que é idempotente quando o
// pacote já está instalado.
func TestLocalInstallVerified(t *testing.T) {
	cases := []struct {
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
		{name: "install: winget list falhou nao assume sucesso", installedErr: errors.New("context canceled"), op: "install", want: false},
		{name: "install: winget list vazio nao assume sucesso", installed: "   ", op: "install", want: false},
		{name: "upgrade: ainda atualizavel precisa fallback", installed: stubInstalledWithBrave, upgradable: stubUpgradableWithBrave, op: "upgrade", want: false},
		{name: "upgrade: atualizado", installed: stubInstalledWithBrave, upgradable: stubUpgradableWithoutBrave, op: "upgrade", want: true},
		{name: "upgrade: lista indisponivel nao assume sucesso", installed: stubInstalledWithBrave, upgradableErr: errors.New("timeout"), op: "upgrade", want: false},
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

// evidenceUnavailable decide quando vale reconsultar o winget antes de concluir.
func TestEvidenceUnavailable(t *testing.T) {
	err := errors.New("0x8a150001")
	cases := []struct {
		name       string
		installed  string
		instErr    error
		upgradable string
		upErr      error
		op         string
		want       bool
	}{
		{name: "install com lista ok", installed: stubInstalledWithBrave, op: "install", want: false},
		{name: "install com erro", installed: stubInstalledWithBrave, instErr: err, op: "install", want: true},
		{name: "install com lista vazia", installed: "  ", op: "install", want: true},
		{name: "upgrade com list ok mas upgrade vazio", installed: stubInstalledWithBrave, op: "upgrade", want: true},
		{name: "upgrade com tudo ok", installed: stubInstalledWithBrave, upgradable: stubUpgradableWithoutBrave, op: "upgrade", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evidenceUnavailable(tc.installed, tc.instErr, tc.upgradable, tc.upErr, tc.op)
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
