//go:build wailsui

package app

// ── Lado Wails do contrato de UI (tag "wailsui") ────────────────────────────
//
// Este arquivo só existe no binário da UI (discovery-agent.exe), construído
// com a build tag "wailsui". Ele contém:
//
//   - os métodos do App com assinatura do Wails (ServiceStartup com
//     application.ServiceOptions e os setters que recebem concretos do Wails);
//   - os ADAPTERS que convertem os concretos do Wails (application.App/Window/
//     Menu/MenuItem/SystemTray/Dialog..., NotificationService) para os
//     contratos locais de ui_contracts.go — toda a lógica do pacote roda sobre
//     os contratos locais, em qualquer build.
//
// O serviço (cmd/discovery-service) não compila este arquivo: por isso
// "go list -deps ./cmd/discovery-service" não contém pacote wails nenhum.

import (
	"context"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	wailsnotifications "github.com/wailsapp/wails/v3/pkg/services/notifications"
)

func init() {
	// Sonda da main thread (main_thread_watchdog.go): com o Wails presente,
	// usa o despacho real para a main thread.
	uiInvokeSync = application.InvokeSync
}

// ── Ciclo de vida do serviço Wails ──────────────────────────────────────────

// ServiceStartup é chamado pelo Wails v3 durante a inicialização da aplicação.
// Substitui o OnStartup do v2. O ctx recebido é o contexto da aplicação.
func (a *App) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	if err := a.RunCore(ctx); err != nil {
		return err
	}
	// Serviço nativo de notificações (Wails v3), usado só pelo chat. O
	// lifecycle é conduzido aqui — e não registrando o serviço em
	// application.Options.Services — para não expor a API de notificações ao
	// webview e para que uma falha de registro do toast não aborte o startup.
	a.startNativeNotificationService(ctx)

	// Retoma o chat de suporte quando existe sessão ativa (agent reiniciado no
	// meio de uma conversa). Roda em goroutine com atraso curto para que a
	// aplicação já consiga servir a nova janela.
	go a.resumeSupportChatIfActive(3 * time.Second)
	// Reconciliação periódica (só na UI: é ela que abre janelas).
	go a.runSupportChatReconcileLoop()

	// Autoproteção: a main thread presa no WebView2 (travamento de 20/10) não
	// tem timeout no lado nativo — a UI passa a se encerrar sozinha quando a
	// thread não responde, e o ui-watchdog do serviço relança em segundos.
	a.startMainThreadWatchdog(ctx)

	return nil
}

// ── Setters com concretos do Wails ──────────────────────────────────────────
//
// Adaptam os concretos do Wails para os contratos locais e delegam aos setters
// do App (untagged, com tipos locais). Mantêm os nomes usados pelo main.go.

// SetApplicationWails adapta *application.App e guarda o contrato local.
//
//wails:ignore
func (a *App) SetApplicationWails(app *application.App) {
	a.SetApplication(wailsAppAsUI(app))
}

// SetMainWindowWails adapta application.Window e guarda o contrato local.
//
//wails:ignore
func (a *App) SetMainWindowWails(window application.Window) {
	a.SetMainWindow(wailsWindowAsUI(window))
}

// SetWindowFactoryWails registra a fábrica da janela principal escrita contra
// o Wails (main.go): cada janela criada é adaptada para uiWindow.
//
//wails:ignore
func (a *App) SetWindowFactoryWails(factory func() application.Window) {
	a.SetWindowFactory(func() uiWindow {
		if factory == nil {
			return nil
		}
		return wailsWindowAsUI(factory())
	})
}

// SetNativeNotificationService adapta o serviço nativo do Wails v3 para o
// contrato local uiNotifications e registra o dispatcher de cliques.
//
//wails:ignore
func (a *App) SetNativeNotificationService(ns *wailsnotifications.NotificationService) {
	if a == nil {
		return
	}
	if ns == nil {
		a.SetNativeNotifications(nil)
		return
	}
	a.SetNativeNotifications(wailsNotificationsAsUI(ns))
}

// ── ADAPTERS: application.Window → uiWindow ─────────────────────────────────

func wailsWindowAsUI(w application.Window) uiWindow {
	if w == nil {
		return nil
	}
	return uiWindowWails{w: w}
}

type uiWindowWails struct {
	w application.Window
}

func (a uiWindowWails) Close()                      { a.w.Close() }
func (a uiWindowWails) ExecJS(js string)            { a.w.ExecJS(js) }
func (a uiWindowWails) Focus()                      { a.w.Focus() }
func (a uiWindowWails) Hide()                       { a.w.Hide() }
func (a uiWindowWails) IsFocused() bool             { return a.w.IsFocused() }
func (a uiWindowWails) IsMinimised() bool           { return a.w.IsMinimised() }
func (a uiWindowWails) IsVisible() bool             { return a.w.IsVisible() }
func (a uiWindowWails) Minimise()                   { a.w.Minimise() }
func (a uiWindowWails) Position() (int, int)        { return a.w.Position() }
func (a uiWindowWails) Reload()                     { a.w.Reload() }
func (a uiWindowWails) SetAlwaysOnTop(b bool)       { a.w.SetAlwaysOnTop(b) }
func (a uiWindowWails) SetFrameless(frameless bool) { a.w.SetFrameless(frameless) }
func (a uiWindowWails) SetPosition(x, y int)        { a.w.SetPosition(x, y) }
func (a uiWindowWails) SetSize(width, height int)   { a.w.SetSize(width, height) }
func (a uiWindowWails) SetURL(s string)             { a.w.SetURL(s) }
func (a uiWindowWails) Show()                       { a.w.Show() }
func (a uiWindowWails) Size() (int, int)            { return a.w.Size() }
func (a uiWindowWails) UnMinimise()                 { a.w.UnMinimise() }

func (a uiWindowWails) GetScreen() (*uiScreen, error) {
	screen, err := a.w.GetScreen()
	if err != nil {
		return nil, err
	}
	if screen == nil {
		return nil, nil
	}
	return screenToUI(screen), nil
}

func (a uiWindowWails) NativeWindow() uintptr {
	return uintptr(a.w.NativeWindow())
}

func (a uiWindowWails) RegisterHook(eventType uiWindowEventType, callback func(event uiWindowEvent)) func() {
	return a.w.RegisterHook(events.WindowEventType(eventType), func(ev *application.WindowEvent) {
		if callback != nil {
			callback(uiWindowEventWails{ev: ev})
		}
	})
}

type uiWindowEventWails struct {
	ev *application.WindowEvent
}

func (e uiWindowEventWails) Cancel() { e.ev.Cancel() }

// ── ADAPTERS: application.App → uiApp ───────────────────────────────────────

func wailsAppAsUI(a *application.App) uiApp {
	if a == nil {
		return nil
	}
	return uiAppWails{a: a}
}

type uiAppWails struct {
	a *application.App
}

func (w uiAppWails) AppName() string                    { return w.a.Config().Name }
func (w uiAppWails) EmitEvent(name string, data ...any) { w.a.Event.Emit(name, data...) }
func (w uiAppWails) Quit()                              { w.a.Quit() }

func (w uiAppWails) Dialog() uiDialogManager {
	return uiDialogManagerWails{d: w.a.Dialog}
}

func (w uiAppWails) Menu() uiMenuManager {
	return uiMenuManagerWails{m: w.a.Menu}
}

func (w uiAppWails) SystemTray() uiSystemTrayManager {
	return uiSystemTrayManagerWails{t: w.a.SystemTray}
}

func (w uiAppWails) Screens() []*uiScreen {
	if w.a.Screen == nil {
		return nil
	}
	return screensToUI(w.a.Screen.GetAll())
}

func (w uiAppWails) NewWebviewWindow(options uiWebviewWindowOptions) uiWindow {
	return wailsWindowAsUI(w.a.Window.NewWithOptions(webviewWindowOptionsFromUI(options)))
}

// ── ADAPTERS: DialogManager ─────────────────────────────────────────────────

type uiDialogManagerWails struct {
	d *application.DialogManager
}

func (w uiDialogManagerWails) OpenFile() uiOpenFileDialog {
	return uiOpenFileDialogWails{d: w.d.OpenFile()}
}

func (w uiDialogManagerWails) SaveFile() uiSaveFileDialog {
	return uiSaveFileDialogWails{d: w.d.SaveFile()}
}

type uiOpenFileDialogWails struct {
	d *application.OpenFileDialogStruct
}

func (w uiOpenFileDialogWails) PromptForSingleSelection() (string, error) {
	return w.d.PromptForSingleSelection()
}

type uiSaveFileDialogWails struct {
	d *application.SaveFileDialogStruct
}

func (w uiSaveFileDialogWails) SetOptions(options *uiSaveFileDialogOptions) {
	w.d.SetOptions(saveFileDialogOptionsFromUI(options))
}

func (w uiSaveFileDialogWails) PromptForSingleSelection() (string, error) {
	return w.d.PromptForSingleSelection()
}

func saveFileDialogOptionsFromUI(o *uiSaveFileDialogOptions) *application.SaveFileDialogOptions {
	if o == nil {
		return nil
	}
	filters := make([]application.FileFilter, len(o.Filters))
	for i, f := range o.Filters {
		filters[i] = application.FileFilter{DisplayName: f.DisplayName, Pattern: f.Pattern}
	}
	return &application.SaveFileDialogOptions{
		CanCreateDirectories:            o.CanCreateDirectories,
		ShowHiddenFiles:                 o.ShowHiddenFiles,
		CanSelectHiddenExtension:        o.CanSelectHiddenExtension,
		AllowOtherFileTypes:             o.AllowOtherFileTypes,
		HideExtension:                   o.HideExtension,
		TreatsFilePackagesAsDirectories: o.TreatsFilePackagesAsDirectories,
		Title:                           o.Title,
		Message:                         o.Message,
		Directory:                       o.Directory,
		Filename:                        o.Filename,
		ButtonText:                      o.ButtonText,
		Filters:                         filters,
	}
}

// ── ADAPTERS: Menu / MenuItem ───────────────────────────────────────────────

type uiMenuManagerWails struct {
	m *application.MenuManager
}

func (w uiMenuManagerWails) New() uiMenu {
	return uiMenuWails{m: w.m.New()}
}

type uiMenuWails struct {
	m *application.Menu
}

func (w uiMenuWails) Add(label string) uiMenuItem {
	return uiMenuItemWails{m: w.m.Add(label)}
}

func (w uiMenuWails) AddSubmenu(label string) uiMenu {
	return uiMenuWails{m: w.m.AddSubmenu(label)}
}

func (w uiMenuWails) AddCheckbox(label string, checked bool) uiMenuItem {
	return uiMenuItemWails{m: w.m.AddCheckbox(label, checked)}
}

func (w uiMenuWails) AddSeparator() { w.m.AddSeparator() }

type uiMenuItemWails struct {
	m *application.MenuItem
}

func (w uiMenuItemWails) SetEnabled(enabled bool) { w.m.SetEnabled(enabled) }
func (w uiMenuItemWails) SetLabel(s string)       { w.m.SetLabel(s) }
func (w uiMenuItemWails) SetChecked(checked bool) { w.m.SetChecked(checked) }

func (w uiMenuItemWails) OnClick(fn func(ctx *uiContext)) uiMenuItem {
	return uiMenuItemWails{m: w.m.OnClick(func(_ *application.Context) {
		if fn != nil {
			fn(&uiContext{})
		}
	})}
}

// menuFromUI devolve o menu concreto do Wails subjacente ao adapter (o único
// produtor de uiMenu é uiMenuManagerWails; nil em caso inesperado).
func menuFromUI(m uiMenu) *application.Menu {
	if mw, ok := m.(uiMenuWails); ok {
		return mw.m
	}
	return nil
}

// ── ADAPTERS: SystemTray ────────────────────────────────────────────────────

type uiSystemTrayManagerWails struct {
	t *application.SystemTrayManager
}

func (w uiSystemTrayManagerWails) New() uiSystemTray {
	return uiSystemTrayWails{t: w.t.New()}
}

type uiSystemTrayWails struct {
	t *application.SystemTray
}

func (w uiSystemTrayWails) SetIcon(icon []byte)       { w.t.SetIcon(icon) }
func (w uiSystemTrayWails) SetTooltip(tooltip string) { w.t.SetTooltip(tooltip) }
func (w uiSystemTrayWails) OnClick(fn func())         { w.t.OnClick(fn) }
func (w uiSystemTrayWails) OnDoubleClick(fn func())   { w.t.OnDoubleClick(fn) }

func (w uiSystemTrayWails) SetMenu(menu uiMenu) {
	w.t.SetMenu(menuFromUI(menu))
}

// ── ADAPTERS: dados de tela ─────────────────────────────────────────────────

func rectToUI(r application.Rect) uiRect {
	return uiRect{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height}
}

func rectFromUI(r uiRect) application.Rect {
	return application.Rect{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height}
}

func screenToUI(s *application.Screen) *uiScreen {
	if s == nil {
		return nil
	}
	return &uiScreen{
		ID:             s.ID,
		Name:           s.Name,
		ScaleFactor:    s.ScaleFactor,
		Bounds:         rectToUI(s.Bounds),
		PhysicalBounds: rectToUI(s.PhysicalBounds),
		WorkArea:       rectToUI(s.WorkArea),
		IsPrimary:      s.IsPrimary,
	}
}

func screenFromUI(s *uiScreen) *application.Screen {
	if s == nil {
		return nil
	}
	return &application.Screen{
		ID:             s.ID,
		Name:           s.Name,
		ScaleFactor:    s.ScaleFactor,
		Bounds:         rectFromUI(s.Bounds),
		PhysicalBounds: rectFromUI(s.PhysicalBounds),
		WorkArea:       rectFromUI(s.WorkArea),
		IsPrimary:      s.IsPrimary,
	}
}

func screensToUI(screens []*application.Screen) []*uiScreen {
	if screens == nil {
		return nil
	}
	out := make([]*uiScreen, len(screens))
	for i, s := range screens {
		out[i] = screenToUI(s)
	}
	return out
}

func webviewWindowOptionsFromUI(o uiWebviewWindowOptions) application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Name:                  o.Name,
		Title:                 o.Title,
		Width:                 o.Width,
		Height:                o.Height,
		AlwaysOnTop:           o.AlwaysOnTop,
		URL:                   o.URL,
		DisableResize:         o.DisableResize,
		Frameless:             o.Frameless,
		Hidden:                o.Hidden,
		MinWidth:              o.MinWidth,
		MinHeight:             o.MinHeight,
		MaxWidth:              o.MaxWidth,
		MaxHeight:             o.MaxHeight,
		InitialPosition:       application.WindowStartPosition(o.InitialPosition),
		X:                     o.X,
		Y:                     o.Y,
		Screen:                screenFromUI(o.Screen),
		MinimiseButtonState:   application.ButtonState(o.MinimiseButtonState),
		MaximiseButtonState:   application.ButtonState(o.MaximiseButtonState),
		CloseButtonState:      application.ButtonState(o.CloseButtonState),
		FullscreenButtonState: application.ButtonState(o.FullscreenButtonState),
	}
}

// ── ADAPTERS: NotificationService ───────────────────────────────────────────

func wailsNotificationsAsUI(ns *wailsnotifications.NotificationService) uiNotifications {
	if ns == nil {
		return nil
	}
	return uiNotificationsWails{ns: ns}
}

type uiNotificationsWails struct {
	ns *wailsnotifications.NotificationService
}

func (w uiNotificationsWails) SendNotification(options uiNotificationOptions) error {
	return w.ns.SendNotification(notificationOptionsFromUI(options))
}

func (w uiNotificationsWails) OnNotificationResponse(callback func(result uiNotificationResult)) {
	w.ns.OnNotificationResponse(func(result wailsnotifications.NotificationResult) {
		if callback != nil {
			callback(notificationResultToUI(result))
		}
	})
}

func (w uiNotificationsWails) ServiceStartup(ctx context.Context) error {
	// O ServiceOptions usado é o mesmo que o Wails passaria
	// (DefaultServiceOptions, vazio) — ver chat_notifications.go.
	return w.ns.ServiceStartup(ctx, application.ServiceOptions{})
}

func (w uiNotificationsWails) ServiceShutdown() error {
	return w.ns.ServiceShutdown()
}

func notificationOptionsFromUI(o uiNotificationOptions) wailsnotifications.NotificationOptions {
	attachments := make([]wailsnotifications.NotificationAttachment, len(o.Attachments))
	for i, a := range o.Attachments {
		attachments[i] = wailsnotifications.NotificationAttachment{ID: a.ID, Path: a.Path, Type: a.Type}
	}
	var sound *wailsnotifications.NotificationSound
	if o.Sound != nil {
		sound = &wailsnotifications.NotificationSound{Silent: o.Sound.Silent, Name: o.Sound.Name}
	}
	var schedule *wailsnotifications.NotificationSchedule
	if o.Schedule != nil {
		schedule = &wailsnotifications.NotificationSchedule{DelaySeconds: o.Schedule.DelaySeconds, At: o.Schedule.At}
	}
	return wailsnotifications.NotificationOptions{
		ID:                o.ID,
		Title:             o.Title,
		Subtitle:          o.Subtitle,
		Body:              o.Body,
		CategoryID:        o.CategoryID,
		Data:              o.Data,
		Sound:             sound,
		Attachments:       attachments,
		ThreadID:          o.ThreadID,
		InterruptionLevel: o.InterruptionLevel,
		Schedule:          schedule,
	}
}

func notificationResultToUI(r wailsnotifications.NotificationResult) uiNotificationResult {
	resp := r.Response
	return uiNotificationResult{
		Response: uiNotificationResponse{
			ID:               resp.ID,
			ActionIdentifier: resp.ActionIdentifier,
			CategoryID:       resp.CategoryID,
			Title:            resp.Title,
			Subtitle:         resp.Subtitle,
			Body:             resp.Body,
			UserText:         resp.UserText,
			UserInfo:         resp.UserInfo,
		},
		Error: r.Error,
	}
}
