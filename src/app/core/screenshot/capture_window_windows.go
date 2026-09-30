//go:build windows

package screenshot

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"discovery/app/core/screen"
)

// ── Captura de uma janela específica (PrintWindow) ────────────────────────
//
// PrintWindow entrega o conteúdo real da janela mesmo quando ela está
// parcialmente coberta — é o que torna a captura útil para diagnóstico pela
// IA (a tela congelada mostra o que está por cima). Fallback: BitBlt direto
// do DC da janela quando o app não responde ao PrintWindow.

const (
	PW_RENDERFULLCONTENT = 0x00000002
	SRCCOPY_WINDOW       = 0x00CC0020
	BI_RGB_WINDOW        = 0
	DIB_RGB_COLORS_W     = 0
)

var (
	gdi32S = syscall.NewLazyDLL("gdi32.dll")

	procGetDCWindow             = user32S.NewProc("GetDC")
	procGetWindowDC             = user32S.NewProc("GetWindowDC")
	procReleaseDCWindow         = user32S.NewProc("ReleaseDC")
	procPrintWindow             = user32S.NewProc("PrintWindow")
	procCreateCompatibleDCS     = gdi32S.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmapS = gdi32S.NewProc("CreateCompatibleBitmap")
	procSelectObjectS           = gdi32S.NewProc("SelectObject")
	procDeleteDCS               = gdi32S.NewProc("DeleteDC")
	procDeleteObjectS           = gdi32S.NewProc("DeleteObject")
	procBitBltS                 = gdi32S.NewProc("BitBlt")
	procGetDIBitsS              = gdi32S.NewProc("GetDIBits")
)

// captureWindow captura uma janela por handle.
func captureWindow(handle uint64, quality, maxDim int) (*CaptureResult, error) {
	hwnd := uintptr(handle)
	if hwnd == 0 {
		return nil, fmt.Errorf("handle de janela invalido")
	}
	if minimized, _, _ := procIsIconicS.Call(hwnd); minimized != 0 {
		return nil, fmt.Errorf("a janela esta minimizada — restaure-a (ou use o modo full) antes da captura")
	}
	x, y, w, h, ok := windowBounds(hwnd)
	if !ok || w <= 0 || h <= 0 {
		return nil, fmt.Errorf("nao foi possivel obter as dimensoes da janela")
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	screenDC, _, _ := procGetDCWindow.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC falhou")
	}
	defer procReleaseDCWindow.Call(0, screenDC)

	windowDC, _, _ := procGetWindowDC.Call(hwnd)
	if windowDC == 0 {
		return nil, fmt.Errorf("GetWindowDC falhou")
	}
	defer procReleaseDCWindow.Call(hwnd, windowDC)

	memDC, _, _ := procCreateCompatibleDCS.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC falhou")
	}
	defer procDeleteDCS.Call(memDC)

	bitmap, _, _ := procCreateCompatibleBitmapS.Call(screenDC, uintptr(w), uintptr(h))
	if bitmap == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap falhou")
	}
	defer procDeleteObjectS.Call(bitmap)
	procSelectObjectS.Call(memDC, bitmap)

	printed, _, _ := procPrintWindow.Call(hwnd, memDC, PW_RENDERFULLCONTENT)
	if printed == 0 {
		// Fallback: copia direto do DC da janela (pode capturar o que estiver
		// por cima, mas garante alguma imagem em apps que ignoram PrintWindow).
		copied, _, _ := procBitBltS.Call(memDC, 0, 0, uintptr(w), uintptr(h), windowDC, 0, 0, SRCCOPY_WINDOW)
		if copied == 0 {
			return nil, fmt.Errorf("PrintWindow e BitBlt falharam para a janela")
		}
	}

	buf := make([]byte, w*h*4)
	var bi [40]byte
	bi[0] = 40
	*(*int32)(unsafe.Pointer(&bi[4])) = int32(w)
	*(*int32)(unsafe.Pointer(&bi[8])) = -int32(h) // top-down
	*(*uint16)(unsafe.Pointer(&bi[12])) = 1
	*(*uint16)(unsafe.Pointer(&bi[14])) = 32
	*(*uint32)(unsafe.Pointer(&bi[16])) = BI_RGB_WINDOW
	lines, _, _ := procGetDIBitsS.Call(screenDC, bitmap, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi[0])), DIB_RGB_COLORS_W)
	if lines == 0 {
		return nil, fmt.Errorf("GetDIBits falhou")
	}

	frame := &screen.Frame{Data: buf, Width: w, Height: h, Stride: w * 4, OriginX: x, OriginY: y}
	res, err := encodeResult(frame, ModeWindow, -1, quality, maxDim)
	if err != nil {
		return nil, err
	}
	var pid uint32
	procGetWindowThreadProcessIdS.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	res.Window = &WindowInfo{
		Handle: handle, Title: windowTitle(hwnd), ProcessName: processName(pid), PID: pid,
		X: x, Y: y, Width: w, Height: h, IsSelf: pid == uint32(selfPID()),
	}
	res.OriginX = x
	res.OriginY = y
	return res, nil
}
