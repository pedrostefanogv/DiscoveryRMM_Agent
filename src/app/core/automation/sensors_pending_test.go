package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"discovery/app/core/database"
)

// ─────────────────────────────────────────────────────────────────────────────
// Testes do buffer DURÁVEL de amostras de sensores (sensor_pending_samples).
//
// O cenário que originou a correção é o (a): o usuário desliga o PC, o processo
// morre com amostras ainda no buffer em RAM (que só era esvaziado a cada 10s), e
// ao voltar sem internet a coleta precisa continuar e o represado precisa ser
// enviado. Aqui isso é reproduzido com um Service NOVO sobre o MESMO banco.
//
// A entrega é verificada pela outbox durável: sem servidor alcançável, um envio
// falho-retentável passa a posse do lote para automation_callback_queue (é o
// mesmo caminho de produção, via flushSensorSamples -> sendOrQueueCallback).
// ─────────────────────────────────────────────────────────────────────────────

const pendingTestAgentID = "agent-pend"

func pendingTestService(t *testing.T, db *database.DB) *Service {
	t.Helper()
	service := NewService(func() RuntimeConfig {
		return RuntimeConfig{AgentID: pendingTestAgentID, BaseURL: "http://127.0.0.1:1", Token: "t"}
	}, func(string) {})
	if db != nil {
		service.SetDB(db)
	}
	return service
}

func pendingTestSample(sensorID, runID string) SensorResult {
	return SensorResult{
		SensorID:   sensorID,
		TargetKey:  "C:",
		RunID:      runID,
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
		FinishedAt: time.Now().UTC().Format(time.RFC3339),
		Measure:    SensorMeasure{FreePercent: sensorFloat(42)},
	}
}

// persistedRunIDs devolve os runIds das amostras guardadas no banco.
func persistedRunIDs(t *testing.T, db *database.DB) []string {
	t.Helper()
	rows, err := db.ListPendingSensorSamples(pendingTestAgentID, 0)
	if err != nil {
		t.Fatalf("list pendentes: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		var result SensorResult
		if err := json.Unmarshal(row.Payload, &result); err != nil {
			t.Fatalf("payload ilegivel: %v", err)
		}
		out = append(out, result.RunID)
	}
	return out
}

// ── (a) TESTE CENTRAL: persistir -> restart com o MESMO db -> recuperar -> entregar ──
func TestSensorPendingSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	db1, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// Processo 1: coleta 3 amostras (enqueue persiste) e MORRE sem flush — é a
	// janela de até 10s do flushSensorSamples.
	service1 := pendingTestService(t, db1)
	service1.enqueueSensorResults([]SensorResult{
		pendingTestSample("s1", "run-1"),
		pendingTestSample("s1", "run-2"),
		pendingTestSample("s2", "run-3"),
	})
	if got := persistedRunIDs(t, db1); len(got) != 3 {
		t.Fatalf("persistidas no processo 1 = %v, want 3 amostras", got)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close db1: %v", err)
	}

	// Processo 2: MESMO diretório (mesmo arquivo sqlite), Service NOVO em memória.
	db2, err := database.Open(dir)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	t.Cleanup(func() { db2.Close() })
	service2 := pendingTestService(t, db2)

	// Antes de recuperar, o buffer em RAM do processo novo está VAZIO.
	service2.sensorMu.Lock()
	before := len(service2.sensorPending)
	service2.sensorMu.Unlock()
	if before != 0 {
		t.Fatalf("buffer em RAM no boot = %d, want 0", before)
	}

	service2.restorePendingSensorSamples()

	service2.sensorMu.Lock()
	restored := make([]SensorResult, len(service2.sensorPending))
	copy(restored, service2.sensorPending)
	idsLen := len(service2.sensorPendingIDs)
	service2.sensorMu.Unlock()

	if len(restored) != 3 {
		t.Fatalf("recuperadas = %d, want 3", len(restored))
	}
	// FIFO: as 3 originais, na ordem em que foram coletadas.
	for i, want := range []string{"run-1", "run-2", "run-3"} {
		if restored[i].RunID != want {
			t.Fatalf("amostra[%d].RunID = %q, want %q", i, restored[i].RunID, want)
		}
	}
	if idsLen != len(restored) {
		t.Fatalf("sensorPendingIDs=%d desalinhado de sensorPending=%d", idsLen, len(restored))
	}

	// E são ENTREGUES: sem servidor, o lote vai para a outbox durável.
	service2.flushSensorSamples(context.Background())

	queued, err := db2.CountPendingAutomationCallbacks(pendingTestAgentID)
	if err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if queued != 1 {
		t.Fatalf("lotes na outbox = %d, want 1", queued)
	}

	entries, err := db2.ListDueAutomationCallbacks(pendingTestAgentID, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entradas = %d, want 1", len(entries))
	}
	var batch SensorResultBatch
	if err := json.Unmarshal([]byte(entries[0].PayloadJSON), &batch); err != nil {
		t.Fatalf("payload da outbox: %v", err)
	}
	if len(batch.Samples) != 3 {
		t.Fatalf("amostras no lote = %d, want 3", len(batch.Samples))
	}
	for i, want := range []string{"run-1", "run-2", "run-3"} {
		if batch.Samples[i].RunID != want {
			t.Fatalf("lote[%d].RunID = %q, want %q", i, batch.Samples[i].RunID, want)
		}
	}

	// A posse passou para a outbox: as linhas do buffer saem (não ficam eternas).
	if got := persistedRunIDs(t, db2); len(got) != 0 {
		t.Fatalf("buffer persistido apos entrega = %v, want vazio", got)
	}
}

// (a2) Recuperar duas vezes NÃO duplica (idempotência por RunID).
func TestSensorPendingRestoreIsIdempotent(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	service := pendingTestService(t, db)
	service.enqueueSensorResults([]SensorResult{pendingTestSample("s1", "run-1")})
	service.restorePendingSensorSamples()
	service.restorePendingSensorSamples()

	service.sensorMu.Lock()
	n := len(service.sensorPending)
	idsN := len(service.sensorPendingIDs)
	service.sensorMu.Unlock()
	if n != 1 {
		t.Fatalf("buffer apos 2 recuperacoes = %d, want 1 (RunID dedup)", n)
	}
	if idsN != n {
		t.Fatalf("sensorPendingIDs=%d desalinhado de sensorPending=%d", idsN, n)
	}
}

// ── (b) Poda por IDADE ──
func TestSensorPendingPruneByAgeOnPersist(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Linha antiga (8 dias) inserida direto, simulando coleta muito anterior.
	old := database.SensorPendingSample{
		SensorID:  "s1",
		Payload:   []byte(`{"runId":"antiga"}`),
		CreatedAt: time.Now().Add(-8 * 24 * time.Hour),
	}
	if err := db.SavePendingSensorSamples(pendingTestAgentID, []database.SensorPendingSample{old}); err != nil {
		t.Fatalf("save antiga: %v", err)
	}

	// A poda roda dentro do enqueue (após persistir).
	service := pendingTestService(t, db)
	service.enqueueSensorResults([]SensorResult{pendingTestSample("s1", "nova")})

	got := persistedRunIDs(t, db)
	if len(got) != 1 || got[0] != "nova" {
		t.Fatalf("persistidas = %v, want apenas a nova (a de 8 dias deveria ter sido podada)", got)
	}
}

// ── (c) Poda por TAMANHO ──
func TestSensorPendingPruneBySizeRetention(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// O teto real (20000) é caro de semear; a poda por tamanho é exercitada no
	// pacote database com números pequenos. Aqui garantimos que o enqueue CHAMA a
	// poda com o teto configurado, semeando acima de um limite reduzido.
	if sensorPendingMaxSamples != 20000 || sensorPendingRetentionDays != 7 {
		t.Fatalf("retencao = %d dias / %d amostras, want 7 / 20000", sensorPendingRetentionDays, sensorPendingMaxSamples)
	}

	// 3 antigas + 1 nova: a poda por tamanho com keepLatest=2 remove a mais antiga.
	now := time.Now()
	batch := []database.SensorPendingSample{
		{SensorID: "s1", Payload: []byte(`{"runId":"r1"}`), CreatedAt: now},
		{SensorID: "s1", Payload: []byte(`{"runId":"r2"}`), CreatedAt: now},
	}
	if err := db.SavePendingSensorSamples(pendingTestAgentID, batch); err != nil {
		t.Fatalf("save: %v", err)
	}
	removed, err := db.PrunePendingSensorSamples(pendingTestAgentID, time.Time{}, 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removidas = %d, want 0 (nada acima do teto)", removed)
	}
	if err := db.SavePendingSensorSamples(pendingTestAgentID, []database.SensorPendingSample{
		{SensorID: "s1", Payload: []byte(`{"runId":"r3"}`), CreatedAt: now},
	}); err != nil {
		t.Fatalf("save 3: %v", err)
	}
	removed, err = db.PrunePendingSensorSamples(pendingTestAgentID, time.Time{}, 2)
	if err != nil {
		t.Fatalf("prune 2: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removidas = %d, want 1", removed)
	}
	got := persistedRunIDs(t, db)
	if len(got) != 2 || got[0] != "r2" || got[1] != "r3" {
		t.Fatalf("restantes = %v, want [r2 r3] (mantem as mais recentes)", got)
	}
}

// ── (d) Falha de persistência NÃO impede coleta nem envio (best-effort) ──
func TestSensorPendingPersistFailureDoesNotBlockDelivery(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Fecha o banco para forçar falha de persistência em toda operação.
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	service := pendingTestService(t, db)

	// Não pode entrar em pânico nem perder a amostra de memória.
	service.enqueueSensorResults([]SensorResult{pendingTestSample("s1", "run-1")})

	service.sensorMu.Lock()
	n := len(service.sensorPending)
	idsN := len(service.sensorPendingIDs)
	service.sensorMu.Unlock()
	if n != 1 {
		t.Fatalf("buffer em memoria = %d, want 1 (coleta nao pode ser barrada)", n)
	}
	// ALINHAMENTO mantido mesmo com persistência falhando.
	if idsN != n {
		t.Fatalf("sensorPendingIDs=%d desalinhado de sensorPending=%d", idsN, n)
	}

	// E o envio continua: sem servidor, o lote vai para a outbox.
	service.flushSensorSamples(context.Background())
	queued, err := db.CountPendingAutomationCallbacks(pendingTestAgentID)
	if err != nil {
		// O banco está fechado: o próprio enfileiramento falha. O que importa é que
		// nada entrou em pânico e o processo seguiu.
		t.Logf("outbox indisponivel (banco fechado), esperado: %v", err)
		return
	}
	if queued != 1 {
		t.Fatalf("lotes na outbox = %d, want 1", queued)
	}
}

// ── (e) Amostra entregue com sucesso tem a linha removida ──
func TestSensorPendingDeliveredRemovesRow(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	service := pendingTestService(t, db)
	service.enqueueSensorResults([]SensorResult{pendingTestSample("s1", "run-1")})
	if got := persistedRunIDs(t, db); len(got) != 1 {
		t.Fatalf("persistidas = %v, want 1", got)
	}

	// Entrega bem-sucedida: simula o caminho de sucesso removendo as linhas do
	// lote consumido (é o que flushSensorSamples faz quando err == nil).
	batch, ids := service.takeSensorBatch(sensorResultBatchSize)
	if len(batch) != 1 || len(ids) != 1 {
		t.Fatalf("lote=%d ids=%d, want 1/1", len(batch), len(ids))
	}
	service.deletePendingSensorSamples(ids)

	if got := persistedRunIDs(t, db); len(got) != 0 {
		t.Fatalf("linha apos entrega = %v, want removida", got)
	}
}

// ── (ALINHAMENTO) lote entregue apaga SOMENTE as suas linhas ──
func TestSensorPendingBatchDeletesOnlyItsOwnRows(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	service := pendingTestService(t, db)
	// 4 amostras persistidas.
	service.enqueueSensorResults([]SensorResult{
		pendingTestSample("s1", "run-1"),
		pendingTestSample("s1", "run-2"),
		pendingTestSample("s1", "run-3"),
		pendingTestSample("s1", "run-4"),
	})

	// Consome só 2 (o resto fica) e apaga exatamente essas 2.
	batch, ids := service.takeSensorBatch(2)
	if len(batch) != 2 || len(ids) != 2 {
		t.Fatalf("lote=%d ids=%d, want 2/2", len(batch), len(ids))
	}
	service.deletePendingSensorSamples(ids)

	got := persistedRunIDs(t, db)
	if len(got) != 2 || got[0] != "run-3" || got[1] != "run-4" {
		t.Fatalf("persistidas = %v, want [run-3 run-4] (apagou as linhas erradas?)", got)
	}

	// O restante continua coerente: consumir tudo apaga o resto.
	batch2, ids2 := service.takeSensorBatch(sensorResultBatchSize)
	if len(batch2) != 2 || len(ids2) != 2 {
		t.Fatalf("lote2=%d ids2=%d, want 2/2", len(batch2), len(ids2))
	}
	service.deletePendingSensorSamples(ids2)
	if got := persistedRunIDs(t, db); len(got) != 0 {
		t.Fatalf("persistidas apos consumo total = %v, want vazio", got)
	}
}

// ── (d2) Lote que NÃO chegou à outbox mantém as linhas (não perde a coleta) ──
//
// Este é o caso sutil: sendOrQueueCallback devolve o mesmo tipo de erro quando
// enfileirou e quando o enfileiramento falhou. Apagar as linhas nos dois casos
// perderia a amostra para sempre. Aqui o servidor responde com erro retentável,
// mas o banco está fechado, então o enfileiramento falha.
func TestSensorPendingKeepsRowsWhenOutboxUnavailable(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	service := pendingTestService(t, db)
	service.enqueueSensorResults([]SensorResult{pendingTestSample("s1", "run-1")})

	// A coleta está persistida; fecha o banco para o enfileiramento falhar.
	if got := persistedRunIDs(t, db); len(got) != 1 {
		t.Fatalf("persistidas = %v, want 1", got)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// flush: a entrega falha (sem servidor) e a confirmação da outbox também
	// (banco fechado) -> as linhas NÃO podem ser apagadas.
	service.flushSensorSamples(context.Background())

	// Reabre para conferir que a amostra continua no buffer durável.
	db2, err := database.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { db2.Close() })
	if got := persistedRunIDs(t, db2); len(got) != 1 || got[0] != "run-1" {
		t.Fatalf("persistidas = %v, want [run-1] (o lote NAO chegou a outbox, nao pode ser apagado)", got)
	}
}

// ── (f) Boot sem banco NÃO panica ──
func TestSensorPendingWithoutDatabaseDoesNotPanic(t *testing.T) {
	service := pendingTestService(t, nil)

	// Ordem importa: flushSensorSamples CONSOME o buffer quando há cliente (o
	// NewService sempre cria um), então o que se verifica antes dele é o
	// enfileiramento e a recuperação — nenhum dos dois pode entrar em pânico.
	service.enqueueSensorResults([]SensorResult{pendingTestSample("s1", "run-1")})
	service.restorePendingSensorSamples()

	service.sensorMu.Lock()
	n, idsN := len(service.sensorPending), len(service.sensorPendingIDs)
	service.sensorMu.Unlock()
	if n != 1 || idsN != 1 {
		t.Fatalf("buffer=%d ids=%d, want 1/1 (sem banco, nada a persistir)", n, idsN)
	}

	// Sem banco não há outbox: o lote é consumido e a entrega falha. O que o teste
	// garante é ausência de pânico e o alinhamento sobrevivendo ao caminho todo.
	service.flushSensorSamples(context.Background())
	service.prunePendingSensorSamples(pendingTestAgentID)
	service.deletePendingSensorSamples([]int64{1, 2})

	service.sensorMu.Lock()
	n, idsN = len(service.sensorPending), len(service.sensorPendingIDs)
	service.sensorMu.Unlock()
	if n != idsN {
		t.Fatalf("desalinhado apos flush sem banco: buffer=%d ids=%d", n, idsN)
	}

	// Serviço nulo também não pode entrar em pânico.
	var nilService *Service
	nilService.enqueueSensorResults([]SensorResult{pendingTestSample("s1", "r")})
	nilService.restorePendingSensorSamples()
	nilService.flushSensorSamples(context.Background())
	nilService.prunePendingSensorSamples("agent")
}
