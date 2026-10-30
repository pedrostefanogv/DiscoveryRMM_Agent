//go:build windows

package remotesession

import (
	"testing"

	"discovery/app/core/screen"
)

// fakeGateCapturer simula um capturer com ordem de canais pendente/resolvida
// para exercitar o gate de publicação (channelOrderGate).
type fakeGateCapturer struct{ pending bool }

func (f *fakeGateCapturer) AcquireNextFrame() (*screen.Frame, error) { return nil, nil }
func (f *fakeGateCapturer) ReleaseFrame()                            {}
func (f *fakeGateCapturer) Close() error                             { return nil }
func (f *fakeGateCapturer) Name() string                             { return "fake" }
func (f *fakeGateCapturer) Geometry() (x, y, w, h int)               { return 0, 0, 1, 1 }
func (f *fakeGateCapturer) ChannelOrderPending() bool                { return f.pending }
func (f *fakeGateCapturer) ChannelOrderName() string                 { return "RGBA (teste)" }

var _ screen.Capturer = (*fakeGateCapturer)(nil)
var _ screen.ChannelOrderReporter = (*fakeGateCapturer)(nil)

// TestChannelOrderGate cobre o comportamento do gate introduzido após o
// incidente de cores invertidas de 2026-10-29:
//  1. pendente  ⇒ segura a publicação;
//  2. resolvido ⇒ publica e pede key frame (o viewer precisa da tela inteira);
//  3. pendente além do deadline ⇒ publica mesmo assim (a sessão não pode
//     ficar preta se a calibração nunca concluir).
func TestChannelOrderGate(t *testing.T) {
	s := &SessionScreen{}
	fake := &fakeGateCapturer{pending: true}

	if !s.channelOrderGate(fake) {
		t.Fatal("pendente deve segurar a publicação")
	}
	if !s.channelGateBlocked {
		t.Fatal("estado blocked deve ficar ativo enquanto segura")
	}

	fake.pending = false
	if s.channelOrderGate(fake) {
		t.Fatal("resolvido deve publicar")
	}
	if s.channelGateBlocked {
		t.Fatal("blocked deve desarmar ao publicar")
	}
	if !s.forceKeyFrame.Load() {
		t.Fatal("deve pedir key frame ao retomar a publicação")
	}
}

func TestChannelOrderGateExpires(t *testing.T) {
	s := &SessionScreen{}
	fake := &fakeGateCapturer{pending: true}

	if !s.channelOrderGate(fake) {
		t.Fatal("primeiro frame pendente deve segurar")
	}
	// Simula o deadline estourado.
	s.channelGateUntil = s.channelGateUntil.Add(-2 * channelGateMaxWait)
	if s.channelOrderGate(fake) {
		t.Fatal("após o deadline deve publicar mesmo pendente")
	}
	if !s.channelGateExpired {
		t.Fatal("flag expired deve registrar o estouro do prazo")
	}
}
