//go:build windows

package remotesession

import (
	"strings"
	"unicode/utf8"
)

// sanitizeCompletionInput remove o prefixo de prompt da linha enviada pelo
// viewer. O caminho ConPTY manda a linha inteira do xterm (ex.:
// "PS C:\Users\x> Get-Ch"); o TabExpansion2 precisa apenas do texto do comando.
// Devolve o texto sem prompt, o cursor ajustado (em runas) e o tamanho do
// prefixo em runas — este ultimo e somado de volta ao ReplacementIndex para
// que o viewer aplique a substituicao na coordenada da linha ORIGINAL.
func sanitizeCompletionInput(input string, cursor int) (string, int, int) {
	bytePrefix := completionPromptPrefixLen(input)
	if bytePrefix <= 0 {
		return input, clampCompletionInt(cursor, 0, utf8.RuneCountInString(input)), 0
	}

	prefixRunes := utf8.RuneCountInString(input[:bytePrefix])
	trimmed := input[bytePrefix:]
	return trimmed, clampCompletionInt(cursor-prefixRunes, 0, utf8.RuneCountInString(trimmed)), prefixRunes
}

// completionPromptPrefixLen devolve o tamanho em bytes do prefixo de prompt no
// inicio da linha, ou 0. Heuristica conservadora — "echo $HOME" NAO pode ser
// confundido com prompt:
//   - PowerShell: "PS C:\Users\x> " / "PS> "
//   - cmd:        "C:\Users\x>" (letra de unidade + ':' + separador)
//   - unix:       "user@host:~$ " / "root# " / "[user@host]$ "
func completionPromptPrefixLen(s string) int {
	const maxPrefix = 300
	if s == "" {
		return 0
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > maxPrefix {
		s = s[:maxPrefix]
	}

	// PowerShell: "PS", "PS>", "PS <caminho>" (o primeiro '>' e o do prompt).
	if len(s) >= 2 && (s[0] == 'P' || s[0] == 'p') && (s[1] == 'S' || s[1] == 's') &&
		(len(s) == 2 || s[2] == ' ' || s[2] == '>' || s[2] == '\\' || s[2] == '/') {
		if i := strings.IndexByte(s, '>'); i >= 0 {
			if p := completionSkipSpaces(s, i+1); p > 0 {
				return p
			}
		}
	}

	// cmd: "C:\...>" — o primeiro '>' depois da unidade e o do prompt.
	if len(s) >= 3 && completionIsDriveLetter(s[0]) && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		if i := strings.IndexByte(s, '>'); i >= 0 {
			if p := completionSkipSpaces(s, i+1); p > 0 {
				return p
			}
		}
	}

	// unix: delimitador '$'/'#'. So aceita quando o prefixo PARECE prompt
	// (contem ':' ou '@', ou nao tem espaco) — evita "echo $HOME".
	if i := strings.IndexAny(s, "$#"); i > 0 && i < 120 {
		head := s[:i]
		p := completionSkipSpaces(s, i+1)
		// Exige espaco (ou fim da linha) depois do delimitador: barra
		// "$env:PATH"/"#region" (i==0) e "foo#bar" (sem espaco).
		if p > i+1 || p == len(s) {
			// So aceita quando o prefixo PARECE prompt: contem ':' ou '@',
			// ou e um "usuario" curto sem espaco (root, admin).
			if strings.ContainsAny(head, ":@") ||
				(!strings.Contains(head, " ") && utf8.RuneCountInString(head) <= 24) {
				return p
			}
		}
	}

	return 0
}

func completionSkipSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func completionIsDriveLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func clampCompletionInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
