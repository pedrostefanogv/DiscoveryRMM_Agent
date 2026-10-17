//go:build windows

package app

import "golang.org/x/sys/windows"

// currentProcessSessionID devolve a sessão do Windows do processo atual.
// Usado para isolar o perfil do WebView2 por sessão (duas UIs em sessões
// diferentes compartilhariam o mesmo perfil Chromium e a segunda falharia).
// 0 quando não há sessão interactiva (serviço) ou em falha.
func currentProcessSessionID() uint32 {
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(windows.GetCurrentProcessId()), &session); err != nil {
		return 0
	}
	return session
}
