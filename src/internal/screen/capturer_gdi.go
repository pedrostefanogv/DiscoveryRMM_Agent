//go:build windows

package screen

import (
	"fmt"
	"runtime"
	"syscall"
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
	SM_CXSCREEN = 0
	SM_CYSCREEN = 1
	SRCCOPY     = 0x00CC0020
	DIB_RGB_COLORS = 0
	BI_RGB         = 0
)

type gdiCapturer struct {
	screenDC  uintptr
	memDC     uintptr
	memBitmap uintptr
	width     int
	height    int
}

func NewGDICapturer() (Capturer, error) {
	runtime.LockOSThread()

	width, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	height, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)
	if width == 0 || height == 0 {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("nao foi possivel obter resolucao da tela")
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("GetDC falhou")
	}

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		procReleaseDC.Call(0, screenDC)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("CreateCompatibleDC falhou")
	}

	memBitmap, _, _ := procCreateCompatibleBitmap.Call(screenDC, width, height)
	if memBitmap == 0 {
		procDeleteDC.Call(memDC)
		procReleaseDC.Call(0, screenDC)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("CreateCompatibleBitmap falhou")
	}

	procSelectObject.Call(memDC, memBitmap)

	return &gdiCapturer{
		screenDC:  screenDC,
		memDC:     memDC,
		memBitmap: memBitmap,
		width:     int(width),
		height:    int(height),
	}, nil
}

func (c *gdiCapturer) AcquireNextFrame() (*Frame, error) {
	r, _, _ := procBitBlt.Call(c.memDC, 0, 0, uintptr(c.width), uintptr(c.height), c.screenDC, 0, 0, SRCCOPY)
	if r == 0 {
		return nil, fmt.Errorf("BitBlt falhou")
	}

	bufSize := c.width * c.height * 4
	frameData := make([]byte, bufSize)

	// BITMAPINFO header
	var bi [40]byte
	bi[0] = 40 // biSize
	*(*int32)(unsafe.Pointer(&bi[4]))  = int32(c.width)
	*(*int32)(unsafe.Pointer(&bi[8]))  = -int32(c.height) // negativo = top-down
	*(*uint16)(unsafe.Pointer(&bi[12])) = 1               // biPlanes
	*(*uint16)(unsafe.Pointer(&bi[14])) = 32              // biBitCount
	*(*uint32)(unsafe.Pointer(&bi[16])) = BI_RGB

	r, _, _ = procGetDIBits.Call(c.memDC, c.memBitmap, 0, uintptr(c.height),
		uintptr(unsafe.Pointer(&frameData[0])),
		uintptr(unsafe.Pointer(&bi[0])),
		DIB_RGB_COLORS)
	if r == 0 {
		return nil, fmt.Errorf("GetDIBits falhou")
	}

	return &Frame{Data: frameData, Width: c.width, Height: c.height, Stride: c.width * 4}, nil
}

func (c *gdiCapturer) ReleaseFrame() {}

func (c *gdiCapturer) Close() error {
	procDeleteObject.Call(c.memBitmap)
	procDeleteDC.Call(c.memDC)
	procReleaseDC.Call(0, c.screenDC)
	runtime.UnlockOSThread()
	return nil
}

func (c *gdiCapturer) Name() string { return "gdi" }
var _ Capturer = (*gdiCapturer)(nil)
