package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestSensorLoopNaoTravaComSyncLento e a regressao do defeito visto ao vivo:
// com o servidor de policy lento, o motor ficava ate 45s sem disparar NENHUMA
// coleta (o sync de arranque era sincrono e vinha ANTES do primeiro dispatch),
// e o painel exibia "Nenhuma coleta registrada na janela atual".
//
// Aqui o endpoint de sync demora mais que qualquer timeout curto do teste, e
// mesmo assim a coleta tem de acontecer — porque a policy JA esta em memoria.
func TestSensorLoopNaoTravaComSyncLento(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Segura a resposta: simula servidor lento/inalcancavel sem derrubar.
		time.Sleep(30 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer slow.Close()

	service := NewService(func() RuntimeConfig {
		return RuntimeConfig{AgentID: "agent-slow", BaseURL: slow.URL, Token: "t"}
	}, func(string) {})

	var response SensorSyncResponse
	if err := json.Unmarshal([]byte(sensorSyncResponseFixture), &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Policy ja em memoria: e o caso do agente que ja sincronizou uma vez.
	service.applySensorPolicy("agent-slow", &response, false)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go service.runSensorsLoop(ctx, func() {})

	// Com o sync de arranque bloqueado por 30s, a coleta tem de ocorrer mesmo
	// assim, bem antes disso.
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		snap := service.SensorDebugSnapshot()
		total := 0
		for _, view := range snap.Sensors {
			total += len(view.LastResults)
		}
		if total > 0 {
			t.Logf("coleta ocorreu com o sync de policy ainda travado (%d amostra(s))", total)
			return
		}
	}

	t.Fatal("o motor ficou travado esperando o sync de policy e nao coletou nada")
}
