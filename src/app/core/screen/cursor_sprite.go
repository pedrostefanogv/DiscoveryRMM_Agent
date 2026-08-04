//go:build windows

package screen

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"syscall"
	"unsafe"
)

// CursorInfo representa a posicao e estado do cursor.
type CursorInfo struct {
	X       int16  `json:"x"`
	Y       int16  `json:"y"`
	Visible bool   `json:"visible"`
	Shape   string `json:"shape"` // arrow, hand, ibeam, etc.
}

// CursorImage representa o bitmap real do cursor (PNG) + hotspot.
// Enviado separadamente do frame apenas quando a forma muda.
type CursorImage struct {
	PNG    []byte // PNG codificado (RGBA, fundo transparente)
	Width  int
	Height int
	HotX   int // hotspot X (ponto de clique)
	HotY   int // hotspot Y
}

// CursorSpriteSender envia o cursor separadamente do frame para reduzir banda.
// Atualiza apenas quando o cursor muda de posicao ou forma.
type CursorSpriteSender struct {
	last CursorInfo
	// lastHandle rastreia o handle do cursor para detectar mudanca de forma
	// sem re-encodar o PNG a cada frame.
	lastHandle uintptr
}

func NewCursorSpriteSender() *CursorSpriteSender {
	return &CursorSpriteSender{}
}

// ShouldSend retorna true se o cursor mudou e deve ser enviado.
func (cs *CursorSpriteSender) ShouldSend(current CursorInfo) bool {
	changed := current.X != cs.last.X ||
		current.Y != cs.last.Y ||
		current.Visible != cs.last.Visible ||
		current.Shape != cs.last.Shape

	if changed {
		cs.last = current
	}
	return changed
}

// ShouldSendImage retorna true se a FORMA do cursor mudou (handle diferente),
// indicando que o bitmap do cursor deve ser re-enviado.
func (cs *CursorSpriteSender) ShouldSendImage(handle uintptr) bool {
	if handle == 0 || handle == cs.lastHandle {
		return false
	}
	cs.lastHandle = handle
	return true
}

// Encode serializa a informacao do cursor em 6 bytes.
// Formato: flags(1) | x(2) | y(2) | shapeLen(1)
func (cs *CursorSpriteSender) Encode(info CursorInfo) []byte {
	buf := make([]byte, 6)
	var flags byte
	if info.Visible {
		flags |= 1 << 0
	}
	switch info.Shape {
	case "hand":
		flags |= 1 << 1
	case "ibeam":
		flags |= 1 << 2
	case "crosshair":
		flags |= 1 << 3
	}

	buf[0] = flags
	binary.BigEndian.PutUint16(buf[1:3], uint16(info.X))
	binary.BigEndian.PutUint16(buf[3:5], uint16(info.Y))
	buf[5] = 0 // reserved

	return buf
}

// ── Win32 cursor capture ──

var (
	procGetCursorPos    = user32.NewProc("GetCursorPos")
	procGetCursorInfo   = user32.NewProc("GetCursorInfo")
	procGetIconInfo     = user32.NewProc("GetIconInfo")
	procDestroyIcon     = user32.NewProc("DestroyIcon")
	procGetSystemCursor = user32.NewProc("CopyIcon")

	// procGetObjectW obtém dimensões do bitmap (não declarado em capturer_gdi.go).
	procGetObjectW = gdi32.NewProc("GetObjectW")
)

// CURSORINFO — usado para detectar visibilidade e handle do cursor.
type cursorInfo struct {
	cbSize      uint32
	flags       uint32
	hCursor     uintptr
	ptScreenPos point
}

const CURSOR_SHOWING = 0x00000001

// ICONINFO — para extrair bitmaps do cursor.
type iconInfo struct {
	fIcon    uint32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

// BITMAP — estrutura GDI para obter dimensoes do bitmap.
type bitmapInfo struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

// BITMAPINFOHEADER — para GetDIBits.
type bitmapInfoHeader struct {
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

const (
	biRGB        = 0
	dibRGBColors = 0
)

// GetCursorPos retorna a posicao atual do cursor via GetCursorPos Win32.
func GetCursorPos() (CursorInfo, error) {
	var p point
	ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	if ret == 0 {
		return CursorInfo{}, fmt.Errorf("GetCursorPos falhou")
	}

	// Determina visibilidade via GetCursorInfo (CURSOR_SHOWING).
	visible := true
	var ci cursorInfo
	ci.cbSize = uint32(unsafe.Sizeof(ci))
	if r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r != 0 {
		visible = ci.flags&CURSOR_SHOWING != 0
	}

	return CursorInfo{
		X:       int16(p.X),
		Y:       int16(p.Y),
		Visible: visible,
		Shape:   "arrow", // forma determinada no render (ver GetCursorShape)
	}, nil
}

// GetCursorHandle retorna o handle atual do cursor (para detectar mudanca de forma).
func GetCursorHandle() uintptr {
	var ci cursorInfo
	ci.cbSize = uint32(unsafe.Sizeof(ci))
	if r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r != 0 {
		return ci.hCursor
	}
	return 0
}

// GetCursorShape tenta inferir a forma do cursor a partir do ID do sistema.
// Retorna um dos valores reconhecidos pelo viewer (arrow/hand/ibeam/crosshair).
func GetCursorShape() string {
	// Fallback simples — sem mapeamento do sistema aqui para evitar
	// dependencia de LoadCursor. O capturer DXGI go-d3d já desenha o cursor
	// real como overlay quando DrawPointer=true; este método serve para
	// o modo cursor-separado.
	return "arrow"
}

// CaptureCursorImage captura o bitmap real do cursor do sistema e o codifica
// como PNG (RGBA com fundo transparente). Retorna nil se o cursor nao estiver
// visivel ou nao puder ser capturado.
func CaptureCursorImage() *CursorImage {
	var ci cursorInfo
	ci.cbSize = uint32(unsafe.Sizeof(ci))
	if r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r == 0 {
		return nil
	}
	if ci.flags&CURSOR_SHOWING == 0 || ci.hCursor == 0 {
		return nil
	}

	// Extrai bitmaps do cursor via GetIconInfo.
	var ii iconInfo
	if r, _, _ := procGetIconInfo.Call(ci.hCursor, uintptr(unsafe.Pointer(&ii))); r == 0 {
		return nil
	}
	defer func() {
		if ii.hbmColor != 0 {
			procDeleteObject.Call(ii.hbmColor)
		}
		if ii.hbmMask != 0 {
			procDeleteObject.Call(ii.hbmMask)
		}
	}()

	// Se nao ha bitmap colorido, usa o mask (cursor monocromatico).
	hbm := ii.hbmColor
	if hbm == 0 {
		hbm = ii.hbmMask
	}
	if hbm == 0 {
		return nil
	}

	// Obtem dimensoes do bitmap.
	var bm bitmapInfo
	if r, _, _ := procGetObjectW.Call(hbm, uintptr(unsafe.Sizeof(bm)), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return nil
	}
	w := int(bm.bmWidth)
	h := int(bm.bmHeight)
	if w <= 0 || h <= 0 || w > 256 || h > 256 {
		return nil
	}

	// Cria DC de memoria e seleciona o bitmap.
	hdc, _, _ := procCreateCompatibleDC.Call(0)
	if hdc == 0 {
		return nil
	}
	defer procDeleteDC.Call(hdc)
	procSelectObject.Call(hdc, hbm)

	// Prepara buffer BGRA (32bpp) para GetDIBits.
	// GetDIBits retorna linhas de baixo para cima (bottom-up) quando biHeight > 0.
	bufSize := w * h * 4
	buf := make([]byte, bufSize)

	bih := bitmapInfoHeader{
		biSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:       int32(w),
		biHeight:      -int32(h), // top-down (negativo) para facilitar
		biPlanes:      1,
		biBitCount:    32,
		biCompression: biRGB,
		biSizeImage:   uint32(bufSize),
	}

	ret, _, _ := procGetDIBits.Call(
		hdc,
		hbm,
		0,
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bih)),
		dibRGBColors,
	)
	if ret == 0 {
		return nil
	}

	// Converte BGRA → RGBA e aplica a mascara (transparencia).
	// O cursor colorido (hbmColor) tem a forma; o mask define transparencia.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	hasMask := ii.hbmMask != 0 && ii.hbmColor != 0

	// Extrai a mascara AND (1 bit por pixel) para transparencia.
	var maskBits []byte
	if hasMask {
		maskBits = captureMaskBits(ii.hbmMask, w, h)
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := (y*w + x) * 4
			b := buf[idx]
			g := buf[idx+1]
			r := buf[idx+2]
			a := buf[idx+3]

			// Aplica mascara: onde o bit AND=1, o pixel e transparente.
			if hasMask && maskBits != nil {
				maskIdx := y*w + x
				byteIdx := maskIdx / 8
				bitIdx := uint(maskIdx % 8)
				if byteIdx < len(maskBits) && (maskBits[byteIdx]>>bitIdx)&1 == 1 {
					a = 0
				}
			}

			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: a})
		}
	}

	// Codifica PNG.
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return nil
	}

	return &CursorImage{
		PNG:    pngBuf.Bytes(),
		Width:  w,
		Height: h,
		HotX:   int(ii.xHotspot),
		HotY:   int(ii.yHotspot),
	}
}

// captureMaskBits extrai os bits da mascara AND de um bitmap monocromatico.
func captureMaskBits(hbm uintptr, w, h int) []byte {
	hdc, _, _ := procCreateCompatibleDC.Call(0)
	if hdc == 0 {
		return nil
	}
	defer procDeleteDC.Call(hdc)
	procSelectObject.Call(hdc, hbm)

	// Mascara e 1bpp — GetDIBits com biBitCount=1.
	rowBytes := (w + 7) / 8
	// GetDIBits alinha cada linha a 32 bits (DWORD).
	alignedRow := ((rowBytes + 3) / 4) * 4
	buf := make([]byte, alignedRow*h)

	bih := bitmapInfoHeader{
		biSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:       int32(w),
		biHeight:      -int32(h),
		biPlanes:      1,
		biBitCount:    1,
		biCompression: biRGB,
		biSizeImage:   uint32(len(buf)),
	}

	ret, _, _ := procGetDIBits.Call(
		hdc,
		hbm,
		0,
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bih)),
		dibRGBColors,
	)
	if ret == 0 {
		return nil
	}

	// Compacta para w*h bits (1 bit por pixel, sem padding).
	out := make([]byte, (w*h+7)/8)
	for y := 0; y < h; y++ {
		srcRow := y * alignedRow
		for x := 0; x < w; x++ {
			byteIdx := srcRow + x/8
			bitIdx := uint(x % 8)
			if byteIdx < len(buf) && (buf[byteIdx]>>bitIdx)&1 == 1 {
				dstIdx := y*w + x
				out[dstIdx/8] |= 1 << uint(dstIdx%8)
			}
		}
	}
	return out
}

// Ensure imports
var _ = syscall.NewLazyDLL
var _ = procGetIconInfo
var _ = procGetSystemCursor
var _ = procDestroyIcon
