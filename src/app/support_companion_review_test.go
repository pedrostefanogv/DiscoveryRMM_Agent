package app

import (
	"encoding/json"
	"errors"
	"testing"

	appsupport "discovery/app/support"
)

// B5: envelope RPC sem \"data\" é erro de protocolo, não zero silencioso.
func TestDecodeIPCSupportResult_MissingData(t *testing.T) {
	if _, err := decodeIPCSupportResult("support:tickets", map[string]any{}); err == nil {
		t.Fatal("envelope sem data deveria retornar erro")
	}
	resp := map[string]any{
		"ok":   true,
		"data": map[string]any{"result": map[string]any{"tickets": []any{map[string]any{"id": "t1"}}}},
	}
	raw, err := decodeIPCSupportResult("support:tickets", resp)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var out appsupport.SupportTicketList
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Tickets) != 1 || out.Tickets[0].ID != "t1" {
		t.Fatalf("payload inesperado: %+v", out)
	}
}

// B7: o serviço antigo devolve \"método desconhecido\" e o bridge deve cair no
// caminho local em vez de propagar o erro.
func TestIsUnknownIPCMethod(t *testing.T) {
	if !isUnknownIPCMethod(errors.New("ipc request support:tickets falhou: método desconhecido: support:tickets")) {
		t.Fatal("método desconhecido deveria ser reconhecido")
	}
	if isUnknownIPCMethod(errors.New("ipc request support:tickets timeout (30s)")) {
		t.Fatal("timeout não pode ser tratado como protocolo antigo")
	}
	if isUnknownIPCMethod(nil) {
		t.Fatal("nil não é método desconhecido")
	}
}
