package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// NormalizeA2uiMessage corrige defeitos recorrentes dos payloads A2UI gerados
// pelo LLM ANTES de a mensagem chegar ao renderer do frontend. A interface é
// montada a partir do grafo de componentes, então um erro de payload vira
// defeito visível e não há CSS que o conserte:
//
//  1. Rótulo duplicado — o LLM põe o Text do rótulo no children do Row/Column
//     E o referencia como child do Button. O componente é renderizado duas
//     vezes: solto no container e dentro do botão ("Atualizar" aparecendo 2x).
//     O exemplo do system prompt ensinava exatamente isso; aqui o id que é
//     child de um Button/Card é retirado do children do pai.
//  2. Markdown cru — o catálogo A2UI renderiza Text como texto PURO: os
//     marcadores de negrito e de título saem literalmente na tela. Eles são
//     removidos aqui.
//  3. Botões empilhados — o catálogo empilha os filhos de Column/Card
//     (flex-direction: column), então botões de ação irmãos saíam um embaixo do
//     outro. Todos os botões irmãos são agrupados num Row (horizontal) — ver
//     groupA2uiButtons.
//
// A função é idempotente: aplicar duas vezes devolve o mesmo resultado (o Row
// criado é ignorado na segunda passada).
//
// Devolve a mensagem normalizada; quando não é um updateComponents parseável,
// devolve a entrada intacta (nunca piora o que veio do servidor).
// normalizeA2uiForClient normaliza uma mensagem A2UI ANTES de entregá-la ao
// frontend. Ponto único para que TODOS os caminhos (streaming multi-round,
// fallback síncrono e Send síncrono) apliquem as mesmas correções: o caminho
// síncrono (SendWithA2ui) entregava a mensagem crua e os cards saíam com
// rótulo duplicado, markdown literal e botões empilhados.
//
// Devolve "" para mensagens vazias ou o literal "null" (o servidor emite
// "a2uiJson":null em alguns eventos) — o chamador só deve repassar se != "".
func normalizeA2uiForClient(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" || msg == "null" {
		return ""
	}
	return NormalizeA2uiMessage(msg)
}

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

	// Botões irmãos consecutivos: o catálogo empilha os filhos de Column/Card
	// (flex-direction: column), então "Voltar / Avançar / Não resolveu" saíam um
	// embaixo do outro. Agrupa a sequência num Row (horizontal, com quebra de
	// linha) para que fiquem lado a lado.
	if grouped, count := groupA2uiButtons(components); count > 0 {
		components = grouped
		update["components"] = components
		changed = true
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

// groupA2uiButtons agrupa TODOS os botões irmãos de cada contêiner num único Row.
// O catálogo empilha os filhos de Column/Card (flex-direction: column), então
// "Voltar / Avançar / Não resolveu" saíam um embaixo do outro — inclusive quando
// havia um Text/Divider entre eles. Contêineres Row já são horizontais e ficam
// intactos; um botão sozinho também (um Row de 1 item só adicionaria ruído).
//
// O Row entra na posição do primeiro botão; os demais filhos mantêm a ordem
// relativa. Devolve a lista de componentes (com os Rows novos no fim) e quantos
// agrupamentos foram feitos. As referências por id continuam válidas: os botões
// mantêm o id e o `child` do rótulo.
func groupA2uiButtons(components []any) ([]any, int) {
	byID := make(map[string]map[string]any, len(components))
	usedIDs := make(map[string]bool, len(components))
	for _, c := range components {
		comp, ok := c.(map[string]any)
		if !ok {
			continue
		}
		id, _ := comp["id"].(string)
		if id == "" {
			continue
		}
		byID[id] = comp
		usedIDs[id] = true
	}
	if len(byID) == 0 {
		return components, 0
	}

	isButton := func(id string) bool {
		comp, ok := byID[id]
		if !ok {
			return false
		}
		name, _ := comp["component"].(string)
		return strings.EqualFold(name, "Button")
	}

	nextRow := 1
	newRowID := func() string {
		for {
			id := fmt.Sprintf("a2ui_row_btn_%d", nextRow)
			nextRow++
			if !usedIDs[id] {
				usedIDs[id] = true
				return id
			}
		}
	}

	added := make([]any, 0, 2)
	grouped := 0
	for _, c := range components {
		comp, ok := c.(map[string]any)
		if !ok {
			continue
		}
		name, _ := comp["component"].(string)
		if strings.EqualFold(name, "Row") {
			continue // já é horizontal
		}
		rawChildren, ok := comp["children"].([]any)
		if !ok || len(rawChildren) < 2 {
			continue
		}

		// Coleta os botões DIRETOS do contêiner, mesmo separados por Text/
		// Divider: um botão isolado entre textos ainda é ação e deve ficar lado
		// a lado com as demais.
		buttonIDs := make([]any, 0, len(rawChildren))
		firstButtonIdx := -1
		for idx, rc := range rawChildren {
			childID, isStr := rc.(string)
			if !isStr || !isButton(childID) {
				continue
			}
			if firstButtonIdx < 0 {
				firstButtonIdx = idx
			}
			buttonIDs = append(buttonIDs, childID)
		}
		if len(buttonIDs) < 2 {
			continue // botão sozinho: deixa onde está
		}

		// O Row entra na posição do primeiro botão; os botões saem da lista de
		// filhos do contêiner (viram filhos do Row) e os demais elementos
		// mantêm a ordem relativa.
		rowID := newRowID()
		out := make([]any, 0, len(rawChildren))
		for idx, rc := range rawChildren {
			if idx == firstButtonIdx {
				out = append(out, rowID)
				continue
			}
			if childID, isStr := rc.(string); isStr && isButton(childID) {
				continue
			}
			out = append(out, rc)
		}
		added = append(added, map[string]any{
			"id":        rowID,
			"component": "Row",
			"children":  buttonIDs,
		})
		comp["children"] = out
		grouped++
	}

	if len(added) == 0 {
		return components, 0
	}
	return append(components, added...), grouped
}
