//go:build windows

package screen

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	dxgi = windows.NewLazySystemDLL("dxgi.dll")
	d3d11 = windows.NewLazySystemDLL("d3d11.dll")

	procCreateDXGIFactory1 = dxgi.NewProc("CreateDXGIFactory1")
	procD3D11CreateDevice  = d3d11.NewProc("D3D11CreateDevice")
)

const (
	DXGI_ERROR_NOT_FOUND = 0x887A0002
)

// dxgiCapturer usa DXGI Desktop Duplication API.
// NOTA: implementacao completa requer bindings COM extensivos (IDXGIOutputDuplication).
// Este arquivo fornece a estrutura base; a implementacao COM detalhada sera completada
// na Fase 5 (Dirty Rects + Otimizacoes) pois requer geracao de bindings via IDL.
type dxgiCapturer struct {
	factory   uintptr
	device    uintptr
	width     int
	height    int
	lastFrame *Frame
}

func NewDXGICapturer(monitorIndex int) (Capturer, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Detecta se DXGI esta disponivel
	var factory uintptr
	hr, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&IID_IDXGIFactory1)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if hr != 0 {
		return nil, fmt.Errorf("CreateDXGIFactory1 falhou: HRESULT 0x%X — fallback GDI", hr)
	}

	// Detecta resolucao do monitor primario
	width, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	height, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)

	return &dxgiCapturer{
		factory: factory,
		width:   int(width),
		height:  int(height),
	}, nil
}

func (c *dxgiCapturer) AcquireNextFrame() (*Frame, error) {
	// Placeholder: a implementacao completa de IDXGIOutputDuplication requer
	// bindings COM com virtual table dispatch. Sera completada na Fase 5.
	return nil, fmt.Errorf("DXGI Desktop Duplication nao implementado — use fallback GDI")
}

func (c *dxgiCapturer) ReleaseFrame() {
	c.lastFrame = nil
}

func (c *dxgiCapturer) Close() error {
	return nil
}

func (c *dxgiCapturer) Name() string { return "dxgi" }

// IIDs
var IID_IDXGIFactory1 = windows.GUID{
	Data1: 0x770aae78,
	Data2: 0xf26f,
	Data3: 0x4dba,
	Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87},
}

var _ Capturer = (*dxgiCapturer)(nil)
var _ = syscall.StringToUTF16
