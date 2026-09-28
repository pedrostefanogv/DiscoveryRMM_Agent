package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"discovery/app/core/automation"
)

// Cache curto das decisões de estado final por pacote: os gates de fetch/re-seed
// e de retenção do P2P_Temp são consultados a cada ciclo (re-seed a cada 2 min)
// para CADA artifact anunciado, e cada consulta executa winget list + winget
// upgrade. Sem cache, um site com vários artifacts anunciados dispararia dezenas
// de processos winget por ciclo. TTL curto: decisões de instalação continuam
// usando estado ao vivo.
const packageFinalStateTTL = 3 * time.Minute

type packageFinalStateEntry struct {
	final bool
	at    time.Time
}

var packageFinalStateCache = struct {
	sync.Mutex
	entries map[string]packageFinalStateEntry
}{entries: map[string]packageFinalStateEntry{}}

// wingetPackageIDFromArtifact extrai o packageId de um artifactID "winget:<id>".
// Retorna vazio quando o artifact não é mapeável (selfupdate:, name:, etc.).
func wingetPackageIDFromArtifact(artifactID string) string {
	trimmed := strings.TrimSpace(artifactID)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "winget:") {
		return ""
	}
	// Strip case-insensitivo do prefixo + trim (id pode ter espaços à direita).
	return strings.TrimSpace(trimmed[len("winget:"):])
}

// packageInFinalStateCached devolve (com cache de TTL curto) se o pacote está em
// estado final: instalado / sem update pendente — não precisa mais do instalador.
func packageInFinalStateCached(now time.Time, packageID string, compute func() bool) bool {
	key := strings.ToLower(strings.TrimSpace(packageID))
	if key == "" {
		return false
	}

	packageFinalStateCache.Lock()
	if entry, ok := packageFinalStateCache.entries[key]; ok && now.Sub(entry.at) < packageFinalStateTTL {
		packageFinalStateCache.Unlock()
		return entry.final
	}
	packageFinalStateCache.Unlock()

	final := compute()

	packageFinalStateCache.Lock()
	packageFinalStateCache.entries[key] = packageFinalStateEntry{final: final, at: now}
	packageFinalStateCache.Unlock()
	return final
}

// packageInFinalState usa o estado real do winget (ShouldPreloadPackage:
// true = NÃO está instalado OU há update pendente) para decidir se o
// instalador ainda é necessário.
func (a *App) packageInFinalState(packageID string) bool {
	return packageInFinalStateCached(time.Now(), packageID, func() bool {
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		return !automation.ShouldPreloadPackage(ctx, a.packageManagerRouter,
			automation.ActionUpdateOrInstallPackage, packageID)
	})
}
