package remotesession

import (
	"bytes"
	"testing"
)

func TestTunnelCodec_BinaryDataRoundTrip(t *testing.T) {
	payload := []byte{0x00, 0x01, 0xff, 0xfe, 0x80, 0x42, 0x13, 0x37}
	frame := EncodeTunnelBinaryData(nil, 0x01020304, payload)
	if len(frame) != TunnelBinaryHeaderLen+len(payload) {
		t.Fatalf("len(frame) = %d, want %d", len(frame), TunnelBinaryHeaderLen+len(payload))
	}
	if frame[0] != TunnelFrameVersion1 || frame[1] != TunnelOpData {
		t.Fatalf("header inesperado: %x", frame[:6])
	}

	isBin, op, sid, gotPayload, ctrl, err := DecodeTunnelFrame(frame)
	if err != nil {
		t.Fatalf("DecodeTunnelFrame: %v", err)
	}
	if !isBin || op != TunnelOpData || sid != 0x01020304 || ctrl != nil {
		t.Fatalf("decode mismatch: isBin=%v op=%x sid=%x ctrl=%v", isBin, op, sid, ctrl)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("payload mismatch: got %x want %x", gotPayload, payload)
	}

	// 64 KiB payload + empty payload
	big := bytes.Repeat([]byte{0xAA}, 64*1024)
	bigFrame := EncodeTunnelBinaryData(nil, 99, big)
	isBin, op, sid, gotBig, _, err := DecodeTunnelFrame(bigFrame)
	if err != nil || !isBin || op != TunnelOpData || sid != 99 || !bytes.Equal(gotBig, big) {
		t.Fatalf("64 KiB roundtrip falhou: err=%v len=%d", err, len(gotBig))
	}

	emptyFrame := EncodeTunnelBinaryData(nil, 7, nil)
	isBin, op, sid, gotEmpty, _, err := DecodeTunnelFrame(emptyFrame)
	if err != nil || !isBin || op != TunnelOpData || sid != 7 || len(gotEmpty) != 0 {
		t.Fatalf("empty binary payload roundtrip falhou: err=%v len=%d", err, len(gotEmpty))
	}
}

func TestTunnelCodec_BinaryCloseAndInvalidFrames(t *testing.T) {
	closeFrame := EncodeTunnelBinaryClose(42, "eof")
	isBin, op, sid, payload, _, err := DecodeTunnelFrame(closeFrame)
	if err != nil || !isBin || op != TunnelOpClose || sid != 42 || string(payload) != "eof" {
		t.Fatalf("binary close falhou: isBin=%v op=%x sid=%d payload=%q err=%v", isBin, op, sid, string(payload), err)
	}

	for _, bad := range [][]byte{
		nil,
		{},
		{0x01, 0x01, 0x00},                   // < 6 bytes
		{0x01, 0x99, 0x00, 0x00, 0x00, 0x01}, // op invalido
		{0x42, 0x01, 0x00, 0x00, 0x00, 0x01}, // prefixo desconhecido
		[]byte(`{"streamId":1,"op":"bogus"}`),
	} {
		if _, _, _, _, _, err := DecodeTunnelFrame(bad); err == nil {
			t.Fatalf("frame invalido %x deveria retornar erro", bad)
		}
	}
}

func TestTunnelCodec_JSONControlRoundTrip(t *testing.T) {
	raw := []byte(" \n\t" + `{"streamId":5,"op":"preflight","rdpStatus":{"ready":true,"termServiceState":"running","rdpEnabled":true,"port":3389,"listening":true,"checkedAtUtc":"2026-11-18T00:00:00Z"}}`)
	isBin, _, sid, _, ctrl, err := DecodeTunnelFrame(raw)
	if err != nil {
		t.Fatalf("DecodeTunnelFrame JSON: %v", err)
	}
	if isBin || sid != 5 || ctrl == nil || ctrl.Op != "preflight" || ctrl.RdpStatus == nil || !ctrl.RdpStatus.Ready || ctrl.RdpStatus.Port != 3389 {
		t.Fatalf("decode JSON inesperado: isBin=%v sid=%d ctrl=%+v", isBin, sid, ctrl)
	}
}
