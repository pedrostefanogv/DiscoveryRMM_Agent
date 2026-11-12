package app

import (
	"testing"
)

// webview_orphan_cleanup_test.go — Regressão do SEGUNDO matador de webviews:
// killOrphanedWebviewBrowsers / orphanedAgentWebviews / classifyLineage
// (webview_orphan_cleanup.go).

// Por que este arquivo existe separado: o matador acima é o do STARTUP da UI,
// chamado por main.go:286 → LogOrphanCleanup — é o caminho que o usuário usa ao
// abrir o agent pelo Menu Iniciar. As estruturas e a assinatura são as mesmas de
// ui_watchdog_webview_orphan_test.go hoje (ambos usam procRecord), mas os dois
// matadores são chamados de pontos diferentes e um deles tem terminador
// injetável; separar deixa o histórico de cada um legível.

// DEFEITO QUE ESTE ARQUIVO TRAVA (inversão de lógica, confirmada em produção):
//   orphanChainHasLiveAgent devolvia `false` no ramo "pai é processo alheio"
//   (comentário dizia "não mexe") e o chamador fazia `if !false { MATA }`.
//   Comentário e código se contradiziam, e o resultado era: webview de TERCEIRO
//   com a cadeia quebrada (o Teams) era morto no startup.
//   A decisão virou lineageVerdict (tri-estado) + a trava do --user-data-dir.

// COBERTURA DESTE ARQUIVO (honestidade sobre o que é medido):
//   cobre a DECISÃO (orphanedAgentWebviews, classifyLineage) **E o LAÇO DE KILL**
//   (killOrphanedWebviewBrowsersFrom com terminador FAKE injetado). O
//   TerminateProcess real do wrapper killOrphanedWebviewBrowsers não é
//   exercitado — nenhum processo do sistema é tocado por estes testes.

// ─────────────────────────────────────────────────────────────────────────
// B1 — TERCEIRO COM CADEIA QUEBRADA: NÃO PODE SER MORTO (o defeito)
// ─────────────────────────────────────────────────────────────────────────

func TestOrphanedAgentWebviews_TeamsWithBrokenChainIsNotKilled(t *testing.T) {
	procs := []procRecord{
		{pid: 5001, ppid: 9999, name: webviewBrowserProcessName, cmdline: `--user-data-dir="` + testTeamsUDD + `"`},
		{pid: 100, ppid: 1, name: agentUIExecutableName},
	}

	if got := orphanedAgentWebviews(procs, testRoot); len(got) != 0 {
		t.Fatalf("webview do Teams com cadeia quebrada NÃO pode ser morto (a inversão de lógica fazia exatamente isso); veio %v", got)
	}
}

func TestOrphanedAgentWebviews_LiveForeignParentIsNotKilled(t *testing.T) {
	// O ramo que devolvia false com comentário "não mexe" — e matava.
	procs := []procRecord{
		{pid: 5001, ppid: 42, name: webviewBrowserProcessName, cmdline: `--user-data-dir="` + testTeamsUDD + `"`},
		{pid: 42, ppid: 1, name: "ms-teams.exe"},
		{pid: 100, ppid: 1, name: agentUIExecutableName},
	}

	if got := orphanedAgentWebviews(procs, testRoot); len(got) != 0 {
		t.Fatalf("pai alheio VIVO significa \"não é nosso\": não pode ser morto; veio %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// B2 — ÓRFÃO NOSSO REAL: CONTINUA SENDO MORTO
// ─────────────────────────────────────────────────────────────────────────

func TestOrphanedAgentWebviews_OurRealOrphanIsStillKilled(t *testing.T) {
	procs := []procRecord{
		{pid: 6001, ppid: 9998, name: webviewBrowserProcessName, cmdline: `--user-data-dir="` + testOurUDD + `"`},
	}

	if got := orphanedAgentWebviews(procs, testRoot); !containsPid(got, 6001) {
		t.Fatalf("órfão NOSSO (cadeia quebrada + UDD sob a raiz) DEVE ser morto; veio %v", got)
	}
}

func TestOrphanedAgentWebviews_LiveAgentAncestorIsNotKilled(t *testing.T) {
	procs := []procRecord{
		{pid: 6001, ppid: 100, name: webviewBrowserProcessName, cmdline: `--user-data-dir="` + testOurUDD + `"`},
		{pid: 100, ppid: 1, name: agentUIExecutableName},
	}

	if got := orphanedAgentWebviews(procs, testRoot); len(got) != 0 {
		t.Fatalf("webview da UI VIVA não pode ser morto; veio %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// B5 — FAIL-SAFE: COMMANDLINE ILEGÍVEL NÃO AUTORIZA LIMPEZA
// ─────────────────────────────────────────────────────────────────────────

func TestOrphanedAgentWebviews_UnreadableCommandLineIsFailSafe(t *testing.T) {
	procs := []procRecord{
		{pid: 6001, ppid: 9998, name: webviewBrowserProcessName, cmdline: ""},
	}

	if got := orphanedAgentWebviews(procs, testRoot); len(got) != 0 {
		t.Fatalf("sem CommandLine legível não há prova de que a pasta é nossa: NÃO mata; veio %v", got)
	}
}

func TestOrphanedAgentWebviews_UnquotedForeignPathIsFailSafe(t *testing.T) {
	// Caminho de terceiro COM espaço e sem aspas: o extrator para no 1º espaço, o
	// resultado não casa com a raiz do agent e a decisão é não matar.
	procs := []procRecord{
		{pid: 6001, ppid: 9998, name: webviewBrowserProcessName, cmdline: `--user-data-dir=C:\Program Files\Kaspersky Lab\WebView2`},
	}

	if got := orphanedAgentWebviews(procs, testRoot); len(got) != 0 {
		t.Fatalf("caminho ambíguo de terceiro tem de ser fail-safe; veio %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// B6 — FROTA MISTA (cenário real) + O LAÇO DE KILL COM TERMINADOR FAKE
// ─────────────────────────────────────────────────────────────────────────
// Diferencial deste arquivo: killOrphanedWebviewBrowsersFrom recebe o
// terminador por parâmetro, então dá para cobrir o LAÇO de verdade — quais pids
// seriam encerrados e quantos — sem tocar em nenhum processo do sistema.

func TestKillOrphanedWebviewBrowsersFrom_MixedFleetKillsOnlyOurOrphan(t *testing.T) {
	procs := []procRecord{
		{pid: 100, ppid: 1, name: agentUIExecutableName},
		// Nossos: UI corrente (não mexe) e um órfão real (morre).
		{pid: 6100, ppid: 100, name: webviewBrowserProcessName, cmdline: `--user-data-dir="` + testOurUDD + `"`},
		{pid: 6101, ppid: 9998, name: webviewBrowserProcessName, cmdline: `--user-data-dir="C:\ProgramData\Discovery\WebView2\session-2\EBWebView"`},
		// Terceiros, TODOS com cadeia quebrada (o cenário que os matava):
		{pid: 5001, ppid: 9999, name: webviewBrowserProcessName, cmdline: `--user-data-dir="` + testTeamsUDD + `"`},
		{pid: 5002, ppid: 9999, name: webviewBrowserProcessName, cmdline: `--user-data-dir="` + testWaUDD + `"`},
		{pid: 5003, ppid: 9999, name: webviewBrowserProcessName, cmdline: `--user-data-dir="C:\Windows\SystemApps\SearchHost\EBWebView"`},
		{pid: 5004, ppid: 9999, name: webviewBrowserProcessName, cmdline: `--user-data-dir="C:\ProgramData\Kaspersky Lab\WebView2\EBWebView"`},
	}

	var terminados []uint32
	fake := func(pid uint32) bool {
		terminados = append(terminados, pid)
		return true
	}

	killed := killOrphanedWebviewBrowsersFrom(procs, testRoot, fake)

	if killed != 1 {
		t.Errorf("esperado matar exatamente 1 (o órfão nosso), veio %d — terminados=%v", killed, terminados)
	}
	if len(terminados) != 1 || terminados[0] != 6101 {
		t.Errorf("só o pid 6101 podia ser terminado; terminados=%v", terminados)
	}
	for _, pid := range []uint32{6100, 5001, 5002, 5003, 5004} {
		for _, got := range terminados {
			if got == pid {
				t.Errorf("pid %d NÃO podia ser terminado (UI viva ou webview de terceiro); terminados=%v", pid, terminados)
			}
		}
	}
}

func TestKillOrphanedWebviewBrowsersFrom_TerminateFailureIsNotCounted(t *testing.T) {
	// Se o TerminateProcess falhar (processo já morto, acesso negado), o pid não
	// entra na contagem — o log precisa refletir o que foi de fato encerrado.
	procs := []procRecord{
		{pid: 6001, ppid: 9998, name: webviewBrowserProcessName, cmdline: `--user-data-dir="` + testOurUDD + `"`},
	}

	killed := killOrphanedWebviewBrowsersFrom(procs, testRoot, func(uint32) bool { return false })

	if killed != 0 {
		t.Fatalf("terminador que falha não pode contar como morto; veio %d", killed)
	}
}

func TestKillOrphanedWebviewBrowsersFrom_EmptySnapshotKillsNothing(t *testing.T) {
	called := false
	killed := killOrphanedWebviewBrowsersFrom(nil, testRoot, func(uint32) bool { called = true; return true });

	if killed != 0 || called {
		t.Fatalf("snapshot vazio não pode chamar o terminador; killed=%d called=%v", killed, called)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// B7 — PREFIXO DE DIRETÓRIO (a classe "casou por engano")
// ─────────────────────────────────────────────────────────────────────────

func TestOrphanedAgentWebviews_SiblingPrefixesDoNotMatch(t *testing.T) {
	for _, udd := range []string{
		`C:\ProgramData\Discovery\WebView2Backup\session-1\EBWebView`,
		`C:\ProgramData\Discovery\WebView2-evil\session-1\EBWebView`,
		`C:\ProgramData\Discovery\WebView2X\EBWebView`,
	} {
		procs := []procRecord{
			{pid: 6001, ppid: 9998, name: webviewBrowserProcessName, cmdline: `--user-data-dir="` + udd + `"`},
		}
		if got := orphanedAgentWebviews(procs, testRoot); len(got) != 0 {
			t.Errorf("UDD %q tem o mesmo TEXTO de prefixo mas NÃO está dentro da raiz: não pode ser morto; veio %v", udd, got)
		}
	}
}

func TestOrphanedAgentWebviews_WindowsPathMatchingIsCaseAndSeparatorInsensitive(t *testing.T) {
	for _, udd := range []string{
		`C:\PROGRAMDATA\DISCOVERY\WEBVIEW2\SESSION-1\EBWebView`,
		`C:/ProgramData/Discovery/WebView2/session-1/EBWebView`,
		`C:\ProgramData\Discovery\WebView2`,
	} {
		procs := []procRecord{
			{pid: 6001, ppid: 9998, name: webviewBrowserProcessName, cmdline: `--user-data-dir="` + udd + `"`},
		}
		if got := orphanedAgentWebviews(procs, testRoot); !containsPid(got, 6001) {
			t.Errorf("UDD %q pertence à raiz (caixa/separador não distinguem no Windows): deveria ser morto; veio %v", udd, got)
		}
	}
}
