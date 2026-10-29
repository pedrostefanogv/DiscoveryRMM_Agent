package windowsupdate

import "strings"

// Ponto de restauracao antes de mutar o sistema (WU-17, decisao D16).
//
// Decisao aprovada: UM checkpoint por comando (nao por update), criado de forma
// preguicosa — so quando existe pelo menos um update que realmente vai mutar o
// sistema —, pulando Definition Updates (nao alteram o sistema de forma
// arriscada) e pulando Windows Server (System Restore nao e confiavel ali).
// Falha ao criar o checkpoint NAO aborta a instalacao: fica registrada e o
// operador ve no log.
const (
	// restorePointDescription aparece no Restore Point do Windows.
	restorePointDescription = "Discovery RMM: instalacao de atualizacoes"
)

// NeedsRestorePoint diz se o lote exige checkpoint antes de instalar.
// Devolve true quando ha pelo menos um update cuja classificacao NAO e
// definition update (as unicas realmente baratas/seguras de pular).
func NeedsRestorePoint(categoriesPerUpdate [][]string) bool {
	for _, categories := range categoriesPerUpdate {
		if isDefinitionUpdate(categories) {
			continue
		}
		return true
	}
	return false
}

// isDefinitionUpdate reconhece Definition Updates (Defender/antivirus).
func isDefinitionUpdate(categories []string) bool {
	for _, category := range categories {
		normalized := strings.ToLower(strings.TrimSpace(category))
		if normalized == "definition updates" || normalized == "definitions" {
			return true
		}
	}
	return false
}
