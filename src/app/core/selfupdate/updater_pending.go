package selfupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"discovery/app/core/errutil"
)

const (
	// cleanupOldDownloadsAge é a idade máxima de arquivos .exe baixados na
	// pasta de updates. Arquivos mais antigos são removidos no startup para
	// evitar acúmulo de instaladores (~30MB cada). Reduzido para 6h para
	// evitar acúmulo rápido durante loops de update.
	cleanupOldDownloadsAge = 6 * time.Hour
)

func (u *Updater) pendingInstallStatePath() string {
	if strings.TrimSpace(u.TempDir) == "" {
		return ""
	}
	return filepath.Join(u.TempDir, pendingInstallFile)
}

func (u *Updater) persistPendingInstallState(state pendingInstallState) error {
	path := u.pendingInstallStatePath()
	if path == "" {
		return errors.New("diretorio temporario de update nao configurado")
	}

	// Carrega estado existente para incrementar contador de tentativas.
	// Previne loop infinito quando buildinfo.Version nao reflete a versao real
	// (ex.: ldflags -X nao injetados no build, fica sempre "0.0.0").
	if existing, err := u.loadPendingInstallState(); err == nil {
		if existing.TargetVersion == state.TargetVersion && existing.CurrentVersion == state.CurrentVersion {
			state.InstallAttempts = existing.InstallAttempts + 1
			u.logf("estado pendente de install: tentativa %d/%d para target=%s current=%s",
				state.InstallAttempts, maxInstallAttempts, state.TargetVersion, state.CurrentVersion)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

func (u *Updater) loadPendingInstallState() (pendingInstallState, error) {
	path := u.pendingInstallStatePath()
	if path == "" {
		return pendingInstallState{}, os.ErrNotExist
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return pendingInstallState{}, err
	}
	var state pendingInstallState
	if err := json.Unmarshal(body, &state); err != nil {
		return pendingInstallState{}, err
	}
	return state, nil
}

func (u *Updater) clearPendingInstallState() {
	path := u.pendingInstallStatePath()
	if path == "" {
		return
	}
	u.installing.Store(false) // libera trava de concorrência
	errutil.LogIfErr(os.Remove(path), "selfupdate: limpar estado de instalacao pendente")
}

// cleanupOldDownloads remove arquivos .exe baixados há mais de cleanupOldDownloadsAge.
// Evita acúmulo de instaladores (~30MB cada) na pasta de updates.
// É chamado no startup pelo ResumePendingInstallReport.
func (u *Updater) cleanupOldDownloads() {
	if strings.TrimSpace(u.TempDir) == "" {
		return
	}

	entries, err := os.ReadDir(u.TempDir)
	if err != nil {
		if !os.IsNotExist(err) {
			u.logf("aviso: nao foi possivel ler pasta de updates para limpeza: %v", err)
		}
		return
	}

	cutoff := time.Now().Add(-cleanupOldDownloadsAge)
	removed := 0
	removedBytes := int64(0)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Limpa arquivos de download residuais (tanto legado discovery-update-*.exe
		// quanto o novo padrão canônico selfupdate-<sha256>.exe).
		// Não remove pending-install.json (gerenciado separadamente).
		isLegacy := strings.HasPrefix(name, "discovery-update-") && strings.HasSuffix(name, ".exe")
		isCanonical := strings.HasPrefix(name, "selfupdate-") && strings.HasSuffix(name, ".exe")
		if !isLegacy && !isCanonical {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			fullPath := filepath.Join(u.TempDir, name)
			if err := os.Remove(fullPath); err != nil {
				continue
			}
			removed++
			removedBytes += info.Size()
		}
	}

	if removed > 0 {
		u.logf("limpeza de updates antigos: %d arquivo(s) removido(s) (%.1f MB liberados)",
			removed, float64(removedBytes)/(1024*1024))
	}
}
