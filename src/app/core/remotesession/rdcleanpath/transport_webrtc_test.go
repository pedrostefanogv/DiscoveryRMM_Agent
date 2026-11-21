//go:build windows

package rdcleanpath

import (
	"context"
	"testing"
)

type stubPublisher struct {
	sent [][]byte
}

func (p *stubPublisher) PublishRdpCleanpathOut(_ string, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	p.sent = append(p.sent, cp)
	return nil
}

func TestNatsTransport_WebRtcDeliverAndSendWithFallback(t *testing.T) {
	pub := &stubPublisher{}
	tr, err := NewNatsTransport("sess-rdp-1", pub, func(_ string, _ func([]byte)) (func(), error) {
		return func() {}, nil
	})
	if err != nil {
		t.Fatalf("NewNatsTransport: %v", err)
	}
	defer tr.Close()

	// Deliver injeta bloco vindo do data channel WebRTC na fila RecvContext.
	tr.Deliver([]byte("hello-rdp"))
	got, err := tr.RecvContext(context.Background())
	if err != nil {
		t.Fatalf("RecvContext: %v", err)
	}
	if string(got) != "hello-rdp" {
		t.Fatalf("RecvContext = %q, esperava hello-rdp", string(got))
	}

	// Com webrtcSend retornando true, não deve publicar no NATS.
	var webrtcOut [][]byte
	tr.SetWebRtcSend(func(data []byte) bool {
		webrtcOut = append(webrtcOut, append([]byte(nil), data...))
		return true
	})
	if err := tr.Send([]byte("via-webrtc")); err != nil {
		t.Fatalf("Send via WebRTC: %v", err)
	}
	if len(webrtcOut) != 1 || string(webrtcOut[0]) != "via-webrtc" {
		t.Fatalf("webrtcOut inesperado: %v", webrtcOut)
	}
	if len(pub.sent) != 0 {
		t.Fatalf("não deveria ter publicado no NATS quando webrtcSend retornou true")
	}

	// Quando webrtcSend retorna false, cai para PublishRdpCleanpathOut.
	tr.SetWebRtcSend(func([]byte) bool { return false })
	if err := tr.Send([]byte("via-nats")); err != nil {
		t.Fatalf("Send fallback NATS: %v", err)
	}
	if len(pub.sent) != 1 || string(pub.sent[0]) != "via-nats" {
		t.Fatalf("fallback NATS inesperado: %v", pub.sent)
	}
}
