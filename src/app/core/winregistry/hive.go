package winregistry

import "strings"

// Hive e um dos cinco ramos raiz aceitos.
//
// A allowlist e fechada de proposito. O registro tem hives que nao sao
// enderecaveis pelo caminho textual (HKEY_PERFORMANCE_DATA vive em memoria e
// nao tem subchaves navegaveis) e abreviacoes que variam entre ferramentas.
// "Adivinhar" o que o operador quis e exatamente como um editor de registro
// remoto acaba escrevendo no lugar errado.
type Hive string

const (
	HiveLocalMachine  Hive = "HKLM"
	HiveCurrentUser   Hive = "HKCU"
	HiveClassesRoot   Hive = "HKCR"
	HiveUsers         Hive = "HKU"
	HiveCurrentConfig Hive = "HKCC"
)

// hiveAliases mapeia toda grafia aceita para o hive canonico.
//
// Cobre os nomes longos do Windows (que e o que aparece em documentacao e em
// scripts) e as abreviacoes usuais. A comparacao e case-insensitive porque
// "hklm", "HKLM" e "Hklm" sao todos comuns em campo.
var hiveAliases = map[string]Hive{
	"hklm":                  HiveLocalMachine,
	"hkey_local_machine":    HiveLocalMachine,
	"hkey_localmachine":     HiveLocalMachine,
	"hkcu":                  HiveCurrentUser,
	"hkey_current_user":     HiveCurrentUser,
	"hkey_currentuser":      HiveCurrentUser,
	"hkcr":                  HiveClassesRoot,
	"hkey_classes_root":     HiveClassesRoot,
	"hkey_classesroot":      HiveClassesRoot,
	"hku":                   HiveUsers,
	"hkey_users":            HiveUsers,
	"hkcc":                  HiveCurrentConfig,
	"hkey_current_config":   HiveCurrentConfig,
	"hkey_currentconfig":    HiveCurrentConfig,
	"hkey_performance_data": "", // explicitamente recusado (ver ParseHive)
	"hkey_dyn_data":         "", // idem: hive legado, sem conteudo real
}

// CanonicalHives lista os hives na ordem de exibicao da arvore na UI.
func CanonicalHives() []Hive {
	return []Hive{HiveLocalMachine, HiveCurrentUser, HiveClassesRoot, HiveUsers, HiveCurrentConfig}
}

// ParseHive normaliza a string recebida do console.
//
// Devolve ok=false para qualquer coisa fora da allowlist, INCLUSIVE para
// HKEY_PERFORMANCE_DATA e HKEY_DYN_DATA, que estao no mapa com valor vazio de
// proposito: existem na documentacao, entao e melhor recusa-los com um motivo
// explicito do que deixa-los cair no "desconhecido" generico.
func ParseHive(raw string) (Hive, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "", false
	}
	hive, ok := hiveAliases[key]
	if !ok || hive == "" {
		return "", false
	}
	return hive, true
}

// String devolve a grafia canonica (a que o protocolo carrega).
func (h Hive) String() string { return string(h) }

// IsMachineHive informa se o hive e da maquina (e nao do usuario).
//
// Usado pela politica de consentimento: alterar HKLM/HKCR afeta todos os
// usuarios da maquina, enquanto HKCU afeta apenas quem esta logado. A distincao
// e o que permite "prompt na escrita de maquina, livre no usuario".
func (h Hive) IsMachineHive() bool {
	switch h {
	case HiveLocalMachine, HiveClassesRoot, HiveCurrentConfig:
		return true
	default:
		return false
	}
}
