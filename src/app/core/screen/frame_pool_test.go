package screen

import "testing"

// TestGetPooledFrame_BasicDimension cobre o Get do pool (capacidade e dims).
func TestGetPooledFrame_BasicDimension(t *testing.T) {
	f := GetPooledFrame(1920, 1080, 1920*4)
	defer PutPooledFrame(f)

	if f.Width != 1920 || f.Height != 1080 || f.Stride != 1920*4 {
		t.Fatalf("dims = %dx%d stride %d, want 1920x1080 stride 7680", f.Width, f.Height, f.Stride)
	}
	if len(f.Data) != 1920*1080*4 {
		t.Fatalf("len(Data) = %d, want %d", len(f.Data), 1920*1080*4)
	}
	if cap(f.Data) < len(f.Data) {
		t.Fatalf("cap < len")
	}
}

// TestFramePool_Reuse valida o ciclo Get-Put-Get: o MESMO buffer volta (reciclagem).
// Drena antes buffers herdados de outros testes (ordem de execução é global
// ao pacote) para o ciclo put/get ser determinístico.
func TestFramePool_Reuse(t *testing.T) {
	for i := 0; i < 32; i++ {
		f := GetPooledFrame(640, 480, 640*4)
		_ = f // descartado SEM Put — remove objetos pré-alocados do pool
	}

	f1 := GetPooledFrame(640, 480, 640*4)
	buf1 := &f1.Data[:1][0] // endereço do primeiro byte
	PutPooledFrame(f1)

	f2 := GetPooledFrame(640, 480, 640*4)
	buf2 := &f2.Data[:1][0]
	defer PutPooledFrame(f2)

	if buf1 != buf2 {
		t.Fatalf("pool nao reciclou o buffer (enderecos diferentes)")
	}
}

// TestFramePool_SizeChange valida que um buffer incompativel (maior que 2x)
// e descartado, evitando retencao de memoria de resolucoes antigas.
func TestFramePool_SizeChange(t *testing.T) {
	f1 := GetPooledFrame(3840, 2160, 3840*4) // 4K (~33 MB)
	PutPooledFrame(f1)

	f2 := GetPooledFrame(640, 480, 640*4) // 480p (~1,2 MB) - cap 4K > 2x
	defer PutPooledFrame(f2)
	if cap(f2.Data) > 2*(480*640*4) {
		t.Fatalf("cap = %d, esperado realocacao (cap antigo >> novo)", cap(f2.Data))
	}
	if len(f2.Data) != 480*640*4 {
		t.Fatalf("len = %d, want %d", len(f2.Data), 480*640*4)
	}
}

// TestPutPooledFrame_Guards cobre os no-ops de seguranca do Put.
func TestPutPooledFrame_Guards(t *testing.T) {
	PutPooledFrame(nil)      // nil - nao pode panic
	PutPooledFrame(&Frame{}) // Data vazio - nao pode panic
	big := &Frame{Data: make([]byte, maxPooledFrameBytes+1)}
	PutPooledFrame(big)      // acima do limite - nao pode panic
}