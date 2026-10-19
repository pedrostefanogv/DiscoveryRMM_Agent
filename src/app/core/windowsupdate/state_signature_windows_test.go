//go:build windows

package windowsupdate

import (
	"testing"
	"time"
)

// O spool do log de eventos pode rotacionar entre duas sondas: a assinatura NAO
// pode mudar so porque linhas antigas deixaram de ser retornadas. Sem isso a
// sonda de 5 min dispararia um scan WUA completo repetidas vezes.
func TestEventActivitySignatureStableAcrossRotation(t *testing.T) {
	full := WindowsUpdateState{}
	full.CheckedAt = timeZero()
	full.LastInstallTime = timeRef()
	full.LastInstallSuccess = nil
	full.DownloadInProgress = true

	rotated := full
	rotated.LastInstallTime = timeRef()
	rotated.DownloadInProgress = true

	if WindowsUpdateStateSignature(full) != WindowsUpdateStateSignature(rotated) {
		t.Fatal("rotacao do spool de eventos nao pode alterar a assinatura")
	}
}

func timeZero() time.Time { return time.Unix(0, 0).UTC() }

func timeRef() *time.Time {
	value := time.Date(2026, 10, 16, 21, 9, 57, 0, time.UTC)
	return &value
}
