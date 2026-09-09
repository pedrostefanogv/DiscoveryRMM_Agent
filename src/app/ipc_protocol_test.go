package app

// Testes das melhorias da revisão 3 (PLANO_SEPARACAO_SERVICO_UI.md §0.4):
// handshake versionado, RPCs logs:tail/automation:state/updates:scan e
// CompanionController (máquina de estados do fallback standalone).

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// ── #4 Handshake versionado ────────────────────────────────────────────────

func TestIPCProtocolVersionConstant(t *testing.T) {
	if IPCProtocolVersion < 1 {
		t.Fatal("IPCProtocolVersion deve ser >= 1")
	}
}

func TestIsServiceProtocolCompatible(t *testing.T) {
	if !IsServiceProtocolCompatible(1) {
		t.Fatal("protocolo 1 (serviços antigos) deve ser compatível")
	}
	if !IsServiceProtocolCompatible(IPCProtocolVersion) {
		t.Fatal("protocolo atual deve ser compatível")
	}
	if IsServiceProtocolCompatible(0) || IsServiceProtocolCompatible(IPCProtocolVersion+1) {
		t.Fatal("protocolo 0 ou futuro deve ser incompatível")
	}
}

func TestHelloAckPayloadContainsProtocol(t *testing.T) {
	// O payload do hello_ack enviado pelo serviço inclui a versão do protocolo
	// (validação estrutural — o round-trip de rede real é coberto no checklist
	// manual, pois exige named pipe).
	ack := NewIPCMessage(IPCMsgHelloAck, map[string]any{
		"service":  ServiceName,
		"protocol": IPCProtocolVersion,
	})
	data := EncodeIPCMessage(ack)
	if data == nil {
		t.Fatal("falha ao serializar hello_ack")
	}
	decoded, err := DecodeIPCMessage(newTestBufioReader(data))
	if err != nil {
		t.Fatalf("DecodeIPCMessage: %v", err)
	}
	if decoded.Type != IPCMsgHelloAck {
		t.Fatalf("tipo esperado hello_ack, got %s", decoded.Type)
	}
	if v, _ := decoded.Payload["protocol"].(float64); int(v) != IPCProtocolVersion {
		t.Fatalf("protocol esperado %d, got %v", IPCProtocolVersion, decoded.Payload["protocol"])
	}
}

// ── #2 RPCs novos (logs:tail / automation:state / updates:scan) ────────────

func TestHandleIPCRequestTailLogs(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: true})
	a.Logs.Append("linha-1")
	a.Logs.Append("linha-2")
	a.Logs.Append("linha-3")

	resp := a.handleIPCRequest(nil, map[string]any{"method": "logs:tail", "count": float64(2)})
	if resp["ok"] != true {
		t.Fatalf("logs:tail deve retornar ok=true, got %v (%v)", resp["ok"], resp["error"])
	}
	data := resp["data"].(map[string]any)
	lines := data["lines"].([]string)
	if len(lines) != 2 || lines[0] != "linha-2" || lines[1] != "linha-3" {
		t.Fatalf("esperado últimas 2 linhas, got %v", lines)
	}
}

func TestHandleIPCRequestTailLogsDefaultCount(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: true})
	for i := 0; i < 250; i++ {
		a.Logs.Append("x")
	}
	resp := a.handleIPCRequest(nil, map[string]any{"method": "logs:tail"})
	data := resp["data"].(map[string]any)
	if lines := data["lines"].([]string); len(lines) != 100 {
		t.Fatalf("default count esperado 100, got %d", len(lines))
	}
}

func TestHandleIPCRequestAutomationStateNilSvc(t *testing.T) {
	a := &App{} // automationSvc nil
	resp := a.handleIPCRequest(nil, map[string]any{"method": "automation:state"})
	if resp["ok"] != false {
		t.Fatal("automation:state com serviço nil deve retornar ok=false")
	}
}

func TestHandleIPCRequestUpdatesScanNilSvc(t *testing.T) {
	a := &App{} // updatesSvc nil
	resp := a.handleIPCRequest(nil, map[string]any{"method": "updates:scan"})
	if resp["ok"] != false {
		t.Fatal("updates:scan com serviço nil deve retornar ok=false")
	}
}

// ── #3 CompanionController ─────────────────────────────────────────────────

type fakeTuner struct {
	mu         sync.Mutex
	sendErr    error
	sends      int
	lostReason string
	fellBack   bool
}

func (f *fakeTuner) SendStatus() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends++
	return f.sendErr
}
func (f *fakeTuner) OnServiceLost(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lostReason = reason
}
func (f *fakeTuner) OnFallbackStandalone() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fellBack = true
}

// waitSends aguarda até que o tuner acumule n envios (sincronização de teste).
func waitSends(t *testing.T, tuner *fakeTuner, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tuner.mu.Lock()
		got := tuner.sends
		tuner.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout aguardando %d sends (got via contador)", n)
}

func TestCompanionControllerResetsFailuresOnSuccess(t *testing.T) {
	tuner := &fakeTuner{}
	cfg := CompanionConfig{PollInterval: time.Millisecond, MaxFailures: 3, Reason: "test"}
	c := NewCompanionController(tuner, cfg)

	ticks := make(chan time.Time, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, ticks); close(done) }()

	// 2 falhas + 1 sucesso + 2 falhas → sem fallback. Como os ticks são
	// buffered, sincroniza cada fase pelo contador de sends antes de mudar
	// sendErr (evita race entre o produtor de ticks e o loop do controller).
	tuner.mu.Lock()
	tuner.sendErr = context.DeadlineExceeded
	tuner.mu.Unlock()
	ticks <- time.Now()
	waitSends(t, tuner, 1)
	ticks <- time.Now()
	waitSends(t, tuner, 2)
	tuner.mu.Lock()
	tuner.sendErr = nil
	tuner.mu.Unlock()
	ticks <- time.Now()
	waitSends(t, tuner, 3)
	tuner.mu.Lock()
	tuner.sendErr = context.DeadlineExceeded
	tuner.mu.Unlock()
	ticks <- time.Now()
	waitSends(t, tuner, 4)
	ticks <- time.Now()
	waitSends(t, tuner, 5)

	cancel()
	<-done

	tuner.mu.Lock()
	defer tuner.mu.Unlock()
	if tuner.fellBack {
		t.Fatal("fallback NÃO deve disparar quando o sucesso interrompe a sequência de falhas")
	}
	if c.Failures() != 2 {
		t.Fatalf("contador esperado 2 (resetado pelo sucesso), got %d", c.Failures())
	}
}

func TestCompanionControllerFallbackAfterMaxFailures(t *testing.T) {
	tuner := &fakeTuner{sendErr: context.DeadlineExceeded}
	cfg := CompanionConfig{PollInterval: time.Millisecond, MaxFailures: 3, Reason: "service_unreachable"}
	c := NewCompanionController(tuner, cfg)

	ticks := make(chan time.Time, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, ticks); close(done) }()

	for i := 0; i < 3; i++ {
		ticks <- time.Now()
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("controller não retornou após MaxFailures")
	}
	cancel()

	tuner.mu.Lock()
	defer tuner.mu.Unlock()
	if tuner.lostReason != "service_unreachable" {
		t.Fatalf("lostReason esperado service_unreachable, got %q", tuner.lostReason)
	}
	if !tuner.fellBack {
		t.Fatal("fallback standalone deve disparar após MaxFailures")
	}
}

func TestCompanionControllerStopsOnContextCancel(t *testing.T) {
	tuner := &fakeTuner{}
	c := NewCompanionController(tuner, DefaultCompanionConfig())
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, ticks); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run deve retornar quando ctx é cancelado")
	}
}

func TestCompanionControllerNotReentrant(t *testing.T) {
	tuner := &fakeTuner{}
	c := NewCompanionController(tuner, DefaultCompanionConfig())
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, ticks)
	time.Sleep(10 * time.Millisecond)
	go c.Run(ctx, ticks) // segunda chamada deve ser no-op
	time.Sleep(10 * time.Millisecond)
}

// ── helpers de RPC existentes (regressão) ──────────────────────────────────

func TestCompanionBridgesStandaloneReturnFalse(t *testing.T) {
	// Sem ipcClient (standalone), os bridges companion retornam (nil, false).
	a := NewApp(AppStartupOptions{ServiceMode: false})
	if lines, ok := a.getTailLogsCompanion(10); ok || lines != nil {
		t.Fatal("getTailLogsCompanion deve retornar false em standalone")
	}
	if raw, ok := a.getAutomationStateCompanion(); ok || raw != nil {
		t.Fatal("getAutomationStateCompanion deve retornar false em standalone")
	}
	if items, ok := a.getPendingUpdatesCompanion(); ok || items != nil {
		t.Fatal("getPendingUpdatesCompanion deve retornar false em standalone")
	}
}

func TestTailLogsCompanionPayloadDecoding(t *testing.T) {
	// Valida o decode do payload do RPC logs:tail (round-trip JSON).
	resp := map[string]any{"ok": true, "data": map[string]any{
		"lines": []any{"a", "b"},
	}}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["lines"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(lines) != 2 || lines[0] != "a" {
		t.Fatalf("esperado [a b], got %v", lines)
	}
}
