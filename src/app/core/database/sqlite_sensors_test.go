package database

import "testing"

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
