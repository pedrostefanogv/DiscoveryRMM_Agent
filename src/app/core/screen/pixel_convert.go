package screen

import (
	"runtime"
	"sync"
	"unsafe"
)

// ── Swap de canais BGRA <-> RGBA e helpers vetorizáveis (Fase 6, 2026-11-20) ──
//
// POR QUE ISTO EXISTE: o swap R↔B aparecia em DOIS lugares quentes do stream —
// no encoder JPEG de frame inteiro (encoder_jpeg.go) e na extração de cada tile
// (dirty_rects.go). A 3440x1440 são ~4,95 MP por frame, ou ~19,8 MB varridos
// para entregar uma imagem que "muda" minimamente. O laço original fazia 4
// operações de máscara por pixel com checagem de limites em cada acesso.
//
// O que este arquivo faz:
//  1. Concentra a implementação num único ponto (antes era duplicada, com
//     semânticas ligeiramente diferentes — uma forçava alfa opaco, a outra não).
//  2. Usa unsafe.Slice, que emite o laço SEM bounds check por elemento: o
//     compilador prova que o índice é válido para a slice inteira.
//  3. Paraleliza por faixas quando a imagem é grande o bastante para o custo de
//     sincronização se pagar.

// swapMinBytesForParallel é o tamanho a partir do qual vale dividir o trabalho
// entre goroutines. Abaixo disso o custo de acordar as goroutines supera o ganho.
// ~256 KB = 65k pixels (ex.: uma janela de 256x256).
const swapMinBytesForParallel = 256 * 1024

// SwapBGRAtoRGBAInPlace converte, NO LUGAR, um buffer BGRA (0xAARRGGBB por
// word LE = bytes B,G,R,A) para RGBA (bytes R,G,B,A), trocando os bytes 0 e 2
// de cada pixel e preservando G e A.
//
// Exige len(buf) múltiplo de 4 (estrutura de pixel de 4 bytes). O chamador
// garante isso: BGRA nunca tem canal parcial.
func SwapBGRAtoRGBAInPlace(buf []byte) {
	if len(buf) < 4 {
		return
	}
	n := len(buf) / 4
	// Caminho rápido: cada faixa é uma slice de uint32 alinhada.
	if len(buf) >= swapMinBytesForParallel {
		parallelSwap(buf[:n*4])
		return
	}
	swapWords(unsafe.Slice((*uint32)(unsafe.Pointer(&buf[0])), n))
}

// swapWords aplica o swap de bytes numa slice de palavras. O laço é escrito
// sobre índices de slice para o compilador eliminar a checagem de limites.
func swapWords(pixels []uint32) {
	for i := range pixels {
		c := pixels[i]
		// BGRA (LE) 0xAARRGGBB → RGBA 0xAABBGGRR:
		//   mantém A (24-31) e G (8-15), troca B (0-7) e R (16-23).
		pixels[i] = (c & 0xFF00FF00) |
			((c & 0x000000FF) << 16) |
			((c & 0x00FF0000) >> 16)
	}
}

// parallelSwap divide o buffer em faixas por núcleo e aplica swapWords em cada
// uma. O trabalho é puramente local (cada faixa toca só os próprios bytes),
// então não há compartilhamento nem falsa partilha relevante: as faixas são
// separadas por milhares de cache lines.
func parallelSwap(buf []byte) {
	workers := runtime.NumCPU()
	if workers > 8 {
		// Acima de 8 o ganho satura e a contenção de memória (o swap é limitado
		// por banda de RAM, não por ALU) passa a dominar.
		workers = 8
	}
	words := len(buf) / 4
	if workers < 2 || words < workers*2 {
		swapWords(unsafe.Slice((*uint32)(unsafe.Pointer(&buf[0])), words))
		return
	}

	chunk := words / workers
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		start := w * chunk
		end := start + chunk
		if w == workers-1 {
			end = words // a última faixa absorve o resto da divisão
		}
		seg := buf[start*4 : end*4]
		go func() {
			defer wg.Done()
			swapWords(unsafe.Slice((*uint32)(unsafe.Pointer(&seg[0])), len(seg)/4))
		}()
	}
	wg.Wait()
}

// CopyBGRAtoRGBA converte BGRA → RGBA COPIANDO para um destino, forçando alfa
// opaco (0xFF). É o que a extração de tile precisa (dirty_rects.go): o tile
// vai virar um JPEG, e alfa parcial em JPEG é ignorado de qualquer forma —
// forçar 0xFF evita que lixo do canal A vaze para o resultado.
//
// dst e src têm o mesmo número de pixels; dst recebe os bytes convertidos.
func CopyBGRAtoRGBA(dst, src []byte) {
	n := len(src) / 4
	if n == 0 || len(dst) < n*4 {
		return
	}
	sp := unsafe.Slice((*uint32)(unsafe.Pointer(&src[0])), n)
	dp := unsafe.Slice((*uint32)(unsafe.Pointer(&dst[0])), n)
	for i := 0; i < n; i++ {
		c := sp[i]
		// Força A=0xFF, mantém G, troca B e R.
		dp[i] = 0xFF000000 |
			((c & 0x000000FF) << 16) |
			(c & 0x0000FF00) |
			((c & 0x00FF0000) >> 16)
	}
}
