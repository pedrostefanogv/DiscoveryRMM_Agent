package screen

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"sort"
	"unsafe"
)

// DirtyRect representa uma regiao alterada detectada entre frames.
type DirtyRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// DirtyDetector detecta regioes alteradas entre dois frames.
// Usa hash de tiles para minimizar computacao.
type DirtyDetector struct {
	lastFrame  []byte
	lastWidth  int
	lastHeight int
	// lastStride é o bytes-por-linha do frame guardado. PRECISA entrar na
	// comparação de cache junto com width/height: dois frames com as MESMAS
	// dimensões e strides diferentes têm layouts de linha diferentes, e
	// comparar linha-a-linha passaria a confrontar posições de tela distintas —
	// gerando dirty rects espúrios (revisão de bugs 2026-11-19,
	// dirty_stride_probe_test.go). Acontece de verdade quando o capturer troca
	// (GDI↔DXGI) com o mesmo tamanho de tela e padding diferente.
	lastStride     int
	tileSize       int // tamanho do tile (ex: 32px)
	changedScratch []bool
	visitedScratch []bool
}

// NewDirtyDetector cria um detector de dirty rects.
func NewDirtyDetector(tileSize int) *DirtyDetector {
	if tileSize < 8 {
		tileSize = 32
	}
	return &DirtyDetector{
		tileSize: tileSize,
	}
}

// Reset limpa o frame anterior — a próxima chamada a Detect retorna um rect
// cobrindo toda a tela (key frame). Usado após troca de desktop (logon/UAC/
// lock screen): o conteúdo mudou completamente e o viewer precisa da base.
func (d *DirtyDetector) Reset() {
	d.lastFrame = nil
	d.lastWidth = 0
	d.lastHeight = 0
	d.lastStride = 0
}

// Detect compara o frame atual com o anterior e retorna as regioes alteradas.
// Se nao ha frame anterior, retorna um unico rect cobrindo toda a tela.
func (d *DirtyDetector) Detect(frame *Frame) []DirtyRect {
	if d.lastFrame == nil || d.lastWidth != frame.Width || d.lastHeight != frame.Height ||
		d.lastStride != frame.Stride || len(d.lastFrame) != len(frame.Data) {
		// Cache invalidado POR COMPLETO — inclusive o stride (ver lastStride) e
		// o tamanho do buffer: um frame de mesmo W/H mas com padding diferente
		// não pode ser comparado linha a linha contra o anterior.
		if cap(d.lastFrame) < len(frame.Data) {
			d.lastFrame = make([]byte, len(frame.Data))
		} else {
			d.lastFrame = d.lastFrame[:len(frame.Data)]
		}
		copy(d.lastFrame, frame.Data)
		d.lastWidth = frame.Width
		d.lastHeight = frame.Height
		d.lastStride = frame.Stride
		return []DirtyRect{{X: 0, Y: 0, Width: frame.Width, Height: frame.Height}}
	}

	cols := (frame.Width + d.tileSize - 1) / d.tileSize
	rows := (frame.Height + d.tileSize - 1) / d.tileSize

	gridSize := rows * cols
	if cap(d.changedScratch) < gridSize {
		d.changedScratch = make([]bool, gridSize)
	} else {
		d.changedScratch = d.changedScratch[:gridSize]
		clear(d.changedScratch)
	}

	// Compara tiles
	for row := 0; row < rows; row++ {
		rowBase := row * cols
		for col := 0; col < cols; col++ {
			if d.tileChanged(frame, col, row) {
				d.changedScratch[rowBase+col] = true
			}
		}
	}

	// Mergela retangulos adjacentes (union-find simplificado)
	rects := d.mergeTilesFlat(d.changedScratch, frame.Width, frame.Height, cols, rows)

	// Atualiza referencia
	copy(d.lastFrame, frame.Data)

	return rects
}

// tileChanged informa se o tile (col,row) mudou desde o último frame.
//
// Custo: este é O laço quente do stream de tela. Ele roda em CADA tile de CADA
// frame, e o profile da Fase 0 (bench_test.go / pprof) mostrou que a comparação
// consumia 67% do tempo total do detector — 3,6 ms por frame a 1080p no regime
// de tela ociosa, que é o mais comum numa sessão de suporte.
//
// Duas mudanças em relação à versão anterior, ambas de semântica idêntica:
//
//  1. Comparação por PALAVRA (uint64) via unsafe/bytes.Equal em blocos de 8
//     bytes, em vez do laço byte-a-byte de hasDifference. O laço anterior
//     impedia o compilador de vetorizar e pagava chamada de função + fatiamento
//     por amostra (261.120 chamadas por frame a 1080p).
//
//  2. A amostragem continua (linhas alternadas), porque o contrato do detector
//     é "detectar mudança visível", não "byte exato": amostrar a cada 2 linhas
//     já pega qualquer conteúdo que mude (texto, janela, cursor) e mantém o
//     custo proporcional à área VARRIDA, não à área total.
//
// O piso de segurança: se o tile tem menos de 8 bytes de largura útil, cai no
// caminho byte-a-byte (nunca acontece com tileSize>=8, mas evita leitura fora
// do buffer em frames degradados).
func (d *DirtyDetector) tileChanged(frame *Frame, col, row int) bool {
	x := col * d.tileSize
	y := row * d.tileSize
	w := min(d.tileSize, frame.Width-x)
	h := min(d.tileSize, frame.Height-y)

	stride := frame.Stride
	cur, prev := frame.Data, d.lastFrame
	n := len(cur)
	if len(prev) < n {
		n = len(prev)
	}
	rowBytes := w * 4

	for py := 0; py < h; py += 2 {
		base := (y+py)*stride + x*4
		if base < 0 || base >= n {
			continue
		}
		end := base + rowBytes
		if end > n {
			end = n
		}
		// Comparação por palavra: 8 bytes por vez, sem chamada de função.
		i := base
		limit := end - 8
		for ; i <= limit; i += 8 {
			if *(*uint64)(unsafe.Pointer(&cur[i])) != *(*uint64)(unsafe.Pointer(&prev[i])) {
				return true
			}
		}
		// Cauda (< 8 bytes): compara byte a byte.
		for ; i < end; i++ {
			if cur[i] != prev[i] {
				return true
			}
		}
	}
	return false
}

func (d *DirtyDetector) mergeTilesFlat(changed []bool, width, height, cols, rows int) []DirtyRect {
	if len(changed) == 0 || cols <= 0 || rows <= 0 {
		return nil
	}

	gridSize := rows * cols
	if cap(d.visitedScratch) < gridSize {
		d.visitedScratch = make([]bool, gridSize)
	} else {
		d.visitedScratch = d.visitedScratch[:gridSize]
		clear(d.visitedScratch)
	}
	visited := d.visitedScratch

	var rects []DirtyRect

	for row := 0; row < rows; row++ {
		rowBase := row * cols
		for col := 0; col < cols; col++ {
			if !changed[rowBase+col] || visited[rowBase+col] {
				continue
			}

			// Expande retangulo
			x := col * d.tileSize
			y := row * d.tileSize

			// Expande para direita
			endCol := col
			for endCol+1 < cols && changed[rowBase+endCol+1] && !visited[rowBase+endCol+1] {
				endCol++
			}

			// Expande para baixo
			endRow := row
		expandDown:
			for endRow+1 < rows {
				nextBase := (endRow + 1) * cols
				for c := col; c <= endCol; c++ {
					if !changed[nextBase+c] || visited[nextBase+c] {
						break expandDown
					}
				}
				endRow++
			}

			// Marca como visitado
			for r := row; r <= endRow; r++ {
				rBase := r * cols
				for c := col; c <= endCol; c++ {
					visited[rBase+c] = true
				}
			}

			rw := min((endCol-col+1)*d.tileSize, width-x)
			rh := min((endRow-row+1)*d.tileSize, height-y)

			rects = append(rects, DirtyRect{X: x, Y: y, Width: rw, Height: rh})
		}
	}

	// Ordena por area (maiores primeiro)
	sort.Slice(rects, func(i, j int) bool {
		return rects[i].Width*rects[i].Height > rects[j].Width*rects[j].Height
	})

	return rects
}

// EncodeDirtyRects codifica apenas as regioes alteradas como tiles JPEG.
// Formato de saída:
//
//	Header (4B): numRects (uint16) | flags (uint16)
//	Para cada tile:
//	  Tile header (12B): x(uint16) y(uint16) w(uint16) h(uint16) size(uint32)
//	  JPEG data (size bytes)
//
// TileScratch reúne os buffers REUTILIZÁVEIS da codificação por tiles. A versão
// anterior alocava, por frame: um bytes.Buffer de saída, um bytes.Buffer por
// tile, um []byte de 12 bytes por tile e uma image.RGBA por tile — 905
// alocações e 16,3 MB por frame num caso de 64 tiles (medido em
// BenchmarkEncodeDirtyRects, Fase 0). O encode roda a 20-30 fps: isso é pressão
// de GC constante durante a sessão inteira, e o GC compete com a captura.
//
// O scratch é por SESSÃO de tela (cada SessionScreen tem o seu) e o encode é
// sequencial dentro da sessão, então não há compartilhamento entre goroutines.
type TileScratch struct {
	out    []byte       // payload acumulado (header + tiles)
	jpeg   bytes.Buffer // saída do jpeg.Encode de UM tile (reaproveitado)
	img    *image.RGBA  // imagem do tile (reaproveitada quando cabe)
	header [12]byte     // header do tile (stack, sem alocação)
}

// EncodeDirtyRects codifica apenas as regioes alteradas como tiles JPEG.
// Formato de saída (INALTERADO — o viewer depende dele byte a byte):
//
//	Header (4B): numRects (uint16) | flags (uint16)
//	Para cada tile:
//	  Tile header (12B): x(uint16) y(uint16) w(uint16) h(uint16) size(uint32)
//	  JPEG data (size bytes)
func EncodeDirtyRects(frame *Frame, rects []DirtyRect, quality int) ([]byte, error) {
	return EncodeDirtyRectsScratch(frame, rects, quality, nil)
}

// EncodeDirtyRectsScratch é EncodeDirtyRects com buffers reutilizáveis. Passe o
// MESMO *TileScratch entre as chamadas de uma sessão para eliminar as
// alocações por frame; nil cria um scratch descartável (mesmo resultado, sem a
// economia — mantém a API antiga funcionando).
func EncodeDirtyRectsScratch(frame *Frame, rects []DirtyRect, quality int, s *TileScratch) ([]byte, error) {
	return EncodeDirtyRectsCodecScratch(frame, rects, quality, "jpeg", s)
}

// EncodeDirtyRectsCodecScratch codifica apenas as regiões alteradas usando o
// codec especificado ("webp" ou "jpeg"), reutilizando o *TileScratch da sessão.
func EncodeDirtyRectsCodecScratch(frame *Frame, rects []DirtyRect, quality int, codec string, s *TileScratch) ([]byte, error) {
	if s == nil {
		s = &TileScratch{}
	}

	// Header: numRects (uint16) + flags (uint16, 0 por enquanto).
	//
	// numRects é escrito com o número de tiles EFETIVAMENTE gravados (patch
	// após o laço), não com len(rects): um rect pode ser descartado no caminho
	// (fora do frame após recorte, ou erro de encode). Anunciar mais tiles do
	// que existem faz o viewer iterar além do payload — hoje ele tolera
	// (truncamento), mas a dessincronia é evitável e o header passa a ser
	// confiável como contrato.
	s.out = append(s.out[:0], 0, 0, 0, 0)

	// ── Codificação dos tiles, EM PARALELO quando compensa (Fase 6, 2026-11-20) ──
	//
	// POR QUE: cada tile é uma imagem INDEPENDENTE — nenhum tile lê o resultado
	// de outro. Eram codificados em série, e a medição de base mostrou que 64
	// tiles de 240x135 custavam ~63 ms (BenchmarkEncodeDirtyRects) contra ~58 ms
	// de um único JPEG do frame inteiro. Ou seja: o caminho que existe para
	// ECONOMIZAR BANDA custava o MESMO TEMPO que mandar a tela toda — o pior
	// dos dois mundos, porque a economia de bytes não pagava o custo de CPU.
	//
	// A paralelização muda essa conta: com N núcleos o trabalho de encode cai
	// por um fator próximo de N, e o tile-mode passa a ser vantajoso nos DOIS
	// eixos (banda E tempo de quadro).
	//
	// A ordem do payload é PRESERVADA: os resultados são escritos em posições
	// fixas e montados na ordem original, não na ordem de conclusão — o viewer
	// desenha tiles sobrepostos na ordem recebida.
	encoded := encodeTilesParallelWithCodec(frame, rects, quality, codec)

	escritos := 0
	for _, te := range encoded {
		if te == nil || len(te.data) == 0 {
			continue
		}
		// Tile header: x, y, w, h (uint16 cada = 8 bytes) + size (uint32 = 4 bytes)
		//
		// Largura/altura vêm da IMAGEM (já recortada nos limites do frame), não do
		// rect original. Um rect que passa da borda direita/inferior tem o
		// tamanho reduzido por extractTile; escrever o valor ORIGINAL no header
		// anunciava um tile maior do que o JPEG contém, e o viewer desenhava os
		// tiles de borda com escala/posição erradas. Defeito PRÉ-EXISTENTE
		// (o código anterior também escrevia rect.Width com a imagem recortada),
		// corrigido junto com a revisão de 2026-11-19 — ver
		// TestEncodeDirtyRectsHeaderBateComImagemRecortada.
		var hdr [12]byte
		binary.BigEndian.PutUint16(hdr[0:2], uint16(te.x))
		binary.BigEndian.PutUint16(hdr[2:4], uint16(te.y))
		binary.BigEndian.PutUint16(hdr[4:6], uint16(te.w))
		binary.BigEndian.PutUint16(hdr[6:8], uint16(te.h))
		binary.BigEndian.PutUint32(hdr[8:12], uint32(len(te.data)))

		s.out = append(s.out, hdr[:]...)
		s.out = append(s.out, te.data...)
		escritos++
	}

	// numRects = tiles EFETIVAMENTE gravados (ver comentário do header acima).
	binary.BigEndian.PutUint16(s.out[0:2], uint16(escritos))

	// Cópia defensiva: o resultado não pode ser um alias do scratch, senão o
	// próximo EncodeDirtyRects da mesma sessão sobrescreveria o buffer que ainda
	// está no caminho do encode/publish (corrupção de frame no stream).
	result := make([]byte, len(s.out))
	copy(result, s.out)
	return result, nil
}

// extractTile copia a região (rect) do frame BGRA para uma image.RGBA
// REUTILIZÁVEL, convertendo BGRA→RGBA.
//
// A imagem é reaproveitada quando as dimensões do tile cabem na capacidade já
// alocada; caso contrário é realocada. O stride é sempre rect.Width*4 (imagem
// compacta), então o buffer pode ser reusado entre tiles de tamanhos diferentes
// desde que o novo caiba.
func (s *TileScratch) extractTile(frame *Frame, rect DirtyRect) *image.RGBA {
	if rect.X < 0 || rect.Y < 0 || rect.Width <= 0 || rect.Height <= 0 {
		return nil
	}
	if rect.X+rect.Width > frame.Width {
		rect.Width = frame.Width - rect.X
	}
	if rect.Y+rect.Height > frame.Height {
		rect.Height = frame.Height - rect.Y
	}
	if rect.Width <= 0 || rect.Height <= 0 {
		return nil
	}

	stride := rect.Width * 4
	need := stride * rect.Height
	if s.img == nil || cap(s.img.Pix) < need {
		s.img = &image.RGBA{Pix: make([]byte, need), Stride: stride, Rect: image.Rect(0, 0, rect.Width, rect.Height)}
	} else {
		s.img.Pix = s.img.Pix[:need]
		s.img.Stride = stride
		s.img.Rect = image.Rect(0, 0, rect.Width, rect.Height)
	}

	pix := s.img.Pix
	for y := 0; y < rect.Height; y++ {
		src := (rect.Y+y)*frame.Stride + rect.X*4
		dst := y * stride
		if src+stride > len(frame.Data) {
			break
		}
		// Swap BGRA→RGBA por PALAVRA, com o helper compartilhado
		// (pixel_convert.go). O laço anterior era byte a byte por canal,
		// 4 escritas + 4 leituras por pixel; agora é uma conversão por
		// palavra com alfa forçado opaco, num único ponto do código.
		CopyBGRAtoRGBA(pix[dst:dst+stride], frame.Data[src:src+stride])
	}
	return s.img
}

// CopyFrameRects copia de src para dst apenas as regiões especificadas em rects
// (para que o encode worker extraia os tiles sem pagar memcpy de 20 MB pela
// tela inteira). Quando rects está vazio ou a geometria difere, copia tudo.
func CopyFrameRects(dst, src *Frame, rects []DirtyRect) {
	if dst == nil || src == nil {
		return
	}
	if len(rects) == 0 || dst.Width != src.Width || dst.Height != src.Height ||
		dst.Stride != src.Stride || len(dst.Data) != len(src.Data) {
		if len(dst.Data) != len(src.Data) {
			dst.Data = dst.Data[:len(src.Data)]
		}
		copy(dst.Data, src.Data)
		return
	}
	const bpp = 4
	stride := src.Stride
	for _, r := range rects {
		x0 := max(0, r.X)
		y0 := max(0, r.Y)
		x1 := min(src.Width, r.X+r.Width)
		y1 := min(src.Height, r.Y+r.Height)
		if x1 <= x0 || y1 <= y0 {
			continue
		}
		spanBytes := (x1 - x0) * bpp
		for y := y0; y < y1; y++ {
			off := y*stride + x0*bpp
			if off+spanBytes > len(src.Data) {
				break
			}
			copy(dst.Data[off:off+spanBytes], src.Data[off:off+spanBytes])
		}
	}
}

// Ensure imports
var _ = fmt.Println
var _ = image.Pt
