package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- buildExecutionCustomFieldCtx ---

func TestBuildExecutionCustomFieldCtx_OmitsSecrets(t *testing.T) {
	fields := []RuntimeCustomField{
		{DefinitionID: "def-1", Name: "public_field", ValueJson: json.RawMessage(`"hello"`), IsSecret: false},
		{DefinitionID: "def-2", Name: "secret_field", ValueJson: json.RawMessage(`"s3cr3t"`), IsSecret: true},
	}
	ctx := buildExecutionCustomFieldCtx(fields)

	if _, ok := ctx.Fields["public_field"]; !ok {
		t.Error("campo público deveria estar no mapa")
	}
	if _, ok := ctx.Fields["secret_field"]; ok {
		t.Error("campo secreto NAO deveria estar no mapa Fields")
	}
	// Mas deve estar nos índices internos para validação de escrita
	if _, ok := ctx.rawByName["secret_field"]; !ok {
		t.Error("campo secreto deveria estar no rawByName para validação")
	}
}

func TestBuildExecutionCustomFieldCtx_OmitsNull(t *testing.T) {
	fields := []RuntimeCustomField{
		{DefinitionID: "def-1", Name: "null_field", ValueJson: json.RawMessage(`null`), IsSecret: false},
		{DefinitionID: "def-2", Name: "empty_field", ValueJson: json.RawMessage(nil), IsSecret: false},
		{DefinitionID: "def-3", Name: "real_field", ValueJson: json.RawMessage(`42`), IsSecret: false},
	}
	ctx := buildExecutionCustomFieldCtx(fields)

	if _, ok := ctx.Fields["null_field"]; ok {
		t.Error("campo null NAO deveria estar no mapa")
	}
	if _, ok := ctx.Fields["empty_field"]; ok {
		t.Error("campo vazio NAO deveria estar no mapa")
	}
	if val, ok := ctx.Fields["real_field"]; !ok {
		t.Error("campo real deveria estar no mapa")
	} else if val.(float64) != 42 {
		t.Errorf("esperado 42, got %v", val)
	}
}

func TestBuildExecutionCustomFieldCtx_IndexedByID(t *testing.T) {
	fields := []RuntimeCustomField{
		{DefinitionID: "uuid-abc", Name: "field_a", ValueJson: json.RawMessage(`"v"`), IsSecret: false},
	}
	ctx := buildExecutionCustomFieldCtx(fields)
	if _, ok := ctx.rawByID["uuid-abc"]; !ok {
		t.Error("campo deveria estar indexado por definitionId")
	}
}

// --- validateCollectedWrite ---

func TestValidateCollectedWrite_ByName_Allowed(t *testing.T) {
	fields := []RuntimeCustomField{
		{DefinitionID: "def-1", Name: "tv_id", ValueJson: json.RawMessage(`"TV1"`), IsSecret: false},
	}
	cfCtx := buildExecutionCustomFieldCtx(fields)
	name := "tv_id"
	req := CollectedValueRequest{Name: &name, Value: json.RawMessage(`"TV9"`)}
	if err := validateCollectedWrite(cfCtx, req); err != nil {
		t.Errorf("validação deveria passar, got: %v", err)
	}
}

func TestValidateCollectedWrite_ByID_Allowed(t *testing.T) {
	fields := []RuntimeCustomField{
		{DefinitionID: "def-99", Name: "tv_id", ValueJson: json.RawMessage(`"TV1"`), IsSecret: false},
	}
	cfCtx := buildExecutionCustomFieldCtx(fields)
	id := "def-99"
	req := CollectedValueRequest{DefinitionID: &id, Value: json.RawMessage(`"TV9"`)}
	if err := validateCollectedWrite(cfCtx, req); err != nil {
		t.Errorf("validação por ID deveria passar, got: %v", err)
	}
}

func TestValidateCollectedWrite_NotInCache(t *testing.T) {
	cfCtx := buildExecutionCustomFieldCtx(nil)
	name := "unknown_field"
	req := CollectedValueRequest{Name: &name, Value: json.RawMessage(`"v"`)}
	err := validateCollectedWrite(cfCtx, req)
	if err == nil {
		t.Fatal("deveria retornar erro para campo fora do cache")
	}
	writeErr, ok := err.(*ErrCustomFieldWrite)
	if !ok {
		t.Fatalf("esperado *ErrCustomFieldWrite, got %T", err)
	}
	if writeErr.Code != WriteErrorNotFound {
		t.Errorf("esperado WriteErrorNotFound, got %d", writeErr.Code)
	}
}

func TestValidateCollectedWrite_SecretBlocked(t *testing.T) {
	fields := []RuntimeCustomField{
		{DefinitionID: "def-s", Name: "secret_f", ValueJson: json.RawMessage(`"s"`), IsSecret: true},
	}
	cfCtx := buildExecutionCustomFieldCtx(fields)
	name := "secret_f"
	req := CollectedValueRequest{Name: &name, Value: json.RawMessage(`"new_val"`)}
	err := validateCollectedWrite(cfCtx, req)
	if err == nil {
		t.Fatal("deveria bloquear escrita de campo secreto")
	}
	writeErr := err.(*ErrCustomFieldWrite)
	if writeErr.Code != WriteErrorNotAllowed {
		t.Errorf("esperado WriteErrorNotAllowed, got %d", writeErr.Code)
	}
}

func TestValidateCollectedWrite_NilContext(t *testing.T) {
	name := "any"
	req := CollectedValueRequest{Name: &name, Value: json.RawMessage(`"v"`)}
	err := validateCollectedWrite(nil, req)
	if err == nil {
		t.Fatal("deveria retornar erro para contexto nil")
	}
}

// --- parseCollectedValues ---

func TestParseCollectedValues_Basic(t *testing.T) {
	output := `Iniciando script
MDZ_COLLECT: {"name":"tv_id","value":"TV123456789"}
Script concluido com sucesso`

	items, cleaned := parseCollectedValues(output)
	if len(items) != 1 {
		t.Fatalf("esperado 1 item, got %d", len(items))
	}
	if *items[0].Name != "tv_id" {
		t.Errorf("esperado name tv_id, got %q", *items[0].Name)
	}
	if strings.Contains(cleaned, "MDZ_COLLECT") {
		t.Error("linha MDZ_COLLECT nao deveria aparecer no output limpo")
	}
	if !strings.Contains(cleaned, "Iniciando script") {
		t.Error("output original deveria ser preservado")
	}
}

func TestParseCollectedValues_MultipleItems(t *testing.T) {
	output := `MDZ_COLLECT: {"name":"field_a","value":42}
MDZ_COLLECT: {"name":"field_b","value":true}
Done`

	items, _ := parseCollectedValues(output)
	if len(items) != 2 {
		t.Fatalf("esperado 2 itens, got %d", len(items))
	}
}

func TestParseCollectedValues_SkipsNullValue(t *testing.T) {
	output := `MDZ_COLLECT: {"name":"field_a","value":null}
MDZ_COLLECT: {"name":"field_b","value":"ok"}`

	items, _ := parseCollectedValues(output)
	if len(items) != 1 {
		t.Fatalf("esperado 1 item (null omitido), got %d", len(items))
	}
	if *items[0].Name != "field_b" {
		t.Errorf("esperado field_b, got %q", *items[0].Name)
	}
}

func TestParseCollectedValues_InvalidJSON_Skipped(t *testing.T) {
	output := `MDZ_COLLECT: not-json
MDZ_COLLECT: {"name":"good","value":"ok"}`

	items, _ := parseCollectedValues(output)
	if len(items) != 1 {
		t.Fatalf("esperado 1 item (json invalido ignorado), got %d", len(items))
	}
}

func TestParseCollectedValues_EmptyOutput(t *testing.T) {
	items, cleaned := parseCollectedValues("")
	if len(items) != 0 {
		t.Errorf("esperado 0 itens, got %d", len(items))
	}
	if cleaned != "" {
		t.Errorf("esperado output vazio, got %q", cleaned)
	}
}

// --- sanitizeCustomFieldErrForLog ---

func TestSanitizeCustomFieldErrForLog_RemovesJSON(t *testing.T) {
	err := &ErrCustomFieldWrite{Code: WriteErrorNotFound, Message: `campo nao encontrado {"definitionId":"def-1","value":"secret"}`}
	safe := sanitizeCustomFieldErrForLog(err)
	if strings.Contains(safe, "secret") {
		t.Error("log nao deve conter payload JSON com dados sensiveis")
	}
}

// --- loadCustomFieldsForExecution (integração com httptest) ---

func TestLoadCustomFieldsForExecution_Integration(t *testing.T) {
	fields := []RuntimeCustomField{
		{DefinitionID: "d1", Name: "hostname", ValueJson: json.RawMessage(`"server01"`), IsSecret: false},
	}
	body, _ := json.Marshal(fields)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	svc := NewService(func() RuntimeConfig {
		return RuntimeConfig{BaseURL: srv.URL, Token: "tok", AgentID: "ag-1"}
	}, func(s string) {})

	cfg := svc.getConfig()
	cfCtx := svc.loadCustomFieldsForExecution(context.Background(), cfg, "exec-test", "t1", "s1", "c1")

	if cfCtx == nil {
		t.Fatal("cfCtx nao deveria ser nil")
	}
	if val, ok := cfCtx.Fields["hostname"]; !ok || val != "server01" {
		t.Errorf("esperado hostname=server01, got: %v (ok=%v)", val, ok)
	}

	// Verifica que foi cacheado
	svc.cfMu.RLock()
	cached := svc.cfCache["exec-test"]
	svc.cfMu.RUnlock()
	if cached == nil {
		t.Error("contexto deveria estar no cache apos load")
	}

	// Limpa e confirma remoção
	svc.clearCustomFieldCtx("exec-test")
	svc.cfMu.RLock()
	afterClear := svc.cfCache["exec-test"]
	svc.cfMu.RUnlock()
	if afterClear != nil {
		t.Error("contexto deveria ser removido do cache apos clear")
	}
}

func TestLoadCustomFieldsForExecution_FallsBackOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc := NewService(func() RuntimeConfig {
		return RuntimeConfig{BaseURL: srv.URL, Token: "tok", AgentID: "ag-1"}
	}, func(s string) {})

	cfg := svc.getConfig()
	cfCtx := svc.loadCustomFieldsForExecution(context.Background(), cfg, "exec-fail", "", "", "")

	if cfCtx == nil {
		t.Fatal("cfCtx nao deveria ser nil mesmo com erro")
	}
	if len(cfCtx.Fields) != 0 {
		t.Error("Fields deveria ser vazio em fallback de erro")
	}
}

// --- buildCustomFieldsEnv ---

func TestBuildCustomFieldsEnv_NonEmpty(t *testing.T) {
	fields := map[string]any{"tv_id": "TV123", "count": 42}
	env := buildCustomFieldsEnv(fields)
	if len(env) != 1 {
		t.Fatalf("esperado 1 entrada de env, got %d", len(env))
	}
	if !strings.HasPrefix(env[0], "MDZ_CUSTOM_FIELDS=") {
		t.Errorf("entrada deve comecar com MDZ_CUSTOM_FIELDS=, got %q", env[0])
	}
	jsonPart := env[0][len("MDZ_CUSTOM_FIELDS="):]
	var decoded map[string]any
	if err := json.Unmarshal([]byte(jsonPart), &decoded); err != nil {
		t.Errorf("JSON invalido no env: %v", err)
	}
	if decoded["tv_id"] != "TV123" {
		t.Errorf("esperado tv_id=TV123, got %v", decoded["tv_id"])
	}
}

func TestBuildCustomFieldsEnv_Empty(t *testing.T) {
	env := buildCustomFieldsEnv(nil)
	if env != nil {
		t.Error("mapa vazio nao deveria gerar env")
	}
	env = buildCustomFieldsEnv(map[string]any{})
	if env != nil {
		t.Error("mapa sem entradas nao deveria gerar env")
	}
}
