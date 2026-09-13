package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── circuit breaker contra loop de self-update (fix homologação 2026-09-13) ──
//
// Cenário real (homologação 2026-09-13): um binário compilado SEM
// -X buildinfo.Version reporta "0.0.0" — o check compara server 1.2.1 >
// current 0.0.0 → baixa → instala com sucesso → reinicia → o binário
// reinstalado TAMBÉM reporta 0.0.0 → baixa de novo… loop infinito de
// download+instalação (~30 MB por ciclo, a cada ~30s).
//
// O maxInstallAttempts não cobre este caso porque a instalação TEM SUCESSO —
// o loop é no CHECK (o buildinfo do processo nunca muda).
//
// Breaker: persiste um contador de instalações lançadas para a MESMA versão
// do servidor. Ao atingir o limite, os checks PAUSAM (com log crítico) até
// que: (a) a versão do servidor mude, ou (b) o buildinfo do processo comece
// a reportar a versão correta (build corrigido + reinstalação manual).
//
// Persistido em arquivo no TempDir — sobrevive aos restarts do processo que
// a cada update o instalador provoca.

const (
	// guardMaxInstallsSameVersion: após N instalações da MESMA versão do
	// servidor sem que o buildinfo do processo mude, pausa os checks.
	guardMaxInstallsSameVersion = 3
	// guardFile é o nome do arquivo de persistência do breaker (no TempDir).
	guardFile = "selfupdate-guard.json"
)

// selfUpdateGuardState é o estado persistido do breaker.
type selfUpdateGuard struct {
	ServerVersion string `json:"serverVersion"`
	Installs      int    `json:"installs"`
	LastAtUTC     string `json:"lastAtUtc"`
}

// loadGuard carrega o estado do breaker (zero se ausente/corrompido).
func (u *Updater) loadGuard() selfUpdateGuard {
	var g selfUpdateGuard
	path := u.guardPath()
	if path == "" {
		return g
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return g
	}
	_ = json.Unmarshal(raw, &g)
	return g
}

// saveGuard persiste o estado do breaker.
func (u *Updater) saveGuard(g selfUpdateGuard) {
	path := u.guardPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// guardPath retorna o caminho do arquivo de guard (vazio se TempDir não definido).
func (u *Updater) guardPath() string {
	dir := strings.TrimSpace(u.TempDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, guardFile)
}

// clearGuardIfResolved limpa o breaker quando a situação se resolveu: a
// versão do servidor mudou, ou o processo passou a reportar a versão correta.
func (u *Updater) clearGuardIfResolved(currentVersion, serverVersion string) {
	g := u.loadGuard()
	if g.ServerVersion == "" {
		return
	}
	if g.ServerVersion != serverVersion || currentVersion == serverVersion {
		_ = os.Remove(u.guardPath())
	}
}

// guardBlocksCheck decide se o check deve ser pausado pelo breaker.
// Chamado no início do check (com current e serverVersion).
func (u *Updater) guardBlocksCheck(currentVersion, serverVersion string) (bool, string) {
	if strings.TrimSpace(serverVersion) == "" || strings.EqualFold(currentVersion, serverVersion) {
		return false, ""
	}
	g := u.loadGuard()
	// Servidor mudou a versão ofertada → recomeça a contagem.
	if g.ServerVersion != serverVersion {
		return false, ""
	}
	if g.Installs >= guardMaxInstallsSameVersion {
		msg := fmt.Sprintf("circuit breaker: %d instalações da versão %s já foram lançadas e o processo continua reportando buildinfo.Version=%q — o binário instalado não tem a versão injetada (build sem -X buildinfo). Checks pausados; reinstale com um build que injete a versão.", g.Installs, serverVersion, currentVersion)
		return true, msg
	}
	return false, ""
}

// guardRecordInstall incrementa o contador de instalações lançadas para a
// versão do servidor (chamado quando o instalador é lançado com sucesso).
func (u *Updater) guardRecordInstall(serverVersion string) {
	if strings.TrimSpace(serverVersion) == "" {
		return
	}
	g := u.loadGuard()
	if g.ServerVersion != serverVersion {
		g = selfUpdateGuard{ServerVersion: serverVersion}
	}
	g.Installs++
	g.LastAtUTC = time.Now().UTC().Format(time.RFC3339)
	u.saveGuard(g)
}