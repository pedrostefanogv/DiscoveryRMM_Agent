//go:build !windows

package notifyui

import "context"

// ShowCard não é suportado fora do Windows: devolve erro para o chamador
// aplicar o fallback (mesma política do PSADT em não-Windows).
func ShowCard(ctx context.Context, req CardRequest) (CardResult, bool) {
	return CardResult{Outcome: OutcomeError, Reason: "cartão nativo suportado apenas no Windows"}, false
}
