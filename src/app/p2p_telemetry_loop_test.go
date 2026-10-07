package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/consolidation"
	"discovery/app/core/database"
	debugsvc "discovery/app/debug"
)

// TestStartP2PTelemetryLoopForcesInitialSendPastConsolidationGate garante que o
// envio inicial não é adiado por uma janela de batching persistida do
// ConsolidationEngine (ex.: política p2p_telemetry=5min vinda do servidor).
func TestStartP2PTelemetryLoopForcesInitialSendPastConsolidationGate(t *testing.T) {
	const token = "mdz_test_token"
	const agentID = "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"

	var telemetryHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/p2p/telemetry") {
			atomic.AddInt32(&telemetryHits, 1)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	engine := consolidation.New(db, agentID)
	engine.Enable()
	engine.SetPolicy("p2p_telemetry", agentconfig.ConsolidationMode5Min)
	// Janela recém-fluída: ShouldFlush("p2p_telemetry") devolveria false.
	now := time.Now()
	if err := db.UpsertConsolidationWindowState(database.ConsolidationWindowStateEntry{
		AgentID:       agentID,
		DataType:      "p2p_telemetry",
		WindowMode:    agentconfig.ConsolidationMode5Min,
		WindowStartAt: now,
		LastFlushAt:   now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("upsert window state: %v", err)
	}

	a := &App{ctx: context.Background()}
	a.DebugSvc = debugsvc.NewService(debugsvc.Options{})
	a.DebugSvc.ApplyRuntimeConnectionConfig("http", strings.TrimPrefix(server.URL, "http://"), token, agentID, "", "")
	a.P2PCoord = newP2PCoordinator(a)
	a.applyP2PConfig(P2PConfig{Enabled: true})
	a.ConsolEngine = engine

	original := p2pTelemetryInterval
	p2pTelemetryInterval = time.Hour
	defer func() { p2pTelemetryInterval = original }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.StartP2PTelemetryLoop(ctx)
	}()

	// 10s (antes 3s): a construção do payload de telemetria leva ~3s nesta
	// máquina e o deadline de 3s empatava com a operação — o teste falhava de
	// forma intermitente sem nenhum defeito no produto.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&telemetryHits) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartP2PTelemetryLoop nao encerrou apos o cancelamento")
	}

	if got := atomic.LoadInt32(&telemetryHits); got == 0 {
		t.Fatal("o envio inicial deveria ignorar o gate de consolidacao")
	}
}

// TestStartupWiresP2PTelemetryLoop garante que o core (runStagedStartup)
// mantém o chamador do loop de telemetria. Foi exatamente a remoção desse
// chamador na refatoração M5 (commit a6ddadd) que zerou as métricas P2P.
func TestStartupWiresP2PTelemetryLoop(t *testing.T) {
	source, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatalf("ler app.go: %v", err)
	}
	if !strings.Contains(string(source), "a.StartP2PTelemetryLoop(ctx)") {
		t.Fatal("app.go nao inicia StartP2PTelemetryLoop: o core do agente deixaria de enviar telemetria P2P")
	}
}

// TestStartP2PTelemetryLoopSendsTelemetry garante que o loop de telemetria P2P
// realmente inicia e envia snapshots ao servidor.
//
// Regressão da refatoração M5 (commit a6ddadd): o único chamador de
// StartP2PTelemetryLoop foi removido do startup da UI e nunca foi re-adicionado
// ao core do serviço. Sem o loop, nenhuma telemetria P2P chegava ao servidor e
// as métricas do dashboard ficavam permanentemente zeradas.
func TestStartP2PTelemetryLoopSendsTelemetry(t *testing.T) {
	const token = "mdz_test_token"
	const agentID = "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"

	var telemetryHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/p2p/telemetry"):
			if r.Method != http.MethodPost {
				t.Errorf("telemetry method = %s, want POST", r.Method)
			}
			atomic.AddInt32(&telemetryHits, 1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("{\"received\":true}"))
		case strings.HasSuffix(r.URL.Path, "/p2p/seed-plan"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{\"plan\":{\"totalAgents\":1,\"configuredPercent\":10,\"minSeeds\":2,\"selectedSeeds\":1}}"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	a := &App{ctx: context.Background()}
	a.DebugSvc = debugsvc.NewService(debugsvc.Options{})
	a.DebugSvc.ApplyRuntimeConnectionConfig("http", strings.TrimPrefix(server.URL, "http://"), token, agentID, "", "")
	a.P2PCoord = newP2PCoordinator(a)
	a.applyP2PConfig(P2PConfig{Enabled: true})

	// Cadência curta apenas para o teste.
	original := p2pTelemetryInterval
	p2pTelemetryInterval = 20 * time.Millisecond
	defer func() { p2pTelemetryInterval = original }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.StartP2PTelemetryLoop(ctx)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&telemetryHits) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartP2PTelemetryLoop nao encerrou apos o cancelamento do contexto")
	}

	if got := atomic.LoadInt32(&telemetryHits); got == 0 {
		t.Fatal("loop de telemetria nao enviou nenhum snapshot ao servidor")
	}
}

// TestStartP2PTelemetryLoopSkipsWhenP2pDisabled garante que, com P2P
// desabilitado, nenhuma telemetria zerada é enviada (evita KPIs fantasma no
// dashboard). O outbox continua sendo drenado.
func TestStartP2PTelemetryLoopSkipsWhenP2pDisabled(t *testing.T) {
	const token = "mdz_test_token"
	const agentID = "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"

	var telemetryHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/p2p/telemetry") {
			atomic.AddInt32(&telemetryHits, 1)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	a := &App{ctx: context.Background()}
	a.DebugSvc = debugsvc.NewService(debugsvc.Options{})
	a.DebugSvc.ApplyRuntimeConnectionConfig("http", strings.TrimPrefix(server.URL, "http://"), token, agentID, "", "")
	a.P2PCoord = newP2PCoordinator(a)
	a.applyP2PConfig(P2PConfig{Enabled: false})

	original := p2pTelemetryInterval
	p2pTelemetryInterval = 20 * time.Millisecond
	defer func() { p2pTelemetryInterval = original }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.StartP2PTelemetryLoop(ctx)
	}()

	time.Sleep(150 * time.Millisecond) // vários ticks
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartP2PTelemetryLoop nao encerrou apos o cancelamento")
	}

	if got := atomic.LoadInt32(&telemetryHits); got != 0 {
		t.Fatalf("P2P desabilitado nao deveria enviar telemetria, recebeu %d POST(s)", got)
	}
}

// TestStartP2PTelemetryLoopSendsImmediately garante que o primeiro snapshot sai
// sem esperar p2pTelemetryInterval — era isso que deixava o card "Sem telemetria
// P2P" por até 5 minutos após cada deploy/restart do serviço.
func TestStartP2PTelemetryLoopSendsImmediately(t *testing.T) {
	const token = "mdz_test_token"
	const agentID = "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"

	var telemetryHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/p2p/telemetry") {
			atomic.AddInt32(&telemetryHits, 1)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	a := &App{ctx: context.Background()}
	a.DebugSvc = debugsvc.NewService(debugsvc.Options{})
	a.DebugSvc.ApplyRuntimeConnectionConfig("http", strings.TrimPrefix(server.URL, "http://"), token, agentID, "", "")
	a.P2PCoord = newP2PCoordinator(a)
	a.applyP2PConfig(P2PConfig{Enabled: true})

	original := p2pTelemetryInterval
	// Intervalo longo de propósito: o envio inicial não pode depender do tick.
	p2pTelemetryInterval = time.Hour
	defer func() { p2pTelemetryInterval = original }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.StartP2PTelemetryLoop(ctx)
	}()

	// 10s (antes 3s): a construção do payload de telemetria leva ~3s nesta
	// máquina e o deadline de 3s empatava com a operação — o teste falhava de
	// forma intermitente sem nenhum defeito no produto.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&telemetryHits) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartP2PTelemetryLoop nao encerrou apos o cancelamento")
	}

	if got := atomic.LoadInt32(&telemetryHits); got == 0 {
		t.Fatal("o loop deveria enviar um snapshot imediatamente, sem esperar o intervalo")
	}
}
