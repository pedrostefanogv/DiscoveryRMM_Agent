package main

import _ "embed"

// trayIconICO holds the embedded system tray icon for the normal state.
// Keep this file in the root package: go:embed cannot use parent paths (..).
// Usa PNG 32x32 (formato recomendado pelo Wails v3 para o systray no Windows).
//go:embed build/windows/tray_normal.png
var trayIconICO []byte

//go:embed build/windows/tray_provisioning.png
var trayProvisioningICO []byte

//go:embed build/windows/tray_offline.png
var trayOfflineICO []byte
