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
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
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

	// Cache de GDI objects (memDC/memBitmap): recriados apenas quando a
	// geometria muda. Não referenciam o desktop — apenas o screenDC obtido
	// por frame importa para o BitBlt.
	memDC     uintptr
	memBitmap uintptr

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

	// Cache de memDC/memBitmap: cria uma vez e reusa (recria quando a
	// geometria muda via releaseCached acima). O bitmap NÃO referencia o
	// desktop — a origem (screenDC) é re-adquirida por frame.
	if c.memDC == 0 {
		memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
		if memDC == 0 {
			return nil, fmt.Errorf("CreateCompatibleDC falhou")
		}
		memBitmap, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(c.width), uintptr(c.height))
		if memBitmap == 0 {
			procDeleteDC.Call(memDC)
			return nil, fmt.Errorf("CreateCompatibleBitmap falhou")
		}
		procSelectObject.Call(memDC, memBitmap)
		c.memDC = memDC
		c.memBitmap = memBitmap
	}

	r, _, _ := procBitBlt.Call(c.memDC, 0, 0, uintptr(c.width), uintptr(c.height), screenDC, uintptr(c.offsetX), uintptr(c.offsetY), SRCCOPY)
	if r == 0 {
		// BitBlt pode falhar em troca de desktop — descarta o cache para
		// recriar os objetos no próximo frame (possível geometria nova).
		c.releaseCached()
		return nil, fmt.Errorf("BitBlt falhou")
	}

	bufSize := c.width * c.height * 4
	frameData := make([]byte, bufSize)

	// BITMAPINFO header
	var bi [40]byte
	bi[0] = 40 // biSize
	*(*int32)(unsafe.Pointer(&bi[4])) = int32(c.width)
	*(*int32)(unsafe.Pointer(&bi[8])) = -int32(c.height) // negativo = top-down
	*(*uint16)(unsafe.Pointer(&bi[12])) = 1              // biPlanes
	*(*uint16)(unsafe.Pointer(&bi[14])) = 32             // biBitCount
	*(*uint32)(unsafe.Pointer(&bi[16])) = BI_RGB

	r, _, _ = procGetDIBits.Call(screenDC, c.memBitmap, 0, uintptr(c.height),
		uintptr(unsafe.Pointer(&frameData[0])),
		uintptr(unsafe.Pointer(&bi[0])),
		DIB_RGB_COLORS)
	if r == 0 {
		return nil, fmt.Errorf("GetDIBits falhou")
	}

	// NOTA (cores invertidas, bug 2026-09-07): o 1º parâmetro de GetDIBits
	// DEVE ser o DC da tela (screenDC), NÃO o memDC. A documentação exige que
	// o bitmap NÃO esteja selecionado no DC passado — e o memBitmap está
	// selecionado no memDC. Passar memDC é comportamento indefinido: em
	// vários drivers o resultado sai com canais R/B corrompidos/invertidos.
	// O MeshAgent (tile.cpp get_desktop_buffer) passa hDesktopDC exatamente
	// por isso. Com screenDC, GetDIBits entrega BGRA canônico (B,G,R,A),
	// que é o contrato esperado pelos encoders (jpeg/webp/tiles).

	return &Frame{Data: frameData, Width: c.width, Height: c.height, Stride: c.width * 4}, nil
}

func (c *gdiCapturer) ReleaseFrame() {}

// releaseCached descarta o cache de GDI objects (uso interno — chamado na
// troca de geometria/falha de BitBlt; o thread já é o do capturador).
func (c *gdiCapturer) releaseCached() {
	if c.memBitmap != 0 {
		procDeleteObject.Call(c.memBitmap)
		c.memBitmap = 0
	}
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

var _ Capturer = (*gdiCapturer)(nil)
