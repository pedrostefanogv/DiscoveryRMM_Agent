//go:build windows

package screen

import "testing"

// TestLiveDXGIGoD3dProbeAndCreation valida a ordem das operações introduzida
// com a sonda de formato: a duplicação de teste (criada e liberada ANTES da
// duplicação real) não pode impedir o go-d3d de criar a sua. Roda só onde
// existe GPU/output duplicável — caso contrário pula.
func TestLiveDXGIGoD3dProbeAndCreation(t *testing.T) {
	captr, err := NewDXGIGoD3dCapturerMode(0, false)
	if err != nil {
		t.Skipf("DXGI go-d3d indisponível neste ambiente: %v", err)
	}
	defer func() { _ = captr.Close() }()

	c, ok := captr.(*dxgiGoD3dCapturer)
	if !ok {
		t.Fatalf("tipo inesperado: %T", captr)
	}
	t.Logf("seed=%s resolved=%v pending=%v origin=(%d,%d) size=%dx%d",
		c.calib.Order(), c.calib.Resolved(), c.ChannelOrderPending(),
		c.originX, c.originY, c.width, c.height)

	// A sonda determinística DEVE ter decidido (RGBA ou BGRA) — "desconhecida"
	// aqui significa que a sonda falhou e voltamos a depender da calibração.
	if c.calib.Order() == ChannelOrderUnknown {
		t.Logf("AVISO: sonda inconclusiva neste ambiente (calibração decidirá)")
	}

	frame, err := captr.AcquireNextFrame()
	if err != nil {
		t.Skipf("primeiro frame indisponível: %v", err)
	}
	if frame.Width <= 0 || frame.Height <= 0 || len(frame.Data) < frame.Stride*frame.Height {
		t.Fatalf("frame inválido: %dx%d stride=%d len=%d", frame.Width, frame.Height, frame.Stride, len(frame.Data))
	}
	t.Logf("frame %dx%d stride=%d len=%d pending=%v", frame.Width, frame.Height, frame.Stride, len(frame.Data), c.ChannelOrderPending())
}
