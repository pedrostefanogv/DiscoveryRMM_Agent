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

// packageKeyFromArtifactName normaliza o nome de arquivo canônico do artifact
// ("winget-foxitfoxitreader.exe") para a chave de lookup do pacote
// ("foxitfoxitreader").
func packageKeyFromArtifactName(name string) string {
	base := strings.TrimSpace(name)
	if base == "" {
		return ""
	}
	// remove extensão (.exe/.msi/.json/...)
	if idx := strings.LastIndex(base, "."); idx > 0 {
		base = base[:idx]
	}
	base = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(base)), "winget-")
	return normalizePackageLookupKey(base)
}

// artifactPackageKey extrai a chave normalizada do pacote de um artifactID:
//
//	"winget:googlechromeexe"            -> "googlechromeexe"
//	"name:winget-foxitfoxitreader.exe"  -> "foxitfoxitreader"
//	"winget-foxitfoxitreader.exe"       -> "foxitfoxitreader"
//
// Retorna vazio quando não há como mapear.
func artifactPackageKey(artifactID, artifactName string) string {
	id := strings.TrimSpace(artifactID)
	lower := strings.ToLower(id)
	if strings.HasPrefix(lower, "winget:") {
		return normalizePackageLookupKey(id[len("winget:"):])
	}
	if strings.HasPrefix(lower, "name:") {
		return packageKeyFromArtifactName(artifactName)
	}
	if strings.HasPrefix(lower, "selfupdate:") {
		return normalizePackageLookupKey(id[len("selfupdate:"):])
	}
	return packageKeyFromArtifactName(artifactName)
}

// isAgentUpdaterArtifact identifica artifacts do próprio updater do agent
// (nunca devem ser bloqueados pela política de pacotes).
func isAgentUpdaterArtifact(artifactID, artifactName string) bool {
	id := strings.ToLower(strings.TrimSpace(artifactID))
	name := strings.ToLower(strings.TrimSpace(artifactName))
	return strings.HasPrefix(id, "selfupdate:") || strings.HasPrefix(name, "selfupdate-")
}

// artifactFetchDecision decide se vale baixar um artifact anunciado pela rede.
//   - updater do agent: sempre;
//   - artifact que NÃO pertence a nenhuma task ativa: não baixa (baixar só para
//     "virar seed" desperdiça banda/disco — ex.: pacotes de outros escopos);
//   - pacote em estado final (instalado / sem update pendente): não baixa.
func artifactFetchDecision(
	artifactID, artifactName string,
	knownPackages map[string]string,
	isFinal func(packageID string) bool,
) bool {
	if isAgentUpdaterArtifact(artifactID, artifactName) {
		return true
	}
	key := artifactPackageKey(artifactID, artifactName)
	if key == "" {
		return false
	}
	packageID := strings.TrimSpace(knownPackages[key])
	if packageID == "" {
		// Não é de nenhuma task ativa deste agent.
		return false
	}
	return !isFinal(packageID)
}

// artifactRetentionDecision decide se o artifact pode ser REMOVIDO do cache
// local. Só remove quando mapeia para uma task ativa cujo pacote já está em
// estado final; artifacts desconhecidos e do updater são preservados.
func artifactRetentionDecision(
	artifactID, artifactName string,
	knownPackages map[string]string,
	isFinal func(packageID string) bool,
) bool {
	if isAgentUpdaterArtifact(artifactID, artifactName) {
		return false
	}
	key := artifactPackageKey(artifactID, artifactName)
	if key == "" {
		return false
	}
	packageID := strings.TrimSpace(knownPackages[key])
	if packageID == "" {
		return false
	}
	return isFinal(packageID)
}

// knownTaskPackagesByKey mapeia chave normalizada -> PackageId REAL (com pontos)
// das tasks ativas. É o que corrige o caso "winget:googlechromeexe" x
// "Google.Chrome.EXE": sem o ID real, o winget não reconhece o pacote e a
// decisão de estado final falha.
func (a *App) knownTaskPackagesByKey() map[string]string {
	out := map[string]string{}
	if a == nil || a.AutomationSvc == nil {
		return out
	}
	for _, task := range a.AutomationSvc.GetState().Tasks {
		packageID := strings.TrimSpace(task.PackageID)
		if packageID == "" {
			continue
		}
		key := normalizePackageLookupKey(packageID)
		if key == "" {
			continue
		}
		if _, exists := out[key]; !exists {
			out[key] = packageID
		}
	}
	return out
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

// artifactFetchUseful é o gate injetado no P2P (fetch/re-seed).
func (a *App) artifactFetchUseful(artifactID, artifactName string) bool {
	return artifactFetchDecision(artifactID, artifactName, a.knownTaskPackagesByKey(), a.packageInFinalState)
}

// artifactShouldBeRemoved é o gate de retenção do P2P_Temp.
func (a *App) artifactShouldBeRemoved(artifactID, artifactName string) bool {
	if a == nil || a.packageManagerRouter == nil {
		return false
	}
	return artifactRetentionDecision(artifactID, artifactName, a.knownTaskPackagesByKey(), a.packageInFinalState)
}
