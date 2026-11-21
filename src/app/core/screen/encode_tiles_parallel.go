package screen

import (
	"image"
	"image/jpeg"
	"runtime"
	"sync"
)

// Codificação PARALELA de tiles (Fase 6, 2026-11-20).
//
// Cada tile é uma imagem independente: extrair o recorte e codificá-lo não
// depende de nenhum outro tile. Isso torna o caminho trivialmente paralelo, e
// era exatamente o que faltava: medido na linha de base, 64 tiles custavam
// ~63 ms contra ~58 ms do frame inteiro — o tile-mode economizava BANDA mas
// não TEMPO, então em máquinas de 1-4 núcleos ele perdia para o frame completo
// em fluidez.
//
// Resultado por tile é escrito em posição fixa (encoded[i]), nunca por append
// concorrente: a ordem do payload é contrato com o viewer, que desenha os
// tiles na ordem recebida (tiles podem se sobrepor após a consolidação).

// tileEncoded é o resultado da codificação de UM tile. w/h vêm da imagem já
// recortada nos limites do frame — é isso que o header precisa anunciar.
type tileEncoded struct {
	x, y, w, h int
	data       []byte
}

// encodeTilesParallelMinTiles é o número de tiles a partir do qual vale
// paralelizar. Com 1-2 tiles o custo de sincronização supera o ganho.
const encodeTilesParallelMinTiles = 2

// encodeTilesParallel extrai e codifica cada rect em JPEG, devolvendo os
// resultados na MESMA ORDEM de `rects`. Posições cujo recorte/encode falhou ficam nil.
func encodeTilesParallel(frame *Frame, rects []DirtyRect, quality int) []*tileEncoded {
	return encodeTilesParallelWithCodec(frame, rects, quality, "jpeg")
}

// encodeTilesParallelWithCodec extrai e codifica cada rect usando o codec
// informado ("webp" ou "jpeg", com fallback para JPEG se WebP falhar).
func encodeTilesParallelWithCodec(frame *Frame, rects []DirtyRect, quality int, codec string) []*tileEncoded {
	out := make([]*tileEncoded, len(rects))
	if len(rects) == 0 {
		return out
	}

	// Um único tile (ou poucos): o caminho serial evita acordar goroutines.
	if len(rects) < encodeTilesParallelMinTiles {
		out[0] = encodeOneTileWithCodec(frame, rects[0], quality, codec, &TileScratch{})
		return out
	}

	workers := runtime.NumCPU()
	if workers > len(rects) {
		workers = len(rects)
	}
	if workers < 2 {
		// Máquina de 1 núcleo: serial, sem custo de sincronização.
		sc := &TileScratch{}
		for i := range rects {
			out[i] = encodeOneTileWithCodec(frame, rects[i], quality, codec, sc)
		}
		return out
	}

	// Fila de índices: cada worker pega o próximo trabalho disponível. Partição
	// DINÂMICA (e não faixas fixas) porque o custo por tile varia muito — um
	// tile de texto comprime devagar, um de área plana comprime rápido. Com
	// faixas fixas o worker que pegasse os tiles caros definiria o tempo total.
	idx := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			// TileScratch por worker: os buffers são reutilizados DENTRO do
			// worker, entre os tiles que ele pegar. Compartilhar um scratch
			// entre goroutines seria uma corrida de dados.
			sc := &TileScratch{}
			for i := range idx {
				out[i] = encodeOneTileWithCodec(frame, rects[i], quality, codec, sc)
			}
		}()
	}
	for i := range rects {
		idx <- i
	}
	close(idx)
	wg.Wait()
	return out
}

// encodeOneTileWithCodec extrai UM tile do frame e o codifica no codec pedido
// ("webp" ou "jpeg"). Devolve nil quando o recorte é inválido ou o encode falha.
func encodeOneTileWithCodec(frame *Frame, rect DirtyRect, quality int, codec string, sc *TileScratch) *tileEncoded {
	img := extractTileWith(frame, rect, sc)
	if img == nil {
		return nil
	}
	sc.jpeg.Reset()
	if codec == "webp" {
		if err := encodeRGBAWebP(&sc.jpeg, img, quality); err != nil {
			sc.jpeg.Reset()
			if jerr := jpeg.Encode(&sc.jpeg, img, &jpeg.Options{Quality: quality}); jerr != nil {
				return nil
			}
		}
	} else {
		if err := jpeg.Encode(&sc.jpeg, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil
		}
	}
	encoded := sc.jpeg.Bytes()
	if len(encoded) == 0 {
		return nil
	}
	// CÓPIA OBRIGATÓRIA: encoded aponta para o buffer do scratch, que será
	// reutilizado no próximo tile DESTE worker. Sem a cópia, dois tiles
	// compartilhariam o mesmo armazenamento e o payload sairia corrompido.
	data := make([]byte, len(encoded))
	copy(data, encoded)

	bounds := img.Bounds()
	return &tileEncoded{
		x: rect.X, y: rect.Y,
		w: bounds.Dx(), h: bounds.Dy(),
		data: data,
	}
}

// extractTileWith é o extractTile do TileScratch, exposto como função para o
// caminho paralelo (que cria um scratch por worker em vez de usar o da sessão).
func extractTileWith(frame *Frame, rect DirtyRect, sc *TileScratch) *image.RGBA {
	return sc.extractTile(frame, rect)
}
