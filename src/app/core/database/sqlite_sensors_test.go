package database

import (
	"fmt"
	"testing"
	"time"
)

// TestSensorConfigStateRoundTrip cobre a tabela sensor_config_state (espelho de
// automation_policy_state): gravar, ler, substituir e o caso "sem registro".
func TestSensorConfigStateRoundTrip(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, _, _, found, err := db.GetSensorConfig("agent-x"); err != nil || found {
		t.Fatalf("agente sem registro: found=%t err=%v", found, err)
	}

	payload := []byte(`{"fingerprint":"fp-1","sensors":[]}`)
	if err := db.SaveSensorConfig("agent-1", "fp-1", payload); err != nil {
		t.Fatalf("save: %v", err)
	}

	stored, fingerprint, updatedAt, found, err := db.GetSensorConfig("agent-1")
	if err != nil || !found {
		t.Fatalf("get: found=%t err=%v", found, err)
	}
	if string(stored) != string(payload) {
		t.Fatalf("payload = %s, want %s", stored, payload)
	}
	if fingerprint != "fp-1" {
		t.Fatalf("fingerprint = %q, want fp-1", fingerprint)
	}
	if updatedAt.IsZero() {
		t.Fatal("updated_at nao gravado")
	}

	// Upsert: o mesmo agente tem uma única linha (PRIMARY KEY agent_id).
	updated := []byte(`{"fingerprint":"fp-2","sensors":[{"sensorId":"s1"}]}`)
	if err := db.SaveSensorConfig("agent-1", "fp-2", updated); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	stored, fingerprint, _, found, err = db.GetSensorConfig("agent-1")
	if err != nil || !found || fingerprint != "fp-2" || string(stored) != string(updated) {
		t.Fatalf("apos upsert: found=%t fp=%q payload=%s err=%v", found, fingerprint, stored, err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Testes do buffer DURÁVEL de amostras pendentes (sensor_pending_samples).
//
// O teste central aqui é o round-trip + remoção por id; o cenário ponta a ponta
// de restart (perder o processo e reenviar) vive em
// automation/sensors_pending_test.go, que é quem conhece SensorResult.
// ─────────────────────────────────────────────────────────────────────────────

func sensorPendingSample(sensorID, payload string, at time.Time) SensorPendingSample {
	return SensorPendingSample{SensorID: sensorID, Payload: []byte(payload), CreatedAt: at}
}

// TestSensorPendingSamplesRoundTrip grava um lote, lê em ordem FIFO e apaga por id.
func TestSensorPendingSamplesRoundTrip(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Agente sem nada persistido: lista vazia e contagem zero, sem erro.
	if samples, err := db.ListPendingSensorSamples("agent-1", 0); err != nil || len(samples) != 0 {
		t.Fatalf("agente sem amostras: n=%d err=%v", len(samples), err)
	}
	if total, err := db.CountPendingSensorSamples("agent-1"); err != nil || total != 0 {
		t.Fatalf("count inicial = %d err=%v", total, err)
	}

	now := time.Now()
	batch := []SensorPendingSample{
		sensorPendingSample("s1", `{"runId":"r1"}`, now),
		sensorPendingSample("s1", `{"runId":"r2"}`, now.Add(time.Second)),
		sensorPendingSample("s2", `{"runId":"r3"}`, now.Add(2*time.Second)),
	}
	if err := db.SavePendingSensorSamples("agent-1", batch); err != nil {
		t.Fatalf("save: %v", err)
	}
	if total, err := db.CountPendingSensorSamples("agent-1"); err != nil || total != 3 {
		t.Fatalf("count = %d err=%v, want 3", total, err)
	}

	stored, err := db.ListPendingSensorSamples("agent-1", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("listadas = %d, want 3", len(stored))
	}
	// FIFO: a ordem de leitura é a ordem de inserção (id ASC).
	for i, want := range []string{`{"runId":"r1"}`, `{"runId":"r2"}`, `{"runId":"r3"}`} {
		if string(stored[i].Payload) != want {
			t.Fatalf("amostra[%d] = %s, want %s (FIFO)", i, stored[i].Payload, want)
		}
		if stored[i].ID == 0 {
			t.Fatalf("amostra[%d] sem id", i)
		}
		if stored[i].SensorID == "" {
			t.Fatalf("amostra[%d] sem sensorId", i)
		}
	}
	if stored[0].CreatedAt.IsZero() {
		t.Fatal("createdAt nao preservado")
	}

	// limit respeitado.
	if limited, err := db.ListPendingSensorSamples("agent-1", 2); err != nil || len(limited) != 2 {
		t.Fatalf("limit=2 devolveu %d err=%v", len(limited), err)
	}

	// Apaga só o que foi entregue (os dois primeiros).
	if err := db.DeletePendingSensorSamples([]int64{stored[0].ID, stored[1].ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rest, err := db.ListPendingSensorSamples("agent-1", 0)
	if err != nil {
		t.Fatalf("list apos delete: %v", err)
	}
	if len(rest) != 1 || string(rest[0].Payload) != `{"runId":"r3"}` {
		t.Fatalf("restante = %+v, want apenas r3", rest)
	}

	// Idempotente: apagar id já removido não é erro.
	if err := db.DeletePendingSensorSamples([]int64{stored[0].ID}); err != nil {
		t.Fatalf("delete repetido: %v", err)
	}
	// Lista vazia é no-op.
	if err := db.DeletePendingSensorSamples(nil); err != nil {
		t.Fatalf("delete vazio: %v", err)
	}

	// Isolamento por agente: outro agente não vê nem apaga o que não é dele.
	if total, err := db.CountPendingSensorSamples("agent-2"); err != nil || total != 0 {
		t.Fatalf("agent-2 count = %d err=%v, want 0", total, err)
	}
}

// TestSensorPendingSamplesPruneByIdade cobre a retenção por idade (7 dias).
func TestSensorPendingSamplesPruneByIdade(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Now()
	old := now.Add(-8 * 24 * time.Hour) // além da retenção de 7 dias
	fresh := now.Add(-1 * time.Hour)
	batch := []SensorPendingSample{
		sensorPendingSample("s1", `{"runId":"velha"}`, old),
		sensorPendingSample("s1", `{"runId":"nova"}`, fresh),
	}
	if err := db.SavePendingSensorSamples("agent-1", batch); err != nil {
		t.Fatalf("save: %v", err)
	}

	removed, err := db.PrunePendingSensorSamples("agent-1", now.Add(-7*24*time.Hour), 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removidas = %d, want 1", removed)
	}
	rest, err := db.ListPendingSensorSamples("agent-1", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rest) != 1 || string(rest[0].Payload) != `{"runId":"nova"}` {
		t.Fatalf("restante = %+v, want apenas a nova", rest)
	}
}

// TestSensorPendingSamplesPruneByTamanho cobre o teto de disco: mantém as N mais
// recentes e remove as antigas.
func TestSensorPendingSamplesPruneByTamanho(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Now()
	const total = 25
	const keep = 10
	batch := make([]SensorPendingSample, 0, total)
	for i := 0; i < total; i++ {
		batch = append(batch, sensorPendingSample("s1", fmt.Sprintf(`{"runId":"r%d"}`, i), now.Add(time.Duration(i)*time.Second)))
	}
	if err := db.SavePendingSensorSamples("agent-1", batch); err != nil {
		t.Fatalf("save: %v", err)
	}

	removed, err := db.PrunePendingSensorSamples("agent-1", time.Time{}, keep)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != total-keep {
		t.Fatalf("removidas = %d, want %d", removed, total-keep)
	}
	rest, err := db.ListPendingSensorSamples("agent-1", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rest) != keep {
		t.Fatalf("restantes = %d, want %d", len(rest), keep)
	}
	// Sobreviveram as MAIS RECENTES: r15..r24.
	if string(rest[0].Payload) != `{"runId":"r15"}` {
		t.Fatalf("mais antiga restante = %s, want r15", rest[0].Payload)
	}
	if string(rest[len(rest)-1].Payload) != `{"runId":"r24"}` {
		t.Fatalf("mais recente = %s, want r24", rest[len(rest)-1].Payload)
	}
}

// TestSensorPendingSamplesPruneEscopoPorAgente garante que a poda não toca em
// outro agente (o mesmo banco pode hospedar mais de um).
func TestSensorPendingSamplesPruneEscopoPorAgente(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Now()
	for _, agent := range []string{"agent-1", "agent-2"} {
		if err := db.SavePendingSensorSamples(agent, []SensorPendingSample{sensorPendingSample("s1", `{"runId":"x"}`, now)}); err != nil {
			t.Fatalf("save %s: %v", agent, err)
		}
	}
	if _, err := db.PrunePendingSensorSamples("agent-1", time.Time{}, 100); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if total, err := db.CountPendingSensorSamples("agent-2"); err != nil || total != 1 {
		t.Fatalf("agent-2 = %d err=%v, want 1 (poda nao pode vazar entre agentes)", total, err)
	}
}

// TestSensorPendingSamplesNoOp: entradas vazias não são erro nem gravam nada.
func TestSensorPendingSamplesNoOp(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.SavePendingSensorSamples("agent-1", nil); err != nil {
		t.Fatalf("save vazio: %v", err)
	}
	if err := db.SavePendingSensorSamples("", []SensorPendingSample{sensorPendingSample("s1", `{}`, time.Now())}); err != nil {
		t.Fatalf("save sem agente: %v", err)
	}
	if total, err := db.CountPendingSensorSamples("agent-1"); err != nil || total != 0 {
		t.Fatalf("count = %d err=%v, want 0", total, err)
	}
}
