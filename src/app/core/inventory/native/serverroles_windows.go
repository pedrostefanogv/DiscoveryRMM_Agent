//go:build windows

package native

import (
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// ─────────────────────────────────────────────────────────────────────────────
// Detecção de roles de Windows Server pelos serviços que elas instalam.
//
// Cada role do Windows instala um serviço com nome estável. Consultar o SCM é
// a forma mais barata e mais confiável de saber se a role está presente: não
// exige elevação (SERVICE_QUERY_STATUS é concedido a usuários comuns), não
// depende de módulo PowerShell instalado e não sofre com a lentidão de
// Get-WindowsFeature/DISM.
//
// Usado pelos coletores condicionais F11 (Hyper-V), F12 (Failover Clustering)
// e F14 (RDS Session Host): cada um só roda quando a role existe.
// ─────────────────────────────────────────────────────────────────────────────

// Nomes de serviço que identificam cada role. São os nomes internos
// (service name), não os nomes de exibição localizados.
const (
	// serviceHyperV é o Virtual Machine Management (role Hyper-V).
	serviceHyperV = "vmms"
	// serviceFailoverCluster é o Cluster Service (role Failover Clustering).
	serviceFailoverCluster = "ClusSvc"
	// serviceRDSConnectionBroker identifica um host com Remote Desktop.
	serviceRDSConnectionBroker = "Tssdis"
	// serviceTerminalServices é o Remote Desktop Services (Session Host).
	serviceTerminalServices = "TermService"
	// serviceStorageSpaces é o Storage Service (Storage Spaces).
	serviceStorageSpaces = "StorageSvc"
)

// serviceExists informa se um serviço está instalado na máquina.
//
// Best-effort: qualquer falha (SCM indisponível, permissão) devolve false.
// Um falso negativo apenas faz o coletor condicional não rodar — nunca
// derruba o inventário, e é a postura segura (não executamos nada a mais).
func serviceExists(name string) bool {
	if name == "" {
		return false
	}
	m, err := mgr.Connect()
	if err != nil {
		// Fallback: alguns contextos permitem abrir o SCM diretamente mas não
		// pela biblioteca de conveniência. Segunda tentativa explícita.
		return serviceExistsViaSCM(name)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return false
	}
	defer s.Close()
	return true
}

// serviceExistsViaSCM é o caminho alternativo direto pelo SCM, usado quando
// mgr.Connect falha. Só faz a abertura do serviço; nenhuma alteração.
func serviceExistsViaSCM(name string) bool {
	mgrHandle, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(mgrHandle)

	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false
	}
	// SERVICE_QUERY_STATUS é o mínimo necessário e o mais amplamente concedido.
	h, err := windows.OpenService(mgrHandle, namePtr, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(h)
	return true
}
