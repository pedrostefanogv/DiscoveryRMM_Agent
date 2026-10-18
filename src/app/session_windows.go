//go:build windows

package app

import "golang.org/x/sys/windows"

// currentProcessSessionID devolve a sessão do Windows do processo atual.
// Usado para isolar o perfil do WebView2 por sessão (duas UIs em sessões
// diferentes compartilhariam o mesmo perfil Chromium e a segunda falharia).
// 0 quando não há sessão interativa (serviço), em falha, ou quando o Windows
// devolve 0xFFFFFFFF ("sem sessão" do WTS) — nunca propagar esse valor como
// se fosse um ID de sessão válido (viraria "session-4294967295").
func currentProcessSessionID() uint32 {
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(windows.GetCurrentProcessId()), &session); err != nil {
		return 0
	}
	if session == 0xFFFFFFFF {
		return 0
	}
	return session
}
