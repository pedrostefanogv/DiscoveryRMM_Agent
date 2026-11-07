//go:build windows

package native

import (
	"context"
	"testing"
	"time"
)

// TestHyperVStateName cobre o mapeamento código->nome. O ponto crítico é
// que um código DESCONHECIDO não pode virar string vazia nem um nome
// inventado: devolvemos "Unknown:<n>" para o operador ver o valor real.
func TestHyperVStateName(t *testing.T) {
	cases := []struct {
		name string
		code int
		want string
	}{
		{"ligada", 2, "Running"},
		{"desligada", 3, "Off"},
		{"salva", 6, "Saved"},
		{"pausada", 9, "Paused"},
		{"desconhecido zero", 0, "Unknown"},
		{"codigo nao mapeado", 12345, "Unknown:12345"},
		{"codigo negativo", -1, "Unknown:-1"},
	}
	for _, tc := range cases {
		if got := hyperVStateName(tc.code); got != tc.want {
			t.Errorf("%s: hyperVStateName(%d) = %q, want %q", tc.name, tc.code, got, tc.want)
		}
	}
}

// TestCollectHyperVInventarioNaoPanica garante o contrato de robustez: o
// coletor pode devolver qualquer coisa, mas NUNCA entra em panic e nunca
// propaga erro — ele roda no ciclo de inventário do agente.
func TestCollectHyperVInventarioNaoPanica(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	inv := CollectHyperVInventario(ctx)

	// Em máquina sem Hyper-V (o caso desta suíte) o esperado é Enabled=false
	// com motivo. Se o runner TIVER Hyper-V, aceitamos Enabled=true.
	if !inv.Enabled && inv.Reason == "" {
		t.Error("sem Hyper-V o coletor deve registrar um motivo")
	}
	if inv.Enabled && inv.Reason == "" && len(inv.VMs) == 0 {
		t.Log("host com Hyper-V sem VMs: aceitavel, mas o motivo ficou vazio")
	}
}

// TestHyperVAvailableNaoPanica cobre a detecção de role: qualquer resultado é
// aceitável (depende da máquina), o que não é aceitável é panicar.
func TestHyperVAvailableNaoPanica(t *testing.T) {
	_ = hyperVAvailable()
}
