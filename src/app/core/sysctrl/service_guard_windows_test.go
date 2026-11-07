//go:build windows

package sysctrl

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Testes do guard de controle remoto de serviços (F7).
//
// O guard é PURO de propósito: estes testes rodam em qualquer máquina Windows
// (inclusive CI e a estação do dev) SEM tocar no SCM e SEM parar serviço real.
// Testar com serviço real seria inaceitável: um "stop NTDS" de verdade
// derrubaria a autenticação da floresta.
// ─────────────────────────────────────────────────────────────────────────────

func defaultPolicy() ServiceGuardPolicy { return DefaultServiceGuardPolicy() }

// ── 1. Ação inválida ────────────────────────────────────────────────────────

func TestEvaluateServiceAction_AcaoInvalidaRecusa(t *testing.T) {
	for _, action := range []ServiceAction{"", "enable", "disable", "delete", "START ", " reboot", "stop x"} {
		d := EvaluateServiceAction(defaultPolicy(), "spooler", action, ProductTypeWorkstation)
		if d.Allowed {
			t.Errorf("acao %q deveria ser recusada", string(action))
		}
		if d.MatchedList != "acao-invalida" {
			t.Errorf("acao %q: MatchedList = %q, esperado acao-invalida", string(action), d.MatchedList)
		}
	}
}

func TestParseServiceAction(t *testing.T) {
	cases := []struct {
		raw    string
		want   ServiceAction
		wantOk bool
	}{
		{"start", ActionStart, true},
		{"stop", ActionStop, true},
		{"restart", ActionRestart, true},
		{"START", ActionStart, true},
		{"  Stop  ", ActionStop, true},
		{"ReStart", ActionRestart, true},
		{"", "", false},
		{"   ", "", false},
		{"enable", "", false},
		{"reboot", "", false},
		{"startx", "", false},
	}
	for _, tc := range cases {
		got, ok := ParseServiceAction(tc.raw)
		if ok != tc.wantOk || got != tc.want {
			t.Errorf("ParseServiceAction(%q) = (%q, %v), esperado (%q, %v)", tc.raw, got, ok, tc.want, tc.wantOk)
		}
	}
}

// ── 2. Nome vazio ───────────────────────────────────────────────────────────

func TestEvaluateServiceAction_NomeVazioRecusa(t *testing.T) {
	for _, name := range []string{"", "   ", "\t"} {
		d := EvaluateServiceAction(defaultPolicy(), name, ActionStart, ProductTypeWorkstation)
		if d.Allowed {
			t.Errorf("nome %q deveria ser recusado", name)
		}
		if d.MatchedList != "nome-vazio" {
			t.Errorf("nome %q: MatchedList = %q, esperado nome-vazio", name, d.MatchedList)
		}
	}
}

// ── 3. FAIL-CLOSED: serviço desconhecido é recusado ─────────────────────────

func TestEvaluateServiceAction_FailClosedServicoDesconhecido(t *testing.T) {
	desconhecidos := []string{
		"meu-servico-inventado",
		"totally-made-up",
		"notepad",
		"chrome",
		"svchost",
		"a",
		"spooler2",
		"spooler ",
	}
	// truque: "spooler " normaliza para "spooler" e DEVE ser aceito; testamos
	// isso separadamente. Aqui só os realmente desconhecidos.
	desconhecidos = desconhecidos[:len(desconhecidos)-1]

	for _, name := range desconhecidos {
		for _, pt := range []string{ProductTypeWorkstation, ProductTypeServerNT, ProductTypeLanmanNT, ProductTypeUnknown} {
			for _, action := range []ServiceAction{ActionStart, ActionStop, ActionRestart} {
				d := EvaluateServiceAction(defaultPolicy(), name, action, pt)
				if d.Allowed {
					t.Errorf("FAIL-CLOSED violado: servico %q acao %q productType %q foi AUTORIZADO", name, action, pt)
				}
				if d.MatchedList != "ausente" {
					t.Errorf("servico %q: MatchedList = %q, esperado ausente", name, d.MatchedList)
				}
				if !strings.Contains(d.Reason, "fail-closed") {
					t.Errorf("servico %q: motivo deveria mencionar fail-closed, veio %q", name, d.Reason)
				}
			}
		}
	}
}

// ── 4. Denylist SEMPRE (qualquer máquina) ───────────────────────────────────

func TestEvaluateServiceAction_DenylistSempre(t *testing.T) {
	criticos := []string{
		"discovery-service", "discoveryagent",
		"rpcss", "winmgmt", "eventlog", "plugplay", "winlogon",
	}
	// Testa em CLIENTE, SERVIDOR MEMBRO e DC: o bloqueio vale nos três.
	for _, pt := range []string{ProductTypeWorkstation, ProductTypeServerNT, ProductTypeLanmanNT, ProductTypeUnknown} {
		for _, name := range criticos {
			for _, action := range []ServiceAction{ActionStart, ActionStop, ActionRestart} {
				d := EvaluateServiceAction(defaultPolicy(), name, action, pt)
				if d.Allowed {
					t.Errorf("servico critico %q foi AUTORIZADO em productType %q acao %q", name, pt, action)
				}
				if d.MatchedList != "denylist-sempre" {
					t.Errorf("servico %q productType %q: MatchedList = %q, esperado denylist-sempre", name, pt, d.MatchedList)
				}
			}
		}
	}
}

// TestEvaluateServiceAction_DenylistSempreCaseInsensitive garante que a
// variação de caixa/espaços do console não fura a denylist.
func TestEvaluateServiceAction_DenylistSempreCaseInsensitive(t *testing.T) {
	for _, name := range []string{"RpcSs", "RPCCS", "  rpcss  ", "WinMgmt", "EventLog", "PlugPlay", "Winlogon", "Discovery-Service"} {
		d := EvaluateServiceAction(defaultPolicy(), name, ActionStop, ProductTypeWorkstation)
		if d.Allowed {
			t.Errorf("variacao %q furou a denylist", name)
		}
	}
}

// ── 5. Denylist de DC (o caso mais perigoso) ────────────────────────────────

func TestEvaluateServiceAction_DenylistDC_BloqueiaEmDC(t *testing.T) {
	dcCriticos := []string{
		"ntds", "netlogon", "kdc", "dns", "w32time", "ismserv",
		"adws", "dfsr", "lanmanserver", "lanmanworkstation", "winrm",
	}
	for _, name := range dcCriticos {
		for _, action := range []ServiceAction{ActionStart, ActionStop, ActionRestart} {
			d := EvaluateServiceAction(defaultPolicy(), name, action, ProductTypeLanmanNT)
			if d.Allowed {
				t.Errorf("DC: servico %q acao %q foi AUTORIZADO (deveria ser bloqueado)", name, action)
			}
			if d.MatchedList != "denylist-dc" {
				t.Errorf("DC: servico %q: MatchedList = %q, esperado denylist-dc", name, d.MatchedList)
			}
		}
	}
}

// TestEvaluateServiceAction_DenylistDC_NaoVazaParaMembro é o teste de
// NÃO-REGRESSÃO mais importante: serviços liberados na allowlist (w32time,
// lanmanserver, lanmanworkstation, winrm) DEVEM ser controláveis em um
// servidor membro, e bloqueados apenas em DC.
func TestEvaluateServiceAction_DenylistDC_NaoVazaParaMembro(t *testing.T) {
	liberaveisEmMembro := []string{"w32time", "lanmanserver", "lanmanworkstation", "winrm"}
	for _, name := range liberaveisEmMembro {
		d := EvaluateServiceAction(defaultPolicy(), name, ActionStop, ProductTypeServerNT)
		if !d.Allowed {
			t.Errorf("SERVIDOR MEMBRO: servico %q deveria ser autorizado, mas foi recusado (%s)", name, d.MatchedList)
		}
		if d.MatchedList != "allowlist" {
			t.Errorf("SERVIDOR MEMBRO: servico %q: MatchedList = %q, esperado allowlist", name, d.MatchedList)
		}

		// E em CLIENTE também.
		dCli := EvaluateServiceAction(defaultPolicy(), name, ActionStop, ProductTypeWorkstation)
		if !dCli.Allowed {
			t.Errorf("CLIENTE: servico %q deveria ser autorizado, mas foi recusado (%s)", name, dCli.MatchedList)
		}
	}
}

// TestEvaluateServiceAction_ProductTypeVazioNaoEhDC: um ProductType
// desconhecido/vazio NÃO deve conceder o benefício de "não é DC" nem ativar a
// denylist de DC por engano. Como o guard é fail-closed, o resultado seguro é
// que serviços só-da-denylist-DC (ex.: ntds) caiam em "ausente" — e serviços da
// allowlist continuem funcionando.
func TestEvaluateServiceAction_ProductTypeVazioNaoEhDC(t *testing.T) {
	d := EvaluateServiceAction(defaultPolicy(), "ntds", ActionStop, "")
	if d.Allowed {
		t.Fatal("ntds NAO pode ser autorizado com ProductType vazio")
	}
	if d.IsDomainController {
		t.Error("ProductType vazio nao deve ser tratado como DC")
	}
	if d.MatchedList != "ausente" {
		t.Errorf("MatchedList = %q, esperado ausente (ntds nao esta na allowlist)", d.MatchedList)
	}
	if d.ProductType != ProductTypeUnknown {
		t.Errorf("ProductType = %q, esperado %q", d.ProductType, ProductTypeUnknown)
	}
}

// ── 6. Allowlist ────────────────────────────────────────────────────────────

func TestEvaluateServiceAction_AllowlistAutoriza(t *testing.T) {
	cases := []struct {
		name        string
		productType string
	}{
		{"spooler", ProductTypeWorkstation},
		{"spooler", ProductTypeServerNT},
		{"spooler", ProductTypeLanmanNT},
		{"wuauserv", ProductTypeServerNT},
		{"wuauserv", ProductTypeLanmanNT},
		{"bits", ProductTypeWorkstation},
		{"dnscache", ProductTypeServerNT},
	}
	for _, tc := range cases {
		for _, action := range []ServiceAction{ActionStart, ActionStop, ActionRestart} {
			d := EvaluateServiceAction(defaultPolicy(), tc.name, action, tc.productType)
			if !d.Allowed {
				t.Errorf("%q em %q acao %q deveria ser autorizado (MatchedList=%s)", tc.name, tc.productType, action, d.MatchedList)
			}
			if d.MatchedList != "allowlist" {
				t.Errorf("%q: MatchedList = %q, esperado allowlist", tc.name, d.MatchedList)
			}
		}
	}
}

func TestEvaluateServiceAction_AllowlistCaseInsensitiveEEspacos(t *testing.T) {
	for _, name := range []string{"Spooler", "SPOOLER", "  spooler  ", "\tSpooler"} {
		d := EvaluateServiceAction(defaultPolicy(), name, ActionRestart, ProductTypeWorkstation)
		if !d.Allowed {
			t.Errorf("variacao %q deveria ser autorizada", name)
		}
		if d.ServiceName != "spooler" {
			t.Errorf("variacao %q: ServiceName normalizado = %q, esperado spooler", name, d.ServiceName)
		}
	}
}

// ── 7. Runtime do Go ────────────────────────────────────────────────────────

func TestEvaluateServiceAction_RuntimeGoBloqueado(t *testing.T) {
	for _, name := range []string{"gc", "GC", " runtime "} {
		d := EvaluateServiceAction(defaultPolicy(), name, ActionStop, ProductTypeWorkstation)
		if d.Allowed {
			t.Errorf("servico sintetico %q do runtime Go foi AUTORIZADO", name)
		}
		if d.MatchedList != "denylist-agente-runtime" {
			t.Errorf("%q: MatchedList = %q, esperado denylist-agente-runtime", name, d.MatchedList)
		}
	}
}

// ── 8. Invariantes estruturais das listas ───────────────────────────────────

// TestGuardLists_SemSobreposicaoInesperada garante que nenhum serviço esteja
// simultaneamente na allowlist e nas denylists "sempre"/"runtime". A
// sobreposição com a denylist de DC é INTENCIONAL e documentada (w32time,
// lanmanserver, lanmanworkstation, winrm) — ela é justamente o mecanismo que
// torna esses serviços controláveis em membro e bloqueados em DC.
func TestGuardLists_SemSobreposicaoInesperada(t *testing.T) {
	p := defaultPolicy()

	for _, name := range AllowedServiceNames() {
		if reason, ok := p.AlwaysDenied[name]; ok {
			t.Errorf("servico %q esta na allowlist E na denylist-sempre (%s) — contradicao", name, reason)
		}
		if reason, ok := p.RuntimeDenied[name]; ok {
			t.Errorf("servico %q esta na allowlist E na denylist-runtime (%s) — contradicao", name, reason)
		}
	}

	// A sobreposição allowlist × denylist-DC deve ser EXATAMENTE o conjunto
	// documentado. Se alguém adicionar um serviço à denylist de DC sem
	// documentar, este teste avisa.
	esperadaSobreposicao := map[string]bool{
		"w32time": true, "lanmanserver": true, "lanmanworkstation": true, "winrm": true,
	}
	for _, name := range AllowedServiceNames() {
		_, emDC := p.DCDenied[name]
		if emDC != esperadaSobreposicao[name] {
			t.Errorf("sobreposicao allowlist x denylist-DC mudou para %q (emDC=%v, esperado=%v) — atualize a documentacao e este teste",
				name, emDC, esperadaSobreposicao[name])
		}
	}
}

// TestGuardLists_NaoVazias trava as listas contra remoção acidental.
func TestGuardLists_NaoVazias(t *testing.T) {
	if got := len(AlwaysDeniedServiceNames()); got < 7 {
		t.Errorf("denylist-sempre tem %d itens, esperado >= 7", got)
	}
	if got := len(DomainControllerDeniedServiceNames()); got < 11 {
		t.Errorf("denylist-DC tem %d itens, esperado >= 11", got)
	}
	if got := len(AllowedServiceNames()); got < 8 {
		t.Errorf("allowlist tem %d itens, esperado >= 8", got)
	}
	if got := len(AgentRuntimeDeniedServiceNames()); got < 2 {
		t.Errorf("denylist-runtime tem %d itens, esperado >= 2", got)
	}
}

// TestGuardLists_Ordenadas garante que as listas expostas são estáveis
// (determinísticas), para o console poder exibi-las sem ruído.
func TestGuardLists_Ordenadas(t *testing.T) {
	check := func(nome string, got []string) {
		for i := 1; i < len(got); i++ {
			if got[i-1] >= got[i] {
				t.Errorf("%s nao esta ordenada: %q >= %q (indice %d)", nome, got[i-1], got[i], i)
			}
		}
	}
	check("AlwaysDenied", AlwaysDeniedServiceNames())
	check("DCDenied", DomainControllerDeniedServiceNames())
	check("RuntimeDenied", AgentRuntimeDeniedServiceNames())
	check("Allowed", AllowedServiceNames())
}

// ── 9. Todo serviço das listas tem motivo não-trivial ───────────────────────

func TestGuardLists_TodosTemMotivo(t *testing.T) {
	p := defaultPolicy()
	verificar := func(origem string, lista map[string]string) {
		for name, reason := range lista {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s: servico %q sem motivo", origem, name)
			}
			if len(reason) < 20 {
				t.Errorf("%s: servico %q tem motivo curto demais (%q) — precisa ser auditavel", origem, name, reason)
			}
		}
	}
	verificar("denylist-sempre", p.AlwaysDenied)
	verificar("denylist-dc", p.DCDenied)
	verificar("denylist-runtime", p.RuntimeDenied)
}

// ── 10. Matriz exaustiva: todo serviço conhecido × toda ação × todo ProductType

// TestEvaluateServiceAction_MatrizExaustivaConsistencia roda o produto
// cartesiano completo e afirma invariantes globais. É a rede de segurança que
// pega qualquer combinação não pensada explicitamente.
func TestEvaluateServiceAction_MatrizExaustivaConsistencia(t *testing.T) {
	nomes := map[string]bool{}
	for _, n := range AlwaysDeniedServiceNames() {
		nomes[n] = true
	}
	for _, n := range DomainControllerDeniedServiceNames() {
		nomes[n] = true
	}
	for _, n := range AgentRuntimeDeniedServiceNames() {
		nomes[n] = true
	}
	for _, n := range AllowedServiceNames() {
		nomes[n] = true
	}
	nomes["servico-desconhecido-xyz"] = true

	productTypes := []string{ProductTypeWorkstation, ProductTypeServerNT, ProductTypeLanmanNT, ProductTypeUnknown, ""}
	actions := []ServiceAction{ActionStart, ActionStop, ActionRestart}
	p := defaultPolicy()

	for nome := range nomes {
		for _, pt := range productTypes {
			for _, action := range actions {
				d := EvaluateServiceAction(p, nome, action, pt)

				// Se autorizado, NÃO pode estar em nenhuma denylist aplicável.
				if d.Allowed {
					if _, bad := p.AlwaysDenied[d.ServiceName]; bad {
						t.Errorf("AUTORIZADO mas esta na denylist-sempre: %q pt=%q", d.ServiceName, pt)
					}
					if _, bad := p.RuntimeDenied[d.ServiceName]; bad {
						t.Errorf("AUTORIZADO mas esta na denylist-runtime: %q pt=%q", d.ServiceName, pt)
					}
					if d.IsDomainController {
						if _, bad := p.DCDenied[d.ServiceName]; bad {
							t.Errorf("AUTORIZADO em DC mas esta na denylist-dc: %q", d.ServiceName)
						}
					}
					if _, ok := p.Allowed[d.ServiceName]; !ok {
						t.Errorf("AUTORIZADO mas ausente da allowlist: %q", d.ServiceName)
					}
					if d.MatchedList != "allowlist" {
						t.Errorf("AUTORIZADO com MatchedList inesperado: %q", d.MatchedList)
					}
				} else {
					// Se recusado, o motivo não pode ser vazio.
					if strings.TrimSpace(d.Reason) == "" {
						t.Errorf("RECUSADO sem motivo: %q acao=%q pt=%q", nome, action, pt)
					}
					if d.MatchedList == "allowlist" {
						t.Errorf("RECUSADO mas MatchedList=allowlist: %q", nome)
					}
				}
			}
		}
	}
}

// ── 11. Cenário real: DC recebendo stop em NTDS (o caso do enunciado) ───────

// TestCenarioReal_DCStopNTDS_Recusado reproduz explicitamente o cenário que o
// usuário apontou como inaceitável: "um stop em NTDS/KDC/Netlogon num DC
// DERRUBA a autenticação da floresta".
func TestCenarioReal_DCStopNTDS_Recusado(t *testing.T) {
	for _, servico := range []string{"NTDS", "KDC", "Netlogon", "DNS", "W32Time"} {
		d := EvaluateServiceAction(defaultPolicy(), servico, ActionStop, ProductTypeLanmanNT)
		if d.Allowed {
			t.Fatalf("INACEITAVEL: stop em %q num DC foi autorizado — derrubaria a autenticacao da floresta", servico)
		}
		if d.MatchedList != "denylist-dc" {
			t.Errorf("%q: MatchedList = %q, esperado denylist-dc", servico, d.MatchedList)
		}
		if !d.IsDomainController {
			t.Errorf("%q: IsDomainController deveria ser true", servico)
		}
	}
}

// TestCenarioReal_ClienteStopSpooler_Autorizado cobre o caso de uso legítimo
// que justifica a feature (destravar fila de impressão num cliente).
func TestCenarioReal_ClienteStopSpooler_Autorizado(t *testing.T) {
	d := EvaluateServiceAction(defaultPolicy(), "Spooler", ActionRestart, ProductTypeWorkstation)
	if !d.Allowed {
		t.Fatalf("restart do Spooler num cliente deveria ser autorizado, foi recusado: %s", d.Reason)
	}
	if d.Action != ActionRestart {
		t.Errorf("Action = %q, esperado restart", d.Action)
	}
}

// ── 12. Normalização do ProductType ─────────────────────────────────────────

// TestNormalizeProductTypeValue cobre os valores canônicos do Windows e as
// variações de formatação. Espelha o teste de native (winserver_windows_test.go)
// de propósito: as duas leituras precisam concordar, senão o inventário diria
// "DC" e o guard diria "não é DC" (ou o contrário).
func TestNormalizeProductTypeValue(t *testing.T) {
	cases := []struct {
		nome string
		raw  string
		want string
	}{
		{"workstation canonico", "WinNT", ProductTypeWorkstation},
		{"servidor membro canonico", "ServerNT", ProductTypeServerNT},
		{"controlador de dominio canonico", "LanmanNT", ProductTypeLanmanNT},
		{"caixa baixa", "winnt", ProductTypeWorkstation},
		{"caixa alta", "SERVERNT", ProductTypeServerNT},
		{"caixa mista", "LaNmAnNt", ProductTypeLanmanNT},
		{"com espacos em volta", "  LanmanNT  ", ProductTypeLanmanNT},
		{"vazio", "", ProductTypeUnknown},
		{"so espacos", "   ", ProductTypeUnknown},
		{"valor inventado", "Server", ProductTypeUnknown},
		{"parecido mas errado", "WinNT5", ProductTypeUnknown},
		{"nulo textual", "null", ProductTypeUnknown},
		{"separador errado", "Lanman-NT", ProductTypeUnknown},
	}
	for _, tc := range cases {
		if got := normalizeProductTypeValue(tc.raw); got != tc.want {
			t.Errorf("%s: normalizeProductTypeValue(%q) = %q, esperado %q", tc.nome, tc.raw, got, tc.want)
		}
	}
}

// TestDetectProductType_NaoVazio garante que a leitura REAL (registry) numa
// máquina Windows devolve sempre um valor canônico — nunca string vazia nem
// lixo. Roda em cliente Windows (WinNT) e em servidor (ServerNT/LanmanNT).
func TestDetectProductType_NaoVazio(t *testing.T) {
	got := DetectProductType()
	switch got {
	case ProductTypeWorkstation, ProductTypeServerNT, ProductTypeLanmanNT, ProductTypeUnknown:
		// ok
	default:
		t.Fatalf("DetectProductType() = %q, esperado um valor canonico", got)
	}
}

// TestIsDomainController_ConsistenteComDetectProductType trava a coerência
// entre as duas funções exportadas.
func TestIsDomainController_ConsistenteComDetectProductType(t *testing.T) {
	want := DetectProductType() == ProductTypeLanmanNT
	if got := IsDomainController(); got != want {
		t.Errorf("IsDomainController() = %v, mas DetectProductType()==LanmanNT e %v", got, want)
	}
}

// ── 13. ApplyServiceAction: a recusa NUNCA toca no SCM ──────────────────────

// TestApplyServiceAction_RecusaNaoExecuta é o teste de segurança central do
// executor: para todo serviço bloqueado, a função deve devolver
// Executed=false, Err=nil (não é erro de SCM) e — o ponto crítico — NÃO chamar
// StartService/StopService/RestartService.
//
// Como não dá para "espionar" as funções do pacote sem injetá-las, o teste usa
// o fato de que os serviços escolhidos NÃO EXISTEM nesta máquina como nomes
// NT, mas estariam na denylist: se o guard falhasse e o SCM fosse chamado, o
// erro seria do SCM (Err != nil) e não da política. Assertamos Err == nil.
func TestApplyServiceAction_RecusaNaoExecuta(t *testing.T) {
	casos := []struct {
		nome        string
		productType string
		wantList    string
	}{
		// denylist-sempre
		{"rpcss", ProductTypeWorkstation, "denylist-sempre"},
		{"winmgmt", ProductTypeServerNT, "denylist-sempre"},
		{"eventlog", ProductTypeLanmanNT, "denylist-sempre"},
		{"discovery-service", ProductTypeWorkstation, "denylist-sempre"},
		// denylist-dc
		{"ntds", ProductTypeLanmanNT, "denylist-dc"},
		{"netlogon", ProductTypeLanmanNT, "denylist-dc"},
		{"kdc", ProductTypeLanmanNT, "denylist-dc"},
		// runtime do Go
		{"gc", ProductTypeWorkstation, "denylist-agente-runtime"},
		// fail-closed
		{"servico-que-nao-existe-xyz", ProductTypeWorkstation, "ausente"},
	}

	for _, tc := range casos {
		for _, action := range []ServiceAction{ActionStart, ActionStop, ActionRestart} {
			got := ApplyServiceAction(tc.nome, action, tc.productType)

			if got.Executed {
				t.Errorf("SEGURANCA: %q acao=%q pt=%q foi EXECUTADO (deveria ser recusado)", tc.nome, action, tc.productType)
			}
			if got.Err != nil {
				t.Errorf("SEGURANCA: %q acao=%q devolveu erro de SCM (%v) — significa que o guard NAO barrou antes de tocar no SCM",
					tc.nome, action, got.Err)
			}
			if got.Decision.Allowed {
				t.Errorf("SEGURANCA: %q acao=%q Decision.Allowed=true (deveria ser recusado)", tc.nome, action)
			}
			if got.Decision.MatchedList != tc.wantList {
				t.Errorf("%q acao=%q: MatchedList = %q, esperado %q", tc.nome, action, got.Decision.MatchedList, tc.wantList)
			}
		}
	}
}

// TestApplyServiceAction_AcaoInvalidaNaoExecuta garante que uma ação não
// reconhecida não cai em nenhum default perigoso.
func TestApplyServiceAction_AcaoInvalidaNaoExecuta(t *testing.T) {
	for _, action := range []ServiceAction{"", "enable", "delete", "reboot"} {
		got := ApplyServiceAction("spooler", action, ProductTypeWorkstation)
		if got.Executed {
			t.Errorf("acao invalida %q foi EXECUTADA", string(action))
		}
		if got.Err != nil {
			t.Errorf("acao invalida %q devolveu erro de SCM (%v) — nao deveria chegar ao SCM", string(action), got.Err)
		}
		if got.Decision.MatchedList != "acao-invalida" {
			t.Errorf("acao invalida %q: MatchedList = %q, esperado acao-invalida", string(action), got.Decision.MatchedList)
		}
	}
}

// TestApplyServiceAction_DecisaoSemprePresente garante que o resultado sempre
// traz a decisão preenchida — é ela que alimenta o log de auditoria.
func TestApplyServiceAction_DecisaoSemprePresente(t *testing.T) {
	for _, action := range []ServiceAction{ActionStart, ActionStop, ActionRestart, "lixo"} {
		got := ApplyServiceAction("servico-inexistente-abc", action, ProductTypeUnknown)
		if strings.TrimSpace(got.Decision.Reason) == "" {
			t.Errorf("acao %q: Decision.Reason vazio — auditoria ficaria sem motivo", string(action))
		}
		if got.Decision.ServiceName == "" {
			t.Errorf("acao %q: Decision.ServiceName vazio", string(action))
		}
		if got.Decision.ProductType == "" {
			t.Errorf("acao %q: Decision.ProductType vazio", string(action))
		}
	}
}

// ── 14. Bateria adversarial final ───────────────────────────────────────────

// TestAdversarial_BypassTentativas tenta ativamente furar o guard com as
// técnicas mais óbvias de bypass: variação de caixa, espaços e tabs. O guard
// normaliza antes de comparar, então todas DEVEM ser recusadas.
func TestAdversarial_BypassTentativas(t *testing.T) {
	p := defaultPolicy()

	tentativas := []struct {
		nome string
		desc string
		emDC bool
	}{
		{"rpcss ", "espaco a direita", false},
		{" rpcss", "espaco a esquerda", false},
		{"  RPCSS  ", "espacos e caixa", false},
		{"RpcSs", "caixa mista", false},
		{"\trpcss", "tab", false},
		{"NTDS", "caixa alta em DC", true},
		{"Ntds ", "caixa mista com espaco em DC", true},
		{" gC ", "runtime com espaco", false},
	}

	for _, tt := range tentativas {
		pt := ProductTypeWorkstation
		if tt.emDC {
			pt = ProductTypeLanmanNT
		}
		d := EvaluateServiceAction(p, tt.nome, ActionStop, pt)
		if d.Allowed {
			t.Errorf("BYPASS: %q (%s) foi autorizado", tt.nome, tt.desc)
		}
	}
}

// TestAdversarial_NomesParecidosNaoColidem garante que nomes PARECIDOS com
// serviços críticos NÃO sejam tratados como eles. O guard compara igualdade
// exata no mapa — não prefixo nem substring — então um nome parecido cai no
// fail-closed (recusado), que é o comportamento seguro.
//
// Isto é importante nos DOIS sentidos: "spooler2" não pode ser liberado por
// começar com "spooler", e "rpc" não pode escapar do bloqueio por ser um
// prefixo de "rpcss".
func TestAdversarial_NomesParecidosNaoColidem(t *testing.T) {
	parecidos := []string{
		"rpcss2", "rpc", "rpcssx", "ntds2", "ntd", "kdc2", "netlogon-old",
		"spoolerx", "spooler2", "wuauserv2", "gc2", "gcx", "runtimex",
	}
	for _, nome := range parecidos {
		d := EvaluateServiceAction(defaultPolicy(), nome, ActionStop, ProductTypeLanmanNT)
		if d.Allowed {
			t.Errorf("nome parecido %q foi AUTORIZADO (deveria cair em fail-closed)", nome)
		}
		if d.MatchedList != "ausente" {
			t.Errorf("nome parecido %q: MatchedList = %q, esperado ausente", nome, d.MatchedList)
		}
	}
}

// TestAdversarial_DC_CobreTodosOsServicosDeAutenticacao trava o conjunto que o
// usuário nomeou explicitamente no pedido: NTDS/KDC/Netlogon e a lista completa.
func TestAdversarial_DC_CobreTodosOsServicosDeAutenticacao(t *testing.T) {
	exigidos := []string{
		"ntds", "netlogon", "kdc", "dns", "w32time", "ismserv",
		"rpcss", "lanmanserver", "lanmanworkstation", "winrm", "adws", "dfsr",
	}
	p := defaultPolicy()
	for _, s := range exigidos {
		d := EvaluateServiceAction(p, s, ActionStop, ProductTypeLanmanNT)
		if d.Allowed {
			t.Errorf("servico exigido %q NAO esta bloqueado em DC", s)
		}
	}
}
