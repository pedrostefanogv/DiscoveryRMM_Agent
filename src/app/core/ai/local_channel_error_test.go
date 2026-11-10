package ai

// Testes do reconhecimento do envelope de erro LOCAL do canal IPC (P2.9/P2.11
// no lado do chat). O caso que estes testes travam é o do incidente de
// 2026-10-17: erro do canal admin chegava como texto cru, o modelo concluía
// "servidor fora do ar" e repetia a mutação 3 vezes.

import (
	"errors"
	"testing"
)

func envelope(scope string, retryable bool) string {
	rt := "false"
	if retryable {
		rt = "true"
	}
	return `{"error":{"code":"LOCAL_IPC_WRITE_UNAVAILABLE","scope":"` + scope +
		`","retryable":` + rt + `,"detail":"canal de gravação local indisponível (pipe \\.\\pipe\\discovery-agent-ipc-admin): erro"}}`
}

// O caso do incidente: scope=local_agent + retryable=false tem de PARAR o loop.
func TestLocalWriteFailureNotRetryable_LocalAgent(t *testing.T) {
	if !localWriteFailureNotRetryable(envelope("local_agent", false)) {
		t.Fatal("envelope local_agent/retryable=false deveria interromper o loop")
	}
}

// Espaços/quebras de linha na frente do JSON não podem esconder o envelope.
func TestLocalWriteFailureNotRetryable_ToleraEspacos(t *testing.T) {
	if !localWriteFailureNotRetryable("  \n\t" + envelope("local_agent", false)) {
		t.Fatal("envelope com espaços na frente deveria continuar sendo reconhecido")
	}
}

// Um erro retentável NÃO pode abortar o turno: o modelo precisa poder corrigir
// argumentos e tentar de novo.
func TestLocalWriteFailureNotRetryable_NaoAbortaQuandoRetentavel(t *testing.T) {
	if localWriteFailureNotRetryable(envelope("local_agent", true)) {
		t.Fatal("retryable=true não pode interromper o loop")
	}
}

// Falha reportada pelo SERVIDOR de suporte (scope remoto) segue o fluxo normal.
func TestLocalWriteFailureNotRetryable_EscopoRemoto(t *testing.T) {
	if localWriteFailureNotRetryable(envelope("remote_service", false)) {
		t.Fatal("escopo remoto não pode ser tratado como falha local do agente")
	}
}

// Resultados de sucesso, texto cru e JSON quebrado nunca abortam o loop.
func TestLocalWriteFailureNotRetryable_ResultadosNormais(t *testing.T) {
	cases := map[string]string{
		"sucesso":            `{"ok":true,"data":{"ticketId":"123"}}`,
		"erro texto cru":     "canal admin indisponível (serviço sem autenticação?)",
		"string error":       `{"error":"parametro obrigatorio ausente"}`,
		"json quebrado":      `{"error":{`,
		"error sem scope":    `{"error":{"code":"X"}}`,
		"vazio":              "",
		"array":              `[1,2,3]`,
		"erro dentro de erro": `{"error":{"code":"Y","scope":"local_agent"}}`,
	}
	for name, result := range cases {
		if localWriteFailureNotRetryable(result) {
			t.Fatalf("%s: não pode interromper o loop (result=%q)", name, result)
		}
	}
}

// Sem scope/retryable EXPLÍCITOS o parser recusa o envelope — a decisão de
// abortar o turno não pode sair de heurística de texto.
func TestParseLocalChannelError_ExigeCampos(t *testing.T) {
	if _, ok := parseLocalChannelError(`{"error":{"code":"A"}}`); ok {
		t.Fatal("envelope sem scope não deveria ser aceito")
	}
	if _, ok := parseLocalChannelError(`{"error":{"scope":"local_agent"}}`); ok {
		t.Fatal("envelope sem code não deveria ser aceito")
	}
	body, ok := parseLocalChannelError(envelope("local_agent", false))
	if !ok {
		t.Fatal("envelope completo deveria ser aceito")
	}
	if body.Code != LocalChannelCodeWriteUnavailable || body.Scope != LocalChannelScopeAgent {
		t.Fatalf("campos lidos errado: %+v", body)
	}
	if body.Retryable == nil || *body.Retryable {
		t.Fatal("retryable deveria ser lido como false")
	}
}

// O resolvedor tipado registrado pelo pacote app tem precedência — é ele que
// preserva o envelope REAL em vez de reconstruí-lo.
func TestLocalChannelErrorEnvelope_UsaResolverRegistrado(t *testing.T) {
	original := localChannelErrorResolver
	defer func() { localChannelErrorResolver = original }()

	sentinel := errors.New("erro tipado")
	RegisterLocalChannelErrorResolver(func(err error) (string, bool) {
		if errors.Is(err, sentinel) {
			return envelope("local_agent", false), true
		}
		return "", false
	})

	got, ok := localChannelErrorEnvelope(sentinel)
	if !ok || got == "" {
		t.Fatal("o resolvedor registrado deveria produzir o envelope")
	}
	if _, ok := localChannelErrorEnvelope(errors.New("outro")); ok {
		t.Fatal("erro não reconhecido pelo resolvedor não pode virar envelope")
	}
	if _, ok := localChannelErrorEnvelope(nil); ok {
		t.Fatal("nil não pode virar envelope")
	}
}
