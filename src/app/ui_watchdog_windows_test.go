//go:build windows

package app

import (
	"context"
	"errors"
	"net"
	"testing"

	"golang.org/x/sys/windows"
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

// ── Regressão WTS: sessão RDP desconectada ────────────────────────────────
//
// Antes só WTSActive entrava na enumeração: quem desconectava o RDP deixava de
// ser visto e ficava sem UI (ou com a UI morta sem relançamento) até
// reconectar. WTSDisconnected precisa entrar — o usuário continua logado e o
// desktop existe. Sessão 0 (serviços) e 0xFFFFFFFF ("sem sessão") nunca entram.
// A enumeração é injetável para o teste não depender de sessões reais da máquina.
func TestUISessionsFromWTS_IncludesDisconnected(t *testing.T) {
	entries := []wtsSessionEntry{
		{SessionID: 0, State: windows.WTSActive},          // serviços
		{SessionID: 1, State: windows.WTSActive},          // console
		{SessionID: 2, State: windows.WTSDisconnected},    // RDP desconectado
		{SessionID: 0xFFFFFFFF, State: windows.WTSActive}, // "sem sessão"
		{SessionID: 3, State: windows.WTSListen},          // listener sem usuário
		{SessionID: 4, State: windows.WTSConnected},       // ainda conectando
	}
	got := uiSessionsFromWTS(entries)
	want := []uint32{1, 2}
	if len(got) != len(want) {
		t.Fatalf("uiSessionsFromWTS = %v, esperado %v (disconnected incluída, 0/0xFFFFFFFF fora)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uiSessionsFromWTS = %v, esperado %v", got, want)
		}
	}
}

// A sessão desconectada chega ao watchdog pelo caminho de produção
// (activeUserSessionsWith), e o fallback de console continua valendo quando a
// API WTS não está disponível.
func TestActiveUserSessionsWith_IncludesDisconnectedAndKeepsFallback(t *testing.T) {
	entries := []wtsSessionEntry{
		{SessionID: 0, State: windows.WTSActive},
		{SessionID: 1, State: windows.WTSActive},
		{SessionID: 2, State: windows.WTSDisconnected},
	}
	if got := activeUserSessionsWith(
		func() ([]wtsSessionEntry, error) { return entries, nil },
		func() uint32 { return 9 },
	); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("sessões = %v, esperado [1 2]", got)
	}

	// API WTS indisponível: comportamento anterior (sessão do console).
	if got := activeUserSessionsWith(
		func() ([]wtsSessionEntry, error) { return nil, errors.New("wtsapi32 indisponível") },
		func() uint32 { return 2 },
	); len(got) != 1 || got[0] != 2 {
		t.Fatalf("fallback de console = %v, esperado [2]", got)
	}

	// Enumeração vazia (sem sessão utilizável) também cai no console.
	if got := activeUserSessionsWith(
		func() ([]wtsSessionEntry, error) { return nil, nil },
		func() uint32 { return 5 },
	); len(got) != 1 || got[0] != 5 {
		t.Fatalf("fallback com lista vazia = %v, esperado [5]", got)
	}

	// Sem console (0/0xFFFFFFFF) não há onde abrir a UI.
	for _, console := range []uint32{0, 0xFFFFFFFF} {
		if got := activeUserSessionsWith(
			func() ([]wtsSessionEntry, error) { return nil, errors.New("wtsapi32 indisponível") },
			func() uint32 { return console },
		); got != nil {
			t.Fatalf("console=0x%X deveria devolver nil; obtido %v", console, got)
		}
	}
}

// O watchdog relança a UI na sessão desconectada sem duplicar a que já existe
// lá (o plano por sessão usa a lista filtrada acima).
func TestUIWatchdogSessions_DisconnectedSessionGetsUI(t *testing.T) {
	f := &uiWatchdogSessionFake{
		hasUI: true,
		sessions: uiSessionsFromWTS([]wtsSessionEntry{
			{SessionID: 0, State: windows.WTSActive},
			{SessionID: 1, State: windows.WTSActive},
			{SessionID: 2, State: windows.WTSDisconnected},
		}),
		counts: map[uint32]int{1: 1, 2: 0},
	}
	w := newUIWatchdogSessionFake(f)

	w.Tick()
	w.Tick()
	if len(f.spawns) != 1 {
		t.Fatalf("só a sessão desconectada sem UI deveria ser relançada; spawns=%v", f.spawns)
	}
	if f.spawns[0] != "2:sessao-sem-ui" {
		t.Fatalf("sessão errada: %v", f.spawns)
	}
}
