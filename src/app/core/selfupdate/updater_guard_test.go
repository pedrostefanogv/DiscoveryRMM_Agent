package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGuardBlocksCheckAfterMaxInstalls valida o circuit breaker anti-loop
// (fix homologação 2026-09-13): 3 instalações da MESMA versão do servidor
// com o processo continuando a reportar 0.0.0 → checks pausados.
func TestGuardBlocksCheck_AfterMaxInstalls(t *testing.T) {
	dir := t.TempDir()
	u := &Updater{TempDir: dir, Logf: func(string, ...any) {}}

	// Nenhuma instalação ainda → não bloqueia.
	if blocked, _ := u.guardBlocksCheck("0.0.0", "1.2.1"); blocked {
		t.Fatal("guard não deve bloquear sem instalações registradas")
	}

	// 1ª e 2ª instalação → ainda não bloqueia.
	u.guardRecordInstall("1.2.1")
	u.guardRecordInstall("1.2.1")
	if blocked, _ := u.guardBlocksCheck("0.0.0", "1.2.1"); blocked {
		t.Fatal("guard não deve bloquear com menos de 3 instalações")
	}

	// 3ª instalação → bloqueia.
	u.guardRecordInstall("1.2.1")
	blocked, msg := u.guardBlocksCheck("0.0.0", "1.2.1")
	if !blocked {
		t.Fatal("guard deve bloquear após 3 instalações da mesma versão")
	}
	if msg == "" {
		t.Fatal("mensagem do breaker não pode ser vazia")
	}

	// Guard persistido em arquivo (sobrevive a restarts do processo).
	if _, err := os.Stat(filepath.Join(dir, guardFile)); err != nil {
		t.Fatalf("guard file não persistido: %v", err)
	}
}

// TestGuard_ServerVersionChangedResets valida que a versão do servidor
// mudando recomeça a contagem do breaker.
func TestGuard_ServerVersionChangedResets(t *testing.T) {
	u := &Updater{TempDir: t.TempDir()}
	u.guardRecordInstall("1.2.1")
	u.guardRecordInstall("1.2.1")
	u.guardRecordInstall("1.2.1")

	// Servidor passou a oferecer 1.3.0 → contador recomeça (não bloqueia).
	if blocked, _ := u.guardBlocksCheck("0.0.0", "1.3.0"); blocked {
		t.Fatal("mudança na versão do servidor deve recomeçar a contagem")
	}
}

// TestClearGuardIfResolved valida a limpeza do breaker quando a situação
// se resolve (processo passou a reportar a versão correta).
func TestClearGuardIfResolved(t *testing.T) {
	dir := t.TempDir()
	u := &Updater{TempDir: dir}
	u.guardRecordInstall("1.2.1")
	u.guardRecordInstall("1.2.1")

	// Processo ainda 0.0.0 e servidor ainda 1.2.1 → NÃO limpa.
	u.clearGuardIfResolved("0.0.0", "1.2.1")
	if _, err := os.Stat(filepath.Join(dir, guardFile)); err != nil {
		t.Fatal("guard não deve ser limpo enquanto o problema persiste")
	}

	// Processo passou a reportar a versão correta → limpa.
	u.clearGuardIfResolved("1.2.1", "1.2.1")
	if _, err := os.Stat(filepath.Join(dir, guardFile)); !os.IsNotExist(err) {
		t.Fatal("guard deve ser limpo quando o buildinfo passa a reportar a versão correta")
	}
}