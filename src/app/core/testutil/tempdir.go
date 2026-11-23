// Package testutil reúne utilitários usados pelos testes do agente.
//
// Importado apenas por arquivos _test.go (nunca entra no binário de produção).
package testutil

import (
	"os"
	"time"
)

// TB é o subconjunto de *testing.T que o helper precisa (também aceita
// *testing.B por compatibilidade futura).
type TB interface {
	Helper()
	TempDir() string
	Cleanup(func())
}

// TempDir devolve um diretório temporário do teste (como t.TempDir) e registra
// um cleanup TOLERANTE para removê-lo.
//
// Motivo: no Windows o RemoveAll interno do testing.T.TempDir falha de forma
// transitória com "The directory is not empty" quando antivírus/indexador ainda
// segura um arquivo recém-escrito (SQLite deixa -wal/-shm; o serviço de config
// deixa .tmp-*; o watcher de config pode reabrir o arquivo logo após o teste).
// O cleanup registrado aqui roda ANTES do cleanup interno (cleanups do testing
// são LIFO) e remove com retry; quando ele consegue, o cleanup interno não
// encontra nada e não falha o teste.
func TempDir(t TB) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { _ = RemoveAllWithRetry(dir) })
	return dir
}

// RemoveAllWithRetry remove o diretório tolerando bloqueios transitórios.
// 5 tentativas com espera crescente (50ms..250ms): cobre a janela de scan do
// antivírus sem travar a suíte.
func RemoveAllWithRetry(dir string) error {
	var err error
	for attempt := 1; attempt <= 5; attempt++ {
		err = os.RemoveAll(dir)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
	}
	return err
}
