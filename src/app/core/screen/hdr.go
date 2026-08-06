//go:build windows

package screen

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// ── HDR / Advanced Color ──
//
// Detecção de Advanced Color (HDR) via IDXGIOutput6::GetDesc1 e tone mapping
// HDR→SDR (scRGB → sRGB 8-bit) para captura de tela remota.
//
// Em telas HDR, o Windows usa Advanced Color e o desktop DXGI pode entregar o
// frame em scRGB (R16G16B16A16_FLOAT) ou HDR10 (R10G10B10A2). Se capturarmos
// direto em 8-bit SDR, o driver faz um clamp simples (sem tone mapping) e o
// conteúdo HDR fica lavado/desbotado. Aqui detectamos o color space e, quando
// HDR, capturamos em scRGB float e aplicamos tone mapping para SDR 8-bit.

// GUID do IDXGIOutput6.
var IID_IDXGIOutput6 = windowsGUID{0x068346e8, 0xaaec, 0x4b84, [8]byte{0xad, 0xd7, 0x13, 0x7f, 0x51, 0x3f, 0x77, 0x01}}

// DXGI_COLOR_SPACE_TYPE (valores oficiais dxgi1_4.h / dxgi1_6.h).
const (
	DXGI_COLOR_SPACE_RGB_FULL_G22_NONE_P709              = 0
	DXGI_COLOR_SPACE_RGB_FULL_G10_NONE_P709              = 1  // scRGB (HDR)
	DXGI_COLOR_SPACE_RGB_STUDIO_G22_NONE_P709            = 2
	DXGI_COLOR_SPACE_RGB_STUDIO_G22_NONE_P2020           = 3
	DXGI_COLOR_SPACE_RGB_FULL_G2084_NONE_P2020           = 6  // HDR10 (PQ)
	DXGI_COLOR_SPACE_YCBCR_STUDIO_G2084_LEFT_P2020       = 7
	DXGI_COLOR_SPACE_RGB_STUDIO_G2084_NONE_P2020         = 8  // HDR10 (PQ studio)
	DXGI_COLOR_SPACE_YCBCR_STUDIO_G22_LEFT_P709          = 9
	DXGI_COLOR_SPACE_YCBCR_STUDIO_G22_LEFT_P2020         = 10
	DXGI_COLOR_SPACE_YCBCR_STUDIO_G22_TOPLEFT_P2020      = 11
	DXGI_COLOR_SPACE_YCBCR_STUDIO_G2084_TOPLEFT_P2020    = 12
	DXGI_COLOR_SPACE_RGB_FULL_G22_NONE_P2020             = 13 // Wide gamut (P2020)
	DXGI_COLOR_SPACE_YCBCR_STUDIO_G2084_LEFT_P2020_FIXED_POINT = 14
	DXGI_COLOR_SPACE_RGB_FULL_G2084_NONE_P2020_FIXED_POINT = 15
	DXGI_COLOR_SPACE_YCBCR_STUDIO_G22_TOPLEFT_P2020_FIXED_POINT = 16
	DXGI_COLOR_SPACE_YCBCR_STUDIO_G2084_TOPLEFT_P2020_FIXED_POINT = 17
	DXGI_COLOR_SPACE_RGB_FULL_G22_NONE_P2020_FIXED_POINT = 18
	DXGI_COLOR_SPACE_YCBCR_STUDIO_G22_NONE_P2020_FIXED_POINT = 19
	// NOTA: o SDK oficial repete os nomes *_FIXED_POINT com valores 20-22
	// (quirk do DXGI_COLOR_SPACE_TYPE). Em Go não podemos ter nomes duplicados,
	// então omitimos os valores 20-22 (não usados na detecção de HDR).
	DXGI_COLOR_SPACE_RGB_FULL_G10_NONE_P709_FIXED_POINT  = 23 // scRGB fixed-point (HDR)
	DXGI_COLOR_SPACE_CUSTOM                              = 0xFFFFFFFF
)

// DXGI_FORMAT_R16G16B16A16_FLOAT (scRGB) — 8 bytes/pixel (4 × half-float).
const DXGI_FORMAT_R16G16B16A16_FLOAT = 10

// VTable slot: IDXGIOutput6::GetDesc1 (após IDXGIOutput5::DuplicateOutput1).
const slotOutput6GetDesc1 = 27

// DXGI_OUTPUT_DESC1 (layout x64 verificado contra dxgi1_6.h).
type dxgiOutputDesc1 struct {
	DeviceName          [32]uint16
	DesktopCoordinates  rect
	AttachedToDesktop   int32
	Rotation            uint32
	Monitor             syscall.Handle
	BitsPerColor        uint32
	ColorSpace          uint32
	RedPrimary          [2]float32
	GreenPrimary        [2]float32
	BluePrimary         [2]float32
	WhitePoint          [2]float32
	MinLuminance        float32
	MaxLuminance        float32
	MaxFullFrameLuminance float32
}

// AdvancedColorInfo descreve o estado de Advanced Color/HDR de um monitor.
type AdvancedColorInfo struct {
	Supported            bool
	IsHDR                bool
	ColorSpace           uint32
	ColorSpaceName       string
	BitsPerColor         uint32
	MaxLuminance         float64
	MinLuminance         float64
	MaxFullFrameLuminance float64
}

func colorSpaceName(cs uint32) string {
	switch cs {
	case DXGI_COLOR_SPACE_RGB_FULL_G22_NONE_P709:
		return "SDR (sRGB)"
	case DXGI_COLOR_SPACE_RGB_FULL_G10_NONE_P709:
		return "scRGB (HDR)"
	case DXGI_COLOR_SPACE_RGB_FULL_G2084_NONE_P2020:
		return "HDR10 (PQ)"
	case DXGI_COLOR_SPACE_RGB_STUDIO_G2084_NONE_P2020:
		return "HDR10 (PQ studio)"
	case DXGI_COLOR_SPACE_RGB_FULL_G22_NONE_P2020:
		return "Wide gamut (P2020)"
	case DXGI_COLOR_SPACE_RGB_FULL_G10_NONE_P709_FIXED_POINT:
		return "scRGB fixed-point (HDR)"
	default:
		return fmt.Sprintf("ColorSpace %d", cs)
	}
}

func isHDRColorSpace(cs uint32) bool {
	switch cs {
	case DXGI_COLOR_SPACE_RGB_FULL_G10_NONE_P709,
		DXGI_COLOR_SPACE_RGB_FULL_G2084_NONE_P2020,
		DXGI_COLOR_SPACE_RGB_STUDIO_G2084_NONE_P2020,
		DXGI_COLOR_SPACE_YCBCR_STUDIO_G2084_LEFT_P2020,
		DXGI_COLOR_SPACE_YCBCR_STUDIO_G2084_TOPLEFT_P2020,
		DXGI_COLOR_SPACE_YCBCR_STUDIO_G2084_LEFT_P2020_FIXED_POINT,
		DXGI_COLOR_SPACE_YCBCR_STUDIO_G2084_TOPLEFT_P2020_FIXED_POINT,
		DXGI_COLOR_SPACE_RGB_FULL_G2084_NONE_P2020_FIXED_POINT,
		DXGI_COLOR_SPACE_RGB_FULL_G10_NONE_P709_FIXED_POINT:
		return true
	}
	return false
}

// DetectAdvancedColor consulta IDXGIOutput6::GetDesc1 para detectar
// Advanced Color / HDR no monitor especificado (0 = primário).
// Retorna um AdvancedColorInfo mesmo quando o monitor não é HDR ou quando
// IDXGIOutput6 não está disponível (Windows antigo) — nesses casos IsHDR=false.
func DetectAdvancedColor(monitorIndex int) (*AdvancedColorInfo, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var factory unsafe.Pointer
	hr, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&IID_IDXGIFactory1)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if hr != 0 || factory == nil {
		return nil, fmt.Errorf("CreateDXGIFactory1: HRESULT 0x%X", uint64(hr))
	}
	defer comRelease(factory)

	var adapter unsafe.Pointer
	hr, _, _ = comCall(factory, slotFactoryEnumAdapters1, 0, uintptr(unsafe.Pointer(&adapter)))
	if hr != 0 || adapter == nil {
		return nil, fmt.Errorf("EnumAdapters1: HRESULT 0x%X", uint64(hr))
	}
	defer comRelease(adapter)

	var output unsafe.Pointer
	hr, _, _ = comCall(adapter, slotAdapterEnumOutputs, uintptr(monitorIndex), uintptr(unsafe.Pointer(&output)))
	if hr != 0 || output == nil {
		return nil, fmt.Errorf("EnumOutputs(%d): HRESULT 0x%X", monitorIndex, uint64(hr))
	}
	defer comRelease(output)

	var output6 unsafe.Pointer
	hr, _, _ = comCall(output, slotQueryInterface, uintptr(unsafe.Pointer(&IID_IDXGIOutput6)), uintptr(unsafe.Pointer(&output6)))
	if hr != 0 || output6 == nil {
		// IDXGIOutput6 indisponível (Windows < 10 ou driver antigo) — sem HDR.
		return &AdvancedColorInfo{Supported: false, IsHDR: false, ColorSpaceName: "nao suportado"}, nil
	}
	defer comRelease(output6)

	var desc dxgiOutputDesc1
	hr, _, _ = comCall(output6, slotOutput6GetDesc1, uintptr(unsafe.Pointer(&desc)))
	if hr != 0 {
		return &AdvancedColorInfo{Supported: false, IsHDR: false, ColorSpaceName: "GetDesc1 falhou"}, nil
	}

	info := &AdvancedColorInfo{
		Supported:            true,
		ColorSpace:           desc.ColorSpace,
		ColorSpaceName:       colorSpaceName(desc.ColorSpace),
		BitsPerColor:         desc.BitsPerColor,
		MaxLuminance:         float64(desc.MaxLuminance),
		MinLuminance:         float64(desc.MinLuminance),
		MaxFullFrameLuminance: float64(desc.MaxFullFrameLuminance),
	}
	info.IsHDR = isHDRColorSpace(desc.ColorSpace)
	return info, nil
}

// ── Tone mapping HDR→SDR ──

// halfToFloat converte um half-float (IEEE 754 binary16) para float32.
func halfToFloat(h uint16) float32 {
	sign := uint32(h>>15) & 1
	exp := uint32(h>>10) & 0x1F
	mant := uint32(h) & 0x3FF
	var bits uint32
	switch {
	case exp == 0:
		if mant == 0 {
			bits = sign << 31
		} else {
			// subnormal
			e := uint32(127 - 15 + 1)
			for mant&0x400 == 0 {
				mant <<= 1
				e--
			}
			mant &= 0x3FF
			bits = sign<<31 | e<<23 | mant<<13
		}
	case exp == 0x1F:
		bits = sign<<31 | 0xFF<<23 | mant<<13 // inf/nan
	default:
		bits = sign<<31 | (exp+127-15)<<23 | mant<<13
	}
	return math.Float32frombits(bits)
}

// toneMapLinear comprime o range HDR (valores > 1.0) preservando o SDR.
// Em scRGB, 1.0 = branco SDR (80 nits). Valores > 1.0 são destaques HDR.
// Mantém o range SDR linear e aplica um "shoulder" suave (Reinhard-like)
// apenas nos destaques, evitando o aspecto lavado do clamp simples.
func toneMapLinear(c float32) float32 {
	if c <= 0 {
		return 0
	}
	if c <= 1.0 {
		return c // range SDR preservado
	}
	const k = 2.0 // largura do shoulder (quanto maior, mais suave)
	d := c - 1.0
	return 1.0 + d/(1.0+d/k)
}

// linearToSRGB aplica a curva de codificação sRGB (gamma 2.2).
func linearToSRGB(c float32) float32 {
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*float32(math.Pow(float64(c), 1.0/2.4)) - 0.055
}

func clampByte(v float32) byte {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 255
	}
	return byte(v*255 + 0.5)
}

// LUT de gamma sRGB (evita math.Pow por pixel — performance em tempo real).
var (
	srgbLUTOnce sync.Once
	srgbLUT     [4096]byte
)

func initSRGBLUT() {
	for i := 0; i < 4096; i++ {
		v := float32(i) / 4095.0
		srgbLUT[i] = clampByte(linearToSRGB(v))
	}
}

func linearToSRGBByte(v float32) byte {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	srgbLUTOnce.Do(initSRGBLUT)
	return srgbLUT[int(v*4095)]
}

// ToneMapHDRToSDR converte um frame scRGB (R16G16B16A16_FLOAT, 8 bytes/pixel)
// para SDR 8-bit BGRA com tone mapping. Retorna um novo Frame; o original
// não é alterado. O frame de entrada deve ter Stride = Width*8.
func ToneMapHDRToSDR(frame *Frame) *Frame {
	w, h := frame.Width, frame.Height
	stride := frame.Stride
	out := &Frame{
		Data:   make([]byte, w*h*4),
		Width:  w,
		Height: h,
		Stride: w * 4,
	}
	src := frame.Data
	dst := out.Data
	for y := 0; y < h; y++ {
		rowSrc := y * stride
		rowDst := y * w * 4
		for x := 0; x < w; x++ {
			off := rowSrc + x*8 // R16G16B16A16 = 8 bytes
			r := halfToFloat(uint16(src[off]) | uint16(src[off+1])<<8)
			g := halfToFloat(uint16(src[off+2]) | uint16(src[off+3])<<8)
			b := halfToFloat(uint16(src[off+4]) | uint16(src[off+5])<<8)

			do := rowDst + x*4
			dst[do] = linearToSRGBByte(toneMapLinear(b))
			dst[do+1] = linearToSRGBByte(toneMapLinear(g))
			dst[do+2] = linearToSRGBByte(toneMapLinear(r))
			dst[do+3] = 0xff
		}
	}
	return out
}
