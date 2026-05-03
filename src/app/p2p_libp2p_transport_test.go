package app

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestDecodeLibp2pGetHeaderAndPayloadReadsPayload(t *testing.T) {
	hdr := libp2pGetResponse{
		ArtifactName: "agent.bin",
		SHA256:       "abc123",
		TotalSize:    16,
		RangeStart:   4,
		RangeEnd:     7,
	}
	payload := []byte("DATA")

	var wire bytes.Buffer
	if err := json.NewEncoder(&wire).Encode(hdr); err != nil {
		t.Fatalf("encode hdr: %v", err)
	}
	if _, err := wire.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	gotHdr, payloadReader, err := decodeLibp2pGetHeaderAndPayload(bytes.NewReader(wire.Bytes()))
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if gotHdr.ArtifactName != hdr.ArtifactName || gotHdr.RangeStart != hdr.RangeStart || gotHdr.RangeEnd != hdr.RangeEnd {
		t.Fatalf("header inesperado: %+v", gotHdr)
	}

	gotPayload, err := io.ReadAll(payloadReader)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("payload divergente: esperado=%q recebido=%q", payload, gotPayload)
	}
}

func TestDecodeLibp2pGetHeaderAndPayloadRejectsInvalidRange(t *testing.T) {
	hdr := libp2pGetResponse{
		ArtifactName: "agent.bin",
		RangeStart:   10,
		RangeEnd:     1,
	}
	var wire bytes.Buffer
	if err := json.NewEncoder(&wire).Encode(hdr); err != nil {
		t.Fatalf("encode hdr: %v", err)
	}

	_, _, err := decodeLibp2pGetHeaderAndPayload(bytes.NewReader(wire.Bytes()))
	if err == nil {
		t.Fatal("esperava erro para range invalido")
	}
	if !strings.Contains(err.Error(), "range retornado invalido") {
		t.Fatalf("mensagem inesperada: %v", err)
	}
}

func TestReadPayloadExactShortRead(t *testing.T) {
	_, err := readPayloadExact(bytes.NewReader([]byte("ab")), 3)
	if err == nil {
		t.Fatal("esperava erro em short read")
	}
	if !strings.Contains(err.Error(), "leitura incompleta") {
		t.Fatalf("mensagem inesperada: %v", err)
	}
}

func TestReadPayloadExactSuccess(t *testing.T) {
	got, err := readPayloadExact(bytes.NewReader([]byte("abcd")), 4)
	if err != nil {
		t.Fatalf("read exact: %v", err)
	}
	if string(got) != "abcd" {
		t.Fatalf("payload inesperado: %q", string(got))
	}
}
