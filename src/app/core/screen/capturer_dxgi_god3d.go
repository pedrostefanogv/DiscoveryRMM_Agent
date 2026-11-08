//go:build windows

package screen

import (
	"fmt"
	"image"
	"runtime"
	"unsafe"

	"github.com/kirides/go-d3d/d3d11"
	"github.com/kirides/go-d3d/outputduplication"
)

// ── DXGI Capturer via go-d3d ──
//
// Substitui capturer_dxgi.go (VTable manual) por bindings tipados.
// Usa Snapshot() que internamente:
//   - AcquireNextFrame com timeout
//   - GetFrameDirtyRects (GPU hardware, zero CPU)
//   - CopyResource/CopySubresourceRegion GPU→staging
//   - Map staging→CPU (apenas dirty regions)
//   - ReleaseFrame imediato
//
// ── ATENÇÃO: ESTE É O CAPTURER DE FALLBACK, NÃO O PRIMÁRIO ───────────────────
//
// Quem escolhe o capturer da sessão é remotesession.NewSessionScreenMonitor
// (session_screen.go): o PRIMÁRIO é o GDI (NewGDICapturerMonitor), que faz
// BitBlt do quadro INTEIRO a cada frame e por isso nunca entrega frame parcial.
// Este capturer go-d3d só entra quando o GDI falha de criação, ou no fallback
// dinâmico após 40 falhas consecutivas de captura GDI (maybeFallbackToDXGI).
//
// CONSEQUÊNCIA PARA A INVESTIGAÇÃO DO INCIDENTE: como o GDI é o capturer
// primário, esta correção só entra em cena quando a sessão está no fallback
// DXGI. Ela conserta um bug REAL (delta de dirty rects sobre staging
// recém-criada = frame majoritariamente preto no primeiro acquire, que casa
// exatamente com o sintoma "só carrega quando mexe o mouse"), mas é DEFESA EM
// PROFUNDIDADE: o log do agente da máquina do print (DESKTOP-DH608V7) NÃO está
// disponível neste workspace, então não há como afirmar que aquela sessão
// rodava em DXGI. A causa dominante do primeiro frame incompleto naquela sessão
// foi no PRODUTOR (session_screen.go: o primeiro frame não era marcado como key
// frame e saía como tiles/dirty rects) — corrigida no arquivo do Lead. Os dois
// defeitos são independentes e ambos precisavam de correção.
//
// ── BUG CORRIGIDO AQUI: primeiro frame parcial (staging sujo) ────────────────
//
// Sintoma relatado: ao conectar, o viewer mostra grandes faixas PRETAS / partes
// sem captura; a tela só vai "carregando" conforme o operador MEXE O MOUSE, e
// não há tráfego de rede anormal (o problema é o CONTEÚDO do primeiro frame,
// não banda).
//
// Causa raiz (verificada em .gomodcache/github.com/kirides/go-d3d@v1.0.1/
// outputduplication/output_duplication.go, Snapshot, ~linhas 164-231):
//
//	O OutputDuplicator mantém uma staging texture PERSISTENTE (dup.stagedTex),
//	criada UMA única vez em initializeStage() — ou seja, na primeira chamada de
//	Snapshot() que consegue um frame. A cada Snapshot():
//	  • frameInfo.TotalMetadataBufferSize > 0 e SEM movedRects
//	        → CopySubresourceRegion2D copia do desktop SOMENTE os dirty rects
//	          (linhas 210-218). O RESTANTE da staging mantém o conteúdo anterior.
//	  • há movedRects         → CopyResource2D integral (linha 222). Seguro.
//	  • TotalMetadataBufferSize == 0 → CopyResource2D integral (linha 226). Seguro.
//
//	No PRIMEIRO frame da duplicação, a staging acabou de ser criada: "o conteúdo
//	anterior" é lixo/indefinido (na prática preto). Como o primeiro update do
//	DXGI tipicamente traz poucos dirty rects (relógio da taskbar, ícone da
//	bandeja, cursor), o frame publicado é MAJORITARIAMENTE PRETO com alguns
//	pedaços corretos — exatamente o print do incidente. Cada novo dirty rect
//	(novo movimento do mouse) pinta mais um pedaço, o que explica o "só carrega
//	quando mexe o mouse".
//
//	O contrato do pipeline é: o PRIMEIRO frame de cada duplicação é KEY FRAME,
//	isto é, contém a TELA INTEIRA. O go-d3d viola esse contrato silenciosamente
//	porque não expõe nem a staging nem os dirty rects do frame.
//
// Correção (abaixo, em AcquireNextFrame): no primeiro frame de cada duplicação
// NÃO confiamos no delta. Entregamos o frame via GDI BitBlt (captura atômica do
// framebuffer inteiro) e, do segundo frame em diante, usamos o Snapshot normal
// do DXGI — nesse ponto a staging já foi preenchida pelos deltas acumulados.
//
// Por que não forçar a cópia integral pelo próprio go-d3d: a API pública do
// pacote não dá acesso a nada disso. NewIDXGIOutputDuplication() devolve
// *OutputDuplicator cujos campos (stagedTex, surface, deviceCtx, dirtyRects,
// movedRects, needsSwizzle) são TODOS não exportados; o construtor que escolhe
// o formato (newIDXGIOutputDuplicationFormat) também é privado e está fixado em
// DXGI_FORMAT_R8G8B8A8_UNORM. Os métodos CopyResource2D /
// CopySubresourceRegion2D existem em d3d11.ID3D11DeviceContext e são públicos,
// mas inúteis aqui: sem o *ID3D11Texture2D da staging não há como usá-los.
// Não há getter, nem interface, nem campo exportado para a staging.
//
// Também descartamos a via "buffer acumulado no capturer" (aplicar os dirty
// rects recebidos sobre um acumulado próprio): o Snapshot() do go-d3d NÃO
// devolve os dirty rects (dup.dirtyRects é privado e não há accessor), então o
// capturer não tem como saber QUAIS retângulos foram atualizados.

type dxgiGoD3dCapturer struct {
	dup          *outputduplication.OutputDuplicator
	device       *d3d11.ID3D11Device
	deviceCtx    *d3d11.ID3D11DeviceContext
	img          *image.RGBA
	width        int
	height       int
	originX      int // origem do output no desktop virtual físico (pixels)
	originY      int
	monitorIndex int
	drawCursor   bool // se true, desenha o cursor no frame (compat); false = cursor separado

	// calib resolve a ordem dos canais (RGBA vs BGRA) do frame. A seed vem da
	// sonda determinística do DuplicateOutput1 (capturer_dxgi_format_probe.go)
	// + cache do processo; a calibração por GDI é a verificação/fallback —
	// ver channel_order.go.
	calib channelCalibrator

	// frameBuf é reutilizado entre frames (otimização de churn de GC: antes
	// era um make([]byte, w*4*h) por frame — ~8 MB @1080p a 30fps). Contrato:
	// o Data do Frame é válido ATÉ o próximo AcquireNextFrame; o consumidor
	// (session_screen) copia para o encode worker antes do próximo acquire.
	frameBuf []byte

	// ── Estado do "primeiro frame da duplicação" (ver bug no topo do arquivo) ──
	//
	// firstFrameDone=false significa que a duplicação DXGI ainda não entregou
	// nenhum frame e que a staging texture está recém-criada/indefinida — logo
	// um Snapshot() traria um DELTA parcial sobre lixo. Enquanto for false,
	// AcquireNextFrame() entrega o frame por GDI (quadro inteiro, atômico) e só
	// depois passa a confiar no delta do DXGI.
	//
	// Sempre que a duplicação é recriada (troca de monitor/resolução, perda de
	// acesso DXGI, recriação do capturer) a staging volta a ser indefinida, então
	// quem recria precisa zerar este campo — é o que NewDXGIGoD3dCapturerMode e
	// resetDuplicationState() fazem.
	firstFrameDone bool

	// firstFrameViaGDI registra COMO o primeiro frame foi obtido ("gdi" quando a
	// captura integral deu certo, "dxgi" quando o GDI falhou e caímos de volta no
	// comportamento antigo com log). Existe para diagnóstico e para o método
	// FirstFrameIntegral()/testes — sem isso não há como saber de fora se o
	// primeiro frame entregue era de fato a tela inteira.
	firstFrameViaGDI bool

	// gdiFactory injeta o capturer GDI usado no primeiro frame. Em produção é nil
	// e o capturer abre NewGDICapturerRegion na geometria exata deste output (ver
	// captureFirstFrameGDI); nos testes é substituído por um fake, o que permite
	// cobrir a lógica de estado sem GPU.
	gdiFactory func(x, y, w, h int) (Capturer, error)
}

// NewDXGIGoD3dCapturer cria um capturador DXGI usando go-d3d.
func NewDXGIGoD3dCapturer(monitorIndex int) (Capturer, error) {
	return NewDXGIGoD3dCapturerMode(monitorIndex, true)
}

// NewDXGIGoD3dCapturerMode cria um capturador DXGI usando go-d3d.
// drawCursor=true desenha o cursor no frame (comportamento antigo/compatível);
// drawCursor=false deixa o frame limpo (cursor enviado separadamente pelo viewer).
func NewDXGIGoD3dCapturerMode(monitorIndex int, drawCursor bool) (Capturer, error) {
	runtime.LockOSThread()

	// Cria D3D11 device
	device, deviceCtx, err := d3d11.NewD3D11Device()
	if err != nil {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("go-d3d D3D11CreateDevice: %w", err)
	}

	// ── Ordem de canais: sonda determinística (ANTES de abrir a duplicação) ──
	// O go-d3d esconde em qual caminho caiu (DuplicateOutput1 x fallback
	// DuplicateOutput) — o campo needsSwizzle é privado. A sonda reproduz a
	// mesma decisão (mesmo device, mesmo output, mesmo formato) e diz se os
	// bytes virão RGBA (swap) ou BGRA (nativo). Só depois a duplicação real é
	// aberta: o DXGI não permite duas duplicações do mesmo output no mesmo
	// processo, então a sonda precisa terminar (e liberar) antes.
	seed, probeErr := detectForcedDuplicateFormat(device, monitorIndex)
	if probeErr != nil {
		debugf("sonda de formato DXGI inconclusiva (%v) — a calibração por GDI decide a ordem dos canais", probeErr)
	}

	dup, err := outputduplication.NewIDXGIOutputDuplication(device, deviceCtx, uint(monitorIndex))
	if err != nil {
		device.Release()
		deviceCtx.Release()
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("go-d3d NewIDXGIOutputDuplication: %w", err)
	}

	// Cursor: desenha no frame (compat) ou deixa limpo (cursor separado).
	dup.DrawPointer = drawCursor
	dup.UpdatePointerInfo = drawCursor

	bounds, err := dup.GetBounds()
	if err != nil {
		dup.Release()
		device.Release()
		deviceCtx.Release()
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("go-d3d GetBounds: %w", err)
	}

	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}

	// Origem do output no desktop virtual FÍSICO. O go-d3d não expõe o
	// DesktopCoordinates do IDXGIOutput; identificamos o monitor pelo PAR
	// (largura x altura) reportado pela duplicação — robusto à diferença de
	// ordenação entre EnumOutputs (DXGI) e EnumDisplayMonitors (GDI). Sem
	// match (ex.: rotacionado na troca), assume (0,0) — comportamento anterior.
	originX, originY := 0, 0
	if mons, merr := GetMonitors(); merr == nil {
		for _, m := range mons {
			if m.Width == width && m.Height == height {
				originX, originY = m.X, m.Y
				break
			}
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))

	return &dxgiGoD3dCapturer{
		dup:          dup,
		device:       device,
		deviceCtx:    deviceCtx,
		originX:      originX,
		originY:      originY,
		img:          img,
		width:        width,
		height:       height,
		monitorIndex: monitorIndex,
		drawCursor:   drawCursor,
		// Duplicação nova ⇒ staging nova/indefinida ⇒ o primeiro AcquireNextFrame
		// tem de entregar a tela inteira (ver bug no topo do arquivo). Explícito
		// aqui para que a invariante fique clara a quem criar o capturer no futuro.
		firstFrameDone: false,
		// Cache por processo + seed determinística: o primeiro frame já sai com
		// a cor certa (antes: palpite RGBA até a calibração concluir).
		calib: newChannelCalibratorWithKey(
			channelOrderCacheKey("dxgi-go-d3d", monitorIndex, width, height), seed),
	}, nil
}

// AcquireNextFrame captura o próximo frame.
//
// Garantia de integralidade: o PRIMEIRO frame entregue por esta instância é
// sempre a tela INTEIRA. Isso é obrigatório porque o contrato do pipeline marca
// o primeiro frame de cada duplicação como key frame (useDirtyRect=false no
// consumidor) — um delta parcial sobre staging indefinida quebraria o viewer
// com faixas pretas. Ver o bloco "BUG CORRIGIDO AQUI" no topo do arquivo.
func (c *dxgiGoD3dCapturer) AcquireNextFrame() (*Frame, error) {
	// Primeiro frame desta duplicação: o Snapshot() do go-d3d copiaria apenas os
	// dirty rects para uma staging recém-criada (= lixo/preto no resto). Entrega
	// o quadro inteiro por GDI e só passa a confiar no delta do DXGI a partir do
	// segundo frame, quando a staging já estiver preenchida.
	if !c.firstFrameDone {
		if frame, err := c.captureFirstFrameGDI(); err == nil {
			c.firstFrameDone = true
			c.firstFrameViaGDI = true
			return frame, nil
		} else {
			// GDI indisponível (raro: sessão presa em desktop seguro, VM sem
			// BitBlt). Não há caminho integral alternativo sem a staging: segue o
			// comportamento antigo (frame parcial no primeiro acquire) e registra
			// o motivo — antes isso falhava em silêncio, o que tornou o incidente
			// difícil de diagnosticar.
			debugf("capturer dxgi-go-d3d: primeiro frame via GDI falhou (%v) — entregando o delta do DXGI (pode sair parcial)", err)
			c.firstFrameDone = true
			c.firstFrameViaGDI = false
		}
	}
	return c.snapshotFrame()
}

// captureFirstFrameGDI produz o primeiro frame da duplicação pela captura GDI
// (BitBlt do framebuffer inteiro para uma DIB section, com GdiFlush explícito —
// ver capturer_gdi.go), garantindo conteúdo integral e atômico.
//
// Geometria: usa a REGIÃO DESTE OUTPUT (originX/originY/width/height), não o
// índice de monitor. O GDI identifica monitores por EnumDisplayMonitors e o DXGI
// por EnumOutputs — as ordens podem divergir. Capturar pela região do output
// mantém Geometry()/OriginX/OriginY consistentes com o que o DXGI reporta.
//
// O capturer GDI é aberto sob demanda e fechado ao terminar: dois capturers
// abertos ao mesmo tempo é desperdício (GDI objects) e, em tela de logon, o DC
// errado poderia ser fixado.
func (c *dxgiGoD3dCapturer) captureFirstFrameGDI() (*Frame, error) {
	factory := c.gdiFactory
	if factory == nil {
		factory = func(x, y, w, h int) (Capturer, error) {
			return NewGDICapturerRegion(x, y, w, h)
		}
	}

	gdi, err := factory(c.originX, c.originY, c.width, c.height)
	if err != nil {
		return nil, fmt.Errorf("abrir capturer GDI: %w", err)
	}
	// Close() SEMPRE roda, inclusive nos caminhos de erro abaixo.
	defer gdi.Close()

	src, err := gdi.AcquireNextFrame()
	if err != nil {
		return nil, fmt.Errorf("captura GDI: %w", err)
	}
	gdi.ReleaseFrame()
	if src == nil {
		return nil, fmt.Errorf("captura GDI devolveu frame nulo")
	}

	// ── CÓPIA OBRIGATÓRIA DOS PIXELS ─────────────────────────────────────────
	// O gdiCapturer entrega Data como uma slice SOBRE a memória da DIB section
	// (unsafe.Slice(c.dibBits, ...), ver capturer_gdi.go) e Close() destrói essa
	// DIB (releaseCached → DeleteObject). Devolver o frame do GDI e fechar o
	// capturer em seguida liberaria a memória ANTES de o consumidor lê-la — um
	// use-after-free que já se manifestou como segfault em
	// TestCartaoRealNuncaVazaParaOFrame. Copiamos para um buffer próprio.
	//
	// Este buffer é separado de c.frameBuf de propósito: frameBuf é reutilizado
	// pelo caminho DXGI (snapshotFrame) e o primeiro frame não pode compartilhar
	// armazenamento com os frames seguintes.
	srcSize := src.Stride * src.Height
	if srcSize <= 0 {
		srcSize = src.Width * 4 * src.Height
	}
	if srcSize <= 0 || len(src.Data) < srcSize {
		return nil, fmt.Errorf("captura GDI devolveu frame inválido (%dx%d, %d bytes)", src.Width, src.Height, len(src.Data))
	}
	dst := make([]byte, srcSize)
	copy(dst, src.Data[:srcSize])

	frame := &Frame{
		Data:   dst,
		Width:  src.Width,
		Height: src.Height,
		Stride: src.Stride,
		// Geometria do DXGI como fonte da verdade: um output que mudou de
		// resolução entre a criação deste capturer e agora não pode deslocar o
		// frame publicado nem o mapeamento de mouse do InputController.
		OriginX: c.originX,
		OriginY: c.originY,
	}

	// NÃO passa por c.calib.apply(): o BitBlt para DIB section 32bpp entrega
	// BGRA canônico por construção (ver ChannelOrderPending do gdiCapturer), e a
	// calibração compararia este frame com uma referência GDI do MESMO conteúdo
	// — o que, além de inútil, poderia fixar a ordem errada e contaminar o cache
	// do processo por causa de um único frame.
	//
	// Cursor: mantém o comportamento de drawCursor (mesma regra do caminho DXGI).
	if c.drawCursor {
		c.drawCursorBGRA(frame)
	}
	return frame, nil
}

// FirstFrameIntegral informa se o primeiro frame desta duplicação já foi
// entregue e se ele veio integral (true) ou pelo delta do DXGI (false, só
// quando o GDI falhou). Diagnóstico/testes — o pipeline não depende disso.
func (c *dxgiGoD3dCapturer) FirstFrameIntegral() bool {
	return c.firstFrameDone && c.firstFrameViaGDI
}

// resetDuplicationState volta ao estado "primeiro frame pendente". Deve ser
// chamado SEMPRE que a duplicação DXGI for recriada: a staging texture nasce de
// novo indefinida e o próximo Snapshot voltaria a ser um delta sobre lixo.
func (c *dxgiGoD3dCapturer) resetDuplicationState() {
	c.firstFrameDone = false
	c.firstFrameViaGDI = false
}

// snapshotFrame é o caminho normal (2º frame em diante): Snapshot do go-d3d +
// normalização de canais + cursor. A staging já contém a tela acumulada pelos
// deltas anteriores, então o delta deste frame é seguro.
func (c *dxgiGoD3dCapturer) snapshotFrame() (*Frame, error) {
	// Usa Snapshot() DIRETAMENTE (não GetImage) para evitar o latch de swizzle
	// do go-d3d: GetImage aplica swizzle.BGRA() (BGRA→RGBA) SOMENTE após o
	// primeiro frame sem metadata — antes disso retorna bytes crus. Isso fazia
	// o frame alternar entre formatos de forma imprevisível → cores invertidas.
	//
	// IMPORTANTE — formato de saída do Snapshot():
	// O go-d3d cria o duplicator via NewIDXGIOutputDuplication, que força
	// DXGI_FORMAT_R8G8B8A8_UNORM (RGBA) através de DuplicateOutput1. No
	// caminho principal (Win10/11 com DPI awareness PerMonitorV2), o
	// Snapshot() devolve a staging herdando esse formato → bytes RGBA.
	// Somente no fallback (DuplicateOutput, sem suporte a formato forçado) o
	// desktop nativo é entregue, tipicamente B8G8R8A8_UNORM (BGRA).
	//
	// Os encoders (jpeg/webp/tile) e os demais capturers (dxgi manual, GDI)
	// assumem BGRA. Para manter o contrato uniforme do Frame e evitar a
	// inversão R↔B (verde ok; vermelho→azul, azul→vermelho), normalizamos o
	// frame para BGRA a seguir.
	unmap, mappedRect, size, err := c.dup.Snapshot(200)
	if err != nil {
		if err == outputduplication.ErrNoImageYet {
			return nil, fmt.Errorf("timeout")
		}
		return nil, fmt.Errorf("go-d3d Snapshot: %w", err)
	}
	defer unmap()

	// Copia linha a linha respeitando o pitch (padding entre linhas).
	dataSize := int(mappedRect.Pitch) * int(size.Y)
	data := unsafe.Slice((*byte)(mappedRect.PBits), dataSize)
	contentWidth := int(size.X) * 4
	dataWidth := int(mappedRect.Pitch)

	// Buffer reutilizável: elimina ~8 MB de alocação por frame (otimização
	// de churn de GC). O consumidor copia antes do próximo acquire.
	frameDataSize := contentWidth * int(size.Y)
	if cap(c.frameBuf) < frameDataSize {
		c.frameBuf = make([]byte, frameDataSize)
	}
	frame := &Frame{
		Data:    c.frameBuf[:frameDataSize],
		Width:   int(size.X),
		Height:  int(size.Y),
		Stride:  contentWidth,
		OriginX: c.originX,
		OriginY: c.originY,
	}
	var src, dst int
	for i := 0; i < int(size.Y); i++ {
		copy(frame.Data[dst:], data[src:src+contentWidth])
		src += dataWidth
		dst += contentWidth
	}

	// Normaliza a ordem de canais para BGRA. NÃO assumimos mais que o Snapshot
	// saiu em RGBA: quando a go-d3d cai no fallback DuplicateOutput, o frame vem
	// no formato NATIVO do desktop (BGRA) e o swap cego invertia R↔B (bug de
	// cor relatado em VM Windows 10). O calibrador compara o frame com uma
	// captura GDI e decide swap/no-swap por máquina — ver channel_order.go.
	c.calib.apply(frame)

	if c.drawCursor {
		// Desenha o cursor sobre o frame capturado (BGRA) — igual ao
		// GetImage com DrawPointer, mas preservando o formato BGRA.
		c.drawCursorBGRA(frame)
	}

	return frame, nil
}

// Geometry retorna a origem e dimensões do output no desktop virtual físico.
func (c *dxgiGoD3dCapturer) Geometry() (x, y, w, h int) {
	return c.originX, c.originY, c.width, c.height
}

// swapRB_BGRA converte um buffer RGBA (R,G,B,A) para BGRA (B,G,R,A) trocando
// os bytes 0 e 2 de cada pixel, preservando G e A. Compatível com o mesmo
// swap feito pelos encoders (jpeg/webp) — idempotente se aplicado de novo.
func swapRB_BGRA(buf []byte) {
	// Buffer vazio ou não alinhado: não há pixels para trocar.
	if len(buf) < 4 {
		return
	}
	// Aproveita chunk de 4 bytes (uint32) quando o comprimento for múltiplo
	// de 4 (sempre verdade para buffers RGBA sem padding).
	if len(buf)%4 == 0 {
		pixels := unsafe.Slice((*uint32)(unsafe.Pointer(&buf[0])), len(buf)/4)
		for i := range pixels {
			c := pixels[i]
			// RGBA como uint32 LE = 0xAABBGGRR (bytes [R,G,B,A]).
			// BGRA como uint32 LE = 0xAARRGGBB (bytes [B,G,R,A]).
			// Mantém G (bits 8-15) e A (bits 24-31); troca R (0-7) e B (16-23).
			pixels[i] = (c & 0xFF00FF00) | // A e G nos lugares certos
				((c & 0x000000FF) << 16) | // R → posição de B
				((c & 0x00FF0000) >> 16) // B → posição de R
		}
		return
	}
	// Fallback byte-a-byte (padding atípico).
	for i := 0; i+3 < len(buf); i += 4 {
		buf[i], buf[i+2] = buf[i+2], buf[i]
	}
}

// drawCursorBGRA desenha o cursor do sistema sobre um frame BGRA.
// Equivalente ao dup.DrawPointer do go-d3d, mas operando sobre []byte BGRA
// (não image.RGBA), preservando o formato esperado pelos encoders.
func (c *dxgiGoD3dCapturer) drawCursorBGRA(frame *Frame) {
	// O go-d3d não expõe a forma do cursor fora do drawPointer interno.
	// No modo cursor separado (padrão), drawCursor=false e este caminho não
	// é executado. Para compatibilidade, desenha um cursor básico na posição
	// atual do mouse via GetCursorPos.
	info, err := GetCursorPos()
	if err != nil {
		return
	}
	// Cursor frame-relativo: GetCursorPos retorna coordenadas do desktop
	// virtual; o frame cobre a região iniciando em (originX, originY). Sem
	// subtrair a origem, o cursor desloca em capturas de monitor não-primário.
	x := int(info.X) - c.originX
	y := int(info.Y) - c.originY
	if x < 0 || y < 0 || x >= frame.Width || y >= frame.Height {
		return
	}

	// Seta simples (12x12) em branco com contorno — BGRA
	path := [][2]int{
		{0, 0}, {0, 12}, {1, 12}, {1, 1}, {12, 1}, {12, 0},
	}
	for _, p := range path {
		px, py := x+p[0], y+p[1]
		if px < 0 || py < 0 || px >= frame.Width || py >= frame.Height {
			continue
		}
		off := py*frame.Stride + px*4
		frame.Data[off] = 0xff
		frame.Data[off+1] = 0xff
		frame.Data[off+2] = 0xff
		frame.Data[off+3] = 0xff
	}
}

func (c *dxgiGoD3dCapturer) ReleaseFrame() {
	// GetImage já chamou ReleaseFrame() internamente — no-op.
}

func (c *dxgiGoD3dCapturer) Close() error {
	if c.dup != nil {
		c.dup.Release()
		c.dup = nil
	}
	if c.device != nil {
		c.device.Release()
		c.device = nil
	}
	if c.deviceCtx != nil {
		c.deviceCtx.Release()
		c.deviceCtx = nil
	}
	// Balanceia o LockOSThread do construtor — essencial quando o capturer
	// é trocado dinamicamente (DXGI↔GDI em troca de desktop): sem isso, cada
	// troca deixaria um lock residual no thread do loop de captura.
	runtime.UnlockOSThread()
	return nil
}

func (c *dxgiGoD3dCapturer) Name() string {
	return "dxgi-go-d3d"
}

// ChannelOrderPending informa se a ordem dos canais (R↔B) ainda não foi
// decidida. O loop de captura usa isso para segurar a PUBLICAÇÃO até o gate
// (ver session_screen.go) — publicar antes entrega cores invertidas.
func (c *dxgiGoD3dCapturer) ChannelOrderPending() bool {
	return !c.calib.Resolved()
}

// ChannelOrderName devolve a ordem efetiva atual, para log de diagnóstico.
func (c *dxgiGoD3dCapturer) ChannelOrderName() string {
	return c.calib.Order().String() + " (" + c.calib.SeedFrom() + ")"
}
