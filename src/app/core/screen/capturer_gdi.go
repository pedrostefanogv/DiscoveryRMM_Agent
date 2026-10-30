//go:build windows

package screen

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// ── GDI Screen Capture (BitBlt + GetDIBits) ──
// Fallback para sistemas sem DXGI (VM sem GPU, RDP, Windows 7/8).

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetDC            = user32.NewProc("GetDC")
	procReleaseDC        = user32.NewProc("ReleaseDC")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")

	gdi32                      = syscall.NewLazyDLL("gdi32.dll")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	// GdiFlush: o GDI pode enfileirar o BitBlt; sem o flush a DIB section pode
	// ser lida antes de a blit ser executada (linha parcialmente atualizada).
	procGdiFlush = gdi32.NewProc("GdiFlush")
	// procGetDIBits é usado pela captura de cursor (cursor_sprite.go). O
	// caminho de captura de tela NÃO usa mais GetDIBits — ver comentário em
	// gdiCapturer.AcquireNextFrame.
	procGetDIBits = gdi32.NewProc("GetDIBits")
)

const (
	SM_CXSCREEN    = 0
	SM_CYSCREEN    = 1
	SRCCOPY        = 0x00CC0020
	DIB_RGB_COLORS = 0
	BI_RGB         = 0
)

type gdiCapturer struct {
	// NÃO guardamos o screenDC entre frames: o MeshAgent (tile.cpp
	// get_desktop_buffer) faz ReleaseDC+GetDC(NULL) a CADA frame justamente
	// porque o DC fica inválido quando o desktop ativo muda (logon, UAC,
	// lock). Guardar o DC da criação produzia tela congelada após a troca.
	width        int
	height       int
	offsetX      int // origem do monitor no desktop virtual (multi-monitor)
	offsetY      int
	monitorIndex int

	// Cache de GDI objects: recriados apenas quando a geometria muda. Não
	// referenciam o desktop — apenas o screenDC obtido por frame importa para o
	// BitBlt.
	//
	// O bitmap é uma DIB SECTION (CreateDIBSection), que expõe o ponteiro dos
	// pixels em memória de sistema: o BitBlt escreve DIRETO no buffer que será
	// entregue ao pipeline, sem GetDIBits. Motivo (incidente de 2026-10-29 em
	// DESKTOP-DH608V7): GetDIBits falhou 40× seguidas nessa máquina, o capturer
	// GDI foi descartado no meio da sessão e o pipeline caiu no DXGI go-d3d —
	// que estava com a ordem de canais errada. Eliminar o GetDIBits remove a
	// causa raiz do fallback (e é mais rápido: um memcpy a menos por frame).
	memDC     uintptr
	dibBitmap uintptr        // HBITMAP (DIB section) selecionado no memDC
	dibBits   unsafe.Pointer // pixels da DIB (top-down, BGRA 32bpp)
	dibPrev   uintptr        // objeto que estava selecionado no memDC antes da DIB

	// Throttle da re-detecção de geometria. GetMonitors() usa
	// syscall.NewCallback, que registra um callback PERMANENTE no runtime
	// (limite ~2000 por processo) — chamar a cada frame esgotaria os slots
	// em ~1 minuto e crashearia o processo. Re-detecta a cada 5s.
	lastGeoCheck time.Time
}

// NewGDICapturer cria um capturador GDI do monitor primário.
func NewGDICapturer() (Capturer, error) {
	return NewGDICapturerMonitor(0)
}

// NewGDICapturerMonitor cria um capturador GDI de um monitor específico.
// monitorIndex 0 = primário. Usa EnumDisplayMonitors para localizar a região.
func NewGDICapturerMonitor(monitorIndex int) (Capturer, error) {
	runtime.LockOSThread()

	// Localiza a região do monitor desejado (0 = primário).
	mons, err := GetMonitors()
	if err != nil || len(mons) == 0 {
		// Fallback: desktop virtual inteiro
		return newGDICapturerRegion(0, 0, 0, 0)
	}

	if monitorIndex < 0 || monitorIndex >= len(mons) {
		monitorIndex = 0
	}
	m := mons[monitorIndex]

	return newGDICapturerRegionIdx(m.X, m.Y, m.Width, m.Height, monitorIndex)
}

// NewGDICapturerRegion cria um capturador GDI de uma região arbitrária do
// desktop virtual (coordenadas FÍSICAS). É usado pela captura de tela avulsa
// (app/core/screenshot) para print de tela cheia/região sem montar um loop de
// captura. width/height devem ser > 0.
func NewGDICapturerRegion(offsetX, offsetY, width, height int) (Capturer, error) {
	return newGDICapturerRegion(offsetX, offsetY, width, height)
}

// newGDICapturerRegion cria o capturer GDI capturando a região (offsetX, offsetY, width, height).
// Se width/height == 0, captura o desktop virtual inteiro.
func newGDICapturerRegion(offsetX, offsetY, width, height int) (Capturer, error) {
	return newGDICapturerRegionIdx(offsetX, offsetY, width, height, -1)
}

// newGDICapturerRegionIdx cria o capturer GDI com índice de monitor para
// re-detectar a geometria a cada frame (resolução pode mudar).
func newGDICapturerRegionIdx(offsetX, offsetY, width, height, monitorIndex int) (Capturer, error) {
	if width <= 0 || height <= 0 {
		w, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
		h, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)
		width, height = int(w), int(h)
		offsetX, offsetY = 0, 0
	}

	// Valida que o DC do desktop é acessível já na criação (erro cedo e
	// claro em vez de falhar silenciosamente a cada frame).
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("GetDC falhou")
	}
	procReleaseDC.Call(0, screenDC)

	return &gdiCapturer{
		width:        width,
		height:       height,
		offsetX:      offsetX,
		offsetY:      offsetY,
		monitorIndex: monitorIndex,
	}, nil
}

func (c *gdiCapturer) AcquireNextFrame() (*Frame, error) {
	// ── Padrão MeshAgent (tile.cpp get_desktop_buffer) ──
	// O DC do desktop é re-adquirido A CADA frame: "We need to do this in
	// case the current desktop changes". Quando o desktop ativo muda
	// (logon/UAC/lock), o DC antigo referencia o desktop anterior — BitBlt
	// continuaria lendo a tela congelada. Com GetDC(NULL) por frame (após o
	// SetThreadDesktop do CheckDesktopSwitch no loop de captura), o DC
	// referencia sempre o desktop ATUAL.
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC falhou")
	}
	defer procReleaseDC.Call(0, screenDC)

	// Re-detecta a geometria do monitor COM THROTTLE (5s): a resolução pode
	// mudar (rotação, DPI, monitor reconectado) e o desktop de logon pode
	// ter geometria diferente do desktop do usuário. NÃO chamar GetMonitors
	// por frame — syscall.NewCallback vaza slots de callback e crasha o
	// processo (ver comentário na struct).
	if c.monitorIndex >= 0 && time.Since(c.lastGeoCheck) >= 5*time.Second {
		c.lastGeoCheck = time.Now()
		if mons, err := GetMonitors(); err == nil && len(mons) > c.monitorIndex {
			m := mons[c.monitorIndex]
			if m.Width != c.width || m.Height != c.height || m.X != c.offsetX || m.Y != c.offsetY {
				c.width, c.height, c.offsetX, c.offsetY = m.Width, m.Height, m.X, m.Y
				// Geometria mudou → descarta cache de GDI objects.
				c.releaseCached()
			}
		}
	}

	// Cache de memDC/DIB: cria uma vez e reusa (recria quando a geometria muda
	// via releaseCached acima). O bitmap NÃO referencia o desktop — a origem
	// (screenDC) é re-adquirida por frame.
	if err := c.ensureDIB(screenDC); err != nil {
		return nil, err
	}

	r, _, _ := procBitBlt.Call(c.memDC, 0, 0, uintptr(c.width), uintptr(c.height), screenDC, uintptr(c.offsetX), uintptr(c.offsetY), SRCCOPY)
	if r == 0 {
		// BitBlt pode falhar em troca de desktop — descarta o cache para
		// recriar os objetos no próximo frame (possível geometria nova).
		c.releaseCached()
		return nil, fmt.Errorf("BitBlt falhou")
	}

	// O GDI pode enfileirar a blit; sem o flush a DIB poderia ser lida antes de
	// a cópia terminar (frame parcialmente atualizado).
	procGdiFlush.Call()

	// Buffer reutilizável: expõe os pixels da DIB (memória de sistema escrita
	// pelo BitBlt) sem copiar — o consumidor copia para o encode worker antes
	// do próximo AcquireNextFrame (mesmo contrato do go-d3d). Elimina o
	// GetDIBits (que falhava nesta máquina) e ~8 MB de memcpy por frame.
	bufSize := c.width * c.height * 4
	frameData := unsafe.Slice((*byte)(c.dibBits), bufSize)

	// NOTA (contrato de cor): o BitBlt da tela para uma DIB section 32bpp
	// entrega BGRA canônico (B,G,R,A) — é o formato que os encoders
	// (jpeg/webp/tiles) esperam. Este caminho NÃO aplica swap R↔B, por isso o
	// capturer reporta ChannelOrderPending() == false (ver channel_order.go).
	return &Frame{
		Data:    frameData,
		Width:   c.width,
		Height:  c.height,
		Stride:  c.width * 4,
		OriginX: c.offsetX,
		OriginY: c.offsetY,
	}, nil
}

// ensureDIB garante memDC + DIB section do tamanho atual (recriados quando a
// geometria muda). A DIB section expõe os pixels diretamente em memória de
// sistema (dibBits), permitindo BitBlt direto no buffer entregue ao pipeline.
func (c *gdiCapturer) ensureDIB(screenDC uintptr) error {
	if c.memDC != 0 && c.dibBitmap != 0 && c.dibBits != nil {
		return nil
	}
	c.releaseCached()

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return fmt.Errorf("CreateCompatibleDC falhou")
	}

	// BITMAPINFO: header de 40 bytes. biHeight NEGATIVO = top-down (linha 0 no
	// topo), que é a ordem esperada por Frame.Stride/pipeline.
	var bi [40]byte
	bi[0] = 40 // biSize
	*(*int32)(unsafe.Pointer(&bi[4])) = int32(c.width)
	*(*int32)(unsafe.Pointer(&bi[8])) = -int32(c.height) // negativo = top-down
	*(*uint16)(unsafe.Pointer(&bi[12])) = 1              // biPlanes
	*(*uint16)(unsafe.Pointer(&bi[14])) = 32             // biBitCount
	*(*uint32)(unsafe.Pointer(&bi[16])) = BI_RGB         // biCompression

	var bits unsafe.Pointer
	hbm, _, _ := procCreateDIBSection.Call(
		screenDC,
		uintptr(unsafe.Pointer(&bi[0])),
		DIB_RGB_COLORS,
		uintptr(unsafe.Pointer(&bits)),
		0, // hSection: memória do sistema
		0, // offset
	)
	if hbm == 0 || bits == nil {
		procDeleteDC.Call(memDC)
		return fmt.Errorf("CreateDIBSection falhou (%dx%d)", c.width, c.height)
	}
	prev, _, _ := procSelectObject.Call(memDC, hbm)

	c.memDC = memDC
	c.dibBitmap = hbm
	c.dibBits = bits
	c.dibPrev = prev
	return nil
}

// Geometry retorna a origem e dimensões atuais da região capturada
// (desktop virtual físico). Re-detectado no loop de captura a cada 5s
// (lastGeoCheck), então reflete mudanças de resolução/DPI com atraso máximo
// de um ciclo — o InputController recebe a origem a cada frame publicado.
func (c *gdiCapturer) Geometry() (x, y, w, h int) {
	return c.offsetX, c.offsetY, c.width, c.height
}

func (c *gdiCapturer) ReleaseFrame() {}

// releaseCached descarta o cache de GDI objects (uso interno — chamado na
// troca de geometria/falha de BitBlt; o thread já é o do capturador).
func (c *gdiCapturer) releaseCached() {
	if c.memDC != 0 && c.dibPrev != 0 {
		// Deseleciona a DIB do DC ANTES de destruí-la (DeleteObject de um
		// bitmap selecionado falha).
		procSelectObject.Call(c.memDC, c.dibPrev)
		c.dibPrev = 0
	}
	if c.dibBitmap != 0 {
		procDeleteObject.Call(c.dibBitmap)
		c.dibBitmap = 0
	}
	c.dibBits = nil
	if c.memDC != 0 {
		procDeleteDC.Call(c.memDC)
		c.memDC = 0
	}
}

func (c *gdiCapturer) Close() error {
	c.releaseCached()
	runtime.UnlockOSThread()
	return nil
}

func (c *gdiCapturer) Name() string { return "gdi" }

// ChannelOrderPending é sempre false no GDI: o BitBlt para uma DIB section
// 32bpp entrega BGRA canônico por construção (não há formato forçado nem
// fallback de driver). O pipeline não precisa esperar/calibrar este capturer.
func (c *gdiCapturer) ChannelOrderPending() bool { return false }

// ChannelOrderName informa a ordem efetiva (diagnóstico/log).
func (c *gdiCapturer) ChannelOrderName() string { return ChannelOrderBGRA.String() + " (gdi)" }

var _ Capturer = (*gdiCapturer)(nil)
