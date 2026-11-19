package rdcleanpath

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Estes vetores foram gerados pela implementacao de referencia do proxy
// (example/lib/rdp-proxy.js do electerm/ironrdp-wasm) e conferidos byte-a-byte
// contra o encoder Go. Servem como teste de interoperabilidade: se alguem
// alterar o codec de forma incompativel com o IronRDP, estes testes quebram.
//
// Vetor 1: response proxy->client
//
//	version=3390, x224=030000130ed000001234000100000000000000,
//	certs=[AA*300, BB*5], server_addr="127.0.0.1:3389"
//
// Vetor 2: error PDU
//
//	error_code=103 (target unreachable), http_status_code=502
const (
	refResponseHex = "3082016ea00402020d3ea6150413030000130ed000001234000100000000000000a782013b308201370482012caaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0405bbbbbbbbbba9100c0e3132372e302e302e313a33333839"
	refErrorHex    = "3015a00402020d3ea10d300ba003020167a104020201f6"
)

func refX224() []byte {
	b, err := hex.DecodeString("030000130ed000001234000100000000000000")
	if err != nil {
		panic(err)
	}
	return b
}

// TestInteropResponseMatchesReference garante que o response Go e identico ao
// produzido pela implementacao de referencia.
func TestInteropResponseMatchesReference(t *testing.T) {
	want, err := hex.DecodeString(refResponseHex)
	if err != nil {
		t.Fatalf("hex de referencia invalido: %v", err)
	}

	cert1 := bytes.Repeat([]byte{0xAA}, 300)
	cert2 := bytes.Repeat([]byte{0xBB}, 5)
	p := NewResponse("127.0.0.1:3389", refX224(), [][]byte{cert1, cert2})

	got, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("response divergente da referencia\n got: %s\nwant: %s", hex.EncodeToString(got), refResponseHex)
	}
}

// TestInteropErrorMatchesReference garante que o Error PDU Go e identico ao de referencia.
func TestInteropErrorMatchesReference(t *testing.T) {
	want, err := hex.DecodeString(refErrorHex)
	if err != nil {
		t.Fatalf("hex de referencia invalido: %v", err)
	}

	var http uint16 = 502
	p := NewErrorResponse(ErrTargetUnreachable, &http)

	got, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("error PDU divergente da referencia\n got: %s\nwant: %s", hex.EncodeToString(got), refErrorHex)
	}
}

// TestDecodeReferenceBytes prova que o decoder Go aceita bytes gerados pelo
// IronRDP/proxy de referencia (interop na direcao de entrada).
func TestDecodeReferenceBytes(t *testing.T) {
	raw, err := hex.DecodeString(refResponseHex)
	if err != nil {
		t.Fatalf("hex invalido: %v", err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode de bytes de referencia: %v", err)
	}
	if !bytes.Equal(got.X224ConnectionPdu, refX224()) {
		t.Error("x224 divergente ao decodificar bytes de referencia")
	}
	if len(got.ServerCertChain) != 2 {
		t.Fatalf("cert chain = %d, esperado 2", len(got.ServerCertChain))
	}
	if len(got.ServerCertChain[0]) != 300 {
		t.Errorf("cert0 len = %d, esperado 300", len(got.ServerCertChain[0]))
	}
	if got.ServerAddr == nil || *got.ServerAddr != "127.0.0.1:3389" {
		t.Errorf("ServerAddr = %v", got.ServerAddr)
	}

	// Error PDU de referencia.
	eraw, err := hex.DecodeString(refErrorHex)
	if err != nil {
		t.Fatalf("hex invalido: %v", err)
	}
	egot, err := Decode(eraw)
	if err != nil {
		t.Fatalf("Decode do error de referencia: %v", err)
	}
	if egot.Error == nil || egot.Error.ErrorCode != ErrTargetUnreachable {
		t.Fatalf("error code = %v", egot.Error)
	}
	if egot.Error.HTTPStatusCode == nil || *egot.Error.HTTPStatusCode != 502 {
		t.Errorf("http status = %v", egot.Error.HTTPStatusCode)
	}
}
