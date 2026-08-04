//go:build windows

package screen

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32DLL          = windows.NewLazySystemDLL("user32.dll")
	procSendInput      = user32DLL.NewProc("SendInput")
	procSetCursorPos   = user32DLL.NewProc("SetCursorPos")
	procMapVirtualKeyW = user32DLL.NewProc("MapVirtualKeyW")
)

const (
	INPUT_MOUSE    = 0
	INPUT_KEYBOARD = 1

	MOUSEEVENTF_MOVE      = 0x0001
	MOUSEEVENTF_LEFTDOWN  = 0x0002
	MOUSEEVENTF_LEFTUP    = 0x0004
	MOUSEEVENTF_RIGHTDOWN = 0x0008
	MOUSEEVENTF_RIGHTUP   = 0x0010
	MOUSEEVENTF_WHEEL     = 0x0800

	KEYEVENTF_KEYDOWN = 0x0000
	KEYEVENTF_KEYUP   = 0x0002
)

type mouseInput struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type hardwareInput struct {
	uMsg    uint32
	wParamL uint16
	wParamH uint16
}

type inputUnion struct {
	mi mouseInput
	ki keybdInput
	hi hardwareInput
}

type winInput struct {
	inputType uint32
	union     inputUnion
}

// InjectMouseMove move o cursor para posicao absoluta (0-65535).
func InjectMouseMove(x, y int32) error {
	mi := mouseInput{
		dx: x,
		dy: y,
		// MOUSEEVENTF_ABSOLUTE (0x8000) + MOUSEEVENTF_VIRTUALDESK (0x4000).
		// VIRTUALDESK é necessário para mapear coordenadas absolutas (0-65535)
		// corretamente no desktop virtual, especialmente com múltiplos monitores
		// ou DPI scaling. Sem ela, o cursor pode não ir para a posição correta.
		dwFlags: MOUSEEVENTF_MOVE | 0x8000 | 0x4000,
	}
	var inputs [1]winInput
	inputs[0].inputType = INPUT_MOUSE
	*(*mouseInput)(unsafePtr(&inputs[0].union)) = mi
	ret, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&inputs[0])), uintptr(unsafe.Sizeof(winInput{})))
	if ret == 0 {
		return fmt.Errorf("SendInput mouse move falhou")
	}
	return nil
}

// InjectMouseClickLeft simula clique esquerdo.
func InjectMouseClickLeft(down bool) error {
	mi := mouseInput{}
	if down {
		mi.dwFlags = MOUSEEVENTF_LEFTDOWN
	} else {
		mi.dwFlags = MOUSEEVENTF_LEFTUP
	}
	var inputs [1]winInput
	inputs[0].inputType = INPUT_MOUSE
	*(*mouseInput)(unsafePtr(&inputs[0].union)) = mi
	ret, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&inputs[0])), uintptr(unsafe.Sizeof(winInput{})))
	if ret == 0 {
		return fmt.Errorf("SendInput mouse click falhou")
	}
	return nil
}

// InjectMouseClickRight simula clique direito.
func InjectMouseClickRight(down bool) error {
	mi := mouseInput{}
	if down {
		mi.dwFlags = MOUSEEVENTF_RIGHTDOWN
	} else {
		mi.dwFlags = MOUSEEVENTF_RIGHTUP
	}
	var inputs [1]winInput
	inputs[0].inputType = INPUT_MOUSE
	*(*mouseInput)(unsafePtr(&inputs[0].union)) = mi
	ret, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&inputs[0])), uintptr(unsafe.Sizeof(winInput{})))
	if ret == 0 {
		return fmt.Errorf("SendInput mouse right click falhou")
	}
	return nil
}

// InjectMouseWheel simula scroll.
func InjectMouseWheel(delta int16) error {
	mi := mouseInput{
		mouseData: uint32(delta) << 16,
		dwFlags:   MOUSEEVENTF_WHEEL,
	}
	var inputs [1]winInput
	inputs[0].inputType = INPUT_MOUSE
	*(*mouseInput)(unsafePtr(&inputs[0].union)) = mi
	ret, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&inputs[0])), uintptr(unsafe.Sizeof(winInput{})))
	if ret == 0 {
		return fmt.Errorf("SendInput mouse wheel falhou")
	}
	return nil
}

// InjectKeyDown simula tecla pressionada.
func InjectKeyDown(vkCode uint16) error {
	ki := keybdInput{
		wVk: vkCode,
	}
	var inputs [1]winInput
	inputs[0].inputType = INPUT_KEYBOARD
	*(*keybdInput)(unsafePtr(&inputs[0].union)) = ki
	ret, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&inputs[0])), uintptr(unsafe.Sizeof(winInput{})))
	if ret == 0 {
		return fmt.Errorf("SendInput key down falhou para VK=0x%X", vkCode)
	}
	return nil
}

// InjectKeyUp simula tecla liberada.
func InjectKeyUp(vkCode uint16) error {
	ki := keybdInput{
		wVk:     vkCode,
		dwFlags: KEYEVENTF_KEYUP,
	}
	var inputs [1]winInput
	inputs[0].inputType = INPUT_KEYBOARD
	*(*keybdInput)(unsafePtr(&inputs[0].union)) = ki
	ret, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&inputs[0])), uintptr(unsafe.Sizeof(winInput{})))
	if ret == 0 {
		return fmt.Errorf("SendInput key up falhou para VK=0x%X", vkCode)
	}
	return nil
}

// InjectKeyPress simula tecla pressionada e liberada.
func InjectKeyPress(vkCode uint16) error {
	if err := InjectKeyDown(vkCode); err != nil {
		return err
	}
	return InjectKeyUp(vkCode)
}

// SetCursorPosAbsolute move o cursor para posicao absoluta em pixels.
func SetCursorPosAbsolute(x, y int32) error {
	ret, _, _ := procSetCursorPos.Call(uintptr(x), uintptr(y))
	if ret == 0 {
		return fmt.Errorf("SetCursorPos falhou")
	}
	return nil
}

// unsafePtr converte *inputUnion para unsafe.Pointer para escrita na union.
// mouseInput e keybdInput são os primeiros campos de inputUnion,
// então o ponteiro da struct coincide com o ponteiro do primeiro campo.
func unsafePtr(v any) unsafe.Pointer {
	return unsafe.Pointer(v.(*inputUnion))
}

// VK constants
const (
	VK_BACK    = 0x08
	VK_TAB     = 0x09
	VK_RETURN  = 0x0D
	VK_SHIFT   = 0x10
	VK_CONTROL = 0x11
	VK_MENU    = 0x12 // ALT
	VK_ESCAPE  = 0x1B
	VK_SPACE   = 0x20
	VK_LEFT    = 0x25
	VK_UP      = 0x26
	VK_RIGHT   = 0x27
	VK_DOWN    = 0x28
	VK_DELETE  = 0x2E
	VK_LWIN    = 0x5B
	VK_RWIN    = 0x5C
	VK_F1      = 0x70
	VK_F12     = 0x7B
)

// Placeholder
var _ = syscall.EINVAL
