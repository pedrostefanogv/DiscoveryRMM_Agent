//go:build windows

package remotesession

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"discovery/app/core/screen"
)

// InputVersion define o contrato canônico de input (v1).
const InputVersion = 1

// InputEvent representa um evento de input do viewer (contrato canônico v1).
type InputEvent struct {
	Version     int            `json:"version"`
	Type        string         `json:"type"` // mouse.move|mouse.down|mouse.up|mouse.wheel|key.down|key.up|clipboard
	FrameWidth  int            `json:"frameWidth"`
	FrameHeight int            `json:"frameHeight"`
	X           int            `json:"x"`
	Y           int            `json:"y"`
	Button      int            `json:"button"` // 0=left, 1=middle, 2=right
	DeltaX      int            `json:"deltaX"`
	DeltaY      int            `json:"deltaY"`
	Code        string         `json:"code"` // KeyboardEvent.code
	Key         string         `json:"key"`  // KeyboardEvent.key
	Modifiers   InputModifiers `json:"modifiers"`
	Sequence    uint64         `json:"sequence"`
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
	frameW, frameH int // dimensões do último frame
	capW, capH     int // dimensões da captura real (virtual desktop)
	rateLimiter    *rateLimiter

	// Teclas atualmente pressionadas (dedup de key-repeat).
	// O browser dispara keydown repetidamente (auto-repeat) enquanto a tecla
	// está pressionada; sem dedup, o agent injeta repetição excessiva ou
	// teclas "presas" na máquina remota.
	keysDown map[string]struct{}

	// Eventos por segundo (leaky bucket)
	lastEventTime time.Time
	eventCount    int
	maxEventsPerS int
}

// NewInputController cria um controlador de input.
func NewInputController(sessionID string) *InputController {
	return &InputController{
		sessionID:     sessionID,
		maxEventsPerS: 300,
		keysDown:      make(map[string]struct{}),
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
	// frameX/frameW * capW = desktopX (usa capW/capH = desktop real)
	absX := int32(float64(evt.X) / float64(fw) * 65535)
	absY := int32(float64(evt.Y) / float64(fh) * 65535)

	if err := screen.InjectMouseMove(absX, absY); err != nil {
		log.Printf("[input-controller] mouse move falhou: %v", err)
	}
}

func (c *InputController) handleMouseClick(evt *InputEvent, down bool) {
	switch evt.Button {
	case 0:
		if err := screen.InjectMouseClickLeft(down); err != nil {
			log.Printf("[input-controller] InjectMouseClickLeft falhou (down=%t): %v", down, err)
		}
	case 1:
		// Botão do meio — não implementado ainda
	case 2:
		if err := screen.InjectMouseClickRight(down); err != nil {
			log.Printf("[input-controller] InjectMouseClickRight falhou (down=%t): %v", down, err)
		}
	}
}

func (c *InputController) handleMouseWheel(evt *InputEvent) {
	delta := int16(evt.DeltaY)
	if delta == 0 {
		delta = int16(evt.DeltaX)
	}
	if err := screen.InjectMouseWheel(delta); err != nil {
		log.Printf("[input-controller] InjectMouseWheel falhou: %v", err)
	}
}

func (c *InputController) handleKey(code, key string, down bool, mods InputModifiers) {
	vk := mapBrowserCodeToVK(code, key)
	if vk == 0 {
		return
	}

	// Dedup de key-repeat (K2): o browser dispara keydown repetido enquanto a
	// tecla está pressionada (auto-repeat). Ignora keydown repetido da mesma
	// tecla sem keyup intermediário — evita repetição excessiva e teclas
	// "presas" na máquina remota.
	keyID := fmt.Sprintf("%s|%s", code, key)
	if keyID == "|" {
		keyID = fmt.Sprintf("vk:%d", vk)
	}
	c.mu.Lock()
	if down {
		if _, already := c.keysDown[keyID]; already {
			c.mu.Unlock()
			return // key-repeat: ignora
		}
		c.keysDown[keyID] = struct{}{}
	} else {
		delete(c.keysDown, keyID)
	}
	c.mu.Unlock()

	// Se a própria tecla é um modificador (Ctrl/Alt/Shift/Win), NÃO aplica o
	// modificador separadamente — senão injeta a tecla duas vezes (ex: Ctrl
	// pressionado → applyModifier(VK_CONTROL) + InjectKeyDown(VK_CONTROL)).
	// O vk já cobre o modificador; os mods são para combinações (ex: Ctrl+C).
	isModifier := vk == VK_CONTROL || vk == VK_MENU || vk == VK_SHIFT || vk == VK_LWIN

	if !isModifier {
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
		if mods.Meta {
			applyModifier(VK_LWIN, down)
		}
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
	// Loga apenas eventos de clique/teclado/scroll — mousemove é muito
	// frequente (~60/s) e inundaria o log com dezenas de milhares de linhas.
	// O payload completo também é omitido (só o tipo), para reduzir I/O.
	if typ != "mousemove" {
		log.Printf("[input-controller] legado: type=%s", typ)
	}

	// O viewer envia frameWidth/frameHeight no payload. Usa-os para
	// normalização quando presentes (mais preciso que o default interno),
	// evitando que o mouse vá para o lugar errado nos primeiros frames.
	if fw, ok := toFloat64(raw["frameWidth"]); ok && fw > 0 {
		if fh, ok2 := toFloat64(raw["frameHeight"]); ok2 && fh > 0 {
			c.UpdateFrameMetrics(int(fw), int(fh), int(fw), int(fh))
		}
	}

	switch typ {
	case "mousedown":
		x, _ := toFloat64(raw["x"])
		y, _ := toFloat64(raw["y"])
		btn, _ := toFloat64(raw["button"])
		// Move o mouse para a posição do clique ANTES de pressionar o botão.
		// O viewer envia x/y (coordenadas do frame) no mousedown.
		if _, hasX := raw["x"]; hasX {
			c.handleMouseMoveNormalized(int(x), int(y))
		}
		if int(btn) == 2 {
			if err := screen.InjectMouseClickRight(true); err != nil {
				log.Printf("[input-controller] mousedown (botao direito) falhou: %v", err)
			}
		} else {
			if err := screen.InjectMouseClickLeft(true); err != nil {
				log.Printf("[input-controller] mousedown falhou (btn=%d x=%d y=%d): %v", int(btn), int(x), int(y), err)
			}
		}
	case "mouseup":
		btn, _ := toFloat64(raw["button"])
		if int(btn) == 2 {
			if err := screen.InjectMouseClickRight(false); err != nil {
				log.Printf("[input-controller] mouseup (botao direito) falhou: %v", err)
			}
		} else {
			if err := screen.InjectMouseClickLeft(false); err != nil {
				log.Printf("[input-controller] mouseup falhou (btn=%d): %v", int(btn), err)
			}
		}
	case "mousemove":
		x, _ := toFloat64(raw["x"])
		y, _ := toFloat64(raw["y"])
		c.handleMouseMoveNormalized(int(x), int(y))
	case "wheel":
		dx, _ := toFloat64(raw["deltaX"])
		dy, _ := toFloat64(raw["deltaY"])
		if dy != 0 {
			if err := screen.InjectMouseWheel(int16(dy)); err != nil {
				log.Printf("[input-controller] wheel falhou (dy=%d): %v", int(dy), err)
			}
		} else if dx != 0 {
			if err := screen.InjectMouseWheel(int16(dx)); err != nil {
				log.Printf("[input-controller] wheel falhou (dx=%d): %v", int(dx), err)
			}
		}
	case "keydown":
		code, _ := raw["code"].(string)
		key, _ := raw["key"].(string)
		ctrl, _ := raw["ctrl"].(bool)
		alt, _ := raw["alt"].(bool)
		shift, _ := raw["shift"].(bool)
		meta, _ := raw["meta"].(bool)
		c.handleKey(code, key, true, InputModifiers{Ctrl: ctrl, Alt: alt, Shift: shift, Meta: meta})
	case "keyup":
		code, _ := raw["code"].(string)
		key, _ := raw["key"].(string)
		ctrl, _ := raw["ctrl"].(bool)
		alt, _ := raw["alt"].(bool)
		shift, _ := raw["shift"].(bool)
		meta, _ := raw["meta"].(bool)
		c.handleKey(code, key, false, InputModifiers{Ctrl: ctrl, Alt: alt, Shift: shift, Meta: meta})
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

	// Normaliza para o espaço absoluto do desktop virtual (0-65535).
	// frameX/frameW * 65535 = desktopX. Usa capW/capH (desktop real) como
	// denominador quando disponível, senão frameW/frameH.
	denW, denH := fw, fh
	if cw > 0 && ch > 0 {
		denW, denH = cw, ch
	}
	absX := int32(float64(x) / float64(denW) * 65535)
	absY := int32(float64(y) / float64(denH) * 65535)
	if absX < 0 {
		absX = 0
	}
	if absY < 0 {
		absY = 0
	}
	if absX > 65535 {
		absX = 65535
	}
	if absY > 65535 {
		absY = 65535
	}
	if err := screen.InjectMouseMove(absX, absY); err != nil {
		log.Printf("[input-controller] InjectMouseMove falhou (absX=%d absY=%d): %v", absX, absY, err)
	}
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
	case "Backspace":
		return VK_BACK
	case "Tab":
		return VK_TAB
	case "Enter", "NumpadEnter":
		return VK_RETURN
	case "ShiftLeft", "ShiftRight":
		return VK_SHIFT
	case "ControlLeft", "ControlRight":
		return VK_CONTROL
	case "AltLeft", "AltRight":
		return VK_MENU
	case "Escape":
		return VK_ESCAPE
	case "Space":
		return VK_SPACE
	case "ArrowLeft":
		return VK_LEFT
	case "ArrowUp":
		return VK_UP
	case "ArrowRight":
		return VK_RIGHT
	case "ArrowDown":
		return VK_DOWN
	case "Delete":
		return VK_DELETE
	case "MetaLeft", "MetaRight":
		return VK_LWIN
	case "CapsLock":
		return 0x14
	case "F1":
		return 0x70
	case "F2":
		return 0x71
	case "F3":
		return 0x72
	case "F4":
		return 0x73
	case "F5":
		return 0x74
	case "F6":
		return 0x75
	case "F7":
		return 0x76
	case "F8":
		return 0x77
	case "F9":
		return 0x78
	case "F10":
		return 0x79
	case "F11":
		return 0x7A
	case "F12":
		return 0x7B
	case "Home":
		return 0x24
	case "End":
		return 0x23
	case "PageUp":
		return 0x21
	case "PageDown":
		return 0x22
	case "Insert":
		return 0x2D
	case "PrintScreen":
		return 0x2C
	case "Pause":
		return 0x13
	case "ScrollLock":
		return 0x91
	case "NumLock":
		return 0x90
	case "ContextMenu":
		return 0x5D

	// Numpad (códigos do teclado numérico)
	case "Numpad0":
		return 0x60
	case "Numpad1":
		return 0x61
	case "Numpad2":
		return 0x62
	case "Numpad3":
		return 0x63
	case "Numpad4":
		return 0x64
	case "Numpad5":
		return 0x65
	case "Numpad6":
		return 0x66
	case "Numpad7":
		return 0x67
	case "Numpad8":
		return 0x68
	case "Numpad9":
		return 0x69
	case "NumpadMultiply":
		return 0x6A
	case "NumpadAdd":
		return 0x6B
	case "NumpadSubtract":
		return 0x6D
	case "NumpadDecimal":
		return 0x6E
	case "NumpadDivide":
		return 0x6F

	// Teclas de mídia e browser
	case "MediaPlayPause":
		return 0xB3
	case "MediaStop":
		return 0xB2
	case "MediaTrackNext":
		return 0xB0
	case "MediaTrackPrevious":
		return 0xB1
	case "VolumeMute":
		return 0xAD
	case "VolumeDown":
		return 0xAE
	case "VolumeUp":
		return 0xAF
	case "BrowserBack":
		return 0xA6
	case "BrowserForward":
		return 0xA7
	case "BrowserRefresh":
		return 0xA8
	case "BrowserStop":
		return 0xA9
	case "BrowserSearch":
		return 0xAA
	case "BrowserFavorites":
		return 0xAB
	case "BrowserHome":
		return 0xAC
	case "LaunchMail":
		return 0xB4
	case "LaunchMediaPlayer":
		return 0xB5
	case "LaunchApp1":
		return 0xB6
	case "LaunchApp2":
		return 0xB7
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
		case '.':
			return 0xBE
		case ',':
			return 0xBC
		case '/':
			return 0xBF
		case '\\':
			return 0xDC
		case '-':
			return 0xBD
		case '=':
			return 0xBB
		case ';':
			return 0xBA
		case '\'':
			return 0xDE
		case '[':
			return 0xDB
		case ']':
			return 0xDD
		case '`':
			return 0xC0
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
