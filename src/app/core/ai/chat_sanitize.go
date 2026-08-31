package ai

import (
	"regexp"
	"strings"
)

// Sanitização de vazamentos de tool calls / marcações internas do LLM no
// texto exibido ao usuário.
//
// Contexto (2026-08-30): o modelo de linguagem às vezes emite tool calls como
// TEXTO em vez de function call nativa, em três formatos observados:
//
//  1. Blocos ```json com array de invokes: [{"name":"get_inventory",...}]
//  2. Marcação DSML nativa do modelo: <｜DSML｜tool_invokes><invoke ...>...</｜DSML｜tool_invokes>
//  3. Blocos ```json com ação A2UI: {"version":"a2ui","action":"search",...}
//
// Esses vazamentos aparecem principalmente no fallback sync (que não suporta
// function calling) e quando o LLM responde sem usar tools. A sanitização é
// aplicada em camadas: backend (multi-round + fallback) e frontend (streaming).

var (
	// dsmlBlockRe captura a marcação DSML nativa do modelo. Formato real:
	// <｜DSML｜tool_invokes>...</｜DSML｜tool_invokes> (separador ｜ fullwidth
	// ou | ASCII, nome da seção em [a-z_]).
	dsmlBlockRe = regexp.MustCompile(`(?s)<[/]?[｜|]DSML[｜|][a-z_]*>.*?<[/][｜|]DSML[｜|][a-z_]*>`)

	// dsmlOrphanRe captura tags DSML órfãs (sem par de abertura/fechamento
	// completo — ex.: stream cortado no meio).
	dsmlOrphanRe = regexp.MustCompile(`</?[｜|]DSML[｜|][a-z_]*>`)

	// jsonFenceRe captura blocos de código ```json ... ``` (ou ``` ... ```).
	jsonFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

	// invokeArrayRe detecta um array de invokes estilo [{"name": "...", "arguments": {...}}].
	invokeArrayRe = regexp.MustCompile(`(?s)^\s*\[\s*\{\s*"name"\s*:`)

	// a2uiActionRe detecta uma ação A2UI embutida em JSON.
	a2uiActionRe = regexp.MustCompile(`(?s)^\s*\{\s*"version"\s*:\s*"a2ui"`)

	// invokeTagRe captura <invoke name="...">...</invoke> soltos (fora de DSML).
	invokeTagRe = regexp.MustCompile(`(?s)<invoke\s+name="[^"]*">.*?</invoke>`)

	// parameterTagRe captura <parameter name="...">...</parameter> soltos.
	parameterTagRe = regexp.MustCompile(`(?s)<parameter\s+name="[^"]*">.*?</parameter>`)

	// toolInvokesTagRe captura <tool_invokes>...</tool_invokes> soltos.
	toolInvokesTagRe = regexp.MustCompile(`(?s)</?[｜|]?tool_invokes[｜|]?>`)
)

// sanitizeAssistantText remove vazamentos de tool calls e marcações internas
// do LLM de um texto de resposta do assistente. Retorna o texto limpo e um
// booleano indicando se algo foi removido.
func sanitizeAssistantText(text string) (string, bool) {
	if text == "" {
		return text, false
	}
	clean := text
	removed := false

	// 1. Remove blocos DSML completos (com conteúdo).
	if dsmlBlockRe.MatchString(clean) {
		clean = dsmlBlockRe.ReplaceAllString(clean, "")
		removed = true
	}
	// 2. Remove tags DSML órfãs.
	if dsmlOrphanRe.MatchString(clean) {
		clean = dsmlOrphanRe.ReplaceAllString(clean, "")
		removed = true
	}
	// 3. Remove <invoke>/<parameter>/<tool_invokes> soltos.
	for _, re := range []*regexp.Regexp{invokeTagRe, parameterTagRe, toolInvokesTagRe} {
		if re.MatchString(clean) {
			clean = re.ReplaceAllString(clean, "")
			removed = true
		}
	}

	// 4. Remove blocos ```json cujo conteúdo seja um array de invokes ou uma
	// ação A2UI (o LLM "prometeu" executar tools como texto).
	clean = jsonFenceRe.ReplaceAllStringFunc(clean, func(m string) string {
		sub := jsonFenceRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		body := strings.TrimSpace(sub[1])
		if invokeArrayRe.MatchString(body) || a2uiActionRe.MatchString(body) {
			removed = true
			return ""
		}
		return m
	})

	if !removed {
		return text, false
	}

	// Limpeza final: colapsa linhas vazias em excesso deixadas pelas remoções.
	clean = strings.TrimSpace(clean)
	clean = collapseBlankLines(clean)
	return clean, true
}

// collapseBlankLines reduz 3+ quebras de linha consecutivas para 2.
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
