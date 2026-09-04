package automation

import (
	"context"
	"fmt"
	"strings"
)

// wingetActionDecision é o resultado da decisão versionada de execução.
type wingetActionDecision struct {
	Skip bool
	// Reason é a mensagem de skip (vazia quando Skip=false).
	Reason string
	// Benign indica skip benigno (nada a fazer: já instalado/atualizado).
	Benign bool
	// AvailableVersion é a versão observada como disponível (winget upgrade
	// col. "Available" ou versão do artifact P2P). Vazia quando desconhecida.
	AvailableVersion string
	// InstalledVersion é a versão instalada localmente (quando conhecida).
	InstalledVersion string
}

// decideWingetAction decide se a operação winget deve executar, combinando:
//  1. Estado local real (winget list / winget upgrade) — fonte primária.
//  2. Versão do artifact P2P ("winget:<id>") — validação extra quando presente:
//     só executa se a versão disponível > instalada; evita loops quando o
//     catálogo do servidor está defasado.
//
// A lógica preserva as mensagens de skip originais (shouldSkipWingetAction)
// para compatibilidade com classifyPackageResult/anti-loop.
func decideWingetAction(ctx context.Context, packages PackageManager, operation, packageID string) wingetActionDecision {
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return wingetActionDecision{}
	}

	switch operation {
	case "install":
		installed, err := packages.ListInstalled(ctx)
		if err != nil {
			// Erro na verificação — prossegue com install (fail-safe).
			return wingetActionDecision{}
		}
		if isPackageInOutput(installed, packageID) {
			return wingetActionDecision{
				Skip:             true,
				Reason:           fmt.Sprintf("pacote %s ja instalado — pulando install", packageID),
				Benign:           true,
				InstalledVersion: findVersionInOutput(installed, packageID),
			}
		}
		return wingetActionDecision{}

	case "upgrade":
		upgradable, err := packages.ListUpgradable(ctx)
		if err != nil {
			// Erro na verificação — prossegue com upgrade (fail-safe).
			return wingetActionDecision{}
		}
		if isPackageInOutput(upgradable, packageID) {
			// Há update pendente segundo o winget real → executa.
			return wingetActionDecision{
				AvailableVersion: findVersionInOutput(upgradable, packageID, "available"),
			}
		}

		// Não está em "upgrade": instalado e atualizado, ou ausente.
		installed, instErr := packages.ListInstalled(ctx)
		installedVersion := ""
		present := false
		if instErr == nil {
			present = isPackageInOutput(installed, packageID)
			installedVersion = findVersionInOutput(installed, packageID)
		}

		if instErr == nil && !present {
			return wingetActionDecision{
				Skip:   true,
				Reason: fmt.Sprintf("pacote %s nao encontrado — pulando upgrade", packageID),
			}
		}

		// Instalado e sem update pendente: skip benigno.
		decision := wingetActionDecision{
			Skip:             true,
			Reason:           fmt.Sprintf("pacote %s ja atualizado — pulando upgrade", packageID),
			Benign:           true,
			InstalledVersion: installedVersion,
		}

		// Validação extra via P2P: se a rede anuncia uma versão do pacote mais
		// nova que a instalada, o winget local pode estar com cache de source
		// stale — executa mesmo assim (o winget upgrade vai verificar de novo).
		if avail := p2pAvailableVersion(packageID); avail != "" {
			decision.AvailableVersion = avail
			if installedVersion != "" && compareVersions(avail, installedVersion) > 0 {
				return wingetActionDecision{
					AvailableVersion: avail,
					InstalledVersion: installedVersion,
					// Sem skip: deixa o winget tentar (cache stale é caso raro;
					// se o winget confirmar que está atualizado, na próxima
					// execução o resultado volta a ser skip benigno).
				}
			}
		}
		return decision
	}

	return wingetActionDecision{}
}

// p2pVersionResolver é injetado pelo App (automation_p2p.go) e consulta a
// versão do artifact "winget:<packageId>" no cache local/índice P2P.
// nil quando não há suporte (testes, agent sem P2P).
var p2pVersionResolver func(packageID string) string

// SetP2PVersionResolver registra o resolvedor de versão P2P. Chamado pelo App
// no startup (depois de criar o packageManagerRouter).
func SetP2PVersionResolver(resolver func(packageID string) string) {
	p2pVersionResolver = resolver
}

// p2pAvailableVersion consulta a versão disponível do pacote na rede P2P.
// Retorna "" quando não há resolver ou o artifact não tem versão.
func p2pAvailableVersion(packageID string) string {
	if p2pVersionResolver == nil {
		return ""
	}
	return strings.TrimSpace(p2pVersionResolver(packageID))
}

// findVersionInOutput extrai a versão de um output tabular do winget
// (list/upgrade) para o pacote alvo. A tabela do winget tem colunas:
// Name / Id / Version [/ Available / Source].
// which: "" = coluna Version; "available" = coluna Available.
func findVersionInOutput(output, packageID string, which ...string) string {
	wantAvailable := len(which) > 0 && which[0] == "available"
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if !isPackageLine(line, packageID) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// Localiza a posição do ID na linha para saber onde começam as versões.
		idIdx := indexOfField(fields, packageID)
		if idIdx < 0 {
			continue
		}
		// Depois do ID: [Version] [Available] [Source]
		rest := fields[idIdx+1:]
		if !wantAvailable {
			if len(rest) >= 1 && looksLikeVersion(rest[0]) {
				return rest[0]
			}
			continue
		}
		// Available: segunda coluna de versão quando existir.
		versions := make([]string, 0, 2)
		for _, f := range rest {
			if looksLikeVersion(f) {
				versions = append(versions, f)
			}
			if len(versions) == 2 {
				break
			}
		}
		if len(versions) >= 2 {
			return versions[1]
		}
	}
	return ""
}

// FindVersionInOutput é a versão exportada de findVersionInOutput para uso
// pelo App (automation_p2p.go).
func FindVersionInOutput(output, packageID string, which ...string) string {
	return findVersionInOutput(output, packageID, which...)
}

// isPackageLine verifica se a linha contém o packageID como token próprio.
func isPackageLine(line, packageID string) bool {
	return isPackageInOutput(line, packageID)
}

// indexOfField retorna o índice do campo igual ao packageID (case-insensitive).
func indexOfField(fields []string, packageID string) int {
	for i, f := range fields {
		if strings.EqualFold(strings.TrimSpace(f), strings.TrimSpace(packageID)) {
			return i
		}
	}
	return -1
}

// ShouldPreloadPackage decide se o pacote precisa de pré-carga P2P com base
// no estado REAL da máquina — evita re-baixar instaladores sem necessidade:
//
//   - InstallPackage: só pré-carrega se o pacote NÃO está instalado.
//     (Instalado → nada a fazer; o TTL local limpa o arquivo e ele não volta.)
//   - UpdatePackage: só pré-carrega se há update pendente no winget upgrade.
//     (Sem pendente → nada a fazer.)
//   - UpdateOrInstallPackage: pré-carrega se não instalado OU há update pendente.
//   - Erro na verificação: fail-safe, retorna true (pré-carga conservadora,
//     mesmo comportamento de decideWingetAction quando winget falha).
//
// A decisão usa os mesmos outputs de winget list/upgrade (cacheados pelos
// chamadores quando possível) que a execução real usará — pré-carga e
// execução nunca divergem.
func ShouldPreloadPackage(ctx context.Context, packages PackageManager, actionType AutomationTaskActionType, packageID string) bool {
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return false
	}
	switch actionType {
	case ActionInstallPackage:
		d := decideWingetAction(ctx, packages, "install", packageID)
		return !d.Skip // instalado → skip benigno → não pré-carrega
	case ActionUpdatePackage:
		d := decideWingetAction(ctx, packages, "upgrade", packageID)
		return !d.Skip // sem update pendente → não pré-carrega
	case ActionUpdateOrInstallPackage:
		// Precisa se (não instalado) OU (há update pendente).
		inst, instErr := packages.ListInstalled(ctx)
		if instErr != nil {
			return true // fail-safe
		}
		if !isPackageInOutput(inst, packageID) {
			return true // não instalado → install futuro precisará do instalador
		}
		up, upErr := packages.ListUpgradable(ctx)
		if upErr != nil {
			return true // fail-safe
		}
		return isPackageInOutput(up, packageID)
	default:
		return false
	}
}
