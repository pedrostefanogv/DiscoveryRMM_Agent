package main

import _ "embed"

// trayIconICO holds the embedded system tray icon for the normal state.
// Keep this file in the root package: go:embed cannot use parent paths (..).
// Usa PNG 32x32 (formato recomendado pelo Wails v3 para o systray no Windows).
//
//go:embed build/windows/tray_normal.png
var trayIconICO []byte

//go:embed build/windows/tray_provisioning.png
var trayProvisioningICO []byte

// appIconICO é o ícone usado como FALLBACK do ícone de janela no Wails v3 no
// Windows: o framework procura o recurso ID 3 do próprio executável e, se não
// existir (é o caso dos builds do instalador, que gravam o ícone com NOME em vez
// de ordinal), cai para Options.Icon. Sem esse fallback a janela do chat de
// suporte — a única com moldura — exibia o ícone padrão do Windows no título.
//
//go:embed build/windows/icon.ico
var appIconICO []byte

//go:embed build/windows/tray_offline.png
var trayOfflineICO []byte
