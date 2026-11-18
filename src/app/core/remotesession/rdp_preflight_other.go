//go:build !windows

package remotesession

import (
	"context"
	"time"
)

// CheckRDPPreflight retorna status de plataforma nao suportada fora do Windows.
func CheckRDPPreflight(_ context.Context) RDPStatus {
	return RDPStatus{
		Ready:            false,
		TermServiceState: "unsupported",
		RDPEnabled:       false,
		Port:             3389,
		Listening:        false,
		Issues:           []string{IssueRDPUnsupportedOS},
		Hints:            []string{"RDP host preflight suportado apenas em Windows."},
		CheckedAtUtc:     time.Now().UTC().Format(time.RFC3339),
	}
}
