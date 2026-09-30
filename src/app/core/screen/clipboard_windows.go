//go:build windows

package screen

import (
	"encoding/binary"
	"fmt"
	"image"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// Clipboard de texto (CF_UNICODETEXT) — usado pela sessão remota de tela para
// copiar/colar texto entre o viewer e a máquina remota (somente texto, sem
// arquivos/binários).

var (
	kernel32Clip = syscall.NewLazyDLL("kernel32.dll")

	procGlobalAlloc  = kernel32Clip.NewProc("GlobalAlloc")
	procGlobalFree   = kernel32Clip.NewProc("GlobalFree")
	procGlobalLock   = kernel32Clip.NewProc("GlobalLock")
	procGlobalUnlock = kernel32Clip.NewProc("GlobalUnlock")

	procOpenClipboard          = user32.NewProc("OpenClipboard")
	procCloseClipboard         = user32.NewProc("CloseClipboard")
	procEmptyClipboard         = user32.NewProc("EmptyClipboard")
	procSetClipboardData       = user32.NewProc("SetClipboardData")
	procGetClipboardData       = user32.NewProc("GetClipboardData")
	procIsClipboardFormatAvail = user32.NewProc("IsClipboardFormatAvailable")
)

const (
	cfUnicodeText = 13
	// cfDIB é o formato clássico de bitmap (BITMAPINFOHEADER + pixels).
	// Suportado por Word, Paint, navegadores, Teams etc.
	cfDIB        = 8
	gmemMoveable = 0x0002
)

// SetClipboardImage coloca um bitmap 32bpp no clipboard do Windows (CF_DIB,
// pixels BGRA bottom-up). Usado pelo botão "Copiar" do lightbox de captura.
// Após SetClipboardData, o sistema passa a ser dono da memória global.
//
//go:nocheckptr
func SetClipboardImage(img image.Image) error {
	data, err := dibBytes(img)
	if err != nil {
		return err
	}
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return fmt.Errorf("OpenClipboard falhou")
	}
	defer procCloseClipboard.Call()
	if r, _, _ := procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("EmptyClipboard falhou")
	}
	hMem, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc falhou")
	}
	p, _, _ := syscall.Syscall(procGlobalLock.Addr(), 1, hMem, 0, 0)
	if p == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("GlobalLock falhou")
	}
	dst := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(nil), p)), len(data))
	copy(dst, data)
	syscall.Syscall(procGlobalUnlock.Addr(), 1, hMem, 0, 0)
	if r, _, _ := procSetClipboardData.Call(cfDIB, hMem); r == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("SetClipboardData(CF_DIB) falhou")
	}
	return nil
}

// dibBytes monta BITMAPINFOHEADER (40 bytes) + pixels BGRA bottom-up.
func dibBytes(img image.Image) ([]byte, error) {
	if img == nil {
		return nil, fmt.Errorf("imagem vazia")
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("dimensoes invalidas (%dx%d)", w, h)
	}
	stride := w * 4
	out := make([]byte, 40+stride*h)
	le := binary.LittleEndian
	le.PutUint32(out[0:], 40)
	le.PutUint32(out[4:], uint32(w))
	le.PutUint32(out[8:], uint32(h)) // positivo = bottom-up
	le.PutUint16(out[12:], 1)
	le.PutUint16(out[14:], 32)
	le.PutUint32(out[16:], 0) // BI_RGB
	le.PutUint32(out[20:], uint32(stride*h))

	if rgba, ok := img.(*image.RGBA); ok {
		for y := 0; y < h; y++ {
			srcRow := rgba.PixOffset(bounds.Min.X, bounds.Min.Y+(h-1-y))
			dstRow := 40 + y*stride
			for x := 0; x < w; x++ {
				s := rgba.Pix[srcRow+x*4:]
				d := out[dstRow+x*4:]
				d[0], d[1], d[2], d[3] = s[2], s[1], s[0], s[3]
			}
		}
		return out, nil
	}

	for y := 0; y < h; y++ {
		srcY := bounds.Min.Y + (h - 1 - y)
		dstRow := 40 + y*stride
		for x := 0; x < w; x++ {
			r, g, b, a := img.At(bounds.Min.X+x, srcY).RGBA()
			d := out[dstRow+x*4:]
			d[0] = byte(b >> 8)
			d[1] = byte(g >> 8)
			d[2] = byte(r >> 8)
			d[3] = byte(a >> 8)
		}
	}
	return out, nil
}

// SetClipboardText coloca o texto no clipboard do Windows.
// Após SetClipboardData, o sistema passa a ser dono da memória global — não
// liberar o handle.
//
//go:nocheckptr
func SetClipboardText(text string) error {
	utf16Str, err := syscall.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("UTF16FromString: %w", err)
	}
	byteLen := len(utf16Str) * 2
	if byteLen == 0 {
		utf16Str = []uint16{0}
		byteLen = 2
	}

	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return fmt.Errorf("OpenClipboard falhou")
	}
	defer procCloseClipboard.Call()

	if r, _, _ := procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("EmptyClipboard falhou")
	}

	hMem, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(byteLen))
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc falhou")
	}

	p, _, _ := syscall.Syscall(procGlobalLock.Addr(), 1, hMem, 0, 0)
	if p == 0 {
		return fmt.Errorf("GlobalLock falhou")
	}
	// unsafe.Add(nil, p) reconstrói o ponteiro HGLOBAL (endereço 0 + offset)
	// sem a conversão uintptr→unsafe.Pointer (evita go vet unsafeptr).
	dst := unsafe.Slice((*uint16)(unsafe.Add(unsafe.Pointer(nil), p)), len(utf16Str))
	copy(dst, utf16Str)
	syscall.Syscall(procGlobalUnlock.Addr(), 1, hMem, 0, 0)

	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, hMem); r == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("SetClipboardData falhou")
	}
	return nil
}

// GetClipboardText lê o texto atual do clipboard do Windows.
// Retorna "" (sem erro) quando o clipboard não contém texto.
//
//go:nocheckptr
func GetClipboardText() (string, error) {
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return "", fmt.Errorf("OpenClipboard falhou")
	}
	defer procCloseClipboard.Call()

	if r, _, _ := procIsClipboardFormatAvail.Call(cfUnicodeText); r == 0 {
		return "", nil // clipboard vazio ou sem texto — não é erro
	}

	hData, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if hData == 0 {
		return "", fmt.Errorf("GetClipboardData falhou")
	}

	p, _, _ := syscall.Syscall(procGlobalLock.Addr(), 1, hData, 0, 0)
	if p == 0 {
		return "", fmt.Errorf("GlobalLock falhou")
	}
	defer syscall.Syscall(procGlobalUnlock.Addr(), 1, hData, 0, 0)

	// Lê UTF-16 até o terminador nulo (limite de segurança 1 Mi chars).
	runes := make([]uint16, 0, 256)
	// unsafe.Add(nil, p) reconstrói o ponteiro HGLOBAL (endereço 0 + offset)
	// sem a conversão uintptr→unsafe.Pointer (evita go vet unsafeptr).
	base := unsafe.Add(unsafe.Pointer(nil), p)
	for i := 0; i < 1<<20; i++ {
		u := *(*uint16)(unsafe.Add(base, uintptr(i*2)))
		if u == 0 {
			break
		}
		runes = append(runes, u)
	}
	return string(utf16.Decode(runes)), nil
}
