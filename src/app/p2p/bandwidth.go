package p2p

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ── M15: limite de bandwidth P2P (token bucket global da máquina) ───────────
//
// O knob MaxBandwidthBytesPerSec (config do servidor via policy sync) existia
// mas nunca era consumido. Este limiter limita o TOTAL P2P da máquina
// (upload de artefatos servidos + download de chunks) para que a
// transferência não sobrecarregue a máquina, seja como sender ou receiver.
//
// Configuração (vinda do servidor):
//   - 0 (ausente)  → sem limite (default)
//   - > 0          → clamp para múltiplo de 10 MB/s no intervalo [10, 100] MB/s
//
// Implementação: token bucket com burst de 1 segundo. O throttle acontece na
// LEITURA dos readers do pipeline (sender serve bytes / receiver baixa chunk),
// em blocos — cada Read consome n tokens e dorme proporcionalmente quando o
// bucket esgota.

var (
	bwMu          sync.Mutex
	bwMaxPerSec   atomic.Int64
	bwTokensFloat float64
	bwLast        time.Time
)

// ConfigureP2PBandwidth aplica o limite global de bandwidth P2P.
// maxBytesPerSec <= 0 = sem limite. Chamado pelo NormalizeConfig quando o
// config (policy do servidor) chega — idempotente e barato.
func ConfigureP2PBandwidth(maxBytesPerSec int64) {
	bwMaxPerSec.Store(maxBytesPerSec)
}

// p2pBandwidthWait consome n bytes do bucket, aguardando se necessário.
func p2pBandwidthWait(ctx context.Context, n int) error {
	max := bwMaxPerSec.Load()
	if max <= 0 {
		return nil // sem limite configurado
	}
	if n <= 0 {
		return nil
	}

	bwMu.Lock()
	now := time.Now()
	if bwLast.IsZero() {
		bwLast = now
	}
	elapsed := now.Sub(bwLast).Seconds()
	bwLast = now
	bwTokensFloat += elapsed * float64(max)
	if bwTokensFloat > float64(max) {
		bwTokensFloat = float64(max) // burst máximo de 1 segundo
	}
	bwTokensFloat -= float64(n)
	waitSec := 0.0
	if bwTokensFloat < 0 {
		waitSec = -bwTokensFloat / float64(max)
	}
	bwMu.Unlock()

	if waitSec <= 0 {
		return nil
	}

	timer := time.NewTimer(time.Duration(waitSec * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// bandwidthThrottleReader envolve um io.Reader e consome tokens de bandwidth
// por bloco lido (M15). Usado no sender (bytes servidos) e no receiver
// (chunks baixados).
type bandwidthThrottleReader struct {
	r   io.Reader
	ctx context.Context
}

func newBandwidthThrottleReader(r io.Reader, ctx context.Context) *bandwidthThrottleReader {
	return &bandwidthThrottleReader{r: r, ctx: ctx}
}

func (b *bandwidthThrottleReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		if werr := p2pBandwidthWait(b.ctx, n); werr != nil && err == nil {
			// ctx cancelado durante o throttle: sinaliza para o caller abortar.
			return n, werr
		}
	}
	return n, err
}
