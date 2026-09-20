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

// TestParseKnowledgeArticle_AgentDto cobre o mapeamento do DTO plano do
// agent (AgentKnowledgeArticleDto): author deriva de createdBy/lastEditedBy
// e scope deriva de clientId/siteId (herança site > client > global).
func TestParseKnowledgeArticle_AgentDto(t *testing.T) {
	// Autor: createdBy presente
	art := parseKnowledgeArticle(map[string]any{
		"id": "a1", "title": "Guia", "createdBy": "admin@acme", "clientId": nil, "siteId": nil,
	})
	if art.Author != "admin@acme" {
		t.Fatalf("author deveria vir de createdBy: %q", art.Author)
	}
	if art.Scope != "Global" {
		t.Fatalf("scope de artigo sem cliente/site deveria ser Global: %q", art.Scope)
	}
	if art.Difficulty != "Global" {
		t.Fatalf("difficulty derivado do scope: %q", art.Difficulty)
	}

	// Autor: fallback para lastEditedBy
	art2 := parseKnowledgeArticle(map[string]any{
		"id": "a2", "lastEditedBy": "tech@acme",
	})
	if art2.Author != "tech@acme" {
		t.Fatalf("author deveria cair para lastEditedBy: %q", art2.Author)
	}

	// Scope por clientId (escopo cliente)
	art3 := parseKnowledgeArticle(map[string]any{
		"id": "a3", "clientId": "0195c9e5-1111-2222-3333-444455556666",
	})
	if art3.Scope != "Client" || art3.Difficulty != "Cliente" {
		t.Fatalf("scope/difficulty de cliente: %q/%q", art3.Scope, art3.Difficulty)
	}

	// Scope por siteId (escopo site — vence sobre clientId)
	art4 := parseKnowledgeArticle(map[string]any{
		"id": "a4", "clientId": "0195c9e5-1111-2222-3333-444455556666",
		"siteId": "0195c9e5-7777-8888-9999-aaaa bbbbcccc",
	})
	if art4.Scope != "Site" || art4.Difficulty != "Site" {
		t.Fatalf("scope/difficulty de site: %q/%q", art4.Scope, art4.Difficulty)
	}

	// scopeOrigin legado também é respeitado
	art5 := parseKnowledgeArticle(map[string]any{"id": "a5", "scopeOrigin": "site"})
	if art5.Scope != "Site" {
		t.Fatalf("scope de scopeOrigin legado: %q", art5.Scope)
	}
}

// TestParseKnowledgeListEnvelope_Pagination cobre o envelope paginado da API
// (items + nextCursor + hasMore) e o mapeamento de status/versão do DTO.
func TestParseKnowledgeListEnvelope_Pagination(t *testing.T) {
	body := []byte("{" +
		"\"items\":[{\"id\":\"a1\",\"title\":\"Guia\",\"status\":\"Published\",\"currentVersionNumber\":7," +
		"\"createdBy\":\"admin@acme\",\"clientId\":\"0195c9e5-1111-2222-3333-444455556666\"}]," +
		"\"nextCursor\":\"MTcwOTk5OTk5OXw4ODg4ODg4ODg4ODg4ODg4ODg4ODg4ODg4ODg4ODg4ODg=\"," +
		"\"hasMore\":true,\"limit\":200}")
	page, err := parseKnowledgeListEnvelope(body)
	if err != nil {
		t.Fatalf("envelope paginado falhou: %v", err)
	}
	if len(page.Articles) != 1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("metadados de paginação incorretos: %+v", page)
	}
	a := page.Articles[0]
	if a.Status != "Published" || a.VersionNumber != 7 {
		t.Fatalf("status/versão: %q/%d", a.Status, a.VersionNumber)
	}
	if a.Author != "admin@acme" || a.Scope != "Client" {
		t.Fatalf("author/scope: %q/%q", a.Author, a.Scope)
	}

	// Array direto (respostas antigas) continua válido e sem paginação
	direct := []byte("[{\"id\":\"b2\",\"title\":\"Outro\"}]")
	page2, err := parseKnowledgeListEnvelope(direct)
	if err != nil || len(page2.Articles) != 1 || page2.HasMore || page2.NextCursor != "" {
		t.Fatalf("array direto falhou: %v %+v", err, page2)
	}

	// Última página: hasMore=false não deve pedir nova página
	last := []byte("{\"items\":[],\"nextCursor\":null,\"hasMore\":false,\"limit\":200}")
	page3, err := parseKnowledgeListEnvelope(last)
	if err != nil || page3.HasMore || page3.NextCursor != "" {
		t.Fatalf("última página falhou: %v %+v", err, page3)
	}
}


