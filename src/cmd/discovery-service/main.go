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
//   --service   modo serviço explícito (auto-detectado pelo SCM também)
//   (sem flag)  roda o core direto (debug/manual, fora do SCM)

package main

import (
	"log"

	appkg "discovery/app"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	appkg.RunServiceMode()
}
