package app

import (
	"strings"
	"testing"
)

// ui_watchdog_webview_orphan_test.go — Regressão do PRIMEIRO matador de webviews:
// agentWebviewOrphans (ui_watchdog_windows.go).

// O BUG DE PRODUÇÃO: com o agent fora do tray, o usuário abria o agent pelo Menu
// Iniciar e o MICROSOFT TEAMS FECHAVA. Log em %ProgramData%\Discovery\logs:
//   [webview] 24 browser process(es) orfaos de UIs mortas encerrados no startup
// (o normal são 5-8: só os webviews da própria UI). Medição nos 33
// msedgewebview2.exe vivos: Teams 8, Kaspersky 7, DISCOVERY 6, WhatsApp 6,
// SearchHost 6 — com 28 de 33 tendo outro msedgewebview2.exe como pai, o
// snapshot instantâneo perdia elos e a linhagem "batia no vazio", classificando
// webview de TERCEIRO como órfão nosso.

// CORREÇÃO A: exigir que o --user-data-dir aponte para dentro de
// <DataDir>\WebView2 antes de matar (trava ADICIONAL; a linhagem continua
// sendo exigida). Fail-safe: na dúvida, NÃO mata.

// COBERTURA DESTE ARQUIVO (honestidade sobre o que é medido):
//   cobre a DECISÃO (agentWebviewOrphans + classifyLineage), que é pura e
//   recebe o root por parâmetro. NÃO cobre o kill real
//   (killAgentWebviewOrphans → OpenProcess/TerminateProcess): isso é chamada de
//   kernel e não é exercitado aqui — o matador seguinte, que tem terminador
//   injetável, cobre o laço em webview_orphan_cleanup_test.go.

// Caminhos fixos dos cenários (determinístico: nada de ler o %ProgramData% da
// máquina — o root vem por parâmetro).
const (
	testRoot     = `C:\ProgramData\Discovery\WebView2`
	testOurUDD   = `C:\ProgramData\Discovery\WebView2\session-1\EBWebView`
	testTeamsUDD = `C:\Users\user\AppData\Local\Packages\MSTeams_8wekyb3d8bbwe\LocalCache\Microsoft\EdgeWebView\EBWebView`
	testWaUDD    = `C:\Users\user\AppData\Local\Packages\5319275A.WhatsAppDesktop_cv1g1gvanyjgm\LocalCache\EBWebView`
)

// ourWebview monta o registro de um msedgewebview2.exe NOSSO (UDD sob a raiz).
func ourWebview(pid, ppid uint32) procRecord {
	return procRecord{pid: pid, ppid: ppid, name: webviewOrphanName, cmdline: `--user-data-dir="` + testOurUDD + `"`}
}

// foreignWebview monta o registro de um msedgewebview2.exe de TERCEIRO.
func foreignWebview(pid, ppid uint32, udd string) procRecord {
	return procRecord{pid: pid, ppid: ppid, name: webviewOrphanName, cmdline: `--user-data-dir="` + udd + `"`}
}

func agentProc(pid uint32) procRecord {
	return procRecord{pid: pid, name: agentUIExecutableName}
}

// containsPid diz se o pid está no resultado (o resultado é pequeno, mas ele
// não vem ordenado).
func containsPid(pids []uint32, pid uint32) bool {
	for _, v := range pids {
		if v == pid {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────
// CASO 1 — O BUG DO TEAMS (o caso de produção; é este teste que prova o fix)
// ─────────────────────────────────────────────────────────────────────────

func TestAgentWebviewOrphans_TeamsWebviewWithDeadParentIsNotKilled(t *testing.T) {
	// Teams com o PAI AUSENTE do snapshot (é o cenário que fechava o Teams: a
	// linhagem bate no vazio e, SEM a trava do user-data-dir, virava órfão).
	procs := []procRecord{
		foreignWebview(5001, 9999, testTeamsUDD), // pai 9999 não existe no snapshot
		agentProc(100),                            // a UI do agent está viva
	}

	got := agentWebviewOrphans(procs, testRoot)

	if containsPid(got, 5001) {
		t.Fatalf("REGRESSÃO DO R7/TEAMS: webview do Teams (UDD fora da raiz do agent) foi classificado como órfão: %v", got)
	}
	if len(got) != 0 {
		t.Fatalf("esperado nenhum órfão, veio %v", got)
	}
}

func TestAgentWebviewOrphans_TeamsWebviewWithLiveForeignParentIsNotKilled(t *testing.T) {
	// Variante com o pai VIVO de outro app (explorer/services/ms-teams). Já era
	// coberta pela linhagem; mantida para travar o comportamento.
	procs := []procRecord{
		foreignWebview(5001, 42, testTeamsUDD),
		{pid: 42, ppid: 1, name: "explorer.exe"},
		agentProc(100),
	}

	if got := agentWebviewOrphans(procs, testRoot); len(got) != 0 {
		t.Fatalf("webview de terceiro com árvore viva não pode ser morto; veio %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// CASO 2 — ÓRFÃO REAL DO AGENT: CONTINUA SENDO MORTO
// ─────────────────────────────────────────────────────────────────────────
// A correção A é ADITIVA: não pode desligar a limpeza que existe para resolver
// o deadlock real do WebView2 (browser órfão segurando o lock da pasta de dados).

func TestAgentWebviewOrphans_AgentOrphanWithDeadParentIsStillKilled(t *testing.T) {
	// NOSSO webview (session-1\EBWebView) cujo pai (o agent) já morreu e não
	// está no snapshot.
	procs := []procRecord{
		ourWebview(6001, 7777), // pai 7777 (agent morto) ausente do snapshot
	}

	got := agentWebviewOrphans(procs, testRoot)

	if !containsPid(got, 6001) {
		t.Fatalf("órfão REAL do agent (pai morto + UDD sob a raiz) DEVE continuar sendo morto; veio %v", got)
	}
}

func TestAgentWebviewOrphans_AgentOrphanThroughOrphanBrowserChainIsStillKilled(t *testing.T) {
	// Cadeia real: browser → browser → agent morto. A subida tem de atravessar os
	// intermediários msedgewebview2.exe e ainda concluir lineageBroken.
	procs := []procRecord{
		ourWebview(6001, 6002),
		ourWebview(6002, 8888), // 8888 (agent morto) ausente
	}

	if got := agentWebviewOrphans(procs, testRoot); !containsPid(got, 6001) {
		t.Fatalf("órfão atrás de cadeia de browsers deveria ser morto; veio %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// CASO 3 — WEBVIEW DE TERCEIRO COM ÁRVORE VIVA: NUNCA MORTO
// ─────────────────────────────────────────────────────────────────────────

func TestAgentWebviewOrphans_ThirdPartyWebviewWithLiveTreeIsNotKilled(t *testing.T) {
	procs := []procRecord{
		foreignWebview(5001, 42, testWaUDD),
		{pid: 42, ppid: 1, name: "WhatsApp.exe"},
		agentProc(100),
	}

	if got := agentWebviewOrphans(procs, testRoot); len(got) != 0 {
		t.Fatalf("webview de terceiro com árvore viva não pode ser morto; veio %v", got)
	}
}

func TestAgentWebviewOrphans_LiveAgentAncestorIsNotKilled(t *testing.T) {
	// Webview da PRÓPRIA UI corrente (pai é um discovery-agent VIVO).
	procs := []procRecord{
		ourWebview(6001, 100),
		agentProc(100),
	}

	if got := agentWebviewOrphans(procs, testRoot); len(got) != 0 {
		t.Fatalf("webview da UI VIVA não pode ser morto; veio %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// CASO 4 — COMMANDLINE ILEGÍVEL / SEM --user-data-dir: FAIL-SAFE (NÃO MATA)
// ─────────────────────────────────────────────────────────────────────────
// REQUISITO EXPLÍCITO: sem prova de que a pasta de dados é nossa, o processo é
// preservado. O custo aceitável é deixar um órfão NOSSO vivo (o watchdog tenta
// de novo depois); o custo inaceitável é fechar o Teams do usuário.

func TestAgentWebviewOrphans_UnreadableCommandLineIsNotKilled(t *testing.T) {
	// cmdline vazio = não foi possível ler (processo protegido/acesso negado).
	procs := []procRecord{
		{pid: 6001, ppid: 7777, name: webviewOrphanName, cmdline: ""},
	}

	if got := agentWebviewOrphans(procs, testRoot); len(got) != 0 {
		t.Fatalf("cmdline ilegível tem de ser fail-safe (não mata); veio %v", got)
	}
}

func TestAgentWebviewOrphans_CommandLineWithoutUserDataDirFlagIsNotKilled(t *testing.T) {
	procs := []procRecord{
		{pid: 6001, ppid: 7777, name: webviewOrphanName, cmdline: `C:\...\msedgewebview2.exe --embedded-browser-webview=1 --no-sandbox`},
	}

	if got := agentWebviewOrphans(procs, testRoot); len(got) != 0 {
		t.Fatalf("sem --user-data-dir não dá para provar a origem: não mata; veio %v", got)
	}
}

func TestAgentWebviewOrphans_ReadableForeignCommandLineOnDeadChainIsNotKilled(t *testing.T) {
	// O caso mais perigoso: linhagem quebrada (parece órfão) MAS o UDD é de outro
	// app. É exatamente o Teams. A trava do user-data-dir é quem salva.
	procs := []procRecord{
		{pid: 6001, ppid: 7777, name: webviewOrphanName, cmdline: `--user-data-dir="C:\Users\u\AppData\Local\Temp\Other\EBWebView"`},
	}

	if got := agentWebviewOrphans(procs, testRoot); len(got) != 0 {
		t.Fatalf("UDD de outro app com cadeia quebrada NÃO pode ser morto; veio %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// CASOS 5–8 — COMPARAÇÃO DE CAMINHO (o discriminante é o PREFIXO)
// ─────────────────────────────────────────────────────────────────────────
// Estes casos cobrem a classe de erro "casou por engano". O Teams também tem
// "EBWebView" no caminho — o sufixo NÃO é o discriminante; o PREFIXO é.

func TestAgentWebviewOrphans_UserDataDirSiblingWithSamePrefixTextDoesNotMatch(t *testing.T) {
	// "WebView2Backup" e "WebView2-evil" NÃO estão dentro de "...\WebView2".
	// strings.HasPrefix/HasSufixo solto casaria os dois — a comparação tem de
	// exigir o limite no separador de diretório.
	casos := []string{
		`C:\ProgramData\Discovery\WebView2Backup` ,
		`C:\ProgramData\Discovery\WebView2-evil` ,
		`C:\ProgramData\Discovery\WebView2Backup\session-1\EBWebView`,
		`C:\ProgramData\Discovery\WebView2-evil\session-1\EBWebView`,
	}

	for _, udd := range casos {
		procs := []procRecord{{pid: 6001, ppid: 7777, name: webviewOrphanName, cmdline: `--user-data-dir="` + udd + `"`}}
		if got := agentWebviewOrphans(procs, testRoot); len(got) != 0 {
			t.Errorf("UDD %q NÃO está dentro de %q e não pode ser morto; veio %v", udd, testRoot, got)
		}
	}
}

func TestAgentWebviewOrphans_UserDataDirMatchIsCaseAndSeparatorInsensitive(t *testing.T) {
	// Windows: caixa e separador não distinguem caminhos.
	casos := []string{
		`C:\PROGRAMDATA\Discovery\WEBVIEW2\SESSION-1\EBWebView`,
		`C:/ProgramData/Discovery/WebView2/session-1/EBWebView`,
		`C:\ProgramData\Discovery\WebView2\SESSION-9\EBWebView`,
		`C:\ProgramData\Discovery\WebView2` , // a própria raiz
		`C:\ProgramData\Discovery\WebView2\` , // raiz com barra final
	}

	for _, udd := range casos {
		procs := []procRecord{{pid: 6001, ppid: 7777, name: webviewOrphanName, cmdline: `--user-data-dir="` + udd + `"`}}
		got := agentWebviewOrphans(procs, testRoot)
		if !containsPid(got, 6001) {
			t.Errorf("UDD %q está dentro da raiz (caixa/separador não distinguem) e DEVERIA ser morto; veio %v", udd, got)
		}
	}
}

func TestAgentWebviewOrphans_UserDataDirFlagWithAndWithoutQuotesAreBothRead(t *testing.T) {
	// O Chromium aceita as duas formas; as duas têm de ser lidas.
	comAspas := []procRecord{{pid: 6001, ppid: 7777, name: webviewOrphanName, cmdline: `--user-data-dir="` + testOurUDD + `" --other=1`}}
	semAspas := []procRecord{{pid: 6001, ppid: 7777, name: webviewOrphanName, cmdline: `--user-data-dir=` + testOurUDD + ` --other=1`}}

	if got := agentWebviewOrphans(comAspas, testRoot); !containsPid(got, 6001) {
		t.Errorf("forma com aspas deveria ser lida; veio %v", got)
	}
	if got := agentWebviewOrphans(semAspas, testRoot); !containsPid(got, 6001) {
		t.Errorf("forma sem aspas deveria ser lida; veio %v", got)
	}
}

func TestAgentWebviewOrphans_TeamsEBWebViewSuffixIsNotMistakenForAgent(t *testing.T) {
	// O caminho do Teams TERMINA com "EBWebView", igual ao nosso. Quem decide é o
	// PREFIXO (a raiz do agent), nunca o sufixo.
	if !strings.HasSuffix(testTeamsUDD, "EBWebView") {
		t.Fatalf("o cenário perdeu o ponto do teste: o UDD do Teams deveria terminar em EBWebView, veio %q", testTeamsUDD)
	}

	procs := []procRecord{{pid: 5001, ppid: 9999, name: webviewOrphanName, cmdline: `--user-data-dir="` + testTeamsUDD + `"`}}

	if got := agentWebviewOrphans(procs, testRoot); len(got) != 0 {
		t.Fatalf("UDD do Teams casa pelo sufixo EBWebView mas não pelo prefixo do agent: não pode ser morto; veio %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// CASO EXTRA — FROTA MISTA: o cenário real (33 processos) reduzido a um teste
// ─────────────────────────────────────────────────────────────────────────
// Prova que SÓ o órfão do agent morre e que Teams/WhatsApp/SearchHost/Kaspersky
// sobrevivem na mesma passada — que é o defeito de produção inteiro.

func TestAgentWebviewOrphans_MixedFleetOnlyOurOrphanIsKilled(t *testing.T) {
	procs := []procRecord{
		agentProc(100), // a UI viva
		// Nossos: a UI corrente (não mexe) e um órfão real (morre).
		ourWebview(6100, 100),
		ourWebview(6101, 9998), // pai (agent morto) ausente → órfão
		// Terceiros, TODOS com o pai ausente do snapshot (o cenário que matava):
		foreignWebview(5001, 9999, testTeamsUDD),
		foreignWebview(5002, 9999, testWaUDD),
		foreignWebview(5003, 9999, `C:\Windows\SystemApps\MicrosoftWindows.Client.CBS_cw5n1h2txyewy\EBWebView`),
		foreignWebview(5004, 9999, `C:\ProgramData\Kaspersky Lab\WebView2\EBWebView`),
	}

	got := agentWebviewOrphans(procs, testRoot)

	if !containsPid(got, 6101) {
		t.Errorf("o órfão NOSSO (6101) deveria ser morto; veio %v", got)
	}
	for _, pid := range []uint32{6100, 5001, 5002, 5003, 5004} {
		if containsPid(got, pid) {
			t.Errorf("pid %d NÃO podia ser morto (UI viva ou webview de terceiro); veio %v", pid, got)
		}
	}
	if len(got) != 1 {
		t.Errorf("esperado exatamente 1 órfão na frota mista, veio %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// classifyLineage — os três desfechos, um por vez (a ambiguidade do bool antigo)
// ─────────────────────────────────────────────────────────────────────────
// O defeito original colapsava "pai é de outro app" e "cadeia quebrada" no
// mesmo `false`, e o chamador tratava os dois como "pode matar". Estes testes
// travam cada desfecho com nome próprio.

func TestClassifyLineage_DistinguishesForeignFromBroken(t *testing.T) {
	alive := map[uint32]bool{100: true}

	agenteVivo := []procRecord{agentProc(100)}
	paiAlheio := []procRecord{{pid: 42, ppid: 1, name: "explorer.exe"}, agentProc(100)}
	cadeiaQuebrada := []procRecord{agentProc(100)} // pai 9999 não existe

	webview := func(ppid uint32) procRecord {
		return procRecord{pid: 6001, ppid: ppid, name: webviewOrphanName}
	}

	if v := classifyLineage(webview(100), append(agenteVivo, webview(100)), alive); v != lineageLiveAgent {
		t.Errorf("pai = discovery-agent vivo deveria dar lineageLiveAgent, veio %v", v)
	}
	if v := classifyLineage(webview(42), append(paiAlheio, webview(42)), alive); v != lineageNotOurs {
		t.Errorf("pai = processo de OUTRO app vivo deveria dar lineageNotOurs (é o false ambíguo do bug), veio %v", v)
	}
	if v := classifyLineage(webview(9999), append(cadeiaQuebrada, webview(9999)), alive); v != lineageBroken {
		t.Errorf("pai ausente do snapshot deveria dar lineageBroken, veio %v", v)
	}
}

func TestClassifyLineage_BrokenAndNotOursAreDifferentValues(t *testing.T) {
	// Guarda contra a regressão exata: se alguém voltar a colapsar os dois num
	// único bool, os valores voltam a coincidir e este teste cai.
	if lineageBroken == lineageNotOurs {
		t.Fatal("lineageBroken e lineageNotOurs voltaram a ser o MESMO valor: a decisão voltou a ser ambígua (bug do Teams)")
	}
}

func TestClassifyLineage_GuardOverflowIsConservative(t *testing.T) {
	// Cadeia maior que o guard: não conseguimos provar nada → conservador.
	var procs []procRecord
	for i := uint32(1); i <= 40; i++ {
		procs = append(procs, procRecord{pid: 7000 + i, ppid: 7000 + i + 1, name: webviewOrphanName})
	}
	procs = append(procs, procRecord{pid: 6001, ppid: 7001, name: webviewOrphanName})

	if v := classifyLineage(procs[len(procs)-1], procs, map[uint32]bool{}); v == lineageBroken {
		t.Fatalf("cadeia que estoura o guard não pode autorizar a limpeza; veio %v", v)
	}
}
