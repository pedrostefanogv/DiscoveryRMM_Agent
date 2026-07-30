package screen

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"bytes"
	"sort"
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
	tileSize   int // tamanho do tile (ex: 32px)
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

// Detect compara o frame atual com o anterior e retorna as regioes alteradas.
// Se nao ha frame anterior, retorna um unico rect cobrindo toda a tela.
func (d *DirtyDetector) Detect(frame *Frame) []DirtyRect {
	if d.lastFrame == nil || d.lastWidth != frame.Width || d.lastHeight != frame.Height {
		d.lastFrame = make([]byte, len(frame.Data))
		copy(d.lastFrame, frame.Data)
		d.lastWidth = frame.Width
		d.lastHeight = frame.Height
		return []DirtyRect{{X: 0, Y: 0, Width: frame.Width, Height: frame.Height}}
	}

	cols := (frame.Width + d.tileSize - 1) / d.tileSize
	rows := (frame.Height + d.tileSize - 1) / d.tileSize

	// Grid booleana de tiles alterados
	changed := make([][]bool, rows)
	for y := range changed {
		changed[y] = make([]bool, cols)
	}

	// Compara tiles
	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			if d.tileChanged(frame, col, row) {
				changed[row][col] = true
			}
		}
	}

	// Mergela retangulos adjacentes (union-find simplificado)
	rects := d.mergeTiles(changed, frame.Width, frame.Height, cols, rows)

	// Atualiza referencia
	copy(d.lastFrame, frame.Data)

	return rects
}

func (d *DirtyDetector) tileChanged(frame *Frame, col, row int) bool {
	x := col * d.tileSize
	y := row * d.tileSize
	w := min(d.tileSize, frame.Width-x)
	h := min(d.tileSize, frame.Height-y)

	// Compara bytes do tile (amostragem a cada 4 bytes para performance)
	stride := frame.Stride
	for py := 0; py < h; py += 2 {
		offset := (y+py)*stride + x*4
		for px := 0; px < w*4; px += 16 {
			idx := offset + px
			if idx+16 <= len(frame.Data) && idx+16 <= len(d.lastFrame) {
				if hasDifference(frame.Data[idx:idx+16], d.lastFrame[idx:idx+16]) {
					return true
				}
			}
		}
	}
	return false
}

func hasDifference(a, b []byte) bool {
	for i := range a {
		if a[i] != b[i] {
			return true
		}
	}
	return false
}

func (d *DirtyDetector) mergeTiles(changed [][]bool, width, height, cols, rows int) []DirtyRect {
	if len(changed) == 0 {
		return nil
	}

	visited := make([][]bool, rows)
	for i := range visited {
		visited[i] = make([]bool, cols)
	}

	var rects []DirtyRect

	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			if !changed[row][col] || visited[row][col] {
				continue
			}

			// Expande retangulo
			x := col * d.tileSize
			y := row * d.tileSize

			// Expande para direita
			endCol := col
			for endCol+1 < cols && changed[row][endCol+1] && !visited[row][endCol+1] {
				endCol++
			}

			// Expande para baixo
			endRow := row
		expandDown:
			for endRow+1 < rows {
				for c := col; c <= endCol; c++ {
					if !changed[endRow+1][c] || visited[endRow+1][c] {
						break expandDown
					}
				}
				endRow++
			}

			// Marca como visitado
			for r := row; r <= endRow; r++ {
				for c := col; c <= endCol; c++ {
					visited[r][c] = true
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
func EncodeDirtyRects(frame *Frame, rects []DirtyRect, quality int) ([]byte, error) {
	var buf bytes.Buffer

	// Header: 4 bytes (numRects uint16) + padding
	binary.BigEndian.PutUint16(make([]byte, 2), uint16(len(rects)))

	for _, rect := range rects {
		// Extrai tile
		tile := extractTile(frame, rect)
		if tile == nil {
			continue
		}

		// JPEG encode
		var tileBuf bytes.Buffer
		if err := jpeg.Encode(&tileBuf, tile, &jpeg.Options{Quality: quality}); err != nil {
			continue
		}

		// Tile header: x, y, w, h (uint16 cada = 8 bytes) + size (uint32 = 4 bytes) + data
		tileHeader := make([]byte, 12)
		binary.BigEndian.PutUint16(tileHeader[0:2], uint16(rect.X))
		binary.BigEndian.PutUint16(tileHeader[2:4], uint16(rect.Y))
		binary.BigEndian.PutUint16(tileHeader[4:6], uint16(rect.Width))
		binary.BigEndian.PutUint16(tileHeader[6:8], uint16(rect.Height))
		binary.BigEndian.PutUint32(tileHeader[8:12], uint32(tileBuf.Len()))

		buf.Write(tileHeader)
		buf.Write(tileBuf.Bytes())
	}

	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result, nil
}

func extractTile(frame *Frame, rect DirtyRect) *image.RGBA {
	if rect.X < 0 || rect.Y < 0 || rect.Width <= 0 || rect.Height <= 0 {
		return nil
	}
	if rect.X+rect.Width > frame.Width {
		rect.Width = frame.Width - rect.X
	}
	if rect.Y+rect.Height > frame.Height {
		rect.Height = frame.Height - rect.Y
	}

	img := image.NewRGBA(image.Rect(0, 0, rect.Width, rect.Height))
	for y := 0; y < rect.Height; y++ {
		for x := 0; x < rect.Width; x++ {
			offset := (rect.Y+y)*frame.Stride + (rect.X+x)*4
			dst := y*img.Stride + x*4
			img.Pix[dst] = frame.Data[offset+2]   // R
			img.Pix[dst+1] = frame.Data[offset+1] // G
			img.Pix[dst+2] = frame.Data[offset]   // B
			img.Pix[dst+3] = 255
		}
	}
	return img
}

// Ensure imports
var _ = fmt.Println
var _ = image.Pt
