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

// materialIconPaths são os paths (viewBox 24x24) do Material Symbols preenchido.
var materialIconPaths = map[string]string{
	IconInfo: "M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z",
	IconWarning: "M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z",
	IconError: "M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z",
	IconQuestion: "M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 17h-2v-2h2v2zm2.07-7.75l-.9.92C13.45 12.9 13 13.5 13 15h-2v-.5c0-1.1.45-2.1 1.17-2.83l1.24-1.26c.37-.36.59-.86.59-1.41 0-1.1-.9-2-2-2s-2 .9-2 2H8c0-2.21 1.79-4 4-4s4 1.79 4 4c0 .88-.36 1.68-.93 2.25z",
}

// iconSubpath é uma polilinha (em coordenadas do viewBox 24x24) fechada na
// pintura; subpaths dentro de subpaths viram furos (fill alternate).
type iconSubpath = []iconPoint

type iconPoint struct {
	X, Y float64
}

// iconCurveSegments controla o flatten das curvas de Bézier (por segmento de
// curva — 16 dividem suficientemente glifos de 24px para qualquer DPI).
const iconCurveSegments = 16

// parseIconSubpaths flatten o path de um kind em polilinhas. Pure function
// (testável sem Windows). Kind desconhecido devolve nil.
func parseIconSubpaths(kind string) []iconSubpath {
	d, ok := materialIconPaths[kind]
	if !ok {
		return nil
	}
	return flattenSVGPath(d)
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

