//go:build windows

package screen

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// ── Ordem dos canais de cor (R/B) e calibração automática ─────────────────────
//
// Contrato do pipeline de captura: Frame.Data é BGRA (B,G,R,A) e os encoders
// (JPEG, WebP e tiles) fazem BGRA→RGBA na hora de codificar. Se um capturer
// entregar bytes RGBA, o viewer mostra as cores com R↔B trocado — céu azul
// vira bege/amarelo, ícones azuis viram laranja.
//
// Duas fontes de decisão, nesta ordem:
//
//  1. DETERMINÍSTICA (preferida): quem cria a duplicação DXGI SABE se
//     IDXGIOutput5::DuplicateOutput1 aceitou o formato forçado.
//     - go-d3d força R8G8B8A8: aceitou ⇒ bytes RGBA ⇒ swap;
//       recusou (fallback DuplicateOutput) ⇒ formato NATIVO do desktop
//       (B8G8R8A8 = BGRA) ⇒ sem swap.
//     - DXGI manual força B8G8R8A8: aceitou ⇒ bytes BGRA ⇒ sem swap;
//       recusou ⇒ formato nativo (indefinido) ⇒ calibrar.
//     Ver capturer_dxgi.go (forcedFormat) e capturer_dxgi_god3d.go
//     (detectForcedDuplicateFormat).
//
//  2. CALIBRAÇÃO (verificação/fallback): compara o frame candidato com uma
//     captura GDI do MESMO monitor (GetDIBits 32bpp é referência BGRA
//     confiável) e escolhe a interpretação com menor erro. Também é o que
//     corrige sessões em que a decisão determinística não está disponível
//     (dependência que esconde o formato) ou divergiu do comportamento real.
//
// Incidente que motivou a reestruturação (log de 2026-10-29T23:35,
// DESKTOP-DH608V7, agent 1.3.0): o capturer go-d3d assumia RGBA por padrão e
// publicava frames com swap ATÉ a calibração concluir. Como o DuplicateOutput1
// caiu no fallback (VM/Win10), o resultado foi ~68 s de cores invertidas por
// sessão (23:35:01,5 → 23:36:09,9) e a "cura" somente quando a calibração
// terminou. Correções aplicadas: decisão determinística + cache por processo +
// gate de publicação no loop de captura (session_screen.go) + referência GDI
// confiável (CreateDIBSection em capturer_gdi.go).

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
func probeChannelOrder(candidate, reference *Frame) ChannelOrder {
	order, _ := probeChannelOrderDetailed(candidate, reference)
	return order
}

// probeChannelOrderDetailed é probeChannelOrder devolvendo também o MOTIVO da
// decisão (ou da indecisão). O motivo vai para o log: sem ele, uma sessão que
// ficou 68 s em modo lento não diz se o problema foi referência GDI
// indisponível, tela acromática ou conteúdo divergente.
//
// Estratégia: o swap R↔B inverte o sinal de (R-B) e não toca em G. Então:
//  1. G do candidato x G da referência precisa correlacionar (mesma imagem);
//  2. a correlação entre (R-B) dos dois diz a ordem: +1 = BGRA, -1 = RGBA.
//
// Comparar (R-B) em vez de valores absolutos elimina a diferença de
// luminância/gama entre capturers (go-d3d x GDI, HDR, perfil de cor do monitor).
func probeChannelOrderDetailed(candidate, reference *Frame) (ChannelOrder, string) {
	if candidate == nil || reference == nil {
		return ChannelOrderUnknown, "frame candidato ou referência ausente"
	}
	w := min(candidate.Width, reference.Width)
	h := min(candidate.Height, reference.Height)
	if w <= 0 || h <= 0 {
		return ChannelOrderUnknown, "geometria inválida (frame vazio)"
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
		return ChannelOrderUnknown, fmt.Sprintf("poucas amostras informativas (%d < %d)", len(refDiff), probeMinSamples)
	}
	if v := variance(refDiff); v < probeMinRBVariance {
		return ChannelOrderUnknown, fmt.Sprintf("pouco contraste R↔B na referência (variância %.1f < %.0f)", v, probeMinRBVariance)
	}
	// A mesma imagem? Sem G correlacionado, o frame veio de outro momento.
	if c := pearson(candG, refG); c < probeMinContentCorrelation {
		return ChannelOrderUnknown, fmt.Sprintf("conteúdo divergente entre capturas (corr G %.2f < %.2f)", c, probeMinContentCorrelation)
	}
	dCorr := pearson(candDiff, refDiff)
	switch {
	case dCorr >= probeDifferentialCorrelation:
		return ChannelOrderBGRA, fmt.Sprintf("corr (R-B) %.2f ⇒ BGRA", dCorr)
	case dCorr <= -probeDifferentialCorrelation:
		return ChannelOrderRGBA, fmt.Sprintf("corr (R-B) %.2f ⇒ RGBA (swap)", dCorr)
	}
	return ChannelOrderUnknown, fmt.Sprintf("corr (R-B) %.2f inconclusiva", dCorr)
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

	// channelLogAttemptsAlways: quantas tentativas inconclusivas são logadas
	// sempre; depois disso o log passa a ser 1 a cada channelLogAttemptEvery
	// (evita inundar o agent-service.log numa sessão de horas).
	channelLogAttemptsAlways = 3
	channelLogAttemptEvery   = 10

	// channelVerifyMaxAttempts: quando já existe uma decisão determinística
	// (DuplicateOutput1/cache), a calibração vira VERIFICAÇÃO e para após estas
	// tentativas — não pode custar uma captura GDI por 200 ms a sessão inteira.
	// Se contradisser a seed, a ordem é corrigida (self-heal); se não conseguir
	// decidir (tela acromática/GDI falhando), a seed permanece.
	channelVerifyMaxAttempts = 5
)

// ── Cache por processo da ordem resolvida ────────────────────────────────────
//
// A calibração é cara (captura GDI de referência) e o capturer é recriado em
// trocas GDI↔DXGI (DXGI_ERROR_ACCESS_LOST, mudança de resolução/DPI/monitor).
// Sem cache, CADA recriação voltava ao palpite padrão e podia reabrir a janela
// de cores invertidas no meio da sessão. O cache é por PROCESSO (worker de
// sessão): se o driver mudar de comportamento, o worker é recriado.
var channelOrderCache sync.Map // string -> ChannelOrder

// channelOrderCacheKey identifica a decisão por motor de captura + monitor +
// resolução. A resolução entra porque uma mudança de modo pode trocar o
// caminho da duplicação.
func channelOrderCacheKey(kind string, monitorIndex, width, height int) string {
	return fmt.Sprintf("%s|mon=%d|%dx%d", kind, monitorIndex, width, height)
}

// seedChannelOrder devolve a ordem já conhecida para a chave (se houver).
func seedChannelOrder(key string) (ChannelOrder, bool) {
	if key == "" {
		return ChannelOrderUnknown, false
	}
	if v, ok := channelOrderCache.Load(key); ok {
		if order, ok := v.(ChannelOrder); ok && order != ChannelOrderUnknown {
			return order, true
		}
	}
	return ChannelOrderUnknown, false
}

// rememberChannelOrder grava a decisão para as próximas instâncias do mesmo
// capturer neste processo.
func rememberChannelOrder(key string, order ChannelOrder) {
	if key == "" || order == ChannelOrderUnknown {
		return
	}
	channelOrderCache.Store(key, order)
}

// resetChannelOrderCache limpa o cache (usado por testes).
func resetChannelOrderCache() {
	channelOrderCache.Range(func(k, _ any) bool {
		channelOrderCache.Delete(k)
		return true
	})
}

// channelCalibrator resolve, uma única vez por sessão de captura, a ordem de
// canais entregue pelo capturer e normaliza o frame para BGRA.
//
// Semântica de estado:
//   - order    = melhor interpretação conhecida (seed determinística/verificada);
//   - resolved = ordem VERIFICADA (determinística ou por calibração/cache).
//
// Enquanto !resolved o frame ainda é normalizado com order (para não vazar
// bytes trocados se a seed estiver certa) e o loop de captura segura a
// PUBLICAÇÃO (ChannelOrderPending) até o deadline do gate.
type channelCalibrator struct {
	order       ChannelOrder
	resolved    bool // ordem decidida (seed determinística/cache/calibração)
	verified    bool // calibração já confirmou (ou desistiu de confirmar)
	warmup      int
	attempts    int
	lastAttempt time.Time
	disabled    bool
	// cacheKey identifica a decisão neste processo (ver channelOrderCacheKey).
	// Vazio = não cachear.
	cacheKey string
	// seedFrom descreve a origem da seed ("probe", "cache", "palpite").
	seedFrom string
	// lastReason guarda o motivo da última tentativa inconclusiva (diagnóstico).
	lastReason string
	// refFn permite injetar a referência BGRA em testes (nil = GDI real).
	refFn func(frame *Frame) (*Frame, error)
}

func newChannelCalibrator(defaultOrder ChannelOrder) channelCalibrator {
	return channelCalibrator{order: defaultOrder, seedFrom: "palpite"}
}

// newChannelCalibratorWithKey cria o calibrador semeado pela decisão
// determinística do capturer (seed) e/ou pelo cache do processo.
//
// Ordem de precedência: cache (já verificado) > seed determinística > palpite.
// Com cache/seed a ordem é publicada imediatamente com a cor certa; a
// verificação por calibração continua apenas quando NÃO há decisão
// determinística (seed == ChannelOrderUnknown).
func newChannelCalibratorWithKey(cacheKey string, seed ChannelOrder) channelCalibrator {
	cc := channelCalibrator{cacheKey: cacheKey, order: ChannelOrderBGRA, seedFrom: "palpite"}
	if order, ok := seedChannelOrder(cacheKey); ok {
		cc.order = order
		cc.resolved = true
		cc.verified = true // decisão de outra instância deste processo
		cc.seedFrom = "cache"
		return cc
	}
	if seed != ChannelOrderUnknown {
		cc.order = seed
		cc.resolved = true
		cc.seedFrom = "probe"
		// A decisão determinística vale para este processo: grava no cache para
		// que trocas de capturer (GDI↔DXGI) e sessões seguintes do mesmo worker
		// não dependam de refazer a sonda (que pode falhar com o output em uso).
		rememberChannelOrder(cacheKey, seed)
		return cc
	}
	return cc
}

// Resolved informa se a ordem efetiva já foi decidida (seed, cache ou
// calibração). A verificação subsequente NÃO volta a pendente — o gate de
// publicação só espera a primeira decisão.
func (cc *channelCalibrator) Resolved() bool { return cc.resolved }

// Order devolve a ordem efetiva atual (seed enquanto não verificada).
func (cc *channelCalibrator) Order() ChannelOrder { return cc.order }

// SeedFrom descreve a origem da decisão atual (diagnóstico).
func (cc *channelCalibrator) SeedFrom() string { return cc.seedFrom }

// apply normaliza frame.Data para BGRA (contrato dos encoders), calibrando na
// primeira oportunidade. Frames HDR (float16/scRGB) não são tocados.
func (cc *channelCalibrator) apply(frame *Frame) {
	if frame == nil || len(frame.Data) < 4 || frame.ColorSpace != 0 {
		return
	}
	if !cc.verified {
		cc.tryResolve(frame)
	}
	if cc.order == ChannelOrderRGBA {
		swapRB_BGRA(frame.Data)
	}
}

// tryResolve compara o frame candidato com uma captura GDI (referência BGRA) e
// fixa a ordem dos canais. Erros não interrompem o pipeline, mas o motivo vai
// para o log (throttled) — foi a falta disso que tornou o incidente de
// 2026-10-29 difícil de diagnosticar.
func (cc *channelCalibrator) tryResolve(candidate *Frame) {
	if cc.disabled || cc.verified {
		return
	}
	// Modo verificação: já há ordem decidida (seed determinística/cache) e a
	// calibração só confere — com orçamento curto de tentativas.
	verifying := cc.resolved

	if cc.warmup < channelWarmupFrames {
		cc.warmup++
		return
	}
	if verifying && cc.attempts >= channelVerifyMaxAttempts {
		cc.verified = true
		debugf("verificação da ordem de canais (%s) encerrada sem confirmação (%s) — mantendo %s",
			cc.seedFrom, cc.lastReason, cc.order)
		return
	}
	interval := channelCalibrateMinInterval
	if !verifying && cc.attempts >= channelFastAttempts {
		interval = channelCalibrateSlowInterval
		if cc.attempts == channelFastAttempts {
			debugf("calibração de canais ainda inconclusiva (%s) — seguindo em modo lento (10s) com %s",
				cc.lastReason, cc.order)
		}
	}
	if !cc.lastAttempt.IsZero() && time.Since(cc.lastAttempt) < interval {
		return
	}
	cc.attempts++
	cc.lastAttempt = time.Now()

	// GDI como referência: GetDIBits 32bpp entrega BGRA canônico.
	//
	// Usa a GEOMETRIA DO FRAME (região), não o índice de monitor: evita
	// EnumDisplayMonitors/syscall.NewCallback a cada tentativa (o callback
	// registrado é permanente no runtime e o limite é ~2000 por processo).
	var reference *Frame
	var err error
	if cc.refFn != nil {
		reference, err = cc.refFn(candidate)
	} else {
		gdi, gerr := NewGDICapturerRegion(candidate.OriginX, candidate.OriginY, candidate.Width, candidate.Height)
		if gerr != nil {
			// GDI nem existe nesta máquina: desiste da calibração (e da
			// verificação) — o palpite/seed permanece e o log registra o porquê.
			cc.disabled = true
			cc.verified = true
			cc.lastReason = "GDI indisponível: " + gerr.Error()
			debugf("calibração de canais desativada (%s) — mantendo %s", cc.lastReason, cc.order)
			return
		}
		reference, err = gdi.AcquireNextFrame()
		gdi.ReleaseFrame()
		_ = gdi.Close()
	}
	if err != nil {
		// Falha transitória (ex.: troca de desktop): tenta de novo no próximo
		// frame — o motivo fica registrado para a próxima linha de log.
		cc.lastReason = "referência GDI falhou: " + err.Error()
		cc.logAttempt()
		return
	}
	order, reason := probeChannelOrderDetailed(candidate, reference)

	if order == ChannelOrderUnknown {
		// tela acromática ou conteúdo mudou: tenta no próximo frame
		cc.lastReason = reason
		cc.logAttempt()
		return
	}

	if verifying {
		if order == cc.order {
			cc.verified = true
			debugf("ordem de canais %s confirmada pela calibração (%s)", cc.order, reason)
			return
		}
		// A seed determinística foi CONTRARRESTADA pela calibração: corrige
		// (self-heal) em vez de confiar cegamente na sonda.
		debugf("ordem de canais: seed %s (%s) contrarrestada pela calibração (%s) — aplicando %s",
			cc.order, cc.seedFrom, reason, order)
		cc.order = order
		cc.resolved = true
		cc.verified = true
		cc.seedFrom = "calibração (corrigiu a seed)"
		rememberChannelOrder(cc.cacheKey, order)
		return
	}

	cc.order = order
	cc.resolved = true
	cc.seedFrom = "calibração"
	rememberChannelOrder(cc.cacheKey, order)
	switch order {
	case ChannelOrderBGRA:
		debugf("ordem de canais detectada: BGRA (nativo do desktop) — sem swap R↔B (%s)", reason)
	default:
		debugf("ordem de canais detectada: RGBA — aplicando swap R↔B para BGRA (%s)", reason)
	}
}

// logAttempt registra o motivo de uma tentativa inconclusiva, com throttle.
func (cc *channelCalibrator) logAttempt() {
	if cc.attempts > channelLogAttemptsAlways && cc.attempts%channelLogAttemptEvery != 0 {
		return
	}
	debugf("calibração de canais: tentativa %d inconclusiva — %s (mantendo %s, seed=%s, chave=%s)",
		cc.attempts, cc.lastReason, cc.order, cc.seedFrom, cc.cacheKey)
}
