package support

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateUTF8 garante corte por runas sem quebrar multibyte (o corte
// antigo por bytes corrompia resumos com acentuação PT-BR).
func TestTruncateUTF8(t *testing.T) {
	if got := truncateUTF8("abc", 10); got != "abc" {
		t.Fatalf("string curta não deve ser alterada: %q", got)
	}
	long := strings.Repeat("ç", 300)
	got := truncateUTF8(long, 180)
	if !utf8.ValidString(got) {
		t.Fatal("resumo truncado deve ser UTF-8 válido")
	}
	if got := []rune(got); len(got) != 183 { // 180 + "..."
		t.Fatalf("tamanho inesperado: %d runas", len(got))
	}
	mixed := strings.Repeat("ação ", 50) // 250 runas
	got2 := truncateUTF8(mixed, 10)
	if !utf8.ValidString(got2) {
		t.Fatal("resumo misto deve ser UTF-8 válido")
	}
}

// TestBuildSummary_MultibyteSafe valida o caminho completo do resumo com
// conteúdo acentuado longo.
func TestBuildSummary_MultibyteSafe(t *testing.T) {
	content := "# Título\n\n" + strings.Repeat("Instalação do agente Discovery — çãõéê ", 20)
	summary := buildSummary(content)
	if !utf8.ValidString(summary) {
		t.Fatal("summary com UTF-8 inválido (corte por bytes regressou?)")
	}
}

// TestKnowledgeBackupKey: chave de backup vive fora dos prefixos que o
// refresh limpa, preservando o fallback offline.
func TestKnowledgeBackupKey(t *testing.T) {
	got := knowledgeBackupKey("knowledge:list:scope:cat")
	if got != "knowledge:backup:list:scope:cat" {
		t.Fatalf("chave inesperada: %q", got)
	}
	if strings.HasPrefix(knowledgeBackupKey("knowledge:list:x"), "knowledge:list:") {
		t.Fatal("backup não deve herdar prefixo knowledge:list:")
	}
}

// TestParseKnowledgeListBody cobre os dois formatos aceitos do servidor.
func TestParseKnowledgeListBody(t *testing.T) {
	direct := []byte(`[{"id":"a1","title":"Guia","content":"# Passo 1"}]`)
	arts, err := parseKnowledgeListBody(direct)
	if err != nil || len(arts) != 1 || arts[0].ID != "a1" {
		t.Fatalf("lista direta falhou: %v %+v", err, arts)
	}
	envelope := []byte(`{"items":[{"id":"b2","title":"Outro"}]}`)
	arts2, err := parseKnowledgeListBody(envelope)
	if err != nil || len(arts2) != 1 || arts2[0].ID != "b2" {
		t.Fatalf("envelope items falhou: %v %+v", err, arts2)
	}
	// summary derivado do conteúdo quando ausente
	if arts[0].Summary == "" {
		t.Fatal("summary deveria ser derivado do conteúdo")
	}
}
