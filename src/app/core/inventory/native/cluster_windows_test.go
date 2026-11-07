//go:build windows

package native

import (
	"context"
	"testing"
	"time"
)

// TestClusterIsDegradedState cobre a classificação de recurso de cluster.
// "Online" é a única condição saudável; qualquer outra (Offline, Failed,
// OnlinePending) é sinal de atenção. Estado vazio NÃO é degradado: não
// inferimos problema a partir de dado ausente.
func TestClusterIsDegradedState(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{"Online", false},
		{"online", false},
		{"ONLINE", false},
		{"", false},
		{"   ", false},
		{"Offline", true},
		{"Failed", true},
		{"OnlinePending", true},
		{"OfflinePending", true},
	}
	for _, tc := range cases {
		if got := clusterIsDegradedState(tc.state); got != tc.want {
			t.Errorf("clusterIsDegradedState(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// TestCollectClusterInventarioNaoPanica garante o contrato: sem a role,
// devolve Present=false com motivo — e nunca panic.
func TestCollectClusterInventarioNaoPanica(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inv := CollectClusterInventario(ctx)
	if !inv.Present && inv.Reason == "" {
		t.Error("sem a role, o coletor deve registrar um motivo")
	}
}

// TestServiceExistsNaoPanica cobre a detecção de role pelo SCM. Qualquer
// resultado depende da máquina; o que não pode acontecer é panic.
func TestServiceExistsNaoPanica(t *testing.T) {
	for _, name := range []string{"", "vmms", "ClusSvc", "servico-que-nao-existe-xyz-123"} {
		_ = serviceExists(name)
	}
	// Um serviço inexistente NUNCA pode ser reportado como existente.
	if serviceExists("servico-que-nao-existe-xyz-123") {
		t.Error("servico inexistente reportado como existente")
	}
	if serviceExists("") {
		t.Error("nome vazio reportado como existente")
	}
}

// TestCollectStorageRDSInventarioNaoPanica garante o contrato do F14:
// as duas coletas são independentes e nenhuma derruba a outra.
func TestCollectStorageRDSInventarioNaoPanica(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inv := CollectStorageRDSInventario(ctx)
	if len(inv.StoragePools) == 0 && !inv.RDSHost && inv.Reason == "" {
		t.Error("resultado vazio deve registrar um motivo")
	}
}

// TestStorageRDSTiposExpostos exercita os campos dos tipos do F14 para que uma
// renomeação acidental quebre o teste em vez de quebrar o contrato JSON com o
// servidor silenciosamente.
func TestStorageRDSTiposExpostos(t *testing.T) {
	pool := StoragePool{Name: "Pool1", HealthStatus: "Healthy", DiskCount: 4}
	if pool.Name != "Pool1" || pool.DiskCount != 4 {
		t.Error("campos de StoragePool alterados")
	}
	sess := RDSSession{SessionID: 3, Username: "user", State: "Active"}
	if sess.SessionID != 3 || sess.Username != "user" {
		t.Error("campos de RDSSession alterados")
	}
}
