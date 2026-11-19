package winregistry

import (
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// GUARD DO REGISTRO REMOTO
//
// Este arquivo e PURO: nao abre chave, nao chama syscall, nao le nada da
// maquina. Recebe (hive, caminho, operacao) e devolve uma decisao. Isso o torna
// 100% testavel sem tocar no registro real — mesmo requisito de
// sysctrl/service_guard_windows.go, e pela mesma razao: um teste que apagasse
// HKLM\SAM de verdade destruiria a maquina de teste.
//
// Postura: FAIL-CLOSED. A decisao default e RECUSAR. So executa o que nao
// casar com nenhuma regra de bloqueio.
//
// Decisao de produto (confirmada com o usuario): Run/RunOnce sao LEITURA
// PERMITIDA e ESCRITA BLOQUEADA. O tecnico precisa ver o que inicia no boot
// (diagnostico legitimo), mas criar persistencia silenciosa e o vetor classico
// de malware e nao tem lugar numa ferramenta de suporte.
// ─────────────────────────────────────────────────────────────────────────────

// Op e a operacao pedida. Distinguir leitura de escrita e o que permite
// "Run/RunOnce: le, nao escreve" e "SECURITY: nem le".
type Op string

const (
	OpRead   Op = "read"   // listKey, getValue, export
	OpWrite  Op = "write"  // setValue, createKey, renameKey
	OpDelete Op = "delete" // deleteValue, deleteKey
)

// ParseOp normaliza a operacao. Devolve ok=false para qualquer outra coisa.
func ParseOp(raw string) (Op, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "read":
		return OpRead, true
	case "write":
		return OpWrite, true
	case "delete":
		return OpDelete, true
	default:
		return "", false
	}
}

// IsMutation informa se a operacao altera o registro.
//
// E o gate de consentimento e de confirmacao: leitura nao pede confirmed=true.
// Delete conta como mutacao (a confirmacao e ainda mais importante ali, porque
// nao ha desfazer).
func (o Op) IsMutation() bool { return o == OpWrite || o == OpDelete }

// GuardDecision e o veredito do guard.
type GuardDecision struct {
	Allowed bool
	// Reason explica a recusa em portugues, para a UI mostrar ao operador.
	// Vazio quando Allowed.
	Reason string
	// Rule identifica a regra que decidiu, para auditoria. Estavel e legivel
	// por maquina (ex.: "hive-security", "run-keys-write").
	Rule string
}

func allow() GuardDecision { return GuardDecision{Allowed: true} }

func deny(rule, reason string) GuardDecision {
	return GuardDecision{Allowed: false, Rule: rule, Reason: reason}
}

// ─────────────────────────────────────────────────────────────────────────────
// REGRA 1 — Hives e subarvores NUNCA acessiveis, nem para leitura.
//
// Criterio: o conteudo e material de credencial do proprio SO. Nem leitura:
// SECURITY guarda a SAM database e as politicas de auditoria; SAM guarda os
// hashes de senha locais. Nao existe cenario de suporte em RMM que precise ler
// isso, e o custo de vazar e a compromisso da maquina e do dominio.
// ─────────────────────────────────────────────────────────────────────────────
var neverAllowedSubtrees = []struct {
	prefix string
	rule   string
	reason string
}{
	{
		"SECURITY",
		"hive-security",
		"HKLM\\SECURITY guarda a SAM database e as politicas de auditoria do SO; nao e acessivel nem para leitura",
	},
	{
		"SAM",
		"hive-sam",
		"HKLM\\SAM guarda os hashes de senha das contas locais; nao e acessivel nem para leitura",
	},
	{
		"SYSTEM\\CurrentControlSet\\Control\\Lsa",
		"lsa-secrets",
		"a subarvore LSA guarda segredos de autenticacao (LSA secrets); nao e acessivel nem para leitura",
	},
	{
		"SYSTEM\\CurrentControlSet\\Control\\SecurityProviders",
		"security-providers",
		"SecurityProviders define os provedores de autenticacao do logon; altera-lo quebra o logon da maquina",
	},
	{
		"SYSTEM\\CurrentControlSet\\Control\\Session Manager\\SAM",
		"session-manager-sam",
		"a subarvore SAM do Session Manager controla o carregamento da base de contas; nao e acessivel",
	},
}

// ─────────────────────────────────────────────────────────────────────────────
// REGRA 2 — Escrita/exclusao bloqueadas, leitura PERMITIDA.
//
// Criterio: o operador precisa diagnosticar o que a chave faz, mas altera-la
// remotamente tem risco desproporcional ao beneficio de suporte. Sao os pontos
// de persistencia e de politica do SO.
// ─────────────────────────────────────────────────────────────────────────────
var readOnlySubtrees = []struct {
	prefix string
	rule   string
	reason string
}{
	{
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run",
		"run-keys-write",
		"Run e ponto de persistencia de autostart; leitura e permitida para diagnostico, mas criar persistencia silenciosa nao",
	},
	{
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\RunOnce",
		"run-keys-write",
		"RunOnce e ponto de persistencia de autostart; leitura e permitida para diagnostico, mas criar persistencia silenciosa nao",
	},
	{
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Policies",
		"policies-write",
		"politicas de maquina aplicadas pelo SO; altera-las muda o comportamento de todos os usuarios e pode imitar GPO",
	},
	{
		"SOFTWARE\\Policies",
		"policies-write",
		"arvore de politicas administradas; altera-la pode imitar configuracao de dominio",
	},
	{
		"SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\Winlogon",
		"winlogon-write",
		"Winlogon define shell, Userinit e notificacoes de logon; e ponto classico de persistencia e de sequestro de sessao",
	},
	{
		"SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\Image File Execution Options",
		"ifeo-write",
		"Image File Execution Options permite sequestrar a execucao de qualquer executavel (inclusive Debugger); leitura para diagnostico, escrita nao",
	},
	{
		"SOFTWARE\\Microsoft\\Windows Defender",
		"defender-write",
		"configuracao do Windows Defender; desativar protecao remotamente e acao de risco e nao e suporte",
	},
	{
		"SYSTEM\\CurrentControlSet\\Services",
		"services-write",
		"a base de servicos do SO; use o controle de servicos dedicado (aba Servicos), que tem guard proprio com contexto de DC",
	},
	{
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Uninstall",
		"uninstall-write",
		"a lista de programas instalados e mantida pelo proprio instalador; editar aqui dessincroniza o inventario da maquina",
	},
}

// agentOwnKeys sao chaves do proprio Discovery Agent. Bloqueadas para escrita
// porque altera-las desconfigura o agente que esta executando a acao.
var agentOwnKeys = []struct {
	prefix string
	rule   string
	reason string
}{
	{
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\Discovery.RMM",
		"agent-own-key",
		"chave de desinstalacao do proprio Discovery Agent; altera-la quebra o descomissionamento do agente",
	},
	{
		"SOFTWARE\\Discovery",
		"agent-own-key",
		"arvore de configuracao do proprio Discovery Agent",
	},
	{
		"SOFTWARE\\DiscoveryRMM",
		"agent-own-key",
		"arvore de configuracao do proprio Discovery Agent",
	},
}

// ─────────────────────────────────────────────────────────────────────────────
// REGRA 3 — Sufixos sensiveis, bloqueados em QUALQUER hive e em qualquer nivel.
//
// Criterio: alguns nomes de chave tem significado de seguranca independente da
// posicao (o SO os procura em mais de um lugar, e malware usa variantes). Casar
// por sufixo fecha as variacoes que uma lista de caminhos exatos deixaria passar
// — ex.: HKLM\...\Image File Execution Options E HKCU\...\Image File Execution Options.
// ─────────────────────────────────────────────────────────────────────────────
var sensitiveSuffixes = []struct {
	suffix string
	rule   string
	reason string
}{
	{
		"\\Image File Execution Options",
		"ifeo-suffix",
		"Image File Execution Options permite sequestrar a execucao de executaveis; escrita bloqueada em qualquer hive",
	},
	{
		"\\Windows Defender\\Exclusions",
		"defender-exclusions",
		"adicionar exclusoes ao antivirus e acao de evasao, nao de suporte",
	},
	{
		"\\Windows Defender\\Exclusions\\Paths",
		"defender-exclusions",
		"adicionar exclusoes ao antivirus e acao de evasao, nao de suporte",
	},
	{
		"\\Windows Defender\\Exclusions\\Extensions",
		"defender-exclusions",
		"adicionar exclusoes ao antivirus e acao de evasao, nao de suporte",
	},
	{
		"\\Windows Defender\\Exclusions\\Processes",
		"defender-exclusions",
		"adicionar exclusoes ao antivirus e acao de evasao, nao de suporte",
	},
	{
		"\\Policies\\System",
		"policies-system",
		"politicas de sistema (inclui DisableTaskMgr, DisableRegistryTools); altera-las restringe o usuario local",
	},
	{
		"\\CurrentVersion\\Explorer\\User Shell Folders",
		"user-shell-folders",
		"User Shell Folders redireciona pastas do perfil; escrita pode desviar dados do usuario",
	},
}

// Evaluate decide se (hive, caminho, operacao) e permitido.
//
// A ORDEM importa e e parte do contrato:
//  1. hive na allowlist (ParseHive ja garante, mas revalidamos: o guard nao
//     confia no chamador);
//  2. subarvores nunca acessiveis — vencem sobre tudo, inclusive leitura;
//  3. sufixos sensiveis e chaves do agente — bloqueiam apenas mutacao;
//  4. subarvores somente-leitura — bloqueiam apenas mutacao;
//  5. caso contrario, permite.
//
// O caminho e normalizado e comparado em MAIUSCULAS: o registro do Windows e
// case-insensitive, e "software\\microsoft\\windows" precisa casar com
// "SOFTWARE\\Microsoft\\Windows" ou a regra seria trivialmente contornavel.
func Evaluate(hive Hive, keyPath string, op Op) GuardDecision {
	// 1 — hive.
	switch hive {
	case HiveLocalMachine, HiveCurrentUser, HiveClassesRoot, HiveUsers, HiveCurrentConfig:
	default:
		return deny("hive-not-allowed", "hive nao permitido: "+string(hive))
	}

	// Operacao desconhecida nunca passa: fail-closed tambem aqui.
	if op != OpRead && op != OpWrite && op != OpDelete {
		return deny("op-not-allowed", "operacao nao reconhecida")
	}

	normalized, err := NormalizePath(keyPath)
	if err != nil {
		return deny("path-invalid", "caminho invalido: "+err.Error())
	}
	upper := strings.ToUpper(normalized)

	// 2 — nunca acessiveis (inclusive leitura).
	for _, rule := range neverAllowedSubtrees {
		if subtreeMatches(upper, rule.prefix) {
			return deny(rule.rule, rule.reason)
		}
	}

	// 3 e 4 — somente mutacao.
	if op.IsMutation() {
		for _, rule := range sensitiveSuffixes {
			if strings.HasSuffix(upper, strings.ToUpper(rule.suffix)) {
				return deny(rule.rule, rule.reason)
			}
		}
		for _, rule := range agentOwnKeys {
			if subtreeMatches(upper, rule.prefix) {
				return deny(rule.rule, rule.reason)
			}
		}
		for _, rule := range readOnlySubtrees {
			if subtreeMatches(upper, rule.prefix) {
				return deny(rule.rule, rule.reason)
			}
		}
	}

	return allow()
}

// subtreeMatches casa o caminho com um prefixo de subarvore.
//
// Casar o PREFIXO (e nao a igualdade) e deliberado: bloquear
// "...\\CurrentVersion\\Run" precisa bloquear tambem "...\\Run\\MinhaApp".
// O separador no fim evita que "RunOnce" case com "Run" — sem ele, "Run" seria
// prefixo textual de "RunOnce" e a distincao entre as duas regras sumiria.
func subtreeMatches(upperPath, prefix string) bool {
	p := strings.ToUpper(strings.Trim(prefix, "\\"))
	if p == "" {
		return false
	}
	// Casa o caminho E TODAS as suas "raizes efetivas".
	//
	// Por que isto e necessario: um hive tem apelidos estruturais. HKU contem um
	// ramo por SID, e "HKU\<SID>\SOFTWARE\...\Run" e o MESMO valor que
	// "HKCU\SOFTWARE\...\Run". HKCR e um espelho de
	// "HKLM\SOFTWARE\Classes". HKCC aponta para um perfil de hardware dentro
	// de "HKLM\SYSTEM\CurrentControlSet\Hardware Profiles\Current".
	//
	// Sem isto, regras ancoradas na raiz do caminho (ex.: "...\CurrentVersion\Run")
	// sao CONTORNAVEIS navegando pelo alias: a protecao de escrita em Run valia em
	// HKLM e HKCU, mas nao em HKU\<SID>\...\Run — que e exatamente a chave de
	// autostart do usuario. Foi um bypass real, encontrado em revisao.
	for _, candidate := range effectiveRoots(upperPath) {
		if candidate == p || strings.HasPrefix(candidate, p+"\\") {
			return true
		}
	}
	return false
}

// effectiveRoots devolve o caminho e as variacoes obtidas removendo prefixos
// estruturais de hive (SID de usuario, ".DEFAULT", "SOFTWARE\Classes").
//
// A remocao e aplicada repetidamente porque os prefixos podem se acumular
// (ex.: HKU\<SID>_Classes e uma variacao de HKCR dentro do HKU).
func effectiveRoots(upperPath string) []string {
	roots := []string{upperPath}
	current := upperPath

	// Remove ate 3 prefixos encadeados (limite defensivo: a estrutura real nao
	// passa disso, e um limite evita trabalho sem fim em caminho patologico).
	for i := 0; i < 3; i++ {
		stripped := stripStructuralPrefix(current)
		if stripped == "" || stripped == current {
			break
		}
		current = stripped
		roots = append(roots, current)
	}
	return roots
}

// stripStructuralPrefix remove UM prefixo estrutural conhecido, se presente.
//
// Cobre:
//   - SID de usuario:      "S-1-5-21-...-1001\SOFTWARE\..." -> "SOFTWARE\..."
//   - SID + sufixo _Classes: "S-1-5-21-...-1001_Classes\..."   -> "SOFTWARE\..."
//   - perfil padrao:        ".DEFAULT\SOFTWARE\..."            -> "SOFTWARE\..."
//   - espelho do HKCR:      "SOFTWARE\CLASSES\..."             -> o que vem depois
func stripStructuralPrefix(p string) string {
	// SID: comeca com "S-1-" e o primeiro componente e o SID inteiro.
	if strings.HasPrefix(p, "S-1-") {
		idx := strings.Index(p, "\\")
		if idx <= 0 {
			return ""
		}
		sid := p[:idx]
		rest := p[idx+1:]
		// "...-1001_CLASSES" e a visao de CLASSES do usuario, equivalente ao
		// HKCR (associacoes de arquivo/COM). O conteudo NAO inclui a subarvore
		// SOFTWARE\Microsoft\Windows, entao nao ha caminho de autostart aqui.
		// Ainda assim mapeamos para o prefixo de Classes para que as regras de
		// sufixo (ex.: IFEO) continuem valendo dentro dessa visao.
		if strings.HasSuffix(sid, "_CLASSES") {
			return "SOFTWARE\\CLASSES\\" + rest
		}
		return rest
	}
	// ".DEFAULT" e o perfil padrao carregado em HKU.
	if strings.HasPrefix(p, ".DEFAULT\\") {
		return p[len(".DEFAULT\\"):]
	}
	if p == ".DEFAULT" {
		return ""
	}
	// HKCR e um espelho de HKLM\SOFTWARE\Classes: o conteudo de
	// "HKCR\X" tambem e alcancavel como "HKLM\SOFTWARE\Classes\X".
	if strings.HasPrefix(p, "SOFTWARE\\CLASSES\\") {
		return p[len("SOFTWARE\\CLASSES\\"):]
	}
	if p == "SOFTWARE\\CLASSES" {
		return "SOFTWARE"
	}
	return ""
}

// Writable informa se a mutacao e permitida aqui (para a UI desabilitar botoes).
func Writable(hive Hive, keyPath string) (bool, string) {
	decision := Evaluate(hive, keyPath, OpWrite)
	if decision.Allowed {
		return true, ""
	}
	return false, decision.Reason
}
