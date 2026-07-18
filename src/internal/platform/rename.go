package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"
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

// isRetryableRenameError verifica se o erro é transiente (arquivo em uso) usando
// o código syscall subjacente no Windows (language-agnostic), com fallback para
// verificação de permissão no Linux/macOS.
func isRetryableRenameError(err error) bool {
	if err == nil {
		return false
	}

	// Windows: verificar pelo código de erro do syscall (independente de locale).
	// ERROR_SHARING_VIOLATION (32): outro processo usa o arquivo.
	// ERROR_LOCK_VIOLATION (33): outro processo bloqueou a região.
	// ERROR_ACCESS_DENIED (5): pode ser AV segurando o handle.
	if runtime.GOOS == "windows" {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			switch errno {
			case 32, 33, 5: // ERROR_SHARING_VIOLATION, ERROR_LOCK_VIOLATION, ERROR_ACCESS_DENIED
				return true
			}
		}
		// Fallback string-based para wrappers que ocultam o Errno (robustez extra).
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "being used by another process") ||
			strings.Contains(msg, "cannot access the file") ||
			strings.Contains(msg, "sharing violation") ||
			strings.Contains(msg, "acesso negado") ||
			strings.Contains(msg, "sendo usado por outro processo") ||
			strings.Contains(msg, "não pode acessar o arquivo") ||
			strings.Contains(msg, "violação de compartilhamento") {
			return true
		}
		return false
	}

	// Linux/macOS: verificar permissão (EACCES, EPERM).
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == syscall.EACCES || errno == syscall.EPERM {
			return true
		}
	}
	// Fallback string-based para Linux.
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "text file busy") || strings.Contains(msg, "permission denied") {
		return true
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
