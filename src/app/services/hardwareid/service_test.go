package hardwareid

import (
	"encoding/json"
	"testing"
)

func TestGetReturnsCachedInfo(t *testing.T) {
	logs := 0
	s := New(Deps{Logf: func(string) { logs++ }})
	first := s.Get()
	second := s.Get()
	// Cache: a segunda chamada devolve exatamente o mesmo valor coletado
	// (o hardware não muda em runtime).
	if first != second {
		t.Fatalf("Get deveria retornar valor cacheado: %+v != %+v", first, second)
	}
}

func TestInfoJSONShape(t *testing.T) {
	info := Info{
		TPMEK:               "abcdef",
		TPMEKAvailable:      true,
		TPMEKAlg:            "RSA",
		SMBIOSUUID:          "uuid-1",
		SMBIOSUUIDAvailable: true,
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"tpmEk", "tpmEkAvailable", "tpmEkAlg", "smbiosUuid", "smbiosUuidAvailable"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("campo JSON %s ausente em %s", key, string(raw))
		}
	}
	// Campo de erro é omitempty: não deve aparecer sem erro.
	if _, ok := decoded["tpmEkError"]; ok {
		t.Fatalf("tpmEkError deveria ser omitido sem erro: %s", string(raw))
	}
}
