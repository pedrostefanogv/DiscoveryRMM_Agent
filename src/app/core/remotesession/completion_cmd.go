//go:build windows

package remotesession

import (
	"context"
	"path/filepath"
	"strings"

	"discovery/app/core/terminal"
)

// ── Provider CMD (cmd.exe) ──
//
// Completa comandos internos do cmd + executáveis do PATH (no primeiro token) e
// arquivos/diretórios do diretório corrente ou de um prefixo com caminho.
// Nunca sugere opções (/algo): só nomes.

// completionCmdBuiltins são os comandos internos do cmd.exe.
var completionCmdBuiltins = []struct{ Nome, Descricao string }{
	{"assoc", "Exibe/associa extensões de arquivo"},
	{"attrib", "Exibe/altera atributos de arquivo"},
	{"break", "Liga/desliga extensão de Ctrl+C"},
	{"call", "Chama um script em lote"},
	{"cd", "Muda o diretório corrente"},
	{"chcp", "Exibe/altera a página de código"},
	{"chdir", "Muda o diretório corrente"},
	{"chkdsk", "Verifica o disco"},
	{"cls", "Limpa a tela"},
	{"color", "Define as cores do console"},
	{"copy", "Copia arquivos"},
	{"date", "Exibe/define a data"},
	{"del", "Apaga arquivos"},
	{"dir", "Lista o conteúdo do diretório"},
	{"diskpart", "Gerencia discos e partições"},
	{"echo", "Exibe mensagens"},
	{"endlocal", "Encerra o escopo local"},
	{"erase", "Apaga arquivos"},
	{"exit", "Encerra o cmd"},
	{"fc", "Compara arquivos"},
	{"find", "Procura texto em arquivos"},
	{"findstr", "Procura strings em arquivos"},
	{"for", "Executa um comando para cada item"},
	{"format", "Formata um disco"},
	{"ftype", "Exibe/associa tipos de arquivo"},
	{"goto", "Salta para um rótulo"},
	{"help", "Ajuda de comandos"},
	{"if", "Execução condicional"},
	{"label", "Cria/altera rótulo de volume"},
	{"md", "Cria diretório"},
	{"mkdir", "Cria diretório"},
	{"mode", "Configura dispositivos do console"},
	{"more", "Pagina a saída"},
	{"move", "Move arquivos"},
	{"path", "Exibe/define o PATH"},
	{"pause", "Pausa o processamento"},
	{"popd", "Restaura o diretório empilhado"},
	{"prompt", "Altera o prompt"},
	{"pushd", "Empilha o diretório e muda"},
	{"rd", "Remove diretório"},
	{"rem", "Comentário"},
	{"ren", "Renomeia arquivos"},
	{"rename", "Renomeia arquivos"},
	{"rmdir", "Remove diretório"},
	{"robocopy", "Cópia robusta de arquivos"},
	{"sc", "Gerencia serviços"},
	{"schtasks", "Gerencia tarefas agendadas"},
	{"set", "Exibe/define variáveis de ambiente"},
	{"setlocal", "Inicia o escopo local"},
	{"shift", "Desloca parâmetros"},
	{"shutdown", "Desliga/reinicia a máquina"},
	{"sort", "Ordena a entrada"},
	{"start", "Inicia programa/janela"},
	{"taskkill", "Encerra processos"},
	{"tasklist", "Lista processos"},
	{"time", "Exibe/define a hora"},
	{"title", "Define o título da janela"},
	{"tree", "Mostra a árvore de diretórios"},
	{"type", "Exibe o conteúdo de arquivos"},
	{"ver", "Exibe a versão do Windows"},
	{"verify", "Liga/desliga verificação de escrita"},
	{"vol", "Exibe o rótulo do volume"},
	{"where", "Localiza arquivos no PATH"},
	{"whoami", "Exibe o usuário atual"},
	{"xcopy", "Copia arquivos e árvores"},
}

// cmdCompletionProvider completa cmd.exe sem executar nada.
type cmdCompletionProvider struct {
	fs   completionFS
	path *completionPathIndex
}

// newCmdCompletionProvider cria o provider sobre um FS injetável.
func newCmdCompletionProvider(fs completionFS) *cmdCompletionProvider {
	return &cmdCompletionProvider{fs: fs, path: newCompletionPathIndex()}
}

// Supports atende cmd.exe no backend ConPTY.
func (p *cmdCompletionProvider) Supports(shellKind terminal.ShellKind, backend string) bool {
	return strings.EqualFold(strings.TrimSpace(string(shellKind)), string(terminal.ShellCmd)) && backend == "conpty"
}

// Complete devolve apenas os itens (contrato completionProvider).
func (p *cmdCompletionProvider) Complete(ctx context.Context, input string, cursor int) ([]completionMatch, error) {
	res, err := p.CompleteSpan(ctx, input, cursor)
	if err != nil {
		return nil, err
	}
	return res.Matches, nil
}

// CompleteSpan devolve itens + trecho a substituir.
func (p *cmdCompletionProvider) CompleteSpan(ctx context.Context, input string, cursor int) (tabExpansionResult, error) {
	cursor = completionClampCursor(input, cursor)
	tok, prefixo := completionTokenAt(input, cursor, completionQuoteCMD)

	matches := make([]completionMatch, 0, 64)
	if tok.Indice == 0 && !completionTemCaminho(prefixo, completionQuoteCMD) {
		for _, b := range completionCmdBuiltins {
			if !strings.HasPrefix(strings.ToLower(b.Nome), strings.ToLower(prefixo)) {
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
			return scanPathExecutables(ctx, dirs, p.fs.ListDir, cmdExecutavel, p.path.dirTimeout)
		})
		for _, nome := range nomes {
			comando := cmdNomeComando(nome)
			if !strings.HasPrefix(strings.ToLower(comando), strings.ToLower(prefixo)) {
				continue
			}
			matches = append(matches, completionMatch{
				Text:     comando,
				ListItem: nome,
				Type:     "command",
				Tooltip:  completionDescribeExecutavel(nome, ""),
			})
		}
	}

	// Opções (/algo, -algo) NÃO são sugeridas: nada de caminho nem comando.
	if strings.HasPrefix(prefixo, "/") || strings.HasPrefix(prefixo, "-") {
		return tabExpansionResult{
			ReplacementIndex:  tok.Inicio,
			ReplacementLength: cursor - tok.Inicio,
			Matches:           []completionMatch{},
		}, nil
	}

	// Arquivos/diretórios (inclusive com prefixo de caminho).
	if paths, err := completeDirectoryEntries(p.fs, prefixo, completionQuoteCMD, tok.Aspas); err == nil {
		matches = append(matches, paths...)
	}
	// Diretório inacessível/digitado pela metade não é erro: devolve o que já
	// havia (comandos), mantendo a sessão utilizável.

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

// cmdExecutavel reconhece extensões executáveis do Windows.
func cmdExecutavel(nome string) bool {
	switch strings.ToLower(filepath.Ext(nome)) {
	case ".exe", ".com", ".cmd", ".bat":
		return true
	}
	return false
}

// cmdNomeComando remove a extensão executável do nome sugerido.
func cmdNomeComando(nome string) string {
	if cmdExecutavel(nome) {
		return strings.TrimSuffix(nome, filepath.Ext(nome))
	}
	return nome
}
