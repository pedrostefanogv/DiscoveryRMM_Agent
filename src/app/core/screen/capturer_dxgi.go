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
//   IDXGIOutputDuplication: 8=AcquireNextFrame, 9=GetFrameDirtyRects,
//                          10=GetFrameMoveRects, 14=ReleaseFrame
//     (base 3 = IUnknown QI/AddRef/Release; IDXGIObject ocupa 3..7 = SetPrivateData,
//      SetPrivateDataInterface, GetPrivateData, GetParent, GetDesc — por isso
//      AcquireNextFrame cai em 8. Conferido contra
//      go-d3d/dxgi/dxgi_vtbl.go:134-146, onde GetDesc vem imediatamente antes de
//      AcquireNextFrame, e GetFrameDirtyRects/GetFrameMoveRects logo depois.)
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
	slotQueryInterface          = 0
	slotFactoryEnumAdapters1    = 12
	slotAdapterEnumOutputs      = 7
	slotOutputGetDesc           = 7
	slotOutput1DuplicateOutput  = 22
	slotOutput1DuplicateOutput1 = 23
	// Os índices abaixo contam a partir do INÍCIO da interface (incluindo os 3
	// de IUnknown), que é como comCall navega a vtable. Confira a ordem real de
	// IDXGIOutputDuplication: GetDesc, AcquireNextFrame, GetFrameDirtyRects,
	// GetFrameMoveRects, GetFramePointerShape, MapDesktopSurface,
	// UnMapDesktopSurface, ReleaseFrame.
	slotDupAcquireNextFrame   = 8
	slotDupGetFrameDirtyRects = 9
	slotDupGetFrameMoveRects  = 10
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
	DeviceName         [32]uint16
	DesktopCoordinates rect
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            syscall.Handle
}

type rect struct{ Left, Top, Right, Bottom int32 }

// dxgiRect é o RECT do Win32 (mesmo layout de rect). Existe como alias nomeado
// porque GetFrameDirtyRects escreve um ARRAY deste tipo — usar rect direto
// deixaria a intenção menos explícita no ponto de conversão para DirtyRect.
type dxgiRect = rect

type d3d11Texture2DDesc struct {
	Width, Height, MipLevels, ArraySize         uint32
	Format                                      uint32
	SampleCount, SampleQuality                  uint32
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
	// DXGI_ERROR_MORE_DATA: GetFrameDirtyRects/GetFrameMoveRects devolvem este
	// HRESULT quando o buffer passado é menor que o necessário, e escrevem em
	// *pRequiredBufferSize o tamanho REAL. É o protocolo de crescimento da API.
	DXGI_ERROR_MORE_DATA = 0x887A000B

	DXGI_FORMAT_B8G8R8A8_UNORM = 87
	DXGI_FORMAT_R8G8B8A8_UNORM = 28

	D3D_DRIVER_TYPE_HARDWARE = 1
	D3D_DRIVER_TYPE_WARP     = 5
	D3D11_SDK_VERSION        = 7

	D3D11_USAGE_STAGING        = 3
	D3D11_CPU_ACCESS_READ      = 1
	D3D11_MAP_READ             = 3
	D3D11_MAP_FLAG_DO_NOT_WAIT = 0x100000
)

// ── Capturer ──

type dxgiCapturer struct {
	factory, adapter, output, output1  unsafe.Pointer
	duplication, d3dDevice, d3dContext unsafe.Pointer

	width, height    int
	originX, originY int // origem do output no desktop virtual (DesktopCoordinates)
	monitorIndex     int

	// calib resolve a ordem dos canais (RGBA vs BGRA) — ver channel_order.go.
	// A seed é determinística: esta captura CRIA a staging em B8G8R8A8 e força
	// esse formato no DuplicateOutput1, então quando ele aceita (forcedFormat)
	// os bytes são BGRA por construção. No fallback o formato é o nativo do
	// desktop (indefinido) e a calibração por GDI decide.
	calib channelCalibrator

	// forcedFormat: true quando IDXGIOutput5::DuplicateOutput1 aceitou o formato
	// forçado (stagingFormat). Antes essa informação era descartada e o
	// capturer caía no palpite; hoje ela vira seed da ordem de canais.
	forcedFormat bool

	// HDR / Advanced Color: quando o monitor é HDR, captura em scRGB
	// (R16G16B16A16_FLOAT, 8 bytes/px) e o pipeline aplica tone mapping.
	hdr           bool
	colorSpace    uint32
	bytesPerPixel int

	lastResource  unsafe.Pointer
	stagingTex    unsafe.Pointer
	stagingMapped bool

	// ── Dirty rects de HARDWARE (Fase 6, 2026-11-20) ──
	//
	// A duplicação já sabe ONDE a tela mudou (GetFrameDirtyRects). Esses rects
	// eram descartados: o pipeline só aproveitava o "mudou/não mudou" implícito
	// no timeout do AcquireNextFrame, e como decideFrameKind exige len(rects)>0
	// para entrar em tile-mode, TODO frame saía como JPEG da tela inteira —
	// medido em produção a 3440x1440 (~5 MP recomprimidos por quadro).
	//
	// dirtyBuf é o buffer de RECT nativo reutilizado entre frames (a API cresce
	// a lista via DXGI_ERROR_MORE_DATA, então guardamos a capacidade).
	// rectsFrame é a tradução para DirtyRect em coordenadas do frame, válida até
	// o próximo AcquireNextFrame (mesmo contrato do Frame.Data).
	dirtyBuf   []dxgiRect
	rectsFrame []DirtyRect

	// lastHadMetadata registra se o último frame trouxe metadados de dirty rect.
	// TotalMetadataBufferSize==0 significa "sem informação de região" (o DXGI
	// não rastreou) — nesse caso devolvemos nil e o pipeline manda frame
	// completo, que é o comportamento seguro.
	lastHadMetadata bool

	// frameBuf é reutilizado entre frames (otimização de churn de GC: antes
	// era um make([]byte, w*h*bpp) por frame — ~8 MB SDR / ~16 MB HDR @1080p
	// a 30fps). c.mu protege o acesso; o consumidor copia antes do próximo
	// acquire (mesmo contrato dos demais capturers).
	frameBuf []byte

	mu     sync.Mutex
	closed bool
}

func NewDXGICapturer(monitorIndex int) (Capturer, error) {
	runtime.LockOSThread()
	c := &dxgiCapturer{monitorIndex: monitorIndex}

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
	// Origem do output no desktop virtual FÍSICO: DesktopCoordinates.Left/Top
	// (pode ser negativo com monitores à esquerda/acima do primário). Consumido
	// pelo InputController para mapear o mouse (frame → desktop → 0..65535).
	c.originX = int(desc.DesktopCoordinates.Left)
	c.originY = int(desc.DesktopCoordinates.Top)
	if c.width <= 0 {
		c.width = 1920
	}
	if c.height <= 0 {
		c.height = 1080
	}

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
		comRelease(c.output1)
		comRelease(c.output)
		comRelease(c.adapter)
		comRelease(c.factory)
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
		Format:      stagingFormat,
		SampleCount: 1, SampleQuality: 0,
		Usage:     D3D11_USAGE_STAGING,
		BindFlags: 0, CPUAccessFlags: D3D11_CPU_ACCESS_READ, MiscFlags: 0,
	}
	hr, _, _ = comCall(c.d3dDevice, slotDevCreateTexture2D, uintptr(unsafe.Pointer(&sd)), 0, uintptr(unsafe.Pointer(&c.stagingTex)))
	if hr != 0 || c.stagingTex == nil {
		comRelease(c.d3dContext)
		comRelease(c.d3dDevice)
		comRelease(c.output1)
		comRelease(c.output)
		comRelease(c.adapter)
		comRelease(c.factory)
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
	if hr == 0 && c.duplication != nil {
		// DuplicateOutput1 aceitou o formato forçado: a duplicação entrega
		// exatamente stagingFormat (BGRA em SDR).
		c.forcedFormat = true
	} else {
		// Fallback: DuplicateOutput (formato nativo) — se o desktop for
		// R8G8B8A8, as cores podem ficar invertidas, mas é melhor que falhar.
		hr, _, _ = comCall(c.output1, slotOutput1DuplicateOutput, uintptr(c.d3dDevice), uintptr(unsafe.Pointer(&c.duplication)))
		c.forcedFormat = false
		// Se caiu no fallback, o formato pode não ser o esperado — desativa HDR
		// para não interpretar bytes errados.
		if c.hdr {
			c.hdr = false
			c.colorSpace = 0
			c.bytesPerPixel = 4
		}
	}
	if hr != 0 || c.duplication == nil {
		comRelease(c.stagingTex)
		comRelease(c.d3dContext)
		comRelease(c.d3dDevice)
		comRelease(c.output1)
		comRelease(c.output)
		comRelease(c.adapter)
		comRelease(c.factory)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("DuplicateOutput1: HRESULT 0x%X", uint64(hr))
	}

	// 9. Ordem de canais determinística + cache do processo.
	// A staging foi criada em stagingFormat e o DuplicateOutput1 o aceitou
	// (forcedFormat) ⇒ os bytes são exatamente stagingFormat ⇒ BGRA (SDR).
	// No fallback o formato é o NATIVO do desktop (indefinido) ⇒ seed
	// desconhecida e a calibração por GDI decide. Antes desta mudança o padrão
	// era cego (BGRA/RGBA) e a cor errada ia para a tela até a calibração
	// concluir (incidente de 2026-10-29).
	seed := ChannelOrderUnknown
	if c.forcedFormat && !c.hdr {
		seed = ChannelOrderBGRA
	}
	c.calib = newChannelCalibratorWithKey(
		channelOrderCacheKey("dxgi-manual", monitorIndex, c.width, c.height), seed)

	return c, nil
}

// ChannelOrderPending informa se a ordem dos canais (R↔B) ainda não foi
// decidida — o loop de captura segura a publicação até o gate.
func (c *dxgiCapturer) ChannelOrderPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.calib.Resolved()
}

// ChannelOrderName devolve a ordem efetiva atual, para log de diagnóstico.
// c.mu: a ordem é escrita por apply() dentro de AcquireNextFrame.
func (c *dxgiCapturer) ChannelOrderName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calib.Order().String() + " (" + c.calib.SeedFrom() + ")"
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

	// ── Dirty rects de HARDWARE (Fase 6, 2026-11-20) ──
	//
	// A duplicação já respondeu ONDE a tela mudou — de graça, junto do frame
	// que ela acabou de entregar. Antes esta informação era descartada e o
	// pipeline ficava com rects=nil, o que desliga o tile-mode (decideFrameKind
	// exige len(rects)>0) e faz TODO frame sair como JPEG da tela inteira.
	// Medido em produção a 3440x1440: ~5 MP recomprimidos por quadro mesmo
	// quando só o cursor se moveu.
	//
	// TotalMetadataBufferSize==0 significa que o DXGI não rastreou regiões
	// neste frame (acontece após mode change, por exemplo). Nesse caso deixamos
	// rectsFrame nil — o chamador trata como "frame completo", que é seguro.
	c.captureDirtyRectsLocked(&fi)

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
	// Buffer reutilizável: elimina ~8-16 MB de alocação por frame (otimização
	// de churn de GC). O consumidor copia antes do próximo acquire.
	if cap(c.frameBuf) < bufSize {
		c.frameBuf = make([]byte, bufSize)
	}
	frameData := c.frameBuf[:bufSize]
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

	frame := &Frame{
		Data:       frameData,
		Width:      c.width,
		Height:     c.height,
		Stride:     c.width * bpp,
		OriginX:    c.originX,
		OriginY:    c.originY,
		ColorSpace: c.colorSpace,
	}
	// Normaliza a ordem de canais para BGRA (contrato dos encoders). Quando o
	// DuplicateOutput1 falha e caímos no DuplicateOutput nativo, o formato pode
	// ser R8G8B8A8 — a calibração detecta e corrige em vez de inverter R↔B.
	c.calib.apply(frame)
	return frame, nil
}

// Geometry retorna a origem e dimensões do output no desktop virtual físico.
func (c *dxgiCapturer) Geometry() (x, y, w, h int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.originX, c.originY, c.width, c.height
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
	if c.closed {
		return nil
	}
	c.closed = true
	c.releaseLocked()
	for _, p := range []unsafe.Pointer{c.duplication, c.stagingTex, c.d3dContext, c.d3dDevice, c.output1, c.output, c.adapter, c.factory} {
		if p != nil {
			comRelease(p)
		}
	}
	runtime.UnlockOSThread()
	return nil
}

// HasHardwareDirtyRects informa que este capturer decide por HARDWARE se a tela
// mudou: AcquireNextFrame usa IDXGIOutputDuplication com timeout de 200 ms e
// devolve DXGI_ERROR_WAIT_TIMEOUT quando nada mudou (tratado como "sem frame").
// O diff de software do pipeline é redundante aqui — ver
// HardwareDirtyRectsReporter em capturer.go.
func (c *dxgiCapturer) HasHardwareDirtyRects() bool { return true }

// HardwareDirtyRects devolve as regiões alteradas reportadas pela GPU no último
// frame, em coordenadas do FRAME e já recortadas/consolidadas para o protocolo
// de tiles do viewer. Ver DirtyRectsReporter em capturer.go.
//
// Devolve nil quando o DXGI não trouxe metadados de região neste frame — o
// chamador interpreta nil como "frame completo" (nunca como idle: idle chega
// como erro de timeout no AcquireNextFrame).
//
// Contrato de validade: a slice é válida até o próximo AcquireNextFrame.
func (c *dxgiCapturer) HardwareDirtyRects() []DirtyRect {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.lastHadMetadata {
		return nil
	}
	return c.rectsFrame
}

// captureDirtyRectsLocked consulta GetFrameDirtyRects e traduz o resultado para
// coordenadas do frame. Chamado com c.mu JÁ travado, entre AcquireNextFrame e
// ReleaseFrame (a API só é válida nessa janela e com um frame adquirido).
//
// IMPORTANTE — não é fatal: qualquer falha aqui é registrada e o capturer segue
// com rectsFrame=nil (frame completo). Perder a otimização é aceitável; derrubar
// a sessão de tela porque o DXGI não quis reportar regiões, não.
func (c *dxgiCapturer) captureDirtyRectsLocked(fi *dxgiOutduplFrameInfo) {
	c.lastHadMetadata = false
	c.rectsFrame = nil

	if fi.TotalMetadataBufferSize == 0 {
		// Sem metadados: o DXGI não rastreou regiões neste frame.
		return
	}

	// A API cresce o buffer via DXGI_ERROR_MORE_DATA: começa com a capacidade
	// que já temos (reutilizada entre frames) e só realoca se ela não bastar.
	// Isso evita alocar um []RECT por frame a 30 fps.
	if len(c.dirtyBuf) == 0 {
		c.dirtyBuf = make([]dxgiRect, 8)
	}
	required := uint32(len(c.dirtyBuf))

	for attempt := 0; attempt < 4; attempt++ {
		hr, _, _ := comCall(c.duplication, slotDupGetFrameDirtyRects,
			uintptr(unsafe.Pointer(&c.dirtyBuf[0])),
			uintptr(unsafe.Pointer(&required)),
		)
		if uint64(hr) == DXGI_ERROR_MORE_DATA {
			// `required` traz o tamanho necessário; realoca e tenta de novo.
			n := int(required)
			if n <= len(c.dirtyBuf) {
				n = len(c.dirtyBuf) * 2
			}
			if n > 4096 { // teto de segurança: 4096 RECTs = 64 KB
				n = 4096
			}
			c.dirtyBuf = make([]dxgiRect, n)
			required = uint32(n)
			continue
		}
		if hr != 0 {
			// Não fatal — segue com frame completo (ver comentário da função).
			return
		}
		// Sucesso: `required` passou a ser o NÚMERO DE RECTS escritos.
		count := int(required)
		if count > len(c.dirtyBuf) {
			count = len(c.dirtyBuf)
		}
		if count == 0 {
			return
		}

		// Traduz RECT para DirtyRect. A origem do output JÁ foi subtraída: os
		// RECTs do DXGI vêm em coordenadas do DESKTOP VIRTUAL, enquanto o frame
		// começa em (originX, originY) — sem subtrair, um monitor não-primário
		// desloca todos os tiles (mesmo bug já corrigido no cursor).
		raw := make([]DirtyRect, 0, count)
		for i := 0; i < count; i++ {
			r := c.dirtyBuf[i]
			w := int(r.Right - r.Left)
			h := int(r.Bottom - r.Top)
			if w <= 0 || h <= 0 {
				continue
			}
			raw = append(raw, DirtyRect{
				X:      int(r.Left) - c.originX,
				Y:      int(r.Top) - c.originY,
				Width:  w,
				Height: h,
			})
		}
		if len(raw) == 0 {
			return
		}

		// Consolida para o que o protocolo do viewer aceita (numRects < 512).
		// Sem isso um arrasto de janela pode gerar centenas de RECTs e o decoder
		// do viewer deixa de reconhecer o payload como tiles.
		c.rectsFrame = CoalesceDirtyRects(raw, c.width, c.height)
		c.lastHadMetadata = len(c.rectsFrame) > 0
		return
	}
}

func (c *dxgiCapturer) Name() string { return "dxgi" }

var _ Capturer = (*dxgiCapturer)(nil)
