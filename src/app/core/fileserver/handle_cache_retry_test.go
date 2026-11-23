package fileserver

import (
	"discovery/app/core/testutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAcquireWriteNovoChunk0NaoInutilizaHandleEmUso é a regressão do
// ACCESS_DENIED em upload concorrente no Windows.
//
// Cenário real: um retry recomeça o upload (chunk 0) enquanto o primeiro ainda
// escreve. O recomeço PRECISA invalidar a ENTRADA do cache (para o próximo
// chunk escrever no arquivo recém-truncado), mas NÃO pode fechar o handle que
// outro goroutine está usando — e, principalmente, não pode remover o arquivo
// enquanto ele está aberto: no Windows isso deixa o .tmp em estado
// delete-pending e o O_CREATE seguinte falha com "Access is denied".
func TestAcquireWriteNovoChunk0NaoInutilizaHandleEmUso(t *testing.T) {
	dir := testutil.TempDir(t)
	cache := newHandleCache(8, time.Minute)
	defer cache.Close()

	tmpPath := filepath.Join(dir, "retry.bin.tmp")

	primeiro, err := cache.acquireWrite(tmpPath, true)
	if err != nil {
		t.Fatalf("primeiro chunk 0: %v", err)
	}
	if _, err := primeiro.file.WriteAt([]byte("primeiro"), 0); err != nil {
		t.Fatalf("escrever com o primeiro handle: %v", err)
	}

	// Retry: novo chunk 0 no MESMO caminho, com o primeiro handle ainda vivo.
	segundo, err := cache.acquireWrite(tmpPath, true)
	if err != nil {
		t.Fatalf("recomeço (chunk 0) com handle vivo falhou: %v", err)
	}

	// O handle antigo continua utilizável (não foi fechado por baixo).
	if _, err := primeiro.file.WriteAt([]byte("ainda-vivo"), 0); err != nil {
		t.Fatalf("handle antigo foi inutilizado pelo recomeço: %v", err)
	}

	// O cache tem UMA entrada (a nova): o recomeço substituiu a anterior sem
	// vazar a antiga.
	cache.mu.Lock()
	entradas := len(cache.entries)
	lru := cache.lru.Len()
	cache.mu.Unlock()
	if entradas != 1 || lru != 1 {
		t.Fatalf("cache após recomeço: entries=%d lru=%d, esperado 1/1", entradas, lru)
	}

	// O arquivo existe e o conteúdo é do handle NOVO (o recomeço truncou).
	if err := cache.release(segundo, false); err != nil {
		t.Fatalf("release segundo: %v", err)
	}
	if err := primeiro.file.Close(); err != nil {
		t.Fatalf("fechar handle antigo: %v", err)
	}
	// O cache fecha o que sobrou no defer; aqui só garantimos que o arquivo pode
	// ser removido (no Windows, arquivo aberto impede o remove).
	_ = cache.release(segundo, true)
	if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remover tmp após o recomeço: %v", err)
	}
}
