//go:build windows

package screen

import "sync"

// ── Pool de frames BGRA (otimização de alocação por frame) ────────────────
//
// Cada frame de tela é um buffer grande: em 1920x1080 BGRA são ~8,3 MB.
// Antes desta otimização, o pipeline de remote session alocava um buffer
// novo POR FRAME no copy para o encode worker (session_screen.go) — a
// 30 fps isso é ~250 MB/s de churn de GC (e o dobro no caminho GDI, que
// também alocava no capturador). O pool recicla esses buffers: o producer
// pega um frame, o encode worker devolve após o encode.
//
// Segurança do ciclo de vida:
//   - O frame devolvido ao pool NÃO pode ser referenciado por ninguém —
//     o chamador só faz Put quando terminou de ler/encodar.
//   - sync.Pool esvazia em GC: sem retenção permanente.
//   - Mudança de resolução: Get descarta buffers com cap incompatível
//     (menor que o necessário ou >2x, para não reter memória de resoluções
//     antigas maiores).

// maxPooledFrameBytes limita o buffer que retorna ao pool. Um 4K HDR
// (3840x2160x8 bytes scRGB = ~66 MB) não deve ficar retido no pool se a
// resolução/qualidade cair.
const maxPooledFrameBytes = 64 << 20 // 64 MB

var framePool = sync.Pool{New: func() any { return new(Frame) }}

// GetPooledFrame retorna um *Frame com Data com capacidade suficiente para
// height*stride bytes. Reusa buffer do pool quando compatível; aloca novo
// quando o tamanho muda para cima. O Data é zerado no comprimento útil?
// NÃO — o caller preenche integralmente (copy do frame capturado), como
// já fazia com make().
func GetPooledFrame(width, height, stride int) *Frame {
	needed := height * stride
	if needed <= 0 {
		return &Frame{Width: width, Height: height, Stride: stride}
	}
	f, _ := framePool.Get().(*Frame)
	if f != nil && (cap(f.Data) < needed || cap(f.Data) > 2*needed) {
		// Capacidade incompatível (resolução mudou) — descarta e aloca.
		f = nil
	}
	if f == nil {
		f = &Frame{Data: make([]byte, needed)}
	}
	f.Data = f.Data[:needed]
	f.Width = width
	f.Height = height
	f.Stride = stride
	f.ColorSpace = 0
	return f
}

// PutPooledFrame devolve o frame ao pool. Após a chamada, o frame NÃO pode
// ser mais usado. No-op para frames vazios/maiores que o limite.
func PutPooledFrame(f *Frame) {
	if f == nil || cap(f.Data) == 0 || cap(f.Data) > maxPooledFrameBytes {
		return
	}
	// Preserva a capacidade integral para o próximo reuso.
	f.Data = f.Data[:cap(f.Data)]
	f.Width, f.Height, f.Stride, f.ColorSpace = 0, 0, 0, 0
	framePool.Put(f)
}