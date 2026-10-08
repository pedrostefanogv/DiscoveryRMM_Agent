package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// Payload REAL de produção (chat.db id 337, 2026-10-08) reduzido aos dois
// primeiros itens — é exatamente o que renderizou "Atualizar" duas vezes e os
// asteriscos crus de markdown na tela.
const realA2uiUpdate = `{"version":"v0.9","updateComponents":{"surfaceId":"updates_card","components":[{"id":"root","component":"Column","children":["title","subtitle","divider","row1","row2"]},{"id":"title","component":"Text","text":"# Atualizações pendentes","variant":"h3"},{"id":"subtitle","component":"Text","text":"14 programas com atualização disponível. Toque em **Atualizar** no que quiser.","variant":"caption"},{"id":"divider","component":"Divider"},{"id":"row1","component":"Row","children":["t1","b1label","b1"]},{"id":"t1","component":"Text","text":"**Google Chrome** — 154.0.8037.58 → 155.0.8059.40"},{"id":"b1label","component":"Text","text":"Atualizar"},{"id":"b1","component":"Button","child":"b1label","action":{"event":{"name":"upgrade_package","context":{"id":"Google.Chrome.EXE"}}}},{"id":"row2","component":"Row","children":["t2","b2label","b2"]},{"id":"b2label","component":"Text","text":"Atualizar"},{"id":"b2","component":"Button","child":"b2label","action":{"event":{"name":"upgrade_package","context":{"id":"AnyDesk.AnyDesk"}}}}]}}`

func decodeA2uiComponents(t *testing.T, msg string) map[string]map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(msg), &parsed); err != nil {
		t.Fatalf("JSON inválido depois da normalização: %v", err)
	}
	update, ok := parsed["updateComponents"].(map[string]any)
	if !ok {
		t.Fatalf("mensagem sem updateComponents: %s", msg)
	}
	comps, ok := update["components"].([]any)
	if !ok {
		t.Fatalf("mensagem sem components: %s", msg)
	}
	byID := make(map[string]map[string]any, len(comps))
	for _, c := range comps {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		byID[id] = m
	}
	return byID
}

func TestNormalizeA2uiMessage_RealProductionPayload(t *testing.T) {
	out := NormalizeA2uiMessage(realA2uiUpdate)
	if out == realA2uiUpdate {
		t.Fatal("payload com rótulo duplicado e markdown deveria ter sido normalizado")
	}
	byID := decodeA2uiComponents(t, out)

	// 1) O botão continua com o rótulo DENTRO dele (não é para apagar o texto).
	b1, ok := byID["b1"]
	if !ok {
		t.Fatal("Button b1 desapareceu")
	}
	if b1["child"] != "b1label" {
		t.Fatalf("Button b1 perdeu o child: %v", b1["child"])
	}
	if _, ok := byID["b1label"]; !ok {
		t.Fatal("componente do rótulo (b1label) desapareceu")
	}

	// 2) O rótulo não é mais listado como irmão no Row -> some a duplicata.
	row1, _ := byID["row1"]["children"].([]any)
	if len(row1) != 2 || row1[0] != "t1" || row1[1] != "b1" {
		t.Fatalf("row1 children = %v, quer [t1 b1]", row1)
	}
	row2, _ := byID["row2"]["children"].([]any)
	if len(row2) != 2 || row2[1] != "b2" {
		t.Fatalf("row2 children = %v, quer [t2 b2]", row2)
	}

	// 3) Markdown cru removido, conteúdo preservado.
	subtitle := byID["subtitle"]["text"].(string)
	if strings.Contains(subtitle, "**") {
		t.Fatalf("subtitle ainda tem markdown: %q", subtitle)
	}
	if !strings.Contains(subtitle, "Atualizar") {
		t.Fatalf("subtitle perdeu texto: %q", subtitle)
	}
	title := byID["title"]["text"].(string)
	if strings.HasPrefix(title, "#") {
		t.Fatalf("title ainda tem heading: %q", title)
	}
	if !strings.Contains(title, "Atualizações pendentes") {
		t.Fatalf("title perdeu texto: %q", title)
	}
	t1 := byID["t1"]["text"].(string)
	if strings.Contains(t1, "**") || !strings.Contains(t1, "→") || !strings.Contains(t1, "155.0.8059.40") {
		t.Fatalf("t1 = %q (esperado sem ** e com a seta/versão)", t1)
	}
}

// Card de passo a passo em que o LLM pôs os botões direto no Column: o catálogo
// empilha os filhos (flex-direction: column) e "Voltar/Avançar/Não resolveu"
// ficavam um embaixo do outro.
const buttonsStackedUpdate = `{"version":"v0.9","updateComponents":{"surfaceId":"wizard","components":[{"id":"root","component":"Column","children":["t","b1","b2","b3"]},{"id":"t","component":"Text","text":"Etapa 1 de 3"},{"id":"b1","component":"Button","child":"l1","action":{"event":{"name":"ui.prev"}}},{"id":"l1","component":"Text","text":"Voltar"},{"id":"b2","component":"Button","child":"l2","action":{"event":{"name":"ui.next"}}},{"id":"l2","component":"Text","text":"Avançar"},{"id":"b3","component":"Button","child":"l3","action":{"event":{"name":"ui.help"}}},{"id":"l3","component":"Text","text":"Nao resolveu"}]}}`

func TestNormalizeA2uiMessage_GroupsConsecutiveButtonsIntoRow(t *testing.T) {
	out := NormalizeA2uiMessage(buttonsStackedUpdate)
	if out == buttonsStackedUpdate {
		t.Fatal("botoes irmãos consecutivos deveriam ter sido agrupados num Row")
	}
	byID := decodeA2uiComponents(t, out)

	root, _ := byID["root"]["children"].([]any)
	if len(root) != 2 || root[0] != "t" {
		t.Fatalf("root children = %v, quer [t <row>]", root)
	}
	rowID, _ := root[1].(string)
	row, ok := byID[rowID]
	if !ok {
		t.Fatalf("Row %q nao existe", rowID)
	}
	if name, _ := row["component"].(string); !strings.EqualFold(name, "Row") {
		t.Fatalf("componente %q = %v, quer Row", rowID, row["component"])
	}
	kids, _ := row["children"].([]any)
	if len(kids) != 3 || kids[0] != "b1" || kids[1] != "b2" || kids[2] != "b3" {
		t.Fatalf("Row children = %v, quer [b1 b2 b3]", kids)
	}
	// Cada botao continua com o rotulo dentro dele (o child nao pode ser perdido).
	for id, label := range map[string]string{"b1": "l1", "b2": "l2", "b3": "l3"} {
		if byID[id]["child"] != label {
			t.Fatalf("%s perdeu o child (%v)", id, byID[id]["child"])
		}
		if _, ok := byID[label]; !ok {
			t.Fatalf("rotulo %s desapareceu", label)
		}
	}
}

// Botões separados por Text/Divider também precisam ficar lado a lado: sem isso
// o caso "botão, explicação, botão" continuava empilhado.
const buttonsSeparatedUpdate = `{"version":"v0.9","updateComponents":{"surfaceId":"wizard","components":[{"id":"root","component":"Column","children":["t","b1","dica","b2","sep","b3"]},{"id":"t","component":"Text","text":"Etapa 1"},{"id":"b1","component":"Button","child":"l1"},{"id":"l1","component":"Text","text":"Voltar"},{"id":"dica","component":"Text","text":"Ligue a impressora"},{"id":"b2","component":"Button","child":"l2"},{"id":"l2","component":"Text","text":"Avançar"},{"id":"sep","component":"Divider"},{"id":"b3","component":"Button","child":"l3"},{"id":"l3","component":"Text","text":"Nao resolveu"}]}}`

func TestNormalizeA2uiMessage_GroupsButtonsSeparatedByTextAndDivider(t *testing.T) {
	out := NormalizeA2uiMessage(buttonsSeparatedUpdate)
	byID := decodeA2uiComponents(t, out)

	root, _ := byID["root"]["children"].([]any)
	if len(root) != 4 || root[0] != "t" {
		t.Fatalf("root children = %v, quer [t <row> dica sep]", root)
	}
	rowID, _ := root[1].(string)
	row, ok := byID[rowID]
	if !ok {
		t.Fatalf("Row %q nao existe", rowID)
	}
	if name, _ := row["component"].(string); !strings.EqualFold(name, "Row") {
		t.Fatalf("componente %q = %v, quer Row", rowID, row["component"])
	}
	kids, _ := row["children"].([]any)
	if len(kids) != 3 || kids[0] != "b1" || kids[1] != "b2" || kids[2] != "b3" {
		t.Fatalf("Row children = %v, quer [b1 b2 b3]", kids)
	}
}

// Payload que o LLM JÁ montou certo (botões dentro de um Row) não deve mudar.
const buttonsInRowUpdate = `{"version":"v0.9","updateComponents":{"surfaceId":"wizard","components":[{"id":"root","component":"Column","children":["nav"]},{"id":"nav","component":"Row","children":["b1","b2"]},{"id":"b1","component":"Button","child":"l1"},{"id":"l1","component":"Text","text":"Voltar"},{"id":"b2","component":"Button","child":"l2"},{"id":"l2","component":"Text","text":"Avançar"}]}}`

func TestNormalizeA2uiMessage_KeepsButtonsAlreadyInRow(t *testing.T) {
	if got := NormalizeA2uiMessage(buttonsInRowUpdate); got != buttonsInRowUpdate {
		t.Fatalf("payload ja correto nao deveria mudar: in = %s / out = %s", buttonsInRowUpdate, got)
	}
}

// normalizeA2uiForClient e o ponto unico usado por TODOS os caminhos que
// entregam A2UI ao frontend (streaming, fallback e Send sincrono).
func TestNormalizeA2uiForClient_DropsEmptyAndNull(t *testing.T) {
	for _, in := range []string{"", "   ", "null", " null "} {
		if got := normalizeA2uiForClient(in); got != "" {
			t.Fatalf("normalizeA2uiForClient(%q) = %q, quer string vazia", in, got)
		}
	}
}

func TestNormalizeA2uiForClient_AppliesButtonGrouping(t *testing.T) {
	out := normalizeA2uiForClient(buttonsStackedUpdate)
	if out == "" || out == buttonsStackedUpdate {
		t.Fatal("mensagem deveria sair normalizada (nao vazia nem igual a entrada)")
	}
	byID := decodeA2uiComponents(t, out)
	root, _ := byID["root"]["children"].([]any)
	if len(root) != 2 {
		t.Fatalf("root children = %v, quer [t <row>]", root)
	}
}

// Idempotencia: o normalizador roda no streaming e pode rodar de novo no
// fallback/cache — a segunda passada nao pode criar Rows aninhados nem mudar o
// payload.
func TestNormalizeA2uiMessage_IsIdempotent(t *testing.T) {
	for name, in := range map[string]string{
		"botoes empilhados": buttonsStackedUpdate,
		"botoes separados":  buttonsSeparatedUpdate,
		"botoes ja em Row":  buttonsInRowUpdate,
		"payload real":      realA2uiUpdate,
	} {
		once := NormalizeA2uiMessage(in)
		twice := NormalizeA2uiMessage(once)
		if twice != once {
			t.Fatalf("%s: normalizacao nao e idempotente (1x = %s / 2x = %s)", name, once, twice)
		}
	}
}

func TestNormalizeA2uiMessage_Passthrough(t *testing.T) {
	cases := []string{
		rawCreate,
		rawCleanUpdate,
		rawOtherVerb,
		"não é json",
		"",
	}
	for _, in := range cases {
		if got := NormalizeA2uiMessage(in); got != in {
			t.Fatalf("mensagem não deveria mudar:\n in = %s\nout = %s", in, got)
		}
	}
}

const rawCreate = `{"version":"v0.9","createSurface":{"surfaceId":"x"}}`

// Payload já correto (rótulo só dentro do botão, texto sem markdown).
const rawCleanUpdate = `{"version":"v0.9","updateComponents":{"surfaceId":"x","components":[{"id":"root","component":"Column","children":["btn"]},{"id":"lbl","component":"Text","text":"Instalar"},{"id":"btn","component":"Button","child":"lbl","action":{"event":{"name":"install_package","context":{}}}}]}}`

const rawOtherVerb = `{"version":"v0.9","updateDataModel":{"surfaceId":"x","data":1}}`
