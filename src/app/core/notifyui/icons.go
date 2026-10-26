package notifyui

import (
	"strconv"
	"strings"
)

// ── Ícones vetoriais do cartão ──
//
// Os glifos do "motivo" da notificação (informação/aviso/erro/pergunta) são os
// paths oficiais do Google Material Symbols (24x24, licença Apache 2.0) —
// vetoriais, então escalam perfeitamente com o DPI. O parser flatten os paths
// (M/L/H/V/C/S/Q/T/Z) em polilinhas que o GDI+ preenche com antialiasing.

// Kinds de ícone aceitos no CardRequest.Icon.
const (
	IconInfo     = "info"
	IconWarning  = "warning"
	IconError    = "error"
	IconQuestion = "question"
)

// normalizeIconKind devolve um kind canônico ("" vira info).
func normalizeIconKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case IconWarning, "warn", "aviso":
		return IconWarning
	case IconError, "erro":
		return IconError
	case IconQuestion, "pergunta":
		return IconQuestion
	default:
		return IconInfo
	}
}

// viewBox do Material Symbols: as fontes variáveis do Google exportam em
// unidades de 24px escaladas por 40 (coordenadas -960..960).
const (
	iconViewBoxMinX = 0
	iconViewBoxMinY = -960
	iconViewBoxSize = 960
)

// glyphChat / glyphDesktop: glifos extras usados pelos demais cartões nativos
// (convite do chat de suporte, consentimento de acesso remoto), padronizados
// no MESMO conjunto Material Symbols.
const (
	GlyphChat    = "chat"
	GlyphDesktop = "desktop_windows"
)

// materialGlyphs são os paths OFICIAIS do Google Material Symbols
// (https://fonts.google.com/icons — estilo Rounded, FILL@1, 24px, Apache 2.0),
// baixados de material-design-icons/symbols/web. O parser não cobre arcos
// porque os glifos oficiais usam apenas M/L/H/V/C/S/Q/T/Z.
var materialGlyphs = map[string]string{
	GlyphChat:    "m240-240-92 92q-19 19-43.5 8.5T80-177v-623q0-33 23.5-56.5T160-880h640q33 0 56.5 23.5T880-800v480q0 33-23.5 56.5T800-240H240Zm40-160h240q17 0 28.5-11.5T560-440q0-17-11.5-28.5T520-480H280q-17 0-28.5 11.5T240-440q0 17 11.5 28.5T280-400Zm0-120h400q17 0 28.5-11.5T720-560q0-17-11.5-28.5T680-600H280q-17 0-28.5 11.5T240-560q0 17 11.5 28.5T280-520Zm0-120h400q17 0 28.5-11.5T720-680q0-17-11.5-28.5T680-720H280q-17 0-28.5 11.5T240-680q0 17 11.5 28.5T280-640Z",
	GlyphDesktop: "M400-200v-80H160q-33 0-56.5-23.5T80-360v-400q0-33 23.5-56.5T160-840h640q33 0 56.5 23.5T880-760v400q0 33-23.5 56.5T800-280H560v80h40q17 0 28.5 11.5T640-160q0 17-11.5 28.5T600-120H360q-17 0-28.5-11.5T320-160q0-17 11.5-28.5T360-200h40Z",
	IconInfo: "M480-280q17 0 28.5-11.5T520-320v-160q0-17-11.5-28.5T480-520q-17 0-28.5 11.5T440-480v160q0 17 11.5 28.5T480-280Zm0-320q17 0 28.5-11.5T520-640q0-17-11.5-28.5T480-680q-17 0-28.5 11.5T440-640q0 17 11.5 28.5T480-600Zm0 520q-83 0-156-31.5T197-197q-54-54-85.5-127T80-480q0-83 31.5-156T197-763q54-54 127-85.5T480-880q83 0 156 31.5T763-763q54 54 85.5 127T880-480q0 83-31.5 156T763-197q-54 54-127 85.5T480-80Z",
	IconWarning: "M109-120q-11 0-20-5.5T75-140q-5-9-5.5-19.5T75-180l370-640q6-10 15.5-15t19.5-5q10 0 19.5 5t15.5 15l370 640q6 10 5.5 20.5T885-140q-5 9-14 14.5t-20 5.5H109Zm371-120q17 0 28.5-11.5T520-280q0-17-11.5-28.5T480-320q-17 0-28.5 11.5T440-280q0 17 11.5 28.5T480-240Zm0-120q17 0 28.5-11.5T520-400v-120q0-17-11.5-28.5T480-560q-17 0-28.5 11.5T440-520v120q0 17 11.5 28.5T480-360Z",
	IconError: "M480-280q17 0 28.5-11.5T520-320q0-17-11.5-28.5T480-360q-17 0-28.5 11.5T440-320q0 17 11.5 28.5T480-280Zm0-160q17 0 28.5-11.5T520-480v-160q0-17-11.5-28.5T480-680q-17 0-28.5 11.5T440-640v160q0 17 11.5 28.5T480-440Zm0 360q-83 0-156-31.5T197-197q-54-54-85.5-127T80-480q0-83 31.5-156T197-763q54-54 127-85.5T480-880q83 0 156 31.5T763-763q54 54 85.5 127T880-480q0 83-31.5 156T763-197q-54 54-127 85.5T480-80Z",
	IconQuestion: "M478-240q21 0 35.5-14.5T528-290q0-21-14.5-35.5T478-340q-21 0-35.5 14.5T428-290q0 21 14.5 35.5T478-240Zm2 160q-83 0-156-31.5T197-197q-54-54-85.5-127T80-480q0-83 31.5-156T197-763q54-54 127-85.5T480-880q83 0 156 31.5T763-763q54 54 85.5 127T880-480q0 83-31.5 156T763-197q-54 54-127 85.5T480-80Zm4-572q25 0 43.5 16t18.5 40q0 22-13.5 39T502-525q-23 20-40.5 44T444-427q0 14 10.5 23.5T479-394q15 0 25.5-10t13.5-25q4-21 18-37.5t30-31.5q23-22 39.5-48t16.5-58q0-51-41.5-83.5T484-720q-38 0-72.5 16T359-655q-7 12-4.5 25.5T368-609q14 8 29 5t25-17q11-15 27.5-23t34.5-8Z",
}

// iconSubpath é uma polilinha (em coordenadas do viewBox, 0..960 com Y
// negativo) fechada na pintura; subpaths dentro de subpaths viram furos
// (fill alternate).
type iconSubpath = []iconPoint

type iconPoint struct {
	X, Y float64
}

// iconCurveSegments controla o flatten das curvas de Bézier (por segmento de
// curva — 16 dividem suficientemente glifos de 24px para qualquer DPI).
const iconCurveSegments = 16

// parseGlyph flatten o path de um glifo/kind em polilinhas. Pure function
// (testável sem Windows). Nome desconhecido devolve nil.
func parseGlyph(name string) []iconSubpath {
	d, ok := materialGlyphs[name]
	if !ok {
		return nil
	}
	return flattenSVGPath(d)
}

// parseIconSubpaths mantém o nome antigo (kinds do CardRequest.Icon).
func parseIconSubpaths(kind string) []iconSubpath {
	return parseGlyph(normalizeIconKind(kind))
}

// flattenSVGPath converte um path SVG (subset M/L/H/V/C/S/Q/T/Z) em subpaths
// poligonais absolutos.
func flattenSVGPath(d string) []iconSubpath {
	var (
		subpaths []iconSubpath
		current  iconSubpath
		pen      iconPoint
		// start guarda o ponto inicial do subpath corrente (para Z).
		start           iconPoint
		lastControl     iconPoint
		lastWasCurve    bool
		cmd             byte
		nums []float64
	)

	flush := func() {
		if len(current) >= 3 {
			subpaths = append(subpaths, current)
		}
		current = nil
	}
	ensureSubpath := func() {
		if current == nil {
			current = iconSubpath{}
			start = pen
		}
	}
	absPoint := func(x, y float64) {
		ensureSubpath()
		pen = iconPoint{X: x, Y: y}
		current = append(current, pen)
	}

	// tokenizar: comandos e números.
	i := 0
	n := len(d)
	nextNumber := func() (float64, bool) {
		for i < n && (d[i] == ' ' || d[i] == ',' || d[i] == '\t' ||
			d[i] == '\n' || d[i] == '\r') {
			i++
		}
		j := i
		if j < n && (d[j] == '-' || d[j] == '+') {
			j++
		}
		dot := false
		for j < n && (d[j] >= '0' && d[j] <= '9' || d[j] == '.') {
			if d[j] == '.' {
				if dot {
					break // próximo número começou no '.'
				}
				dot = true
			}
			j++
		}
		if j == i {
			return 0, false
		}
		v, err := strconv.ParseFloat(d[i:j], 64)
		i = j
		if err != nil {
			return 0, false
		}
		return v, true
	}
	readArgs := func(count int) ([]float64, bool) {
		nums = nums[:0]
		for k := 0; k < count; k++ {
			v, ok := nextNumber()
			if !ok {
				return nil, false
			}
			nums = append(nums, v)
		}
		return nums, true
	}

	for i < n {
		c := d[i]
		switch {
		case c == ' ' || c == ',' || c == '\t' || c == '\n' || c == '\r':
			i++
			continue
		case (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.':
			// Repetição implícita do último comando.
		default:
			cmd = c
			i++
			if cmd == 'z' || cmd == 'Z' {
				ensureSubpath()
				if len(current) > 0 {
					current = append(current, start)
				}
				flush()
				pen = start
				lastWasCurve = false
				continue
			}
		}

		switch cmd {
		case 'M', 'm':
			args, ok := readArgs(2)
			if !ok {
				return subpaths
			}
			flush()
			if cmd == 'm' {
				pen = iconPoint{X: pen.X + args[0], Y: pen.Y + args[1]}
			} else {
				pen = iconPoint{X: args[0], Y: args[1]}
			}
			start = pen
			current = iconSubpath{pen}
			// Depois do primeiro m, pares seguintes viram linhas: troca o cmd.
			cmd = 'L'
			if d[i-1] == 'm' {
				cmd = 'l'
			}
			lastWasCurve = false
		case 'L', 'l':
			args, ok := readArgs(2)
			if !ok {
				return subpaths
			}
			x, y := args[0], args[1]
			if cmd == 'l' {
				x += pen.X
				y += pen.Y
			}
			absPoint(x, y)
			lastWasCurve = false
		case 'H', 'h':
			args, ok := readArgs(1)
			if !ok {
				return subpaths
			}
			x := args[0]
			if cmd == 'h' {
				x += pen.X
			}
			absPoint(x, pen.Y)
			lastWasCurve = false
		case 'V', 'v':
			args, ok := readArgs(1)
			if !ok {
				return subpaths
			}
			y := args[0]
			if cmd == 'v' {
				y += pen.Y
			}
			absPoint(pen.X, y)
			lastWasCurve = false
		case 'C', 'c':
			args, ok := readArgs(6)
			if !ok {
				return subpaths
			}
			x1, y1, x2, y2, x, y := args[0], args[1], args[2], args[3], args[4], args[5]
			if cmd == 'c' {
				x1 += pen.X
				y1 += pen.Y
				x2 += pen.X
				y2 += pen.Y
				x += pen.X
				y += pen.Y
			}
			current = flattenCubic(current, pen, iconPoint{x1, y1}, iconPoint{x2, y2}, iconPoint{x, y})
			lastControl = iconPoint{x2, y2}
			pen = iconPoint{x, y}
			ensureSubpath()
			current = append(current, pen)
			lastWasCurve = true
		case 'S', 's':
			args, ok := readArgs(4)
			if !ok {
				return subpaths
			}
			x2, y2, x, y := args[0], args[1], args[2], args[3]
			if cmd == 's' {
				x2 += pen.X
				y2 += pen.Y
				x += pen.X
				y += pen.Y
			}
			x1, y1 := pen.X, pen.Y
			if lastWasCurve {
				x1 = 2*pen.X - lastControl.X
				y1 = 2*pen.Y - lastControl.Y
			}
			current = flattenCubic(current, pen, iconPoint{x1, y1}, iconPoint{x2, y2}, iconPoint{x, y})
			lastControl = iconPoint{x2, y2}
			pen = iconPoint{x, y}
			ensureSubpath()
			current = append(current, pen)
			lastWasCurve = true
		case 'Q', 'q':
			args, ok := readArgs(4)
			if !ok {
				return subpaths
			}
			x1, y1, x, y := args[0], args[1], args[2], args[3]
			if cmd == 'q' {
				x1 += pen.X
				y1 += pen.Y
				x += pen.X
				y += pen.Y
			}
			current = flattenQuadratic(current, pen, iconPoint{x1, y1}, iconPoint{x, y})
			lastControl = iconPoint{x1, y1}
			pen = iconPoint{x, y}
			ensureSubpath()
			current = append(current, pen)
			lastWasCurve = true
		case 'T', 't':
			args, ok := readArgs(2)
			if !ok {
				return subpaths
			}
			x, y := args[0], args[1]
			if cmd == 't' {
				x += pen.X
				y += pen.Y
			}
			x1, y1 := pen.X, pen.Y
			if lastWasCurve {
				x1 = 2*pen.X - lastControl.X
				y1 = 2*pen.Y - lastControl.Y
			}
			current = flattenQuadratic(current, pen, iconPoint{x1, y1}, iconPoint{x, y})
			lastControl = iconPoint{x1, y1}
			pen = iconPoint{x, y}
			ensureSubpath()
			current = append(current, pen)
			lastWasCurve = true
		default:
			// Comando desconhecido (arco etc.): aborta com o que há.
			return subpaths
		}
	}
	flush()
	return subpaths
}

// flattenCubic devolve o subpath com os pontos interpolados da cúbica.
func flattenCubic(out iconSubpath, p0, p1, p2, p3 iconPoint) iconSubpath {
	for k := 1; k < iconCurveSegments; k++ {
		t := float64(k) / iconCurveSegments
		out = append(out, cubicAt(p0, p1, p2, p3, t))
	}
	return out
}

func cubicAt(p0, p1, p2, p3 iconPoint, t float64) iconPoint {
	u := 1 - t
	a := u * u * u
	b := 3 * u * u * t
	c := 3 * u * t * t
	d := t * t * t
	return iconPoint{
		X: a*p0.X + b*p1.X + c*p2.X + d*p3.X,
		Y: a*p0.Y + b*p1.Y + c*p2.Y + d*p3.Y,
	}
}

// flattenQuadratic devolve o subpath com os pontos interpolados da quadrática.
func flattenQuadratic(out iconSubpath, p0, p1, p2 iconPoint) iconSubpath {
	for k := 1; k < iconCurveSegments; k++ {
		t := float64(k) / iconCurveSegments
		u := 1 - t
		a := u * u
		b := 2 * u * t
		c := t * t
		out = append(out, iconPoint{
			X: a*p0.X + b*p1.X + c*p2.X,
			Y: a*p0.Y + b*p1.Y + c*p2.Y,
		})
	}
	return out
}

