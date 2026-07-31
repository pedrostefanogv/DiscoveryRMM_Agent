//go:build windows

package screen

import (
	"encoding/binary"
	"fmt"
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

// CursorSpriteSender envia o cursor separadamente do frame para reduzir banda.
// Atualiza apenas quando o cursor muda de posicao ou forma.
type CursorSpriteSender struct {
	last CursorInfo
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

// GetCursorShape tenta inferir a forma do cursor a partir do ID do sistema.
// Retorna um dos valores reconhecidos pelo viewer (arrow/hand/ibeam/crosshair).
func GetCursorShape() string {
	// Fallback simples — sem mapeamento do sistema aqui para evitar
	// dependencia de LoadCursor. O capturer DXGI go-d3d já desenha o cursor
	// real como overlay quando DrawPointer=true; este método serve para
	// o modo cursor-separado.
	return "arrow"
}

// Ensure imports
var _ = syscall.NewLazyDLL
var _ = procGetIconInfo
var _ = procGetSystemCursor
var _ = procDestroyIcon
