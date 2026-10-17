//go:build windows

package remotesession

import (
	"context"

	"discovery/app/core/terminal"
)

// ── Provider PowerShell (pwsh / Windows PowerShell) ──
//
// Wrapper fino sobre o código existente: a consulta roda num PROCESSO AUXILIAR
// (pwsh/powershell -NoProfile -NonInteractive) com TabExpansion2 e o input vai
// por variável de ambiente base64. Comportamento idêntico ao anterior — inclusive
// o replacementIndex vindo do PowerShell.

// powerShellCompletionProvider adapta o TabExpansion2 ao contrato de provider.
type powerShellCompletionProvider struct{}

// newPowerShellCompletionProvider cria o provider de PowerShell.
func newPowerShellCompletionProvider() *powerShellCompletionProvider {
	return &powerShellCompletionProvider{}
}

// Supports atende PowerShell/pwsh no backend ConPTY.
func (p *powerShellCompletionProvider) Supports(shellKind terminal.ShellKind, backend string) bool {
	return isPowerShellShellKind(string(shellKind)) && backend == "conpty"
}

// Complete devolve apenas os itens (contrato completionProvider).
func (p *powerShellCompletionProvider) Complete(ctx context.Context, input string, cursor int) ([]completionMatch, error) {
	res, err := p.CompleteSpan(ctx, input, cursor)
	if err != nil {
		return nil, err
	}
	return res.Matches, nil
}

// CompleteSpan consulta o TabExpansion2 no processo auxiliar.
func (p *powerShellCompletionProvider) CompleteSpan(ctx context.Context, input string, cursor int) (tabExpansionResult, error) {
	return runTabExpansionQuery(ctx, input, cursor)
}
