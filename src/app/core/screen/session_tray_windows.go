//go:build windows

package screen

import (
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Ícone da bandeja da sessão remota ──
//
// É o canal de controle LOCAL do usuário acessado: mostra que há sessão em
// andamento (operador + tempo) e oferece "Desconectar sessão remota" no menu de
// contexto — SEM cobrir nenhum app.
//
// Antes essa ação ficava no banner, que é topmost e excluído da captura: o
// operador remoto não via o banner e tinha cliques engolidos (abas do
// navegador). O banner virou apenas informativo e click-through; o controle
// migrou para cá.
//
// O mesmo ícone é reutilizado pelo input lock (KVM) da sessão: quando o viewer
// bloqueia teclado/mouse o tooltip/balloon mudam, sem criar um segundo ícone.

const (
	sessionTrayClassName = "DiscoveryRmmSessionTray"
	trayCallbackMessage  = 0x8000 + 2 // WM_APP + 2
	trayRefreshMessage   = 0x8000 + 3 // WM_APP + 3
	trayPromoteMessage   = 0x8000 + 4 // WM_APP + 4 (recriar ícone após IsPromoted)
	trayTimerID          = 7
	trayRefreshMS        = 1000

	trayMenuInfo       = 1
	trayMenuDisconnect = 2

	// Mensagens/estilos usados pelo ícone e pelo menu.
	trayWMDestroy          = 0x0002
	trayWMClose            = 0x0010
	trayWMCommand          = 0x0111
	trayWMTimer            = 0x0113
	trayWMRButtonUp        = 0x0205
	trayWMLButtonUp        = 0x0202
	trayWMContextMenu      = 0x007B
	trayNIFMessage         = 0x00000001
	trayNIFIcon            = 0x00000002
	trayNIFTip             = 0x00000004
	trayNIFInfo            = 0x00000010
	trayNIMAdd             = 0x00000000
	trayNIMModify          = 0x00000001
	trayNIMDelete          = 0x00000002
	trayNIIFInfo           = 0x00000001
	trayMFString           = 0x00000000
	trayMFGrayed           = 0x00000001
	trayMFSeparator        = 0x00000800
	trayTPMReturnCmd       = 0x0100
	trayTPMRightButton     = 0x0002
	trayTPMNoNotify        = 0x0080
	trayBalloonTitle       = "Acesso remoto Discovery"
	trayDisconnectLabel    = "Desconectar sessão remota"
	trayIconTipMaxRunes    = 100
	trayMenuHeaderMaxRunes = 80
	// trayMenuReopenGuardMS evita que um duplo-clique reabra o menu duas vezes
	// (o primeiro clique abre o menu modal e o segundo WM_LBUTTONUP chega depois).
	trayMenuReopenGuardMS = 300
	trayBalloonOnStartup  = "Sessão remota iniciada"
)

var (
	procCreatePopupMenu       = user32DLL.NewProc("CreatePopupMenu")
	procAppendMenuW           = user32DLL.NewProc("AppendMenuW")
	procDestroyMenu           = user32DLL.NewProc("DestroyMenu")
	procTrackPopupMenu        = user32DLL.NewProc("TrackPopupMenu")
	procSetForegroundWindow   = user32DLL.NewProc("SetForegroundWindow")
	procRegisterWindowMessage = user32DLL.NewProc("RegisterWindowMessageW")
	procPostMessageW          = user32DLL.NewProc("PostMessageW")
	procSetTimer              = user32DLL.NewProc("SetTimer")
	procPostQuitMessage       = user32DLL.NewProc("PostQuitMessage")
)

type trayPoint struct {
	X int32
	Y int32
}

type trayMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// SessionTrayOptions descreve o ícone a criar.
type SessionTrayOptions struct {
	Operator        string
	Text            string
	AllowDisconnect bool
	OnDisconnect    func()
}

// SessionTray é o ícone da bandeja da sessão remota (janela oculta + thread).
type SessionTray struct {
	mu sync.Mutex

	hwnd uintptr
	icon uintptr

	start           time.Time
	operator        string
	text            string
	allowDisconnect bool
	onDisconnect    func()

	// override é a mensagem do input lock ("" = estado normal da sessão).
	override string
	balloon  string

	added          bool
	closed         bool
	lastMenuClosed time.Time

	taskbarCreatedMessage uint32

	ready  chan error
	doneCh chan struct{}
}

var (
	sessionTrayClassOnce sync.Once
	sessionTrayClassErr  error
)

func ensureSessionTrayClass() error {
	sessionTrayClassOnce.Do(func() {
		sessionTrayClassErr = registerSessionTrayClass()
	})
	return sessionTrayClassErr
}

func registerSessionTrayClass() error {
	className, err := windows.UTF16PtrFromString(sessionTrayClassName)
	if err != nil {
		return err
	}
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   windows.NewCallback(sessionTrayWndProc),
		lpszClassName: className,
	}
	ret, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if ret == 0 && errnoOf(callErr) != 1410 {
		return fmt.Errorf("RegisterClassExW(tray) falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
	}
	return nil
}

// NewSessionTray cria e exibe o ícone da sessão. Retorna erro quando a janela
// oculta/ícone não podem ser criados.
func NewSessionTray(opts SessionTrayOptions) (*SessionTray, error) {
	if err := ensureSessionTrayClass(); err != nil {
		return nil, err
	}

	tray := &SessionTray{
		start:           time.Now(),
		operator:        strings.TrimSpace(opts.Operator),
		text:            strings.TrimSpace(opts.Text),
		allowDisconnect: opts.AllowDisconnect,
		onDisconnect:    opts.OnDisconnect,
		ready:           make(chan error, 1),
		doneCh:          make(chan struct{}),
	}

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(tray.doneCh)

		className, _ := windows.UTF16PtrFromString(sessionTrayClassName)
		windowName, _ := windows.UTF16PtrFromString("Discovery RMM — sessão remota (bandeja)")
		hwnd, _, callErr := procCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(windowName)),
			0, // janela oculta (só recebe mensagens do ícone)
			0, 0, 0, 0,
			0, 0, 0, 0,
		)
		if hwnd == 0 {
			tray.ready <- fmt.Errorf("CreateWindowExW(tray) falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
			return
		}
		icon, _, _ := procLoadIconW.Call(0, idiInfoIcon)
		taskbarCreated, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(mustUTF16Pointer("TaskbarCreated"))))

		tray.mu.Lock()
		tray.hwnd = hwnd
		tray.icon = icon
		tray.taskbarCreatedMessage = uint32(taskbarCreated)
		tray.mu.Unlock()

		if err := tray.addIcon(); err != nil {
			procDestroyWindow.Call(hwnd)
			tray.ready <- err
			return
		}
		tray.setBalloon(trayBalloonOnStartup + trayOperatorSuffix(tray.snapshotOperator()))
		procSetTimer.Call(hwnd, trayTimerID, trayRefreshMS, 0)
		// Windows 11 esconde ícones novos no overflow; promove este ícone para
		// a área visível (best-effort, em segundo plano).
		go tray.promoteSessionTrayIcon()
		tray.ready <- nil

		var msg trayMsg
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()

	if err := <-tray.ready; err != nil {
		return nil, err
	}
	return tray, nil
}

// mustUTF16Pointer devolve o ponteiro UTF-16 de s (para APIs que o consomem
// apenas durante a chamada).
func mustUTF16Pointer(value string) *uint16 {
	ptr, _ := windows.UTF16PtrFromString(value)
	return ptr
}

func sessionTrayWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	raw, ok := sessionTrayStates.Load(hwnd)
	if !ok {
		ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return ret
	}
	tray, _ := raw.(*SessionTray)

	switch msg {
	case trayCallbackMessage:
		event := int32(lparam & 0xFFFF)
		switch event {
		case trayWMRButtonUp, trayWMContextMenu, trayWMLButtonUp:
			tray.showMenu()
		}
		return 0
	case trayRefreshMessage:
		tray.refreshTip()
		if balloon := tray.takeBalloon(); balloon != "" {
			tray.notify(balloon)
		}
		return 0
	case trayPromoteMessage:
		// O shell lê IsPromoted no NIM_ADD: recria o ícone depois de gravar a
		// preferência para ele sair do overflow na hora.
		tray.reapplyIcon()
		return 0
	case trayWMTimer:
		tray.refreshTip()
		return 0
	case trayWMCommand:
		if int32(wparam&0xFFFF) == trayMenuDisconnect {
			tray.disconnect()
		}
		return 0
	case trayWMClose:
		if err := tray.removeIcon(); err != nil {
			log.Printf("[session-tray] falha ao remover ícone da bandeja: %v", err)
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case trayWMDestroy:
		sessionTrayStates.Delete(hwnd)
		procPostQuitMessage.Call(0)
		return 0
	}

	// Recriação do ícone após reinício do explorer.exe.
	if tray.taskbarCreatedMessage != 0 && msg == uintptr(tray.taskbarCreatedMessage) {
		tray.mu.Lock()
		tray.added = false
		tray.mu.Unlock()
		_ = tray.addIcon()
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return ret
}

var sessionTrayStates sync.Map // hwnd -> *SessionTray

// Show exibe/atualiza o estado de bloqueio (input lock) no ícone. Mantém o
// ícone visível — diferente do TrayIndicator antigo, aqui a sessão continua.
func (t *SessionTray) Show(message string) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.override = strings.TrimSpace(message)
	t.balloon = t.override
	hwnd := t.hwnd
	t.mu.Unlock()
	procPostMessageW.Call(hwnd, trayRefreshMessage, 0, 0)
	return nil
}

// Hide volta o ícone ao estado normal da sessão (elenco/tempo).
func (t *SessionTray) Hide() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.override = ""
	hwnd := t.hwnd
	t.mu.Unlock()
	procPostMessageW.Call(hwnd, trayRefreshMessage, 0, 0)
	return nil
}

// RequestClose pede a remoção do ícone/encerramento da thread sem bloquear
// (idempotente). É o método usado pelo manager de sessões: Close() espera até
// 2s e o closeSessionLocked roda com o mutex do manager preso.
func (t *SessionTray) RequestClose() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, trayWMClose, 0, 0)
	}
}

// Close remove o ícone e encerra a thread, esperando até 2s. Idempotente.
func (t *SessionTray) Close() {
	if t == nil {
		return
	}
	t.RequestClose()
	if t.doneCh == nil {
		// Tray montado manualmente (sem thread de janela): nada a aguardar.
		return
	}
	select {
	case <-t.doneCh:
	case <-time.After(2 * time.Second):
	}
}

// ── internos (thread da bandeja) ──

func (t *SessionTray) snapshotOperator() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.operator
}

func trayOperatorSuffix(operator string) string {
	if operator == "" {
		return ""
	}
	return " por " + operator
}

func (t *SessionTray) addIcon() error {
	t.mu.Lock()
	hwnd, icon := t.hwnd, t.icon
	t.mu.Unlock()
	nid := notifyIconDataW{
		cbSize:           uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:             hwnd,
		uID:              trayIconID,
		uFlags:           trayNIFMessage | trayNIFIcon | trayNIFTip,
		uCallbackMessage: trayCallbackMessage,
		hIcon:            icon,
	}
	copyUTF16(nid.szTip[:], t.tooltipText())
	ret, _, callErr := procShellNotifyIconW.Call(trayNIMAdd, uintptr(unsafe.Pointer(&nid)))
	if ret == 0 {
		return fmt.Errorf("Shell_NotifyIconW(ADD) falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
	}
	sessionTrayStates.Store(hwnd, t)
	t.mu.Lock()
	t.added = true
	t.mu.Unlock()
	return nil
}

func (t *SessionTray) removeIcon() error {
	t.mu.Lock()
	if !t.added {
		t.mu.Unlock()
		return nil
	}
	t.added = false
	hwnd := t.hwnd
	t.mu.Unlock()
	nid := notifyIconDataW{
		cbSize: uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:   hwnd,
		uID:    trayIconID,
	}
	ret, _, callErr := procShellNotifyIconW.Call(trayNIMDelete, uintptr(unsafe.Pointer(&nid)))
	if ret == 0 {
		return fmt.Errorf("Shell_NotifyIconW(DELETE) falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
	}
	return nil
}

func (t *SessionTray) refreshTip() {
	t.mu.Lock()
	if t.closed || !t.added {
		t.mu.Unlock()
		return
	}
	hwnd := t.hwnd
	tip := t.tooltipTextLocked()
	t.mu.Unlock()

	nid := notifyIconDataW{
		cbSize: uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:   hwnd,
		uID:    trayIconID,
		uFlags: trayNIFTip,
	}
	copyUTF16(nid.szTip[:], tip)
	procShellNotifyIconW.Call(trayNIMModify, uintptr(unsafe.Pointer(&nid)))
}

// tooltipText devolve o tooltip atual (com mutex próprio) — usado na criação.
func (t *SessionTray) tooltipText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tooltipTextLocked()
}

// tooltipTextLocked: chamador precisa segurar t.mu.
func (t *SessionTray) tooltipTextLocked() string {
	if t.override != "" {
		return truncateRunes(t.override, trayIconTipMaxRunes)
	}
	clock := trayElapsedClock(time.Since(t.start))
	if t.operator != "" {
		return truncateRunes("Sessão remota — "+t.operator+" · "+clock, trayIconTipMaxRunes)
	}
	return truncateRunes("Sessão remota — "+clock, trayIconTipMaxRunes)
}

// menuHeaderText descreve a sessão no topo do menu de contexto.
func (t *SessionTray) menuHeaderText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.override != "" {
		return t.override
	}
	clock := trayElapsedClock(time.Since(t.start))
	if t.text != "" {
		return truncateRunes(t.text+" · "+clock, trayMenuHeaderMaxRunes)
	}
	if t.operator != "" {
		return "Sessão remota: " + t.operator + " · " + clock
	}
	return "Sessão remota · " + clock
}

// setBalloon agenda um balloon informativo (mostrado no próximo refresh).
func (t *SessionTray) setBalloon(message string) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.balloon = strings.TrimSpace(message)
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, trayRefreshMessage, 0, 0)
	}
}

// takeBalloon consome o balloon pendente.
func (t *SessionTray) takeBalloon() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	message := t.balloon
	t.balloon = ""
	return message
}

// notify mostra um balloon na área de notificação.
func (t *SessionTray) notify(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	t.mu.Lock()
	hwnd, added := t.hwnd, t.added
	t.mu.Unlock()
	if !added {
		return
	}
	nid := notifyIconDataW{
		cbSize:      uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:        hwnd,
		uID:         trayIconID,
		uFlags:      trayNIFInfo,
		dwInfoFlags: trayNIIFInfo,
	}
	copyUTF16(nid.szInfoTitle[:], trayBalloonTitle)
	copyUTF16(nid.szInfo[:], message)
	procShellNotifyIconW.Call(trayNIMModify, uintptr(unsafe.Pointer(&nid)))
}

// showMenu abre o menu de contexto (Desconectar) na posição do cursor.
func (t *SessionTray) showMenu() {
	t.mu.Lock()
	hwnd := t.hwnd
	allow := t.allowDisconnect
	// Duplo-clique: o primeiro clique abre o menu (modal) e o segundo
	// WM_LBUTTONUP é entregue depois — sem o guard o menu reabriria sozinho.
	recent := !t.lastMenuClosed.IsZero() && time.Since(t.lastMenuClosed) < trayMenuReopenGuardMS*time.Millisecond
	t.mu.Unlock()
	if hwnd == 0 || recent {
		return
	}

	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	appendTrayMenu(menu, trayMFString|trayMFGrayed, trayMenuInfo, t.menuHeaderText())
	appendTrayMenu(menu, trayMFSeparator, 0, "")
	disconnectFlags := uintptr(trayMFString)
	if !allow {
		disconnectFlags |= trayMFGrayed
	}
	appendTrayMenu(menu, disconnectFlags, trayMenuDisconnect, trayDisconnectLabel)

	var pt trayPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// SetForegroundWindow é necessário para o menu fechar ao clicar fora.
	procSetForegroundWindow.Call(hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(
		menu,
		trayTPMReturnCmd|trayTPMRightButton|trayTPMNoNotify,
		uintptr(pt.X), uintptr(pt.Y),
		0, hwnd, 0,
	)
	// Workaround clássico: fecha o menu se o usuário clicar fora.
	procPostMessageW.Call(hwnd, 0, 0, 0)
	t.mu.Lock()
	t.lastMenuClosed = time.Now()
	t.mu.Unlock()
	if int32(cmd) == trayMenuDisconnect {
		t.disconnect()
	}
}

func appendTrayMenu(menu, flags, itemID uintptr, label string) {
	var text uintptr
	if label != "" {
		ptr, _ := windows.UTF16PtrFromString(label)
		text = uintptr(unsafe.Pointer(ptr))
	}
	procAppendMenuW.Call(menu, flags, itemID, text)
}

// disconnect chama o callback de encerramento (se permitido pela política).
func (t *SessionTray) disconnect() {
	t.mu.Lock()
	allow, callback := t.allowDisconnect, t.onDisconnect
	t.mu.Unlock()
	if !allow || callback == nil {
		return
	}
	callback()
}

// trayElapsedClock formata o tempo de sessão como mm:ss.
func trayElapsedClock(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	minutes := int(elapsed.Minutes())
	seconds := int(elapsed.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

// truncateRunes limita o texto ao orçamento do tooltip do shell.
func truncateRunes(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}
