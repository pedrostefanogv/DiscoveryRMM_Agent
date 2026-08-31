package ai

import (
	"strings"
	"testing"
)

// TestParseMultiRoundSSE_PreservesTokenWhitespace é regressão do bug de
// 2026-08-30: o TrimSpace na linha SSE e no conteúdo do token apagava
// espaços/quebras de linha legítimos nas fronteiras dos tokens, produzindo
// textos como "apenas23 MB", "de1 GB" e linhas de tabela markdown coladas
// ("|6307s|23 MB|"), quebrando a renderização no frontend.
func TestParseMultiRoundSSE_PreservesTokenWhitespace(t *testing.T) {
	// Tokens fragmentados como o servidor emite (char-a-char / palavra-a-palavra).
	const sse = "data: {\"type\":\"token\",\"content\":\"CPU acumulada de \"}\n" +
		"data: {\"type\":\"token\",\"content\":\"6307s e apenas \"}\n" +
		"data: {\"type\":\"token\",\"content\":\"23 MB de memória.\\n\\n\"}\n" +
		"data: {\"type\":\"token\",\"content\":\"| Processo | CPU |\\n\"}\n" +
		"data: {\"type\":\"token\",\"content\":\"|---|---|\\n\"}\n" +
		"data: {\"type\":\"token\",\"content\":\"| avp | 3526s |\\n\"}\n" +
		"data: {\"type\":\"done\",\"sessionId\":\"s-ws\"}"

	var collected strings.Builder
	var pending []pendingToolCall

	sessionID, done, err := (&Service{}).parseMultiRoundSSE(
		strings.NewReader(sse),
		func(tok string) { collected.WriteString(tok) },
		&pending,
	)
	if err != nil {
		t.Fatalf("parseMultiRoundSSE erro: %v", err)
	}
	if !done {
		t.Errorf("esperado done=true")
	}
	if sessionID != "s-ws" {
		t.Errorf("sessionID: esperado s-ws, obteve %q", sessionID)
	}

	got := collected.String()
	want := "CPU acumulada de 6307s e apenas 23 MB de memória.\n\n| Processo | CPU |\n|---|---|\n| avp | 3526s |\n"
	if got != want {
		t.Fatalf("conteúdo com whitespace corrompido:\n got: %q\nwant: %q", got, want)
	}
	// Verificações explícitas dos sintomas do bug.
	if strings.Contains(got, "apenas23") || strings.Contains(got, "de6307s") {
		t.Errorf("espaços entre palavras perdidos: %q", got)
	}
	if strings.Contains(got, "|Processo|") || strings.Contains(got, "|3526s|") {
		t.Errorf("espaços das células da tabela perdidos: %q", got)
	}
}

// TestParseSSEStream_PreservesTokenWhitespace cobre o mesmo cenário no parser
// do stream single-round (parseSSEStream).
func TestParseSSEStream_PreservesTokenWhitespace(t *testing.T) {
	const sse = "data: {\"type\":\"token\",\"content\":\"Uso de \"}\n" +
		"data: {\"type\":\"token\",\"content\":\"memória: \"}\n" +
		"data: {\"type\":\"token\",\"content\":\"627 MB\\n\"}\n" +
		"data: {\"type\":\"done\",\"sessionId\":\"s-ws2\"}"

	var collected strings.Builder

	content, sessionID, hasToken, err := (&Service{}).parseSSEStream(
		strings.NewReader(sse),
		func(tok string) { collected.WriteString(tok) },
	)
	if err != nil {
		t.Fatalf("parseSSEStream erro: %v", err)
	}
	if !hasToken {
		t.Errorf("esperado hasToken=true")
	}
	if sessionID != "s-ws2" {
		t.Errorf("sessionID: esperado s-ws2, obteve %q", sessionID)
	}

	want := "Uso de memória: 627 MB\n"
	if collected.String() != want {
		t.Fatalf("conteúdo com whitespace corrompido:\n got: %q\nwant: %q", collected.String(), want)
	}
	if content != want {
		t.Fatalf("contentBuf diverge do onToken:\n got: %q\nwant: %q", content, want)
	}
}

// TestParseMultiRoundSSE_TrailingSpaceInToken garante que um token que TERMINA
// com espaço (fronteira típica entre palavras) não é aparado.
func TestParseMultiRoundSSE_TrailingSpaceInToken(t *testing.T) {
	const sse = "data: {\"type\":\"token\",\"content\":\"Olá \"}\n" +
		"data: {\"type\":\"token\",\"content\":\"mundo \"}\n" +
		"data: {\"type\":\"token\",\"content\":\"!\"}\n" +
		"data: {\"type\":\"done\"}"

	var collected strings.Builder
	var pending []pendingToolCall

	_, _, err := (&Service{}).parseMultiRoundSSE(
		strings.NewReader(sse),
		func(tok string) { collected.WriteString(tok) },
		&pending,
	)
	if err != nil {
		t.Fatalf("parseMultiRoundSSE erro: %v", err)
	}
	if got := collected.String(); got != "Olá mundo !" {
		t.Fatalf("esperado %q, obteve %q", "Olá mundo !", got)
	}
}
