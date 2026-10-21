package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ── Autoproteção da UI: a thread principal responde? ───────────────────────
//
// O travamento da interface (dump 20/10 21:29 e 21:49) acontece DENTRO do
// WebView2: a main thread entra em CreateCoreWebView2EnvironmentWithOptions
// (espera nativa, SEM prazo) pela subida do processo do navegador e nunca
// volta. Nada no código Go pode dar timeout nisso — a thread fica presa no
// syscall e a interface inteira morre com ela (todas as janelas, IPC, cartão).
//
// Este watchdog NÃO conserta o wait nativo: detecta a thread presa e dá à UI
// uma saída limpa — encerra o próprio processo (os.Exit) e o ui-watchdog do
// serviço relança uma UI nova em segundos. O pior caso deixa de ser "interface
// travada para sempre" (até o serviço notar e matar) e passa a ser ~2 min de
// indisponibilidade com log claro do motivo.
//
// Limiar: a criação fria do WebView2 desta máquina chegou a levar ~54 s
// (window_lazy.go) — o limiar precisa cobrir isso com folga; acima dele,
// thread presa é travamento, não lentidão.
const (
	// mainThreadProbeInterval é o intervalo entre sondas.
	mainThreadProbeInterval = 30 * time.Second
	// mainThreadProbeTimeout é quanto tempo a main thread tem para responder.
	mainThreadProbeTimeout = 90 * time.Second
)

// mainFreezeDumpAfter é quanto tempo de main thread presa durante a criação de
// uma janela basta para gravar o minidump imediato (diagnóstico com o estado
// vivo da chamada nativa culpada).
const mainFreezeDumpAfter = 5 * time.Second

// startMainThreadWatchdog sobe a sonda periódica da main thread. Só faz
// sentido onde existe aplicação Wails (a UI); no modo serviço a.app é nil.
func (a *App) startMainThreadWatchdog(ctx context.Context) {
	if a == nil || a.app == nil || ctx == nil {
		return
	}
	go a.runMainThreadWatchdog(ctx)
}

func (a *App) runMainThreadWatchdog(ctx context.Context) {
	ticker := time.NewTicker(mainThreadProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !a.probeMainThread() {
				a.Logs.Append("[main-watchdog] thread principal sem responder a " +
					mainThreadProbeTimeout.String() + " (WebView2 travado na criação do ambiente); encerrando a UI para relancamento limpo")
				// DUMP ANTES DO EXIT: cada travamento real vira evidência — o
				// minidump mostra em que chamada nativa a main thread estava
				// presa (env creation? controller? loader?). O rundll32 é um
				// processo INDEPENDENTE: sobrevive ao os.Exit e o serviço envia
				// o arquivo na próxima sincronização de diagnóstico.
				captureMainThreadFreezeDump(a)
				// Tempo para o logstore descarregar a linha antes do exit.
				time.Sleep(500 * time.Millisecond)
				os.Exit(2)
			}
		}
	}
}

// captureMainThreadFreezeDump grava um minidump do próprio processo (stacks de
// TODAS as threads) no diretório de diagnóstico, antes do relançamento. Falha
// silenciosamente: diagnóstico é o melhor esforço — o encerramento limpo não
// pode depender dele.
func captureMainThreadFreezeDump(a *App) {
	dir := freezeDumpDir()
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	dump := filepath.Join(dir, fmt.Sprintf("ui-freeze-%s.dmp", time.Now().Format("20060102_150405")))
	// MiniDump SEM "full": guarda as stacks de todas as threads (o que o
	// diagnóstico precisa) sem o peso do heap completo.
	// rundll32 pode ser bloqueado por política/AV (Access is denied) — fallback
	// para o DbgHelp do próprio processo (MiniDumpWriteDump no handle do arquivo).
	cmd := exec.Command("rundll32.exe", "comsvcs.dll,MiniDump",
		fmt.Sprintf("%d", os.Getpid()), dump)
	if err := cmd.Start(); err != nil {
		a.Logs.Append("[main-watchdog] rundll32 indisponível (" + err.Error() + "); usando MiniDumpWriteDump in-process")
		if !writeMiniDumpInProcess(dump) {
			a.Logs.Append("[main-watchdog] minidump falhou nos dois caminhos; seguindo com o relancamento")
			return
		}
	} else {
		// O rundll32 PRECISA terminar antes do os.Exit (o processo-alvo morrendo
		// no meio do dump deixa arquivo vazio/corrompido — foi o que aconteceu
		// no freeze de 09:41). Espera com folga, mas sem bloquear indefinidamente.
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			a.Logs.Append("[main-watchdog] minidump demorou demais; seguindo")
		}
	}
	if st, err := os.Stat(dump); err == nil && st.Size() > 0 {
		a.Logs.Append("[main-watchdog] minidump do congelamento gravado: " + dump + " (" + fmt.Sprint(st.Size()) + " bytes)")
	} else {
		a.Logs.Append("[main-watchdog] minidump gravou arquivo vazio/inexistente: " + dump)
	}
}

// freezeDumpDir devolve o diretório de diagnóstico (ao lado do logstore).
func freezeDumpDir() string {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		return ""
	}
	return filepath.Join(programData, "Discovery", "logs", "freeze-dumps")
}

// writeMiniDumpInProcess grava o minidump via MiniDumpWriteDump (DbgHelp.dll)
// rodando no próprio processo — caminho que funciona mesmo quando a execução
// de rundll32 é bloqueada por política ou antivírus. Tipo MiniDumpNormal (0):
// só as stacks de todas as threads, ~230 KB.
func writeMiniDumpInProcess(path string) bool {
	dbghelp := windows.NewLazySystemDLL("dbghelp.dll")
	f, err := os.Create(path)
	if err != nil {
		return false
	}
	defer f.Close()
	writeDump := dbghelp.NewProc("MiniDumpWriteDump")
	hProcess := windows.CurrentProcess()
	ret, _, _ := writeDump.Call(
		uintptr(hProcess),
		uintptr(uint32(os.Getpid())),
		f.Fd(),
		0,      // MiniDumpNormal
		0, 0, 0, // ExceptionParam, UserStreamParam, CallbackParam
	)
	return ret != 0
}

// probeMainThread dispara uma tarefa trivial na main thread e espera no prazo.
// Devolve true quando a thread respondeu.
func (a *App) probeMainThread() bool {
	return a.probeMainThreadWithin(mainThreadProbeTimeout)
}

// probeMainThreadWithin é a sonda com prazo configurável. O goroutine vaza de
// propósito quando a thread não responde (uma tarefa enfileirada, no máximo) —
// o processo encerra em seguida em qualquer caminho de timeout.
func (a *App) probeMainThreadWithin(timeout time.Duration) bool {
	done := make(chan struct{})
	var once atomic.Bool
	go func() {
		defer func() { _ = recover() }()
		application.InvokeSync(func() {
			if once.CompareAndSwap(false, true) {
				close(done)
			}
		})
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// freezeDumpCooldown é o intervalo mínimo entre minidumps do congelamento no
// MESMO processo (o relançamento em loop não pode virar tempestade de dumps).
var freezeDumpCooldown atomic.Int64 // unix nanos do último dump

// vigiaCriacaoJanela observa a main thread ENQUANTO uma janela nasce. Cada
// falha de sonda é logada com o tempo de prisão; no primeiro sinal de prisão
// acima de mainFreezeDumpAfter, grava o MINIDUMP IMEDIATO — não no relançamento
// (90 s depois, quando o estado já se perdeu), e sim com a main thread AINDA
// presa na chamada nativa culpada.
func (a *App) vigiaCriacaoJanela(rotulo string) (libera func()) {
	if a == nil || a.app == nil {
		return func() {}
	}
	inicio := time.Now()
	stop := make(chan struct{})
	dumpFeito := &atomic.Bool{}
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Second):
			}
			if a.probeMainThreadWithin(3 * time.Second) {
				continue // respondendo
			}
			preso := time.Since(inicio).Round(time.Second)
			a.Logs.Append("[janela] " + rotulo + ": MAIN THREAD presa ha ~" +
				preso.String() + " durante a criacao (sinal de travamento nativo)")
			// CONTENÇÃO 21/10: trava na janela do chat = disjuntor persistente
			// (impede o loop congelar-relançar-congelar enquanto a sessão viva
			// força a reconciliação a recriar a janela em cada relançamento).
			if strings.Contains(rotulo, "janela do chat") {
				chatWindowBreakerDisparaSeTravado(rotulo, preso)
			}
			if preso < mainFreezeDumpAfter {
				continue
			}
			if dumpFeito.CompareAndSwap(false, true) {
				if last := freezeDumpCooldown.Load(); last == 0 || time.Since(time.Unix(0, last)) > 2*time.Minute {
					freezeDumpCooldown.Store(time.Now().UnixNano())
					a.Logs.Append("[janela] capturando minidump IMEDIATO com a main thread presa (" + rotulo + ")")
					captureMainThreadFreezeDump(a)
				} else {
					a.Logs.Append("[janela] minidump recente ja existe; pulando captura extra")
				}
			}
		}
	}()
	return func() {
		close(stop)
		dur := time.Since(inicio).Round(time.Millisecond)
		if dur > 3*time.Second {
			a.Logs.Append("[janela] " + rotulo + ": criacao concluida em " + dur.String())
		}
	}
}
