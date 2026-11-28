package app

import (
	"testing"
	"time"
)

// O retry do enrollment não pode ficar preso no ciclo de 6h: sem certificado
// mTLS o agente perde toda a API HTTP.
func TestNextMtlsRetry_BackoffCappedAtRenewInterval(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{mtlsRetryMinInterval, 4 * time.Minute},
		{4 * time.Minute, 16 * time.Minute},
		{16 * time.Minute, 64 * time.Minute},
		{mtlsRenewCheckInterval, mtlsRenewCheckInterval},
		{mtlsRenewCheckInterval - time.Minute, mtlsRenewCheckInterval},
	}
	for _, c := range cases {
		if got := nextMtlsRetry(c.in); got != c.want {
			t.Fatalf("nextMtlsRetry(%s) = %s, esperado %s", c.in, got, c.want)
		}
	}
	if mtlsRetryMinInterval <= 0 || mtlsRetryMinInterval >= mtlsRenewCheckInterval {
		t.Fatalf("mtlsRetryMinInterval fora de faixa: %s", mtlsRetryMinInterval)
	}
}
