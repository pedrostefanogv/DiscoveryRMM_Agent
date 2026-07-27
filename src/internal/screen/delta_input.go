package screen

import (
	"encoding/binary"
)

// DeltaInput codifica eventos de input com compressao delta.
// Em vez de enviar posicoes absolutas a cada evento, envia apenas as diferencas.

// InputEvent representa um evento de input comprimido.
type InputEvent struct {
	Type      byte   // 0=mouse, 1=key, 2=wheel, 3=clipboard
	Flags     byte   // mouse: left/right/middle, key: down/up
	DX        int16  // delta X (mouse)
	DY        int16  // delta Y (mouse)
	KeyCode   uint16 // virtual key code
	WheelDelta int16 // scroll delta
}

// DeltaInputEncoder comprime eventos de input consecutivos.
type DeltaInputEncoder struct {
	lastX, lastY int16
	keyRepeat    map[uint16]int // conta repeticoes de tecla para coalescing
}

// NewDeltaInputEncoder cria um encoder de input delta.
func NewDeltaInputEncoder() *DeltaInputEncoder {
	return &DeltaInputEncoder{
		keyRepeat: make(map[uint16]int),
	}
}

// EncodeMouseMove codifica movimento de mouse como delta.
func (e *DeltaInputEncoder) EncodeMouseMove(x, y int16) []byte {
	evt := InputEvent{
		Type: 0,
		DX:   x - e.lastX,
		DY:   y - e.lastY,
	}
	e.lastX = x
	e.lastY = y

	// Se delta zero, nao envia (coalescing)
	if evt.DX == 0 && evt.DY == 0 {
		return nil
	}

	return e.encodeEvent(evt)
}

// EncodeMouseClick codifica clique de mouse.
func (e *DeltaInputEncoder) EncodeMouseClick(button byte, down bool) []byte {
	evt := InputEvent{
		Type: 0,
		Flags: button | boolToFlag(down, 7),
	}
	return e.encodeEvent(evt)
}

// EncodeKey codifica evento de teclado com coalescing de repeticoes.
func (e *DeltaInputEncoder) EncodeKey(vkCode uint16, down bool) []byte {
	if down {
		e.keyRepeat[vkCode]++
		// Coalesce: nao envia key repeat individual
		if e.keyRepeat[vkCode] > 3 {
			return nil // ja foi enviado
		}
	} else {
		e.keyRepeat[vkCode] = 0
	}

	evt := InputEvent{
		Type:    1,
		Flags:   boolToFlag(down, 0),
		KeyCode: vkCode,
	}
	return e.encodeEvent(evt)
}

// EncodeWheel codifica scroll do mouse.
func (e *DeltaInputEncoder) EncodeWheel(delta int16) []byte {
	evt := InputEvent{
		Type:       2,
		WheelDelta: delta,
	}
	return e.encodeEvent(evt)
}

// encodeEvent serializa um evento em formato binario compacto.
// Formato: type(1) | flags(1) | dx(2) | dy(2) | keycode(2) | wheel(2)
// Total: 10 bytes por evento (vs ~80 bytes JSON)
func (e *DeltaInputEncoder) encodeEvent(evt InputEvent) []byte {
	buf := make([]byte, 10)
	buf[0] = evt.Type
	buf[1] = evt.Flags
	binary.BigEndian.PutUint16(buf[2:4], uint16(evt.DX))
	binary.BigEndian.PutUint16(buf[4:6], uint16(evt.DY))
	binary.BigEndian.PutUint16(buf[6:8], evt.KeyCode)
	binary.BigEndian.PutUint16(buf[8:10], uint16(evt.WheelDelta))
	return buf
}

// DecodeEvent decodifica um evento de input binario.
func DecodeEvent(data []byte) (InputEvent, bool) {
	if len(data) < 10 {
		return InputEvent{}, false
	}
	return InputEvent{
		Type:       data[0],
		Flags:      data[1],
		DX:         int16(binary.BigEndian.Uint16(data[2:4])),
		DY:         int16(binary.BigEndian.Uint16(data[4:6])),
		KeyCode:    binary.BigEndian.Uint16(data[6:8]),
		WheelDelta: int16(binary.BigEndian.Uint16(data[8:10])),
	}, true
}

func boolToFlag(b bool, bit uint8) byte {
	if b {
		return 1 << bit
	}
	return 0
}

// Ensure imports
var _ = binary.BigEndian
