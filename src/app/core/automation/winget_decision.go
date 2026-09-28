package automation

import (
	"context"
	"fmt"
	"strings"

	"discovery/app/core/winget"
)

// richPackageLister é a capacidade opcional do PackageManager que entrega o
// JSON do winget ("--output json", Ids completos) quando suportado. Sem ela o
// decision cai para o ListInstalled tabular normal.
type richPackageLister interface {
	ListInstalledRich(ctx context.Context) (string, error)
	ListUpgradableRich(ctx context.Context) (string, error)
}

func listInstalledForDecision(ctx context.Context, packages PackageManager) (string, error) {
	if rich, ok := packages.(richPackageLister); ok {
		return rich.ListInstalledRich(ctx)
	}
	return packages.ListInstalled(ctx)
}

func listUpgradableForDecision(ctx context.Context, packages PackageManager) (string, error) {
	if rich, ok := packages.(richPackageLister); ok {
		return rich.ListUpgradableRich(ctx)
	}
	return packages.ListUpgradable(ctx)
}

// ShouldSkipPackageActionBeforePrompt decide, ANTES do prompt de confirmação
// (Welcome PSADT / toast), se uma ação de PACOTE não tem nada a fazer: já
// instalado, já atualizado ou ausente para upgrade. Sem isso o usuário era
// interrompido para confirmar uma instalação que o executor pularia logo depois
// (o welcome acontecia antes da checagem de estado).
//
// Só faz sentido quando a tarefa exigiria confirmação; não é usado para
// RunScript/CustomCommand/RemovePackage (esses sempre executam).
func ShouldSkipPackageActionBeforePrompt(ctx context.Context, packages PackageManager, task AutomationTask) bool {
	if packages == nil {
		return false
	}
	packageID := strings.TrimSpace(task.PackageID)
	if packageID == "" {
		return false
	}

	switch task.ActionType {
	case ActionInstallPackage:
		return decideWingetAction(ctx, packages, "install", packageID).Skip
	case ActionUpdatePackage:
		return decideWingetAction(ctx, packages, "upgrade", packageID).Skip
	case ActionUpdateOrInstallPackage:
		// Instalado E sem update pendente → nada a fazer.
		return !ShouldPreloadPackage(ctx, packages, ActionUpdateOrInstallPackage, packageID)
	default:
		return false
	}
}

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
	// DecidedBy registra a fonte da decisão para telemetria no servidor:
	// "winget" (list/upgrade responderam) ou "inventory-cache" (winget
	// indisponível e o cache de instalados confirmou o pacote).
	DecidedBy string
}

const (
	decidedByWinget = "winget"
	decidedByCache  = "inventory-cache"
)

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
		installed, err := listInstalledForDecision(ctx, packages)
		// O output é avaliado mesmo quando o winget sai com erro: ele costuma
		// imprimir a tabela inteira e só depois falhar numa fonte, e descartar
		// esse texto fazia o agente re-baixar instalador de pacote já instalado.
		if isPackageInOutput(installed, packageID) {
			return wingetActionDecision{
				Skip:             true,
				Reason:           fmt.Sprintf("pacote %s ja instalado — pulando install", packageID),
				Benign:           true,
				InstalledVersion: findVersionInOutput(installed, packageID),
				DecidedBy:        decidedByWinget,
			}
		}
		if err != nil || strings.TrimSpace(installed) == "" {
			// Lista indisponível: antes o fluxo seguia para o download (fail-open).
			// O cache do inventário é a última evidência confiável de instalação —
			// se ele confirma o pacote, pula sem transferir nada.
			if knownInstalledFromCache(packageID) {
				logWingetDecision("install: winget list indisponivel (%v) mas %s consta instalado no cache do inventario — skip sem baixar", err, packageID)
				return wingetActionDecision{
					Skip:      true,
					Reason:    fmt.Sprintf("pacote %s ja instalado (cache do inventario) — pulando install", packageID),
					Benign:    true,
					DecidedBy: decidedByCache,
				}
			}
			if err != nil {
				logWingetDecision("install: winget list falhou (%v) e %s nao esta no cache — prosseguindo (fail-safe)", err, packageID)
			}
		}
		if err == nil && strings.TrimSpace(installed) != "" {
			// Lista confiável: o pacote realmente não está instalado.
			return wingetActionDecision{DecidedBy: decidedByWinget}
		}
		return wingetActionDecision{}

	case "upgrade":
		upgradable, upErr := listUpgradableForDecision(ctx, packages)
		// Mesma regra do install: usa o output mesmo com erro de saída.
		if isPackageInOutput(upgradable, packageID) {
			// Há update pendente segundo o winget real → executa.
			return wingetActionDecision{
				AvailableVersion: findVersionInOutput(upgradable, packageID, "available"),
				DecidedBy:        decidedByWinget,
			}
		}
		if upErr != nil {
			// Antes um erro aqui retornava direto e o router baixava o instalador
			// para rodar um upgrade que podia nem existir. Agora segue para a
			// validação de estado instalado.
			logWingetDecision("upgrade: winget upgrade indisponivel (%v) packageId=%s — validando estado instalado antes de prosseguir", upErr, packageID)
		}

		// Não está em "upgrade": instalado e atualizado, ou ausente.
		installed, instErr := listInstalledForDecision(ctx, packages)
		installedVersion := ""
		// decidedBy fica vazio quando nenhuma fonte confirmou o estado (skip
		// conservador sem evidência) — não atribuir a autoria ao winget nesse caso.
		decidedBy := ""
		present := isPackageInOutput(installed, packageID)
		if present {
			installedVersion = findVersionInOutput(installed, packageID)
			decidedBy = decidedByWinget
		} else if knownInstalledFromCache(packageID) {
			present = true
			decidedBy = decidedByCache
			logWingetDecision("upgrade: %s consta instalado no cache do inventario (winget list err=%v) — sem download", packageID, instErr)
		}

		// Fonte confiável (sem erro e com conteúdo) e pacote ausente: nada a fazer.
		if !present && instErr == nil && strings.TrimSpace(installed) != "" {
			return wingetActionDecision{
				Skip:      true,
				Reason:    fmt.Sprintf("pacote %s nao encontrado — pulando upgrade", packageID),
				DecidedBy: decidedByWinget,
			}
		}

		// Instalado e sem update pendente: skip benigno. Quando o estado não pôde
		// ser confirmado, mantém o skip conservador (não transfere instalador para
		// um update incerto; o próximo ciclo reavalia).
		decision := wingetActionDecision{
			Skip:             true,
			Reason:           fmt.Sprintf("pacote %s ja atualizado — pulando upgrade", packageID),
			Benign:           true,
			InstalledVersion: installedVersion,
			DecidedBy:        decidedBy,
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

// installedPackageChecker é injetado pelo App (inventory.Service) e informa se o
// pacote consta na última lista de instalados conhecida (cache do "winget list").
// Retorna (known, installed): known=false quando não há lista boa em cache.
// Usado para NÃO baixar instalador quando o winget está momentaneamente
// indisponível mas o pacote já está instalado.
var installedPackageChecker func(packageID string) (known bool, installed bool)

// SetInstalledPackageChecker registra o verificador de pacote instalado.
func SetInstalledPackageChecker(checker func(packageID string) (known bool, installed bool)) {
	installedPackageChecker = checker
}

// wingetDecisionLogger é injetado pelo App para registrar decisões relevantes
// (ex.: winget indisponível e uso do cache para evitar transferência).
var wingetDecisionLogger func(string)

// SetWingetDecisionLogger registra o logger das decisões.
func SetWingetDecisionLogger(logger func(string)) {
	wingetDecisionLogger = logger
}

func logWingetDecision(format string, args ...any) {
	if wingetDecisionLogger != nil {
		wingetDecisionLogger(fmt.Sprintf(format, args...))
	}
}

// knownInstalledFromCache consulta o cache do inventário. Só devolve true com
// evidência POSITIVA de instalação — nunca conclui "não instalado" a partir dele.
func knownInstalledFromCache(packageID string) bool {
	if installedPackageChecker == nil {
		return false
	}
	known, installed := installedPackageChecker(packageID)
	return known && installed
}

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
	if entries, ok := winget.ParseListJSON(output); ok {
		target := strings.TrimSpace(packageID)
		for _, entry := range entries {
			if !strings.EqualFold(strings.TrimSpace(entry.ID), target) {
				continue
			}
			if wantAvailable {
				return entry.AvailableVersionValue()
			}
			return strings.TrimSpace(entry.Version)
		}
		return ""
	}
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
		inst, instErr := listInstalledForDecision(ctx, packages)
		if strings.TrimSpace(inst) != "" && isPackageInOutput(inst, packageID) {
			up, upErr := listUpgradableForDecision(ctx, packages)
			if strings.TrimSpace(up) == "" || upErr != nil {
				// Lista de updates indisponível, mas o cache confirma que está
				// instalado: não pré-carrega (não há evidência de update).
				return false
			}
			return isPackageInOutput(up, packageID)
		}
		if instErr == nil && strings.TrimSpace(inst) != "" {
			return true // lista confiável e pacote ausente → precisará do instalador
		}
		// Lista indisponível: só evita o download com evidência positiva do cache.
		if knownInstalledFromCache(packageID) {
			up, upErr := listUpgradableForDecision(ctx, packages)
			if upErr == nil && strings.TrimSpace(up) != "" && isPackageInOutput(up, packageID) {
				return true
			}
			return false
		}
		return true // fail-safe
	default:
		return false
	}
}
