//go:build !windows

package winget

import (
	"context"
	"strings"
)

// resolveUserContextExecutable: fora do Windows não há token de sessão.
func resolveUserContextExecutable(_ context.Context) (string, error) { return "", nil }

// envValue existe nos dois alvos (usado pelo arquivo Windows e pelos testes).
func envValue(env []string, key string) string {
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok && strings.EqualFold(strings.TrimSpace(name), key) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
