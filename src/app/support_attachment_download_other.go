//go:build !windows

package app

// activeUserDownloadsDir não se aplica fora do Windows: o processo não roda sob
// a separação serviço/usuário que cria o problema do perfil do sistema.
func activeUserDownloadsDir() string { return "" }
