package app

import (
	"context"
	"errors"
	"testing"
	"time"
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
		{name: "upgrade: erro do winget nao assume sucesso", installed: stubInstalledWithBrave, upgradableErr: errors.New("timeout"), op: "upgrade", want: false},
		// `winget upgrade` sem pendências legitimamente não lista nada: lista
		// VAZIA com exit 0 é evidência válida de "nada a atualizar" e não pode
		// forçar um subprocesso winget extra em toda execução.
		{name: "upgrade: lista vazia e valida como nada a atualizar", installed: stubInstalledWithBrave, upgradable: "", op: "upgrade", want: true},
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

// O orçamento total da execução direta (localInstallerExeBudget, aplicado por
// runLocalInstallerFull) depende de o deadline do ctx PAI limitar cada
// tentativa: executeHiddenProcess faz context.WithTimeout(parent, timeout),
// então prevalece min(orçamento, timeout da tentativa). Sem isso, o stub do
// Brave (que não retorna) consumiria 5 min por conjunto de flags e o fallback
// `winget install` só rodaria dezenas de minutos depois.
func TestExecuteHiddenProcessHonorsParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	started := time.Now()
	_, err := executeHiddenProcess(ctx, 10*time.Minute, "cmd", []string{"/c", "ping -n 30 127.0.0.1 > nul"})
	if err == nil {
		t.Fatal("esperava erro: o deadline do ctx pai precisa cancelar a tentativa")
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("deadline do ctx pai ignorado: a tentativa levou %s", elapsed)
	}
}

// Sem sessão interativa não existe caminho para um pacote user-scope: o erro
// tem de ser EXPLÍCITO (e nada pode ser executado) em vez de um fallback
// silencioso que reporta sucesso sem instalar.
func TestInstallInUserSessionWithoutInteractiveSession(t *testing.T) {
	orig := activeUserTokenFn
	defer func() { activeUserTokenFn = orig }()
	activeUserTokenFn = func(ctx context.Context) (context.Context, func(), bool) {
		return ctx, func() {}, false
	}
	m := &automationPackageManagerRouter{logf: func(string, ...any) {}}
	if _, err := m.installInUserSession(context.Background(), "brave.brave", "", ""); err == nil {
		t.Fatal("esperado erro explicito quando nao ha sessao interativa ativa")
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
		{name: "upgrade com lista vazia (sem erro) nao e indisponibilidade", installed: stubInstalledWithBrave, op: "upgrade", want: false},
		{name: "upgrade com erro do winget e indisponibilidade", installed: stubInstalledWithBrave, upErr: err, op: "upgrade", want: true},
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
