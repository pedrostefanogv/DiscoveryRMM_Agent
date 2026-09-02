package automation

import (
	"strconv"
	"strings"
)

// Comparação de versões tolerante para pacotes winget. Winget não garante
// semver: versões podem ser "132.0.1", "2026.1.3.36551", "1.29.289.0",
// "80.44.56884", com sufixos ("-beta", "b", "rc1"). A estratégia é:
//   1. Normalizar: lower, trim, remover prefixo "v".
//   2. Split por "." e comparar componente a componente numericamente.
//   3. Componente com sufixo não numérico: compara a parte numérica; empate
//      cai em strcmp do sufixo (beta < rc < vazio — aproximação).
//   4. Versão com mais componentes que outra: componentes ausentes = 0.

// compareVersions retorna -1 se a < b, 0 se a == b, 1 se a > b.
// Versões vazias são consideradas as menores possíveis.
func compareVersions(a, b string) int {
	aParts := splitVersionComponents(a)
	bParts := splitVersionComponents(b)

	maxLen := len(aParts)
	if len(bParts) > maxLen {
		maxLen = len(bParts)
	}

	for i := 0; i < maxLen; i++ {
		var pa, pb versionComponent
		if i < len(aParts) {
			pa = aParts[i]
		}
		if i < len(bParts) {
			pb = bParts[i]
		}
		if c := compareVersionComponent(pa, pb); c != 0 {
			return c
		}
	}
	return 0
}

// CompareVersions é a versão exportada de compareVersions para uso pelo App
// (automation_p2p.go — seleção da maior versão anunciada pelos peers).
func CompareVersions(a, b string) int {
	return compareVersions(a, b)
}

type versionComponent struct {
	num    int    // parte numérica (0 quando ausente)
	suffix string // sufixo textual ("" quando ausente)
}

// splitVersionComponents quebra a versão em componentes.
// Retorna nil para string vazia.
func splitVersionComponents(v string) []versionComponent {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	comps := make([]versionComponent, 0, len(parts))
	for _, p := range parts {
		comps = append(comps, parseVersionComponent(p))
	}
	return comps
}

// parseVersionComponent separa a parte numérica do sufixo textual.
// "3" -> {3, ""}; "3-beta" -> {3, "beta"}; "beta" -> {0, "beta"}.
func parseVersionComponent(s string) versionComponent {
	s = strings.TrimSpace(s)
	digitEnd := 0
	for digitEnd < len(s) && s[digitEnd] >= '0' && s[digitEnd] <= '9' {
		digitEnd++
	}
	num := 0
	if digitEnd > 0 {
		if n, err := strconv.Atoi(s[:digitEnd]); err == nil {
			num = n
		}
	}
	return versionComponent{num: num, suffix: strings.TrimSpace(s[digitEnd:])}
}

func compareVersionComponent(a, b versionComponent) int {
	if a.num != b.num {
		if a.num < b.num {
			return -1
		}
		return 1
	}
	// Empate numérico: sufixo vazio é "maior" (release final) que sufixos
	// tipo beta/rc; entre sufixos, strcmp.
	as, bs := a.suffix, b.suffix
	if as == bs {
		return 0
	}
	if as == "" {
		return 1
	}
	if bs == "" {
		return -1
	}
	return strings.Compare(as, bs)
}

// versionFromInstallerFilename tenta extrair uma versão do nome do arquivo do
// instalador (ex.: "Firefox-132.0.1.exe" -> "132.0.1", "app_2026.1.3_x64.msi"
// -> "2026.1.3"). Retorna "" quando não encontra padrão de versão.
func versionFromInstallerFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// Heurística: procura sequências de dígitos separados por pontos com
	// pelo menos 2 componentes numéricas dentro do nome (sem extensão).
	base := name
	if idx := strings.LastIndex(base, "."); idx > 0 {
		ext := strings.ToLower(base[idx+1:])
		if ext == "exe" || ext == "msi" || ext == "msix" || ext == "zip" {
			base = base[:idx]
		}
	}
	best := ""
	for _, token := range strings.FieldsFunc(base, func(r rune) bool {
		return !(r >= '0' && r <= '9') && r != '.'
	}) {
		if !looksLikeVersion(token) {
			continue
		}
		// Prefere o token mais longo (mais componentes = mais provável ser versão).
		if len(token) > len(best) {
			best = token
		}
	}
	return best
}

// VersionFromInstallerFilename é a versão exportada de versionFromInstallerFilename
// para uso pelo App (automation_p2p.go).
func VersionFromInstallerFilename(name string) string {
	return versionFromInstallerFilename(name)
}

// looksLikeVersion valida se o token tem formato de versão: componentes
// numéricas separadas por ponto, mínimo 2 componentes, dígitos nas pontas.
func looksLikeVersion(token string) bool {
	token = strings.Trim(token, ".")
	if token == "" || !strings.Contains(token, ".") {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return false
	}
	for i, p := range parts {
		if p == "" {
			return false
		}
		digits := 0
		for _, r := range p {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits == 0 {
			return false
		}
		// Primeiro e último componentes devem ser puramente numéricos.
		if (i == 0 || i == len(parts)-1) && digits != len(p) {
			return false
		}
	}
	return true
}
