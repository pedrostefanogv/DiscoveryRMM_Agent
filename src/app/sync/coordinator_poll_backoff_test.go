package sync

import (
	"errors"
	"testing"
	"time"
)

// Regressão: o poll do sync-manifest não tinha backoff em falha recorrente
// (ex.: HTTP 502 do nginx), martelando o servidor no intervalo fixo.
func TestNextPollIntervalBacksOffAndResets(t *testing.T) {
	c := New(&fakeDeps{})
	c.setPollEvery(60 * time.Second)

	if got := c.nextPollInterval(); got != 60*time.Second {
		t.Fatalf("sem falhas: %s, esperado 60s", got)
	}
	c.recordManifestResult(errors.New("sync-manifest retornou HTTP 502"))
	if got := c.nextPollInterval(); got != 120*time.Second {
		t.Fatalf("1 falha: %s, esperado 120s", got)
	}
	c.recordManifestResult(errors.New("sync-manifest retornou HTTP 502"))
	if got := c.nextPollInterval(); got != 240*time.Second {
		t.Fatalf("2 falhas: %s, esperado 240s", got)
	}
	for i := 0; i < 20; i++ {
		c.recordManifestResult(errors.New("sync-manifest retornou HTTP 502"))
	}
	if got := c.nextPollInterval(); got != manifestPollBackoffMax {
		t.Fatalf("backoff saturado: %s, esperado %s", got, manifestPollBackoffMax)
	}
	c.recordManifestResult(nil)
	if got := c.nextPollInterval(); got != 60*time.Second {
		t.Fatalf("após sucesso: %s, esperado 60s", got)
	}
}
