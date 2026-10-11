package screenshot

import (
	"testing"

	"discovery/app/core/screen"
)

// Regressão do relato "quando manda anexar print da tela, a tela toda fica
// preta": a Desktop Duplication (caminho HDR do overlay) devolve frame PRETO
// sem erro, e o overlay — uma janela preta cobrindo o desktop — aparecia todo
// preto. O detector abaixo é o que faz o app descartar esse frame e cair para o
// BitBlt (ou avisar o usuário).
func TestFrameIsBlank_DetectaFrameTodoPreto(t *testing.T) {
	black := &screen.Frame{
		Width:  64,
		Height: 64,
		Stride: 64 * 4,
		Data:   make([]byte, 64*64*4),
	}
	// BGRA preto com alpha cheio (é o que o capturer devolve).
	for i := 3; i < len(black.Data); i += 4 {
		black.Data[i] = 0xFF
	}

	if !FrameIsBlank(black, 0) {
		t.Fatal("frame todo preto deveria ser detectado como vazio")
	}
}

func TestFrameIsBlank_NaoConfundeComImagemReal(t *testing.T) {
	frame := &screen.Frame{
		Width:  64,
		Height: 64,
		Stride: 64 * 4,
		Data:   make([]byte, 64*64*4),
	}
	for i := 3; i < len(frame.Data); i += 4 {
		frame.Data[i] = 0xFF
	}
	// Um único pixel claro já caracteriza conteúdo.
	off := (32*64 + 32) * 4
	frame.Data[off] = 0xFF
	frame.Data[off+1] = 0xFF
	frame.Data[off+2] = 0xFF

	if FrameIsBlank(frame, 0) {
		t.Fatal("frame com conteúdo não pode ser tratado como preto")
	}
}

func TestFrameIsBlank_FrameVazioEhPreto(t *testing.T) {
	if !FrameIsBlank(nil, 0) {
		t.Fatal("frame nil deveria contar como preto/vazio")
	}
	if !FrameIsBlank(&screen.Frame{Width: 0, Height: 0}, 0) {
		t.Fatal("frame sem dimensões deveria contar como preto/vazio")
	}
}

func TestFrameIsBlank_ToleranciaPermitidaParaQuasePreto(t *testing.T) {
	frame := &screen.Frame{
		Width:  16,
		Height: 16,
		Stride: 16 * 4,
		Data:   make([]byte, 16*16*4),
	}
	// Cinza muito escuro (ruído de compressão/tone mapping).
	for i := 0; i < len(frame.Data); i += 4 {
		frame.Data[i] = 8
		frame.Data[i+1] = 8
		frame.Data[i+2] = 8
		frame.Data[i+3] = 0xFF
	}

	if !FrameIsBlank(frame, 0) {
		t.Fatal("cinza praticamente preto deveria ser tratado como frame preto")
	}
}
