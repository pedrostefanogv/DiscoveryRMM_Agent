package windowsupdate

import "strings"

// Proveniencia da severidade de um update (decisao D10: derivar no agente).
//
// A API persiste este valor junto da severidade para que a UI nunca confunda um
// rating do fornecedor com um rating inferido pelo agente.
const (
	// SeveritySourceVendor: o fornecedor publicou o rating (IUpdate.MsrcSeverity).
	SeveritySourceVendor = "vendor"
	// SeveritySourceDerived: o agente inferiu o rating a partir da classificacao
	// e do titulo porque o fornecedor nao publicou nenhum.
	SeveritySourceDerived = "derived"
	// SeveritySourceUnknown: nao ha rating e a derivacao nao se aplica (ex.:
	// drivers, definitions, feature packs, upgrades, tools).
	SeveritySourceUnknown = "unknown"
)

// canonicalSeverities normaliza o valor do MSRC para a forma exibida na UI.
// A comparacao nas regras de auto-aprovacao da API e case-insensitive
// (PatchApprovalResolver), entao a normalizacao e apenas de apresentacao.
var canonicalSeverities = map[string]string{
	"critical":  "Critical",
	"important": "Important",
	"moderate":  "Moderate",
	"low":       "Low",
}

// unratedClassifications sao classificacoes que NUNCA carregam rating de
// seguranca. Comparadas por igualdade (case-insensitive) para que um produto
// como "Microsoft Security Essentials" nao seja confundido com "Security
// Updates".
var unratedClassifications = map[string]bool{
	"definition updates": true,
	"drivers":            true,
	"feature packs":      true,
	"tools":              true,
	"upgrades":           true,
}

// ResolveSeverity devolve a severidade e sua proveniencia para um update do
// Windows Update.
//
// Regra (portada do Breeze, agent/internal/patching/severity.go):
//  1. MsrcSeverity valido (critical/important/moderate/low) sempre vence e a
//     proveniencia e "vendor".
//  2. Sem rating do fornecedor, deriva da classificacao/titulo, com TETO em
//     "Important": so o MSRC pode dizer "Critical". Derivar "critical" inflaria
//     qualquer regra de auto-aprovacao que aceite apenas criticos.
//  3. Definition Updates, Drivers, Feature Packs, Tools e Upgrades nunca
//     recebem rating (checado primeiro, para que "Security Intelligence Update"
//     de um definition update nao pegue rating pelo titulo).
//  4. Critical Updates / Security Updates -> Important.
//  5. Update Rollups -> Important so com "security" no titulo.
//  6. Qualquer outra classificacao -> Important quando o titulo diz security
//     update ou cumulative update (nao-preview).
//
// Quando nada se aplica devolve ("", SeveritySourceUnknown): severidade vazia
// vira NULL na API e nao casa nenhuma lista de auto-aprovacao.
func ResolveSeverity(msrcSeverity string, categoryNames []string, title string) (severity, source string) {
	if canonical, ok := canonicalSeverities[strings.ToLower(strings.TrimSpace(msrcSeverity))]; ok {
		return canonical, SeveritySourceVendor
	}
	if derived := deriveSeverity(categoryNames, title); derived != "" {
		return derived, SeveritySourceDerived
	}
	return "", SeveritySourceUnknown
}

// deriveSeverity aplica a heuristica documentada em ResolveSeverity. Devolve ""
// quando o update nao deve receber rating.
func deriveSeverity(categoryNames []string, title string) string {
	classifications := make(map[string]bool, len(categoryNames))
	for _, name := range categoryNames {
		classifications[strings.ToLower(strings.TrimSpace(name))] = true
	}

	for name := range classifications {
		if unratedClassifications[name] {
			return ""
		}
	}
	// Critical Updates nao e rating de seguranca; o teto e Important.
	if classifications["critical updates"] || classifications["security updates"] {
		return "Important"
	}

	lowerTitle := strings.ToLower(title)
	if classifications["update rollups"] {
		if strings.Contains(lowerTitle, "security") {
			return "Important"
		}
		return ""
	}
	if strings.Contains(lowerTitle, "security update") {
		return "Important"
	}
	if strings.Contains(lowerTitle, "cumulative update") && !strings.Contains(lowerTitle, "preview") {
		return "Important"
	}
	return ""
}
