package main

import _ "embed"

// trayIconICO holds the embedded system tray icon for the normal state.
// Keep this file in the root package: go:embed cannot use parent paths (..).
//go:embed build/windows/icon.ico
var trayIconICO []byte

//go:embed build/windows/provisionig.ico
var trayProvisioningICO []byte

//go:embed build/windows/offline.ico
var trayOfflineICO []byte
