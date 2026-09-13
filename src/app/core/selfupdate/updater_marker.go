package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── Marker do último install confirmado (fix do loop de self-update) ──────
//
// Contrato do dono: o agente compara version E commit com o servidor —
// versão diferente → update; commit diferente (mesmo com versão igual) →
// update; ambos iguais → skip.
//
// O problema: a comparação usa o buildinfo do processo, e um binário
// compilado sem -X buildinfo reporta "0.0.0"/"unknown" PARA SEMPRE — aí o
// check sempre vê "servidor mais novo" e reinstala em loop (homologação
// 2026-09-13: 5 installs em ~1min).
//
// Solução: ao confirmar que um install foi CONCLUÍDO (installer.log / versão /
// commit), gravamos um MARKER persistido com a versão/commit que estão
// deployados. Quando o buildinfo não é confiável (0.0.0/unknown), o marker
// vira a versão/commit EFETIVOS para a comparação — o loop quebra porque o
// servidor passa a oferecer o MESMO build que o marker já registra.

// installedMarkerFile é o nome do marker (no TempDir, junto do pending state).
const installedMarkerFile = "selfupdate-installed.json"

type installedMarker struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	RecordedAtUTC string `json:"recordedAtUtc"`
}

func (u *Updater) markerPath() string {
	dir := strings.TrimSpace(u.TempDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, installedMarkerFile)
}

// saveInstalledMarker grava o último install confirmado.
func (u *Updater) saveInstalledMarker(version, commit string) {
	path := u.markerPath()
	if path == "" || strings.TrimSpace(version) == "" {
		return
	}
	m := installedMarker{
		Version:       strings.TrimSpace(version),
		Commit:        strings.TrimSpace(commit), // pode ser "unknown" (fallback sem /version)
		RecordedAtUTC: time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// loadInstalledMarker carrega o marker (false se ausente/corrompido/vazio).
func (u *Updater) loadInstalledMarker() (installedMarker, bool) {
	var m installedMarker
	path := u.markerPath()
	if path == "" {
		return m, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return m, false
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, false
	}
	if strings.TrimSpace(m.Version) == "" {
		return m, false
	}
	return m, true
}

// clearInstalledMarker remove o marker (uso em testes).
func (u *Updater) clearInstalledMarker() {
	if path := u.markerPath(); path != "" {
		_ = os.Remove(path)
	}
}

// effectiveCurrent resolve a versão/commit EFETIVOS do processo para a
// comparação com o servidor:
//   - buildinfo confiável (≠ "0.0.0") → usa o buildinfo;
//   - buildinfo "0.0.0"/vazio (build sem -X buildinfo) → usa o marker do
//     último install confirmado, que representa o que está deployado.
func (u *Updater) effectiveCurrent(buildVersion, buildCommit string) (string, string) {
	bv := strings.TrimSpace(buildVersion)
	if bv != "" && bv != "0.0.0" {
		return bv, strings.TrimSpace(buildCommit)
	}
	if m, ok := u.loadInstalledMarker(); ok {
		return m.Version, m.Commit
	}
	return bv, strings.TrimSpace(buildCommit)
}

// sameBuildInstalled aplica o contrato do dono: o servidor está oferecendo o
// MESMO build que o agente já tem (versão igual E commit igual, quando
// comparáveis) → skip. Caso contrário → update.
//   - versão diferente                    → update;
//   - versão igual + commit diferente     → update (rebuild);
//   - versão igual + commits incomparáveis → skip (proteção anti-loop: o
//     install via fallback sem /version não tem commit conhecido).
func sameBuildInstalled(serverVersion, serverCommit, effVersion, effCommit string) bool {
	if compareVersions(serverVersion, effVersion) != 0 {
		return false
	}
	if strings.TrimSpace(serverCommit) == "" || strings.EqualFold(serverCommit, "unknown") {
		return true // servidor sem commit: versão igual é o melhor sinal
	}
	if strings.TrimSpace(effCommit) == "" || strings.EqualFold(effCommit, "unknown") {
		return true // eff sem commit (install via fallback): evita loop
	}
	return strings.EqualFold(effCommit, serverCommit)
}
