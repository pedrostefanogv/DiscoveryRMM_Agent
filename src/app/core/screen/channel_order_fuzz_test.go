//go:build windows

package screen

import (
	"math/rand"
	"testing"
)

// TestFuzzSafeFrameWindowInvariante é um teste exaustivo/aleatório da invariante
// central: se safeFrameWindow devolve ok(), então indexar até a última linha da
// janela e a última coluna amostrada NUNCA sai de Data.
//
// Existe porque o teste de casos fixos (TestSafeFrameStrideNuncaExcedeBuffer)
// cobre só as combinações que EU imaginei — e foi exatamente um caso não
// imaginado (buffer com menos de h linhas) que passou na primeira versão da
// correção. Este teste não depende da minha imaginação.
func TestFuzzSafeFrameWindowInvariante(t *testing.T) {
	rng := rand.New(rand.NewSource(20261120)) // determinístico: falha é reproduzível

	for i := 0; i < 200000; i++ {
		w := rng.Intn(200) - 2   // inclui 0 e negativos
		h := rng.Intn(200) - 2
		stride := rng.Intn(4000) - 5 // inclui 0 e negativos
		dataLen := rng.Intn(20000) - 3

		if dataLen < 0 {
			dataLen = 0
		}
		f := &Frame{
			Data:   make([]byte, dataLen),
			Width:  w,
			Height: h,
			Stride: stride,
		}

		fw := safeFrameWindow(f, w, h)
		if !fw.ok() {
			continue
		}
		if fw.stride <= 0 {
			t.Fatalf("ok() com stride<=0: %+v (w=%d h=%d stride=%d n=%d)", fw, w, h, stride, dataLen)
		}
		if fw.rows <= 0 {
			t.Fatalf("ok() com rows<=0: %+v (w=%d h=%d stride=%d n=%d)", fw, w, h, stride, dataLen)
		}
		// A invariante: o pior caso de indexação da sonda.
		last := (fw.rows-1)*fw.stride + (w-1)*4
		if last+3 >= len(f.Data) {
			t.Fatalf("INVARIANTE VIOLADA: last+3=%d len(Data)=%d (w=%d h=%d stride=%d rows=%d safeStride=%d)",
				last+3, len(f.Data), w, h, stride, fw.rows, fw.stride)
		}
	}
}
