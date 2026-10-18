//go:build windows

package screen

import (
	"math"
	"time"
)

// ── Ordem dos canais de cor (R/B) e calibração automática ─────────────────────
//
// Contrato do pipeline de captura: Frame.Data é BGRA (B,G,R,A) e os encoders
// (JPEG, WebP e tiles) fazem BGRA→RGBA na hora de codificar. Se um capturer
// entregar bytes RGBA, o viewer mostra as cores com R↔B trocado — céu azul
// vira bege/amarelo, ícones azuis viram laranja — exatamente o bug observado
// em 2026-10-18 numa VM Windows 10 (build 19045).
//
// Origem do problema: o capturer go-d3d (capturer_dxgi_god3d.go) aplicava swap
// RGBA→BGRA INCONDICIONAL, assumindo que IOutputDuplication::DuplicateOutput1
// sempre aceita DXGI_FORMAT_R8G8B8A8_UNORM. Quando ele cai no fallback
// DuplicateOutput (o caso comum em VM/serviço sem manifest PerMonitorV2), o
// frame vem no formato NATIVO do desktop (B8G8R8A8 = BGRA) e o swap extra
// inverte R↔B. A biblioteca go-d3d sabe disso (campo needsSwizzle), mas ele é
// privado — não dá para consultar de fora.
//
// Solução: calibração automática. O GDI (GetDIBits 32bpp) é referência
// confiável BGRA; comparamos o frame candidato com uma captura GDI do MESMO
// monitor e escolhemos a interpretação com menor erro. Decide sozinho, por
// máquina/driver, e não depende de detalhe interno da dependência.

// ChannelOrder é a ordem dos canais observada nos bytes de um frame.
type ChannelOrder int

const (
	// ChannelOrderUnknown = não foi possível decidir (tela acromática,
	// conteúdo mudou entre as capturas, referência indisponível).
	ChannelOrderUnknown ChannelOrder = iota
	// ChannelOrderBGRA = bytes já estão em BGRA (contrato do pipeline).
	ChannelOrderBGRA
	// ChannelOrderRGBA = bytes estão em RGBA e precisam de swap R↔B.
	ChannelOrderRGBA
)

func (o ChannelOrder) String() string {
	switch o {
	case ChannelOrderBGRA:
		return "BGRA"
	case ChannelOrderRGBA:
		return "RGBA"
	default:
		return "desconhecida"
	}
}

const (
	// Grade de amostragem: custo O(1) independente da resolução.
	probeGridX = 64
	probeGridY = 36

	// probeMinSamples: amostras INFORMATIVAS mínimas (pixels claros e com
	// diferença R↔B real) para decidir.
	probeMinSamples = 96

	// probeMinRBVariance: variância mínima de (R-B) na referência. Sem isso não
	// existe sinal para distinguir as hipóteses (tela acromática/cinza).
	probeMinRBVariance = 64.0

	// probeMinContentCorrelation: correlação mínima do canal G entre candidato
	// e referência. G não muda com swap R↔B, então é o teste de "é a mesma
	// imagem". Baixa correlação = conteúdo mudou entre as capturas → não decidir.
	probeMinContentCorrelation = 0.5

	// probeDifferentialCorrelation: correlação entre (R-B) do candidato e
	// (R-B) da referência. ~+1 = candidato já é BGRA; ~-1 = candidato é RGBA
	// (swap). É invariante a diferença de luminância/gama entre capturers — que
	// é o que torna o teste robusto no parque real (HDR, color profiles).
	probeDifferentialCorrelation = 0.5
)

// pearson devolve o coeficiente de correlação de Pearson entre xs e ys.
func pearson(xs, ys []float64) float64 {
	if len(xs) != len(ys) || len(xs) < 2 {
		return 0
	}
	n := float64(len(xs))
	var sx, sy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
	}
	mx, my := sx/n, sy/n
	var num, vx, vy float64
	for i := range xs {
		dx, dy := xs[i]-mx, ys[i]-my
		num += dx * dy
		vx += dx * dx
		vy += dy * dy
	}
	if vx == 0 || vy == 0 {
		return 0
	}
	return num / math.Sqrt(vx*vy)
}

// probeChannelOrder decide a ordem de canais de candidate comparando com uma
// referência confiável em BGRA (captura GDI). Função pura — testável sem GPU.
//
// Estratégia: o swap R↔B inverte o sinal de (R-B) e não toca em G. Então:
//  1. G do candidato x G da referência precisa correlacionar (mesma imagem);
//  2. a correlação entre (R-B) dos dois diz a ordem: +1 = BGRA, -1 = RGBA.
//
// Comparar (R-B) em vez de valores absolutos elimina a diferença de
// luminância/gama entre capturers (go-d3d x GDI, HDR, perfil de cor do monitor).
func probeChannelOrder(candidate, reference *Frame) ChannelOrder {
	if candidate == nil || reference == nil {
		return ChannelOrderUnknown
	}
	w := min(candidate.Width, reference.Width)
	h := min(candidate.Height, reference.Height)
	if w <= 0 || h <= 0 {
		return ChannelOrderUnknown
	}

	stepX := max(1, w/probeGridX)
	stepY := max(1, h/probeGridY)

	candG := make([]float64, 0, probeGridX*probeGridY)
	refG := make([]float64, 0, probeGridX*probeGridY)
	candDiff := make([]float64, 0, probeGridX*probeGridY)
	refDiff := make([]float64, 0, probeGridX*probeGridY)

	for y := 0; y < h; y += stepY {
		cRow := y * candidate.Stride
		rRow := y * reference.Stride
		for x := 0; x < w; x += stepX {
			co := cRow + x*4
			ro := rRow + x*4
			if co+3 >= len(candidate.Data) || ro+3 >= len(reference.Data) {
				continue
			}
			candB := float64(candidate.Data[co])
			candGv := float64(candidate.Data[co+1])
			candR := float64(candidate.Data[co+2])
			refB := float64(reference.Data[ro])
			refGv := float64(reference.Data[ro+1])
			refR := float64(reference.Data[ro+2])

			// Amostras escuras (região ainda não populada pelo capturer) e sem
			// contraste R↔B não informam ordem de canais.
			if refR < 8 && refGv < 8 && refB < 8 {
				continue
			}
			diff := refR - refB
			if math.Abs(diff) < 6 {
				continue
			}

			candG = append(candG, candGv)
			refG = append(refG, refGv)
			candDiff = append(candDiff, candR-candB)
			refDiff = append(refDiff, diff)
		}
	}
	if len(refDiff) < probeMinSamples {
		return ChannelOrderUnknown
	}
	if variance(refDiff) < probeMinRBVariance {
		return ChannelOrderUnknown
	}
	// A mesma imagem? Sem G correlacionado, o frame veio de outro momento.
	if pearson(candG, refG) < probeMinContentCorrelation {
		return ChannelOrderUnknown
	}
	dCorr := pearson(candDiff, refDiff)
	switch {
	case dCorr >= probeDifferentialCorrelation:
		return ChannelOrderBGRA
	case dCorr <= -probeDifferentialCorrelation:
		return ChannelOrderRGBA
	}
	return ChannelOrderUnknown
}

// variance devolve a variância populacional de xs.
func variance(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var sum float64
	for _, v := range xs {
		sum += v
	}
	mean := sum / float64(len(xs))
	var acc float64
	for _, v := range xs {
		d := v - mean
		acc += d * d
	}
	return acc / float64(len(xs))
}

const (
	// channelWarmupFrames: frames ignorados antes da primeira tentativa. Os
	// primeiros frames do go-d3d podem ter só as dirty rects copiadas (buffer
	// de staging recém-criado) — comparar isso dá falso negativo.
	channelWarmupFrames = 3
	// channelFastAttempts: tentativas em cadência rápida (~4s). Depois disso a
	// calibração continua em modo lento — a sessão NÃO fica presa numa ordem
	// errada só porque a tela estava em cinza/preto no início.
	channelFastAttempts = 20
	// channelCalibrateMinInterval: espaça as tentativas rápidas (a tela precisa
	// mudar para o frame vir completo e o conteúdo bater com a referência).
	channelCalibrateMinInterval = 200 * time.Millisecond
	// channelCalibrateSlowInterval: cadência após esgotar as tentativas rápidas.
	channelCalibrateSlowInterval = 10 * time.Second
)

// channelCalibrator resolve, uma única vez por sessão de captura, a ordem de
// canais entregue pelo capturer e normaliza o frame para BGRA.
type channelCalibrator struct {
	// order é a ordem EFETIVA aplicada. Antes da calibração vale o padrão do
	// capturer (preserva o comportamento anterior até provar o contrário).
	order       ChannelOrder
	resolved    bool
	warmup      int
	attempts    int
	lastAttempt time.Time
	disabled    bool
}

func newChannelCalibrator(defaultOrder ChannelOrder) channelCalibrator {
	return channelCalibrator{order: defaultOrder}
}

// apply normaliza frame.Data para BGRA (contrato dos encoders), calibrando na
// primeira oportunidade. Frames HDR (float16/scRGB) não são tocados.
func (cc *channelCalibrator) apply(frame *Frame) {
	if frame == nil || len(frame.Data) < 4 || frame.ColorSpace != 0 {
		return
	}
	if !cc.resolved {
		cc.tryResolve(frame)
	}
	if cc.order == ChannelOrderRGBA {
		swapRB_BGRA(frame.Data)
	}
}

// tryResolve compara o frame candidato com uma captura GDI (referência BGRA) e
// fixa a ordem dos canais. Erros são silenciosos: mantém o padrão do capturer.
func (cc *channelCalibrator) tryResolve(candidate *Frame) {
	if cc.disabled {
		return
	}
	if cc.warmup < channelWarmupFrames {
		cc.warmup++
		return
	}
	interval := channelCalibrateMinInterval
	if cc.attempts >= channelFastAttempts {
		interval = channelCalibrateSlowInterval
	}
	if !cc.lastAttempt.IsZero() && time.Since(cc.lastAttempt) < interval {
		return
	}
	if cc.attempts == channelFastAttempts {
		debugf("calibração de canais ainda inconclusiva — seguindo em modo lento (10s) com %s", cc.order)
	}
	cc.attempts++
	cc.lastAttempt = time.Now()

	// GDI como referência: GetDIBits 32bpp entrega BGRA canônico.
	//
	// Usa a GEOMETRIA DO FRAME (região), não o índice de monitor: evita
	// EnumDisplayMonitors/syscall.NewCallback a cada tentativa (o callback
	// registrado é permanente no runtime e o limite é ~2000 por processo).
	gdi, err := NewGDICapturerRegion(candidate.OriginX, candidate.OriginY, candidate.Width, candidate.Height)
	if err != nil {
		cc.disabled = true
		debugf("calibração de canais desativada (GDI indisponível: %v) — mantendo %s", err, cc.order)
		return
	}
	reference, err := gdi.AcquireNextFrame()
	if err != nil {
		gdi.ReleaseFrame()
		_ = gdi.Close()
		return
	}
	order := probeChannelOrder(candidate, reference)
	gdi.ReleaseFrame()
	_ = gdi.Close()

	if order == ChannelOrderUnknown {
		return // tela acromática ou conteúdo mudou: tenta no próximo frame
	}
	cc.order = order
	cc.resolved = true
	if order == ChannelOrderBGRA {
		debugf("ordem de canais detectada: BGRA (nativo do desktop) — sem swap R↔B")
	} else {
		debugf("ordem de canais detectada: RGBA — aplicando swap R↔B para BGRA")
	}
}
