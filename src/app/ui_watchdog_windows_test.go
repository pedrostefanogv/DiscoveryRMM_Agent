//go:build windows

package app

import (
	"context"
	"net"
	"testing"
)

// Aqui o watchdog é construído com as dependências REAIS do serviço (processos,
// sessão do usuário, spawn). O que se verifica é o wiring: ele existe, é único e
// aceita pedido de relançamento sem disparar nenhuma verificação.
func TestStartUIWatchdog_Wiring(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: true})
	// O serviço real só ativa o watchdog com o servidor IPC no ar (sem ele
	// nenhuma UI conseguiria conectar). Reproduzimos esse estado com um servidor
	// vazio: sem clientes conectados.
	a.ipcServer = &IPCServer{conns: make(map[net.Conn]*ipcClientConn)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a.startUIWatchdog(ctx)
	first := a.uiWatchdog.Load()
	if first == nil {
		t.Fatal("startUIWatchdog deveria registrar o watchdog em modo serviço")
	}

	// Idempotente: um segundo RunCore/startup não recria nem reinicia o loop.
	a.startUIWatchdog(ctx)
	if a.uiWatchdog.Load() != first {
		t.Fatal("startUIWatchdog não pode substituir o watchdog já ativo")
	}

	// Pedido de relançamento sem UI conectada não pode entrar em pânico nem
	// bloquear o chamador (o spawn real falha por não existir discovery-agent.exe
	// ao lado do binário de teste — e isso é só um log).
	a.requestUIWatchdogSpawn("teste")
}

// Sem servidor IPC o watchdog NÃO pode ser ativado: ele relançaria a interface
// em ciclo (a UI sobe, não consegue conectar e é tratada como travada).
func TestStartUIWatchdog_DisabledWithoutIPCServer(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: true})
	if a.ipcServer != nil {
		t.Fatal("cenário de teste precisa começar sem servidor IPC")
	}

	a.startUIWatchdog(context.Background())
	if a.uiWatchdog.Load() != nil {
		t.Fatal("sem servidor IPC o watchdog não deve ser ativado")
	}
	// E o pedido de relançamento vindo de um convite não pode explodir.
	a.requestUIWatchdogSpawn("teste")
}

// A mensagem de estado não pode repetir a cada tick: numa máquina sem usuário
// logado o watchdog roda por horas.
func TestUIWatchdog_NoticeIsDeduplicated(t *testing.T) {
	f := &uiWatchdogFake{noSession: true}
	w := newUIWatchdogFake(f)

	for i := 0; i < 5; i++ {
		w.Tick()
	}

	const expected = "sem sessão de usuário ativa — UI será relançada quando alguém fizer logon"
	sessionLogs := 0
	for _, line := range f.logs {
		if line == expected {
			sessionLogs++
		}
	}
	if sessionLogs != 1 {
		t.Fatalf("a mesma condição deve gerar UMA linha de log, não uma por tick; obtido %d (logs=%v)",
			sessionLogs, f.logs)
	}
}

// Um relançamento bem-sucedido libera a deduplicação: se a UI cair de novo mais
// tarde, o aviso volta a aparecer.
func TestUIWatchdog_NoticeResetsAfterSuccessfulSpawn(t *testing.T) {
	f := &uiWatchdogFake{}
	w := newUIWatchdogFake(f)

	w.Tick()
	w.Tick()
	if len(f.spawns) != 1 {
		t.Fatalf("esperado 1 spawn; spawns=%v", f.spawns)
	}

	f.hasUI = true
	w.Tick() // UI voltou
	f.hasUI = false
	f.advance(uiWatchdogSpawnCooldown)
	w.Tick()
	w.Tick()
	if len(f.spawns) != 2 {
		t.Fatalf("esperado novo relançamento depois da queda; spawns=%v", f.spawns)
	}
}
