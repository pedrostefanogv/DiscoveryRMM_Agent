package main

import _ "embed"

// trayIconICO holds the embedded system tray icon.
// The //go:embed directive must remain in the root package because
// Go does not allow embedding files from parent directories (no ".." in paths).
// The bytes are passed to app.AppStartupOptions.TrayIcon at startup.
//
//go:embed build/windows/icon.ico
var trayIconICO []byte
