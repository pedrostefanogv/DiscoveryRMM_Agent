//go:build windows

package notifyui

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── MessageBox nativa (compatibilidade do console de debug com o antigo
// Show-ADTDialogBox: o resultado é o texto do botão clicado) ──

var procMessageBoxW = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

// MessageBoxFlags mapeia o conjunto de botões para os flags do MessageBoxW.
func MessageBoxFlags(buttons string) uintptr {
	switch strings.ToLower(strings.TrimSpace(buttons)) {
	case "okcancel":
		return 0x00000001
	case "abortretryignore":
		return 0x00000002
	case "yesnocancel":
		return 0x00000003
	case "yesno":
		return 0x00000004
	case "retrycancel":
		return 0x00000005
	case "canceltrycontinue":
		return 0x00000006
	default:
		return 0x00000000 // OK
	}
}

// MessageBoxButtonText traduz o retorno do MessageBoxW para o texto do botão.
func MessageBoxButtonText(id int, buttons string) string {
	switch strings.ToLower(strings.TrimSpace(buttons)) {
	case "okcancel":
		if id == 1 {
			return "OK"
		}
		return "Cancel"
	case "abortretryignore":
		switch id {
		case 3:
			return "Abort"
		case 4:
			return "Retry"
		case 5:
			return "Ignore"
		}
	case "yesnocancel":
		switch id {
		case 6:
			return "Yes"
		case 7:
			return "No"
		case 2:
			return "Cancel"
		}
	case "yesno":
		if id == 6 {
			return "Yes"
		}
		return "No"
	case "retrycancel":
		if id == 4 {
			return "Retry"
		}
		return "Cancel"
	case "canceltrycontinue":
		switch id {
		case 2:
			return "Cancel"
		case 10:
			return "Try Again"
		case 11:
			return "Continue"
		}
	default:
		return "OK"
	}
	return ""
}

// ShowMessageBox exibe uma MessageBox Win32 síncrona e devolve o texto do
// botão clicado.
func ShowMessageBox(title, text, buttons string) (string, error) {
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return "", err
	}
	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return "", err
	}
	// MB_SETFOREGROUND (0x00010000): o diálogo de notificação não deve nascer
	// atrás de outras janelas.
	flags := MessageBoxFlags(buttons) | 0x00010000
	ret, _, callErr := procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		flags,
	)
	if ret == 0 {
		return "", callErr
	}
	return MessageBoxButtonText(int(ret), buttons), nil
}
