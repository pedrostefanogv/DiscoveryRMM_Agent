//go:build windows

package mcp

import (
	"fmt"
	"syscall"
)

// A limpeza do cache DNS via `ipconfig /flushdns` exige ELEVAÇÃO na maioria
// dos Windows modernos: rodando no contexto do usuário o comando falha com
// "A operação solicitada requer elevação" (exit 1) e o cache nunca era limpo.
//
// `dnsapi!DnsFlushResolverCache` faz a MESMA operação sem UAC — é a API que o
// próprio ipconfig usa por baixo. Chamá-la direto também evita depender do
// PATH (`ipconfig` some em ambientes de serviço com PATH mínimo).
var (
	dnsapiDLL                 = syscall.NewLazyDLL("dnsapi.dll")
	procDnsFlushResolverCache = dnsapiDLL.NewProc("DnsFlushResolverCache")
)

// flushDNSNative limpa o cache do resolvedor DNS pela API do Windows.
// Retorna nil quando a chamada teve sucesso (BOOL diferente de zero).
//
// syscall.LazyProc.Call PANICa quando o símbolo não existe na DLL, então a
// resolução é feita antes com Find() e ainda há um recover: uma falha aqui não
// pode derrubar o agent — ela deve apenas cair no fallback por comando.
func flushDNSNative() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("DnsFlushResolverCache indisponivel: %v", r)
		}
	}()
	if loadErr := dnsapiDLL.Load(); loadErr != nil {
		return fmt.Errorf("dnsapi.dll indisponivel: %w", loadErr)
	}
	if findErr := procDnsFlushResolverCache.Find(); findErr != nil {
		return fmt.Errorf("DnsFlushResolverCache nao encontrado em dnsapi.dll: %w", findErr)
	}
	ret, _, callErr := procDnsFlushResolverCache.Call()
	if ret == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return fmt.Errorf("DnsFlushResolverCache falhou: %w", callErr)
		}
		return fmt.Errorf("DnsFlushResolverCache retornou falha")
	}
	return nil
}
