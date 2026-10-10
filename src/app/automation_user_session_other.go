//go:build !windows

package app

import "context"

var activeUserTokenFn = withActiveUserToken

// withActiveUserToken: fora do Windows não há sessão de usuário a assumir.
func withActiveUserToken(ctx context.Context) (context.Context, func(), bool) {
	return ctx, func() {}, false
}
