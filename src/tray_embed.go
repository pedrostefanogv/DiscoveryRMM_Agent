package main

import _ "embed"

// trayIconICO holds the embedded system tray icon.
// Keep this file in the root package: go:embed cannot use parent paths (..).
//go:embed build/windows/icon.ico
var trayIconICO []byte
