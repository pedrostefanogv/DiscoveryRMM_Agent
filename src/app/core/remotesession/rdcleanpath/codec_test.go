package rdcleanpath

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestEncodeVersionOnly verifica o encoding minimo: SEQUENCE { [0] INTEGER 3390 }.
// Esperado: 30 05 a0 03 02 01 0a 4e  (3390 = 0x0D3E -> precisa de 2 bytes: 0d 3e)
func TestEncodeVersionOnly(t *testing.T) {
	p := &Pdu{Version: Version1}
	der, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// 3390 = 0x0D3E -> INTEGER de 2 bytes, sem zero a esquerda (0x0d < 0x80).
	// Estrutura: SEQUENCE(30) { [0](a0) { INTEGER(02) 0d 3e } }
	want := []byte{0x30, 0x06, 0xa0, 0x04, 0x02, 0x02, 0x0d, 0x3e}
	if !bytes.Equal(der, want) {
		t.Fatalf("encoding divergente\n got: %s\nwant: %s", hex.EncodeToString(der), hex.EncodeToString(want))
	}
}

// TestVersionValue confirma que VERSION_1 = 3390 (BASE 3389 + 1), como no IronRDP.
func TestVersionValue(t *testing.T) {
	if Version1 != 3390 {
		t.Fatalf("Version1 = %d, esperado 3390", Version1)
	}
	if BASEVersion != 3389 {
		t.Fatalf("BASEVersion = %d, esperado 3389", BASEVersion)
	}
}

// TestRoundTripRequest cobre o request client->proxy com todos os campos que o
// IronRDP envia: version, destination, proxy_auth, preconnection_blob, x224.
func TestRoundTripRequest(t *testing.T) {
	x224 := []byte{0x03, 0x00, 0x00, 0x13, 0x0e, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x08, 0x00, 0x03, 0x00, 0x00, 0x00}
	pcb := "5c./myapp"
	p := NewRequest(x224, "127.0.0.1:3389", "token-abc", &pcb)

	der, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	got, err := Decode(der)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if got.Version != Version1 {
		t.Errorf("Version = %d", got.Version)
	}
	if got.Destination == nil || *got.Destination != "127.0.0.1:3389" {
		t.Errorf("Destination = %v", got.Destination)
	}
	if got.ProxyAuth == nil || *got.ProxyAuth != "token-abc" {
		t.Errorf("ProxyAuth = %v", got.ProxyAuth)
	}
	if got.PreconnectionBlob == nil || *got.PreconnectionBlob != pcb {
		t.Errorf("PreconnectionBlob = %v", got.PreconnectionBlob)
	}
	if !bytes.Equal(got.X224ConnectionPdu, x224) {
		t.Errorf("X224 = %s", hex.EncodeToString(got.X224ConnectionPdu))
	}
	// Campos proxy->client nao devem aparecer no request.
	if got.ServerCertChain != nil {
		t.Error("ServerCertChain deveria estar ausente no request")
	}
	if got.Error != nil {
		t.Error("Error deveria estar ausente no request")
	}
}

// TestRoundTripResponse cobre o response proxy->client, incluindo a cadeia de
// certificados (SEQUENCE OF OCTET STRING dentro do tag explicito [7]).
func TestRoundTripResponse(t *testing.T) {
	x224resp := []byte{0x03, 0x00, 0x00, 0x13, 0x0e, 0xd0, 0x00, 0x00, 0x12, 0x34, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	cert1 := bytes.Repeat([]byte{0xAA}, 300) // > 127 bytes forca length multi-byte
	cert2 := bytes.Repeat([]byte{0xBB}, 5)
	p := NewResponse("127.0.0.1:3389", x224resp, [][]byte{cert1, cert2})

	der, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(der)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if !bytes.Equal(got.X224ConnectionPdu, x224resp) {
		t.Errorf("X224 response divergente")
	}
	if len(got.ServerCertChain) != 2 {
		t.Fatalf("cert chain len = %d, esperado 2", len(got.ServerCertChain))
	}
	if !bytes.Equal(got.ServerCertChain[0], cert1) {
		t.Errorf("cert1 divergente")
	}
	if !bytes.Equal(got.ServerCertChain[1], cert2) {
		t.Errorf("cert2 divergente")
	}
	if got.ServerAddr == nil || *got.ServerAddr != "127.0.0.1:3389" {
		t.Errorf("ServerAddr = %v", got.ServerAddr)
	}
}

// TestRoundTripError cobre o Error PDU com http_status_code.
func TestRoundTripError(t *testing.T) {
	codes := []uint16{ErrGeneralError, ErrNegotiationError, ErrExpired, ErrTargetUnreachable, ErrDNSResolutionFailed, ErrTLSHandshakeFailed}
	for _, c := range codes {
		var http uint16 = 502
		p := NewErrorResponse(c, &http)
		der, err := p.Encode()
		if err != nil {
			t.Fatalf("Encode code %d: %v", c, err)
		}
		got, err := Decode(der)
		if err != nil {
			t.Fatalf("Decode code %d: %v", c, err)
		}
		if got.Error == nil {
			t.Fatalf("code %d: Error ausente", c)
		}
		if got.Error.ErrorCode != c {
			t.Errorf("ErrorCode = %d, esperado %d", got.Error.ErrorCode, c)
		}
		if got.Error.HTTPStatusCode == nil || *got.Error.HTTPStatusCode != 502 {
			t.Errorf("code %d: HTTPStatusCode = %v", c, got.Error.HTTPStatusCode)
		}
	}
}

// TestDecodeRejectsWrongVersion garante falha explicita em versao desconhecida.
func TestDecodeRejectsWrongVersion(t *testing.T) {
	p := &Pdu{Version: 1}
	der, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := Decode(der); err == nil {
		t.Fatal("esperava erro para versao 1, veio nil")
	}
}

// TestDecodeRejectsGarbage garante que lixo nao vira PDU silenciosamente.
func TestDecodeRejectsGarbage(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0x00},
		{0xff, 0xff, 0xff},
		[]byte("nao e DER"),
		{0x30, 0x00}, // SEQUENCE vazia -> sem version
	}
	for i, c := range cases {
		if _, err := Decode(c); err == nil {
			t.Errorf("caso %d: esperava erro, veio nil", i)
		}
	}
}

// TestLargeIntegerEncoding confirma o zero a esquerda quando o bit alto esta setado.
func TestLargeIntegerEncoding(t *testing.T) {
	var http uint16 = 502 // 0x01F6 -> nao precisa de zero
	p := NewErrorResponse(ErrGeneralError, &http)
	der, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(der)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if *got.Error.HTTPStatusCode != 502 {
		t.Fatalf("HTTPStatusCode = %d", *got.Error.HTTPStatusCode)
	}
	// 0x01F6 tem bit alto 0, entao 2 bytes sem padding.
	if !bytes.Contains(der, []byte{0x02, 0x02, 0x01, 0xf6}) {
		t.Errorf("INTEGER 502 deveria ser 02 02 01 f6; der=%s", hex.EncodeToString(der))
	}
}

// TestHighBitIntegerPadding cobre valor com bit alto setado (ex. 3389 = 0x0D3D nao
// precisa, mas 0xD3D... usamos 0x80xx para forcar padding).
func TestHighBitIntegerPadding(t *testing.T) {
	// 33022 = 0x80FE -> bit alto setado, precisa de 00 a esquerda.
	if got := encInteger(0x80FE); !bytes.Equal(got, []byte{0x02, 0x03, 0x00, 0x80, 0xfe}) {
		t.Fatalf("encInteger(0x80FE) = % x", got)
	}
	// 3390 = 0x0D3E -> sem padding.
	if got := encInteger(3390); !bytes.Equal(got, []byte{0x02, 0x02, 0x0d, 0x3e}) {
		t.Fatalf("encInteger(3390) = % x", got)
	}
}
