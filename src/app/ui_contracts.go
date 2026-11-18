package app

// ── Contratos de UI independentes do Wails (F14) ────────────────────────────
//
// Este arquivo NÃO importa github.com/wailsapp/wails: ele define as interfaces
// e structs locais que espelham o subconjunto da API do Wails v3 usado pelo
// pacote app. Com isso o serviço (cmd/discovery-service) compila sem puxar
// nenhuma dependência transitiva do Wails, enquanto o binário da UI
// (discovery-agent.exe, construído com a build tag "wailsui") injeta, via
// app_ui_wails.go, adapters que delegam para os concretos do Wails.
//
// Regras:
//   - Nenhum símbolo daqui pode referenciar tipos do Wails;
//   - Métodos que no Wails devolvem o próprio tipo (ex.: Hide() Window) são
//     declarados sem retorno — o adapter descarta o valor;
//   - Métodos que devolvem/recebem structs de dados do Wails (Screen, Rect,
//     Point, NotificationOptions...) usam os espelhos locais (uiScreen,
//     uiNotificationOptions...), convertidos no adapter.

import "context"

// ── Tipos de dados espelhados ───────────────────────────────────────────────

// uiRect espelha application.Rect (campos usados pelo pacote).
type uiRect struct {
	X      int
	Y      int
	Width  int
	Height int
}

// uiPoint espelha application.Point.
type uiPoint struct {
	X int
	Y int
}

// uiScreen espelha application.Screen (somente os campos lidos pelo pacote).
// A conversão wails→local é feita pelo adapter; a conversão local→wails
// (criação de janela posicionada) reconstrói a Screen a partir destes campos.
type uiScreen struct {
	ID             string
	Name           string
	ScaleFactor    float32
	Bounds         uiRect
	PhysicalBounds uiRect
	WorkArea       uiRect
	IsPrimary      bool
}

// uiWindowEventType espelha events.WindowEventType (uint). Os valores das
// constantes abaixo PRECISAM permanecer idênticos aos de
// github.com/wailsapp/wails/v3/pkg/events (newCommonEvents): o adapter
// converte numericamente.
type uiWindowEventType uint

const (
	uiEventWindowClosing uiWindowEventType = 1030 // events.Common.WindowClosing
	uiEventWindowHide    uiWindowEventType = 1037 // events.Common.WindowHide
)

// uiWindowEvent espelha *application.WindowEvent no subconjunto usado
// (Cancel, para interceptar o fechamento da janela do chat).
type uiWindowEvent interface {
	Cancel()
}

// uiButtonState espelha application.ButtonState (valores idênticos).
type uiButtonState int

const (
	uiButtonEnabled  uiButtonState = 0
	uiButtonDisabled uiButtonState = 1
	uiButtonHidden   uiButtonState = 2
)

// uiWindowStartPosition espelha application.WindowStartPosition (valores idênticos).
type uiWindowStartPosition int

const (
	uiWindowCentered uiWindowStartPosition = 0
	uiWindowXY       uiWindowStartPosition = 1
)

// uiWebviewWindowOptions espelha application.WebviewWindowOptions (somente os
// campos usados pelo pacote). O adapter converte para o struct do Wails.
type uiWebviewWindowOptions struct {
	Name                  string
	Title                 string
	Width                 int
	Height                int
	AlwaysOnTop           bool
	URL                   string
	DisableResize         bool
	Frameless             bool
	Hidden                bool
	MinWidth              int
	MinHeight             int
	MaxWidth              int
	MaxHeight             int
	InitialPosition       uiWindowStartPosition
	X                     int
	Y                     int
	Screen                *uiScreen
	MinimiseButtonState   uiButtonState
	MaximiseButtonState   uiButtonState
	CloseButtonState      uiButtonState
	FullscreenButtonState uiButtonState
}

// ── Janela ──────────────────────────────────────────────────────────────────

// uiWindow é o contrato local da janela: o subconjunto da API de
// application.Window efetivamente usada pelo pacote (em todos os builds).
// Implementações concretas do Wails são adaptadas em app_ui_wails.go.
type uiWindow interface {
	Close()
	ExecJS(js string)
	Focus()
	GetScreen() (*uiScreen, error)
	Hide()
	IsFocused() bool
	IsMinimised() bool
	IsVisible() bool
	Minimise()
	NativeWindow() uintptr
	Position() (x int, y int)
	Reload()
	RegisterHook(eventType uiWindowEventType, callback func(event uiWindowEvent)) func()
	SetAlwaysOnTop(b bool)
	SetFrameless(frameless bool)
	SetPosition(x, y int)
	SetSize(width, height int)
	SetURL(s string)
	Show()
	Size() (width, height int)
	UnMinimise()
}

// UIWindow é o alias exportado de uiWindow, para uso pelo package main
// (binário da UI, construído com a tag "wailsui").
type UIWindow = uiWindow

// ── Aplicação ───────────────────────────────────────────────────────────────

// uiContext é o contexto opaco passado aos callbacks de menu (espelha
// *application.Context, cujos campos não são usados pelo pacote).
type uiContext struct{}

// uiMenuItem espelha os métodos de *application.MenuItem usados pelo pacote.
type uiMenuItem interface {
	SetEnabled(enabled bool)
	SetLabel(s string)
	SetChecked(checked bool)
	OnClick(fn func(ctx *uiContext)) uiMenuItem
}

// uiMenu espelha os métodos de *application.Menu usados pelo pacote.
type uiMenu interface {
	Add(label string) uiMenuItem
	AddSubmenu(label string) uiMenu
	AddCheckbox(label string, checked bool) uiMenuItem
	AddSeparator()
}

// uiMenuManager espelha application.App.Menu (MenuManager).
type uiMenuManager interface {
	New() uiMenu
}

// uiSystemTray espelha os métodos de *application.SystemTray usados pelo pacote.
type uiSystemTray interface {
	SetIcon(icon []byte)
	SetTooltip(tooltip string)
	OnClick(fn func())
	OnDoubleClick(fn func())
	SetMenu(menu uiMenu)
}

// uiSystemTrayManager espelha application.App.SystemTray (SystemTrayManager).
type uiSystemTrayManager interface {
	New() uiSystemTray
}

// uiFileFilter espelha application.FileFilter.
type uiFileFilter struct {
	DisplayName string
	Pattern     string
}

// uiSaveFileDialogOptions espelha application.SaveFileDialogOptions (campos usados).
type uiSaveFileDialogOptions struct {
	CanCreateDirectories            bool
	ShowHiddenFiles                 bool
	CanSelectHiddenExtension        bool
	AllowOtherFileTypes             bool
	HideExtension                   bool
	TreatsFilePackagesAsDirectories bool
	Title                           string
	Message                         string
	Directory                       string
	Filename                        string
	ButtonText                      string
	Filters                         []uiFileFilter
}

// uiOpenFileDialog espelha *application.OpenFileDialogStruct (métodos usados).
type uiOpenFileDialog interface {
	PromptForSingleSelection() (string, error)
}

// uiSaveFileDialog espelha *application.SaveFileDialogStruct (métodos usados).
type uiSaveFileDialog interface {
	SetOptions(options *uiSaveFileDialogOptions)
	PromptForSingleSelection() (string, error)
}

// uiDialogManager espelha application.App.Dialog (DialogManager).
type uiDialogManager interface {
	OpenFile() uiOpenFileDialog
	SaveFile() uiSaveFileDialog
}

// uiApp é o contrato local da aplicação Wails v3 (*application.App): o
// subconjunto usado pelo pacote. Campos do Wails (Event, Dialog, Menu,
// SystemTray, Screen) viram métodos com retornos convertidos para os contratos
// locais — o adapter faz a conversão concreto→local.
type uiApp interface {
	// AppName espelha Config().Name.
	AppName() string
	// EmitEvent espelha Event.Emit.
	EmitEvent(name string, data ...any)
	// Quit espelha Quit.
	Quit()
	// Dialog espelha o campo Dialog.
	Dialog() uiDialogManager
	// Menu espelha o campo Menu.
	Menu() uiMenuManager
	// SystemTray espelha o campo SystemTray.
	SystemTray() uiSystemTrayManager
	// Screens espelha Screen.GetAll().
	Screens() []*uiScreen
	// NewWebviewWindow espelha Window.NewWithOptions.
	NewWebviewWindow(options uiWebviewWindowOptions) uiWindow
}

// ── Notificações nativas ────────────────────────────────────────────────────

// uiNotificationSound espelha wailsnotifications.NotificationSound.
type uiNotificationSound struct {
	Silent bool
	Name   string
}

// uiNotificationAttachment espelha wailsnotifications.NotificationAttachment.
type uiNotificationAttachment struct {
	ID   string
	Path string
	Type string
}

// uiNotificationSchedule espelha wailsnotifications.NotificationSchedule.
type uiNotificationSchedule struct {
	DelaySeconds int
	At           int64
}

// uiNotificationOptions espelha wailsnotifications.NotificationOptions.
type uiNotificationOptions struct {
	ID                string
	Title             string
	Subtitle          string
	Body              string
	CategoryID        string
	Data              map[string]interface{}
	Sound             *uiNotificationSound
	Attachments       []uiNotificationAttachment
	ThreadID          string
	InterruptionLevel string
	Schedule          *uiNotificationSchedule
}

// uiNotificationResponse espelha wailsnotifications.NotificationResponse.
type uiNotificationResponse struct {
	ID               string
	ActionIdentifier string
	CategoryID       string
	Title            string
	Subtitle         string
	Body             string
	UserText         string
	UserInfo         map[string]interface{}
}

// uiNotificationResult espelha wailsnotifications.NotificationResult: é o tipo
// consumido pelos handlers registrados via RegisterNativeNotificationHandler.
type uiNotificationResult struct {
	Response uiNotificationResponse
	Error    error
}

// uiNotifications é o contrato local do serviço nativo de notificações do
// Wails v3 (*wailsnotifications.NotificationService). O ServiceStartup local
// não recebe opções: o adapter repassa application.ServiceOptions{} (o mesmo
// DefaultServiceOptions que o Wails passaria).
type uiNotifications interface {
	SendNotification(options uiNotificationOptions) error
	OnNotificationResponse(callback func(result uiNotificationResult))
	ServiceStartup(ctx context.Context) error
	ServiceShutdown() error
}
