//go:build windows

package remotesession

import (
	"context"
	"strings"

	"discovery/app/core/terminal"
)

// ── Provider POSIX (bash, zsh, wsl e wsl:<distro>) ──
//
// Primeiro token: builtins + executáveis do PATH. Demais tokens: arquivos e
// diretórios do diretório corrente ou de prefixos com caminho. Nomes ocultos só
// aparecem quando o prefixo começa com ".". Sugestões recebem escape coerente
// com as aspas já abertas e barra final para diretórios.

// completionPosixBuiltins são os builtins comuns a bash/zsh.
var completionPosixBuiltins = []struct{ Nome, Descricao string }{
	{".", "Executa um script no shell atual"},
	{":", "Comando nulo"},
	{"[", "Avalia uma expressão (alias de test)"},
	{"alias", "Define/exibe aliases"},
	{"bg", "Retoma job em background"},
	{"break", "Sai de um laço"},
	{"builtin", "Executa um builtin"},
	{"cd", "Muda o diretório corrente"},
	{"command", "Executa um comando ignorando funções/aliases"},
	{"continue", "Continua o laço"},
	{"declare", "Declara variáveis (bash)"},
	{"dirs", "Lista a pilha de diretórios"},
	{"echo", "Exibe texto"},
	{"eval", "Avalia argumentos como comando"},
	{"exec", "Substitui o shell pelo comando"},
	{"exit", "Encerra o shell"},
	{"export", "Exporta variáveis de ambiente"},
	{"false", "Retorna falha"},
	{"fg", "Traz job para o primeiro plano"},
	{"getopts", "Processa opções"},
	{"hash", "Gerencia o cache de caminhos"},
	{"jobs", "Lista jobs"},
	{"local", "Variável local (função)"},
	{"popd", "Desempilha diretório"},
	{"printf", "Formata/imprime dados"},
	{"pushd", "Empilha diretório"},
	{"pwd", "Exibe o diretório corrente"},
	{"read", "Lê uma linha da entrada"},
	{"return", "Retorna de função/script"},
	{"set", "Define opções/parâmetros"},
	{"shift", "Desloca parâmetros posicionais"},
	{"source", "Executa um script no shell atual"},
	{"test", "Avalia uma expressão"},
	{"trap", "Define handlers de sinais"},
	{"true", "Retorna sucesso"},
	{"type", "Descreve um comando"},
	{"ulimit", "Limita recursos"},
	{"umask", "Define a máscara de permissões"},
	{"unalias", "Remove aliases"},
	{"unset", "Remove variáveis"},
	{"wait", "Aguarda job"},
}

// posixCompletionProvider completa bash/zsh/WSL sem executar nada.
type posixCompletionProvider struct {
	fs   completionFS
	path *completionPathIndex
}

// newPOSIXCompletionProvider cria o provider sobre um FS injetável.
func newPOSIXCompletionProvider(fs completionFS) *posixCompletionProvider {
	return &posixCompletionProvider{fs: fs, path: newCompletionPathIndex()}
}

// Supports atende bash, zsh, wsl e wsl:<distro> no backend ConPTY.
func (p *posixCompletionProvider) Supports(shellKind terminal.ShellKind, backend string) bool {
	if backend != "conpty" {
		return false
	}
	k := strings.ToLower(strings.TrimSpace(string(shellKind)))
	return k == "bash" || k == "zsh" || strings.HasPrefix(k, "wsl")
}

// Complete devolve apenas os itens (contrato completionProvider).
func (p *posixCompletionProvider) Complete(ctx context.Context, input string, cursor int) ([]completionMatch, error) {
	res, err := p.CompleteSpan(ctx, input, cursor)
	if err != nil {
		return nil, err
	}
	return res.Matches, nil
}

// CompleteSpan devolve itens + trecho a substituir.
func (p *posixCompletionProvider) CompleteSpan(ctx context.Context, input string, cursor int) (tabExpansionResult, error) {
	cursor = completionClampCursor(input, cursor)
	tok, prefixo := completionTokenAt(input, cursor, completionQuotePOSIX)

	matches := make([]completionMatch, 0, 64)
	if tok.Indice == 0 && !completionTemCaminho(prefixo, completionQuotePOSIX) {
		for _, b := range completionPosixBuiltins {
			if !strings.HasPrefix(b.Nome, prefixo) {
				continue
			}
			matches = append(matches, completionMatch{
				Text:     b.Nome,
				ListItem: b.Nome,
				Type:     "command",
				Tooltip:  b.Descricao,
			})
		}
		// Executáveis do PATH (cache com TTL — não varre o disco a cada TAB).
		nomes := p.path.get(ctx, p.fs.PathDirs(), func(ctx context.Context, dirs []string) []string {
			return scanPathExecutables(ctx, dirs, p.fs.ListDir, nil, p.path.dirTimeout)
		})
		for _, nome := range nomes {
			if !strings.HasPrefix(nome, prefixo) {
				continue
			}
			matches = append(matches, completionMatch{
				Text:     nome,
				ListItem: nome,
				Type:     "command",
				Tooltip:  completionDescribeExecutavel(nome, ""),
			})
		}
	}

	// Arquivos/diretórios. O prefixo "~" é expandido para o home e a sugestão
	// mantém o "~/" digitado (ex.: "cd ~/Doc" -> "~/Documents/").
	if paths, err := completeDirectoryEntries(p.fs, prefixo, completionQuotePOSIX, tok.Aspas); err == nil {
		matches = append(matches, paths...)
	}

	matches = dedupeCompletionMatches(matches)
	if len(matches) > completionMaxItems {
		matches = matches[:completionMaxItems]
	}
	return tabExpansionResult{
		ReplacementIndex:  tok.Inicio,
		ReplacementLength: cursor - tok.Inicio,
		Matches:           matches,
	}, nil
}
