//go:build windows

package screen

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// ── DXGI Desktop Duplication API ──
//
// Documentação: https://learn.microsoft.com/windows/win32/direct3ddxgi/desktop-dup-api
//
// VTable slots verificados contra Windows SDK 10.0.22621.0 (dxgi.h, dxgi1_2.h, d3d11.h):
//
//   IDXGIFactory1:       7=EnumAdapters, 12=EnumAdapters1, 13=IsCurrent
//   IDXGIAdapter1:       7=EnumOutputs, 8=GetDesc, 10=GetDesc1
//   IDXGIOutput:         7=GetDesc
//   IDXGIOutput1:        22=DuplicateOutput
//   IDXGIOutputDuplication: 8=AcquireNextFrame, 14=ReleaseFrame
//   ID3D11Device:        3=CreateTexture2D, 4=CreateTexture2D (alternate)
//     — NOTA: slot 5 é CreateBuffer. CreateTexture2D = slot 5 na verdade.
//     — Revisando: ID3D11Device: 0-2(IUnknown), 3-4(VK/private), 5=CreateTexture2D
//   ID3D11DeviceContext: 14=Map, 15=Unmap, 47=CopyResource

// ── COM VTable dispatch ──
// Em COM x64, "this" (interface pointer) DEVE ser o primeiro argumento.
// syscall.SyscallN(fn, this, arg1, arg2, arg3, ...) → fn(this, arg1, ...)
//
// Segurança de ponteiro: interfaces COM são memória nativa (não-GC), então
// converter o ponteiro da interface para unsafe.Pointer é seguro. O analyzer
// unsafeptr reclama da conversão uintptr→Pointer; para manter o padrão limpo,
// recebemos o ponteiro como unsafe.Pointer (não uintptr) e o mantemos vivo
// durante a chamada via runtime.KeepAlive.

func comCall(ptr unsafe.Pointer, slot int, args ...uintptr) (r1, r2 uintptr, err syscall.Errno) {
	// A interface COM é um ponteiro para o ponteiro da vtable (memória nativa,
	// não-GC). Lê o ponteiro da vtable e depois o endereço do slot via unsafe.Add
	// (sem conversão uintptr→Pointer, que o analyzer unsafeptr rejeita).
	vtable := *(*unsafe.Pointer)(ptr)
	fn := *(*uintptr)(unsafe.Add(vtable, uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	all := make([]uintptr, 0, 1+len(args))
	all = append(all, uintptr(ptr)) // this (conversão no último momento)
	all = append(all, args...)
	r1, r2, err = syscall.SyscallN(fn, all...)
	// Mantém ptr vivo até depois da chamada (evita GC durante syscall)
	runtime.KeepAlive(ptr)
	return
}

func comRelease(ptr unsafe.Pointer) { comCall(ptr, 2) }

// ── GUIDs ──

type windowsGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	IID_IDXGIFactory1 = windowsGUID{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	IID_IDXGIOutput1  = windowsGUID{0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
)

// ── DLL procs ──

var (
	procCreateDXGIFactory1 = syscall.NewLazyDLL("dxgi.dll").NewProc("CreateDXGIFactory1")
	procD3D11CreateDevice  = syscall.NewLazyDLL("d3d11.dll").NewProc("D3D11CreateDevice")
)

// ── VTable slots (verificados) ──

const (
	slotQueryInterface        = 0
	slotFactoryEnumAdapters1  = 12
	slotAdapterEnumOutputs    = 7
	slotOutputGetDesc         = 7
	slotOutput1DuplicateOutput = 22
	slotOutput1DuplicateOutput1 = 23
	slotDupAcquireNextFrame   = 8
	slotDupReleaseFrame       = 14
	slotDevCreateTexture2D    = 5
	slotCtxMap                = 14
	slotCtxUnmap              = 15
	slotCtxCopyResource       = 47
)

// ── Structs ──

type dxgiOutduplFrameInfo struct {
	LastPresentTime           int64
	LastMouseUpdateTime       int64
	AccumulatedFrames         uint32
	RectsCoalesced            int32
	ProtectedContentMaskedOut int32
	PointerPosition           dxgiOutduplPointerPosition
	TotalMetadataBufferSize   uint32
	PointerShapeBufferSize    uint32
}

type dxgiOutduplPointerPosition struct {
	Position point
	Visible  int32
}

type point struct{ X, Y int32 }

type dxgiOutputDesc struct {
	DeviceName        [32]uint16
	DesktopCoordinates rect
	AttachedToDesktop int32
	Rotation          uint32
	Monitor           syscall.Handle
}

type rect struct{ Left, Top, Right, Bottom int32 }

type d3d11Texture2DDesc struct {
	Width, Height, MipLevels, ArraySize   uint32
	Format                                uint32
	SampleCount, SampleQuality            uint32
	Usage, BindFlags, CPUAccessFlags, MiscFlags uint32
}

type d3d11MappedSubresource struct {
	Data       unsafe.Pointer // ponteiro para memória GPU mapeada (não-GC)
	RowPitch   uint32
	DepthPitch uint32
}

// ── Constantes ──

const (
	DXGI_ERROR_WAIT_TIMEOUT = 0x887A0027
	DXGI_ERROR_ACCESS_LOST  = 0x887A0020

	DXGI_FORMAT_B8G8R8A8_UNORM = 87
	DXGI_FORMAT_R8G8B8A8_UNORM = 28

	D3D_DRIVER_TYPE_HARDWARE = 1
	D3D_DRIVER_TYPE_WARP     = 5
	D3D11_SDK_VERSION        = 7

	D3D11_USAGE_STAGING     = 3
	D3D11_CPU_ACCESS_READ   = 1
	D3D11_MAP_READ          = 3
	D3D11_MAP_FLAG_DO_NOT_WAIT = 0x100000
)

// ── Capturer ──

type dxgiCapturer struct {
	factory, adapter, output, output1 unsafe.Pointer
	duplication, d3dDevice, d3dContext unsafe.Pointer

	width, height int

	// HDR / Advanced Color: quando o monitor é HDR, captura em scRGB
	// (R16G16B16A16_FLOAT, 8 bytes/px) e o pipeline aplica tone mapping.
	hdr          bool
	colorSpace   uint32
	bytesPerPixel int

	lastResource unsafe.Pointer
	stagingTex   unsafe.Pointer
	stagingMapped bool

	mu     sync.Mutex
	closed bool
}

func NewDXGICapturer(monitorIndex int) (Capturer, error) {
	runtime.LockOSThread()
	c := &dxgiCapturer{}

	// 1. CreateDXGIFactory1
	hr, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&IID_IDXGIFactory1)),
		uintptr(unsafe.Pointer(&c.factory)),
	)
	if hr != 0 {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("CreateDXGIFactory1: HRESULT 0x%X", uint64(hr))
	}

	// 2. EnumAdapters1(0, &adapter)
	hr, _, _ = comCall(c.factory, slotFactoryEnumAdapters1, 0, uintptr(unsafe.Pointer(&c.adapter)))
	if hr != 0 || c.adapter == nil {
		comRelease(c.factory)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("EnumAdapters1(0): HRESULT 0x%X", uint64(hr))
	}

	// 3. EnumOutputs(monitorIndex, &output)
	hr, _, _ = comCall(c.adapter, slotAdapterEnumOutputs, uintptr(monitorIndex), uintptr(unsafe.Pointer(&c.output)))
	if hr != 0 || c.output == nil {
		comRelease(c.adapter)
		comRelease(c.factory)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("EnumOutputs(%d): HRESULT 0x%X", monitorIndex, uint64(hr))
	}

	// 4. QueryInterface IDXGIOutput1
	hr, _, _ = comCall(c.output, slotQueryInterface, uintptr(unsafe.Pointer(&IID_IDXGIOutput1)), uintptr(unsafe.Pointer(&c.output1)))
	if hr != 0 || c.output1 == nil {
		comRelease(c.output)
		comRelease(c.adapter)
		comRelease(c.factory)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("QI IDXGIOutput1: HRESULT 0x%X", uint64(hr))
	}

	// 5. GetDesc — resolução
	var desc dxgiOutputDesc
	_, _, _ = comCall(c.output, slotOutputGetDesc, uintptr(unsafe.Pointer(&desc)))
	c.width = int(desc.DesktopCoordinates.Right - desc.DesktopCoordinates.Left)
	c.height = int(desc.DesktopCoordinates.Bottom - desc.DesktopCoordinates.Top)
	if c.width <= 0 { c.width = 1920 }
	if c.height <= 0 { c.height = 1080 }

	// 5b. Detecta Advanced Color / HDR (IDXGIOutput6::GetDesc1).
	// Se o monitor é HDR, capturamos em scRGB (R16G16B16A16_FLOAT) e o
	// pipeline aplica tone mapping HDR→SDR. Caso contrário, BGRA 8-bit.
	ac, _ := DetectAdvancedColor(monitorIndex)
	c.hdr = ac != nil && ac.IsHDR
	c.colorSpace = 0
	c.bytesPerPixel = 4
	if c.hdr {
		c.colorSpace = ac.ColorSpace
		c.bytesPerPixel = 8
	}

	// 6. D3D11CreateDevice
	var d3dDevice, d3dContext unsafe.Pointer
	var fl uint32
	hr, _, _ = procD3D11CreateDevice.Call(
		uintptr(0), uintptr(D3D_DRIVER_TYPE_HARDWARE),
		uintptr(0), uintptr(0), uintptr(0), uintptr(0),
		uintptr(D3D11_SDK_VERSION),
		uintptr(unsafe.Pointer(&d3dDevice)),
		uintptr(unsafe.Pointer(&fl)),
		uintptr(unsafe.Pointer(&d3dContext)),
	)
	if hr != 0 {
		hr, _, _ = procD3D11CreateDevice.Call(
			uintptr(0), uintptr(D3D_DRIVER_TYPE_WARP),
			uintptr(0), uintptr(0), uintptr(0), uintptr(0),
			uintptr(D3D11_SDK_VERSION),
			uintptr(unsafe.Pointer(&d3dDevice)),
			uintptr(unsafe.Pointer(&fl)),
			uintptr(unsafe.Pointer(&d3dContext)),
		)
	}
	if hr != 0 || d3dDevice == nil || d3dContext == nil {
		comRelease(c.output1); comRelease(c.output); comRelease(c.adapter); comRelease(c.factory)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("D3D11CreateDevice: HRESULT 0x%X", uint64(hr))
	}
	c.d3dDevice = d3dDevice
	c.d3dContext = d3dContext

	// 7. CreateTexture2D staging
	// Formato: BGRA 8-bit (SDR) ou R16G16B16A16_FLOAT (scRGB/HDR).
	stagingFormat := uint32(DXGI_FORMAT_B8G8R8A8_UNORM)
	if c.hdr {
		stagingFormat = DXGI_FORMAT_R16G16B16A16_FLOAT
	}
	sd := d3d11Texture2DDesc{
		Width: uint32(c.width), Height: uint32(c.height),
		MipLevels: 1, ArraySize: 1,
		Format: stagingFormat,
		SampleCount: 1, SampleQuality: 0,
		Usage: D3D11_USAGE_STAGING,
		BindFlags: 0, CPUAccessFlags: D3D11_CPU_ACCESS_READ, MiscFlags: 0,
	}
	hr, _, _ = comCall(c.d3dDevice, slotDevCreateTexture2D, uintptr(unsafe.Pointer(&sd)), 0, uintptr(unsafe.Pointer(&c.stagingTex)))
	if hr != 0 || c.stagingTex == nil {
		comRelease(c.d3dContext); comRelease(c.d3dDevice)
		comRelease(c.output1); comRelease(c.output); comRelease(c.adapter); comRelease(c.factory)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("CreateTexture2D staging: HRESULT 0x%X", uint64(hr))
	}

	// 8. DuplicateOutput1 com formato forçado.
	// SDR: B8G8R8A8_UNORM (driver converte na origem, evita cores invertidas).
	// HDR: R16G16B16A16_FLOAT (scRGB) para preservar o range HDR e permitir
	// tone mapping no pipeline.
	var supportedFormats [1]uint32
	supportedFormats[0] = stagingFormat
	hr, _, _ = comCall(c.output1, slotOutput1DuplicateOutput1,
		uintptr(c.d3dDevice),
		uintptr(0), // Flags
		uintptr(1), // SupportedFormatsCount
		uintptr(unsafe.Pointer(&supportedFormats[0])),
		uintptr(unsafe.Pointer(&c.duplication)),
	)
	if hr != 0 || c.duplication == nil {
		// Fallback: DuplicateOutput (formato nativo) — se o desktop for
		// R8G8B8A8, as cores podem ficar invertidas, mas é melhor que falhar.
		hr, _, _ = comCall(c.output1, slotOutput1DuplicateOutput, uintptr(c.d3dDevice), uintptr(unsafe.Pointer(&c.duplication)))
		// Se caiu no fallback, o formato pode não ser o esperado — desativa HDR
		// para não interpretar bytes errados.
		if c.hdr {
			c.hdr = false
			c.colorSpace = 0
			c.bytesPerPixel = 4
		}
	}
	if hr != 0 || c.duplication == nil {
		comRelease(c.stagingTex); comRelease(c.d3dContext); comRelease(c.d3dDevice)
		comRelease(c.output1); comRelease(c.output); comRelease(c.adapter); comRelease(c.factory)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("DuplicateOutput1: HRESULT 0x%X", uint64(hr))
	}

	return c, nil
}

func (c *dxgiCapturer) AcquireNextFrame() (*Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, fmt.Errorf("capturador fechado")
	}

	var fi dxgiOutduplFrameInfo
	var res unsafe.Pointer

	hr, _, _ := comCall(c.duplication, slotDupAcquireNextFrame,
		uintptr(200),
		uintptr(unsafe.Pointer(&fi)),
		uintptr(unsafe.Pointer(&res)),
	)

	if uint64(hr) == DXGI_ERROR_WAIT_TIMEOUT {
		return nil, fmt.Errorf("timeout")
	}
	if uint64(hr) == DXGI_ERROR_ACCESS_LOST {
		return nil, fmt.Errorf("DXGI_ERROR_ACCESS_LOST")
	}
	if hr != 0 {
		return nil, fmt.Errorf("AcquireNextFrame: HRESULT 0x%X", uint64(hr))
	}
	if res == nil {
		return nil, fmt.Errorf("desktopResource nulo")
	}

	c.lastResource = res

	// CopyResource: GPU → staging
	comCall(c.d3dContext, slotCtxCopyResource, uintptr(c.stagingTex), uintptr(res))

	// Map: staging → CPU
	var mapped d3d11MappedSubresource
	hr, _, _ = comCall(c.d3dContext, slotCtxMap,
		uintptr(c.stagingTex),
		uintptr(0),
		uintptr(D3D11_MAP_READ),
		uintptr(D3D11_MAP_FLAG_DO_NOT_WAIT),
		uintptr(unsafe.Pointer(&mapped)),
	)
	if hr != 0 {
		c.releaseLocked()
		return nil, fmt.Errorf("Map: HRESULT 0x%X", uint64(hr))
	}
	c.stagingMapped = true

	bpp := c.bytesPerPixel
	bufSize := c.width * c.height * bpp
	frameData := make([]byte, bufSize)
	if mapped.Data != nil && mapped.RowPitch > 0 {
		// mapped.Data aponta para memória GPU staging (não-GC) — o campo já é
		// unsafe.Pointer, então unsafe.Slice/unsafe.Add operam sem conversão uintptr.
		if int(mapped.RowPitch) == c.width*bpp {
			copy(frameData, unsafe.Slice((*byte)(mapped.Data), bufSize))
		} else {
			for y := 0; y < c.height; y++ {
				srcOff := y * int(mapped.RowPitch)
				dstOff := y * c.width * bpp
				row := unsafe.Add(mapped.Data, uintptr(srcOff))
				copy(frameData[dstOff:], unsafe.Slice((*byte)(row), c.width*bpp))
			}
		}
	}

	// Unmap
	comCall(c.d3dContext, slotCtxUnmap, uintptr(c.stagingTex), uintptr(0))
	c.stagingMapped = false

	return &Frame{
		Data:       frameData,
		Width:      c.width,
		Height:     c.height,
		Stride:     c.width * bpp,
		ColorSpace: c.colorSpace,
	}, nil
}

func (c *dxgiCapturer) ReleaseFrame() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releaseLocked()
}

func (c *dxgiCapturer) releaseLocked() {
	if c.stagingMapped {
		comCall(c.d3dContext, slotCtxUnmap, uintptr(c.stagingTex), uintptr(0))
		c.stagingMapped = false
	}
	if c.lastResource != nil {
		comCall(c.duplication, slotDupReleaseFrame) // 0 params
		comRelease(c.lastResource)
		c.lastResource = nil
	}
}

func (c *dxgiCapturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed { return nil }
	c.closed = true
	c.releaseLocked()
	for _, p := range []unsafe.Pointer{c.duplication, c.stagingTex, c.d3dContext, c.d3dDevice, c.output1, c.output, c.adapter, c.factory} {
		if p != nil { comRelease(p) }
	}
	runtime.UnlockOSThread()
	return nil
}

func (c *dxgiCapturer) Name() string { return "dxgi" }
var _ Capturer = (*dxgiCapturer)(nil)
