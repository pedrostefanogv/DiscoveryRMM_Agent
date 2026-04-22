//go:build !windows

package service

import "context"

// enrichContextWithToken é no-op em plataformas não-Windows.
func enrichContextWithToken(ctx context.Context, actionID string) context.Context {
	return ctx
}

// closeActionToken é no-op em plataformas não-Windows.
func closeActionToken(ctx context.Context) {}

// impersonateAndRun em plataformas não-Windows apenas executa fn diretamente,
// pois impersonation via Named Pipe é específico do Windows.
func impersonateAndRun(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
