//go:build !windows

package automation

import "context"

// userLoginEvent não é suportado fora do Windows (fallback: uma vez por processo).
type userLoginEvent struct {
	SessionID uint32
	UserName  string
}

// startUserLoginWatcher retorna canal nil fora do Windows — o caller mantém o fallback.
func startUserLoginWatcher(ctx context.Context) (<-chan userLoginEvent, func()) {
	return nil, func() {}
}
