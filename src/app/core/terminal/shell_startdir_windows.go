//go:build windows

package terminal

// Diretório inicial do shell do terminal (pedido 2026-10-27):
//   - runAs=user → perfil do usuário (C:\Users\<user>), NUNCA system32;
//   - runAs=system/inherit com processo SYSTEM → mantém o cwd herdado
//     (C:\WINDOWS\system32), comportamento esperado p/ SYSTEM.
//
// POR QUÊ o cwd herdado é system32: o worker é spawnado pelo serviço sem
// diretório corrente — o Windows posiciona processos de serviço em system32.
// Sem isso, runAs=user abria "PS C:\WINDOWS\system32>" mesmo rodando como
// pedro.

import (
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

var cbProcGetUserProfileDirectory = windows.NewLazySystemDLL("userenv.dll").NewProc("GetUserProfileDirectoryW")

// userProfileDir devolve o diretório de perfil do TOKEN dado (0 = token do
// próprio processo). "" em falha (chamador mantém o cwd herdado).
func userProfileDir(tok windows.Token) string {
	t := tok
	closeTok := false
	if t == 0 {
		var ct windows.Token
		if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &ct); err != nil {
			log.Printf("[terminal] perfil do usuário: OpenProcessToken falhou: %v", err)
			return ""
		}
		t = ct
		closeTok = true
	}
	if closeTok {
		defer func() { _ = t.Close() }()
	}
	var n uint32
	if r1, _, _ := cbProcGetUserProfileDirectory.Call(uintptr(t), 0, uintptr(unsafe.Pointer(&n))); r1 == 0 || n == 0 {
		log.Printf("[terminal] perfil do usuário: GetUserProfileDirectory (tamanho) falhou")
		return ""
	}
	buf := make([]uint16, n)
	if r1, _, _ := cbProcGetUserProfileDirectory.Call(uintptr(t), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r1 == 0 {
		log.Printf("[terminal] perfil do usuário: GetUserProfileDirectory falhou")
		return ""
	}
	return windows.UTF16ToString(buf)
}

// resolveShellStartDir decide o diretório inicial do shell:
//   - token de usuário (runAs=user resolvido) → perfil do usuário;
//   - sem token MAS processo já é o usuário (remote-session-worker) → perfil
//     do usuário via token do próprio processo;
//   - processo SYSTEM (runAs=system/inherit no serviço) → "" (mantém system32).
func resolveShellStartDir(tok windows.Token) string {
	if tok != 0 {
		return userProfileDir(tok)
	}
	if isProcessSystem() {
		return ""
	}
	return userProfileDir(0)
}
