//go:build windows

package app

import (
	"context"
	"syscall"

	"golang.org/x/sys/windows"

	"discovery/app/core/ctxutil"
)

// activeUserTokenFn é injetável em teste (default: sessão interativa real).
var activeUserTokenFn = withActiveUserToken

// withActiveUserToken devolve um ctx cujos processos filhos rodam na IDENTIDADE
// do usuário da sessão interativa (Go usa CreateProcessAsUser quando
// SysProcAttr.Token está preenchido) e um cleanup para fechar o handle do
// token depois que o processo terminar.
//
// Devolve ok=false quando não há sessão de console ativa nem usuário logado —
// nesse caso o caller mantém o comportamento SYSTEM (nenhuma regressão).
//
// Usa o MESMO WTSQueryUserToken já empregado pelo worker de sessão remota
// (remote_session_worker_spawn.go); exige que o processo seja SYSTEM (o
// serviço é), o que é justamente o caso da automação.
func withActiveUserToken(ctx context.Context) (context.Context, func(), bool) {
	sessionID := windows.WTSGetActiveConsoleSessionId()
	if sessionID == 0xFFFFFFFF {
		return ctx, func() {}, false
	}
	tok, err := wtsQueryUserToken(sessionID)
	if err != nil || tok == 0 {
		return ctx, func() {}, false
	}
	userCtx := ctxutil.WithProcessUserToken(ctx, syscall.Token(tok))
	cleanup := func() { _ = tok.Close() }
	return userCtx, cleanup, true
}
