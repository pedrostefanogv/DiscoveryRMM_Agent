//go:build !windows

package mcp

import "errors"

// errFlushDNSNativeUnsupported: fora do Windows a limpeza usa os comandos do
// sistema (resolvectl / systemd-resolve no Linux, killall no macOS).
var errFlushDNSNativeUnsupported = errors.New("flush DNS nativo indisponivel nesta plataforma")

func flushDNSNative() error {
	return errFlushDNSNativeUnsupported
}
