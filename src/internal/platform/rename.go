package platform

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	renameMaxRetries    = 5
	renameRetryBaseWait = 100 * time.Millisecond
)

// RenameAtomic tenta renomear oldPath para newPath com retry e fallback.
//
// No Windows, os.Rename pode falhar se outro processo (AV, indexador de busca)
// abrir o arquivo brevemente entre Close e Rename. Essa função faz até
// renameMaxRetries tentativas com backoff exponencial e, se todas falharem,
// tenta copy + delete como fallback.
func RenameAtomic(oldPath, newPath string) error {
	var lastErr error
	for attempt := 0; attempt < renameMaxRetries; attempt++ {
		lastErr = os.Rename(oldPath, newPath)
		if lastErr == nil {
			return nil
		}
		// Só faz retry se o erro for de arquivo em uso / acesso negado.
		if !isRetryableRenameError(lastErr) {
			return lastErr
		}
		if attempt < renameMaxRetries-1 {
			time.Sleep(renameRetryBaseWait * time.Duration(1<<attempt))
		}
	}

	// Fallback: copy + delete (funciona mesmo se o arquivo estiver com lock
	// de leitura de outro processo).
	if copyErr := copyAndDelete(oldPath, newPath); copyErr != nil {
		return fmt.Errorf("rename falhou (5 tentativas): %w; fallback copy+delete: %w", lastErr, copyErr)
	}
	return nil
}

// isRetryableRenameError verifica se o erro é transiente (arquivo em uso).
func isRetryableRenameError(err error) bool {
	if err == nil {
		return false
	}
	// No Windows, os.Rename falha com "The process cannot access the file
	// because it is being used by another process" quando outro programa
	// (AV, indexador) tem handle aberto.
	if runtime.GOOS == "windows" {
		msg := err.Error()
		// Erros comuns: acesso negado, arquivo em uso, sharing violation.
		if containsAny(msg, []string{
			"being used by another process",
			"acesso negado",
			"cannot access the file",
			"sharing violation",
		}) {
			return true
		}
	}
	return false
}

// copyAndDelete copia src para dst e remove src.
func copyAndDelete(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("fallback copy: open src: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("fallback copy: create dst: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("fallback copy: copy: %w", err)
	}
	if err := dstFile.Sync(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("fallback copy: sync: %w", err)
	}
	dstFile.Close()
	srcFile.Close()

	if err := os.Remove(src); err != nil {
		// Não falha — o dst já está escrito corretamente.
		// O src orfão pode ser limpo depois.
		return nil
	}
	return nil
}

func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
