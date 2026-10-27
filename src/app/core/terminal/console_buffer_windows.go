//go:build windows

package terminal

// Backend "console-buffer" (Fase 5 — spike, estilo MeshCentral/win-terminal.js).
//
// MOTIVAÇÃO: em máquinas onde o AV/EDR mata TODO filho do ConPTY (0xC0000142),
// o fallback legacy (pipes) funciona mas SEM VT (sem setas/histórico/TAB). O
// MeshCentral resolve isso sem ConPTY: o agente faz AllocConsole, roda o shell
// como filho que COMPARTA o console, lê o SCREEN BUFFER (ReadConsoleOutput) e
// injeta teclas (WriteConsoleInput) — imune à injeção de DLL nos filhos do
// pseudoconsole e com VT reconstruído no viewer (ansi via diff do buffer).
//
// ESCOPO DO SPIKE (opt-in — NUNCA no modo auto):
//   DISCOVERY_TERM_BACKEND=consolebuffer
//   - Texto + cores 16 (attrs → SGR), diff por linha, cursor posicionado;
//   - Teclado: setas/Home/End/Del/PgUp/PgDn/Esc/Tab/Backspace/Enter,
//     Ctrl+letra, chars via VkKeyScanW (fallback UnicodeChar puro p/ acentos);
//   - Resize real (SetConsoleScreenBufferSize + SetConsoleWindowInfo);
//   - 1 shell por processo (console é recurso único) — liberado no Close.
//
// NÃO implementado no spike: WinEvent hooks (poll de 33 ms), TUI de meia tela
// parcial (repaint full quando o retângulo muda), modo SYSTEM.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	cbModKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	cbModUser32   = windows.NewLazySystemDLL("user32.dll")

	cbProcAllocConsole               = cbModKernel32.NewProc("AllocConsole")
	cbProcFreeConsole                = cbModKernel32.NewProc("FreeConsole")
	cbProcGetConsoleWindow           = cbModKernel32.NewProc("GetConsoleWindow")
	cbProcReadConsoleOutputW         = cbModKernel32.NewProc("ReadConsoleOutputW")
	cbProcWriteConsoleInputW         = cbModKernel32.NewProc("WriteConsoleInputW")
	cbProcGetConsoleScreenBufferInfo = cbModKernel32.NewProc("GetConsoleScreenBufferInfo")
	cbProcSetConsoleScreenBufferSize = cbModKernel32.NewProc("SetConsoleScreenBufferSize")
	cbProcSetConsoleWindowInfo       = cbModKernel32.NewProc("SetConsoleWindowInfo")
	cbProcShowWindow                 = cbModUser32.NewProc("ShowWindow")
	cbProcVkKeyScanW                 = cbModUser32.NewProc("VkKeyScanW")
)

const (
	cbKeyEvent = 0x0001

	cbShiftPressed     = 0x0010
	cbLeftCtrlPressed  = 0x0008
	cbRightCtrlPressed = 0x0004
	cbLeftAltPressed   = 0x0002

	cbVKBack   = 0x08
	cbVKTab    = 0x09
	cbVKReturn = 0x0D
	cbVKEsc    = 0x1B
	cbVKPrior  = 0x21
	cbVKNext   = 0x22
	cbVKEnd    = 0x23
	cbVKHome   = 0x24
	cbVKLeft   = 0x25
	cbVKUp     = 0x26
	cbVKRight  = 0x27
	cbVKDown   = 0x28
	cbVKInsert = 0x2D
	cbVKDelete = 0x2E
)

type cbCoord struct{ X, Y int16 }

type cbSmallRect struct{ Left, Top, Right, Bottom int16 }

type cbCharInfo struct {
	UnicodeChar uint16
	Attributes  uint16
}

type cbScreenBufferInfo struct {
	DwSize           cbCoord
	DwCursorPosition cbCoord
	WAttributes      uint16
	SrWindow         cbSmallRect
	DwMaximum        cbCoord
}

type cbKeyEventRecord struct {
	BKeyDown          int32
	WRepeatCount      uint16
	WVirtualKeyCode   uint16
	WVirtualScanCode  uint16
	UnicodeChar       uint16
	DwControlKeyState uint32
}

type cbInputRecord struct {
	EventType uint16
	_         uint16
	KeyEvent  cbKeyEventRecord
}

// cbCell é a célula do frame reconstruído.
type cbCell struct {
	ch   rune
	attr uint16
}

// consoleBufferActive garante UM shell console-buffer por processo (o console é
// recurso único do processo — AllocConsole falha se já houver console anexado).
var consoleBufferActive atomic.Bool

// ConsoleBufferShell implementa IShell via screen buffer (spike Fase 5).
type ConsoleBufferShell struct {
	mu          sync.Mutex
	kind        ShellKind
	cols, rows  int
	weAllocated bool
	coninFile   *os.File
	conoutFile  *os.File
	conin       windows.Handle
	conout      windows.Handle

	cmd      *exec.Cmd
	exited   atomic.Bool
	exitCode atomic.Int32
	done     chan struct{}

	stopCh   chan struct{}
	stopOnce sync.Once

	outMu sync.Mutex

	prev     []cbCell
	prevRect cbSmallRect
	forceAll atomic.Bool

	inputQueue chan []cbInputRecord
	inputDrops atomic.Int64
	dropLogged atomic.Bool
}

// NewConsoleBufferShell cria o shell console-buffer (sem run-as custom: o
// worker já roda com o token do usuário na sessão interativa).
func NewConsoleBufferShell(shell ShellKind, cols, rows int, onOutput func(string)) (IShell, error) {
	return NewConsoleBufferShellRunAs(shell, cols, rows, onOutput, RunAsInherit)
}

// NewConsoleBufferShellRunAs é a variante run-as; SYSTEM não é suportado no
// spike (o console precisa nascer na sessão interativa) — degrada p/ herdar.
func NewConsoleBufferShellRunAs(shell ShellKind, cols, rows int, onOutput func(string), runAs RunAs) (IShell, error) {
	if runAs == RunAsSystem {
		log.Printf("[terminal] console-buffer: runAs=system não suportado no spike — herdando identidade do processo")
	}
	if consoleBufferActive.CompareAndSwap(false, true) {
		// ok: somos o único
	} else {
		return nil, fmt.Errorf("console-buffer: já existe um shell console-buffer neste processo")
	}

	s := &ConsoleBufferShell{
		kind:       shell,
		cols:       clampCb(cols, 20, 300),
		rows:       clampCb(rows, 8, 120),
		done:       make(chan struct{}),
		stopCh:     make(chan struct{}),
		inputQueue: make(chan []cbInputRecord, 256),
	}

	// Falha de inicialização → libera o flag do singleton.
	release := func() { consoleBufferActive.Store(false) }

	if err := s.attachConsole(); err != nil {
		release()
		return nil, err
	}
	if err := s.openConsoleHandles(); err != nil {
		s.detachConsole()
		release()
		return nil, err
	}
	if err := s.spawnShell(); err != nil {
		s.closeHandles()
		s.detachConsole()
		release()
		return nil, err
	}

	// Monitor de exit do processo.
	go func() {
		err := s.cmd.Wait()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			log.Printf("[terminal] console-buffer wait: %v", err)
		}
		s.exitCode.Store(int32(code))
		s.exited.Store(true)
		close(s.done)
	}()

	go s.inputLoop()
	go s.readLoop(onOutput)

	log.Printf("[terminal] console-buffer shell criado: kind=%s cols=%d rows=%d pid=%d", s.kind, s.cols, s.rows, s.cmd.Process.Pid)
	return s, nil
}

func clampCb(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// attachConsole anexa o processo do worker a um console real (AllocConsole),
// escondendo a janela. Reusa console existente (worker morno reusado).
func (s *ConsoleBufferShell) attachConsole() error {
	hw, _, _ := cbProcGetConsoleWindow.Call()
	if hw != 0 {
		return nil // já temos console
	}
	if r1, _, err := cbProcAllocConsole.Call(); r1 == 0 {
		return fmt.Errorf("AllocConsole: %v", err)
	}
	s.weAllocated = true
	if hw2, _, _ := cbProcGetConsoleWindow.Call(); hw2 != 0 {
		// SW_HIDE = 0
		_, _, _ = cbProcShowWindow.Call(hw2, 0)
	}
	return nil
}

func (s *ConsoleBufferShell) detachConsole() {
	if s.weAllocated {
		_, _, _ = cbProcFreeConsole.Call()
		s.weAllocated = false
	}
}

func (s *ConsoleBufferShell) openConsoleHandles() error {
	conin, err := windows.CreateFile(
		windows.StringToUTF16Ptr("CONIN$"),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return fmt.Errorf("CONIN$: %w", err)
	}
	conout, err := windows.CreateFile(
		windows.StringToUTF16Ptr("CONOUT$"),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		_ = windows.CloseHandle(conin)
		return fmt.Errorf("CONOUT$: %w", err)
	}
	s.conin, s.conout = conin, conout
	// os.File wrappers: fechá-los fecha os handles — nunca fechar duas vezes.
	s.coninFile = os.NewFile(uintptr(conin), "CONIN$")
	s.conoutFile = os.NewFile(uintptr(conout), "CONOUT$")
	return nil
}

func (s *ConsoleBufferShell) closeHandles() {
	if s.coninFile != nil {
		_ = s.coninFile.Close()
		s.coninFile = nil
		s.conin = 0
	}
	if s.conoutFile != nil {
		_ = s.conoutFile.Close()
		s.conoutFile = nil
		s.conout = 0
	}
}

// spawnShell roda o shell como filho que COMPARTILHA o console deste processo
// (sem CREATE_NEW_CONSOLE; HideWindow esconde a janela do console herdada).
func (s *ConsoleBufferShell) spawnShell() error {
	_, exePath := ResolveShell(s.kind)
	if strings.TrimSpace(exePath) == "" {
		return fmt.Errorf("shell %s não resolvido", s.kind)
	}
	cmd := exec.Command(exePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}
	// Diretório inicial (pedido 2026-10-27): o worker/console-buffer roda como
	// o usuário → perfil do usuário (tok=0 + processo não-SYSTEM resolve pelo
	// token do próprio processo).
	if dir := resolveShellStartDir(0); dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = s.coninFile
	cmd.Stdout = s.conoutFile
	cmd.Stderr = s.conoutFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn %s: %w", exePath, err)
	}
	s.cmd = cmd
	return nil
}

// readLoop publica diffs do screen buffer (~30 fps) via onOutput.
func (s *ConsoleBufferShell) readLoop(onOutput func(string)) {
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
		}
		cells, rect, cursor, ok := s.readFrame()
		if !ok {
			if s.exited.Load() {
				return
			}
			continue
		}
		if diff := renderFrameDiff(s.prev, s.prevRect, cells, rect, s.forceAll.Swap(false)); diff != "" {
			s.emit(onOutput, diff)
		}
		s.prev = cells
		s.prevRect = rect
		// Cursor do console → posiciona o cursor do viewer.
		s.emit(onOutput, fmt.Sprintf("\x1b[%d;%dH", cursor.Y+1, cursor.X+1))
		if s.exited.Load() {
			return
		}
	}
}

func (s *ConsoleBufferShell) emit(onOutput func(string), data string) {
	s.outMu.Lock()
	onOutput(data)
	s.outMu.Unlock()
}

// readFrame lê a janela visível do console. ok=false em erro transitório.
func (s *ConsoleBufferShell) readFrame() ([]cbCell, cbSmallRect, cbCoord, bool) {
	var info cbScreenBufferInfo
	if r1, _, _ := cbProcGetConsoleScreenBufferInfo.Call(uintptr(s.conout), uintptr(unsafe.Pointer(&info))); r1 == 0 {
		return nil, cbSmallRect{}, cbCoord{}, false
	}
	rect := info.SrWindow
	w := int(rect.Right - rect.Left + 1)
	h := int(rect.Bottom - rect.Top + 1)
	if w <= 0 || h <= 0 || w > 500 || h > 200 {
		return nil, cbSmallRect{}, cbCoord{}, false
	}
	buf := make([]cbCharInfo, w*h)
	bufSize := packCbCoord(w, h)
	bufCoord := packCbCoord(0, 0)
	r1, _, _ := cbProcReadConsoleOutputW.Call(
		uintptr(s.conout),
		uintptr(unsafe.Pointer(&buf[0])),
		bufSize,
		bufCoord,
		uintptr(unsafe.Pointer(&rect)),
	)
	if r1 == 0 {
		return nil, cbSmallRect{}, cbCoord{}, false
	}
	w = int(rect.Right - rect.Left + 1)
	h = int(rect.Bottom - rect.Top + 1)
	if w <= 0 || h <= 0 {
		return nil, cbSmallRect{}, cbCoord{}, false
	}
	cells := make([]cbCell, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ci := buf[y*w+x]
			ch := rune(ci.UnicodeChar)
			if ch == 0 {
				ch = ' '
			}
			cells = append(cells, cbCell{ch: ch, attr: ci.Attributes})
		}
	}
	return cells, rect, info.DwCursorPosition, true
}

func packCbCoord(x, y int) uintptr {
	return uintptr(uint32(uint16(x)) | uint32(uint16(y))<<16)
}

// renderFrameDiff devolve o ANSI (CUP + runs SGR) das linhas alteradas.
// full=true (startup, resize, scroll do retângulo) renderiza a tela inteira.
func renderFrameDiff(prev []cbCell, prevRect cbSmallRect, cur []cbCell, rect cbSmallRect, full bool) string {
	w := int(rect.Right - rect.Left + 1)
	h := int(rect.Bottom - rect.Top + 1)
	if w <= 0 || h <= 0 || len(cur) != w*h {
		return ""
	}
	changedRows := make([]bool, h)
	if full || len(prev) != len(cur) || prevRect != rect {
		for i := range changedRows {
			changedRows[i] = true
		}
	} else {
		for y := 0; y < h; y++ {
			row := cur[y*w : (y+1)*w]
			prevRow := prev[y*w : (y+1)*w]
			for x := 0; x < w; x++ {
				if row[x] != prevRow[x] {
					changedRows[y] = true
					break
				}
			}
		}
	}
	var b strings.Builder
	for y := 0; y < h; y++ {
		if !changedRows[y] {
			continue
		}
		renderFrameRow(&b, cur[y*w:(y+1)*w], rect.Left, y)
	}
	return b.String()
}

// cbCellInvisible reporta células "vazias": espaço (ou NUL — célula nunca
// escrita) com atributo default do console (0x0007 cinza/preto) ou attr 0
// (preto/preto). Ambas são invisíveis e cortáveis no trim do frame.
func cbCellInvisible(c cbCell) bool {
	return (c.ch == ' ' || c.ch == 0) && (c.attr == 0x0007 || c.attr == 0)
}

// renderFrameRow emite CUP + runs de atributo para UMA linha, cortando células
// finais invisíveis para reduzir bytes.
func renderFrameRow(b *strings.Builder, row []cbCell, rectLeft int16, y int) {
	last := len(row)
	for last > 0 && cbCellInvisible(row[last-1]) {
		last--
	}
	if last == 0 {
		return
	}
	// Primeira coluna visível para pular leading blanks.
	first := 0
	for first < last && cbCellInvisible(row[first]) {
		first++
	}
	b.WriteString(fmt.Sprintf("\x1b[%d;%dH\x1b[0m", y+1, int(rectLeft)+first+1))
	curAttr := uint16(0xFFFF)
	for x := first; x < last; x++ {
		c := row[x]
		if c.attr != curAttr {
			b.WriteString(attrToSGR(c.attr))
			curAttr = c.attr
		}
		if c.ch == 0 {
			b.WriteRune(' ')
		} else {
			b.WriteRune(c.ch)
		}
	}
}

// attrToSGR converte o atributo de CHAR_INFO (fg/bg DOS de 4 bits) em SGR ANSI.
func attrToSGR(attr uint16) string {
	fg := attr & 0x0F
	bg := (attr >> 4) & 0x0F
	var parts []string
	if fg&0x8 != 0 {
		parts = append(parts, itoa(90+int(dosToAnsi(fg&0x7))))
	} else {
		parts = append(parts, itoa(30+int(dosToAnsi(fg&0x7))))
	}
	if bg&0x8 != 0 {
		parts = append(parts, itoa(100+int(dosToAnsi(bg&0x7))))
	} else {
		parts = append(parts, itoa(40+int(dosToAnsi(bg&0x7))))
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

// dosToAnsi mapeia a paleta DOS (IRGB) para os códigos ANSI 0-7.
// ANSI:    0 preto, 1 vermelho, 2 verde, 3 amarelo, 4 azul, 5 magenta, 6 ciano, 7 branco.
// DOS:     0 preto, 1 azul,     2 verde, 3 ciano,   4 vermelho, 5 magenta, 6 amarelo, 7 branco.
// (A ordem de azul/vermelho/ciano/amarelo TROCA entre as paletas.)
var dosToAnsiTable = [8]uint16{0, 4, 2, 6, 1, 5, 3, 7}

func dosToAnsi(v uint16) uint16 {
	return dosToAnsiTable[v&0x7]
}

func itoa(v int) string {
	return fmt.Sprintf("%d", v)
}

// WriteStdin traduz texto/VT do viewer em KEY_EVENTs do console.
func (s *ConsoleBufferShell) WriteStdin(data string) error {
	recs, drops := translateKeysToRecords(data)
	if drops > 0 {
		s.inputDrops.Add(int64(drops))
	}
	if len(recs) == 0 {
		return nil
	}
	select {
	case <-s.done:
		return fmt.Errorf("console-buffer: shell encerrado")
	default:
	}
	select {
	case s.inputQueue <- recs:
		return nil
	default:
		s.inputDrops.Add(int64(1))
		return nil
	}
}

// inputLoop escreve os eventos na fila de input do console (com backpressure:
// WriteConsoleInputW bloqueia se a fila do conhost encher).
func (s *ConsoleBufferShell) inputLoop() {
	for {
		select {
		case <-s.done:
			return
		case <-s.stopCh:
			return
		case evs := <-s.inputQueue:
			if len(evs) == 0 {
				continue
			}
			written := uint32(0)
			for written < uint32(len(evs)) {
				var n uint32
				r1, _, _ := cbProcWriteConsoleInputW.Call(
					uintptr(s.conin),
					uintptr(unsafe.Pointer(&evs[written])),
					uintptr(len(evs)-int(written)),
					uintptr(unsafe.Pointer(&n)),
				)
				if r1 == 0 || n == 0 {
					s.inputDrops.Add(int64(len(evs) - int(written)))
					break
				}
				written += n
			}
			if s.inputDrops.Load() > 0 && !s.dropLogged.Swap(true) {
				log.Printf("[terminal] console-buffer: input descartado (fila cheia/erro): %d eventos", s.inputDrops.Load())
			}
		}
	}
}

// keyEvent monta o par down+up de um evento de tecla.
func keyEvent(vk, char uint16, ctrlState uint32) []cbInputRecord {
	down := cbInputRecord{EventType: cbKeyEvent, KeyEvent: cbKeyEventRecord{
		BKeyDown:          1,
		WRepeatCount:      1,
		WVirtualKeyCode:   vk,
		UnicodeChar:       char,
		DwControlKeyState: ctrlState,
	}}
	up := down
	up.KeyEvent.BKeyDown = 0
	return []cbInputRecord{down, up}
}

// translateKeysToRecords converte a entrada do viewer em INPUT_RECORDs.
// Retorna também quantos runes/sequências não traduzíveis foram descartados.
func translateKeysToRecords(data string) ([]cbInputRecord, int) {
	var recs []cbInputRecord
	drops := 0
	runes := []rune(data)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\r' || r == '\n':
			recs = append(recs, keyEvent(cbVKReturn, uint16(utf16.Encode([]rune{'\r'})[0]), 0)...)
		case r == '\t':
			recs = append(recs, keyEvent(cbVKTab, '\t', 0)...)
		case r == 0x7F || r == 0x08:
			recs = append(recs, keyEvent(cbVKBack, '\b', 0)...)
		case r == 0x1B:
			// Sequência VT: consome os próximos runes.
			if vk, ok := matchVTSequence(runes, &i); ok {
				recs = append(recs, keyEvent(vk, 0, 0)...)
			} else {
				recs = append(recs, keyEvent(cbVKEsc, 0x1B, 0)...)
			}
		case r >= 0x01 && r <= 0x1A && r != '\t' && r != '\r' && r != '\n' && r != 0x08:
			// Ctrl+letra.
			letter := r + 'A' - 1
			recs = append(recs, keyEvent(uint16(letter), uint16(letter), cbLeftCtrlPressed)...)
		case r == ' ':
			recs = append(recs, keyEvent(' ', ' ', 0)...)
		case r >= 0x20 && r < 0x7F:
			recs = append(recs, keyEventForPrintable(r)...)
		case r >= 0x7F:
			// Não-ASCII (acentos): UnicodeChar direto (conhost entrega o char).
			u := utf16.Encode([]rune{r})[0]
			recs = append(recs, keyEvent(0, u, 0)...)
		default:
			drops++
		}
	}
	return recs, drops
}

// keyEventForPrintable resolve vk/shift via VkKeyScanW (layout atual).
// ATENÇÃO (revisão 2026-10-27): VkKeyScanW devolve SHORT em AX — o uintptr
// vindo de Call tem os bits altos indefinidos em x64; comparar com
// 0xFFFFFFFFFFFFFFFF pode passar errado e gerar VK=255. Só AX é confiável:
// lê int16(r1 & 0xFFFF) e testa -1.
func keyEventForPrintable(r rune) []cbInputRecord {
	u := utf16.Encode([]rune{r})[0]
	var vk uint16
	var ctrl uint32
	r1, _, _ := cbProcVkKeyScanW.Call(uintptr(u))
	scan := int16(uint32(r1) & 0xFFFF)
	if scan != -1 {
		vk = uint16(scan & 0xFF)
		shift := uint16(scan>>8) & 0xFF
		if shift&1 != 0 {
			ctrl |= cbShiftPressed
		}
		if shift&2 != 0 {
			ctrl |= cbLeftCtrlPressed
		}
		if shift&4 != 0 {
			ctrl |= cbLeftAltPressed
		}
	}
	return keyEvent(vk, u, ctrl)
}

// matchVTSequence casa as sequências VT do xterm suportadas no spike e avança
// o índice de consumo. Suportado: setas (\x1b[A e \x1bOA), Home/End, Ins/Del,
// PgUp/PgDn, F1-F4 simples. Retorna ok=false para \x1b solto (ESC puro).
func matchVTSequence(runes []rune, i *int) (uint16, bool) {
	if *i+1 >= len(runes) || runes[*i+1] != '[' && runes[*i+1] != 'O' {
		return 0, false
	}
	start := *i + 1
	j := start + 1
	for j < len(runes) && runes[j] >= 0x20 && runes[j] <= 0x7E {
		j++
	}
	if j > len(runes) {
		return 0, false
	}
	seq := string(runes[start+1 : j])
	*i = j - 1
	switch seq {
	case "A":
		return cbVKUp, true
	case "B":
		return cbVKDown, true
	case "C":
		return cbVKRight, true
	case "D":
		return cbVKLeft, true
	case "H", "1~":
		return cbVKHome, true
	case "F", "4~":
		return cbVKEnd, true
	case "2~":
		return cbVKInsert, true
	case "3~":
		return cbVKDelete, true
	case "5~":
		return cbVKPrior, true
	case "6~":
		return cbVKNext, true
	}
	return 0, false
}

// Resize aplica as dimensões no console real e força repaint full.
func (s *ConsoleBufferShell) Resize(cols, rows int) error {
	s.mu.Lock()
	s.cols = clampCb(cols, 20, 300)
	s.rows = clampCb(rows, 8, 120)
	cols, rows = s.cols, s.rows
	conout := s.conout
	s.mu.Unlock()
	if conout == 0 {
		return fmt.Errorf("console-buffer: sem console")
	}
	if r1, _, err := cbProcSetConsoleScreenBufferSize.Call(uintptr(conout), packCbCoord(cols, rows)); r1 == 0 {
		// Buffer mínimo do console pode ser maior que o pedido (fonte grande):
		// segue com repaint — a janela visível é o que o viewer espelha.
		log.Printf("[terminal] console-buffer: SetConsoleScreenBufferSize falhou: %v", err)
	}
	rect := cbSmallRect{Left: 0, Top: 0, Right: int16(cols - 1), Bottom: int16(rows - 1)}
	if r1, _, err := cbProcSetConsoleWindowInfo.Call(uintptr(conout), 1, uintptr(unsafe.Pointer(&rect))); r1 == 0 {
		log.Printf("[terminal] console-buffer: SetConsoleWindowInfo falhou: %v", err)
	}
	s.forceAll.Store(true)
	return nil
}

// Close encerra o processo do shell, fecha handles e libera o console.
func (s *ConsoleBufferShell) Close() error {
	s.stopOnce.Do(func() { close(s.stopCh) })
	if s.cmd != nil && s.cmd.Process != nil && !s.exited.Load() {
		_ = s.cmd.Process.Kill()
	}
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
	s.closeHandles()
	s.detachConsole()
	consoleBufferActive.Store(false)
	return nil
}

func (s *ConsoleBufferShell) Wait() error {
	<-s.done
	if code := s.exitCode.Load(); code != 0 {
		return fmt.Errorf("exit status %d", code)
	}
	return nil
}

func (s *ConsoleBufferShell) ShellKind() ShellKind { return s.kind }

func (s *ConsoleBufferShell) Alive() bool {
	return !s.exited.Load()
}

// ExitCodeNow devolve o exit code corrente (contrato de diagnóstico da escada).
func (s *ConsoleBufferShell) ExitCodeNow() (int, bool) {
	if !s.exited.Load() {
		return 0, false
	}
	return int(s.exitCode.Load()), true
}
