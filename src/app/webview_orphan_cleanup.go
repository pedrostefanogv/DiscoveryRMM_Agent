package app

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

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
// A limpeza roda no STARTUP da UI, ANTES da primeira janela: mata os
// msedgewebview2 cujo PAI era um discovery-agent que já não existe (órfãos).
// Browsers de UIs VIVAS (pai vivo na cadeia) não são tocados.

const webviewBrowserProcessName = "msedgewebview2.exe"

type orphanProcInfo struct {
	pid     uint32
	parent  uint32
	isAgent bool
	isWebvw bool
}

// killOrphanedWebviewBrowsers devolve quantos browsers órfãos foram mortos.
func killOrphanedWebviewBrowsers() int {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snapshot)

	procs := []orphanProcInfo{}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return 0
	}
	for {
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		procs = append(procs, orphanProcInfo{
			pid:     entry.ProcessID,
			parent:  entry.ParentProcessID,
			isAgent: name == "discovery-agent.exe",
			isWebvw: name == webviewBrowserProcessName,
		})
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}

	aliveAgents := map[uint32]bool{}
	for _, p := range procs {
		if p.isAgent {
			aliveAgents[p.pid] = true
		}
	}
	killed := 0
	for _, p := range procs {
		if !p.isWebvw || aliveAgents[p.parent] {
			continue
		}
		// Pai não é uma UI viva. Se o pai era um discovery-agent morto (ou um
		// browser também órfão), o nó inteiro está órfão — mata o browser raiz.
		if !orphanChainHasLiveAgent(p.parent, procs, aliveAgents, 0) {
			if ph, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, p.pid); err == nil {
				_ = windows.TerminateProcess(ph, 1)
				_ = windows.CloseHandle(ph)
				killed++
			}
		}
	}
	return killed
}

// orphanChainHasLiveAgent sobe a cadeia de pais procurando um discovery-agent
// VIVO (defensivo para sessões múltiplas): achou = NÃO mexer.
func orphanChainHasLiveAgent(parent uint32, procs []orphanProcInfo, aliveAgents map[uint32]bool, depth int) bool {
	if depth > 8 {
		return false
	}
	for _, p := range procs {
		if p.pid != parent {
			continue
		}
		if p.isAgent && aliveAgents[p.pid] {
			return true
		}
		if !p.isAgent {
			return false // pai é processo alheio (não nosso): não mexe
		}
		return orphanChainHasLiveAgent(p.parent, procs, aliveAgents, depth+1)
	}
	return false
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
