package screen

import (
	"fmt"
	"image"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetDC            = user32.NewProc("GetDC")
	procReleaseDC        = user32.NewProc("ReleaseDC")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")

	gdi32                      = windows.NewLazySystemDLL("gdi32.dll")
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

	SRCCOPY = 0x00CC0020

	DIB_RGB_COLORS = 0
	BI_RGB         = 0
)

type gdiCapturer struct {
	screenDC  uintptr
	memDC     uintptr
	memBitmap uintptr
	width     int
	height    int
	lastFrame *Frame
}

func NewGDICapturer() (Capturer, error) {
	// GDI objects (HDC, HBITMAP) are thread-affine on Windows.
	// We must pin this goroutine to a single OS thread for the entire
	// lifetime of the capturer. The pin is released in Close().
	runtime.LockOSThread()

	width, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	height, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)

	if width == 0 || height == 0 {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("nao foi possivel obter resolucao da tela")
	}

	screenDC, _, _ := procGetDC.Call(0) // 0 = desktop inteiro
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

	memBitmap, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(width), uintptr(height))
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
	ret, _, _ := procBitBlt.Call(c.memDC, 0, 0, uintptr(c.width), uintptr(c.height), c.screenDC, 0, 0, SRCCOPY)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt falhou (GDI)")
	}

	bufSize := c.width * c.height * 4
	buf := make([]byte, bufSize)

	var bi BITMAPINFO
	bi.bmiHeader.biSize = uint32(unsafe.Sizeof(bi.bmiHeader))
	bi.bmiHeader.biWidth = int32(c.width)
	bi.bmiHeader.biHeight = -int32(c.height) // top-down
	bi.bmiHeader.biPlanes = 1
	bi.bmiHeader.biBitCount = 32
	bi.bmiHeader.biCompression = BI_RGB

	ret, _, _ = procGetDIBits.Call(c.memDC, c.memBitmap, 0, uintptr(c.height),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bi)),
		DIB_RGB_COLORS)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits falhou (GDI)")
	}

	c.lastFrame = &Frame{
		Data:   buf,
		Width:  c.width,
		Height: c.height,
		Stride: c.width * 4,
	}
	return c.lastFrame, nil
}

func (c *gdiCapturer) ReleaseFrame() {
	c.lastFrame = nil
}

func (c *gdiCapturer) Close() error {
	procDeleteObject.Call(c.memBitmap)
	procDeleteDC.Call(c.memDC)
	procReleaseDC.Call(0, c.screenDC)
	runtime.UnlockOSThread()
	return nil
}

func (c *gdiCapturer) Name() string { return "gdi" }

// BITMAPINFO para GetDIBits
type BITMAPINFOHEADER struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type BITMAPINFO struct {
	bmiHeader BITMAPINFOHEADER
	bmiColors [1]uint32
}

// Ensure gdiCapturer implements Capturer
var _ Capturer = (*gdiCapturer)(nil)
var _ = syscall.StringToUTF16 // keep syscall import happy
var _ = image.Pt              // keep image import happy
