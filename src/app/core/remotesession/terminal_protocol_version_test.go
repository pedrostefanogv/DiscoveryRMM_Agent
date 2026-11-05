//go:build windows

package remotesession

import (
	"encoding/json"
	"testing"
)

// ── Versão de protocolo do canal de terminal (pv) ──
//
// O handshake term.in aceita um campo aditivo "pv". A regra é:
//   - ausente/0  → viewer antigo, COMPATÍVEL (não pode ser quebrado);
//   - == agente  → compatível;
//   - >  agente  → incompatível, recusado com erro EXPLÍCITO (não silencioso).

func TestTermProtocolVersionValue(t *testing.T) {
	// Guarda-corpo: mudar a versão é uma decisão de contrato. Se este teste
	// quebrar, atualize também o viewer (useTerminalStream.ts) e este teste.
	if termProtocolVersion != 1 {
		t.Fatalf("termProtocolVersion = %d, want 1 (atualize o viewer e este teste)", termProtocolVersion)
	}
}

// parseTermIn espelha o struct de decodificação usado no handler de term.in,
// garantindo que os nomes de campo JSON ("pv") são os esperados no wire.
func parseTermIn(t *testing.T, raw string) (pv int, isHello bool, lastSeq *int64) {
	t.Helper()
	var req struct {
		Data    string `json:"data"`
		Cols    int    `json:"cols"`
		Rows    int    `json:"rows"`
		Hello   bool   `json:"hello"`
		LastSeq *int64 `json:"lastSeq"`
		Pv      int    `json:"pv"`
	}
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return req.Pv, req.Hello, req.LastSeq
}

// Viewer ANTIGO (sem "pv") deve continuar sendo aceito — é o requisito de
// compatibilidade: clientes em produção não podem quebrar com o campo novo.
func TestTermProtocolVersionLegacyViewerIsCompatible(t *testing.T) {
	pv, isHello, lastSeq := parseTermIn(t, `{"hello":true,"lastSeq":42,"cols":120,"rows":40}`)
	if pv != 0 {
		t.Fatalf("pv ausente deveria decodificar como 0, got %d", pv)
	}
	if pv > termProtocolVersion {
		t.Fatal("viewer legado (pv=0) NÃO pode ser considerado incompatível")
	}
	if !isHello {
		t.Fatal("hello deveria ter sido reconhecido")
	}
	if lastSeq == nil || *lastSeq != 42 {
		t.Fatalf("lastSeq = %v, want 42", lastSeq)
	}
}

func TestTermProtocolVersionCurrentViewerIsCompatible(t *testing.T) {
	pv, isHello, _ := parseTermIn(t, `{"hello":true,"pv":1,"cols":120,"rows":40}`)
	if pv != termProtocolVersion {
		t.Fatalf("pv = %d, want %d", pv, termProtocolVersion)
	}
	if pv > termProtocolVersion {
		t.Fatal("mesma versão do agente deve ser compatível")
	}
	if !isHello {
		t.Fatal("hello deveria ter sido reconhecido")
	}
}

// Viewer MAIS NOVO que o agente: incompatível. O handler publica erro explícito
// em vez de ignorar o campo novo e deixar o viewer esperando um comportamento
// que este agente não tem (falha silenciosa observada no TacticalRMM).
func TestTermProtocolVersionNewerViewerIsRejected(t *testing.T) {
	pv, _, _ := parseTermIn(t, `{"hello":true,"pv":99,"cols":120,"rows":40}`)
	if pv <= termProtocolVersion {
		t.Fatalf("pv = %d deveria exceder a versão do agente (%d)", pv, termProtocolVersion)
	}
}

// O frame de erro precisa ser publicável e trazer os campos que o front usa
// (error=true → banner de erro; code/reason para diagnóstico).
func TestPublishProtocolMismatchPayloadShape(t *testing.T) {
	// Constrói o MESMO payload do helper (o helper exige um NatsStreamHandler
	// conectado; aqui validamos só o formato que o front consome).
	payload, mErr := json.Marshal(map[string]any{
		"error":  true,
		"code":   "protocol_version_mismatch",
		"reason": "viewer protocol v99 is newer than agent v1; update the agent",
		"pv":     termProtocolVersion,
	})
	if mErr != nil {
		t.Fatalf("marshal: %v", mErr)
	}

	var decoded struct {
		Error  bool   `json:"error"`
		Code   string `json:"code"`
		Reason string `json:"reason"`
		Pv     int    `json:"pv"`
	}
	if uErr := json.Unmarshal(payload, &decoded); uErr != nil {
		t.Fatalf("unmarshal: %v", uErr)
	}
	if !decoded.Error {
		t.Fatal("o frame de erro precisa ter error=true para o front exibir o banner")
	}
	if decoded.Code != "protocol_version_mismatch" {
		t.Fatalf("code = %q, want protocol_version_mismatch", decoded.Code)
	}
	if decoded.Reason == "" {
		t.Fatal("reason não pode ser vazio (diagnóstico do operador)")
	}
	if decoded.Pv != termProtocolVersion {
		t.Fatalf("pv do frame = %d, want %d", decoded.Pv, termProtocolVersion)
	}
}
