package windowsupdate

import "strings"

// Classificacao por CategoryID do WUA (WU-14).
//
// Os NOMES das categorias do Windows Update sao localizados (em pt-BR vem
// "Atualizacoes de Seguranca", "Atualizacoes de Definicao", ...), enquanto o
// codigo comparava strings em ingles. Em maquina localizada a classificacao
// caia no fallback (primeira categoria, que costuma ser o PRODUTO) e os filtros
// de driver/upgrade deixavam de casar.
//
// O CategoryID e um GUID estavel e independente de idioma. A classificacao
// passa a olhar o GUID primeiro e so usa os nomes como fallback (agente antigo,
// WUA que nao exponha CategoryID).
var categoryNameByGUID = map[string]string{
	"e6cf1350-c01b-414d-a61f-263d14d133b4": "Critical Updates",
	"0fa1201d-4330-4fa8-8ae9-b877473b6441": "Security Updates",
	"e0789628-ce08-4437-be74-2495b842f43b": "Definition Updates",
	"28bc880e-0592-4cbf-8f95-c79b17911d5f": "Update Rollups",
	"68c5b0a3-d1a6-4553-ae49-01d3a7827828": "Service Packs",
	"b54e7d24-7add-428f-8b75-90a396fa584f": "Feature Packs",
	"ebfc1fc5-71a4-4f7b-9aca-3b9a503104a0": "Drivers",
	"3689bdc8-b205-4af4-8d4a-a63924c5e9d5": "Upgrades",
	"2c24e84f-dcbf-4d64-9d50-a3ddf6d62b4e": "Tools",
}

// categoryPriority define a ordem de escolha quando o update tem varias
// categorias (produto + classificacao).
var categoryPriority = []string{
	"Critical Updates",
	"Security Updates",
	"Definition Updates",
	"Update Rollups",
	"Service Packs",
	"Feature Packs",
	"Drivers",
	"Upgrades",
	"Tools",
}

// DeriveUpdateType classifica o TIPO do update (WU-15): feature | driver |
// software. Vai no envelope para a API nao precisar re-derivar por titulo —
// heuristica duplicada em dois lugares ja causou divergencia no Breeze.
func DeriveUpdateType(categoryIDs []string, categoryNames []string) string {
	if IsUpgradeCategory(categoryIDs, categoryNames) {
		return "feature"
	}
	if IsDriverCategory(categoryIDs, categoryNames) {
		return "driver"
	}
	return "software"
}

// ClassifyCategory resolve a classificacao canonica por GUID, caindo para os
// nomes (comportamento antigo) quando nenhum GUID e conhecido.
func ClassifyCategory(categoryIDs []string, categoryNames []string) string {
	seen := make(map[string]bool, len(categoryIDs))
	for _, id := range categoryIDs {
		if name, ok := categoryNameByGUID[strings.ToLower(strings.TrimSpace(id))]; ok {
			seen[name] = true
		}
	}
	for _, wanted := range categoryPriority {
		if seen[wanted] {
			return wanted
		}
	}
	return classify(categoryNames)
}

// IsDriverCategory informa se algum GUID/nome e da categoria Drivers.
func IsDriverCategory(categoryIDs []string, categoryNames []string) bool {
	for _, id := range categoryIDs {
		if strings.EqualFold(strings.TrimSpace(categoryNameByGUID[strings.ToLower(strings.TrimSpace(id))]), "Drivers") {
			return true
		}
	}
	for _, name := range categoryNames {
		if strings.EqualFold(strings.TrimSpace(name), "drivers") {
			return true
		}
	}
	return false
}

// IsUpgradeCategory informa se algum GUID/nome e da categoria Upgrades.
func IsUpgradeCategory(categoryIDs []string, categoryNames []string) bool {
	for _, id := range categoryIDs {
		if strings.EqualFold(strings.TrimSpace(categoryNameByGUID[strings.ToLower(strings.TrimSpace(id))]), "Upgrades") {
			return true
		}
	}
	for _, name := range categoryNames {
		if strings.EqualFold(strings.TrimSpace(name), "upgrades") {
			return true
		}
	}
	return false
}
