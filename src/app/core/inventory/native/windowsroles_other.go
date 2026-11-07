//go:build !windows

package native

import "context"

// Os tipos abaixo existem fora do Windows para que o pacote de inventário
// compile em qualquer plataforma e os testes de contrato do payload não
// dependam de um build Windows. O agente é Windows-only hoje, mas manter o
// tipo disponível evita um //go:build no consumidor só por causa disso.

// WindowsRolesResult é o resultado da coleta de roles/features.
type WindowsRolesResult struct {
	Roles  []WindowsRole `json:"roles"`
	Source string        `json:"source,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// WindowsRole é uma role/feature do Windows Server.
type WindowsRole struct {
	Name         string `json:"name"`
	DisplayName  string `json:"displayName,omitempty"`
	Installed    bool   `json:"installed"`
	InstallState string `json:"installState,omitempty"`
}

// CollectWindowsRoles não tem implementação fora do Windows: roles de
// servidor não existem em outras plataformas.
func CollectWindowsRoles(context.Context) WindowsRolesResult { return WindowsRolesResult{} }
