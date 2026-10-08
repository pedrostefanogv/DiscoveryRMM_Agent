package ai

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// NormalizeA2uiMessage corrige defeitos recorrentes dos payloads A2UI gerados
// pelo LLM ANTES de a mensagem chegar ao renderer do frontend. A interface é
// montada a partir do grafo de componentes, então dois erros de payload viram
// defeitos visíveis e não há CSS que os conserte:
//
//  1. Rótulo duplicado — o LLM põe o Text do rótulo no children do Row/Column
//     E o referencia como child do Button. O componente é renderizado duas
//     vezes: solto no container e dentro do botão ("Atualizar" aparecendo 2x).
//     O exemplo do system prompt ensinava exatamente isso; aqui o id que é
//     child de um Button/Card é retirado do children do pai.
//  2. Markdown cru — o catálogo A2UI renderiza Text como texto PURO: os
//     marcadores de negrito e de título saem literalmente na tela. Eles são
//     removidos aqui.
//
// Devolve a mensagem normalizada; quando não é um updateComponents parseável,
// devolve a entrada intacta (nunca piora o que veio do servidor).
func NormalizeA2uiMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return raw
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(msg), &parsed); err != nil {
		return raw
	}
	update, ok := parsed["updateComponents"].(map[string]any)
	if !ok {
		return raw
	}
	components, ok := update["components"].([]any)
	if !ok || len(components) == 0 {
		return raw
	}

	changed := false

	// Ids usados como child de algum componente (ex.: child do Button): esses
	// já são renderizados DENTRO do componente pai.
	childRefs := make(map[string]bool)
	for _, c := range components {
		comp, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if child, _ := comp["child"].(string); strings.TrimSpace(child) != "" {
			childRefs[strings.TrimSpace(child)] = true
		}
	}

	// Filtra children: remove os ids que já são child de outro componente, os
	// repetidos e a auto-referência.
	seenChild := make(map[string]bool, len(components))
	for _, c := range components {
		comp, ok := c.(map[string]any)
		if !ok {
			continue
		}
		rawChildren, ok := comp["children"].([]any)
		if !ok {
			continue
		}
		selfID, _ := comp["id"].(string)
		filtered := make([]any, 0, len(rawChildren))
		removed := false
		for _, rc := range rawChildren {
			childID, isStr := rc.(string)
			if !isStr {
				filtered = append(filtered, rc)
				continue
			}
			childID = strings.TrimSpace(childID)
			switch {
			case childID == "":
				removed = true
			case childRefs[childID]:
				// Rótulo do botão listado como irmão: some a duplicata.
				removed = true
			case childID == selfID:
				removed = true
			case seenChild[childID]:
				removed = true
			default:
				seenChild[childID] = true
				filtered = append(filtered, childID)
			}
		}
		if removed {
			comp["children"] = filtered
			changed = true
		}
	}

	// Text: tira o markdown que o renderer do catálogo não interpreta.
	for _, c := range components {
		comp, ok := c.(map[string]any)
		if !ok {
			continue
		}
		name, _ := comp["component"].(string)
		if !strings.EqualFold(name, "Text") {
			continue
		}
		text, ok := comp["text"].(string)
		if !ok || text == "" {
			continue
		}
		if clean := stripA2uiMarkdown(text); clean != text {
			comp["text"] = clean
			changed = true
		}
	}

	if !changed {
		return raw
	}

	// SetEscapeHTML(false): sem isso o json.Marshal escaparia os sinais de
	// maior/menor dos intervalos de versão, poluindo os logs e a comparação de
	// payloads duplicados feita pelo chamador.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(parsed); err != nil {
		return raw
	}
	return strings.TrimRight(buf.String(), "\n")
}

var (
	// Negrito em asteriscos e em underscores vira o texto puro.
	a2uiBoldStarRe       = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	a2uiBoldUnderscoreRe = regexp.MustCompile(`__([^_]+)__`)
	// Prefixo de título (# .. ######) sai; a variante h1..h5 já dá o estilo.
	a2uiHeadingRe = regexp.MustCompile(`^\s*#{1,6}\s+`)
)

// stripA2uiMarkdown remove os marcadores de markdown que o Text do catálogo
// A2UI exibe literalmente. Mantém o restante intacto (inclusive setas e travessões).
func stripA2uiMarkdown(text string) string {
	out := a2uiBoldStarRe.ReplaceAllString(text, "$1")
	out = a2uiBoldUnderscoreRe.ReplaceAllString(out, "$1")
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		lines[i] = a2uiHeadingRe.ReplaceAllString(line, "")
	}
	return strings.Join(lines, "\n")
}
