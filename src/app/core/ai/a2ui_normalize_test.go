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
