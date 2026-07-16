package selfupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"discovery/internal/errutil"
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
	errutil.LogIfErr(os.Remove(path), "selfupdate: limpar estado de instalacao pendente")
}
