//go:build windows

package app

// Testes do fallback de UNINSTALL na sessão do usuário.
//
// Contexto: a loja passou a executar no SERVIÇO (SYSTEM). Um pacote
// USER-scope instalado no perfil do usuário não é visível para o winget do
// SYSTEM; sem o fallback a remoção falharia (regressão em relação ao modelo
// antigo, em que a UI removia como o próprio usuário).

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"

	"discovery/app/core/ctxutil"
	"discovery/app/core/services"
)

// fakeWinget registra em que contexto cada chamada rodou (token de usuário
// presente no ctx?) e devolve os erros programados na ordem.
type fakeWinget struct {
	calls []bool
	errs  []error
}

func (f *fakeWinget) record(ctx context.Context) error {
	_, hasUser := ctxutil.ProcessUserToken(ctx)
	i := len(f.calls)
	f.calls = append(f.calls, hasUser)
	if i < len(f.errs) {
		return f.errs[i]
	}
	return nil
}

func (f *fakeWinget) Install(ctx context.Context, id string) (string, error) {
	return "", f.record(ctx)
}
func (f *fakeWinget) Uninstall(ctx context.Context, id string) (string, error) {
	if err := f.record(ctx); err != nil {
		return "", err
	}
	return "removido", nil
}
func (f *fakeWinget) Upgrade(ctx context.Context, id string) (string, error) {
	return "", f.record(ctx)
}
func (f *fakeWinget) UpgradeAll(ctx context.Context) (string, error)     { return "", f.record(ctx) }
func (f *fakeWinget) ListInstalled(ctx context.Context) (string, error)  { return "", f.record(ctx) }
func (f *fakeWinget) ListUpgradable(ctx context.Context) (string, error) { return "", f.record(ctx) }
func (f *fakeWinget) Download(ctx context.Context, id, dir string) (string, error) {
	return "", f.record(ctx)
}

func newUninstallTestRouter(f *fakeWinget) *automationPackageManagerRouter {
	return &automationPackageManagerRouter{
		fallback: services.NewAppsService(f, nil),
		logf:     func(string, ...any) {},
	}
}

func TestRouterUninstallFallsBackToUserSession(t *testing.T) {
	orig := activeUserTokenFn
	defer func() { activeUserTokenFn = orig }()
	activeUserTokenFn = func(ctx context.Context) (context.Context, func(), bool) {
		return ctxutil.WithProcessUserToken(ctx, syscall.Token(1)), func() {}, true
	}

	f := &fakeWinget{errs: []error{errors.New("não encontrado como SYSTEM")}}
	out, err := newUninstallTestRouter(f).Uninstall(context.Background(), "brave.brave")
	if err != nil {
		t.Fatalf("fallback deveria remover como usuário: %v", err)
	}
	if out != "removido" {
		t.Fatalf("output=%q", out)
	}
	if len(f.calls) != 2 {
		t.Fatalf("esperado 2 tentativas (SYSTEM + usuário); houve %d", len(f.calls))
	}
	if f.calls[0] {
		t.Fatal("a 1a tentativa deve ser como SYSTEM (sem token de usuário)")
	}
	if !f.calls[1] {
		t.Fatal("a 2a tentativa deve rodar na identidade do usuário logado")
	}
}

func TestRouterUninstallReturnsOriginalErrorWithoutSession(t *testing.T) {
	orig := activeUserTokenFn
	defer func() { activeUserTokenFn = orig }()
	activeUserTokenFn = func(ctx context.Context) (context.Context, func(), bool) {
		return ctx, func() {}, false
	}

	first := errors.New("falha como SYSTEM")
	f := &fakeWinget{errs: []error{first}}
	_, err := newUninstallTestRouter(f).Uninstall(context.Background(), "videolan.vlc")
	if !errors.Is(err, first) {
		t.Fatalf("erro original deve ser devolvido: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("sem sessão ativa não deve haver 2a tentativa; houve %d", len(f.calls))
	}
}

func TestRouterUninstallReportsBothFailures(t *testing.T) {
	orig := activeUserTokenFn
	defer func() { activeUserTokenFn = orig }()
	activeUserTokenFn = func(ctx context.Context) (context.Context, func(), bool) {
		return ctxutil.WithProcessUserToken(ctx, syscall.Token(1)), func() {}, true
	}

	f := &fakeWinget{errs: []error{errors.New("falha SYSTEM"), errors.New("falha usuário")}}
	_, err := newUninstallTestRouter(f).Uninstall(context.Background(), "7zip.7zip")
	if err == nil {
		t.Fatal("esperava erro")
	}
	if !strings.Contains(err.Error(), "uninstall na sessao do usuario tambem falhou") {
		t.Fatalf("erro deveria citar as duas tentativas: %v", err)
	}
}
