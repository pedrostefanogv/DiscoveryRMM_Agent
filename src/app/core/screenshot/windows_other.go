//go:build !windows

package screenshot

import "errors"

var errNotSupported = errors.New("captura de tela suportada apenas no Windows")

// ListWindows não tem equivalente fora do Windows.
func ListWindows() ([]WindowInfo, error) { return nil, errNotSupported }

// ListWindowsWithOptions não tem equivalente fora do Windows.
func ListWindowsWithOptions(includeUntitled bool) ([]WindowInfo, error) {
	return nil, errNotSupported
}

// ForegroundWindowHandle não tem equivalente fora do Windows.
func ForegroundWindowHandle() uint64 { return 0 }

// captureWindow não tem equivalente fora do Windows.
func captureWindow(handle uint64, quality, maxDim int) (*CaptureResult, error) {
	return nil, errNotSupported
}
