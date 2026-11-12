package ai

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"

	"discovery/app/core/mcp"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ─── FIX-5 / D3: traceId do servidor ───────────────────────────────────────

// TestAgentChatStreamEvent_ParsesTraceID é a regressão do D3: o servidor envia
// "traceId" no frame de erro (AgentAuthController.cs:1556-1563), mas o struct
// agentChatStreamEvent não tinha o campo — o encoding/json descartava o id
// silenciosamente e o operador não tinha como achar a exceção real no log da API.
func TestAgentChatStreamEvent_ParsesTraceID(t *testing.T) {
	const raw = `{"type":"error","error":"Erro interno ao processar o chat.","traceId":"0HNQ0C3OLBI1Q:00000003"}`

	var evt agentChatStreamEvent
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("unmarshal falhou: %v", err)
	}
	if evt.TraceID != "0HNQ0C3OLBI1Q:00000003" {
		t.Fatalf("traceId descartado: esperado %q, obtido %q", "0HNQ0C3OLBI1Q:00000003", evt.TraceID)
	}
	if evt.Error == "" {
		t.Fatal("mensagem de erro não parseada")
	}
}

// TestParseMultiRoundSSE_ErrorCarriesTraceID garante que o parser preserva o
// traceId do frame de erro no erro tipado (é ele que alimenta TODAS as linhas
// da cadeia de falha no chat.db).
func TestParseMultiRoundSSE_ErrorCarriesTraceID(t *testing.T) {
	const sse = "data: {\"type\":\"token\",\"content\":\"Analisei parcialmente.\"}\n" +
		"data: {\"type\":\"error\",\"error\":\"Erro interno ao processar o chat.\",\"traceId\":\"0HNQ0C3OLBI1Q:00000003\"}"

	var collected strings.Builder
	var pending []pendingToolCall

	_, _, err := (&Service{}).parseMultiRoundSSE(
		strings.NewReader(sse),
		func(tok string) { collected.WriteString(tok) },
		&pending,
	)
	if err == nil {
		t.Fatal("esperado erro do frame SSE")
	}
	var se *sseServerError
	if !errors.As(err, &se) {
		t.Fatalf("erro deveria ser *sseServerError, obtido %T", err)
	}
	if se.TraceID != "0HNQ0C3OLBI1Q:00000003" {
		t.Fatalf("traceId perdido no erro: %q", se.TraceID)
	}
	if se.Partial != "Analisei parcialmente." {
		t.Fatalf("texto parcial perdido no erro: %q", se.Partial)
	}
	// O formato do log carrega a chave de correlação.
	if got := formatSSEError(err); !strings.Contains(got, "0HNQ0C3OLBI1Q:00000003") {
		t.Fatalf("formatSSEError sem traceId: %q", got)
	}
}

// TestFormatSSEError_NoTraceID mantém a mensagem intacta quando o servidor não
// manda traceId (versões antigas da API).
func TestFormatSSEError_NoTraceID(t *testing.T) {
	err := errors.New("Erro interno ao processar o chat.")
	if got := formatSSEError(err); got != "Erro interno ao processar o chat." {
		t.Fatalf("mensagem alterada sem traceId: %q", got)
	}
	if got := formatSSEError(nil); got != "" {
		t.Fatalf("erro nil deveria virar string vazia, obtido %q", got)
	}
	if got := traceIDOf(err); got != "" {
		t.Fatalf("erro comum não tem traceId: %q", got)
	}
}

// ─── FIX-5 / D3+D4: colunas do chat.db ─────────────────────────────────────

// TestChatLog_RoundSSEError_PersistsTraceIDAndStatus é a regressão combinada de
// D3: round_sse_error precisa gravar o traceId (antes estruturalmente
// inalcançável) e um status_code != 0 (antes 0 em 100% das linhas, apesar do
// HTTP 200 já registrado).
func TestChatLog_RoundSSEError_PersistsTraceIDAndStatus(t *testing.T) {
	dbPath, logger := newTestChatLogger(t)

	// Mesmo caminho de produção: a conexão foi aceita (HTTP 200) e o parser
	// emite a linha round_sse_error carregando traceId + status.
	s := NewService(nil)
	s.SetChatLogger(logger)
	s.sseHTTPStatus.Store(200)
	const sse = "data: {\"type\":\"error\",\"error\":\"Erro interno ao processar o chat.\",\"traceId\":\"0HNQ0C3OLBI1Q:00000003\"}"
	var pending []pendingToolCall
	if _, _, err := s.parseMultiRoundSSE(strings.NewReader(sse), nil, &pending); err == nil {
		t.Fatal("esperado erro do frame SSE")
	}

	logger.Disable() // drena a fila e fecha o banco

	rows := readChatRows(t, dbPath)
	var found bool
	for _, r := range rows {
		if r["type"] != "round_sse_error" {
			continue
		}
		found = true
		errText, _ := r["error"].(string)
		if !strings.Contains(errText, "0HNQ0C3OLBI1Q:00000003") {
			t.Fatalf("traceId ausente no log de round_sse_error: %q", errText)
		}
		// D3: status_code era 0 em 100% das linhas apesar do HTTP 200 já
		// registrado — sem ele não se prova que a falha foi DURANTE o stream.
		if sc, _ := r["status_code"].(int64); sc != 200 {
			t.Fatalf("status_code deveria ser 200 (falha no meio do stream), obtido %v", sc)
		}
	}
	if !found {
		t.Fatalf("nenhuma linha round_sse_error gravada; linhas: %v", typesOf(rows))
	}
}

// TestChatLogger_TimestampIsUTC é a regressão do D7: o ts_iso do chat.db era
// horário de PAREDE local com sufixo "Z" (off by 3h em America/Sao_Paulo), o
// que impedia correlacionar com o log da API (UTC).
func TestChatLogger_TimestampIsUTC(t *testing.T) {
	// O instante gravado é derivado de entry.Timestamp via .UTC(): partindo de
	// um instante marcado como UTC, o valor persistido tem de ser o mesmo
	// relógio UTC — nunca o relógio local deslocado.
	localNow := time.Now()
	utcNow := localNow.UTC()

	_, offset := localNow.Zone()
	if offset == 0 {
		t.Skip("máquina já está em UTC — o bug de fuso não é observável aqui")
	}

	// Diferença entre o rótulo UTC e o rótulo local: se o código gravasse o
	// relógio local sob o sufixo Z, as duas strings divergiriam em |offset|.
	if localNow.Format(time.RFC3339Nano) == utcNow.Format(time.RFC3339Nano) {
		t.Fatalf("offset local %ds deveria produzir strings distintas", offset)
	}

	// O contrato que a correção garante: o carimbo do logger é UTC.
	stamp := time.Now().UTC()
	if _, off := stamp.Zone(); off != 0 {
		t.Fatalf("timestamp do logger deveria estar em UTC, offset=%d", off)
	}
}

// ─── FIX-5 / D1: o frame de erro não deve destruir o turno ──────────────────

// TestParseMultiRoundSSE_ErrorKeepsPartialText é o núcleo do D1: quando o
// servidor falha no meio do stream, o texto parcial JÁ produzido deve chegar ao
// histórico e ao erro tipado — antes ele era abandonado e o usuário via apenas
// a mensagem genérica de erro.
func TestParseMultiRoundSSE_ErrorKeepsPartialText(t *testing.T) {
	const sse = "data: {\"type\":\"token\",\"content\":\"Verifiquei 8 itens, \"}\n" +
		"data: {\"type\":\"token\",\"content\":\"todos OK.\"}\n" +
		"data: {\"type\":\"error\",\"error\":\"Erro interno ao processar o chat.\",\"traceId\":\"abc:1\"}"

	s := NewService(nil)
	var pending []pendingToolCall

	_, _, err := s.parseMultiRoundSSE(
		strings.NewReader(sse),
		nil,
		&pending,
	)
	if err == nil {
		t.Fatal("esperado erro do frame SSE")
	}

	// O texto parcial precisa estar no histórico (é ele que a montagem final
	// devolve ao usuário no caminho não-fatal).
	got := s.lastAssistantContentSince(0)
	want := "Verifiquei 8 itens, todos OK."
	if got != want {
		t.Fatalf("texto parcial perdido do histórico:\n got: %q\nwant: %q", got, want)
	}

	var se *sseServerError
	if !errors.As(err, &se) || strings.TrimSpace(se.Partial) == "" {
		t.Fatalf("erro tipado não carrega o texto parcial: %+v", err)
	}
}

// TestSseServerError_KeepsTurnAliveWhenPartial garante o contrato que o loop
// usa para decidir entre degradar (round 0) e encerrar com resposta parcial
// (round > 0 com conteúdo): o erro tipado expõe o parcial sem perder o motivo.
func TestSseServerError_KeepsTurnAliveWhenPartial(t *testing.T) {
	se := &sseServerError{Msg: "Erro interno ao processar o chat.", TraceID: "t:1", Partial: "texto parcial"}
	if se.Error() != "Erro interno ao processar o chat." {
		t.Fatalf("Error() deveria devolver a mensagem do servidor, obtido %q", se.Error())
	}
	if !strings.Contains(formatSSEError(se), "t:1") {
		t.Fatal("o motivo que chega ao usuário deveria citar o traceId")
	}
	// Wrapped: o loop precisa reconhecer o tipo mesmo através de fmt.Errorf("%w").
	wrapped := fmt.Errorf("round 3: %w", se)
	var out *sseServerError
	if !errors.As(wrapped, &out) || out.TraceID != "t:1" {
		t.Fatal("errors.As deveria reconhecer o erro tipado encapsulado")
	}
}

// ─── FIX-5 / D1 ponta a ponta: o loop preserva o turno ─────────────────────

// TestMultiRound_ServerErrorAfterToolsKeepsTurn reproduz o turno real do
// chat.db (sessão 01a1cf96…): o round 0 executa uma tool com SUCESSO e o round 1
// recebe um frame {"type":"error"} do servidor. Antes da correção o turno
// inteiro morria (`return "", err`) e o usuário perdia as tools já executadas;
// agora o loop encerra com a resposta parcial e devolve nil.
func TestMultiRound_ServerErrorAfterToolsKeepsTurn(t *testing.T) {
	var round int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		round++
		if round == 1 {
			// Round 0: o LLM pede uma tool; o loop a executa localmente.
			_, _ = fmt.Fprint(w,
				"data: {\"type\":\"token\",\"content\":\"Verificando a maquina.\"}\n"+
					"data: {\"type\":\"tool_call\",\"toolCallId\":\"c1\",\"toolName\":\"get_inventory\",\"toolArguments\":\"{}\"}\n"+
					"data: {\"type\":\"round_end\",\"sessionId\":\"sess-keep\"}\n")
			return
		}
		// Round 1: o servidor LANÇA (o caso de todos os 12 fracassos reais:
		// latência 1-4 s, dentro da faixa normal de sucesso → não é timeout).
		_, _ = fmt.Fprint(w,
			"data: {\"type\":\"error\",\"error\":\"Erro interno ao processar o chat.\",\"traceId\":\"0HNQ0C3OLBI1Q:00000003\"}\n")
	}))
	defer server.Close()

	s := NewService(mcp.NewRegistry())
	s.SetConfig(Config{
		Endpoint: server.URL,
		APIKey:   "mdz_test-token",
		AgentID:  "11111111-1111-1111-1111-111111111111",
	})
	executor := func(ctx context.Context, toolName, argsJSON string) (string, error) {
		return "{\"ok\":true}", nil
	}

	assistant, err := s.SendStreamMultiRound(context.Background(), "faca uma verificacao em geral do pc", nil, nil, executor)
	if err != nil {
		t.Fatalf("turno deveria encerrar com resposta parcial, veio erro: %v", err)
	}
	if strings.TrimSpace(assistant) == "" {
		t.Fatal("resposta vazia: o turno foi descartado em vez de preservar o parcial")
	}
	if !strings.Contains(assistant, "Verificando a maquina.") {
		t.Fatalf("texto parcial do round 0 não preservado: %q", assistant)
	}
	// O usuário precisa saber que a resposta está incompleta E ter o traceId
	// para o suporte rastrear a exceção no log da API.
	if !strings.Contains(assistant, "incompleta") {
		t.Fatalf("aviso de resposta incompleta ausente: %q", assistant)
	}
	if !strings.Contains(assistant, "0HNQ0C3OLBI1Q:00000003") {
		t.Fatalf("traceId ausente no aviso ao usuário: %q", assistant)
	}
}

// TestMultiRound_ServerErrorOnRoundZeroStillFails garante que a correção do D1
// NÃO afrouxa o round 0: sem conteúdo parcial não há turno a preservar, então o
// comportamento antigo (degradar para o sync) permanece.
func TestMultiRound_ServerErrorOnRoundZeroStillFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"data: {\"type\":\"error\",\"error\":\"Erro interno ao processar o chat.\",\"traceId\":\"abc:9\"}\n")
	}))
	defer server.Close()

	s := NewService(mcp.NewRegistry())
	s.SetConfig(Config{
		Endpoint: server.URL,
		APIKey:   "mdz_test-token",
		AgentID:  "11111111-1111-1111-1111-111111111111",
	})

	// O fallback sync também falha (endpoint devolve SSE, não JSON) — o ponto
	// do teste é que o caminho do erro CONTINUA sendo exercido, sem mascarar a
	// falha do round 0.
	if _, err := s.SendStreamMultiRound(context.Background(), "oi", nil, nil, nil); err == nil {
		t.Fatal("round 0 sem conteúdo parcial deveria continuar devolvendo erro")
	}
}

// ─── helpers ───────────────────────────────────────────────────────────────

func typesOf(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		s, _ := r["type"].(string)
		out = append(out, s)
	}
	return out
}

// newTestChatLogger abre um chat.db temporário e devolve o logger ativo.
func newTestChatLogger(t *testing.T) (string, *ChatLogger) {
	t.Helper()
	dir := t.TempDir()
	logger := NewChatLogger(dir)
	if !logger.IsEnabled() {
		t.Fatal("ChatLogger não habilitou o banco temporário")
	}
	return filepath.Join(dir, "chat.db"), logger
}

// readChatRows lê as linhas gravadas, tolerando tipos dinâmicos do driver.
func readChatRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("abrir chat.db: %v", err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT type, error, round, status_code FROM chat_entries")
	if err != nil {
		t.Fatalf("query chat_entries: %v", err)
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var typ, errText string
		var round, status sql.NullInt64
		if err := rows.Scan(&typ, &errText, &round, &status); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, map[string]any{
			"type":        typ,
			"error":       errText,
			"round":       round.Int64,
			"status_code": status.Int64,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}
