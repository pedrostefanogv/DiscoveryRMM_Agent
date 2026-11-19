package screen

import (
	"testing"
)

// ── Benchmarks do caminho quente do stream de tela (Fase 0 do plano de ──
// otimização do acesso remoto, 2026-11-19).
//
// POR QUE ISTO EXISTE: o pipeline de captura paga, por frame, o software dirty
// diff (dirty_rects.go), a cópia integral do frame (session_screen.go) e a
// reescrita da máscara do banner (banner_patch_windows.go). Não havia nenhum
// benchmark no repositório, então toda a discussão de custo era analítica.
// Estes benchmarks transformam o "achismo" em número: rode com
//
//	go test ./app/core/screen -run '^$' -bench . -benchmem
//
// e compare antes/depois de qualquer mudança no pipeline.

// benchFrameBGRA monta um frame BGRA sintético com conteúdo PSEUDO-ALEATÓRIO
// determinístico: tiles de ruído garantem que o dirty detector NÃO possa
// atalhar (todo tile difere do anterior), que é o pior caso honesto para o
// custo de comparação. Uma tela uniforme mediria só o caminho de leitura.
func benchFrameBGRA(w, h int, seed uint32) *Frame {
	stride := w * 4
	data := make([]byte, stride*h)
	x := seed | 1
	for i := 0; i < len(data); i += 4 {
		// xorshift32: barato e determinístico entre execuções.
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		data[i] = byte(x)
		data[i+1] = byte(x >> 8)
		data[i+2] = byte(x >> 16)
		data[i+3] = 0xFF
	}
	return &Frame{Data: data, Width: w, Height: h, Stride: stride}
}

// BenchmarkDirtyDetect mede o custo de comparação do dirty detector no pior
// caso (todo tile mudou). É o número que decide a Fase 1 do plano.
func BenchmarkDirtyDetect(b *testing.B) {
	cases := []struct {
		name string
		w, h int
	}{
		{"1080p", 1920, 1080},
		{"1440p", 2560, 1440},
		{"4K", 3840, 2160},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			d := NewDirtyDetector(32)
			frame := benchFrameBGRA(tc.w, tc.h, 1)
			next := benchFrameBGRA(tc.w, tc.h, 2)
			// Aquece: a primeira chamada aloca lastFrame e não é representativa.
			d.Detect(frame)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Alterna o conteúdo a cada iteração: sem isso o detector
				// encontraria "nada mudou" e mediria o caminho barato.
				if i%2 == 0 {
					d.Detect(next)
				} else {
					d.Detect(frame)
				}
			}
		})
	}
}

// BenchmarkDirtyDetectIdle mede o mesmo detector quando NADA mudou — o caso da
// tela ociosa, que é o regime mais comum numa sessão de suporte (o operador
// olha a tela sem mexer). Aqui o custo é o piso: toda a varredura acontece
// mesmo assim (o detector só sabe que nada mudou depois de comparar).
func BenchmarkDirtyDetectIdle(b *testing.B) {
	d := NewDirtyDetector(32)
	frame := benchFrameBGRA(1920, 1080, 7)
	d.Detect(frame)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Detect(frame)
	}
}

// BenchmarkFrameCopy mede a cópia integral do frame feita ANTES do encode
// (session_screen.go: o buffer do capturer é sobrescrito por AcquireNextFrame,
// então o frame é copiado para o pool). É o número que decide a Fase 2.
func BenchmarkFrameCopy(b *testing.B) {
	cases := []struct {
		name string
		w, h int
	}{
		{"1080p", 1920, 1080},
		{"1440p", 2560, 1440},
		{"4K", 3840, 2160},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			src := benchFrameBGRA(tc.w, tc.h, 3)
			dst := make([]byte, len(src.Data))
			b.SetBytes(int64(len(src.Data)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				copy(dst, src.Data)
			}
		})
	}
}

// BenchmarkEncodeDirtyRects mede a codificação por TILES (um JPEG por região),
// que é o caminho padrão do agente (tileMode=true). Compara com o custo de um
// único JPEG do frame inteiro — a razão entre os dois é o que decide a Fase 4.
func BenchmarkEncodeDirtyRects(b *testing.B) {
	frame := benchFrameBGRA(1920, 1080, 11)
	// 64 tiles de 240x135: cenário realista de janela movendo na tela.
	var rects []DirtyRect
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			rects = append(rects, DirtyRect{X: c * 240, Y: r * 135, Width: 240, Height: 135})
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncodeDirtyRects(frame, rects, 60); err != nil {
			b.Fatalf("encode tiles: %v", err)
		}
	}
}

// BenchmarkEncodeFullFrameJPEG é a linha de base do benchmark acima: um único
// JPEG do frame inteiro na mesma qualidade.
func BenchmarkEncodeFullFrameJPEG(b *testing.B) {
	frame := benchFrameBGRA(1920, 1080, 11)
	enc := NewJPEGEncoder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := enc.Encode(frame, 60); err != nil {
			b.Fatalf("encode full: %v", err)
		}
	}
}
