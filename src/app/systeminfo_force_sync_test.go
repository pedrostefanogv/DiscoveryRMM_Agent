package app

import "testing"

// O servidor manda as flags explícitas; o agent deve respeitar "tudo
// desmarcado" em vez de cair no default histórico (bug: as flags eram
// descartadas no backend e o agent sempre rodava policies+inventory).
func TestResolveForceSyncFlags_RespeitaValoresExplicitos(t *testing.T) {
	flags := resolveForceSyncFlags(map[string]any{
		"Policies":  true,
		"Inventory": false,
		"Software":  true,
		"AppStore":  false,
	})

	if !flags.Policies || flags.Inventory || !flags.Software || flags.AppStore {
		t.Fatalf("flags explicitas ignoradas: %+v", flags)
	}
}

func TestResolveForceSyncFlags_TudoDesmarcadoNaoAplicaDefault(t *testing.T) {
	flags := resolveForceSyncFlags(map[string]any{
		"Policies":  false,
		"Inventory": false,
		"Software":  false,
		"AppStore":  false,
	})

	if flags.Policies || flags.Inventory || flags.Software || flags.AppStore {
		t.Fatalf("tudo desmarcado deveria ser no-op, veio: %+v", flags)
	}
}

func TestResolveForceSyncFlags_PayloadLegadoUsaDefault(t *testing.T) {
	flags := resolveForceSyncFlags(map[string]any{
		"Operation": "force-sync",
	})

	if !flags.Policies || !flags.Inventory {
		t.Fatalf("default historico (policies+inventory) esperado, veio: %+v", flags)
	}
	if flags.Software || flags.AppStore {
		t.Fatalf("software/appStore nao fazem parte do default, veio: %+v", flags)
	}
}

func TestResolveForceSyncFlags_AceitaCamelCase(t *testing.T) {
	flags := resolveForceSyncFlags(map[string]any{
		"policies":  "true",
		"inventory": "false",
		"software":  float64(1),
		"appStore":  true,
	})

	if !flags.Policies || flags.Inventory || !flags.Software || !flags.AppStore {
		t.Fatalf("payload camelCase ignorado: %+v", flags)
	}
}
