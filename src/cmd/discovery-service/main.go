//go:build windows

// discovery-service — binário do serviço Windows do Discovery Agent
// (PLANO_SEPARACAO_SERVICO_UI.md, Fase B — decisão D1: dois binários).
//
// Este executável é instalado pelo NSIS como serviço `DiscoveryAgent`
// (LocalSystem, start= auto). Ele roda o core do agent (agentConn/NATS,
// inventory, sync, automation, P2P, self-update, outboxes) e NUNCA inicializa
// a UI Wails: sem janela, sem tray, sem WebView2, sem frontend embedado.
//
// A UI do usuário é o binário discovery-agent.exe (sessão interativa), que
// conecta a este serviço via IPC named pipe (modo companion).
//
// Aceita as flags:
//   --service                modo serviço explícito (auto-detectado pelo SCM também)
//   --remote-session-worker  modo worker de remote session (spawnado pelo
//                            próprio serviço na sessão interativa — NUNCA
//                            rodar o core nesses processos)
//   --terminal-dispatcher    dispatcher do terminal (ConPTY isolado em processo
//                            filho, spawnado pelo core/worker — nunca rodar o
//                            core nesses processos)
//   (sem flag)               roda o core direto (debug/manual, fora do SCM)

package main

import (
	"log"
	"os"
	"strings"

	appkg "discovery/app"
)

// hasStartupArg replica a detecção de flags do main.go da UI (case-insensitive,
// trim de espaços — mesmas regras de parsing dos dois binários).
func hasStartupArg(arg string) bool {
	for _, value := range os.Args[1:] {
		if strings.EqualFold(strings.TrimSpace(value), arg) {
			return true
		}
	}
	return false
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	// ── Modo worker de remote session (PLANO_AGENT_SERVICE_SYSTEM.md §7.2) ──
	// O serviço spawna ESTE executável com --remote-session-worker na sessão
	// interativa (CreateProcessAsUser com lpDesktop winsta0\winlogon quando
	// não há usuário logado). O worker roda na SESSÃO INTERATIVA como SYSTEM:
	// captura a tela REAL da máquina — inclusive o desktop Winlogon ANTES do
	// login (o input desktop é seguido por CheckDesktopSwitch a cada iteração,
	// padrão MeshAgent kvm.c) — e injeta input em todas as janelas.
	//
	// FIX 2026-09-13 (acesso remoto não conecta): este dispatch NÃO existia no
	// binário do serviço — a flag só era tratada no main.go da UI. O processo
	// spawnado bootava uma SEGUNDA instância completa do serviço (core, NATS,
	// sync) em vez do worker: nenhum frame era publicado, o spawn reportava
	// exitCode=0 falso e as duas instâncias disputavam config/SQLite/NATS.
	if hasStartupArg("--remote-session-worker") || hasStartupArg("/remote-session-worker") || hasStartupArg("-remote-session-worker") {
		appkg.RunRemoteSessionWorker()
		return
	}

	// ── Modo dispatcher do terminal (ConPTY isolado em processo filho) ──
	// Spawnado pelo core/worker quando o dispatcher está habilitado
	// (padrão a partir de 13/09/2026). Sem este dispatch, o processo filho
	// bootaria OUTRA instância completa do serviço (mesma classe de bug do
	// --remote-session-worker) e o terminal nunca iniciaria.
	if hasStartupArg("--terminal-dispatcher") || hasStartupArg("/terminal-dispatcher") || hasStartupArg("-terminal-dispatcher") {
		appkg.TerminalRunDispatcher()
		return
	}

	appkg.RunServiceMode()
}
