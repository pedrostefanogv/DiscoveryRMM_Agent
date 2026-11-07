//go:build windows

package sysctrl

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// ─────────────────────────────────────────────────────────────────────────────
// GUARD DE CONTROLE REMOTO DE SERVIÇOS (F7)
//
// Este arquivo é PURO: não chama SCM, não lê o registro, não executa nada.
// Recebe o nome do serviço, a ação e o ProductType da máquina e devolve uma
// decisão. Isso o torna 100% testável sem tocar em serviço real — requisito
// explícito da task, porque um teste que parasse o NTDS de verdade derrubaria
// a autenticação de uma floresta.
//
// Postura: FAIL-CLOSED. O default é RECUSAR. Só executa o que estiver
// explicitamente autorizado por allowlist E não estiver em nenhuma denylist.
// ─────────────────────────────────────────────────────────────────────────────

// ServiceAction é a ação pedida pelo console.
type ServiceAction string

const (
	ActionStart   ServiceAction = "start"
	ActionStop    ServiceAction = "stop"
	ActionRestart ServiceAction = "restart"
)

// ParseServiceAction normaliza a string recebida do servidor.
// Devolve ok=false para qualquer coisa que não seja uma das três ações —
// nunca "adivinha" a intenção (mesma filosofia de normalizeProductType).
func ParseServiceAction(raw string) (ServiceAction, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "start":
		return ActionStart, true
	case "stop":
		return ActionStop, true
	case "restart":
		return ActionRestart, true
	default:
		return "", false
	}
}

// ProductType espelha os valores canônicos de
// HKLM\SYSTEM\CurrentControlSet\Control\ProductOptions\ProductType.
//
// Redefinidos aqui (em vez de importar inventory/native) de propósito: o
// pacote sysctrl não deve depender do coletor de inventário. O chamador faz a
// ponte — ver app.serviceGuardProductType().
const (
	ProductTypeWorkstation = "WinNT"    // cliente
	ProductTypeServerNT    = "ServerNT" // servidor membro (não é DC)
	ProductTypeLanmanNT    = "LanmanNT" // controlador de domínio
	ProductTypeUnknown     = "unknown"
)

// ─────────────────────────────────────────────────────────────────────────────
// DENYLIST 1 — SEMPRE bloqueados, em QUALQUER máquina (cliente ou servidor).
//
// Critério: parar esses serviços derruba o PRÓPRIO AGENTE ou o SO. Não há
// cenário operacional legítimo para um RMM pará-los remotamente, e o custo do
// erro é a máquina sair da rede (perdendo o canal de comando junto).
//
// Chaves em minúsculas: a comparação é case-insensitive.
// ─────────────────────────────────────────────────────────────────────────────
var alwaysDeniedServices = map[string]string{
	// ── O próprio agente ──
	"discovery-service": "e o servico do proprio Discovery Agent; para-lo interrompe o canal de comando e a maquina sai do RMM",
	"discoveryagent":    "e o servico do proprio Discovery Agent (nome curto); para-lo interrompe o canal de comando",

	// ── Núcleo do SO ──
	"rpcss":    "RPC (RpcSs) e dependencia de praticamente todo o Windows; para-lo derruba servicos em cascata",
	"winmgmt":  "WMI (Winmgmt) e usado pelo proprio inventario e por diagnostico do agente",
	"eventlog": "Windows Event Log (EventLog) e a auditoria do SO; para-lo cega a propria auditoria da acao",
	"plugplay": "Plug and Play (PlugPlay) nao pode ser parado de forma suportada; derruba a enumeracao de dispositivos",
	"winlogon": "Winlogon gerencia logon/logoff; para-lo derruba a sessao interativa e o desktop",
}

// ─────────────────────────────────────────────────────────────────────────────
// DENYLIST 2 — bloqueados SOMENTE em controlador de domínio (LanmanNT).
//
// Critério: parar QUALQUER um destes em um DC pode derrubar autenticação da
// floresta inteira. O usuário foi explícito sobre isso no pedido.
//
// ATENÇÃO ao caso do DNS: em um DC, o DNS costuma hospedar as zonas AD
// integradas. Parar o DNS no DC impede que os próprios DCs se localizem
// (os registros SRV ficam inalcançáveis), o que degrada a autenticação mesmo
// que o NTDS continue de pé. Por isso DNS está nesta lista, e não na geral.
//
// W32Time, LanmanServer e LanmanWorkstation aparecem AQUI e também na
// allowlist de propósito: são serviços legítimos de operar em um membro, mas
// nocivos em um DC. A denylist é avaliada antes, então vence em DC.
// ─────────────────────────────────────────────────────────────────────────────
var domainControllerDeniedServices = map[string]string{
	"ntds":              "AD DS (NTDS) e o diretorio; para-lo derruba a autenticacao do dominio",
	"netlogon":          "Netlogon e o canal de autenticacao das estacoes; para-lo derruba o logon no dominio",
	"kdc":               "Kerberos KDC emite os tickets de autenticacao; para-lo derruba todo o SSO do dominio",
	"dns":               "DNS em DC hospeda zonas AD integradas; para-lo impede a localizacao dos DCs (SRV) e degrada a autenticacao",
	"w32time":           "W32Time em DC e a fonte de tempo do dominio; a deriva quebra Kerberos (tolerancia de 5 min)",
	"ismserv":           "IsmServ e a replicacao entre sites do AD; para-lo quebra a convergencia do diretorio",
	"adws":              "ADWS e o gateway de servicos web do AD DS; para-lo quebra a gestao do dominio",
	"dfsr":              "DFSR replica SYSVOL e o proprio AD; para-lo impede a propagacao de politicas (GPO)",
	"lanmanserver":      "LanmanServer no DC serve SYSVOL/NETLOGON por SMB; para-lo impede que as estacoes leiam as GPOs",
	"lanmanworkstation": "LanmanWorkstation no DC e necessaria para acessar SYSVOL de outros DCs; para-la quebra a replicacao",
	"winrm":             "WinRM no DC e o canal de administracao remota; para-lo isola o DC",
}

// ─────────────────────────────────────────────────────────────────────────────
// DENYLIST 3 — serviços SINTÉTICOS do runtime Go do agente.
//
// O runtime do Go é exposto como serviços ("GC"/"runtime") para diagnóstico.
// "Parar" um deles não para um serviço do Windows: altera o tuning do coletor
// DENTRO do processo do agente (os.gcpercent). É um efeito colateral silencioso
// e indesejado para um comando que se apresenta como controle de serviço.
// ─────────────────────────────────────────────────────────────────────────────
var agentRuntimeDeniedServices = map[string]string{
	"gc":      "e o coletor de lixo do runtime Go do agente, nao um servico do Windows; a acao altera o tuning interno do processo",
	"runtime": "e um servico sintetico do runtime Go do agente, nao um servico do Windows",
}

// ─────────────────────────────────────────────────────────────────────────────
// ALLOWLIST — serviços que o console pode controlar.
//
// FAIL-CLOSED: um serviço fora desta lista é RECUSADO mesmo que não esteja em
// nenhuma denylist. A denylist é uma segunda barreira (defesa em profundidade),
// não a única.
//
// As chaves são os nomes NT internos (os usados por sc.exe / OpenService),
// que é exatamente o que sysctrl.StartService/StopService recebem.
// ─────────────────────────────────────────────────────────────────────────────
var allowedServices = map[string]string{
	// Impressão
	"spooler": "Spooler de impressao",

	// Aplicação/atualização
	"wuauserv": "Windows Update",
	"bits":     "Background Intelligent Transfer Service",
	"dnscache": "Cliente DNS (cache do resolver)",

	// Rede/compartilhamento de uso rotineiro (bloqueados em DC pela denylist)
	"w32time":           "Windows Time",
	"lanmanserver":      "Servidor (compartilhamento de arquivos)",
	"lanmanworkstation": "Estacao de trabalho",
	"winrm":             "WinRM (administracao remota)",
}

// ─────────────────────────────────────────────────────────────────────────────
// DECISÃO
// ─────────────────────────────────────────────────────────────────────────────

// ServiceGuardDecision é o resultado da avaliação. Sempre auditável: carrega
// a decisão, o motivo em PT-BR e a lista que decidiu.
type ServiceGuardDecision struct {
	// Allowed indica se a ação PODE ser executada.
	Allowed bool
	// ServiceName é o nome já normalizado (minúsculas, sem espaços).
	ServiceName string
	// Action é a ação normalizada.
	Action ServiceAction
	// Reason explica a decisão em texto claro (vai para o log de auditoria).
	Reason string
	// MatchedList identifica a lista que decidiu: "denylist-sempre",
	// "denylist-dc", "denylist-agente-runtime", "allowlist", "ausente",
	// "acao-invalida" ou "nome-vazio".
	MatchedList string
	// ProductType é o ProductType considerado na decisão.
	ProductType string
	// IsDomainController indica se a máquina foi tratada como DC.
	IsDomainController bool
}

// ServiceGuardPolicy encapsula as listas para permitir teste com políticas
// alternativas sem mutar o estado global do pacote.
type ServiceGuardPolicy struct {
	AlwaysDenied  map[string]string
	DCDenied      map[string]string
	RuntimeDenied map[string]string
	Allowed       map[string]string
}

// DefaultServiceGuardPolicy devolve a política de produção.
func DefaultServiceGuardPolicy() ServiceGuardPolicy {
	return ServiceGuardPolicy{
		AlwaysDenied:  alwaysDeniedServices,
		DCDenied:      domainControllerDeniedServices,
		RuntimeDenied: agentRuntimeDeniedServices,
		Allowed:       allowedServices,
	}
}

// EvaluateServiceAction é o guard propriamente dito. PURO: não executa nada,
// não toca no SCM, não lê o registro. Só classifica.
//
// Ordem de avaliação (a primeira que casa decide):
//  1. Ação válida?               → senão recusa (nunca "adivinha")
//  2. Nome não vazio?            → senão recusa
//  3. Denylist de runtime do Go  → recusa
//  4. Denylist SEMPRE            → recusa
//  5. Denylist de DC (se for DC) → recusa
//  6. Allowlist                  → autoriza
//  7. Ausente da allowlist       → recusa (FAIL-CLOSED)
//
// A denylist é checada ANTES da allowlist de propósito: se um serviço estiver
// nas duas, a denylist vence em DC.
func EvaluateServiceAction(policy ServiceGuardPolicy, serviceName string, action ServiceAction, productType string) ServiceGuardDecision {
	normalized := normalizeServiceName(serviceName)
	productType = strings.TrimSpace(productType)
	if productType == "" {
		productType = ProductTypeUnknown
	}
	isDC := productType == ProductTypeLanmanNT

	decision := ServiceGuardDecision{
		ServiceName:        normalized,
		Action:             action,
		ProductType:        productType,
		IsDomainController: isDC,
	}

	// 1) Ação inválida: recusa explícita (nunca trata como start por omissão).
	switch action {
	case ActionStart, ActionStop, ActionRestart:
	default:
		decision.Allowed = false
		decision.MatchedList = "acao-invalida"
		decision.Reason = fmt.Sprintf("acao %q nao reconhecida (use start, stop ou restart)", string(action))
		return decision
	}

	// 2) Nome vazio.
	if normalized == "" {
		decision.Allowed = false
		decision.MatchedList = "nome-vazio"
		decision.Reason = "nome do servico vazio"
		return decision
	}

	// 3) Runtime do Go (serviços sintéticos do agente).
	if reason, ok := policy.RuntimeDenied[normalized]; ok {
		decision.Allowed = false
		decision.MatchedList = "denylist-agente-runtime"
		decision.Reason = fmt.Sprintf("servico %q bloqueado: %s", normalized, reason)
		return decision
	}

	// 4) Denylist global — o agente ou o SO.
	if reason, ok := policy.AlwaysDenied[normalized]; ok {
		decision.Allowed = false
		decision.MatchedList = "denylist-sempre"
		decision.Reason = fmt.Sprintf("servico %q bloqueado em qualquer maquina: %s", normalized, reason)
		return decision
	}

	// 5) Denylist de DC.
	if isDC {
		if reason, ok := policy.DCDenied[normalized]; ok {
			decision.Allowed = false
			decision.MatchedList = "denylist-dc"
			decision.Reason = fmt.Sprintf("servico %q bloqueado em controlador de dominio: %s", normalized, reason)
			return decision
		}
	}

	// 6) Allowlist.
	if label, ok := policy.Allowed[normalized]; ok {
		decision.Allowed = true
		decision.MatchedList = "allowlist"
		decision.Reason = fmt.Sprintf("servico %q (%s) autorizado", normalized, label)
		return decision
	}

	// 7) Fail-closed.
	decision.Allowed = false
	decision.MatchedList = "ausente"
	decision.Reason = fmt.Sprintf("servico %q nao esta na allowlist deste agente; por seguranca a acao foi recusada (fail-closed)", normalized)
	return decision
}

// normalizeServiceName padroniza o nome do serviço para comparação: sem
// espaços nas pontas e em minúsculas. O Windows trata nomes de serviço de
// forma case-insensitive, então "Spooler" e "spooler" são o mesmo serviço.
//
// Não remove prefixos nem tenta "adivinhar" variações: um nome que não casa
// exatamente cai no fail-closed, que é o comportamento desejado.
func normalizeServiceName(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// AlwaysDeniedServiceNames devolve a lista ordenada de serviços bloqueados em
// qualquer máquina. Exposto para auditoria/documentação e para o console
// exibir ao operador.
func AlwaysDeniedServiceNames() []string { return sortedGuardKeys(alwaysDeniedServices) }

// DomainControllerDeniedServiceNames devolve a lista ordenada de serviços
// bloqueados quando a máquina é controlador de domínio.
func DomainControllerDeniedServiceNames() []string {
	return sortedGuardKeys(domainControllerDeniedServices)
}

// AgentRuntimeDeniedServiceNames devolve a lista ordenada de serviços
// sintéticos do runtime Go bloqueados.
func AgentRuntimeDeniedServiceNames() []string { return sortedGuardKeys(agentRuntimeDeniedServices) }

// AllowedServiceNames devolve a lista ordenada da allowlist.
func AllowedServiceNames() []string { return sortedGuardKeys(allowedServices) }

func sortedGuardKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// LEITURA DO PRODUCTTYPE
//
// O guard precisa do ProductType da máquina para decidir o caso do DC. A
// leitura é feita AQUI (e não importando inventory/native) por dois motivos:
//  1. sysctrl não deve depender do coletor de inventário — são camadas
//     independentes, e o inventário é pesado (WMI/COM/osquery);
//  2. permite ler o valor VIVO no momento do comando, em vez de confiar em
//     um inventário possivelmente antigo — um DC promovido/rebaixado depois do
//     último sync seria classificado errado, que é exatamente o erro que este
//     guard não pode cometer.
//
// A chave do registro é a MESMA consultada por native.detectProductType()
// (core/inventory/native/winserver_windows.go:15): uma fonte só de verdade no
// Windows.
// ─────────────────────────────────────────────────────────────────────────────

// productTypeKeyPath é a chave canônica do tipo de produto da instalação.
const productTypeKeyPath = `SYSTEM\CurrentControlSet\Control\ProductOptions`

// productTypeValueName é o nome do valor REG_SZ dentro de productTypeKeyPath.
const productTypeValueName = "ProductType"

// DetectProductType lê o ProductType canônico da máquina atual.
//
// Best-effort: qualquer falha (chave ausente, permissão, valor inesperado)
// devolve ProductTypeUnknown — nunca panic, nunca erro propagado. Como o guard
// é fail-closed, "unknown" NÃO concede privilégio nenhum: apenas deixa de
// aplicar a denylist específica de DC, e os serviços exclusivos dessa denylist
// caem em "ausente" (recusados de qualquer forma).
func DetectProductType() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, productTypeKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return ProductTypeUnknown
	}
	defer key.Close()

	raw, _, err := key.GetStringValue(productTypeValueName)
	if err != nil {
		return ProductTypeUnknown
	}
	return normalizeProductTypeValue(raw)
}

// normalizeProductTypeValue converte o valor bruto em um dos canônicos.
// É PURA (não acessa o sistema) para poder ser testada sem registry.
//
// A comparação ignora caixa e espaços porque imagens customizadas já
// apareceram com variações de formatação. Qualquer outra coisa (inclusive
// vazio) vira ProductTypeUnknown: nunca adivinhamos.
func normalizeProductTypeValue(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "winnt":
		return ProductTypeWorkstation
	case "servernt":
		return ProductTypeServerNT
	case "lanmannt":
		return ProductTypeLanmanNT
	default:
		return ProductTypeUnknown
	}
}

// IsDomainController informa se a máquina atual é controlador de domínio.
// Conveniência sobre DetectProductType.
func IsDomainController() bool {
	return DetectProductType() == ProductTypeLanmanNT
}
