package app

// ── Hooks de UI chamados por código sem tag ─────────────────────────────────
//
// Pontos de entrada da UI que dependem do runtime do Wails mas são chamados de
// código compilado SEM a build tag "wailsui" (inclusive pelo serviço). O
// default é um no-op seguro; o app_ui_wails.go (tagged) sobrescreve no init()
// com a implementação real que delega ao Wails.

// uiInvokeSync executa fn na main thread do Wails (application.InvokeSync).
// Default (sem tag / serviço): executa inline — não existe main thread de UI.
var uiInvokeSync = func(fn func()) {
	if fn == nil {
		return
	}
	fn()
}
