package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

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
	cmd := exec.Command("rundll32.exe", "comsvcs.dll,MiniDump",
		fmt.Sprintf("%d", os.Getpid()), dump)
	if err := cmd.Start(); err != nil {
		a.Logs.Append("[main-watchdog] falha ao iniciar o minidump: " + err.Error())
		return
	}
	// NÃO esperamos o rundll32: o processo de dump vive por si e o exit da UI
	// não o derruba.
	go func() { _ = cmd.Wait() }()
	a.Logs.Append("[main-watchdog] minidump do congelamento gravado: " + dump)
}

// freezeDumpDir devolve o diretório de diagnóstico (ao lado do logstore).
func freezeDumpDir() string {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		return ""
	}
	return filepath.Join(programData, "Discovery", "logs", "freeze-dumps")
}

// probeMainThread dispara uma tarefa trivial na main thread e espera no prazo.
// Devolve true quando a thread respondeu.
func (a *App) probeMainThread() bool {
	done := make(chan struct{})
	var once atomic.Bool
	go func() {
		// Shutdown da aplicação pode deixar a tarefa sem processamento: o
		// goroutine vaza de propósito (uma no máximo por sonda) — o processo
		// encerra em seguida em qualquer caminho de timeout.
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
	case <-time.After(mainThreadProbeTimeout):
		return false
	}
}
