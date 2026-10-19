//go:build windows

package winui

import (
	"fmt"
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Janela Win32 (classe, loop de mensagens e pintura sem cintilação) ──

var (
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procUpdateWindow     = user32.NewProc("UpdateWindow")
	procInvalidateRect   = user32.NewProc("InvalidateRect")
	procSetTimer         = user32.NewProc("SetTimer")
	procKillTimer        = user32.NewProc("KillTimer")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procSetFocus         = user32.NewProc("SetFocus")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procEndPaint         = user32.NewProc("EndPaint")
	procSetWindowRgn     = user32.NewProc("SetWindowRgn")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
	procSetCursor        = user32.NewProc("SetCursor")
	procTrackMouseEvent  = user32.NewProc("TrackMouseEvent")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procSetCapture       = user32.NewProc("SetCapture")
	procReleaseCapture   = user32.NewProc("ReleaseCapture")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procSetLayeredAttrs  = user32.NewProc("SetLayeredWindowAttributes")
	procGetDpiForWindow  = user32.NewProc("GetDpiForWindow")
	procScreenToClient   = user32.NewProc("ScreenToClient")

	procCreateRoundRectRgn = gdi32.NewProc("CreateRoundRectRgn")
)

// Mensagens e estilos do Win32 usados pelas janelas do agente.
const (
	MsgDestroy       = 0x0002
	MsgClose         = 0x0010
	MsgPaint         = 0x000F
	MsgEraseBkgnd    = 0x0014
	MsgSetFocus      = 0x0007
	MsgKillFocus     = 0x0008
	MsgSettingChange = 0x001A
	MsgSetCursor     = 0x0020
	MsgNCHitTest     = 0x0084
	MsgMouseActivate = 0x0021
	MsgTimer         = 0x0113
	MsgKeyDown       = 0x0100
	// MsgPrintClient permite renderizar a janela em um DC (impressão,
	// acessibilidade e testes de layout/pré-visualização).
	MsgPrintClient = 0x0318
	MsgMouseLeave  = 0x02A3
	MsgMouseMove   = 0x0200
	MsgLButtonDown = 0x0201
	MsgLButtonUp   = 0x0202

	// WinStylePopup/WinStyleClipSiblings compõem a janela do cartão flutuante.
	WinStylePopup        = 0x80000000
	WinStyleVisible      = 0x10000000
	WinStyleClipSiblings = 0x04000000
	// ExStyleTopmost mantém o cartão acima das demais janelas.
	ExStyleTopmost = 0x00000008
	// ExStyleToolWindow evita ícone na barra de tarefas.
	ExStyleToolWindow = 0x00000080
	// ExStyleNoActivate mantém o foco do usuário (banner não deve roubar foco).
	ExStyleNoActivate = 0x08000000
	// ExStyleLayered/ExStyleTransparent tornam a janela "click-through": o
	// mouse atravessa para a janela de baixo (usado no banner, que é apenas
	// informativo — nunca pode engolir cliques do app em uso).
	ExStyleLayered     = 0x00080000
	ExStyleTransparent = 0x00000020
	// ClassStyleDropShadow desenha a sombra do DWM ao redor da janela.
	ClassStyleDropShadow = 0x00020000
	// SwShowNormal mostra a janela no estado normal.
	SwShowNormal = 5

	// HtTransparent é o HTTRANSPARENT do WM_NCHITTEST: o clique atravessa a
	// janela e atinge o app de baixo.
	HtTransparent = ^uintptr(0)

	// VkReturn/VkEscape/VkTab/VkSpace/VkLeft/VkRight são teclas virtuais.
	VkReturn = 0x0D
	VkEscape = 0x1B
	VkTab    = 0x09
	VkSpace  = 0x20
	VkLeft   = 0x25
	VkRight  = 0x27

	// cursors de sistema.
	cursorArrow = 32512
	cursorHand  = 32649
	// trackMouseLeave é o TME_LEAVE do TrackMouseEvent.
	trackMouseLeave = 0x00000002
)

type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}

type trackMouseEvent struct {
	Size      uint32
	Flags     uint32
	Track     uintptr
	HoverTime uint32
}

// PaintStruct espelha PAINTSTRUCT do Win32.
type PaintStruct struct {
	Hdc       uintptr
	Erase     int32
	PaintRect nativeRect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

// Msg espelha MSG (somente os campos usados pelo loop de mensagens).
type Msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// RegisterWindowClass registra a classe da janela. wndProc deve vir de
// windows.NewCallback e a classe é registrada uma vez por processo.
func RegisterWindowClass(className string, wndProc uintptr, style uint32) error {
	name, err := windows.UTF16PtrFromString(className)
	if err != nil {
		return err
	}
	cursor, _, _ := procLoadCursorW.Call(0, cursorArrow)
	class := wndClassExW{
		Size:      uint32(unsafe.Sizeof(wndClassExW{})),
		Style:     style,
		WndProc:   wndProc,
		Cursor:    cursor,
		ClassName: name,
	}
	ret, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if ret == 0 {
		// ERROR_CLASS_ALREADY_EXISTS (1410): outra instância/repetição no mesmo
		// processo — não é erro.
		if errno, ok := callErr.(windows.Errno); ok && errno == 1410 {
			return nil
		}
		return fmt.Errorf("windui: RegisterClassExW(%s) falhou: %v", className, callErr)
	}
	return nil
}

// CreateWindow cria (sem mostrar) a janela no tamanho/posição informados.
func CreateWindow(className, title string, exStyle, style uint32, x, y, width, height int) (uintptr, error) {
	class, err := windows.UTF16PtrFromString(className)
	if err != nil {
		return 0, err
	}
	windowName, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0, err
	}
	hwnd, _, callErr := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(class)),
		uintptr(unsafe.Pointer(windowName)),
		uintptr(style),
		uintptr(x), uintptr(y), uintptr(width), uintptr(height),
		0, 0, 0, 0,
	)
	if hwnd == 0 {
		return 0, fmt.Errorf("windui: CreateWindowExW(%s) falhou: %v", className, callErr)
	}
	return hwnd, nil
}

// DefWindowProc delega a mensagem não tratada ao procedimento padrão.
func DefWindowProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return ret
}

// ShowWindow mostra a janela.
func ShowWindow(hwnd uintptr, command int) {
	procShowWindow.Call(hwnd, uintptr(command))
}

// UpdateWindow força a pintura imediata.
func UpdateWindow(hwnd uintptr) {
	procUpdateWindow.Call(hwnd)
}

// DestroyWindow destrói a janela.
func DestroyWindow(hwnd uintptr) {
	procDestroyWindow.Call(hwnd)
}

// Invalidate marca a janela inteira para repintura.
func Invalidate(hwnd uintptr) {
	if hwnd != 0 {
		procInvalidateRect.Call(hwnd, 0, 0)
	}
}

// SetTimer agenda o timer (id) a cada milliseconds.
func SetTimer(hwnd, id uintptr, milliseconds uint32) bool {
	ret, _, _ := procSetTimer.Call(hwnd, id, uintptr(milliseconds), 0)
	return ret != 0
}

// KillTimer cancela o timer (id).
func KillTimer(hwnd, id uintptr) {
	procKillTimer.Call(hwnd, id)
}

// SetLayeredAlpha aplica transparência uniforme à janela layered (0-255).
func SetLayeredAlpha(hwnd uintptr, alpha byte) {
	const lwaAlpha = 0x00000002
	procSetLayeredAttrs.Call(hwnd, 0, uintptr(alpha), lwaAlpha)
}

// ScaleForDPI converte DPI em fator de escala de layout (96 = 1.0).
// Clampa em [1, 3]: não encolhe controles abaixo do projeto e evita tamanhos
// absurdos em configurações de acessibilidade extremas. Mesma política usada
// pelo banner e pelo prompt de consentimento.
func ScaleForDPI(dpi int) float64 {
	if dpi <= 0 {
		return 1
	}
	scale := float64(dpi) / 96.0
	if scale < 1 {
		return 1
	}
	if scale > 3 {
		return 3
	}
	return scale
}

// ScalePx aplica o fator de escala a uma medida de projeto (96 DPI).
func ScalePx(value int, scale float64) int {
	if scale <= 0 || scale == 1 {
		return value
	}
	return int(math.Round(float64(value) * scale))
}

// DpiForWindow devolve o DPI efetivo do monitor da janela (96 = 100%).
// GetDpiForWindow existe desde o Windows 10 1607; em sistemas sem a API o
// fallback é 96 (comportamento antigo, sem escala).
func DpiForWindow(hwnd uintptr) int {
	if hwnd == 0 {
		return 96
	}
	if err := procGetDpiForWindow.Find(); err != nil {
		return 96
	}
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	if dpi < 48 || dpi > 480 {
		return 96
	}
	return int(dpi)
}

// MoveResizeWindow reposiciona e redimensiona a janela sem ativá-la nem mudar
// a ordem Z (usado pelo banner depois de descobrir o DPI do monitor).
func MoveResizeWindow(hwnd uintptr, x, y, width, height int) {
	if hwnd == 0 {
		return
	}
	const (
		swpNoZOrder   = 0x0004
		swpNoActivate = 0x0010
	)
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y),
		uintptr(width), uintptr(height), swpNoZOrder|swpNoActivate)
}

// EnsureTopMost recoloca a janela no topo da ordem Z sem alterar posição/tamanho.
func EnsureTopMost(hwnd uintptr) {
	const (
		hwndTopMost   = ^uintptr(0) // HWND_TOPMOST (-1)
		swpNoSize     = 0x0001
		swpNoMove     = 0x0002
		swpNoActivate = 0x0010
	)
	procSetWindowPos.Call(hwnd, hwndTopMost, 0, 0, 0, 0, swpNoSize|swpNoMove|swpNoActivate)
}

// SetForegroundWindow traz a janela para frente.
func SetForegroundWindow(hwnd uintptr) {
	procSetForeground.Call(hwnd)
}

// SetFocusTo coloca o foco de teclado na janela.
func SetFocusTo(hwnd uintptr) {
	procSetFocus.Call(hwnd)
}

// BeginPaint/EndPaint delimitam a pintura de WM_PAINT.
func BeginPaint(hwnd uintptr) (uintptr, PaintStruct) {
	var paint PaintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
	return hdc, paint
}

// EndPaint conclui a pintura de WM_PAINT.
func EndPaint(hwnd uintptr, paint *PaintStruct) {
	procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(paint)))
}

// SetRoundedRegion recorta a janela em um retângulo arredondado.
//
// A região usa width/height exatos (right/bottom são exclusivos no GDI): o
// "+1" antigo deslocava os centros dos arcos do canto inferior/direito em 1px.
// O cartão do banner desenha a borda 1px para dentro com raio radius-1 e
// centros (radius, radius) e (width-radius, height-radius); sem o ajuste, a
// borda ficava 2px afastada do recorte nesses cantos (e sobrava uma faixa sem
// pintura no consentimento em fallback).
func SetRoundedRegion(hwnd uintptr, width, height, radius int) {
	if hwnd == 0 {
		return
	}
	region, _, _ := procCreateRoundRectRgn.Call(
		0, 0, uintptr(width), uintptr(height),
		uintptr(radius*2), uintptr(radius*2),
	)
	if region == 0 {
		return
	}
	// SetWindowRgn assume a posse da região (não liberar aqui).
	procSetWindowRgn.Call(hwnd, region, 1)
}

// TrackMouseLeave passa a avisar WM_MOUSELEAVE na próxima saída do mouse.
func TrackMouseLeave(hwnd uintptr) {
	event := trackMouseEvent{
		Size:  uint32(unsafe.Sizeof(trackMouseEvent{})),
		Flags: trackMouseLeave,
		Track: hwnd,
	}
	procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&event)))
}

// PostMessage enfileira uma mensagem para a janela (seguro de outras threads).
func PostMessage(hwnd uintptr, message uint32, wparam, lparam uintptr) {
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, uintptr(message), wparam, lparam)
	}
}

// SetCapture prende o mouse à janela (arrastar/clicar).
func SetCapture(hwnd uintptr) {
	procSetCapture.Call(hwnd)
}

// ReleaseCapture libera a captura do mouse.
func ReleaseCapture() {
	procReleaseCapture.Call()
}

// SetCursorHand troca o cursor para a "mãozinha" (área clicável).
func SetCursorHand() {
	hand, _, _ := procLoadCursorW.Call(0, cursorHand)
	procSetCursor.Call(hand)
}

// SetCursorArrow restaura o cursor padrão.
func SetCursorArrow() {
	arrow, _, _ := procLoadCursorW.Call(0, cursorArrow)
	procSetCursor.Call(arrow)
}

// screenPoint espelha POINT.
type screenPoint struct {
	X int32
	Y int32
}

// ScreenToClient converte coordenadas de tela em coordenadas de cliente da
// janela (usado no WM_NCHITTEST, cujo LPARAM vem em coordenadas de tela).
func ScreenToClient(hwnd uintptr, x, y int) (int, int) {
	pt := screenPoint{X: int32(x), Y: int32(y)}
	procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
	return int(pt.X), int(pt.Y)
}

// NextMessage lê a próxima mensagem da fila. Retorna <= 0 para encerrar o loop.
func NextMessage(msg *Msg) int32 {
	ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(msg)), 0, 0, 0)
	return int32(ret)
}

// TranslateMessage/DispatchMessage processo padrão de mensagens.
func TranslateMessage(msg *Msg) {
	procTranslateMessage.Call(uintptr(unsafe.Pointer(msg)))
}

// DispatchMessage entrega a mensagem ao wndproc da janela.
func DispatchMessage(msg *Msg) {
	procDispatchMessageW.Call(uintptr(unsafe.Pointer(msg)))
}

// PostQuitMessage encerra o loop de mensagens da thread.
func PostQuitMessage(code int) {
	procPostQuitMessage.Call(uintptr(code))
}

// BufferedPaint devolve um DC de memória do tamanho da janela (double buffer).
// Retorna zeros se o DC/bitmap não puderem ser criados.
func BufferedPaint(screenDC uintptr, width, height int) (memDC, bitmap, oldBitmap uintptr) {
	if screenDC == 0 || width <= 0 || height <= 0 {
		return 0, 0, 0
	}
	memDC, _, _ = procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return 0, 0, 0
	}
	bitmap, _, _ = procCreateCompatibleBitmap.Call(screenDC, uintptr(width), uintptr(height))
	if bitmap == 0 {
		procDeleteDC.Call(memDC)
		return 0, 0, 0
	}
	oldBitmap, _, _ = procSelectObject.Call(memDC, bitmap)
	return memDC, bitmap, oldBitmap
}

// FlushBufferedPaint copia o DC de memória para a tela e libera os recursos.
func FlushBufferedPaint(screenDC, memDC, bitmap, oldBitmap uintptr, width, height int) {
	if memDC == 0 {
		return
	}
	procBitBlt.Call(screenDC, 0, 0, uintptr(width), uintptr(height), memDC, 0, 0, srcCopy)
	procSelectObject.Call(memDC, oldBitmap)
	procDeleteObject.Call(bitmap)
	procDeleteDC.Call(memDC)
}
