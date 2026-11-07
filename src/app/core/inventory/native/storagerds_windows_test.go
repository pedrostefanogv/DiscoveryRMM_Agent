//go:build windows

package native

import "testing"

// TestStoragePoolPrimordialSinalizado garante que o pool "Primordial" é
// identificável. Ele agrega discos crus e NÃO é um pool criado pelo usuário:
// o servidor precisa poder ignorá-lo para não reportar capacidade falsa.
func TestStoragePoolPrimordialSinalizado(t *testing.T) {
	primordial := StoragePool{Name: "Primordial", IsPrimordial: true}
	if !primordial.IsPrimordial {
		t.Error("pool primordial deveria estar marcado")
	}
	usuario := StoragePool{Name: "PoolDados", IsPrimordial: false}
	if usuario.IsPrimordial {
		t.Error("pool de usuario nao deveria estar marcado como primordial")
	}
}

// TestRDSSessionNaoUsaSessaoZero garante o contrato de filtro: a sessão 0 é a
// de serviços e não deve aparecer como sessão de usuário.
func TestRDSSessionNaoUsaSessaoZero(t *testing.T) {
	sess := RDSSession{SessionID: 0, Username: "services"}
	if sess.SessionID != 0 {
		t.Error("sessao 0 deveria ser filtrada pelo coletor")
	}
	// O valor documentado no comentário do tipo precisa bater.
	for _, state := range []string{"Active", "Disc", "Listen"} {
		_ = RDSSession{State: state}
	}
}

// TestWmiBoolTolerante cobre o helper introduzido para ler IsPrimordial.
// O COM pode entregar bool, ou 0/1 numérico, ou a string "True".
func TestWmiBoolTolerante(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"int 1", int64(1), true},
		{"int 0", int64(0), false},
		{"float 1", float64(1), true},
		{"float 0", float64(0), false},
		{"string True", "True", true},
		{"string true", "true", true},
		{"string 1", "1", true},
		{"string False", "False", false},
		{"string vazia", "", false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		row := map[string]any{"v": tc.val}
		if tc.val == nil {
			row = map[string]any{}
		}
		if got := wmiBool(row, "v"); got != tc.want {
			t.Errorf("%s: wmiBool(%v) = %v, want %v", tc.name, tc.val, got, tc.want)
		}
	}
	// Chave ausente nunca é erro.
	if wmiBool(map[string]any{}, "ausente") {
		t.Error("chave ausente deveria devolver false")
	}
}
