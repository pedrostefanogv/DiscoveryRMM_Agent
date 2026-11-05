package automation

import (
	"context"
	"encoding/json"
	"testing"
)

// contarResultados soma as amostras que o painel enxerga no snapshot.
func contarResultados(snap SensorDebugSnapshot) int {
	total := 0
	for _, view := range snap.Sensors {
		total += len(view.LastResults)
	}
	return total
}

// TestSensorHistoryRegistradaAposColetaReal fecha a lacuna que deixou o painel
// de debug mostrando "Nenhuma coleta registrada na janela atual" em producao.
//
// Os testes existentes so verificavam o AGENDAMENTO (markSensorsDue devolve
// count > 0). Nenhum verificava o efeito: que a coleta roda de fato e que o
// SNAPSHOT do painel enxerga o resultado. Agendar sem coletar e exatamente o
// modo de falha silencioso que o operador via na tela.
func TestSensorHistoryRegistradaAposColetaReal(t *testing.T) {
	service := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-1"} }, func(string) {})

	var response SensorSyncResponse
	if err := json.Unmarshal([]byte(sensorSyncResponseFixture), &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	service.applySensorPolicy("agent-1", &response, false)

	// O painel precisa saber o tamanho da janela e nao pode inventar historico.
	if snap := service.SensorDebugSnapshot(); snap.HistoryWindow == 0 {
		t.Fatal("HistoryWindow = 0: o painel nao saberia o tamanho da janela")
	} else if got := contarResultados(snap); got != 0 {
		t.Fatalf("historico deveria comecar vazio, got %d", got)
	}

	// Executa a coleta REAL de cada sensor da policy, pelo mesmo caminho que o
	// loop usa (runSensor), sem depender de ticker nem de rede.
	sensors, _, _ := service.sensorStateForTests()
	if len(sensors) == 0 {
		t.Fatal("policy aplicada nao produziu sensores")
	}
	for _, cfg := range sensors {
		service.runSensor(context.Background(), cfg)
	}

	// O historico tem de registrar uma amostra por sensor coletado — inclusive
	// quando a coleta falha, porque a FALHA tambem e evidencia para o operador.
	snap := service.SensorDebugSnapshot()
	if got := contarResultados(snap); got == 0 {
		t.Fatal("nenhum sensor tem LastResults apos a coleta: e o defeito do painel vazio")
	}

	// E cada amostra precisa trazer StartedAt, senao a tabela mostra "Ultima"
	// vazia mesmo com a coleta tendo acontecido.
	for _, view := range snap.Sensors {
		if len(view.LastResults) == 0 {
			continue
		}
		if view.LastResults[0].StartedAt == "" {
			t.Fatalf("sensor %q: LastResults sem StartedAt", view.Name)
		}
	}
}
