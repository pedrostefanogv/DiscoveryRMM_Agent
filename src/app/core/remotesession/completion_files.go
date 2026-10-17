//go:build windows

package remotesession

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ── Completação de arquivos/diretórios (cmd e POSIX) ──
//
// O caminho digitado é dividido em diretório (como o usuário digitou) + base.
// As sugestões preservam o prefixo digitado ("C:\Program Files\", "~/Doc",
// "src/") e recebem aspas/barra final conforme o estilo do shell.

// completionPathTarget descreve o prefixo digitado já resolvido.
type completionPathTarget struct {
	// Dir é o diretório absoluto a listar.
	Dir string
	// Display é o prefixo do diretório COMO o usuário digitou (reusado no texto
	// sugerido), incluindo o separador final.
	Display string
	// Base é o trecho do nome a casar.
	Base string
}

// resolveCompletionPath resolve o diretório do prefixo digitado.
func resolveCompletionPath(fs completionFS, prefixo string, style completionQuoteStyle) completionPathTarget {
	if style == completionQuoteCMD {
		i := strings.LastIndexAny(prefixo, "\\/")
		if i < 0 {
			cwd, _ := fs.Cwd()
			return completionPathTarget{Dir: cwd, Base: prefixo}
		}
		display := prefixo[:i+1]
		dir := display
		if !filepath.IsAbs(dir) {
			cwd, _ := fs.Cwd()
			// "\Windows\" é enraizado na unidade ATUAL (sem letra). Join com o
			// cwd produziria "C:\<cwd>\Windows" — o correto é a raiz da unidade
			// do cwd. Caminhos UNC (\\servidor\share\) já são absolutos.
			if strings.HasPrefix(display, "\\") || strings.HasPrefix(display, "/") {
				if vol := filepath.VolumeName(cwd); vol != "" {
					dir = vol + display
				}
			}
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(cwd, display)
			}
		}
		return completionPathTarget{Dir: dir, Display: display, Base: prefixo[i+1:]}
	}

	i := strings.LastIndex(prefixo, "/")
	if i < 0 {
		if strings.HasPrefix(prefixo, "~") {
			// "~" sozinho: lista o home e sugere com o prefixo "~/".
			return completionPathTarget{Dir: fs.HomeDir(), Display: "~/", Base: ""}
		}
		cwd, _ := fs.Cwd()
		return completionPathTarget{Dir: cwd, Base: prefixo}
	}
	display := prefixo[:i+1]
	dir := display
	if strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(fs.HomeDir(), dir[2:])
	} else if !strings.HasPrefix(dir, "/") {
		cwd, _ := fs.Cwd()
		dir = filepath.Join(cwd, dir)
	}
	return completionPathTarget{Dir: dir, Display: display, Base: prefixo[i+1:]}
}

// completeDirectoryEntries lista arquivos/diretórios que casam com o prefixo.
// Nomes ocultos (".algo") só aparecem quando a base já começa com ".".
func completeDirectoryEntries(fs completionFS, prefixo string, style completionQuoteStyle, aberta rune) ([]completionMatch, error) {
	alvo := resolveCompletionPath(fs, prefixo, style)
	entries, err := fs.ListDir(alvo.Dir)
	if err != nil {
		return nil, err
	}

	mostraOcultos := strings.HasPrefix(alvo.Base, ".")
	matches := make([]completionMatch, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".") && !mostraOcultos {
			continue
		}
		if !completionNameMatches(alvo.Base, e.Name, style) {
			continue
		}
		texto := quoteCompletionText(alvo.Display+e.Name, style, aberta, e.IsDir)
		tipo := "file"
		if e.IsDir {
			tipo = "directory"
		}
		matches = append(matches, completionMatch{
			Text:     texto,
			ListItem: alvo.Display + e.Name,
			Type:     tipo,
			Tooltip:  completionFullPath(alvo.Dir, e.Name),
		})
		if len(matches) >= completionMaxItems {
			break
		}
	}
	return matches, nil
}

// completionFullPath monta o caminho completo para o tooltip.
func completionFullPath(dir, nome string) string {
	if strings.TrimSpace(dir) == "" {
		return nome
	}
	return filepath.Join(dir, nome)
}

// completionNameMatches compara o prefixo digitado com o nome da entrada. No
// cmd o FS do Windows é case-insensitive; no POSIX o match é exato.
func completionNameMatches(base, nome string, style completionQuoteStyle) bool {
	if base == "" {
		return true
	}
	if style == completionQuoteCMD {
		return strings.HasPrefix(strings.ToLower(nome), strings.ToLower(base))
	}
	return strings.HasPrefix(nome, base)
}

// quoteCompletionText aplica aspas/escape ao texto sugerido e a barra final de
// diretório (fora das aspas, para não escapar a aspa de fechamento).
//   - aberta é a aspa que já estava aberta no token (0 = nenhuma); a sugestão
//     continua com o MESMO tipo de aspa do texto atual;
//   - cmd: aspas duplas quando há espaço/metacaractere;
//   - POSIX: aspas simples por padrão, duplas quando o texto contém "'".
func quoteCompletionText(texto string, style completionQuoteStyle, aberta rune, dir bool) string {
	if style == completionQuoteCMD {
		sep := ""
		if dir {
			sep = "\\"
		}
		if aberta == '"' || completionPrecisaAspasCMD(texto) {
			return "\"" + strings.ReplaceAll(texto, "\"", "\"\"") + "\"" + sep
		}
		return texto + sep
	}

	sep := ""
	if dir {
		sep = "/"
	}
	if aberta == 0 && !completionPrecisaAspasPOSIX(texto) {
		return texto + sep
	}
	if aberta == '\'' || (aberta == 0 && !strings.Contains(texto, "'")) {
		return "'" + strings.ReplaceAll(texto, "'", "'\\''") + "'" + sep
	}
	// Aspas duplas: escapam \, ", $ e backtick.
	escapado := strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
		"$", "\\$",
		"\x60", "\\\x60",
	).Replace(texto)
	return "\"" + escapado + "\"" + sep
}

// completionPrecisaAspasCMD indica se o texto precisa de aspas no cmd.exe.
func completionPrecisaAspasCMD(texto string) bool {
	return strings.ContainsAny(texto, " \t&|<>^()\"")
}

// completionPrecisaAspasPOSIX indica se o texto precisa de aspas no bash/zsh.
func completionPrecisaAspasPOSIX(texto string) bool {
	return strings.ContainsAny(texto, " \t\"'$&|;<>()*?[]{}!#\\\x60")
}

// completionDescribeExecutavel devolve o tooltip de um executável do PATH.
func completionDescribeExecutavel(nome, dir string) string {
	if strings.TrimSpace(dir) == "" {
		return fmt.Sprintf("%s (PATH)", nome)
	}
	return fmt.Sprintf("%s (PATH: %s)", nome, dir)
}
