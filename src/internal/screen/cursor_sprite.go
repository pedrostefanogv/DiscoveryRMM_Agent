package screen

import (
	"encoding/binary"
	"fmt"
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

// GetCursorPos retorna a posicao atual do cursor via GetCursorPos Win32.
func GetCursorPos() (CursorInfo, error) {
	// Placeholder: seria implementado via syscall GetCursorPos + GetCursorInfo
	// Na Fase 5, retorna posicao fixa como fallback
	return CursorInfo{X: 0, Y: 0, Visible: true, Shape: "arrow"}, nil
}

// Ensure imports
var _ = fmt.Println
var _ = binary.BigEndian
