//go:build windows

package service

import (
	"context"
	"fmt"
	"runtime"
	"syscall"

	"golang.org/x/sys/windows"

	"discovery/internal/ctxutil"
)

// actionTokenKey é a chave de contexto para o token Windows de impersonação.
type actionTokenKey struct{}

// enrichContextWithToken anexa o token armazenado para actionID ao contexto.
// Deve ser chamado em processNextQueuedAction antes de executeQueuedAction.
// O token é consumido do registro global (pendingActionTokens) nesta chamada.
//
// O token é armazenado duas vezes:
//   - via actionTokenKey{}: permite que impersonateAndRun faça ImpersonateLoggedOnUser
//     (afeta apenas o thread da goroutine corrente).
//   - via ctxutil.WithProcessUserToken: permite que processutil.ApplyUserContext
//     propague o token para cmd.SysProcAttr.Token em processos filhos (CreateProcessAsUser),
//     garantindo que winget, PowerShell, Python e CMD executem como o usuário real.
//
// IMPORTANTE: ImpersonateLoggedOnUser NÃO propaga identidade para processos filhos.
// Processos filhos herdam o primary token do processo pai (SYSTEM). Por isso
// ctxutil.WithProcessUserToken + SysProcAttr.Token são necessários para spawn externo.
func enrichContextWithToken(ctx context.Context, actionID string) context.Context {
	tok, ok := consumeActionToken(actionID)
	if !ok || tok == 0 {
		return ctx
	}
	ctx = context.WithValue(ctx, actionTokenKey{}, tok)
	return ctxutil.WithProcessUserToken(ctx, syscall.Token(tok))
}

// closeActionToken fecha o handle de token armazenado no contexto, se houver.
// Deve ser chamado via defer após o retorno de executeQueuedAction.
func closeActionToken(ctx context.Context) {
	if v := ctx.Value(actionTokenKey{}); v != nil {
		if tok, ok := v.(windows.Token); ok && tok != 0 {
			tok.Close()
		}
	}
}

// impersonateAndRun executa fn no contexto de identidade do usuário descrito em ctx.
//
// Se o contexto contiver um token Windows (capturado via ImpersonateNamedPipeClient
// durante a conexão IPC), a execução ocorre com ImpersonateLoggedOnUser:
//   - runtime.LockOSThread garante que a impersonação afete a goroutine correta.
//   - RevertToSelf é sempre chamado antes de retornar.
//
// Se nenhum token estiver disponível, a ação executa como SYSTEM (contexto do service).
//
// NOTA: ImpersonateLoggedOnUser afeta apenas o token do thread corrente — processos
// filhos (ex.: winget, PowerShell) NÃO herdam essa identidade automaticamente.
// Para spawn de subprocessos com identidade do usuário, o token primário é propagado
// via ctxutil.WithProcessUserToken → cmd.SysProcAttr.Token (CreateProcessAsUser),
// o que é feito automaticamente por processutil.ApplyUserContext nos call sites.
func impersonateAndRun(ctx context.Context, fn func(ctx context.Context) error) error {
	userCtx, hasUser := actionUserContextFrom(ctx)

	tokVal := ctx.Value(actionTokenKey{})
	tok, hasToken := tokVal.(windows.Token)
	if !hasToken || tok == 0 {
		if hasUser && userCtx.UserSID != "" {
			fmt.Printf("[SERVICE.Impersonation] executando ação como SYSTEM (token não disponível para usuário %s / %s)\n",
				userCtx.UserName, userCtx.UserSID)
		}
		return fn(ctx)
	}

	// Token disponível: impersonar o usuário para a duração da ação.
	// LockOSThread garante que a impersonação de thread afete esta goroutine.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := callImpersonateLoggedOnUser(tok); err != nil {
		fmt.Printf("[SERVICE.Impersonation] ImpersonateLoggedOnUser falhou: %v — executando como SYSTEM\n", err)
		return fn(ctx)
	}
	defer windows.RevertToSelf() //nolint:errcheck

	if hasUser {
		fmt.Printf("[SERVICE.Impersonation] executando ação como usuário %s (%s)\n",
			userCtx.UserName, userCtx.UserSID)
	}
	return fn(ctx)
}
