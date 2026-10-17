//go:build windows

package remotesession

import "strings"

// ── Tokenização mínima para completação ──
//
// O tokenizer só precisa responder "qual é o token sob o cursor?", "é o
// primeiro token?" e "há aspas abertas?". É simples de propósito: precisa ser
// testável e não pode quebrar o texto já digitado (o replacement cobre
// exatamente [Inicio, cursor)).

// completionQuoteStyle define a família de aspas/escape do shell.
type completionQuoteStyle int

const (
	// completionQuoteCMD: cmd.exe — aspas duplas, escape com '^'.
	completionQuoteCMD completionQuoteStyle = iota
	// completionQuotePOSIX: bash/zsh/WSL — aspas simples/duplas, escape '\'.
	completionQuotePOSIX
)

// completionToken descreve o token sob o cursor.
type completionToken struct {
	// Inicio é o índice (em runas) do início do token na linha.
	Inicio int
	// Texto é o trecho cru do token, de Inicio até o cursor.
	Texto string
	// Indice é a posição do token na linha (0 = comando).
	Indice int
	// Aspas é a aspa ainda aberta no cursor (0 = nenhuma).
	Aspas rune
}

// tokenizeCompletion percorre a linha até o cursor e devolve o token corrente.
func tokenizeCompletion(input string, cursor int, style completionQuoteStyle) completionToken {
	runas := []rune(input)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runas) {
		cursor = len(runas)
	}

	inicio := 0
	indice := 0
	temConteudo := false
	var aspas rune
	for i := 0; i < cursor; i++ {
		r := runas[i]
		if aspas != 0 {
			// Dentro de aspas: só fecha na aspa correspondente. Em POSIX,
			// dentro de "..." o '\' escapa o próximo caractere.
			if style == completionQuotePOSIX && aspas == '"' && r == '\\' && i+1 < cursor {
				i++
				continue
			}
			if r == aspas {
				aspas = 0
			}
			continue
		}
		// Fora de aspas: escapes.
		if style == completionQuotePOSIX && r == '\\' && i+1 < cursor {
			i++
			continue
		}
		if style == completionQuoteCMD && r == '^' && i+1 < cursor {
			i++
			continue
		}
		if r == '"' || (style == completionQuotePOSIX && r == '\'') {
			// Abrir aspas JÁ conta como conteúdo do token: um argumento entre
			// aspas (mesmo vazio) é um token, então o separador seguinte precisa
			// incrementar Indice. Sem isso, o token depois de um argumento entre
			// aspas era tratado como comando (Indice 0) e o TAB sugeria builtins
			// em vez de arquivos.
			aspas = r
			temConteudo = true
			continue
		}
		if completionIsTokenSeparator(r, style) {
			if temConteudo {
				indice++
				temConteudo = false
			}
			inicio = i + 1
			continue
		}
		temConteudo = true
	}

	return completionToken{
		Inicio: inicio,
		Texto:  string(runas[inicio:cursor]),
		Indice: indice,
		Aspas:  aspas,
	}
}

// unquoteCompletionToken devolve o valor do token sem aspas/escapes — é sobre
// ele que o matching de nomes acontece.
func unquoteCompletionToken(texto string, style completionQuoteStyle) string {
	var b strings.Builder
	runas := []rune(texto)
	var aspas rune
	for i := 0; i < len(runas); i++ {
		r := runas[i]
		if aspas != 0 {
			// Em POSIX, dentro de "..." o '\' só escapa $, crase, ", \ e
			// newline; antes de qualquer outro caractere ele é LITERAL (bash
			// mantém a barra). Desescapar tudo transformava "C:\Users\x" em
			// "C:Usersx" e o nome nunca casava.
			if style == completionQuotePOSIX && aspas == '"' && r == '\\' && i+1 < len(runas) &&
				completionPosixEscapavelEmAspasDuplas(runas[i+1]) {
				b.WriteRune(runas[i+1])
				i++
				continue
			}
			if r == aspas {
				aspas = 0
				continue
			}
			b.WriteRune(r)
			continue
		}
		if style == completionQuotePOSIX && r == '\\' && i+1 < len(runas) {
			b.WriteRune(runas[i+1])
			i++
			continue
		}
		if style == completionQuoteCMD && r == '^' && i+1 < len(runas) {
			b.WriteRune(runas[i+1])
			i++
			continue
		}
		if r == '"' || (style == completionQuotePOSIX && r == '\'') {
			aspas = r
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// completionPosixEscapavelEmAspasDuplas informa quais caracteres o bash/zsh
// realmente desescapa dentro de "...". Qualquer outro '\' é literal.
func completionPosixEscapavelEmAspasDuplas(r rune) bool {
	switch r {
	case '$', '\x60', '"', '\\', '\n':
		return true
	}
	return false
}

// completionIsTokenSeparator indica onde um token termina.
func completionIsTokenSeparator(r rune, style completionQuoteStyle) bool {
	switch r {
	case ' ', '\t', '\n', '\r', ';', '|', '&', '<', '>', '(', ')':
		return true
	case '=', ',':
		// No cmd, '=' e ',' separam argumentos; no POSIX, '=' faz parte do
		// token (VAR=valor, --opt=valor).
		return style == completionQuoteCMD
	}
	return false
}

// completionTemCaminho indica que o prefixo já aponta para um caminho (e não
// para um nome de comando).
func completionTemCaminho(prefixo string, style completionQuoteStyle) bool {
	if strings.Contains(prefixo, "/") {
		return true
	}
	if style == completionQuoteCMD && strings.Contains(prefixo, "\\") {
		return true
	}
	if strings.HasPrefix(prefixo, "~") {
		return true
	}
	if style == completionQuoteCMD && len(prefixo) >= 2 && prefixo[1] == ':' {
		return true
	}
	return false
}

// completionClampCursor limita o cursor (em runas) ao tamanho do texto.
func completionClampCursor(input string, cursor int) int {
	n := len([]rune(input))
	if cursor < 0 {
		return 0
	}
	if cursor > n {
		return n
	}
	return cursor
}

// completionTokenAt devolve o token sob o cursor e o seu valor SEM aspas (é
// sobre esse valor que o matching de nomes acontece).
func completionTokenAt(input string, cursor int, style completionQuoteStyle) (completionToken, string) {
	tok := tokenizeCompletion(input, cursor, style)
	return tok, unquoteCompletionToken(tok.Texto, style)
}

// completionDefaultSpan devolve o índice (em runas) do início do token sob o
// cursor. Usado como replacementIndex quando o provider não informa o seu.
func completionDefaultSpan(input string, cursor int) int {
	return tokenizeCompletion(input, cursor, completionQuotePOSIX).Inicio
}
