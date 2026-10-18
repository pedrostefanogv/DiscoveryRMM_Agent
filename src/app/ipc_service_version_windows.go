//go:build windows

package app

import (
	"log"
	"strings"

	"discovery/app/core/buildinfo"
)

// recordServiceHandshake guarda a versão/protocolo reportados pelo serviço no
// hello_ack e avisa (log) quando o par UI × serviço está em versões diferentes.
// A UI consulta via binding GetIPCServiceInfo para mostrar o aviso ao usuário
// (A.2.2 do plano: "version no handshake; UI avisa/bloqueia incompatibilidade").
func (c *IPCClient) recordServiceHandshake(ack IPCServiceHelloAck) {
	if c == nil {
		return
	}
	version := strings.TrimSpace(ack.Version)
	protocol := ack.Protocol
	if protocol <= 0 {
		protocol = 1
	}

	c.serviceVersionMu.Lock()
	previous := c.serviceVersion
	c.serviceVersion = version
	c.serviceProtocol = protocol
	c.serviceVersionMu.Unlock()

	uiVersion := strings.TrimSpace(buildinfo.Version)
	if version != "" && uiVersion != "" && version != uiVersion && version != previous {
		log.Printf("[ipc] AVISO: UI %s conectada ao serviço %s (protocolo %d) — versões diferentes; "+
			"atualize o par (instalador troca os dois binários)", uiVersion, version, protocol)
	}
}

// ServiceHandshake devolve a última versão/protocolo reportados pelo serviço.
// ok=false quando ainda não houve handshake (ou em serviço antigo sem version).
func (c *IPCClient) ServiceHandshake() (version string, protocol int, ok bool) {
	if c == nil {
		return "", 0, false
	}
	c.serviceVersionMu.Lock()
	defer c.serviceVersionMu.Unlock()
	return c.serviceVersion, c.serviceProtocol, c.serviceVersion != ""
}
