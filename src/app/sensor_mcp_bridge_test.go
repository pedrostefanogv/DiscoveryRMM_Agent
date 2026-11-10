package app

// ─────────────────────────────────────────────────────────────────────────────
// Testes da ponte MCP dos sensores (AppBridge.SensorDebugSnapshotForMCP).
//
// A tool MCP get_sensors é de LEITURA e reaproveita a MESMA cadeia do painel de
// debug. Estes testes fixam:
//   - companion primeiro (é o serviço que coleta — M5), local como fallback;
//   - nunca erro e nunca panic, mesmo com App/serviço/mercado ausentes;
//   - a ponte alimenta a tool: snapshot com sensores chega ao payload;
//   - o método NÃO é mutante (não entra no canal admin / ipcKnownMutatingMethods).
//
// Os helpers (newSensorDebugAppForRun, stubSensorDebugIPC, snapshotDoServico,
// appCompanion) vivem em sensor_debug_commands_test.go.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"strings"
	"testing"

	"discovery/app/core/automation"
)

// TestSensorMCPPonteVemDoServico prova o roteamento companion: com o RPC
// respondendo, a ponte devolve o snapshot DO SERVIÇO. Sem isso a tool leria o
// AutomationSvc local (que em companion existe mas nunca coleta) e responderia
// "0 sensores" para uma máquina com sensores configurados.
func TestSensorMCPPonteVemDoServico(t *testing.T) {
	a := newSensorDebugAppForRun(t)

	doServico := automation.SensorDebugSnapshot{
		AgentID:       "agent-servico",
		GeneratedAt:   "2026-11-05T00:09:48Z",
		HistoryWindow: 100,
		Sensors:       []automation.SensorDebugView{{SensorID: "s1", Name: "Disco C"}},
	}
	stubSensorDebugIPC(t, snapshotDoServico(t, doServico), true)

	snapshot := a.SensorDebugSnapshotForMCP()
	if len(snapshot.Sensors) != 1 || snapshot.Sensors[0].SensorID != "s1" {
		t.Fatalf("snapshot deveria vir do servico, veio %#v", snapshot.Sensors)
	}
	if snapshot.AgentID != "agent-servico" {
		t.Fatalf("agentId deveria vir do servico: %q", snapshot.AgentID)
	}
}

// TestSensorMCPPonteFallbackLocal cobre standalone (ou RPC mudo): sem resposta
// do serviço vale o motor local.
func TestSensorMCPPonteFallbackLocal(t *testing.T) {
	a := newSensorDebugAppForRun(t)
	stubSensorDebugIPC(t, nil, false) // RPC não respondeu

	snapshot := a.SensorDebugSnapshotForMCP()
	// O motor local desta App de teste existe e tem policy vazia: o contrato
	// mínimo é não devolver nil e ter Sensors não-nil.
	if snapshot.Sensors == nil {
		t.Fatal("Sensors deveria ser nao-nil no fallback local")
	}
}

// TestSensorMCPPonteNuncaFalhaNemEntraEmPanic cobre os piores cenários: App
// vazia, serviço ausente, RPC mudo e RPC respondendo vazio. A tool de leitura
// precisa responder SEMPRE — "não tenho sensores" é resposta legítima.
func TestSensorMCPPonteNuncaFalhaNemEntraEmPanic(t *testing.T) {
	casos := []struct {
		nome    string
		montar  func(t *testing.T) *App
		rpcOK   bool
	}{
		{"app_vazia_sem_rpc", func(*testing.T) *App { return &App{} }, false},
		{"app_vazia_com_rpc_vazio", func(*testing.T) *App { return &App{} }, true},
		{"app_nil", func(*testing.T) *App { return nil }, false},
		{"companion_sem_motor", func(*testing.T) *App { return appCompanion() }, false},
		{"app_com_motor", func(t *testing.T) *App { return newSensorDebugAppForRun(t) }, true},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			a := c.montar(t)
			stubSensorDebugIPC(t, snapshotDoServico(t, automation.SensorDebugSnapshot{}), c.rpcOK)

			snapshot := a.SensorDebugSnapshotForMCP() // não pode entrar em panic
			if snapshot.Sensors == nil {
				t.Fatal("Sensors deveria ser sempre nao-nil (a tool nunca trata null)")
			}
		})
	}
}

// TestSensorMCPPonteAlimentaPayloadDoNucleo garante que a ponte entrega à tool
// um snapshot UTILIZÁVEL: o mesmo retrato do painel, sem tradução no caminho.
func TestSensorMCPPonteAlimentaPayloadDoNucleo(t *testing.T) {
	a := newSensorDebugAppForRun(t)
	aplicarPolicyDeTeste(t, a) // policy local com 1 sensor
	stubSensorDebugIPC(t, nil, false)

	snapshot := a.SensorDebugSnapshotForMCP()
	if len(snapshot.Sensors) != 1 {
		t.Fatalf("snapshot deveria ter 1 sensor da policy, veio %d", len(snapshot.Sensors))
	}
	if got := string(snapshot.Sensors[0].SensorType); !strings.EqualFold(got, "Ping") {
		t.Fatalf("tipo do sensor inesperado: %q", got)
	}
	if snapshot.Sensors[0].SensorID == "" {
		t.Fatal("sensorId deveria estar preenchido")
	}
	// HistoryWindow é o teto do ring local: é o que a tool expõe ao modelo.
	if snapshot.HistoryWindow <= 0 {
		t.Fatalf("historyWindow deveria ser > 0, veio %d", snapshot.HistoryWindow)
	}
}

// TestSensorMCPNaoEhMetodoMutante trava a classificação: a consulta de sensores
// é LEITURA. Se alguém registrar um método RPC com sufixo mutante por engano,
// isto avisa — a tool não pode exigir canal admin.
func TestSensorMCPNaoEhMetodoMutante(t *testing.T) {
	// O método RPC de leitura usado pela ponte continua livre.
	if ipcRequestRequiresAdmin("sensors:debug_snapshot", nil) {
		t.Fatal("sensors:debug_snapshot e leitura e nao deve exigir admin")
	}
	// E nenhum método de sensores entra na tabela de mutantes conhecidos.
	for metodo := range ipcKnownMutatingMethods {
		if strings.HasPrefix(metodo, "sensors:") {
			t.Fatalf("metodo de sensores %q nao pode ser classificado como mutante", metodo)
		}
	}
}
