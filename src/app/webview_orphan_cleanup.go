package app

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// ── Limpeza de browser processes órfãos (fix 21/10, congelamento do chat) ──
//
// Quando a UI do agent morre SEM encerrar (taskkill do watchdog, instalador do
// self-update, os.Exit do main-watchdog), o processo msedgewebview2 dela (o
// "browser process", dono da pasta de dados compartilhada session-N) fica
// VIVO por um tempo como órfão — e ele carrega o estado do cliente morto,
// inclusive pedidos de criação de controller que estavam EM CURSO.
//
// A UI seguinte anexa nesse browser órfão: a PRIMEIRA janela funciona (o env
// attach é rápido), mas a SEGUNDA criação de controller enfileira atrás do
// pedido morto e nunca sai — a main thread trava dentro do
// EmbeddedBrowserWebView.dll esperando a resposta que nunca vem (dump
// ui-zombie-23468: SendMessage cross-thread em espera). Foi o ciclo dos logs
// de 21/10: todo congelamento numa UI relançada; a simulação isolada nunca
// reproduz (browser fresco).
//
// A limpeza roda no STARTUP da UI, ANTES da primeira janela. É ESTE o caminho
// que o usuário dispara ao abrir o agent pelo Menu Iniciar.

// ════════════════════════════════════════════════════════════════════════════
// INCIDENTE EM PRODUÇÃO (22/10) — LEIA ANTES DE AFROUXAR QUALQUER CRITÉRIO.
// ════════════════════════════════════════════════════════════════════════════
//
// SINTOMA: com o agent fora do tray, o usuário abria o agent pelo Menu Iniciar
// e o MICROSOFT TEAMS FECHAVA. Reproduzível e determinístico.
//
// EVIDÊNCIA (logs.db): "24 browser process(es) orfaos ... encerrados no
// startup" — o normal são 5-8 (só os webviews da nossa UI). 23-24 é a
// assinatura de estar matando webviews de TERCEIROS.
//
// MEDIÇÃO na máquina (33 msedgewebview2.exe vivos, por --user-data-dir):
//     Teams 8 | Kaspersky 7 | DISCOVERY 6 | WhatsApp 6 | SearchHost 6
//
// HAVIA DOIS MATADORES, com defeitos independentes:
//
// (1) ESTE arquivo (startup). orphanChainHasLiveAgent documentava
//     "pai é processo alheio (não nosso): não mexe" e retornava false; o
//     chamador fazia `if !orphanChainHasLiveAgent(...)` => MATA. Comentário e
//     código diziam coisas OPOSTAS. Um webview de terceiro cujo pai é outro
//     webview (o caso do Teams, com árvore profunda) era morto.
//     Corrigido com lineageVerdict tri-state: "não é nosso" é um veredito
//     PRÓPRIO e nunca resulta em kill.
//
// (2) ui_watchdog_windows.go (ciclo matar/relançar). Decidia a identidade só
//     pela linhagem sobre um snapshot INSTANTÂNEO; com 28 de 33 webviews tendo
//     outro msedgewebview2.exe como pai, qualquer elo ausente do snapshot
//     (processo nascendo/morrendo/reparentado) fazia a subida bater no vazio e
//     classificar webview de TERCEIRO como órfão nosso.
//
// TRAVA ADICIONAL COMUM AOS DOIS (correção A aprovada pelo usuário): além da
// linhagem, o candidato precisa COMPROVAR que o diretório de dados
// (--user-data-dir) aponta para dentro de <DataDir>\WebView2. Esse dado é
// inequívoco — o Teams usa %LOCALAPPDATA%\Packages\MSTeams_... — então um
// falso positivo da linhagem deixa de bastar para matar. É PROTEÇÃO EM
// PROFUNDIDADE: mesmo que a lógica de linhagem erre de novo, webview de
// terceiro não morre.
//
// REGRA DE OURO: na dúvida, NÃO mata. O custo aceitável é deixar um órfão NOSSO
// vivo ocasionalmente (o watchdog tenta de novo depois); o custo inaceitável é
// fechar o Teams do usuário.

const webviewBrowserProcessName = "msedgewebview2.exe"

// webView2RootSuffix é a subpasta do DataDir que contém os perfis do WebView2.
// A raiz de comparação é <DataDir>\WebView2 (o diretório BASE), que cobre
// tanto "session-<id>\EBWebView" quanto o perfil base (sem sessão).
// NÃO use <DataDir>\WebView2\EBWebView: o UDD real é
// "...\WebView2\session-1\EBWebView" e o prefixo não casaria, tornando a
// limpeza um no-op. NÃO use <DataDir> sozinho: casaria coisa de terceiro.
const webView2RootSuffix = "WebView2"

// webView2OrphanFlag é a flag do Chromium que define a pasta de dados.
const webView2OrphanFlag = "--user-data-dir"

// lineageVerdict é o resultado TRI-STATE da subida da cadeia de pais.
//
// Um bool não expressa os três desfechos possíveis, e foi exatamente essa
// ambiguidade que produziu o defeito: "pai é processo alheio" e "cadeia
// quebrada" colapsavam no mesmo false, e o chamador tratava os dois como
// "pode matar". Aqui cada desfecho tem nome próprio.
type lineageVerdict int

const (
	// lineageNotOurs: a subida alcançou um processo de OUTRO app vivo. Nunca matar.
	lineageNotOurs lineageVerdict = iota
	// lineageLiveAgent: a subida alcançou um discovery-agent.exe VIVO (a UI
	// corrente). Nunca matar.
	lineageLiveAgent
	// lineageBroken: a subida bateu num pai AUSENTE do snapshot — resíduo de um
	// agent recém-morto. É o único desfecho que autoriza a limpeza (e ainda
	// depende da trava do user-data-dir).
	lineageBroken
)

// String existe para o log/diagnóstico não imprimir um número cru.
func (v lineageVerdict) String() string {
	switch v {
	case lineageLiveAgent:
		return "alca-agent-vivo"
	case lineageNotOurs:
		return "nao-e-nosso"
	case lineageBroken:
		return "cadeia-quebrada"
	default:
		return "desconhecido"
	}
}

// classifyLineage sobe a cadeia pai-a-pai e classifica o desfecho.
//
// Regras (cada uma casa com o comentário — sem inversão):
//   - pai ausente do snapshot ............... lineageBroken
//   - pai é discovery-agent.exe VIVO ........ lineageLiveAgent
//   - pai é outro msedgewebview2.exe ........ sobe mais um nível
//   - pai é qualquer OUTRO processo vivo .... lineageNotOurs  (nunca matar)
func classifyLineage(p procRecord, procs []procRecord, aliveAgents map[uint32]bool) lineageVerdict {
	lowerUI := strings.ToLower(agentUIExecutableName)
	cur := p
	for guard := 0; guard < 16; guard++ {
		parent, ok := findProcRecord(procs, cur.ppid)
		if !ok {
			return lineageBroken
		}
		name := strings.ToLower(parent.name)
		switch name {
		case lowerUI:
			if aliveAgents[parent.pid] {
				return lineageLiveAgent
			}
			// agent morto: continua subindo pela cadeia dele
			cur = parent
			continue
		case webviewBrowserProcessName:
			cur = parent
			continue
		default:
			// Processo alheio VIVO (explorer, services, Teams...): NÃO é nosso.
			return lineageNotOurs
		}
	}
	// Guard estourou: não conseguimos provar nada — conservador.
	return lineageNotOurs
}

// findProcRecord localiza um pid na lista.
func findProcRecord(procs []procRecord, pid uint32) (procRecord, bool) {
	for _, p := range procs {
		if p.pid == pid {
			return p, true
		}
	}
	return procRecord{}, false
}

// orphanedAgentWebviews é a DECISÃO PURA (sem I/O): devolve os pids de
// msedgewebview2.exe comprovadamente órfãos do agent.
//
// Um candidato só é órfão quando TODAS valem:
//  1. o nome é msedgewebview2.exe;
//  2. a linhagem é lineageBroken (nem host vivo nosso, nem app alheio);
//  3. o --user-data-dir está DENTRO de webView2Root (trava da correção A).
//
// webView2Root vem por PARÂMETRO (o chamador passa platform.DataDir()+\WebView2)
// para a função continuar pura e testável sem tocar no ambiente da máquina.
func orphanedAgentWebviews(procs []procRecord, webView2Root string) []uint32 {
	aliveAgents := map[uint32]bool{}
	for _, p := range procs {
		if strings.EqualFold(p.name, agentUIExecutableName) {
			aliveAgents[p.pid] = true
		}
	}

	var orphans []uint32
	for _, p := range procs {
		if !strings.EqualFold(p.name, webviewBrowserProcessName) {
			continue
		}
		if classifyLineage(p, procs, aliveAgents) != lineageBroken {
			continue
		}
		// TRAVA: só mata com prova de que a pasta de dados é NOSSA.
		if !webviewUserDataDirIsOurs(p.cmdline, webView2Root) {
			continue
		}
		orphans = append(orphans, p.pid)
	}
	return orphans
}

// webviewUserDataDirIsOurs decide, de forma CONSERVADORA, se o processo pode
// ser considerado nosso pela pasta de dados.
//
// Fail-safe: CommandLine ilegível, flag ausente ou caminho que não resolva
// para dentro da raiz => false (NÃO mata).
func webviewUserDataDirIsOurs(cmdline, webView2Root string) bool {
	dir, ok := extrairUserDataDir(cmdline)
	if !ok {
		return false
	}
	return pathHasDirPrefix(dir, webView2Root)
}

// killOrphanedWebviewBrowsers devolve quantos browsers órfãos foram mortos.
func killOrphanedWebviewBrowsers() int {
	procs, err := processSnapshotWithCommandLine()
	if err != nil {
		return 0
	}
	return killOrphanedWebviewBrowsersFrom(procs, agentWebView2Root(), func(pid uint32) bool {
		h, openErr := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if openErr != nil {
			return false
		}
		defer windows.CloseHandle(h)
		return windows.TerminateProcess(h, 1) == nil
	})
}

// killOrphanedWebviewBrowsersFrom é o núcleo testável: recebe o snapshot, a
// raiz dos dados e um terminador injetado. NÃO faz I/O próprio.
func killOrphanedWebviewBrowsersFrom(procs []procRecord, webView2Root string, terminate func(uint32) bool) int {
	killed := 0
	for _, pid := range orphanedAgentWebviews(procs, webView2Root) {
		if terminate(pid) {
			killed++
		}
	}
	return killed
}

// LogOrphanCleanup registra o resultado da limpeza no log do agent. Publicado
// para o main.go chamar no startup da UI, antes da primeira janela.
func LogOrphanCleanup(a *App) {
	if a == nil {
		return
	}
	if n := killOrphanedWebviewBrowsers(); n > 0 {
		a.Logs.Append(fmt.Sprintf("[webview] %d browser process(es) orfaos de UIs mortas encerrados no startup (evita o deadlock da segunda janela do WebView2)", n))
	} else if os.Getenv("DISCOVERY_DEBUG_ORPHAN") != "" {
		a.Logs.Append("[webview] nenhum browser orfao encontrado no startup")
	}
}

