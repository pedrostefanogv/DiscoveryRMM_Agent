package remotesession

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"discovery/internal/screen"
)

// InputVersion define o contrato canônico de input (v1).
const InputVersion = 1

// InputEvent representa um evento de input do viewer (contrato canônico v1).
type InputEvent struct {
	Version     int              `json:"version"`
	Type        string           `json:"type"` // mouse.move|mouse.down|mouse.up|mouse.wheel|key.down|key.up|clipboard
	FrameWidth  int              `json:"frameWidth"`
	FrameHeight int              `json:"frameHeight"`
	X           int              `json:"x"`
	Y           int              `json:"y"`
	Button      int              `json:"button"` // 0=left, 1=middle, 2=right
	DeltaX      int              `json:"deltaX"`
	DeltaY      int              `json:"deltaY"`
	Code        string           `json:"code"`  // KeyboardEvent.code
	Key         string           `json:"key"`   // KeyboardEvent.key
	Modifiers   InputModifiers   `json:"modifiers"`
	Sequence    uint64           `json:"sequence"`
}

// InputModifiers representa teclas modificadoras.
type InputModifiers struct {
	Ctrl  bool `json:"ctrl"`
	Alt   bool `json:"alt"`
	Shift bool `json:"shift"`
	Meta  bool `json:"meta"`
}

// InputController gerencia entrada do viewer e injeta no Windows.
type InputController struct {
	sessionID string

	mu             sync.Mutex
	lastSeq        uint64
	frameW, frameH int    // dimensões do último frame
	capW, capH     int    // dimensões da captura real (virtual desktop)
	rateLimiter    *rateLimiter

	// Eventos por segundo (leaky bucket)
	lastEventTime time.Time
	eventCount    int
	maxEventsPerS int
}

// NewInputController cria um controlador de input.
func NewInputController(sessionID string) *InputController {
	return &InputController{
		sessionID:     sessionID,
		maxEventsPerS: 100,
	}
}

// UpdateFrameMetrics atualiza dimensões do frame e captura para normalização.
func (c *InputController) UpdateFrameMetrics(frameW, frameH, capW, capH int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frameW = frameW
	c.frameH = frameH
	c.capW = capW
	c.capH = capH
}

// HandleInput processa um evento de input raw do viewer.
func (c *InputController) HandleInput(data []byte) {
	// Rate limit
	if !c.checkRateLimit() {
		return
	}

	var evt InputEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		// Tenta formato legado (JSON simples sem version)
		log.Printf("[input-controller] JSON inválido, tentando legado: %v", err)
		c.handleLegacyInput(data)
		return
	}

	// Se version é 0 (ausente), trata como formato legado
	if evt.Version == 0 {
		c.handleLegacyInput(data)
		return
	}

	if evt.Version != InputVersion {
		log.Printf("[input-controller] versão não suportada: %d", evt.Version)
		return
	}

	log.Printf("[input-controller] recebido: type=%s x=%d y=%d button=%d deltaX=%d deltaY=%d",
		evt.Type, evt.X, evt.Y, evt.Button, evt.DeltaX, evt.DeltaY)

	// Dedup por sequência
	c.mu.Lock()
	if evt.Sequence > 0 && evt.Sequence <= c.lastSeq {
		c.mu.Unlock()
		return
	}
	c.lastSeq = evt.Sequence
	c.mu.Unlock()

	switch evt.Type {
	case "mouse.move":
		c.handleMouseMove(&evt)
	case "mouse.down":
		c.handleMouseClick(&evt, true)
	case "mouse.up":
		c.handleMouseClick(&evt, false)
	case "mouse.wheel":
		c.handleMouseWheel(&evt)
	case "key.down":
		c.handleKey(evt.Code, evt.Key, true, evt.Modifiers)
	case "key.up":
		c.handleKey(evt.Code, evt.Key, false, evt.Modifiers)
	default:
		log.Printf("[input-controller] tipo desconhecido: %s", evt.Type)
	}
}

func (c *InputController) handleMouseMove(evt *InputEvent) {
	c.mu.Lock()
	fw, fh := c.frameW, c.frameH
	cw, ch := c.capW, c.capH
	c.mu.Unlock()

	if fw <= 0 || fh <= 0 || cw <= 0 || ch <= 0 {
		return
	}

	// Converte coordenadas do frame para desktop virtual
	// frameX/frameW * capW = desktopX
	absX := int32(float64(evt.X) / float64(fw) * 65535)
	absY := int32(float64(evt.Y) / float64(fh) * 65535)

	if err := screen.InjectMouseMove(absX, absY); err != nil {
		log.Printf("[input-controller] mouse move falhou: %v", err)
	}
}

func (c *InputController) handleMouseClick(evt *InputEvent, down bool) {
	switch evt.Button {
	case 0:
		_ = screen.InjectMouseClickLeft(down)
	case 1:
		// Botão do meio — não implementado ainda
	case 2:
		_ = screen.InjectMouseClickRight(down)
	}
}

func (c *InputController) handleMouseWheel(evt *InputEvent) {
	delta := int16(evt.DeltaY)
	if delta == 0 {
		delta = int16(evt.DeltaX)
	}
	_ = screen.InjectMouseWheel(delta)
}

func (c *InputController) handleKey(code, key string, down bool, mods InputModifiers) {
	vk := mapBrowserCodeToVK(code, key)
	if vk == 0 {
		return
	}

	// Aplica modificadores antes da tecla
	if mods.Ctrl {
		applyModifier(VK_CONTROL, down)
	}
	if mods.Alt {
		applyModifier(VK_MENU, down)
	}
	if mods.Shift {
		applyModifier(VK_SHIFT, down)
	}

	if down {
		_ = screen.InjectKeyDown(vk)
	} else {
		_ = screen.InjectKeyUp(vk)
	}
}

func applyModifier(vk uint16, down bool) {
	if down {
		_ = screen.InjectKeyDown(vk)
	} else {
		_ = screen.InjectKeyUp(vk)
	}
}

// handleLegacyInput processa formato JSON simples (usado pelo viewer atual).
func (c *InputController) handleLegacyInput(data []byte) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	typ, _ := raw["type"].(string)
	log.Printf("[input-controller] legado: type=%s raw=%s", typ, string(data))

	switch typ {
	case "mousedown":
		x, _ := toFloat64(raw["x"]); y, _ := toFloat64(raw["y"]); btn, _ := toFloat64(raw["button"])
		c.handleMouseMoveNormalized(int(x), int(y))
		if int(btn) == 2 {
			_ = screen.InjectMouseClickRight(true)
		} else {
			_ = screen.InjectMouseClickLeft(true)
		}
	case "mouseup":
		btn, _ := toFloat64(raw["button"])
		if int(btn) == 2 {
			_ = screen.InjectMouseClickRight(false)
		} else {
			_ = screen.InjectMouseClickLeft(false)
		}
	case "mousemove":
		x, _ := toFloat64(raw["x"]); y, _ := toFloat64(raw["y"])
		c.handleMouseMoveNormalized(int(x), int(y))
	case "wheel":
		dx, _ := toFloat64(raw["deltaX"]); dy, _ := toFloat64(raw["deltaY"])
		if dy != 0 {
			_ = screen.InjectMouseWheel(int16(dy))
		} else if dx != 0 {
			_ = screen.InjectMouseWheel(int16(dx))
		}
	case "keydown":
		code, _ := raw["code"].(string); key, _ := raw["key"].(string)
		ctrl, _ := raw["ctrl"].(bool); alt, _ := raw["alt"].(bool); shift, _ := raw["shift"].(bool)
		c.handleKey(code, key, true, InputModifiers{Ctrl: ctrl, Alt: alt, Shift: shift})
	case "keyup":
		code, _ := raw["code"].(string); key, _ := raw["key"].(string)
		c.handleKey(code, key, false, InputModifiers{})
	}
}

func (c *InputController) handleMouseMoveNormalized(x, y int) {
	c.mu.Lock()
	fw, fh := c.frameW, c.frameH
	cw, ch := c.capW, c.capH
	c.mu.Unlock()

	if fw <= 0 || fh <= 0 {
		fw, fh = 1920, 1080
	}
	if cw <= 0 || ch <= 0 {
		cw, ch = 1920, 1080
	}

	absX := int32(float64(x) / float64(fw) * 65535)
	absY := int32(float64(y) / float64(fh) * 65535)
	_ = screen.InjectMouseMove(absX, absY)
}

func (c *InputController) checkRateLimit() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if now.Sub(c.lastEventTime) >= time.Second {
		c.eventCount = 0
		c.lastEventTime = now
	}

	c.eventCount++
	return c.eventCount <= c.maxEventsPerS
}

// rateLimiter é um placeholder para implementação futura.
type rateLimiter struct{}

// Keyboard code → Virtual Key mapping (parcial, cobrindo casos comuns)
func mapBrowserCodeToVK(code, key string) uint16 {
	// Mapeamento por code (mais confiável)
	switch code {
	case "Backspace": return VK_BACK
	case "Tab": return VK_TAB
	case "Enter", "NumpadEnter": return VK_RETURN
	case "ShiftLeft", "ShiftRight": return VK_SHIFT
	case "ControlLeft", "ControlRight": return VK_CONTROL
	case "AltLeft", "AltRight": return VK_MENU
	case "Escape": return VK_ESCAPE
	case "Space": return VK_SPACE
	case "ArrowLeft": return VK_LEFT
	case "ArrowUp": return VK_UP
	case "ArrowRight": return VK_RIGHT
	case "ArrowDown": return VK_DOWN
	case "Delete": return VK_DELETE
	case "MetaLeft", "MetaRight": return VK_LWIN
	case "CapsLock": return 0x14
	case "F1": return 0x70
	case "F2": return 0x71
	case "F3": return 0x72
	case "F4": return 0x73
	case "F5": return 0x74
	case "F6": return 0x75
	case "F7": return 0x76
	case "F8": return 0x77
	case "F9": return 0x78
	case "F10": return 0x79
	case "F11": return 0x7A
	case "F12": return 0x7B
	case "Home": return 0x24
	case "End": return 0x23
	case "PageUp": return 0x21
	case "PageDown": return 0x22
	case "Insert": return 0x2D
	case "PrintScreen": return 0x2C
	}

	// Fallback por key (menos confiável, depende do layout)
	if len(key) == 1 {
		ch := key[0]
		if ch >= 'a' && ch <= 'z' {
			return uint16(ch - 'a' + 'A') // uppercase virtual key
		}
		if ch >= 'A' && ch <= 'Z' {
			return uint16(ch)
		}
		if ch >= '0' && ch <= '9' {
			return uint16(ch)
		}
		// Símbolos comuns
		switch ch {
		case '.': return 0xBE
		case ',': return 0xBC
		case '/': return 0xBF
		case '\\': return 0xDC
		case '-': return 0xBD
		case '=': return 0xBB
		case ';': return 0xBA
		case '\'': return 0xDE
		case '[': return 0xDB
		case ']': return 0xDD
		case '`': return 0xC0
		}
	}

	return 0
}

// VK redefinidos localmente para evitar conflito com screen package
const (
	VK_BACK    = 0x08
	VK_TAB     = 0x09
	VK_RETURN  = 0x0D
	VK_SHIFT   = 0x10
	VK_CONTROL = 0x11
	VK_MENU    = 0x12
	VK_ESCAPE  = 0x1B
	VK_SPACE   = 0x20
	VK_LEFT    = 0x25
	VK_UP      = 0x26
	VK_RIGHT   = 0x27
	VK_DOWN    = 0x28
	VK_DELETE  = 0x2E
	VK_LWIN    = 0x5B
)

var _ = fmt.Sprintf // unused import guard
