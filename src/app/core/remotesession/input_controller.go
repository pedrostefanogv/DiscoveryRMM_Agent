//go:build windows

package remotesession

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"discovery/app/core/screen"
)

// InputVersion define o contrato canônico de input (v1).
const InputVersion = 1

// Pontos de injeção (vars, não chamadas diretas): permitem stub nos testes
// sem alterar o pacote screen.
var (
	injectKeyDown    = screen.InjectKeyDown
	injectKeyUp      = screen.InjectKeyUp
	injectMouseMove  = screen.InjectMouseMove
	injectMouseLeft  = screen.InjectMouseClickLeft
	injectMouseRight = screen.InjectMouseClickRight
	injectMouseWheel = screen.InjectMouseWheel
)

// virtualDesktopBounds fornece a geometria do desktop virtual FÍSICO
// (var para stub nos testes). O mapeamento do mouse precisa dela porque
// InjectMouseMove usa MOUSEEVENTF_ABSOLUTE|MOUSEEVENTF_VIRTUALDESK — o
// espaço 0..65535 cobre o desktop virtual INTEIRO, não o monitor capturado.
var virtualDesktopBounds = screen.VirtualDesktopBounds

// Watchdog de teclas presas: uma tecla "down" sem NENHUM evento de teclado
// por keyStuckTimeout é liberada (keyup injetado). O browser emite keydown
// repetido (~30/s) enquanto a tecla está segurada, então silêncio prolongado
// com tecla "down" só acontece quando o keyup se perde (viewer fechado, foco
// migrou para input local, queda de rede). Vars para os testes encurtarem.
var (
	keyStuckTimeout = 2 * time.Second
	keyWatchdogTick = 500 * time.Millisecond
)

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
	frameW, frameH int // dimensões do último frame (pixels do frame codificado)
	capW, capH     int // dimensões da captura real no desktop (pode diferir do frame com escala)
	// frameOriginX/Y: origem do canto superior esquerdo do frame no desktop
	// virtual FÍSICO (pixels). Vem do capturer (Frame.OriginX/OriginY). Com
	// multi-monitor ou monitor não-primário, a origem NÃO é (0,0) — ignorá-la
	// desloca todos os cliques (bug DPI/multi-monitor do acesso remoto).
	frameOriginX, frameOriginY int
	rateLimiter                *rateLimiter

	// Teclas logicamente pressionadas (keyID → VK). NÃO é mais filtro de
	// key-repeat: os repeats do browser são REPASSADOS ao remoto (ver
	// handleKey). O mapa existe para o watchdog liberar teclas "presas"
	// (keyup perdido em queda de conexão / foco perdido no viewer) e para
	// liberar tudo no encerramento da sessão (Close).
	keysDown map[string]uint16

	// Modificadores (Ctrl/Alt/Shift/Win) atualmente INJETADOS como down.
	// Sincronizados com os flags do evento (syncModifiers): injeta uma única
	// vez antes da tecla (repeats não re-injetam) e libera quando os flags
	// deixam de indicar o modificador ou no keyup da própria tecla.
	modsActive map[uint16]bool

	// Último evento de teclado recebido — base do watchdog de teclas presas:
	// tecla "down" sem nenhum evento por > keyStuckTimeout (viewer fechou /
	// foco migrou para input local / keyup perdido na rede) é liberada.
	lastKeyActivity time.Time

	keyWatchdogStop chan struct{}
	keyWatchdogDone chan struct{} // fechado quando watchdogLoop sai (Close aguarda)
	closeOnce       sync.Once
	closed          bool

	// Eventos por segundo (leaky bucket)
	lastEventTime time.Time
	eventCount    int
	maxEventsPerS int

	// Rate-limit de LOGS de falha de injeção: mousemove chega a ~60/s — sem
	// agregação, um SendInput falhando (UIPI/desktop trocado) inunda o log com
	// milhares de linhas idênticas por minuto. Loga no máximo 1 linha/3s.
	lastErrLog time.Time

	// netstatsHandler recebe as métricas de rede medidas no VIEWER (type
	// "netstats" via .input, a cada 2s) — alimenta a escada adaptativa do
	// QualityManager. Opcional: nil = métricas ignoradas.
	netstatsHandler func(rttMs, recvKbps float64, recvFrames int)
}

// logInputFailure registra a falha de injeção com agregação (1 linha/3s).
// O erro carrega o diagnóstico completo (sendInputErr anota o mecanismo UIPI
// no errno=5), então a linha agregada basta para suporte.
func (c *InputController) logInputFailure(op string, err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	if !c.lastErrLog.IsZero() && time.Since(c.lastErrLog) < 3*time.Second {
		c.mu.Unlock()
		return
	}
	c.lastErrLog = time.Now()
	c.mu.Unlock()
	log.Printf("[input-controller] %s falhou (agregado 1/3s): %v", op, err)
}

// SetNetstatsHandler registra o receptor das métricas de rede do viewer.
func (c *InputController) SetNetstatsHandler(fn func(rttMs, recvKbps float64, recvFrames int)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.netstatsHandler = fn
}

// NewInputController cria um controlador de input.
func NewInputController(sessionID string) *InputController {
	c := &InputController{
		sessionID:       sessionID,
		maxEventsPerS:   300,
		keysDown:        make(map[string]uint16),
		modsActive:      make(map[uint16]bool),
		keyWatchdogStop: make(chan struct{}),
		keyWatchdogDone: make(chan struct{}),
	}
	go c.watchdogLoop()
	return c
}

// Close encerra o watchdog e libera qualquer tecla/modificador que tenha
// ficado pressionado no remoto (ex.: viewer fechou com a tecla segurada).
// Idempotente.
func (c *InputController) Close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		close(c.keyWatchdogStop)
		<-c.keyWatchdogDone // watchdog parado antes da liberação final
		c.releaseAllKeys("sessão encerrada")
	})
}

// watchdogLoop verifica periodicamente se há teclas presas.
func (c *InputController) watchdogLoop() {
	defer close(c.keyWatchdogDone)
	ticker := time.NewTicker(keyWatchdogTick)
	defer ticker.Stop()
	for {
		select {
		case <-c.keyWatchdogStop:
			return
		case <-ticker.C:
			c.mu.Lock()
			hasDown := len(c.keysDown) > 0 || len(c.modsActive) > 0
			stale := !c.lastKeyActivity.IsZero() && time.Since(c.lastKeyActivity) >= keyStuckTimeout
			c.mu.Unlock()
			if hasDown && stale {
				c.releaseAllKeys("watchdog keyup perdido")
			}
		}
	}
}

// releaseAllKeys injeta keyup para todas as teclas/modificadores marcados
// como pressionados e limpa o estado.
func (c *InputController) releaseAllKeys(reason string) {
	c.mu.Lock()
	if len(c.keysDown) == 0 && len(c.modsActive) == 0 {
		c.mu.Unlock()
		return
	}
	down := make([]uint16, 0, len(c.keysDown))
	for _, vk := range c.keysDown {
		down = append(down, vk)
	}
	mods := make([]uint16, 0, len(c.modsActive))
	for vk := range c.modsActive {
		mods = append(mods, vk)
	}
	c.keysDown = make(map[string]uint16)
	c.modsActive = make(map[uint16]bool)
	c.mu.Unlock()

	for _, vk := range mods {
		if err := injectKeyUp(vk); err != nil {
			c.logInputFailure(fmt.Sprintf("key up modificador (%s) VK=0x%X", reason, vk), err)
		}
	}
	for _, vk := range down {
		if err := injectKeyUp(vk); err != nil {
			c.logInputFailure(fmt.Sprintf("key up (%s) VK=0x%X", reason, vk), err)
		}
	}
	log.Printf("[input-controller] %s: %d tecla(s) e %d modificador(es) liberados", reason, len(down), len(mods))
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

// UpdateFrameDims atualiza APENAS as dimensões do frame codificado (informadas
// pelo viewer no payload legado frameWidth/frameHeight). NÃO sobrescreve
// capW/capH já definidos pelo capturer (cobertura real da captura — difere do
// frame quando o agente reduz a resolução com escala<1). Quando capW/capH
// ainda estão zerados (pré-primeiro frame), usa as dimensões do viewer como
// bootstrap provisório — sem isso, eventos mouse.move do contrato v1 eram
// descartados até o primeiro frame codificado (cw/ch>0 exigido em
// handleMouseMove) e o fallback do legado usava default fixo 1920x1080.
func (c *InputController) UpdateFrameDims(frameW, frameH int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frameW = frameW
	c.frameH = frameH
	if c.capW <= 0 || c.capH <= 0 {
		c.capW = frameW
		c.capH = frameH
	}
}

// UpdateFrameOrigin atualiza a origem (desktop virtual físico, em pixels) do
// canto superior esquerdo da região capturada. Deve refletir o capturer ATIVO
// — a troca GDI→DXGI (fallback) pode mudar monitor/origem.
func (c *InputController) UpdateFrameOrigin(x, y int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frameOriginX = x
	c.frameOriginY = y
}

// mapFrameToAbsolute converte coordenadas (x,y) em pixels do frame para o
// espaço absoluto 0..65535 do desktop virtual FÍSICO — o espaço que o
// SendInput entende com MOUSEEVENTF_ABSOLUTE|MOUSEEVENTF_VIRTUALDESK.
//
// Pipeline (DPI-correct):
//  1. frame → desktop: escala frame/captura (quando o agente reduz o frame)
//     + origem da captura no desktop virtual;
//  2. desktop → 0..65535: posição RELATIVA ao retângulo do desktop virtual
//     (GetSystemMetrics SM_*VIRTUALSCREEN), não ao monitor primário.
//
// O mapeamento antigo (x/frameW*65535) tratava o frame capturado como se
// fosse o desktop virtual inteiro começando em (0,0). Só é correto quando a
// captura cobre o desktop todo — monitor único primário. Com dois monitores
// (setup típico de laptop com escala 125%) os cliques caíam no lugar errado;
// em processo DPI-unaware as métricas vinham virtualizadas (125% → lógico),
// deslocando também o cursor/overlay. ok=false quando as métricas são
// insuficientes (caller aplica o fallback legado proporcional).
func mapFrameToAbsolute(x, y, frameW, frameH, capW, capH, originX, originY, vx, vy, vw, vh int) (absX, absY int32, ok bool) {
	if frameW <= 0 || frameH <= 0 || capW <= 0 || capH <= 0 || vw <= 0 || vh <= 0 {
		return 0, 0, false
	}

	// 1. Pixel do frame → pixel do desktop virtual físico.
	deskX := float64(originX) + float64(x)*float64(capW)/float64(frameW)
	deskY := float64(originY) + float64(y)*float64(capH)/float64(frameH)

	// 2. Pixel do desktop → absoluto 0..65535 relativo ao desktop virtual.
	ax := clampAbsolute((deskX - float64(vx)) / float64(vw) * 65535.0)
	ay := clampAbsolute((deskY - float64(vy)) / float64(vh) * 65535.0)
	return ax, ay, true
}

// clampAbsolute limita o valor absoluto ao espaço 0..65535 (com arredondamento).
func clampAbsolute(v float64) int32 {
	if v < 0 {
		return 0
	}
	if v > 65535 {
		return 65535
	}
	return int32(math.Round(v))
}

// HandleInput processa um evento de input raw do viewer.
func (c *InputController) HandleInput(data []byte) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	// Anexa o thread ao input desktop ativo ANTES de injetar. Sem isso, o
	// SendInput falha com Acesso negado (UIPI) quando o desktop mudou desde a
	// criação do thread (UAC/secure desktop, lock screen, logon) — o thread
	// continua preso no desktop antigo. Padrão MeshAgent CheckDesktopSwitch.
	// Erro é não-fatal: em desktop normal o thread já está no desktop certo.
	if _, err := screen.CheckDesktopSwitch(); err != nil {
		log.Printf("[input-controller] aviso desktop switch: %v", err)
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

	// Rate limit — key.up NUNCA é descartado: um keyup perdido deixa a tecla
	// "presa" (logicamente down) no remoto até o watchdog agir.
	if !c.checkRateLimit(evt.Type == "key.up") {
		return
	}

	// Dedup por sequência
	c.mu.Lock()
	if evt.Sequence > 0 && evt.Sequence <= c.lastSeq {
		c.mu.Unlock()
		return
	}
	c.lastSeq = evt.Sequence
	c.mu.Unlock()

	// Log seletivo: mousemove (~60/s) e key.down/key.up durante hold
	// (auto-repeat ~30/s) inundariam o log — eventos de alta frequência são
	// silenciosos; eventos raros (cliques, wheel, clipboard) são logados.
	switch evt.Type {
	case "mouse.move", "key.down", "key.up":
	default:
		log.Printf("[input-controller] recebido: type=%s x=%d y=%d button=%d deltaX=%d deltaY=%d",
			evt.Type, evt.X, evt.Y, evt.Button, evt.DeltaX, evt.DeltaY)
	}

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
	ox, oy := c.frameOriginX, c.frameOriginY
	c.mu.Unlock()

	if fw <= 0 || fh <= 0 || cw <= 0 || ch <= 0 {
		return
	}

	// Converte frame → desktop virtual físico → absoluto 0..65535 relativo ao
	// RETÂNGULO do desktop virtual (mapeamento DPI-correct; ver
	// mapFrameToAbsolute e handleMouseMoveNormalized).
	if vx, vy, vw, vh, ok := virtualDesktopBounds(); ok {
		if absX, absY, ok2 := mapFrameToAbsolute(evt.X, evt.Y, fw, fh, cw, ch, ox, oy, vx, vy, vw, vh); ok2 {
			if err := injectMouseMove(absX, absY); err != nil {
				c.logInputFailure("mouse move", err)
			}
			return
		}
	}

	// Fallback legado (métricas do desktop virtual indisponíveis): proporcional
	// ao frame — só correto quando a captura cobre o desktop virtual inteiro.
	absX := int32(float64(evt.X) / float64(fw) * 65535)
	absY := int32(float64(evt.Y) / float64(fh) * 65535)

	if err := injectMouseMove(absX, absY); err != nil {
		c.logInputFailure("mouse move", err)
	}
}

func (c *InputController) handleMouseClick(evt *InputEvent, down bool) {
	switch evt.Button {
	case 0:
		if err := injectMouseLeft(down); err != nil {
			c.logInputFailure(fmt.Sprintf("click esquerdo (down=%t)", down), err)
		}
	case 1:
		// Botão do meio — não implementado ainda
	case 2:
		if err := injectMouseRight(down); err != nil {
			c.logInputFailure(fmt.Sprintf("click direito (down=%t)", down), err)
		}
	}
}

func (c *InputController) handleMouseWheel(evt *InputEvent) {
	delta := int16(evt.DeltaY)
	if delta == 0 {
		delta = int16(evt.DeltaX)
	}
	if err := injectMouseWheel(delta); err != nil {
		c.logInputFailure("mouse wheel", err)
	}
}

// handleKey processa um evento de teclado do viewer (formato v1 e legado).
//
// Auto-repeat: o browser dispara keydown repetido enquanto a tecla está
// pressionada (~30/s após o delay inicial, cadência das configurações de
// teclado do SO do viewer). Os repeats são REPASSADOS ao remoto — cada um
// injeta um SendInput down, produzindo o comportamento esperado de "segurar
// a tecla" (backspace apagando continuamente, espaço/letras repetindo). O
// Windows NÃO auto-repete um SendInput down solitário, então repassar os
// repeats do viewer é o mecanismo de repetição — mesmo padrão de VNC e
// MeshCentral. O mapa keysDown NÃO filtra mais repeats; serve apenas ao
// watchdog de teclas presas e à liberação no Close.
func (c *InputController) handleKey(code, key string, down bool, mods InputModifiers) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	vk := mapBrowserCodeToVK(code, key)
	if vk == 0 {
		return
	}

	keyID := fmt.Sprintf("%s|%s", code, key)
	if keyID == "|" {
		keyID = fmt.Sprintf("vk:%d", vk)
	}

	// Se a própria tecla é um modificador (Ctrl/Alt/Shift/Win), NÃO aplica os
	// mods do evento separadamente — senão injetaria a tecla duas vezes. O
	// estado do modificador é gerenciado pelos PRÓPRIOS eventos da tecla.
	isModifier := vk == VK_CONTROL || vk == VK_MENU || vk == VK_SHIFT || vk == VK_LWIN

	c.mu.Lock()
	c.lastKeyActivity = time.Now()
	if down {
		c.keysDown[keyID] = vk
	} else {
		delete(c.keysDown, keyID)
	}
	c.mu.Unlock()

	if isModifier {
		c.mu.Lock()
		already := c.modsActive[vk]
		if down && !already {
			c.modsActive[vk] = true
		}
		if !down {
			delete(c.modsActive, vk)
		}
		c.mu.Unlock()
		// Auto-repeat do modificador (segurar Shift): o estado "down" já
		// persiste no Windows — repetir a injeção não é necessário.
		if down && already {
			return
		}
	} else if down {
		// Injeta os modificadores indicados pelos flags e ainda não injetados
		// — UMA vez (repeats da tecla não re-injetam, evitando thrash
		// down/up de modificador durante o hold).
		c.syncModifiers(mods, true)
	}

	if down {
		if err := injectKeyDown(vk); err != nil {
			c.logInputFailure(fmt.Sprintf("key down VK=0x%X", vk), err)
		}
	} else {
		if err := injectKeyUp(vk); err != nil {
			c.logInputFailure(fmt.Sprintf("key up VK=0x%X", vk), err)
		}
		if !isModifier {
			// Libera modificadores injetados que os flags do keyup indicam
			// como não mais pressionados fisicamente (os flags refletem o
			// estado real no momento do evento). Modificadores ainda
			// pressionados permanecem até o keyup da própria tecla.
			c.syncModifiers(mods, false)
		}
	}
}

// syncModifiers alinha o estado de modificadores INJETADOS com o estado
// físico reportado pelo viewer (flags do evento). down=true injeta os
// desejados que ainda não estão; down=false libera os injetados que deixaram
// de ser desejados.
func (c *InputController) syncModifiers(mods InputModifiers, down bool) {
	desired := make(map[uint16]bool, 4)
	if mods.Ctrl {
		desired[VK_CONTROL] = true
	}
	if mods.Alt {
		desired[VK_MENU] = true
	}
	if mods.Shift {
		desired[VK_SHIFT] = true
	}
	if mods.Meta {
		desired[VK_LWIN] = true
	}

	c.mu.Lock()
	var toInject, toRelease []uint16
	if down {
		for vk := range desired {
			if !c.modsActive[vk] {
				toInject = append(toInject, vk)
			}
		}
	} else {
		for vk := range c.modsActive {
			if !desired[vk] {
				toRelease = append(toRelease, vk)
			}
		}
	}
	for _, vk := range toInject {
		c.modsActive[vk] = true
	}
	for _, vk := range toRelease {
		delete(c.modsActive, vk)
	}
	c.mu.Unlock()

	for _, vk := range toInject {
		if err := injectKeyDown(vk); err != nil {
			c.logInputFailure(fmt.Sprintf("key down modificador VK=0x%X", vk), err)
		}
	}
	for _, vk := range toRelease {
		if err := injectKeyUp(vk); err != nil {
			c.logInputFailure(fmt.Sprintf("key up modificador VK=0x%X", vk), err)
		}
	}
}

// handleLegacyInput processa formato JSON simples (usado pelo viewer atual).
func (c *InputController) handleLegacyInput(data []byte) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	typ, _ := raw["type"].(string)
	// Loga apenas eventos raros — mousemove (~60/s), keydown/keyup durante
	// hold (auto-repeat ~30/s) e netstats (2s) são silenciosos para não
	// inundar o log com dezenas de milhares de linhas idênticas por minuto.
	// O payload completo também é omitido (só o tipo), para reduzir I/O.
	if typ != "mousemove" && typ != "netstats" && typ != "keydown" && typ != "keyup" {
		log.Printf("[input-controller] legado: type=%s", typ)
	}

	// Métricas de rede do viewer → escada adaptativa de qualidade (modo auto).
	if typ == "netstats" && c.netstatsHandler != nil {
		rttMs, _ := toFloat64(raw["rttMs"])
		recvKbps, _ := toFloat64(raw["recvKbps"])
		recvFrames, _ := toFloat64(raw["recvFrames"])
		c.netstatsHandler(rttMs, recvKbps, int(recvFrames))
		return
	}

	// Rate limit — keyup NUNCA é descartado: um keyup perdido deixa a tecla
	// "presa" (logicamente down) no remoto até o watchdog agir.
	if typ != "keyup" && !c.checkRateLimit(false) {
		return
	}

	// O viewer envia frameWidth/frameHeight no payload. Usa-os para
	// normalização quando presentes (mais preciso que o default interno),
	// evitando que o mouse vá para o lugar errado nos primeiros frames.
	// UpdateFrameDims NÃO sobrescreve capW/capH (cobertura da captura vinda
	// do capturer — difere do frame quando o agente escala a resolução).
	if fw, ok := toFloat64(raw["frameWidth"]); ok && fw > 0 {
		if fh, ok2 := toFloat64(raw["frameHeight"]); ok2 && fh > 0 {
			c.UpdateFrameDims(int(fw), int(fh))
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
	ox, oy := c.frameOriginX, c.frameOriginY
	c.mu.Unlock()

	// Defaults ANTES da primeira métrica (viewer envia frameWidth/Height no
	// payload e o loop de captura publica as dimensões reais a cada frame).
	// NOTA: 1920x1080 é apenas fallback — em telas maiores (ex.: 2560x1440
	// com escala 125%) os primeiros eventos podem deslocar até chegar o
	// primeiro frame; o log de geometria na abertura da sessão ajuda a
	// diagnosticar esse caso.
	if fw <= 0 || fh <= 0 {
		fw, fh = 1920, 1080
	}
	if cw <= 0 || ch <= 0 {
		cw, ch = fw, fh
	}

	// Mapeamento canônico (DPI/multi-monitor correct): frame → desktop virtual
	// físico (origem da captura + escala frame/captura) → 0..65535 relativo ao
	// retângulo do desktop virtual. InjectMouseMove usa MOUSEEVENTF_ABSOLUTE|
	// MOUSEEVENTF_VIRTUALDESK: 0..65535 cobre o desktop virtual INTEIRO, então
	// mapear x/frameW*65535 direto só é correto com captura = desktop inteiro
	// começando em (0,0) (monitor único primário). Com 2 monitores o clique
	// caía no monitor errado; com DPI-unaware as métricas vinham lógicas.
	if vx, vy, vw, vh, ok := virtualDesktopBounds(); ok {
		if absX, absY, ok2 := mapFrameToAbsolute(x, y, fw, fh, cw, ch, ox, oy, vx, vy, vw, vh); ok2 {
			injectAbsoluteMove(absX, absY)
			return
		}
	}

	// Fallback legado: proporcional à captura (denominador capW/capH quando
	// disponível — comportamento anterior preservado).
	absX := clampAbsolute(float64(x) / float64(cw) * 65535)
	absY := clampAbsolute(float64(y) / float64(ch) * 65535)
	injectAbsoluteMove(absX, absY)
}

// injectAbsoluteMove injeta o movimento absoluto com log de falha agregado.
func injectAbsoluteMove(absX, absY int32) {
	if err := injectMouseMove(absX, absY); err != nil {
		log.Printf("[input-controller] InjectMouseMove falhou (absX=%d absY=%d): %v", absX, absY, err)
	}
}

// checkRateLimit controla o budget de eventos/s. bypass=true (key.up) sempre
// passa — descartar um keyup deixaria a tecla presa no remoto.
func (c *InputController) checkRateLimit(bypass bool) bool {
	if bypass {
		return true
	}
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
