//go:build windows

package screenshot

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"discovery/app/core/screen"
)

// ── Captura de uma janela específica (PrintWindow com watchdog) ───────────
//
// PrintWindow entrega o conteúdo real da janela mesmo quando ela está
// parcialmente coberta — é o que torna a captura útil para diagnóstico pela
// IA. Porém apps travados podem PENDURAR a chamada indefinidamente; por isso
// ela roda em goroutine própria com timeout: estourou, marca a janela como
// "sem PrintWindow" por um período e usa BitBlt (rápido) como fallback.

const (
	PW_RENDERFULLCONTENT = 0x00000002
	SRCCOPY_WINDOW       = 0x00CC0020
	BI_RGB_WINDOW        = 0
	DIB_RGB_COLORS_W     = 0
	// printWindowTimeout é o teto de espera pelo PrintWindow de uma janela.
	printWindowTimeout = 5 * time.Second
	// printWindowSkipTTL evita retentar PrintWindow em janela que já travou.
	printWindowSkipTTL = 60 * time.Second
	// printWindowSkipCleanup remove entradas antigas do cache de skip.
	printWindowSkipCleanup = 10 * time.Minute
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

	printWindowSkipMu sync.Mutex
	printWindowSkip   = map[uintptr]time.Time{}
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

	var frame *screen.Frame
	now := time.Now()
	if !shouldSkipPrintWindow(hwnd, now) {
		printed, err := windowFrameWithTimeout(hwnd, w, h, printWindowTimeout)
		if err == nil {
			frame = printed
		} else {
			markPrintWindowSkip(hwnd, now)
		}
	}
	if frame == nil {
		bit, err := windowFrameFromGDI(hwnd, w, h, false)
		if err != nil {
			return nil, fmt.Errorf("captura da janela falhou (PrintWindow e BitBlt): %w", err)
		}
		frame = bit
	}

	frame.OriginX, frame.OriginY = x, y
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

type windowFrameResult struct {
	frame *screen.Frame
	err   error
}

// windowFrameWithTimeout executa o PrintWindow em goroutine própria e devolve
// erro se a janela não responder dentro do timeout. A goroutine presa não pode
// ser cancelada (limitação do Win32): os recursos dela são liberados quando a
// chamada finalmente retornar, e o cache de skip evita acumular novas.
func windowFrameWithTimeout(hwnd uintptr, w, h int, timeout time.Duration) (*screen.Frame, error) {
	ch := make(chan windowFrameResult, 1)
	go func() {
		frame, err := windowFrameFromGDI(hwnd, w, h, true)
		ch <- windowFrameResult{frame: frame, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case out := <-ch:
		return out.frame, out.err
	case <-timer.C:
		return nil, fmt.Errorf("PrintWindow excedeu %s (janela sem resposta)", timeout)
	}
}

// windowFrameFromGDI captura a janela via PrintWindow (usePrintWindow=true) ou
// BitBlt do DC da janela (false). Retorna BGRA top-down.
func windowFrameFromGDI(hwnd uintptr, w, h int, usePrintWindow bool) (*screen.Frame, error) {
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

	copied := false
	if usePrintWindow {
		printed, _, _ := procPrintWindow.Call(hwnd, memDC, PW_RENDERFULLCONTENT)
		copied = printed != 0
	}
	if !copied {
		// BitBlt direto do DC da janela: pode capturar o que estiver por cima,
		// mas funciona em apps que ignoram PrintWindow e não bloqueia como ele.
		ret, _, _ := procBitBltS.Call(memDC, 0, 0, uintptr(w), uintptr(h), windowDC, 0, 0, SRCCOPY_WINDOW)
		if ret == 0 {
			return nil, fmt.Errorf("PrintWindow/BitBlt falharam para a janela")
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
	return &screen.Frame{Data: buf, Width: w, Height: h, Stride: w * 4}, nil
}

func shouldSkipPrintWindow(hwnd uintptr, now time.Time) bool {
	printWindowSkipMu.Lock()
	defer printWindowSkipMu.Unlock()
	until, ok := printWindowSkip[hwnd]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(printWindowSkip, hwnd)
		return false
	}
	return true
}

func markPrintWindowSkip(hwnd uintptr, now time.Time) {
	printWindowSkipMu.Lock()
	defer printWindowSkipMu.Unlock()
	for handle, until := range printWindowSkip {
		if now.Sub(until) > printWindowSkipCleanup {
			delete(printWindowSkip, handle)
		}
	}
	printWindowSkip[hwnd] = now.Add(printWindowSkipTTL)
}
