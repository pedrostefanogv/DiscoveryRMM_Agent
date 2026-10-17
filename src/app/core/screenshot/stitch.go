package screenshot

import (
	"bytes"

	"discovery/app/core/screen"
)

// Costura de capturas de rolagem ("long screenshot"). Tudo aqui é PURO: recebe
// frames BGRA já capturados e devolve um frame novo, sem tocar em Windows nem
// em I/O — o teste usa imagens sintéticas.

const (
	// MaxStitchHeight limita a altura final para não gerar um PNG gigante (a
	// imagem ainda precisa passar pelo teto de payload do encoder).
	MaxStitchHeight = 20000
	// defaultStitchSearchPx é a faixa procurada quando o chamador não informa.
	defaultStitchSearchPx = 240
	// maxStitchSearchPx evita varredura absurda se vier valor da UI. Precisa
	// cobrir a altura de um frame 4K/5K: a rolagem típica deixa a sobreposição
	// perto da tela inteira, e um teto menor faria a costura falhar (duplicando
	// conteúdo) justamente nos monitores grandes.
	maxStitchSearchPx = 8192
	// minStitchOverlap é a menor sobreposição aceita: abaixo disso a coincidência
	// de linhas (barra fixa, fundo liso) é ruído, não continuidade de conteúdo.
	minStitchOverlap = 8
	// fixedBandMaxFraction limita cada faixa fixa (cabeçalho/rodapé) a 1/3 da
	// altura: acima disso o "fixo" engoliria o conteúdo e a costura não teria o
	// que alinhar.
	fixedBandMaxFraction = 3
)

// StitchScrollFrames costura verticalmente os frames na ordem em que foram
// capturados. Para cada frame novo procura a MAIOR faixa de linhas em que o fim
// do acumulado coincide com o topo do novo (tolerando poucas linhas diferentes,
// ex.: cursor/scrollbar) e acrescenta só o restante. Sem sobreposição, concatena
// o frame inteiro (comportamento previsível para conteúdo que não rola).
//
// Devolve o frame costurado e quantos frames entraram na costura (0 quando a
// lista não tem frame utilizável). A altura para em MaxStitchHeight.
func StitchScrollFrames(frames []*screen.Frame, searchPx int) (*screen.Frame, int) {
	valid := make([]*screen.Frame, 0, len(frames))
	for _, f := range frames {
		if frameUsable(f) {
			valid = append(valid, f)
		}
	}
	if len(valid) == 0 {
		return nil, 0
	}
	if searchPx <= 0 {
		searchPx = defaultStitchSearchPx
	}
	if searchPx > maxStitchSearchPx {
		searchPx = maxStitchSearchPx
	}

	// Uma captura de desktop inteiro tem faixas FIXAS (barra de título/abas em
	// cima, taskbar embaixo, papel de parede nas bordas). Nessas linhas a
	// sobreposição "fim do acumulado × topo do próximo" não existe e a costura
	// antiga concatenava os frames inteiros, duplicando a tela. Quando as faixas
	// são detectáveis, costuramos apenas o CONTEÚDO e recolocamos cabeçalho e
	// rodapé uma única vez no final.
	if topFixed, bottomFixed := detectFixedBands(valid[0], valid[1:]); topFixed > 0 || bottomFixed > 0 {
		content := make([]*screen.Frame, 0, len(valid))
		for _, f := range valid {
			contentHeight := f.Height - topFixed - bottomFixed
			contentFrame := copyFrameRows(f, topFixed, contentHeight)
			if contentFrame == nil {
				return nil, 0
			}
			content = append(content, contentFrame)
		}

		// A sobreposição do CONTEÚDO pode ser maior que a janela de busca pedida
		// (a janela costuma vir do tamanho da tela, e o conteúdo já está sem as
		// faixas fixas). Sem cobrir a altura do conteúdo, a costura "não acha"
		// sobreposição e concatena tudo — era o que duplicava a tela.
		contentSearch := searchPx
		if contentSearch < content[0].Height {
			contentSearch = content[0].Height
		}
		if contentSearch > maxStitchSearchPx {
			contentSearch = maxStitchSearchPx
		}

		stitchedContent, used := stitchStack(content, contentSearch)
		if stitchedContent == nil {
			return nil, 0
		}
		return wrapWithBands(valid[0], stitchedContent, topFixed, bottomFixed), used
	}

	return stitchStack(valid, searchPx)
}

// detectFixedBands devolve a altura das faixas fixas (topo e rodapé) comuns aos
// frames. A comparação é por linha na MESMA posição: linha do conteúdo muda de
// lugar quando a rolagem avança, então a faixa fixa termina onde as linhas
// começam a divergir. Sem par de frames (ou quando quase tudo é fixo) devolve
// zeros, e a costura cai no caminho antigo.
func detectFixedBands(first *screen.Frame, rest []*screen.Frame) (topFixed, bottomFixed int) {
	if !frameUsable(first) || len(rest) == 0 || !frameUsable(rest[0]) {
		return 0, 0
	}
	limit := first.Height / fixedBandMaxFraction
	if limit < 1 {
		return 0, 0
	}

	// A banda é fixa só se TODOS os frames comparados concordarem: um cursor ou
	// uma animação em um frame isolado não pode "cortar" o cabeçalho.
	for _, other := range rest {
		if !frameUsable(other) || other.Width != first.Width || other.Height != first.Height {
			return 0, 0
		}
	}

	baseTop := rowHashes(first, 0, limit)
	if baseTop == nil {
		return 0, 0
	}
	for y := 0; y < limit; y++ {
		if !rowMatchesAll(rest, y, baseTop[y]) {
			break
		}
		topFixed++
	}

	baseBottom := rowHashes(first, first.Height-limit, limit)
	if baseBottom == nil {
		return topFixed, 0
	}
	for i := 0; i < limit; i++ {
		rowIndex := first.Height - 1 - i
		if rowIndex < topFixed+minStitchOverlap {
			break
		}
		if !rowMatchesAll(rest, rowIndex, baseBottom[limit-1-i]) {
			break
		}
		bottomFixed++
	}
	// Se o "fixo" sobrar pouco conteúdo, não vale separar bandas: a costura
	// antiga (frame inteiro) evita resultado degenerado.
	if first.Height-topFixed-bottomFixed < 2*minStitchOverlap {
		return 0, 0
	}
	return topFixed, bottomFixed

}

// rowMatchesAll informa se todos os frames têm, na linha y, o hash esperado.
func rowMatchesAll(frames []*screen.Frame, y int, wantHash uint64) bool {
	for _, other := range frames {
		row := packedRow(other, y)
		if row == nil || hashRow(row) != wantHash {
			return false
		}
	}
	return true
}

// wrapWithBands remonta a imagem final: cabeçalho + conteúdo costurado + rodapé,
// respeitando o teto de altura (o excedente é cortado no FIM do conteúdo, nunca
// nas bandas).
func wrapWithBands(first, content *screen.Frame, topFixed, bottomFixed int) *screen.Frame {
	headerHeight := topFixed
	footerHeight := bottomFixed

	budget := MaxStitchHeight - headerHeight - footerHeight
	if budget < 0 {
		budget = 0
	}
	contentHeight := content.Height
	if contentHeight > budget {
		contentHeight = budget
	}

	out := copyFrameRows(first, 0, headerHeight)
	if out == nil {
		out = &screen.Frame{Width: first.Width, Height: 0, Stride: first.Width * 4}
	}
	if contentHeight > 0 {
		out = appendFrameRows(out, content, 0, contentHeight)
	}
	if footerHeight > 0 {
		out = appendFrameRows(out, first, first.Height-footerHeight, footerHeight)
	}
	return out
}

// stitchStack é a costura linha-a-linha original (sobreposição fim do acumulado ×
// topo do próximo). Opera sobre os frames já sem as faixas fixas.
func stitchStack(valid []*screen.Frame, searchPx int) (*screen.Frame, int) {
	firstHeight := valid[0].Height
	if firstHeight > MaxStitchHeight {
		firstHeight = MaxStitchHeight
	}
	stitched := copyFrameRows(valid[0], 0, firstHeight)
	used := 1
	for _, next := range valid[1:] {
		if stitched.Height >= MaxStitchHeight {
			break
		}
		if next.Width != stitched.Width {
			// Frames de larguras diferentes não têm como ser costurados linha a
			// linha; ignorar é melhor que gerar uma imagem com colunas trocadas.
			continue
		}
		overlap := findVerticalOverlap(stitched, next, searchPx)
		if overlap >= next.Height {
			// Frame inteiro já está contido no acumulado (rolagem voltou ou não
			// andou o suficiente): nada a acrescentar.
			used++
			continue
		}
		added := next.Height - overlap
		remaining := MaxStitchHeight - stitched.Height
		truncated := added > remaining
		if truncated {
			added = remaining
		}
		if added > 0 {
			stitched = appendFrameRows(stitched, next, overlap, added)
		}
		used++
		if truncated {
			break
		}
	}
	return stitched, used
}

// FramesIdentical informa se dois frames têm exatamente o mesmo conteúdo
// visível. Usado para encerrar a rolagem quando o conteúdo parou de mudar.
func FramesIdentical(a, b *screen.Frame) bool {
	if a == nil || b == nil || a.Width != b.Width || a.Height != b.Height || a.Width <= 0 || a.Height <= 0 {
		return false
	}
	for y := 0; y < a.Height; y++ {
		rowA := packedRow(a, y)
		rowB := packedRow(b, y)
		if rowA == nil || rowB == nil || !bytes.Equal(rowA, rowB) {
			return false
		}
	}
	return true
}

func frameUsable(f *screen.Frame) bool {
	if f == nil || f.Width <= 0 || f.Height <= 0 {
		return false
	}
	stride := f.Stride
	if stride <= 0 {
		stride = f.Width * 4
	}
	if stride < f.Width*4 {
		return false
	}
	needed := (f.Height-1)*stride + f.Width*4
	return len(f.Data) >= needed
}

func packedRow(f *screen.Frame, y int) []byte {
	if f == nil || y < 0 || y >= f.Height {
		return nil
	}
	stride := f.Stride
	if stride <= 0 {
		stride = f.Width * 4
	}
	start := y * stride
	end := start + f.Width*4
	if start < 0 || end > len(f.Data) {
		return nil
	}
	return f.Data[start:end]
}

func copyFrameRows(f *screen.Frame, fromY, count int) *screen.Frame {
	if !frameUsable(f) || count <= 0 {
		return nil
	}
	if fromY < 0 {
		fromY = 0
	}
	if fromY >= f.Height {
		return nil
	}
	if fromY+count > f.Height {
		count = f.Height - fromY
	}
	width := f.Width
	stride := width * 4
	data := make([]byte, stride*count)
	for y := 0; y < count; y++ {
		row := packedRow(f, fromY+y)
		if row == nil {
			return nil
		}
		copy(data[y*stride:(y+1)*stride], row)
	}
	return &screen.Frame{
		Data:       data,
		Width:      width,
		Height:     count,
		Stride:     stride,
		OriginX:    f.OriginX,
		OriginY:    f.OriginY,
		ColorSpace: f.ColorSpace,
	}
}

func appendFrameRows(dst, src *screen.Frame, fromY, count int) *screen.Frame {
	if dst == nil || !frameUsable(src) || count <= 0 || src.Width != dst.Width {
		return dst
	}
	if fromY < 0 {
		fromY = 0
	}
	if fromY+count > src.Height {
		count = src.Height - fromY
	}
	if count <= 0 {
		return dst
	}
	width := dst.Width
	stride := width * 4
	newHeight := dst.Height + count
	needed := stride * newHeight
	var data []byte
	if cap(dst.Data) >= needed {
		// Reaproveita a capacidade: cada frame novo copiava o acumulado inteiro
		// para um buffer novo (O(n²) de memória em capturas longas).
		data = dst.Data[:needed]
	} else {
		capacity := needed * 2
		if limit := stride * MaxStitchHeight; capacity > limit {
			capacity = limit
		}
		if capacity < needed {
			capacity = needed
		}
		data = make([]byte, needed, capacity)
		copy(data, dst.Data)
	}
	for y := 0; y < count; y++ {
		row := packedRow(src, fromY+y)
		if row == nil {
			return dst
		}
		copy(data[(dst.Height+y)*stride:(dst.Height+y+1)*stride], row)
	}
	return &screen.Frame{
		Data:       data,
		Width:      width,
		Height:     newHeight,
		Stride:     stride,
		OriginX:    dst.OriginX,
		OriginY:    dst.OriginY,
		ColorSpace: dst.ColorSpace,
	}
}

// findVerticalOverlap devolve quantas linhas do TOPO de bottom coincidem com o
// FIM de top. A comparação usa hash por linha (barato) e tolera até 10% de
// linhas diferentes para não falhar com cursor piscando/scrollbar.
func findVerticalOverlap(top, bottom *screen.Frame, searchPx int) int {
	if !frameUsable(top) || !frameUsable(bottom) {
		return 0
	}
	maxK := searchPx
	if maxK > top.Height {
		maxK = top.Height
	}
	if maxK > bottom.Height {
		maxK = bottom.Height
	}
	if maxK <= 0 {
		return 0
	}
	topHashes := rowHashes(top, top.Height-maxK, maxK)
	bottomHashes := rowHashes(bottom, 0, maxK)
	if len(topHashes) != maxK || len(bottomHashes) != maxK {
		return 0
	}
	minK := minStitchOverlap
	if minK > maxK {
		minK = maxK
	}
	// Primeiro a sobreposição EXATA: com conteúdo sintético (e na maioria das
	// páginas) ela existe e é a resposta certa — a segunda passada, tolerante,
	// podia "esticar" a sobreposição em uma linha e cortar conteúdo.
	for k := maxK; k >= minK; k-- {
		offset := maxK - k
		exact := true
		for i := 0; i < k; i++ {
			if topHashes[offset+i] != bottomHashes[i] {
				exact = false
				break
			}
		}
		if exact {
			return k
		}
	}
	bestK, bestScore := 0, 0
	for k := maxK; k >= minK; k-- {
		allowed := k / 10
		offset := maxK - k
		mismatches := 0
		for i := 0; i < k; i++ {
			if topHashes[offset+i] != bottomHashes[i] {
				mismatches++
				if mismatches > allowed {
					break
				}
			}
		}
		if mismatches > allowed {
			continue
		}
		// Penaliza linha diferente sem deixar de preferir sobreposições maiores.
		score := k - mismatches*4
		if score > bestScore {
			bestK, bestScore = k, score
		}
	}
	return bestK
}

func rowHashes(f *screen.Frame, fromY, count int) []uint64 {
	if !frameUsable(f) || count <= 0 {
		return nil
	}
	if fromY < 0 {
		fromY = 0
	}
	if fromY+count > f.Height {
		count = f.Height - fromY
	}
	if count <= 0 {
		return nil
	}
	hashes := make([]uint64, count)
	for y := 0; y < count; y++ {
		row := packedRow(f, fromY+y)
		if row == nil {
			return nil
		}
		hashes[y] = hashRow(row)
	}
	return hashes
}

// hashRow é FNV-1a de 64 bits escrito à mão: evitar hash.Hash por linha elimina
// uma alocação por linha numa varredura que roda a cada frame capturado.
func hashRow(row []byte) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	hash := uint64(offset64)
	for _, b := range row {
		hash ^= uint64(b)
		hash *= prime64
	}
	return hash
}
